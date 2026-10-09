//! WhatBreaks Discovery Engine v1
//!
//! The Discovery Engine applies deterministic rules to a set of [`Evidence`]
//! observations and produces [`DiscoveredRelationship`] results — relationships
//! that are derivable from what was actually observed.
//!
//! # Design Principles
//!
//! - **Deterministic**: No probabilities, confidence scores, fuzzy matching,
//!   LLMs, or heuristics. A relationship is derived only when a rule's
//!   conditions are fully and exactly satisfied.
//!
//! - **Evidence-backed**: Every derived relationship carries the [`EvidenceId`]
//!   references that caused it. Nothing is asserted without an observation trail.
//!
//! - **Conservative**: Insufficient or ambiguous evidence does NOT produce a
//!   relationship. The absence of evidence for a relationship is not treated as
//!   proof that the relationship does not exist.
//!
//! - **Non-mutating**: Discovery never modifies the original [`Evidence`] or
//!   [`Relationship`] models.
//!
//! - **Extensible**: New [`DiscoveryRule`] implementations can be added without
//!   changing the engine or the existing domain models.
//!
//! # Architecture
//!
//! ```text
//! Evidence[]
//!     ↓
//! DiscoveryEngine (applies DiscoveryRule implementations)
//!     ↓
//! DiscoveryResult[] (Discovered | Insufficient | Conflict | Invalid)
//! ```
//!
//! # Scope of v1
//!
//! v1 implements one concrete rule:
//!
//! [`RuntimeConnectionRule`]: If a `RUNTIME_CONNECTION` observation (with
//! `destination` + `port`) is matched by a `RESOURCE_REFERENCE` observation
//! (with `address` + `port`) that maps the same endpoint to a known resource,
//! derive:
//! ```text
//! source --DEPENDS_ON--> target
//! ```
//!
//! [`Evidence`]: crate::evidence::Evidence
//! [`EvidenceId`]: crate::evidence::EvidenceId
//! [`Relationship`]: crate::relationship::Relationship

use std::collections::{HashMap, HashSet};

use serde::{Deserialize, Serialize};

use crate::evidence::{Evidence, EvidenceId, ObservationType};
use crate::relationship::{Relationship, RelationshipKind};
use crate::resource::{Provider, ResourceIdentity, ResourceKind};

// ---------------------------------------------------------------------------
// DiscoveredRelationship
// ---------------------------------------------------------------------------

/// A relationship derived deterministically from evidence observations.
///
/// Preserves the derived [`Relationship`] and the [`EvidenceId`]s of all
/// observations that caused it to be derived. This enables WB to answer:
/// *"Why does this relationship exist?"*
///
/// # What it does NOT contain
///
/// - Confidence scores
/// - Graph node IDs
/// - The full [`Evidence`] records (only IDs are stored — no unnecessary copies)
/// - Discovery state or inference metadata
///
/// [`Evidence`]: crate::evidence::Evidence
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct DiscoveredRelationship {
    /// The derived directional relationship.
    pub relationship: Relationship,
    /// The IDs of all evidence observations that support this relationship.
    /// At least one ID is always present.
    pub supporting_evidence: Vec<EvidenceId>,
}

impl DiscoveredRelationship {
    /// Construct a `DiscoveredRelationship`.
    pub fn new(relationship: Relationship, supporting_evidence: Vec<EvidenceId>) -> Self {
        Self {
            relationship,
            supporting_evidence,
        }
    }
}

// ---------------------------------------------------------------------------
// DiscoveryResult
// ---------------------------------------------------------------------------

/// The outcome of applying a discovery rule to a set of evidence.
///
/// Distinguishes four cases that must not be conflated:
///
/// - [`DiscoveryResult::Discovered`]: A relationship was derived.
/// - [`DiscoveryResult::Insufficient`]: Not enough evidence to derive anything.
/// - [`DiscoveryResult::Conflict`]: Evidence exists but is contradictory; the
///   rule cannot deterministically resolve it.
/// - [`DiscoveryResult::Invalid`]: Evidence was structurally malformed and
///   could not be processed. A description is provided for diagnostics.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "outcome", rename_all = "snake_case")]
pub enum DiscoveryResult {
    /// A relationship was deterministically derived from the evidence.
    Discovered(DiscoveredRelationship),

    /// Evidence exists but is insufficient to derive a relationship.
    ///
    /// Example: a `RUNTIME_CONNECTION` observation exists for an endpoint, but
    /// no `RESOURCE_REFERENCE` observation maps that endpoint to a resource.
    Insufficient,

    /// Evidence is contradictory and cannot be deterministically resolved.
    ///
    /// Example: two `RESOURCE_REFERENCE` observations map the same endpoint to
    /// two different resources. Discovery cannot pick one without confidence
    /// scoring, which is explicitly excluded from v1.
    Conflict {
        /// A human-readable description of the conflict.
        description: String,
    },

    /// Evidence was structurally malformed (missing required fields, wrong
    /// types, etc.). The rule returned this rather than panicking.
    Invalid {
        /// A human-readable description of what was malformed.
        description: String,
    },
}

impl DiscoveryResult {
    /// Return the [`DiscoveredRelationship`] if this result is `Discovered`.
    pub fn discovered(&self) -> Option<&DiscoveredRelationship> {
        match self {
            Self::Discovered(r) => Some(r),
            _ => None,
        }
    }

    /// Return `true` if this result represents a successful discovery.
    pub fn is_discovered(&self) -> bool {
        matches!(self, Self::Discovered(_))
    }

    /// Return `true` if evidence was insufficient.
    pub fn is_insufficient(&self) -> bool {
        matches!(self, Self::Insufficient)
    }

    /// Return `true` if evidence was contradictory.
    pub fn is_conflict(&self) -> bool {
        matches!(self, Self::Conflict { .. })
    }

    /// Return `true` if evidence was malformed.
    pub fn is_invalid(&self) -> bool {
        matches!(self, Self::Invalid { .. })
    }
}

// ---------------------------------------------------------------------------
// DiscoveryRule (trait)
// ---------------------------------------------------------------------------

/// A deterministic rule that can derive relationships from evidence observations.
///
/// Each implementation encodes one specific discovery strategy. New rules can
/// be added without changing the [`DiscoveryEngine`] or the existing domain
/// models.
///
/// # Contract
///
/// - Rules must NEVER panic on malformed evidence. Return `Invalid` instead.
/// - Rules must NEVER return `Discovered` without complete, valid evidence.
/// - Rules must preserve relationship directionality as specified by the rule.
/// - Rules must store `EvidenceId` references, not copies of evidence payloads.
pub trait DiscoveryRule: Send + Sync {
    /// A short, unique name for this rule (for diagnostics and logging).
    fn name(&self) -> &str;

    /// Apply this rule to the given slice of evidence observations.
    ///
    /// Returns a list of [`DiscoveryResult`]s — one per candidate relationship
    /// discovered (or one per conflict/invalid case). An empty `Vec` means the
    /// evidence is simply not relevant to this rule.
    fn apply(&self, evidence: &[Evidence]) -> Vec<DiscoveryResult>;
}

// ---------------------------------------------------------------------------
// Endpoint (internal helper)
// ---------------------------------------------------------------------------

/// An address+port pair used as a matching key within rules.
///
/// This is an internal type — it never appears in public outputs.
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
struct Endpoint {
    address: String,
    port: u16,
}

impl Endpoint {
    /// Extract an `Endpoint` from a `RUNTIME_CONNECTION` evidence payload.
    ///
    /// Required fields:
    /// - `data.destination`: string IP/hostname
    /// - `data.port`: u16 integer
    fn from_connection(ev: &Evidence) -> Option<Self> {
        let destination = ev.data.get("destination")?.as_str()?.to_owned();
        let port = u16::try_from(ev.data.get("port")?.as_u64()?).ok()?;
        Some(Self {
            address: destination,
            port,
        })
    }

    /// Extract an `Endpoint` from a `RESOURCE_REFERENCE` evidence payload
    /// that encodes an address mapping.
    ///
    /// Required fields:
    /// - `data.address`: string IP/hostname
    /// - `data.port`: u16 integer
    fn from_mapping(ev: &Evidence) -> Option<Self> {
        let address = ev.data.get("address")?.as_str()?.to_owned();
        let port = u16::try_from(ev.data.get("port")?.as_u64()?).ok()?;
        Some(Self { address, port })
    }
}

// ---------------------------------------------------------------------------
// RuntimeConnectionRule
// ---------------------------------------------------------------------------

