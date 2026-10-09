//! WhatBreaks Impact Engine v1
//!
//! A lightweight, deterministic, in-memory impact analysis layer operating on
//! [`Graph`](crate::graph::Graph), [`Traversal`](crate::traversal::Traversal),
//! and [`ResourceIdentity`](crate::resource::ResourceIdentity).
//!
//! # Architecture
//!
//! ```text
//! ResourceIdentity (Nodes)
//!         │
//!         ▼
//!    Relationship (Edges: source --kind--> target)
//!         │
//!         ▼
//!      Evidence (Factual observations)
//!         │
//!         ▼
//!     Discovery (Evidence-backed relationship derivation)
//!         │
//!         ▼
//!       Graph (Graph v1 — Directional Storage & Indexing)
//!         │
//!         ▼
//!     Traversal (Traversal v1 — Directional Reachability & Hop Depth)
//!         │
//!         ▼
//!      Impact (Impact v1 — Candidate Blast Radius & Policy Enforcement)
//!         │
//!         ▼
//!   "What Breaks?" (Future Analysis & Visualization)
//! ```
//!
//! # Core Semantic Distinction
//!
//! - **Traversal** answers: *"Which resources are reachable?"*
//! - **Impact** answers: *"Which reachable resources are candidates for being affected?"*
//!
//! Impact v1 does NOT expose raw traversal results. It applies an explicit
//! impact-propagation policy to filter relationship edges during traversal.
//! Non-propagating relationship kinds act as traversal boundaries and are
//! never traversed.
//!
//! # Impact Propagation Policy (v1)
//!
//! The following relationship kinds propagate runtime impact:
//! - [`RelationshipKind::DEPENDS_ON`] (`A depends on B` -> failure in B affects A)
//! - [`RelationshipKind::CALLS`] (`A calls B` -> failure in B affects A)
//! - [`RelationshipKind::READS_FROM`] (`A reads from B` -> failure in B affects A)
//! - [`RelationshipKind::WRITES_TO`] (`A writes to B` -> failure in B affects A)
//!
//! The following relationship kinds do NOT propagate runtime impact in v1:
//! - [`RelationshipKind::OWNS`] (lifecycle/management ownership boundary)
//! - [`RelationshipKind::RUNS_ON`] (execution placement boundary)
//! - [`RelationshipKind::AUTHORIZES`] (security/policy boundary)
//! - Any unmapped custom relationship kinds
//!
//! # Invariants & Boundaries
//!
//! - **Candidate Blast Radius**: Computes reachable candidate affected resources based
//!   on known relationships and propagation policy. Does NOT compute certainty, probability,
//!   confidence, or risk scores.
//! - **Target Separation**: The target resource being analyzed is never included in the
//!   `impacted` resource list, even in the presence of self-loops.
//! - **Directional**: Accepts explicit [`TraversalDirection`]. The standard direction for
//!   failure/change blast-radius analysis is [`TraversalDirection::Incoming`].
//! - **Read-Only**: Borrows the [`Graph`] immutably and never mutates graph state.
//! - **Deterministic**: Results are strictly ordered by depth ascending, and canonical
//!   `(provider, resource_type, provider_id)` order within the same depth.
//! - **Cycle & Self-Loop Safe**: Uses visited tracking to prevent cycles and termination issues.
//! - **No External Dependencies**: Pure in-memory Rust code with no database, networking,
//!   or async runtime.

use crate::graph::Graph;
use crate::relationship::RelationshipKind;
use crate::resource::ResourceIdentity;
use crate::traversal::{Traversal, TraversalDirection};

// ---------------------------------------------------------------------------
// Impact Propagation Policy
// ---------------------------------------------------------------------------

/// Check if a relationship kind propagates impact in Impact Engine v1.
///
/// Impact-propagating kinds in v1:
/// - [`RelationshipKind::DEPENDS_ON`]
/// - [`RelationshipKind::CALLS`]
/// - [`RelationshipKind::READS_FROM`]
/// - [`RelationshipKind::WRITES_TO`]
///
/// Non-propagating kinds in v1:
/// - [`RelationshipKind::OWNS`]
/// - [`RelationshipKind::RUNS_ON`]
/// - [`RelationshipKind::AUTHORIZES`]
/// - (any unknown or custom kinds)
#[inline]
pub fn propagates_impact(kind: &RelationshipKind) -> bool {
    matches!(
        kind.as_str(),
        "DEPENDS_ON" | "CALLS" | "READS_FROM" | "WRITES_TO"
    )
}

// ---------------------------------------------------------------------------
// Change Intent & Assessment Types
// ---------------------------------------------------------------------------

/// Strongly validated change types supported for change-aware impact analysis in v1.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub enum ChangeType {
    /// Target resource is being deleted or permanently decommissioned.
    Delete,
    /// Target resource is undergoing configuration or attribute modification.
    Update,
    /// Target resource is undergoing capacity, replica count, or throughput adjustment.
    Scale,
    /// Target resource is being destroyed and recreated / replaced.
    Replace,
}

impl ChangeType {
    /// Return the canonical string representation of the change type.
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Delete => "DELETE",
            Self::Update => "UPDATE",
            Self::Scale => "SCALE",
            Self::Replace => "REPLACE",
        }
    }

    /// Parse a change type string strictly, rejecting unsupported or unknown values.
    pub fn from_str_strict(s: &str) -> Option<Self> {
        match s.trim().to_ascii_uppercase().as_str() {
            "DELETE" => Some(Self::Delete),
            "UPDATE" => Some(Self::Update),
            "SCALE" => Some(Self::Scale),
            "REPLACE" => Some(Self::Replace),
            _ => None,
        }
    }
}

/// Proposed change intent specified by the caller.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProposedChange {
    /// The specific change type.
    pub change_type: ChangeType,
    /// Optional change details or metadata.
    pub details: Option<String>,
}

impl ProposedChange {
    /// Create a new `ProposedChange` for the given change type.
    pub fn new(change_type: ChangeType) -> Self {
        Self {
            change_type,
            details: None,
        }
    }

    /// Create a new `ProposedChange` with specific details.
    pub fn with_details(change_type: ChangeType, details: impl Into<String>) -> Self {
        Self {
            change_type,
            details: Some(details.into()),
        }
    }

    /// Evaluates whether this change type propagates across a given relationship kind.
    ///
    /// Propagation Policy (v1):
    /// - DELETE: propagates across DEPENDS_ON, CALLS, READS_FROM, WRITES_TO.
    /// - UPDATE: propagates across DEPENDS_ON, CALLS, READS_FROM, WRITES_TO (up to max depth 1).
    /// - SCALE: propagates ONLY across CALLS (runtime network/invocation traffic). Suppresses declarative DEPENDS_ON.
    /// - REPLACE: propagates across DEPENDS_ON, CALLS, READS_FROM, WRITES_TO (up to max depth 1).
    ///
    /// Non-propagating boundary kinds (OWNS, RUNS_ON, AUTHORIZES) are NEVER traversed for any change.
    pub fn propagates(&self, kind: &RelationshipKind) -> bool {
        match self.change_type {
            ChangeType::Delete => propagates_impact(kind),
            ChangeType::Update => propagates_impact(kind),
            ChangeType::Scale => matches!(kind.as_str(), "CALLS"),
            ChangeType::Replace => propagates_impact(kind),
        }
    }

    /// Returns the effective maximum traversal depth under this change policy.
    ///
    /// Traversal Depth Policy (v1):
    /// - DELETE: Full requested depth (multi-hop transitive blast radius).
    /// - UPDATE: Restricted to depth 1 (direct dependents only; indirect propagation suppressed without key-level semantic diffs).
    /// - SCALE: Restricted to depth 1 (direct callers only).
    /// - REPLACE: Restricted to depth 1 (transient rollover window affecting direct connections only).
    pub fn effective_max_depth(&self, requested_depth: usize) -> usize {
        match self.change_type {
            ChangeType::Delete => requested_depth,
            ChangeType::Update => requested_depth.min(1),
            ChangeType::Scale => requested_depth.min(1),
            ChangeType::Replace => requested_depth.min(1),
        }
    }

    /// Factual explanation of why a resource is impacted at a specific depth and relationship kind.
    pub fn impact_reason(&self, kind: Option<&str>, depth: usize) -> String {
        let kind_str = kind.unwrap_or("relationship");
        match (self.change_type, depth) {
            (ChangeType::Delete, 1) => {
                format!(
                    "Direct dependent via {}: target resource is proposed for deletion",
                    kind_str
                )
            }
            (ChangeType::Delete, d) => {
                format!(
                    "Indirect dependent at depth {}: transitively affected by target deletion",
                    d
                )
            }
            (ChangeType::Update, 1) => {
                format!(
                    "Direct dependent via {}: may require reload or restart to consume updated target state",
                    kind_str
                )
            }
            (ChangeType::Update, d) => {
                format!(
                    "Indirect dependent at depth {}: potential transitive update impact",
                    d
                )
            }
            (ChangeType::Scale, 1) => {
                format!(
                    "Direct runtime caller via {}: subject to traffic capacity, latency, or concurrency changes",
                    kind_str
                )
            }
            (ChangeType::Scale, d) => {
                format!(
                    "Indirect runtime caller at depth {}: potential downstream concurrency impact",
                    d
                )
            }
            (ChangeType::Replace, 1) => {
                format!(
                    "Direct connection via {}: subject to transient rollover or reconnection during replacement",
                    kind_str
                )
            }
            (ChangeType::Replace, d) => {
                format!(
                    "Indirect connection at depth {}: potential transitive rollover impact",
                    d
                )
            }
        }
    }

    /// Return a depth-based impact reason when relationship kind is not yet resolved.
    pub fn impact_reason_for_depth(&self, depth: usize) -> String {
        self.impact_reason(None, depth)
    }

    /// High-level assessment of the nature of the impact.
    pub fn impact_nature(&self) -> &'static str {
        "POTENTIAL_IMPACT"
    }

    /// Assumptions underlying this change assessment in v1.
    pub fn assumptions(&self) -> Vec<String> {
        match self.change_type {
            ChangeType::Delete => vec![
                "Target resource removal disrupts dependents relying on its declared presence or runtime availability.".to_string(),
                "Cascading transitive failures assume no graceful degradation or fallback in upstream dependents unless verified.".to_string(),
            ],
            ChangeType::Update => vec![
                "Target update modifies configuration or schema; direct consumers may require reconciliation or restart.".to_string(),
                "Transitive multi-hop propagation is suppressed in v1 due to absence of field-level change diffs.".to_string(),
            ],
            ChangeType::Scale => vec![
                "Target scale event alters runtime throughput and concurrency without altering resource interface contracts.".to_string(),
                "Only direct runtime CALLS edges are affected; static configuration dependencies remain intact.".to_string(),
            ],
            ChangeType::Replace => vec![
                "Target replacement implies temporary disruption during teardown and recreation of the resource instance.".to_string(),
                "Interface identity is preserved post-rollover; long-term multi-hop cascade is not assumed.".to_string(),
            ],
        }
    }

    /// Known limitations for this change assessment in v1.
    pub fn limitations(&self) -> Vec<String> {
        match self.change_type {
            ChangeType::Delete => vec![
                "Does not evaluate client retry policies, circuit breakers, or redundant failover replicas.".to_string(),
                "Assumes binary availability; does not model partial or graceful degradation.".to_string(),
            ],
            ChangeType::Update => vec![
                "Does not perform schema or key-level diffing of the updated resource content.".to_string(),
                "Multi-hop propagation beyond depth 1 is not modeled in v1 without semantic change payloads.".to_string(),
            ],
            ChangeType::Scale => vec![
                "Does not evaluate pod replica counts, autoscale thresholds, or specific load profiles.".to_string(),
                "Static/declarative dependencies (DEPENDS_ON) are excluded; only runtime invocation edges (CALLS) are evaluated.".to_string(),
            ],
            ChangeType::Replace => vec![
                "Does not verify rolling update parameters (maxSurge/maxUnavailable) or zero-downtime mechanisms.".to_string(),
                "Impact is limited to direct connections during the replacement transition window.".to_string(),
            ],
        }
    }
}

/// Deterministic assessment of a proposed change's nature, assumptions, and limitations.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ChangeAssessment {
    /// The change type that was evaluated.
    pub change_type: ChangeType,
    /// High-level assessment nature (e.g. "POTENTIAL_IMPACT").
    pub impact_nature: String,
    /// Explicit assumptions underlying the assessment.
    pub assumptions: Vec<String>,
    /// Known limitations of the v1 analysis for this change.
    pub limitations: Vec<String>,
}

impl ChangeAssessment {
    /// Create a `ChangeAssessment` from a `ProposedChange`.
    pub fn from_proposed_change(change: &ProposedChange) -> Self {
        Self {
            change_type: change.change_type,
            impact_nature: change.impact_nature().to_string(),
            assumptions: change.assumptions(),
            limitations: change.limitations(),
        }
    }