/// Derives `source --DEPENDS_ON--> target` from two evidence observations:
///
/// 1. A [`ObservationType::RUNTIME_CONNECTION`] observation on `source` that
///    records a `destination` address and `port` in its `data`.
///
/// 2. A [`ObservationType::RESOURCE_REFERENCE`] observation on `target` that
///    records an `address` and `port` in its `data`, mapping the same endpoint
///    to the target resource's identity.
///
/// # Matching
///
/// Both `destination`/`address` (string equality) AND `port` (exact integer
/// match) must agree. If they agree, the rule derives the relationship. If
/// the same endpoint maps to two different resources, the result is `Conflict`.
///
/// # Validation
///
/// Evidence with missing or malformed `destination`/`address`/`port` fields
/// is silently excluded from matching (it is not an `Invalid` result — the
/// evidence may simply be intended for a different rule). `Invalid` is only
/// returned when evidence passes the `observation_type` filter but has an
/// unrecoverable structural error that makes it impossible to determine whether
/// a relationship exists.
pub struct RuntimeConnectionRule;

impl DiscoveryRule for RuntimeConnectionRule {
    fn name(&self) -> &str {
        "RuntimeConnectionRule"
    }

    fn apply(&self, evidence: &[Evidence]) -> Vec<DiscoveryResult> {
        // Partition evidence by observation type.
        let connections: Vec<&Evidence> = evidence
            .iter()
            .filter(|e| e.observation_type == ObservationType::RUNTIME_CONNECTION)
            .collect();

        let mappings: Vec<&Evidence> = evidence
            .iter()
            .filter(|e| e.observation_type == ObservationType::RESOURCE_REFERENCE)
            .collect();

        // No connection evidence → nothing for this rule to do.
        if connections.is_empty() {
            return vec![];
        }

        // Build a mapping: Endpoint → Vec<(ResourceIdentity, EvidenceId)>
        // Multiple entries for the same endpoint indicate a conflict.
        let mut endpoint_to_targets: HashMap<Endpoint, Vec<(ResourceIdentity, EvidenceId)>> =
            HashMap::new();

        for mapping_ev in &mappings {
            if let Some(ep) = Endpoint::from_mapping(mapping_ev) {
                endpoint_to_targets
                    .entry(ep)
                    .or_default()
                    .push((mapping_ev.subject.clone(), mapping_ev.id));
            }
        }

        let mut results: Vec<DiscoveryResult> = Vec::new();

        for conn_ev in &connections {
            let Some(endpoint) = Endpoint::from_connection(conn_ev) else {
                // RUNTIME_CONNECTION evidence with missing/malformed required
                // fields cannot participate in this rule.
                results.push(DiscoveryResult::Invalid {
                    description: format!(
                        "RUNTIME_CONNECTION evidence {} is missing required 'destination' or 'port' fields",
                        conn_ev.id
                    ),
                });
                continue;
            };

            match endpoint_to_targets.get(&endpoint) {
                None => {
                    // Valid connection evidence, but no mapping found → insufficient.
                    results.push(DiscoveryResult::Insufficient);
                }

                Some(targets) if targets.len() > 1 => {
                    // Multiple resources claim the same endpoint → conflict.
                    let resource_strs: Vec<String> =
                        targets.iter().map(|(ri, _)| ri.to_string()).collect();
                    results.push(DiscoveryResult::Conflict {
                        description: format!(
                            "Endpoint {}:{} maps to multiple resources: {}",
                            endpoint.address,
                            endpoint.port,
                            resource_strs.join(", ")
                        ),
                    });
                }

                Some(targets) => {
                    // Exactly one target → derive the relationship.
                    let (target_identity, mapping_evidence_id) = &targets[0];

                    let relationship = Relationship::new(
                        conn_ev.subject.clone(),
                        target_identity.clone(),
                        RelationshipKind::DEPENDS_ON,
                    );

                    results.push(DiscoveryResult::Discovered(DiscoveredRelationship::new(
                        relationship,
                        vec![conn_ev.id, *mapping_evidence_id],
                    )));
                }
            }
        }

        results
    }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/// Helper to extract and validate a [`ResourceIdentity`] from a JSON object.
fn parse_resource_identity(val: &serde_json::Value) -> Result<ResourceIdentity, String> {
    let obj = val
        .as_object()
        .ok_or_else(|| "expected JSON object for resource identity".to_string())?;

    let provider_str = obj
        .get("provider")
        .and_then(|v| v.as_str())
        .ok_or_else(|| "missing or non-string 'provider'".to_string())?;
    if provider_str.trim().is_empty() {
        return Err("empty 'provider'".to_string());
    }

    let type_str = obj
        .get("resource_type")
        .and_then(|v| v.as_str())
        .ok_or_else(|| "missing or non-string 'resource_type'".to_string())?;
    if type_str.trim().is_empty() {
        return Err("empty 'resource_type'".to_string());
    }

    let id_str = obj
        .get("provider_id")
        .and_then(|v| v.as_str())
        .ok_or_else(|| "missing or non-string 'provider_id'".to_string())?;
    if id_str.trim().is_empty() {
        return Err("empty 'provider_id'".to_string());
    }

    Ok(ResourceIdentity::new(
        Provider::new(provider_str),
        ResourceKind::new(type_str),
        id_str,
    ))
}

// ---------------------------------------------------------------------------
// OwnershipRule
// ---------------------------------------------------------------------------

/// Derives `owner --OWNS--> subject` (and transitive owner chains) from
/// [`ObservationType::OWNERSHIP_REFERENCE`] observations.
///
/// # Semantics
///
/// 1. An observation with `observation_type == OWNERSHIP_REFERENCE` expresses an ownership
///    claim where `ev.subject` is the owned child and `ev.data.owner` is the parent owner.
/// 2. If `ev.data` is missing an "owner" field or owner is null, the outcome is `Insufficient`.
/// 3. If `ev.data.owner` is malformed (not a valid object or missing required identity fields),
///    the outcome is `Invalid`.
/// 4. If a resource has multiple conflicting controller owners (more than one distinct owner
///    claiming `controller: true`), the outcome is `Conflict`.
/// 5. Valid observations derive a direct [`RelationshipKind::OWNS`] relationship:
///    `owner --OWNS--> subject`, backed by the observation's [`EvidenceId`].
/// 6. When an owner is itself owned by an ancestor controller (e.g. Deployment -> ReplicaSet -> Pod),
///    the rule transitively derives the root-level ownership `ancestor --OWNS--> subject`,
///    backed by all supporting evidence IDs along the controller chain.
pub struct OwnershipRule;

impl DiscoveryRule for OwnershipRule {
    fn name(&self) -> &str {
        "OwnershipRule"
    }