    /// Create a `ChangeAssessment` tailored to the specific graph analysis context and outcome.
    pub fn for_analysis(
        change: &ProposedChange,
        graph: &Graph,
        target: &ResourceIdentity,
        direction: TraversalDirection,
        impacted_count: usize,
    ) -> Self {
        let mut assessment = Self::from_proposed_change(change);

        if impacted_count == 0 {
            if !graph.contains_resource(target) {
                assessment.limitations.push(
                    "Target resource is not observed in the graph; impact cannot be identified from current topology.".to_string(),
                );
            } else {
                let adjacent_rels = match direction {
                    TraversalDirection::Incoming => graph.get_relationships_to(target),
                    TraversalDirection::Outgoing => graph.get_relationships_from(target),
                };

                match change.change_type {
                    ChangeType::Scale => {
                        let has_declarative = adjacent_rels.iter().any(|r| {
                            matches!(r.kind.as_str(), "DEPENDS_ON" | "READS_FROM" | "WRITES_TO")
                        });
                        if has_declarative {
                            assessment.limitations.push(
                                "Target resource has declarative dependencies (e.g. DEPENDS_ON), but no runtime CALLS relationships were observed. Under the v1 SCALE policy, declarative dependencies are excluded from scaling impact; zero identified impacts reflects an absence of qualifying runtime caller evidence in the current model, not confirmed absence of operational scaling consequences.".to_string(),
                            );
                        } else {
                            assessment.limitations.push(
                                "No runtime caller (CALLS) relationships or evidence were observed for the target resource. Zero identified impacts reflects an absence of caller evidence in the current model, not confirmed absence of scaling consequences.".to_string(),
                            );
                        }
                    }
                    ChangeType::Delete => {
                        assessment.limitations.push(
                            "No qualifying incoming relationships (DEPENDS_ON, CALLS, READS_FROM, WRITES_TO) were observed for the target resource. Zero identified impacts reflects an absence of dependent relationships in the current graph model, not a guarantee that deletion will have no external impact.".to_string(),
                        );
                    }
                    ChangeType::Update => {
                        assessment.limitations.push(
                            "No qualifying direct dependents (depth 1) were observed for the target resource. Zero identified impacts reflects an absence of direct dependency relationships in the current graph model, not a guarantee that configuration changes will have no operational effect.".to_string(),
                        );
                    }
                    ChangeType::Replace => {
                        assessment.limitations.push(
                            "No qualifying direct connections (depth 1) were observed for the target resource. Zero identified impacts reflects an absence of direct connection relationships in the current graph model during the replacement transition window.".to_string(),
                        );
                    }
                }
            }
        }

        assessment
    }
}

// ---------------------------------------------------------------------------
// ImpactRequest
// ---------------------------------------------------------------------------

/// A request to compute the candidate blast radius for a target resource.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ImpactRequest {
    /// The resource that is changing, failing, being removed, or analyzed.
    pub target: ResourceIdentity,
    /// Direction of relationship traversal.
    ///
    /// For failure/change blast-radius analysis, the standard direction is
    /// [`TraversalDirection::Incoming`], discovering upstream dependents.
    pub direction: TraversalDirection,
    /// Maximum relationship-hop depth for impact analysis.
    pub max_depth: usize,
    /// Optional proposed change intent for change-aware analysis.
    pub proposed_change: Option<ProposedChange>,
}

impl ImpactRequest {
    /// Construct a new `ImpactRequest`.
    pub fn new(target: ResourceIdentity, direction: TraversalDirection, max_depth: usize) -> Self {
        Self {
            target,
            direction,
            max_depth,
            proposed_change: None,
        }
    }

    /// Construct a standard incoming blast-radius request for `target` up to `max_depth`.
    pub fn incoming(target: ResourceIdentity, max_depth: usize) -> Self {
        Self::new(target, TraversalDirection::Incoming, max_depth)
    }

    /// Construct an outgoing impact request for `target` up to `max_depth`.
    pub fn outgoing(target: ResourceIdentity, max_depth: usize) -> Self {
        Self::new(target, TraversalDirection::Outgoing, max_depth)
    }

    /// Set a proposed change on this request.
    pub fn with_proposed_change(mut self, change: ProposedChange) -> Self {
        self.proposed_change = Some(change);
        self
    }
}

// ---------------------------------------------------------------------------
// ImpactedResource
// ---------------------------------------------------------------------------

/// A resource identified as a candidate for being affected by a change/failure in the target.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ImpactedResource {
    /// The identity of the impacted resource.
    pub resource: ResourceIdentity,
    /// Minimum relationship-hop distance from the target along impact-propagating edges.
    /// Always >= 1. The target itself is never included in `impacted`.
    pub depth: usize,
    /// Impact classification: "DIRECT" (depth = 1) or "INDIRECT" (depth > 1).
    pub impact_type: Option<String>,
    /// Factual explanation of why this resource is impacted under the proposed change.
    pub impact_reason: Option<String>,
}

impl ImpactedResource {
    /// Construct a new `ImpactedResource`.
    pub fn new(resource: ResourceIdentity, depth: usize) -> Self {
        Self {
            resource,
            depth,
            impact_type: None,
            impact_reason: None,
        }
    }

    /// Construct a new `ImpactedResource` with impact type and reason.
    pub fn with_details(
        resource: ResourceIdentity,
        depth: usize,
        impact_type: impl Into<String>,
        impact_reason: impl Into<String>,
    ) -> Self {
        Self {
            resource,
            depth,
            impact_type: Some(impact_type.into()),
            impact_reason: Some(impact_reason.into()),
        }
    }
}

// ---------------------------------------------------------------------------
// ImpactResult
// ---------------------------------------------------------------------------

/// The result of an impact analysis, detailing the candidate blast radius.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ImpactResult {
    /// The target resource that was analyzed.
    pub target: ResourceIdentity,
    /// All candidate impacted resources, ordered by depth ascending and canonical identity.
    /// The target resource itself is NEVER present in `impacted`.
    pub impacted: Vec<ImpactedResource>,
}

impl ImpactResult {
    /// Construct a new `ImpactResult`.
    pub fn new(target: ResourceIdentity, impacted: Vec<ImpactedResource>) -> Self {
        Self { target, impacted }
    }

    /// Return the number of candidate impacted resources.
    pub fn len(&self) -> usize {
        self.impacted.len()
    }

    /// Return `true` if no resources are candidate impacted.
    pub fn is_empty(&self) -> bool {
        self.impacted.is_empty()
    }

    /// Return an iterator over the candidate impacted resources.
    pub fn iter(&self) -> std::slice::Iter<'_, ImpactedResource> {
        self.impacted.iter()
    }

    /// Return a slice of the candidate impacted resources.
    pub fn as_slice(&self) -> &[ImpactedResource] {
        &self.impacted
    }

    /// Return all impacted resource identities in depth/canonical order.
    pub fn resources(&self) -> Vec<ResourceIdentity> {
        self.impacted.iter().map(|i| i.resource.clone()).collect()
    }

    /// Check if a specific resource identity is present in the blast radius.
    pub fn contains(&self, resource: &ResourceIdentity) -> bool {
        self.impacted.iter().any(|i| &i.resource == resource)
    }

    /// Return the hop depth of a specific resource if it is in the blast radius.
    pub fn depth_of(&self, resource: &ResourceIdentity) -> Option<usize> {
        self.impacted
            .iter()
            .find(|i| &i.resource == resource)
            .map(|i| i.depth)
    }
}

impl IntoIterator for ImpactResult {
    type Item = ImpactedResource;
    type IntoIter = std::vec::IntoIter<ImpactedResource>;

    fn into_iter(self) -> Self::IntoIter {
        self.impacted.into_iter()
    }
}

impl<'a> IntoIterator for &'a ImpactResult {
    type Item = &'a ImpactedResource;
    type IntoIter = std::slice::Iter<'a, ImpactedResource>;

    fn into_iter(self) -> Self::IntoIter {
        self.impacted.iter()
    }
}

// ---------------------------------------------------------------------------
// Impact Engine
// ---------------------------------------------------------------------------

/// Stateless, deterministic impact analysis engine.
#[derive(Debug, Clone, Copy, Default)]
pub struct ImpactEngine;