    fn apply(&self, evidence: &[Evidence]) -> Vec<DiscoveryResult> {
        let ownership_evidences: Vec<&Evidence> = evidence
            .iter()
            .filter(|e| e.observation_type == ObservationType::OWNERSHIP_REFERENCE)
            .collect();

        if ownership_evidences.is_empty() {
            return vec![];
        }

        let mut results: Vec<DiscoveryResult> = Vec::new();

        // child -> Vec<(owner, evidence_id, is_controller)>
        let mut child_to_owners: HashMap<
            ResourceIdentity,
            Vec<(ResourceIdentity, EvidenceId, bool)>,
        > = HashMap::new();

        for ev in &ownership_evidences {
            let Some(owner_val) = ev.data.get("owner") else {
                results.push(DiscoveryResult::Insufficient);
                continue;
            };

            if owner_val.is_null() {
                results.push(DiscoveryResult::Insufficient);
                continue;
            }

            let owner_identity = match parse_resource_identity(owner_val) {
                Ok(id) => id,
                Err(err) => {
                    results.push(DiscoveryResult::Invalid {
                        description: format!(
                            "OWNERSHIP_REFERENCE evidence {} has malformed 'owner': {}",
                            ev.id, err
                        ),
                    });
                    continue;
                }
            };

            let is_controller = ev
                .data
                .get("controller")
                .and_then(|c| c.as_bool())
                .unwrap_or(false);

            child_to_owners
                .entry(ev.subject.clone())
                .or_default()
                .push((owner_identity, ev.id, is_controller));
        }

        // Check for conflicting controller owners for each child.
        let mut conflicted_children: HashSet<ResourceIdentity> = HashSet::new();
        for (child, owners) in &child_to_owners {
            let mut controller_owners: Vec<&ResourceIdentity> = Vec::new();
            for (owner, _, is_controller) in owners {
                if *is_controller && !controller_owners.contains(&owner) {
                    controller_owners.push(owner);
                }
            }
            if controller_owners.len() > 1 {
                conflicted_children.insert(child.clone());
                let owner_strs: Vec<String> =
                    controller_owners.iter().map(|o| o.to_string()).collect();
                results.push(DiscoveryResult::Conflict {
                    description: format!(
                        "Resource {} has multiple conflicting controller owners: {}",
                        child,
                        owner_strs.join(", ")
                    ),
                });
            }
        }

        // Collect discovered relationships: (source, target) -> HashSet<EvidenceId>
        let mut discovered_rels: HashMap<
            (ResourceIdentity, ResourceIdentity),
            HashSet<EvidenceId>,
        > = HashMap::new();

        // 1. Direct ownership relationships
        for (child, owners) in &child_to_owners {
            if conflicted_children.contains(child) {
                continue;
            }
            for (owner, ev_id, _) in owners {
                discovered_rels
                    .entry((owner.clone(), child.clone()))
                    .or_default()
                    .insert(*ev_id);
            }
        }

        // 2. Transitive / Controller chain ownership (e.g. Deployment -> ReplicaSet -> Pod)
        for child in child_to_owners.keys() {
            if conflicted_children.contains(child) {
                continue;
            }

            let mut visited: HashSet<ResourceIdentity> = HashSet::new();
            visited.insert(child.clone());

            let mut current_ancestors: Vec<(ResourceIdentity, Vec<EvidenceId>)> = Vec::new();
            if let Some(immediate_owners) = child_to_owners.get(child) {
                for (owner, ev_id, _) in immediate_owners {
                    current_ancestors.push((owner.clone(), vec![*ev_id]));
                }
            }

            while !current_ancestors.is_empty() {
                let mut next_ancestors = Vec::new();
                for (ancestor, path_evs) in current_ancestors {
                    if !visited.insert(ancestor.clone()) {
                        // Cycle detected along chain; stop walking this path
                        continue;
                    }

                    if let Some(higher_owners) = child_to_owners.get(&ancestor) {
                        if !conflicted_children.contains(&ancestor) {
                            for (higher_owner, higher_ev_id, _) in higher_owners {
                                let mut combined_evs = path_evs.clone();
                                combined_evs.push(*higher_ev_id);

                                let ev_set = discovered_rels
                                    .entry((higher_owner.clone(), child.clone()))
                                    .or_default();
                                for id in &combined_evs {
                                    ev_set.insert(*id);
                                }

                                next_ancestors.push((higher_owner.clone(), combined_evs));
                            }
                        }
                    }
                }
                current_ancestors = next_ancestors;
            }
        }

        // Deterministic sorting of discovered relationships
        let mut sorted_rel_keys: Vec<(ResourceIdentity, ResourceIdentity)> =
            discovered_rels.keys().cloned().collect();
        sorted_rel_keys.sort_by(|(s1, t1), (s2, t2)| {
            s1.to_string()
                .cmp(&s2.to_string())
                .then_with(|| t1.to_string().cmp(&t2.to_string()))
        });

        for key in sorted_rel_keys {
            if let Some(ev_ids) = discovered_rels.remove(&key) {
                let (source, target) = key;
                let rel = Relationship::new(source, target, RelationshipKind::OWNS);
                let mut ev_vec: Vec<EvidenceId> = ev_ids.into_iter().collect();
                ev_vec.sort_by_key(|a| a.as_uuid());
                results.push(DiscoveryResult::Discovered(DiscoveredRelationship::new(
                    rel, ev_vec,
                )));
            }
        }

        results
    }
}

// ---------------------------------------------------------------------------
// ResourceReferenceRule
// ---------------------------------------------------------------------------

/// Derives `subject --DEPENDS_ON--> target` from [`ObservationType::RESOURCE_REFERENCE`]
/// observations that explicitly target another resource by identity.
///
/// # Invariants
///
/// 1. An observation with `observation_type == RESOURCE_REFERENCE` that references a target
///    resource (e.g. ConfigMap, Secret, ServiceAccount, or Service backend) derives:
///    `subject --DEPENDS_ON--> target`.
/// 2. Pure network endpoint observations (e.g. `reference_type == "service_endpoint"` or
///    with `address` and no `target`) are excluded (handled by [`RuntimeConnectionRule`]).
/// 3. If "target" is missing or null, the outcome is `Insufficient`.
/// 4. If "target" is malformed, the outcome is `Invalid`.
/// 5. Supporting evidence IDs are attached to each discovered relationship.
pub struct ResourceReferenceRule;

impl DiscoveryRule for ResourceReferenceRule {
    fn name(&self) -> &str {
        "ResourceReferenceRule"
    }

    fn apply(&self, evidence: &[Evidence]) -> Vec<DiscoveryResult> {
        let reference_evidences: Vec<&Evidence> = evidence
            .iter()
            .filter(|e| e.observation_type == ObservationType::RESOURCE_REFERENCE)
            .collect();

        if reference_evidences.is_empty() {
            return vec![];
        }

        let mut results: Vec<DiscoveryResult> = Vec::new();
        let mut discovered_rels: HashMap<
            (ResourceIdentity, ResourceIdentity),
            HashSet<EvidenceId>,
        > = HashMap::new();

        for ev in &reference_evidences {
            // Skip pure network endpoint mappings intended for RuntimeConnectionRule
            if let Some(ref_type) = ev.data.get("reference_type").and_then(|v| v.as_str()) {
                if ref_type == "service_endpoint" || ref_type == "node_address" {
                    continue;
                }
            } else if ev.data.get("address").is_some() && ev.data.get("target").is_none() {
                continue;
            }

            let Some(target_val) = ev.data.get("target") else {
                results.push(DiscoveryResult::Insufficient);
                continue;
            };

            if target_val.is_null() {
                results.push(DiscoveryResult::Insufficient);
                continue;
            }

            let target_identity = match parse_resource_identity(target_val) {
                Ok(id) => id,
                Err(err) => {
                    results.push(DiscoveryResult::Invalid {
                        description: format!(
                            "RESOURCE_REFERENCE evidence {} has malformed 'target': {}",
                            ev.id, err
                        ),
                    });
                    continue;
                }
            };

            discovered_rels
                .entry((ev.subject.clone(), target_identity))
                .or_default()
                .insert(ev.id);
        }

        let mut sorted_rel_keys: Vec<(ResourceIdentity, ResourceIdentity)> =
            discovered_rels.keys().cloned().collect();
        sorted_rel_keys.sort_by(|(s1, t1), (s2, t2)| {
            s1.to_string()
                .cmp(&s2.to_string())
                .then_with(|| t1.to_string().cmp(&t2.to_string()))
        });

        for key in sorted_rel_keys {
            if let Some(ev_ids) = discovered_rels.remove(&key) {
                let (source, target) = key;
                let rel = Relationship::new(source, target, RelationshipKind::DEPENDS_ON);
                let mut ev_vec: Vec<EvidenceId> = ev_ids.into_iter().collect();
                ev_vec.sort_by_key(|a| a.as_uuid());
                results.push(DiscoveryResult::Discovered(DiscoveredRelationship::new(
                    rel, ev_vec,
                )));
            }
        }

        results
    }
}

// ---------------------------------------------------------------------------
// DiscoveryEngine
// ---------------------------------------------------------------------------

/// Applies a set of [`DiscoveryRule`] implementations to a collection of
/// [`Evidence`] observations and returns all [`DiscoveryResult`]s.
///
/// # Architecture and Responsibilities
///
/// The engine is intentionally generic and thin:
/// - It coordinates execution across a collection of heterogeneous [`DiscoveryRule`] implementations.
/// - It passes the identical evidence slice to each registered rule.
/// - It collects results in the exact order rules are registered.
/// - It contains no domain-specific, provider-specific, or network-level logic (it knows nothing
///   about addresses, ports, Kubernetes, AWS, etc.).
/// - It does not deduplicate relationships or resolve conflicting results; deduplication and
///   semantic identity management belong to the graph/relationship layer.
///
/// # Usage
///
/// ```rust,ignore
/// let engine = DiscoveryEngine::default_v2();
/// let results = engine.run(&evidence_slice);
/// ```
///
/// # Extensibility
///
/// Additional rules are added by implementing [`DiscoveryRule`] and passing
/// the new rule instance to [`DiscoveryEngine::new`]. The engine itself does
/// not need to be modified.
pub struct DiscoveryEngine {
    rules: Vec<Box<dyn DiscoveryRule>>,
}

impl DiscoveryEngine {
    /// Create a `DiscoveryEngine` with the given set of rules.
    pub fn new(rules: Vec<Box<dyn DiscoveryRule>>) -> Self {
        Self { rules }
    }

    /// Create a `DiscoveryEngine` pre-loaded with the default v1 rule set.
    ///
    /// v1 rules: [`RuntimeConnectionRule`].
    pub fn default_v1() -> Self {
        Self::new(vec![Box::new(RuntimeConnectionRule)])
    }

    /// Create a `DiscoveryEngine` pre-loaded with the default v2 rule set.
    ///
    /// v2 rules: [`RuntimeConnectionRule`], [`OwnershipRule`], [`ResourceReferenceRule`].
    pub fn default_v2() -> Self {
        Self::new(vec![
            Box::new(RuntimeConnectionRule),
            Box::new(OwnershipRule),
            Box::new(ResourceReferenceRule),
        ])
    }

    /// Return the number of rules registered in this engine.
    pub fn rule_count(&self) -> usize {
        self.rules.len()
    }