impl ImpactEngine {
    /// Analyze candidate impact / blast radius for `request.target` in `graph`.
    ///
    /// # Semantics
    /// - Operates strictly on [`ResourceIdentity`].
    /// - If `request.target` is not present in `graph`, returns `ImpactResult` with empty `impacted`.
    /// - `max_depth = 0` returns `ImpactResult` with empty `impacted`.
    /// - Follows edges strictly according to `request.direction`.
    /// - Only traverses edges whose kind satisfies [`propagates_impact`].
    ///   Non-propagating edges (such as `OWNS`, `RUNS_ON`, `AUTHORIZES`) act as hard boundaries.
    /// - Traversal uses minimum hop BFS with visited tracking. Cycles and self-loops terminate cleanly.
    /// - `request.target` is NEVER included in `impacted`.
    /// - Resources discovered at the same depth are ordered deterministically by
    ///   canonical `(provider, resource_type, provider_id)`.
    /// - Does not mutate `graph`.
    pub fn analyze(graph: &Graph, request: &ImpactRequest) -> ImpactResult {
        let effective_depth = if let Some(ref change) = request.proposed_change {
            change.effective_max_depth(request.max_depth)
        } else {
            request.max_depth
        };

        let traversal_result = Traversal::traverse_filtered(
            graph,
            &request.target,
            request.direction,
            effective_depth,
            |rel| {
                if let Some(ref change) = request.proposed_change {
                    change.propagates(&rel.kind)
                } else {
                    propagates_impact(&rel.kind)
                }
            },
        );

        let impacted: Vec<ImpactedResource> = traversal_result
            .nodes
            .into_iter()
            .filter(|node| node.depth > 0)
            .map(|node| {
                if let Some(ref change) = request.proposed_change {
                    let impact_type = if node.depth == 1 {
                        "DIRECT"
                    } else {
                        "INDIRECT"
                    };
                    let impact_reason = change.impact_reason_for_depth(node.depth);
                    ImpactedResource::with_details(
                        node.resource,
                        node.depth,
                        impact_type,
                        impact_reason,
                    )
                } else {
                    ImpactedResource::new(node.resource, node.depth)
                }
            })
            .collect();

        ImpactResult::new(request.target.clone(), impacted)
    }

    /// Check if a relationship kind propagates impact according to the v1 policy.
    pub fn propagates(kind: &RelationshipKind) -> bool {
        propagates_impact(kind)
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::relationship::{Relationship, RelationshipKind};
    use crate::resource::{Provider, ResourceKind};

    fn make_identity(name: &str) -> ResourceIdentity {
        ResourceIdentity::new(Provider::new("kubernetes"), ResourceKind::new("pod"), name)
    }

    fn make_custom_identity(provider: &str, kind: &str, id: &str) -> ResourceIdentity {
        ResourceIdentity::new(Provider::new(provider), ResourceKind::new(kind), id)
    }

    fn make_rel(src: &str, tgt: &str, kind: RelationshipKind) -> Relationship {
        Relationship::new(make_identity(src), make_identity(tgt), kind)
    }

    // -----------------------------------------------------------------------
    // 1. Empty graph
    // -----------------------------------------------------------------------
    #[test]
    fn test_empty_graph() {
        let g = Graph::new();
        let target = make_identity("target");
        let req = ImpactRequest::incoming(target.clone(), 5);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.target, target);
        assert!(res.is_empty());
        assert_eq!(res.len(), 0);
        assert!(res.impacted.is_empty());
    }