    /// Return a slice of registered rules.
    pub fn rules(&self) -> &[Box<dyn DiscoveryRule>] {
        &self.rules
    }

    /// Apply all rules to the given evidence and return all results.
    ///
    /// Each rule receives the identical evidence slice and contributes zero or more results.
    /// Results from all rules are concatenated in registration order. The engine does not
    /// attempt to deduplicate results across rules or resolve conflicts.
    pub fn run(&self, evidence: &[Evidence]) -> Vec<DiscoveryResult> {
        self.rules
            .iter()
            .flat_map(|rule| rule.apply(evidence))
            .collect()
    }
}

impl Default for DiscoveryEngine {
    fn default() -> Self {
        Self::default_v2()
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::evidence::{CollectorId, EvidenceSource};
    use crate::resource::{Provider, ResourceKind};
    use chrono::Utc;
    use serde_json::json;

    // -----------------------------------------------------------------------
    // Helpers
    // -----------------------------------------------------------------------

    fn k8s_source() -> EvidenceSource {
        EvidenceSource::new(
            Provider::new("kubernetes"),
            CollectorId::from_static("k8s-runtime"),
        )
    }

    fn aws_source() -> EvidenceSource {
        EvidenceSource::new(
            Provider::new("aws"),
            CollectorId::from_static("aws-network"),
        )
    }

    fn pod_identity(name: &str) -> ResourceIdentity {
        ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            format!("payments/{name}"),
        )
    }

    fn db_identity(name: &str) -> ResourceIdentity {
        ResourceIdentity::new(
            Provider::new("aws"),
            ResourceKind::new("rds"),
            format!("arn:aws:rds:us-east-1:123:{name}"),
        )
    }

    fn connection_evidence(subject: ResourceIdentity, destination: &str, port: u16) -> Evidence {
        Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RUNTIME_CONNECTION,
            subject,
            json!({ "destination": destination, "port": port, "protocol": "tcp" }),
        )
    }

    fn mapping_evidence(subject: ResourceIdentity, address: &str, port: u16) -> Evidence {
        Evidence::new(
            aws_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            subject,
            json!({ "address": address, "port": port }),
        )
    }