    // -----------------------------------------------------------------------
    // 2. Unknown target
    // -----------------------------------------------------------------------
    #[test]
    fn test_unknown_target() {
        let mut g = Graph::new();
        g.add_resource(make_identity("existing"));

        let unknown = make_identity("unknown");
        let req = ImpactRequest::incoming(unknown.clone(), 5);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.target, unknown);
        assert!(res.is_empty());
        assert_eq!(res.len(), 0);
    }

    // -----------------------------------------------------------------------
    // 3. Target is not included in impacted
    // -----------------------------------------------------------------------
    #[test]
    fn test_target_is_not_included_in_impacted() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));

        let b = make_identity("b");
        let req = ImpactRequest::incoming(b.clone(), 5);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.target, b);
        assert!(!res.contains(&b));
        assert_eq!(res.depth_of(&b), None);
        assert_eq!(res.len(), 1);
        assert_eq!(res.impacted[0].resource, make_identity("a"));
    }

    // -----------------------------------------------------------------------
    // 4. Direct DEPENDS_ON impact
    // -----------------------------------------------------------------------
    #[test]
    fn test_direct_depends_on_impact() {
        let mut g = Graph::new();
        // A depends on B. B fails -> A is impacted.
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));

        let req = ImpactRequest::incoming(make_identity("b"), 1);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 1);
        assert_eq!(
            res.impacted[0],
            ImpactedResource::new(make_identity("a"), 1)
        );
    }

    // -----------------------------------------------------------------------
    // 5. Direct CALLS impact
    // -----------------------------------------------------------------------
    #[test]
    fn test_direct_calls_impact() {
        let mut g = Graph::new();
        // A calls B. B fails -> A is impacted.
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));

        let req = ImpactRequest::incoming(make_identity("b"), 1);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 1);
        assert_eq!(
            res.impacted[0],
            ImpactedResource::new(make_identity("a"), 1)
        );
    }

    // -----------------------------------------------------------------------
    // 6. Direct READS_FROM impact
    // -----------------------------------------------------------------------
    #[test]
    fn test_direct_reads_from_impact() {
        let mut g = Graph::new();
        // A reads from B. B fails -> A is impacted.
        g.add_relationship(make_rel("a", "b", RelationshipKind::READS_FROM));

        let req = ImpactRequest::incoming(make_identity("b"), 1);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 1);
        assert_eq!(
            res.impacted[0],
            ImpactedResource::new(make_identity("a"), 1)
        );
    }

    // -----------------------------------------------------------------------
    // 7. Direct WRITES_TO impact
    // -----------------------------------------------------------------------
    #[test]
    fn test_direct_writes_to_impact() {
        let mut g = Graph::new();
        // A writes to B. B fails -> A is impacted.
        g.add_relationship(make_rel("a", "b", RelationshipKind::WRITES_TO));

        let req = ImpactRequest::incoming(make_identity("b"), 1);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 1);
        assert_eq!(
            res.impacted[0],
            ImpactedResource::new(make_identity("a"), 1)
        );
    }

    // -----------------------------------------------------------------------
    // 8. OWNS does not propagate
    // -----------------------------------------------------------------------
    #[test]
    fn test_owns_does_not_propagate() {
        let mut g = Graph::new();
        // Admin owns API. API fails -> Admin is NOT automatically in runtime blast radius.
        g.add_relationship(make_rel("admin", "api", RelationshipKind::OWNS));

        let req = ImpactRequest::incoming(make_identity("api"), 1);
        let res = ImpactEngine::analyze(&g, &req);

        assert!(res.is_empty());
        assert!(!res.contains(&make_identity("admin")));
    }

    // -----------------------------------------------------------------------
    // 9. RUNS_ON does not propagate
    // -----------------------------------------------------------------------
    #[test]
    fn test_runs_on_does_not_propagate() {
        let mut g = Graph::new();
        // Node runs API. API fails -> Node is NOT in runtime blast radius.
        g.add_relationship(make_rel("node", "api", RelationshipKind::RUNS_ON));

        let req = ImpactRequest::incoming(make_identity("api"), 1);
        let res = ImpactEngine::analyze(&g, &req);

        assert!(res.is_empty());
        assert!(!res.contains(&make_identity("node")));
    }

    // -----------------------------------------------------------------------
    // 10. AUTHORIZES does not propagate
    // -----------------------------------------------------------------------
    #[test]
    fn test_authorizes_does_not_propagate() {
        let mut g = Graph::new();
        // Role authorizes Service. Service fails -> Role is NOT in runtime blast radius.
        g.add_relationship(make_rel("role", "service", RelationshipKind::AUTHORIZES));

        let req = ImpactRequest::incoming(make_identity("service"), 1);
        let res = ImpactEngine::analyze(&g, &req);

        assert!(res.is_empty());
        assert!(!res.contains(&make_identity("role")));
    }

    // -----------------------------------------------------------------------
    // 11. Multi-level impact
    // -----------------------------------------------------------------------
    #[test]
    fn test_multi_level_impact() {
        let mut g = Graph::new();
        // Frontend -> API -> Database
        g.add_relationship(make_rel("frontend", "api", RelationshipKind::CALLS));
        g.add_relationship(make_rel("api", "database", RelationshipKind::DEPENDS_ON));

        let req = ImpactRequest::incoming(make_identity("database"), 2);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 2);
        assert_eq!(
            res.impacted[0],
            ImpactedResource::new(make_identity("api"), 1)
        );
        assert_eq!(
            res.impacted[1],
            ImpactedResource::new(make_identity("frontend"), 2)
        );
    }

    // -----------------------------------------------------------------------
    // 12. max_depth = 0
    // -----------------------------------------------------------------------
    #[test]
    fn test_max_depth_zero() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));

        let req = ImpactRequest::incoming(make_identity("b"), 0);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.target, make_identity("b"));
        assert!(res.is_empty());
        assert_eq!(res.len(), 0);
    }

    // -----------------------------------------------------------------------
    // 13. max_depth = 1
    // -----------------------------------------------------------------------
    #[test]
    fn test_max_depth_one() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("c", "b", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("b", "a", RelationshipKind::DEPENDS_ON));

        let req = ImpactRequest::incoming(make_identity("a"), 1);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 1);
        assert_eq!(res.impacted[0].resource, make_identity("b"));
        assert_eq!(res.impacted[0].depth, 1);
        assert!(!res.contains(&make_identity("c")));
    }

    // -----------------------------------------------------------------------
    // 14. max_depth limiting
    // -----------------------------------------------------------------------
    #[test]
    fn test_max_depth_limiting() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("d", "c", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("c", "b", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("b", "a", RelationshipKind::DEPENDS_ON));

        let req = ImpactRequest::incoming(make_identity("a"), 2);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 2);
        assert_eq!(res.depth_of(&make_identity("b")), Some(1));
        assert_eq!(res.depth_of(&make_identity("c")), Some(2));
        assert_eq!(res.depth_of(&make_identity("d")), None);
    }

    // -----------------------------------------------------------------------
    // 15. Cycle termination
    // -----------------------------------------------------------------------
    #[test]
    fn test_cycle_termination() {
        let mut g = Graph::new();
        // A -> B -> C -> A
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("b", "c", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("c", "a", RelationshipKind::DEPENDS_ON));

        let req = ImpactRequest::incoming(make_identity("a"), 10);
        let res = ImpactEngine::analyze(&g, &req);

        // Incoming from A: C (incoming to A: C->A) depth 1, B (incoming to C: B->C) depth 2.
        // A must not reappear.
        assert_eq!(res.len(), 2);
        assert_eq!(
            res.impacted[0],
            ImpactedResource::new(make_identity("c"), 1)
        );
        assert_eq!(
            res.impacted[1],
            ImpactedResource::new(make_identity("b"), 2)
        );
        assert!(!res.contains(&make_identity("a")));
    }

    // -----------------------------------------------------------------------
    // 16. Self-loop handling
    // -----------------------------------------------------------------------
    #[test]
    fn test_self_loop_handling() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "a", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("b", "a", RelationshipKind::DEPENDS_ON));

        let req = ImpactRequest::incoming(make_identity("a"), 5);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.target, make_identity("a"));
        assert_eq!(res.len(), 1);
        assert_eq!(
            res.impacted[0],
            ImpactedResource::new(make_identity("b"), 1)
        );
        assert!(!res.contains(&make_identity("a")));
    }

    // -----------------------------------------------------------------------
    // 17. Diamond deduplication
    // -----------------------------------------------------------------------
    #[test]
    fn test_diamond_deduplication() {
        let mut g = Graph::new();
        //     Top
        //    /   \
        //   B     C
        //    \   /
        //    Target
        g.add_relationship(make_rel("b", "target", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("c", "target", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("top", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("top", "c", RelationshipKind::CALLS));

        let req = ImpactRequest::incoming(make_identity("target"), 5);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 3);
        assert_eq!(res.depth_of(&make_identity("b")), Some(1));
        assert_eq!(res.depth_of(&make_identity("c")), Some(1));
        assert_eq!(res.depth_of(&make_identity("top")), Some(2));
    }

    // -----------------------------------------------------------------------
    // 18. Multiple relationship kinds between same endpoints
    // -----------------------------------------------------------------------
    #[test]
    fn test_multiple_relationship_kinds_between_same_endpoints() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));

        let req = ImpactRequest::incoming(make_identity("b"), 2);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 1);
        assert_eq!(
            res.impacted[0],
            ImpactedResource::new(make_identity("a"), 1)
        );
    }

    // -----------------------------------------------------------------------
    // 19. Non-propagating relationship blocks traversal (boundary test)
    // -----------------------------------------------------------------------
    #[test]
    fn test_non_propagating_relationship_blocks_traversal() {
        let mut g = Graph::new();
        // C --OWNS--> A --DEPENDS_ON--> B
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("c", "a", RelationshipKind::OWNS));

        let req = ImpactRequest::incoming(make_identity("b"), 5);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 1);
        assert_eq!(
            res.impacted[0],
            ImpactedResource::new(make_identity("a"), 1)
        );
        assert!(!res.contains(&make_identity("c")));
    }

    // -----------------------------------------------------------------------
    // 20. Mixed propagating/non-propagating graph
    // -----------------------------------------------------------------------
    #[test]
    fn test_mixed_propagating_non_propagating_graph() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("app", "db", RelationshipKind::READS_FROM));
        g.add_relationship(make_rel("worker", "db", RelationshipKind::WRITES_TO));
        g.add_relationship(make_rel("owner", "db", RelationshipKind::OWNS));
        g.add_relationship(make_rel("host", "db", RelationshipKind::RUNS_ON));

        let req = ImpactRequest::incoming(make_identity("db"), 2);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 2);
        assert!(res.contains(&make_identity("app")));
        assert!(res.contains(&make_identity("worker")));
        assert!(!res.contains(&make_identity("owner")));
        assert!(!res.contains(&make_identity("host")));
    }

    // -----------------------------------------------------------------------
    // 21. Incoming default-style blast-radius example
    // -----------------------------------------------------------------------
    #[test]
    fn test_incoming_default_style_blast_radius_example() {
        let mut g = Graph::new();
        let db = make_custom_identity("aws", "rds", "postgres");
        let api = make_custom_identity("kubernetes", "service", "api");
        let client = make_custom_identity("kubernetes", "deployment", "web");

        g.add_relationship(Relationship::new(
            api.clone(),
            db.clone(),
            RelationshipKind::DEPENDS_ON,
        ));
        g.add_relationship(Relationship::new(
            client.clone(),
            api.clone(),
            RelationshipKind::CALLS,
        ));

        let req = ImpactRequest::incoming(db.clone(), 2);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.target, db);
        assert_eq!(res.len(), 2);
        assert_eq!(res.impacted[0], ImpactedResource::new(api, 1));
        assert_eq!(res.impacted[1], ImpactedResource::new(client, 2));
    }

    // -----------------------------------------------------------------------
    // 22. Explicit outgoing direction
    // -----------------------------------------------------------------------
    #[test]
    fn test_explicit_outgoing_direction() {
        let mut g = Graph::new();
        // A calls B, B calls C
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "c", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "d", RelationshipKind::OWNS)); // Non-propagating

        let req = ImpactRequest::outgoing(make_identity("a"), 2);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.target, make_identity("a"));
        assert_eq!(res.len(), 2);
        assert_eq!(
            res.impacted[0],
            ImpactedResource::new(make_identity("b"), 1)
        );
        assert_eq!(
            res.impacted[1],
            ImpactedResource::new(make_identity("c"), 2)
        );
        assert!(!res.contains(&make_identity("d")));
    }

    // -----------------------------------------------------------------------
    // 23. Disconnected resources excluded
    // -----------------------------------------------------------------------
    #[test]
    fn test_disconnected_resources_excluded() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel(
            "island_1",
            "island_2",
            RelationshipKind::DEPENDS_ON,
        ));

        let req = ImpactRequest::incoming(make_identity("b"), 5);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 1);
        assert!(res.contains(&make_identity("a")));
        assert!(!res.contains(&make_identity("island_1")));
        assert!(!res.contains(&make_identity("island_2")));
    }

    // -----------------------------------------------------------------------
    // 24. Deterministic ordering
    // -----------------------------------------------------------------------
    #[test]
    fn test_deterministic_ordering() {
        let mut g = Graph::new();
        // Insert neighbors out-of-order: z, m, b
        g.add_relationship(make_rel("z", "target", RelationshipKind::CALLS));
        g.add_relationship(make_rel("m", "target", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "target", RelationshipKind::CALLS));

        let req = ImpactRequest::incoming(make_identity("target"), 1);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 3);
        assert_eq!(res.impacted[0].resource.provider_id, "b");
        assert_eq!(res.impacted[1].resource.provider_id, "m");
        assert_eq!(res.impacted[2].resource.provider_id, "z");
    }

    // -----------------------------------------------------------------------
    // 25. Repeated analysis produces identical result
    // -----------------------------------------------------------------------
    #[test]
    fn test_repeated_analysis_produces_identical_result() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "target", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "target", RelationshipKind::CALLS));
        g.add_relationship(make_rel("c", "a", RelationshipKind::CALLS));
        g.add_relationship(make_rel("d", "b", RelationshipKind::CALLS));

        let req = ImpactRequest::incoming(make_identity("target"), 2);
        let run1 = ImpactEngine::analyze(&g, &req);
        let run2 = ImpactEngine::analyze(&g, &req);
        let run3 = ImpactEngine::analyze(&g, &req);

        assert_eq!(run1, run2);
        assert_eq!(run2, run3);
    }

    // -----------------------------------------------------------------------
    // 26. Graph remains unchanged
    // -----------------------------------------------------------------------
    #[test]
    fn test_graph_remains_unchanged() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("b", "c", RelationshipKind::CALLS));

        let initial_resources = g.resources();
        let initial_relationships = g.relationships();
        let initial_res_count = g.resource_count();
        let initial_rel_count = g.relationship_count();

        let req1 = ImpactRequest::incoming(make_identity("c"), 5);
        let _ = ImpactEngine::analyze(&g, &req1);

        let req2 = ImpactRequest::outgoing(make_identity("a"), 5);
        let _ = ImpactEngine::analyze(&g, &req2);

        let req3 = ImpactRequest::incoming(make_identity("missing"), 5);
        let _ = ImpactEngine::analyze(&g, &req3);

        assert_eq!(g.resources(), initial_resources);
        assert_eq!(g.relationships(), initial_relationships);
        assert_eq!(g.resource_count(), initial_res_count);
        assert_eq!(g.relationship_count(), initial_rel_count);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 27. Large/simple dependency chain
    // -----------------------------------------------------------------------
    #[test]
    fn test_large_dependency_chain() {
        let mut g = Graph::new();
        let chain_len = 50;
        for i in 0..chain_len {
            g.add_relationship(make_rel(
                &format!("node_{}", i + 1),
                &format!("node_{}", i),
                RelationshipKind::DEPENDS_ON,
            ));
        }

        let target = make_identity("node_0");
        let req = ImpactRequest::incoming(target.clone(), 25);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 25);
        for i in 1..=25 {
            assert_eq!(res.impacted[i - 1].depth, i);
            assert_eq!(
                res.impacted[i - 1].resource,
                make_identity(&format!("node_{}", i))
            );
        }
    }

    // -----------------------------------------------------------------------
    // 28. Minimum-depth behavior
    // -----------------------------------------------------------------------
    #[test]
    fn test_minimum_depth_behavior() {
        let mut g = Graph::new();
        // Direct path: A -> Target (depth 1)
        // Longer path: A -> Intermediate -> Target (depth 2)
        g.add_relationship(make_rel("a", "target", RelationshipKind::CALLS));
        g.add_relationship(make_rel("inter", "target", RelationshipKind::CALLS));
        g.add_relationship(make_rel("a", "inter", RelationshipKind::CALLS));

        let req = ImpactRequest::incoming(make_identity("target"), 5);
        let res = ImpactEngine::analyze(&g, &req);

        // A must be recorded at minimum depth 1, not 2
        assert_eq!(res.depth_of(&make_identity("a")), Some(1));
        assert_eq!(res.depth_of(&make_identity("inter")), Some(1));
    }

    // -----------------------------------------------------------------------
    // 29. Send + Sync assertions
    // -----------------------------------------------------------------------
    #[test]
    fn test_send_sync_thread_safety() {
        fn assert_send_sync<T: Send + Sync>() {}
        assert_send_sync::<ImpactRequest>();
        assert_send_sync::<ImpactedResource>();
        assert_send_sync::<ImpactResult>();
        assert_send_sync::<ImpactEngine>();
    }

    // -----------------------------------------------------------------------
    // 30. Result helper methods
    // -----------------------------------------------------------------------
    #[test]
    fn test_result_helper_methods() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));

        let req = ImpactRequest::incoming(make_identity("b"), 2);
        let res = ImpactEngine::analyze(&g, &req);

        assert!(!res.is_empty());
        assert_eq!(res.len(), 1);
        assert_eq!(res.as_slice().len(), 1);
        assert_eq!(res.resources(), vec![make_identity("a")]);
        assert!(res.contains(&make_identity("a")));
        assert!(!res.contains(&make_identity("b")));
        assert_eq!(res.depth_of(&make_identity("a")), Some(1));
        assert_eq!(res.depth_of(&make_identity("b")), None);

        // Iteration
        let count = res.iter().count();
        assert_eq!(count, 1);
        let into_count = res.into_iter().count();
        assert_eq!(into_count, 1);
    }

    // -----------------------------------------------------------------------
    // CRITICAL TEST 1: Exact Architecture Specification Test
    //
    // Graph:
    // Frontend ──CALLS──────> API
    // API      ──DEPENDS_ON─> Database
    // Admin    ──OWNS───────> API
    // Node     ──RUNS_ON────> API
    // Role     ──AUTHORIZES─> API
    //
    // Request:
    // target = Database
    // direction = Incoming
    // max_depth = 5
    //
    // Expected impacted resources:
    // API      depth 1
    // Frontend depth 2
    //
    // Expected NOT impacted:
    // Admin
    // Node
    // Role
    // -----------------------------------------------------------------------
    #[test]
    fn test_critical_architecture_blast_radius() {
        let mut g = Graph::new();
        let frontend = make_custom_identity("kubernetes", "deployment", "frontend");
        let api = make_custom_identity("kubernetes", "service", "api");
        let db = make_custom_identity("aws", "rds", "database");
        let admin = make_custom_identity("auth", "user", "admin");
        let node = make_custom_identity("kubernetes", "node", "k8s-node-1");
        let role = make_custom_identity("aws", "iam_role", "api-role");

        g.add_relationship(Relationship::new(
            frontend.clone(),
            api.clone(),
            RelationshipKind::CALLS,
        ));
        g.add_relationship(Relationship::new(
            api.clone(),
            db.clone(),
            RelationshipKind::DEPENDS_ON,
        ));
        g.add_relationship(Relationship::new(
            admin.clone(),
            api.clone(),
            RelationshipKind::OWNS,
        ));
        g.add_relationship(Relationship::new(
            node.clone(),
            api.clone(),
            RelationshipKind::RUNS_ON,
        ));
        g.add_relationship(Relationship::new(
            role.clone(),
            api.clone(),
            RelationshipKind::AUTHORIZES,
        ));

        let req = ImpactRequest::incoming(db.clone(), 5);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.target, db);
        assert_eq!(res.len(), 2);
        assert_eq!(res.impacted[0], ImpactedResource::new(api, 1));
        assert_eq!(res.impacted[1], ImpactedResource::new(frontend, 2));

        assert!(!res.contains(&admin));
        assert!(!res.contains(&node));
        assert!(!res.contains(&role));
    }

    // -----------------------------------------------------------------------
    // CRITICAL TEST 2: Exact Boundary Propagation Test
    //
    // Graph:
    // A ──DEPENDS_ON──> B
    // C ──OWNS────────> A
    // D ──CALLS───────> A
    //
    // Target = B
    // direction = Incoming
    //
    // Expected:
    // A depth 1
    // D depth 2
    //
    // C must NOT appear.
    // -----------------------------------------------------------------------
    #[test]
    fn test_critical_boundary_propagation() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("c", "a", RelationshipKind::OWNS));
        g.add_relationship(make_rel("d", "a", RelationshipKind::CALLS));

        let req = ImpactRequest::incoming(make_identity("b"), 5);
        let res = ImpactEngine::analyze(&g, &req);

        assert_eq!(res.len(), 2);
        assert_eq!(
            res.impacted[0],
            ImpactedResource::new(make_identity("a"), 1)
        );
        assert_eq!(
            res.impacted[1],
            ImpactedResource::new(make_identity("d"), 2)
        );
        assert!(!res.contains(&make_identity("c")));
    }

    // -----------------------------------------------------------------------
    // Change-Aware Impact Analysis v1 Tests
    // -----------------------------------------------------------------------

    #[test]
    fn test_change_type_parsing() {
        assert_eq!(
            ChangeType::from_str_strict("delete"),
            Some(ChangeType::Delete)
        );
        assert_eq!(
            ChangeType::from_str_strict("UPDATE"),
            Some(ChangeType::Update)
        );
        assert_eq!(
            ChangeType::from_str_strict("Scale"),
            Some(ChangeType::Scale)
        );
        assert_eq!(
            ChangeType::from_str_strict("replace"),
            Some(ChangeType::Replace)
        );
        assert_eq!(ChangeType::from_str_strict("INVALID"), None);
        assert_eq!(ChangeType::from_str_strict(""), None);
        assert_eq!(ChangeType::from_str_strict("   "), None);
    }

    #[test]
    fn test_change_aware_delete_multihop() {
        // A -> B -> C -> Target
        let mut g = Graph::new();
        g.add_relationship(make_rel("c", "target", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("b", "c", RelationshipKind::CALLS));
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("owner", "c", RelationshipKind::OWNS));

        let req = ImpactRequest::incoming(make_identity("target"), 5)
            .with_proposed_change(ProposedChange::new(ChangeType::Delete));
        let res = ImpactEngine::analyze(&g, &req);

        // Under DELETE, multi-hop propagation reaches c (depth 1), b (depth 2), a (depth 3)
        assert_eq!(res.len(), 3);
        assert_eq!(res.impacted[0].resource, make_identity("c"));
        assert_eq!(res.impacted[0].depth, 1);
        assert_eq!(res.impacted[0].impact_type.as_deref(), Some("DIRECT"));
        assert_eq!(res.impacted[1].resource, make_identity("b"));
        assert_eq!(res.impacted[1].depth, 2);
        assert_eq!(res.impacted[1].impact_type.as_deref(), Some("INDIRECT"));
        assert_eq!(res.impacted[2].resource, make_identity("a"));
        assert_eq!(res.impacted[2].depth, 3);
        assert_eq!(res.impacted[2].impact_type.as_deref(), Some("INDIRECT"));

        // OWNS boundary must remain strictly non-propagating
        assert!(!res.contains(&make_identity("owner")));
    }

    #[test]
    fn test_change_aware_update_depth_one_limit() {
        // A -> B -> Target
        let mut g = Graph::new();
        g.add_relationship(make_rel("b", "target", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));

        let req = ImpactRequest::incoming(make_identity("target"), 5)
            .with_proposed_change(ProposedChange::new(ChangeType::Update));
        let res = ImpactEngine::analyze(&g, &req);

        // Under UPDATE, propagation is strictly limited to depth 1 in v1
        assert_eq!(res.len(), 1);
        assert_eq!(res.impacted[0].resource, make_identity("b"));
        assert_eq!(res.impacted[0].depth, 1);
        assert_eq!(res.impacted[0].impact_type.as_deref(), Some("DIRECT"));
        assert!(!res.contains(&make_identity("a")));
    }

    #[test]
    fn test_change_aware_scale_calls_only() {
        // caller --CALLS--> target
        // dep --DEPENDS_ON--> target
        // reader --READS_FROM--> target
        // transitive_caller --CALLS--> caller
        let mut g = Graph::new();
        g.add_relationship(make_rel("caller", "target", RelationshipKind::CALLS));
        g.add_relationship(make_rel("dep", "target", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("reader", "target", RelationshipKind::READS_FROM));
        g.add_relationship(make_rel(
            "transitive_caller",
            "caller",
            RelationshipKind::CALLS,
        ));

        let req = ImpactRequest::incoming(make_identity("target"), 5)
            .with_proposed_change(ProposedChange::new(ChangeType::Scale));
        let res = ImpactEngine::analyze(&g, &req);

        // Under SCALE, only direct CALLS edges propagate (depth 1)
        assert_eq!(res.len(), 1);
        assert_eq!(res.impacted[0].resource, make_identity("caller"));
        assert_eq!(res.impacted[0].depth, 1);
        assert_eq!(res.impacted[0].impact_type.as_deref(), Some("DIRECT"));

        // Declarative dependencies (DEPENDS_ON, READS_FROM) and multi-hop callers must NOT be included
        assert!(!res.contains(&make_identity("dep")));
        assert!(!res.contains(&make_identity("reader")));
        assert!(!res.contains(&make_identity("transitive_caller")));
    }

    #[test]
    fn test_change_aware_replace_depth_one_limit() {
        // direct_dep --DEPENDS_ON--> target
        // direct_caller --CALLS--> target
        // indirect_dep --DEPENDS_ON--> direct_dep
        let mut g = Graph::new();
        g.add_relationship(make_rel(
            "direct_dep",
            "target",
            RelationshipKind::DEPENDS_ON,
        ));
        g.add_relationship(make_rel("direct_caller", "target", RelationshipKind::CALLS));
        g.add_relationship(make_rel(
            "indirect_dep",
            "direct_dep",
            RelationshipKind::DEPENDS_ON,
        ));

        let req = ImpactRequest::incoming(make_identity("target"), 5)
            .with_proposed_change(ProposedChange::new(ChangeType::Replace));
        let res = ImpactEngine::analyze(&g, &req);

        // Under REPLACE, only direct connections (depth 1) are included
        assert_eq!(res.len(), 2);
        assert!(res.contains(&make_identity("direct_dep")));
        assert!(res.contains(&make_identity("direct_caller")));
        assert!(!res.contains(&make_identity("indirect_dep")));
        assert_eq!(res.impacted[0].depth, 1);
        assert_eq!(res.impacted[1].depth, 1);
    }

    #[test]
    fn test_change_aware_replace_multihop_and_owns_boundary() {
        // hop3 --DEPENDS_ON--> hop2 --CALLS--> hop1 --DEPENDS_ON--> target
        // owner --OWNS--> target
        let mut g = Graph::new();
        g.add_relationship(make_rel("hop1", "target", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("hop2", "hop1", RelationshipKind::CALLS));
        g.add_relationship(make_rel("hop3", "hop2", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("owner", "target", RelationshipKind::OWNS));

        let req = ImpactRequest::incoming(make_identity("target"), 5)
            .with_proposed_change(ProposedChange::new(ChangeType::Replace));
        let res = ImpactEngine::analyze(&g, &req);

        // Only hop1 is included at depth 1; hop2 and hop3 are suppressed by depth 1 limit.
        // owner is excluded by the non-propagating OWNS boundary.
        assert_eq!(res.len(), 1);
        assert_eq!(res.impacted[0].resource, make_identity("hop1"));
        assert_eq!(res.impacted[0].depth, 1);
        assert_eq!(res.impacted[0].impact_type.as_deref(), Some("DIRECT"));
        assert!(!res.contains(&make_identity("hop2")));
        assert!(!res.contains(&make_identity("hop3")));
        assert!(!res.contains(&make_identity("owner")));
    }

    #[test]
    fn test_change_assessment_scale_zero_impact_with_depends_on() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("dep", "target", RelationshipKind::DEPENDS_ON));

        let change = ProposedChange::new(ChangeType::Scale);
        let assessment = ChangeAssessment::for_analysis(
            &change,
            &g,
            &make_identity("target"),
            TraversalDirection::Incoming,
            0,
        );

        assert_eq!(assessment.change_type, ChangeType::Scale);
        assert_eq!(assessment.impact_nature, "POTENTIAL_IMPACT");
        // Must explain that target has declarative dependencies, but CALLS was not observed
        let has_dep_explanation = assessment.limitations.iter().any(|l| {
            l.contains("declarative dependencies (e.g. DEPENDS_ON)")
                && l.contains("no runtime CALLS relationships were observed")
        });
        assert!(
            has_dep_explanation,
            "limitations: {:?}",
            assessment.limitations
        );
    }

    #[test]
    fn test_change_assessment_scale_zero_impact_with_no_relationships() {
        let mut g = Graph::new();
        g.add_resource(make_identity("target"));

        let change = ProposedChange::new(ChangeType::Scale);
        let assessment = ChangeAssessment::for_analysis(
            &change,
            &g,
            &make_identity("target"),
            TraversalDirection::Incoming,
            0,
        );

        assert_eq!(assessment.change_type, ChangeType::Scale);
        let has_no_caller_explanation = assessment.limitations.iter().any(|l| {
            l.contains("No runtime caller (CALLS) relationships or evidence were observed")
        });
        assert!(
            has_no_caller_explanation,
            "limitations: {:?}",
            assessment.limitations
        );
    }

    #[test]
    fn test_change_assessment_scale_with_calls_has_standard_limitations() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("caller", "target", RelationshipKind::CALLS));

        let change = ProposedChange::new(ChangeType::Scale);
        let assessment = ChangeAssessment::for_analysis(
            &change,
            &g,
            &make_identity("target"),
            TraversalDirection::Incoming,
            1,
        );

        assert_eq!(assessment.change_type, ChangeType::Scale);
        // When impacts > 0, does NOT claim zero identified impacts
        let has_zero_claim = assessment
            .limitations
            .iter()
            .any(|l| l.contains("zero identified impacts"));
        assert!(
            !has_zero_claim,
            "should not claim zero impacts: {:?}",
            assessment.limitations
        );
    }

    #[test]
    fn test_change_assessment_empty_impacts_for_all_change_types() {
        let mut g = Graph::new();
        g.add_resource(make_identity("target"));

        for ct in [ChangeType::Delete, ChangeType::Update, ChangeType::Replace] {
            let change = ProposedChange::new(ct);
            let assessment = ChangeAssessment::for_analysis(
                &change,
                &g,
                &make_identity("target"),
                TraversalDirection::Incoming,
                0,
            );
            assert_eq!(assessment.change_type, ct);
            assert_eq!(assessment.impact_nature, "POTENTIAL_IMPACT");
            assert!(!assessment.assumptions.is_empty());
            assert!(
                assessment
                    .limitations
                    .iter()
                    .any(|l| l.contains("Zero identified impacts")),
                "assessment for {:?} should explain zero impacts: {:?}",
                ct,
                assessment.limitations
            );
        }
    }
}