    // -----------------------------------------------------------------------
    // 1. Runtime connection + resource mapping discovers DEPENDS_ON.
    // -----------------------------------------------------------------------
    #[test]
    fn test_runtime_connection_mapping_discovers_depends_on() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);
        let evidence = vec![conn, map];

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&evidence);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_discovered());
        let dr = results[0].discovered().unwrap();
        assert_eq!(dr.relationship.kind, RelationshipKind::DEPENDS_ON);
    }

    // -----------------------------------------------------------------------
    // 2. Discovered relationship has correct source.
    // -----------------------------------------------------------------------
    #[test]
    fn test_discovered_relationship_correct_source() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        let dr = results[0].discovered().unwrap();
        assert_eq!(dr.relationship.source, pod);
    }

    // -----------------------------------------------------------------------
    // 3. Discovered relationship has correct target.
    // -----------------------------------------------------------------------
    #[test]
    fn test_discovered_relationship_correct_target() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        let dr = results[0].discovered().unwrap();
        assert_eq!(dr.relationship.target, db);
    }

    // -----------------------------------------------------------------------
    // 4. Discovered relationship has correct RelationshipKind.
    // -----------------------------------------------------------------------
    #[test]
    fn test_discovered_relationship_correct_kind() {
        let pod = pod_identity("checkout");
        let db = db_identity("orders-db");

        let conn = connection_evidence(pod.clone(), "192.168.1.10", 3306);
        let map = mapping_evidence(db.clone(), "192.168.1.10", 3306);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        let dr = results[0].discovered().unwrap();
        assert_eq!(dr.relationship.kind, RelationshipKind::DEPENDS_ON);
        assert_eq!(
            dr.relationship.category,
            crate::relationship::RelationshipCategory::Dependency
        );
    }

    // -----------------------------------------------------------------------
    // 5. Supporting EvidenceIds are preserved.
    // -----------------------------------------------------------------------
    #[test]
    fn test_supporting_evidence_ids_preserved() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let conn_id = conn.id;
        let map_id = map.id;

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        let dr = results[0].discovered().unwrap();
        assert_eq!(dr.supporting_evidence.len(), 2);
        assert!(dr.supporting_evidence.contains(&conn_id));
        assert!(dr.supporting_evidence.contains(&map_id));
    }

    // -----------------------------------------------------------------------
    // 6. Same endpoint produces a valid discovery.
    // -----------------------------------------------------------------------
    #[test]
    fn test_same_endpoint_produces_valid_discovery() {
        let pod = pod_identity("api");
        let db = db_identity("main-db");

        let conn = connection_evidence(pod.clone(), "172.16.0.5", 5439);
        let map = mapping_evidence(db.clone(), "172.16.0.5", 5439);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_discovered());
    }

    // -----------------------------------------------------------------------
    // 7. Different endpoints do not produce a relationship.
    // -----------------------------------------------------------------------
    #[test]
    fn test_different_endpoints_no_relationship() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        // connection to .15, mapping for .99 — no match
        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.99", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_insufficient());
    }

    // -----------------------------------------------------------------------
    // 8. Missing connection evidence does not produce a relationship.
    // -----------------------------------------------------------------------
    #[test]
    fn test_missing_connection_evidence_no_relationship() {
        let db = db_identity("payments-db");

        // Only a mapping, no connection
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[map]);

        // No RUNTIME_CONNECTION → rule returns nothing at all
        assert!(results.is_empty());
    }

    // -----------------------------------------------------------------------
    // 9. Missing resource mapping does not produce a relationship.
    // -----------------------------------------------------------------------
    #[test]
    fn test_missing_resource_mapping_no_relationship() {
        let pod = pod_identity("payments-api");

        // Connection exists, but no RESOURCE_REFERENCE to map the endpoint
        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_insufficient());
    }

    // -----------------------------------------------------------------------
    // 10. Malformed evidence does not panic — returns Invalid.
    // -----------------------------------------------------------------------
    #[test]
    fn test_malformed_connection_evidence_does_not_panic() {
        let pod = pod_identity("payments-api");

        // RUNTIME_CONNECTION with missing destination field
        let malformed = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RUNTIME_CONNECTION,
            pod,
            json!({ "port": 5432 }), // missing "destination"
        );

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[malformed]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_invalid());
    }

    // -----------------------------------------------------------------------
    // 11. Duplicate discovery does not change semantic relationship identity.
    // -----------------------------------------------------------------------
    #[test]
    fn test_duplicate_discovery_same_relationship_identity() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn1 = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let conn2 = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn1, conn2, map]);

        // Two connections to the same endpoint produce two discovery results
        // but both describe the SAME semantic relationship (source, target, kind).
        assert_eq!(results.len(), 2);
        let dr1 = results[0].discovered().unwrap();
        let dr2 = results[1].discovered().unwrap();

        // Same semantic identity
        assert_eq!(dr1.relationship, dr2.relationship);
        // But different supporting evidence (different connection observations)
        assert_ne!(dr1.supporting_evidence, dr2.supporting_evidence);
    }

    // -----------------------------------------------------------------------
    // 12. Multiple supporting EvidenceIds can support the same relationship.
    // -----------------------------------------------------------------------
    #[test]
    fn test_multiple_supporting_evidence_ids() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let conn_id = conn.id;
        let map_id = map.id;

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        let dr = results[0].discovered().unwrap();
        // Both connection evidence and mapping evidence are preserved
        assert_eq!(dr.supporting_evidence.len(), 2);
        assert!(dr.supporting_evidence.contains(&conn_id));
        assert!(dr.supporting_evidence.contains(&map_id));
    }

    // -----------------------------------------------------------------------
    // 13. Conflicting resource mappings are not arbitrarily resolved.
    // -----------------------------------------------------------------------
    #[test]
    fn test_conflicting_resource_mappings_produce_conflict() {
        let pod = pod_identity("payments-api");
        let db_b = db_identity("database-b");
        let db_c = db_identity("database-c");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        // Two different resources claim the same endpoint
        let map_b = mapping_evidence(db_b.clone(), "10.0.2.15", 5432);
        let map_c = mapping_evidence(db_c.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map_b, map_c]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_conflict());

        if let DiscoveryResult::Conflict { description } = &results[0] {
            assert!(description.contains("10.0.2.15"));
            assert!(description.contains("5432"));
        }
    }

    // -----------------------------------------------------------------------
    // 14. Discovery does not modify the original Evidence.
    // -----------------------------------------------------------------------
    #[test]
    fn test_discovery_does_not_modify_evidence() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let original_conn_id = conn.id;
        let original_conn_subject = conn.subject.clone();
        let original_conn_data = conn.data.clone();

        let rule = RuntimeConnectionRule;
        let _ = rule.apply(&[conn.clone(), map]);

        // Evidence is unchanged
        assert_eq!(conn.id, original_conn_id);
        assert_eq!(conn.subject, original_conn_subject);
        assert_eq!(conn.data, original_conn_data);
    }

    // -----------------------------------------------------------------------
    // 15. Discovery does not modify the existing Relationship model.
    // -----------------------------------------------------------------------
    #[test]
    fn test_discovery_does_not_modify_relationship_model() {
        // Verify that Relationship is unchanged post-discovery — its fields
        // match what was specified before discovery ran.
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        let dr = results[0].discovered().unwrap();
        // Relationship fields are exactly what we expect — no extra fields added
        assert_eq!(dr.relationship.source, pod);
        assert_eq!(dr.relationship.target, db);
        assert_eq!(dr.relationship.kind, RelationshipKind::DEPENDS_ON);
        // No evidence inside Relationship itself
        // (verified at compile time — Relationship has no evidence field)
    }

    // -----------------------------------------------------------------------
    // 16. Serialization works for DiscoveredRelationship and DiscoveryResult.
    // -----------------------------------------------------------------------
    #[test]
    fn test_serialization_round_trip() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        let dr = results[0].discovered().unwrap().clone();

        // Round-trip DiscoveredRelationship
        let json = serde_json::to_string(&dr).expect("serialization failed");
        let restored: DiscoveredRelationship =
            serde_json::from_str(&json).expect("deserialization failed");
        assert_eq!(dr, restored);
        assert_eq!(dr.supporting_evidence, restored.supporting_evidence);

        // Round-trip DiscoveryResult
        let result = DiscoveryResult::Discovered(dr.clone());
        let result_json = serde_json::to_string(&result).expect("result serialization failed");
        let restored_result: DiscoveryResult =
            serde_json::from_str(&result_json).expect("result deserialization failed");
        assert_eq!(result, restored_result);
    }

    // -----------------------------------------------------------------------
    // 17. DiscoveryEngine::default_v1 runs RuntimeConnectionRule correctly.
    // -----------------------------------------------------------------------
    #[test]
    fn test_discovery_engine_default_v1() {
        let pod = pod_identity("checkout-api");
        let db = db_identity("orders-db");

        let conn = connection_evidence(pod.clone(), "10.1.0.5", 5432);
        let map = mapping_evidence(db.clone(), "10.1.0.5", 5432);

        let engine = DiscoveryEngine::default_v1();
        let results = engine.run(&[conn, map]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_discovered());

        let dr = results[0].discovered().unwrap();
        assert_eq!(dr.relationship.source, pod);
        assert_eq!(dr.relationship.target, db);
        assert_eq!(dr.relationship.kind, RelationshipKind::DEPENDS_ON);
    }

    // -----------------------------------------------------------------------
    // 18. Port mismatch alone prevents discovery.
    // -----------------------------------------------------------------------
    #[test]
    fn test_port_mismatch_prevents_discovery() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        // Same IP, different port — should not match
        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5433);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[conn, map]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_insufficient());
    }

    // -----------------------------------------------------------------------
    // 19. Non-RUNTIME_CONNECTION evidence is not treated as connection evidence.
    // -----------------------------------------------------------------------
    #[test]
    fn test_unrelated_observation_types_are_ignored() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        // CONFIGURATION observation — should not be treated as a connection
        let config_ev = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::CONFIGURATION,
            pod,
            json!({ "destination": "10.0.2.15", "port": 5432 }),
        );
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        let rule = RuntimeConnectionRule;
        let results = rule.apply(&[config_ev, map]);

        // No RUNTIME_CONNECTION → rule returns nothing
        assert!(results.is_empty());
    }

    // -----------------------------------------------------------------------
    // Test-only Mock Discovery Rules (extensibility demonstration)
    // -----------------------------------------------------------------------

    /// Mock rule simulating ownership discovery (e.g. GitHub team OWNS Kubernetes service).
    struct MockOwnershipRule;

    impl DiscoveryRule for MockOwnershipRule {
        fn name(&self) -> &str {
            "MockOwnershipRule"
        }

        fn apply(&self, evidence: &[Evidence]) -> Vec<DiscoveryResult> {
            if evidence.is_empty() {
                return Vec::new();
            }
            let team = ResourceIdentity::new(
                Provider::new("github"),
                ResourceKind::new("team"),
                "payments-team",
            );
            let service = ResourceIdentity::new(
                Provider::new("kubernetes"),
                ResourceKind::new("service"),
                "payments-service",
            );
            let rel = Relationship::new(team, service, RelationshipKind::OWNS);
            vec![DiscoveryResult::Discovered(DiscoveredRelationship::new(
                rel,
                evidence.iter().map(|e| e.id).collect(),
            ))]
        }
    }

    /// Mock rule simulating configuration discovery (e.g. deployment DEPENDS_ON configmap).
    struct MockConfigurationRule;

    impl DiscoveryRule for MockConfigurationRule {
        fn name(&self) -> &str {
            "MockConfigurationRule"
        }

        fn apply(&self, evidence: &[Evidence]) -> Vec<DiscoveryResult> {
            if evidence.is_empty() {
                return Vec::new();
            }
            let deployment = ResourceIdentity::new(
                Provider::new("kubernetes"),
                ResourceKind::new("deployment"),
                "payments-api",
            );
            let configmap = ResourceIdentity::new(
                Provider::new("kubernetes"),
                ResourceKind::new("configmap"),
                "payments-config",
            );
            let rel = Relationship::new(deployment, configmap, RelationshipKind::DEPENDS_ON);
            vec![DiscoveryResult::Discovered(DiscoveredRelationship::new(
                rel,
                evidence.iter().map(|e| e.id).collect(),
            ))]
        }
    }

    /// Mock rule with configurable name and target identity, used to test execution ordering.
    struct MockOrderedRule {
        rule_name: &'static str,
        target_id: &'static str,
    }

    impl DiscoveryRule for MockOrderedRule {
        fn name(&self) -> &str {
            self.rule_name
        }

        fn apply(&self, evidence: &[Evidence]) -> Vec<DiscoveryResult> {
            let src = ResourceIdentity::new(
                Provider::new("internal"),
                ResourceKind::new("service"),
                "caller",
            );
            let tgt = ResourceIdentity::new(
                Provider::new("internal"),
                ResourceKind::new("service"),
                self.target_id,
            );
            let rel = Relationship::new(src, tgt, RelationshipKind::CALLS);
            vec![DiscoveryResult::Discovered(DiscoveredRelationship::new(
                rel,
                evidence.iter().map(|e| e.id).collect(),
            ))]
        }
    }

    /// Mock rule that always returns an empty vector of results.
    struct MockEmptyRule;

    impl DiscoveryRule for MockEmptyRule {
        fn name(&self) -> &str {
            "MockEmptyRule"
        }

        fn apply(&self, _evidence: &[Evidence]) -> Vec<DiscoveryResult> {
            Vec::new()
        }
    }

    /// Mock rule that emits a fixed relationship (used to test deduplication non-occurrence).
    struct MockDuplicateRule;

    impl DiscoveryRule for MockDuplicateRule {
        fn name(&self) -> &str {
            "MockDuplicateRule"
        }

        fn apply(&self, evidence: &[Evidence]) -> Vec<DiscoveryResult> {
            let src = ResourceIdentity::new(
                Provider::new("test"),
                ResourceKind::new("service"),
                "common-src",
            );
            let tgt = ResourceIdentity::new(
                Provider::new("test"),
                ResourceKind::new("service"),
                "common-tgt",
            );
            let rel = Relationship::new(src, tgt, RelationshipKind::DEPENDS_ON);
            vec![DiscoveryResult::Discovered(DiscoveredRelationship::new(
                rel,
                evidence.iter().map(|e| e.id).collect(),
            ))]
        }
    }

    /// Mock rule that emits a Conflict result.
    struct MockConflictRule;

    impl DiscoveryRule for MockConflictRule {
        fn name(&self) -> &str {
            "MockConflictRule"
        }

        fn apply(&self, _evidence: &[Evidence]) -> Vec<DiscoveryResult> {
            vec![DiscoveryResult::Conflict {
                description: "ambiguous routing candidate".to_string(),
            }]
        }
    }

    /// Mock rule that captures observed EvidenceIds into a shared vector.
    struct MockEvidenceRecordingRule {
        captured_ids: std::sync::Arc<std::sync::Mutex<Vec<EvidenceId>>>,
    }

    impl DiscoveryRule for MockEvidenceRecordingRule {
        fn name(&self) -> &str {
            "MockEvidenceRecordingRule"
        }

        fn apply(&self, evidence: &[Evidence]) -> Vec<DiscoveryResult> {
            let mut ids = self.captured_ids.lock().unwrap();
            *ids = evidence.iter().map(|e| e.id).collect();
            Vec::new()
        }
    }

    // -----------------------------------------------------------------------
    // 20. Engine extensible with multiple heterogeneous rules.
    // -----------------------------------------------------------------------
    #[test]
    fn test_engine_extensibility_with_multiple_heterogeneous_rules() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");

        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);
        let evidence = vec![conn, map];

        let engine = DiscoveryEngine::new(vec![
            Box::new(RuntimeConnectionRule),
            Box::new(MockOwnershipRule),
            Box::new(MockConfigurationRule),
        ]);

        assert_eq!(engine.rule_count(), 3);
        assert_eq!(engine.rules()[0].name(), "RuntimeConnectionRule");
        assert_eq!(engine.rules()[1].name(), "MockOwnershipRule");
        assert_eq!(engine.rules()[2].name(), "MockConfigurationRule");

        let results = engine.run(&evidence);

        // All three rules ran and produced their respective discoveries
        assert_eq!(results.len(), 3);

        // Result 0: RuntimeConnectionRule -> pod DEPENDS_ON db
        let r0 = results[0]
            .discovered()
            .expect("result 0 should be Discovered");
        assert_eq!(r0.relationship.source, pod);
        assert_eq!(r0.relationship.target, db);
        assert_eq!(r0.relationship.kind, RelationshipKind::DEPENDS_ON);

        // Result 1: MockOwnershipRule -> team OWNS service
        let r1 = results[1]
            .discovered()
            .expect("result 1 should be Discovered");
        assert_eq!(r1.relationship.source.provider.as_str(), "github");
        assert_eq!(r1.relationship.source.resource_type.as_str(), "team");
        assert_eq!(r1.relationship.source.provider_id.as_str(), "payments-team");
        assert_eq!(r1.relationship.kind, RelationshipKind::OWNS);

        // Result 2: MockConfigurationRule -> deployment DEPENDS_ON configmap
        let r2 = results[2]
            .discovered()
            .expect("result 2 should be Discovered");
        assert_eq!(r2.relationship.source.resource_type.as_str(), "deployment");
        assert_eq!(r2.relationship.target.resource_type.as_str(), "configmap");
        assert_eq!(r2.relationship.kind, RelationshipKind::DEPENDS_ON);
    }

    // -----------------------------------------------------------------------
    // 21. Engine preserves rule execution order in output results.
    // -----------------------------------------------------------------------
    #[test]
    fn test_engine_preserves_rule_execution_order() {
        let pod = pod_identity("payments-api");
        let conn = connection_evidence(pod, "10.0.2.15", 5432);
        let evidence = vec![conn];

        // Register in order: RuleA, RuleB, RuleC
        let engine_abc = DiscoveryEngine::new(vec![
            Box::new(MockOrderedRule {
                rule_name: "RuleA",
                target_id: "target-a",
            }),
            Box::new(MockOrderedRule {
                rule_name: "RuleB",
                target_id: "target-b",
            }),
            Box::new(MockOrderedRule {
                rule_name: "RuleC",
                target_id: "target-c",
            }),
        ]);

        let results_abc = engine_abc.run(&evidence);
        assert_eq!(results_abc.len(), 3);
        let targets_abc: Vec<&str> = results_abc
            .iter()
            .map(|r| {
                r.discovered()
                    .unwrap()
                    .relationship
                    .target
                    .provider_id
                    .as_str()
            })
            .collect();
        assert_eq!(targets_abc, vec!["target-a", "target-b", "target-c"]);

        // Register in different order: RuleC, RuleA, RuleB
        let engine_cab = DiscoveryEngine::new(vec![
            Box::new(MockOrderedRule {
                rule_name: "RuleC",
                target_id: "target-c",
            }),
            Box::new(MockOrderedRule {
                rule_name: "RuleA",
                target_id: "target-a",
            }),
            Box::new(MockOrderedRule {
                rule_name: "RuleB",
                target_id: "target-b",
            }),
        ]);

        let results_cab = engine_cab.run(&evidence);
        assert_eq!(results_cab.len(), 3);
        let targets_cab: Vec<&str> = results_cab
            .iter()
            .map(|r| {
                r.discovered()
                    .unwrap()
                    .relationship
                    .target
                    .provider_id
                    .as_str()
            })
            .collect();
        assert_eq!(targets_cab, vec!["target-c", "target-a", "target-b"]);
    }

    // -----------------------------------------------------------------------
    // 22. Empty engine behaves correctly.
    // -----------------------------------------------------------------------
    #[test]
    fn test_empty_engine_behaves_correctly() {
        let engine = DiscoveryEngine::new(vec![]);
        assert_eq!(engine.rule_count(), 0);
        assert!(engine.rules().is_empty());

        let pod = pod_identity("payments-api");
        let conn = connection_evidence(pod, "10.0.2.15", 5432);

        // Run with evidence
        let results_with_evidence = engine.run(&[conn]);
        assert!(results_with_evidence.is_empty());

        // Run without evidence
        let results_empty = engine.run(&[]);
        assert!(results_empty.is_empty());
    }

    // -----------------------------------------------------------------------
    // 23. One rule returning no results does not prevent subsequent rules.
    // -----------------------------------------------------------------------
    #[test]
    fn test_empty_rule_result_does_not_prevent_subsequent_rules() {
        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");
        let conn = connection_evidence(pod, "10.0.2.15", 5432);
        let map = mapping_evidence(db, "10.0.2.15", 5432);
        let evidence = vec![conn, map];

        // Register:
        // 1. MockEmptyRule (returns 0 results)
        // 2. RuntimeConnectionRule (returns 1 result)
        // 3. MockEmptyRule (returns 0 results)
        // 4. MockOwnershipRule (returns 1 result)
        let engine = DiscoveryEngine::new(vec![
            Box::new(MockEmptyRule),
            Box::new(RuntimeConnectionRule),
            Box::new(MockEmptyRule),
            Box::new(MockOwnershipRule),
        ]);

        let results = engine.run(&evidence);
        assert_eq!(results.len(), 2);
        assert_eq!(
            results[0].discovered().unwrap().relationship.kind,
            RelationshipKind::DEPENDS_ON
        );
        assert_eq!(
            results[1].discovered().unwrap().relationship.kind,
            RelationshipKind::OWNS
        );
    }

    // -----------------------------------------------------------------------
    // 24. Engine does not deduplicate relationships across rules.
    // -----------------------------------------------------------------------
    #[test]
    fn test_engine_does_not_deduplicate_across_rules() {
        let engine = DiscoveryEngine::new(vec![
            Box::new(MockDuplicateRule),
            Box::new(MockDuplicateRule),
        ]);

        let pod = pod_identity("payments-api");
        let conn = connection_evidence(pod, "10.0.2.15", 5432);

        let results = engine.run(&[conn]);
        // The engine must preserve both discoveries; deduplication belongs to graph layer
        assert_eq!(results.len(), 2);
        assert_eq!(results[0], results[1]);
    }

    // -----------------------------------------------------------------------
    // 25. Engine preserves Conflict results without resolving or dropping them.
    // -----------------------------------------------------------------------
    #[test]
    fn test_engine_preserves_conflict_results_without_resolving() {
        let engine = DiscoveryEngine::new(vec![
            Box::new(MockOwnershipRule),
            Box::new(MockConflictRule),
        ]);

        let pod = pod_identity("payments-api");
        let conn = connection_evidence(pod, "10.0.2.15", 5432);

        let results = engine.run(&[conn]);
        assert_eq!(results.len(), 2);
        assert!(results[0].is_discovered());
        assert!(results[1].is_conflict());

        if let DiscoveryResult::Conflict { description } = &results[1] {
            assert_eq!(description, "ambiguous routing candidate");
        } else {
            panic!("expected Conflict result variant");
        }
    }

    // -----------------------------------------------------------------------
    // 26. Engine passes the identical evidence slice to all registered rules.
    // -----------------------------------------------------------------------
    #[test]
    fn test_engine_passes_identical_evidence_to_all_rules() {
        let captured_1 = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
        let captured_2 = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));

        let engine = DiscoveryEngine::new(vec![
            Box::new(MockEvidenceRecordingRule {
                captured_ids: std::sync::Arc::clone(&captured_1),
            }),
            Box::new(MockEvidenceRecordingRule {
                captured_ids: std::sync::Arc::clone(&captured_2),
            }),
        ]);

        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");
        let conn = connection_evidence(pod, "10.0.2.15", 5432);
        let map = mapping_evidence(db, "10.0.2.15", 5432);
        let expected_ids = vec![conn.id, map.id];

        let _ = engine.run(&[conn, map]);

        assert_eq!(*captured_1.lock().unwrap(), expected_ids);
        assert_eq!(*captured_2.lock().unwrap(), expected_ids);
    }

    // -----------------------------------------------------------------------
    // 27. OwnershipRule: valid owner reference -> Discovered
    // -----------------------------------------------------------------------
    #[test]
    fn test_ownership_valid_owner_reference_discovered() {
        let dep = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("deployment"),
            "payments/web-deploy",
        );
        let pod = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            "payments/web-pod-xyz",
        );

        let ev = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::OWNERSHIP_REFERENCE,
            pod.clone(),
            json!({
                "owner": {
                    "provider": "kubernetes",
                    "resource_type": "deployment",
                    "provider_id": "payments/web-deploy"
                },
                "controller": true
            }),
        );

        let rule = OwnershipRule;
        let results = rule.apply(std::slice::from_ref(&ev));

        assert_eq!(results.len(), 1);
        let disc = results[0].discovered().expect("expected Discovered result");
        assert_eq!(disc.relationship.source, dep);
        assert_eq!(disc.relationship.target, pod);
        assert_eq!(disc.relationship.kind, RelationshipKind::OWNS);
        assert_eq!(disc.supporting_evidence, vec![ev.id]);
    }

    // -----------------------------------------------------------------------
    // 28. OwnershipRule: transitive controller chain (Deployment -> ReplicaSet -> Pod)
    // -----------------------------------------------------------------------
    #[test]
    fn test_ownership_transitive_controller_chain() {
        let dep = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("deployment"),
            "payments/web-deploy",
        );
        let rs = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("replicaset"),
            "payments/web-rs-123",
        );
        let pod = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            "payments/web-pod-xyz",
        );

        let ev_rs = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::OWNERSHIP_REFERENCE,
            rs.clone(),
            json!({
                "owner": {
                    "provider": "kubernetes",
                    "resource_type": "deployment",
                    "provider_id": "payments/web-deploy"
                },
                "controller": true
            }),
        );

        let ev_pod = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::OWNERSHIP_REFERENCE,
            pod.clone(),
            json!({
                "owner": {
                    "provider": "kubernetes",
                    "resource_type": "replicaset",
                    "provider_id": "payments/web-rs-123"
                },
                "controller": true
            }),
        );

        let rule = OwnershipRule;
        let results = rule.apply(&[ev_rs.clone(), ev_pod.clone()]);

        // Expected 3 relationships:
        // 1. dep OWNS rs (ev_rs)
        // 2. dep OWNS pod (ev_rs, ev_pod)
        // 3. rs OWNS pod (ev_pod)
        assert_eq!(results.len(), 3);

        let rels: Vec<_> = results.iter().map(|r| r.discovered().unwrap()).collect();

        // 1. dep OWNS pod
        let dep_pod = rels
            .iter()
            .find(|d| d.relationship.source == dep && d.relationship.target == pod)
            .expect("expected dep OWNS pod");
        assert_eq!(dep_pod.relationship.kind, RelationshipKind::OWNS);
        let mut expected_evs = vec![ev_rs.id, ev_pod.id];
        expected_evs.sort_by_key(|a| a.as_uuid());
        assert_eq!(dep_pod.supporting_evidence, expected_evs);

        // 2. dep OWNS rs
        let dep_rs = rels
            .iter()
            .find(|d| d.relationship.source == dep && d.relationship.target == rs)
            .expect("expected dep OWNS rs");
        assert_eq!(dep_rs.supporting_evidence, vec![ev_rs.id]);

        // 3. rs OWNS pod
        let rs_pod = rels
            .iter()
            .find(|d| d.relationship.source == rs && d.relationship.target == pod)
            .expect("expected rs OWNS pod");
        assert_eq!(rs_pod.supporting_evidence, vec![ev_pod.id]);
    }

    // -----------------------------------------------------------------------
    // 29. OwnershipRule: missing owner metadata -> Insufficient
    // -----------------------------------------------------------------------
    #[test]
    fn test_ownership_missing_owner_insufficient() {
        let pod = pod_identity("payments-api");
        let ev = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::OWNERSHIP_REFERENCE,
            pod,
            json!({ "controller": true }), // no "owner" key
        );

        let rule = OwnershipRule;
        let results = rule.apply(&[ev]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_insufficient());
    }

    // -----------------------------------------------------------------------
    // 30. OwnershipRule: malformed owner metadata -> Invalid
    // -----------------------------------------------------------------------
    #[test]
    fn test_ownership_malformed_owner_invalid() {
        let pod = pod_identity("payments-api");
        let ev = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::OWNERSHIP_REFERENCE,
            pod,
            json!({ "owner": "string-instead-of-object" }),
        );

        let rule = OwnershipRule;
        let results = rule.apply(&[ev]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_invalid());
    }

    // -----------------------------------------------------------------------
    // 31. OwnershipRule: conflicting controller owners -> Conflict
    // -----------------------------------------------------------------------
    #[test]
    fn test_ownership_conflicting_controller_owners() {
        let pod = pod_identity("payments-api");
        let ev1 = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::OWNERSHIP_REFERENCE,
            pod.clone(),
            json!({
                "owner": {
                    "provider": "kubernetes",
                    "resource_type": "deployment",
                    "provider_id": "payments/deploy-1"
                },
                "controller": true
            }),
        );
        let ev2 = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::OWNERSHIP_REFERENCE,
            pod,
            json!({
                "owner": {
                    "provider": "kubernetes",
                    "resource_type": "deployment",
                    "provider_id": "payments/deploy-2"
                },
                "controller": true
            }),
        );

        let rule = OwnershipRule;
        let results = rule.apply(&[ev1, ev2]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_conflict());
    }

    // -----------------------------------------------------------------------
    // 32. OwnershipRule: deterministic output
    // -----------------------------------------------------------------------
    #[test]
    fn test_ownership_deterministic_output() {
        let rs = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("replicaset"),
            "payments/web-rs-123",
        );
        let pod = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            "payments/web-pod-xyz",
        );

        let ev_rs = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::OWNERSHIP_REFERENCE,
            rs,
            json!({
                "owner": {
                    "provider": "kubernetes",
                    "resource_type": "deployment",
                    "provider_id": "payments/web-deploy"
                },
                "controller": true
            }),
        );
        let ev_pod = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::OWNERSHIP_REFERENCE,
            pod,
            json!({
                "owner": {
                    "provider": "kubernetes",
                    "resource_type": "replicaset",
                    "provider_id": "payments/web-rs-123"
                },
                "controller": true
            }),
        );

        let rule = OwnershipRule;
        let run1 = rule.apply(&[ev_rs.clone(), ev_pod.clone()]);
        let run2 = rule.apply(&[ev_pod, ev_rs]);

        assert_eq!(run1, run2);
    }

    // -----------------------------------------------------------------------
    // 33. ResourceReferenceRule: valid reference -> Discovered
    // -----------------------------------------------------------------------
    #[test]
    fn test_resource_reference_valid_discovered() {
        let dep = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("deployment"),
            "payments/web-deploy",
        );
        let cm = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("configmap"),
            "payments/app-config",
        );

        let ev = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            dep.clone(),
            json!({
                "reference_type": "config_map_ref",
                "target": {
                    "provider": "kubernetes",
                    "resource_type": "configmap",
                    "provider_id": "payments/app-config"
                }
            }),
        );

        let rule = ResourceReferenceRule;
        let results = rule.apply(std::slice::from_ref(&ev));

        assert_eq!(results.len(), 1);
        let disc = results[0].discovered().expect("expected Discovered result");
        assert_eq!(disc.relationship.source, dep);
        assert_eq!(disc.relationship.target, cm);
        assert_eq!(disc.relationship.kind, RelationshipKind::DEPENDS_ON);
        assert_eq!(disc.supporting_evidence, vec![ev.id]);
    }

    // -----------------------------------------------------------------------
    // 34. ResourceReferenceRule: missing target -> Insufficient
    // -----------------------------------------------------------------------
    #[test]
    fn test_resource_reference_missing_target_insufficient() {
        let dep = pod_identity("web-deploy");
        let ev = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            dep,
            json!({ "reference_type": "config_map_ref" }), // missing "target"
        );

        let rule = ResourceReferenceRule;
        let results = rule.apply(&[ev]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_insufficient());
    }

    // -----------------------------------------------------------------------
    // 35. ResourceReferenceRule: malformed target -> Invalid
    // -----------------------------------------------------------------------
    #[test]
    fn test_resource_reference_malformed_target_invalid() {
        let dep = pod_identity("web-deploy");
        let ev = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            dep,
            json!({ "target": 12345 }),
        );

        let rule = ResourceReferenceRule;
        let results = rule.apply(&[ev]);

        assert_eq!(results.len(), 1);
        assert!(results[0].is_invalid());
    }

    // -----------------------------------------------------------------------
    // 36. ResourceReferenceRule: endpoint mappings skipped
    // -----------------------------------------------------------------------
    #[test]
    fn test_resource_reference_skips_endpoint_mappings() {
        let svc = pod_identity("web-svc");
        let ev = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            svc,
            json!({
                "address": "10.0.0.1",
                "port": 80,
                "reference_type": "service_endpoint"
            }),
        );

        let rule = ResourceReferenceRule;
        let results = rule.apply(&[ev]);

        // Pure endpoint mappings must be skipped by ResourceReferenceRule
        assert!(results.is_empty());
    }

    // -----------------------------------------------------------------------
    // 37. DiscoveryEngine::default_v2 coordinates all rules
    // -----------------------------------------------------------------------
    #[test]
    fn test_discovery_engine_default_v2_runs_all_rules() {
        let engine = DiscoveryEngine::default_v2();
        assert_eq!(engine.rule_count(), 3);

        let pod = pod_identity("payments-api");
        let db = db_identity("payments-db");
        let dep = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("deployment"),
            "payments/web-deploy",
        );
        let cm = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("configmap"),
            "payments/app-config",
        );

        // 1. Runtime connection evidence
        let conn = connection_evidence(pod.clone(), "10.0.2.15", 5432);
        let map = mapping_evidence(db.clone(), "10.0.2.15", 5432);

        // 2. Ownership evidence
        let owner_ev = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::OWNERSHIP_REFERENCE,
            pod.clone(),
            json!({
                "owner": {
                    "provider": "kubernetes",
                    "resource_type": "deployment",
                    "provider_id": "payments/web-deploy"
                },
                "controller": true
            }),
        );

        // 3. Resource reference evidence
        let ref_ev = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            dep.clone(),
            json!({
                "target": {
                    "provider": "kubernetes",
                    "resource_type": "configmap",
                    "provider_id": "payments/app-config"
                }
            }),
        );

        let results = engine.run(&[conn, map, owner_ev, ref_ev]);

        // Expected:
        // - RuntimeConnectionRule: pod -> db (DEPENDS_ON)
        // - OwnershipRule: dep -> pod (OWNS)
        // - ResourceReferenceRule: dep -> cm (DEPENDS_ON)
        assert_eq!(results.len(), 3);

        let rels: Vec<_> = results.iter().map(|r| r.discovered().unwrap()).collect();
        assert!(rels.iter().any(|d| d.relationship.source == pod
            && d.relationship.target == db
            && d.relationship.kind == RelationshipKind::DEPENDS_ON));
        assert!(rels.iter().any(|d| d.relationship.source == dep
            && d.relationship.target == pod
            && d.relationship.kind == RelationshipKind::OWNS));
        assert!(rels.iter().any(|d| d.relationship.source == dep
            && d.relationship.target == cm
            && d.relationship.kind == RelationshipKind::DEPENDS_ON));
    }

    // -----------------------------------------------------------------------
    // 38. ResourceReferenceRule: all Kubernetes declarative reference types
    // -----------------------------------------------------------------------
    #[test]
    fn test_resource_reference_all_k8s_declarative_reference_types() {
        let pod = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            "prod/web-pod-1",
        );
        let node = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("node"),
            "prod-worker-1",
        );
        let cm = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("config_map"),
            "prod/app-config",
        );
        let sec = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("secret"),
            "prod/db-secret",
        );
        let pvc = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("persistent_volume_claim"),
            "prod/data-pvc",
        );
        let sa = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("service_account"),
            "prod/web-sa",
        );
        let ing = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("ingress"),
            "prod/web-ingress",
        );
        let svc = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("service"),
            "prod/web-service",
        );

        let ev_node = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            pod.clone(),
            json!({
                "reference_type": "pod_scheduled_node",
                "target": {
                    "provider": "kubernetes",
                    "resource_type": "node",
                    "provider_id": "prod-worker-1"
                }
            }),
        );

        let ev_cm = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            pod.clone(),
            json!({
                "reference_type": "config_map_ref",
                "target": {
                    "provider": "kubernetes",
                    "resource_type": "config_map",
                    "provider_id": "prod/app-config"
                }
            }),
        );

        let ev_sec = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            pod.clone(),
            json!({
                "reference_type": "secret_ref",
                "target": {
                    "provider": "kubernetes",
                    "resource_type": "secret",
                    "provider_id": "prod/db-secret"
                }
            }),
        );

        let ev_pvc = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            pod.clone(),
            json!({
                "reference_type": "pvc_mount_ref",
                "target": {
                    "provider": "kubernetes",
                    "resource_type": "persistent_volume_claim",
                    "provider_id": "prod/data-pvc"
                }
            }),
        );

        let ev_sa = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            pod.clone(),
            json!({
                "reference_type": "service_account_ref",
                "target": {
                    "provider": "kubernetes",
                    "resource_type": "service_account",
                    "provider_id": "prod/web-sa"
                }
            }),
        );

        let ev_ing = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            ing.clone(),
            json!({
                "reference_type": "ingress_backend_ref",
                "target": {
                    "provider": "kubernetes",
                    "resource_type": "service",
                    "provider_id": "prod/web-service"
                }
            }),
        );

        let rule = ResourceReferenceRule;
        let results = rule.apply(&[
            ev_node.clone(),
            ev_cm.clone(),
            ev_sec.clone(),
            ev_pvc.clone(),
            ev_sa.clone(),
            ev_ing.clone(),
        ]);

        assert_eq!(results.len(), 6);
        for res in &results {
            let disc = res.discovered().expect("expected discovered relationship");
            assert_eq!(disc.relationship.kind, RelationshipKind::DEPENDS_ON);
            assert_eq!(
                disc.relationship.category,
                crate::relationship::RelationshipCategory::Dependency
            );
        }

        let rels: Vec<_> = results.iter().map(|r| r.discovered().unwrap()).collect();
        assert!(rels
            .iter()
            .any(|d| d.relationship.source == pod && d.relationship.target == node));
        assert!(rels
            .iter()
            .any(|d| d.relationship.source == pod && d.relationship.target == cm));
        assert!(rels
            .iter()
            .any(|d| d.relationship.source == pod && d.relationship.target == sec));
        assert!(rels
            .iter()
            .any(|d| d.relationship.source == pod && d.relationship.target == pvc));
        assert!(rels
            .iter()
            .any(|d| d.relationship.source == pod && d.relationship.target == sa));
        assert!(rels
            .iter()
            .any(|d| d.relationship.source == ing && d.relationship.target == svc));
    }

    // -----------------------------------------------------------------------
    // 39. ResourceReferenceRule: multiple observations merge supporting evidence IDs
    // -----------------------------------------------------------------------
    #[test]
    fn test_resource_reference_multiple_observations_merge_evidence_ids() {
        let pod = pod_identity("app-pod");
        let cm = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("config_map"),
            "default/shared-cfg",
        );

        // First observation: envFrom ConfigMap
        let ev1 = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            pod.clone(),
            json!({
                "reference_type": "config_map_ref",
                "target": {
                    "provider": "kubernetes",
                    "resource_type": "config_map",
                    "provider_id": "default/shared-cfg"
                }
            }),
        );

        // Second observation: volume ConfigMap
        let ev2 = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::RESOURCE_REFERENCE,
            pod.clone(),
            json!({
                "reference_type": "config_map_ref",
                "target": {
                    "provider": "kubernetes",
                    "resource_type": "config_map",
                    "provider_id": "default/shared-cfg"
                }
            }),
        );

        let rule = ResourceReferenceRule;
        let results = rule.apply(&[ev1.clone(), ev2.clone()]);

        assert_eq!(results.len(), 1);
        let disc = results[0]
            .discovered()
            .expect("expected discovered relationship");
        assert_eq!(disc.relationship.source, pod);
        assert_eq!(disc.relationship.target, cm);
        assert_eq!(disc.relationship.kind, RelationshipKind::DEPENDS_ON);
        assert_eq!(disc.supporting_evidence.len(), 2);
        assert!(disc.supporting_evidence.contains(&ev1.id));
        assert!(disc.supporting_evidence.contains(&ev2.id));
    }

    // -----------------------------------------------------------------------
    // 40. Negative Test: Service selector does NOT generate CALLS or DEPENDS_ON
    // -----------------------------------------------------------------------
    #[test]
    fn test_negative_service_selector_does_not_create_relationships() {
        let svc = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("service"),
            "default/frontend-svc",
        );
        let pod = ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("pod"),
            "default/frontend-pod",
        );

        // Service CONFIGURATION with selector
        let svc_ev = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::CONFIGURATION,
            svc.clone(),
            json!({
                "selector": { "app": "frontend" },
                "type": "ClusterIP"
            }),
        );

        // Pod CONFIGURATION with matching labels
        let pod_ev = Evidence::new(
            k8s_source(),
            Utc::now(),
            ObservationType::CONFIGURATION,
            pod.clone(),
            json!({
                "labels": { "app": "frontend" }
            }),
        );

        let engine = DiscoveryEngine::default_v2();
        let results = engine.run(&[svc_ev, pod_ev]);

        // Zero relationships should be derived from matching selectors/labels alone!
        assert!(
            results.is_empty(),
            "architectural invariant violated: relationships derived merely from label selector matching"
        );
    }
}
