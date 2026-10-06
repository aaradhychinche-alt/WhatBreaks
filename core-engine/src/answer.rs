//! WhatBreaks Answer Engine v1
//!
//! A deterministic, domain-level orchestration layer that composes existing core engine results
//! into a structured, explainable, evidence-backed product answer.
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
//!      Evidence (Immutable factual observations)
//!         │
//!         ▼
//!     Discovery (Derives relationships from evidence)
//!         │
//!         ▼
//!       Graph (Directional storage & indexing)
//!         │
//!         ▼
//!     Traversal (Directional reachability & hop depth)
//!         │
//!         ▼
//!      Impact (Candidate blast radius & propagation policy)
//!         │
//!         ▼
//!    Provenance (Relationship ──supports── EvidenceId dual-index)
//!         │
//!         ▼
//! Relationship History / State (Observation history & current state)
//!         │
//!         ▼
//!   Answer Engine (Structured, explainable product-level facts)
//! ```
//!
//! # Core Question
//!
//! The Answer Engine supports the foundational WhatBreaks question:
//!
//! > *"What could be affected if this resource changes, and why?"*
//!
//! # Design Principles
//!
//! - **Structured Facts, Not Prose**: Produces strictly structured domain types.
//!   Never produces UI strings, HTML, templates, or natural-language sentences.
//! - **No Heuristics, ML, or LLMs**: Operates 100% deterministically on actual graph edges,
//!   paths, and evidence.
//! - **Separation of Concerns**: Orchestrates [`ImpactEngine`], [`Graph`], [`ProvenanceStore`],
//!   and [`RelationshipStateDerivation`] without mutating them or duplicating their logic.
//! - **Preserve Edge Direction**: Paths explicitly preserve the natural `source -> target`
//!   direction of every relationship.
//! - **Deterministic Ordering**: Guarantees identical outputs for identical inputs regardless
//!   of platform or hash map iterations.
//! - **Conservative State Semantics**: Absence of evidence leaves relationship state as
//!   [`RelationshipState::Unknown`], never `Inactive`.

use std::collections::{HashMap, HashSet};

use chrono::{DateTime, Utc};

use crate::evidence::{Evidence, EvidenceId, EvidenceSource, ObservationType};
use crate::graph::Graph;
use crate::history::{RelationshipState, RelationshipStateDerivation};
use crate::impact::{propagates_impact, ImpactEngine, ImpactRequest, ImpactedResource};
use crate::provenance::ProvenanceStore;
use crate::relationship::Relationship;
use crate::resource::ResourceIdentity;
use crate::traversal::TraversalDirection;

// ---------------------------------------------------------------------------
// Canonical Sorting Helpers
// ---------------------------------------------------------------------------

#[inline]
fn resource_key(id: &ResourceIdentity) -> (&str, &str, &str) {
    (
        id.provider.as_str(),
        id.resource_type.as_str(),
        &id.provider_id,
    )
}

#[inline]
fn cmp_resource_identity(a: &ResourceIdentity, b: &ResourceIdentity) -> std::cmp::Ordering {
    resource_key(a).cmp(&resource_key(b))
}

#[inline]
fn relationship_key(rel: &Relationship) -> (&str, &str, &str, &str, &str, &str, &str) {
    (
        rel.source.provider.as_str(),
        rel.source.resource_type.as_str(),
        &rel.source.provider_id,
        rel.target.provider.as_str(),
        rel.target.resource_type.as_str(),
        &rel.target.provider_id,
        rel.kind.as_str(),
    )
}

#[inline]
fn cmp_relationship(a: &Relationship, b: &Relationship) -> std::cmp::Ordering {
    relationship_key(a).cmp(&relationship_key(b))
}

#[inline]
fn cmp_evidence_id(a: &EvidenceId, b: &EvidenceId) -> std::cmp::Ordering {
    a.as_uuid().cmp(&b.as_uuid())
}

#[inline]
fn cmp_impact_path(a: &ImpactPath, b: &ImpactPath) -> std::cmp::Ordering {
    a.len()
        .cmp(&b.len())
        .then_with(|| {
            for (r_a, r_b) in a.resources.iter().zip(b.resources.iter()) {
                let cmp = cmp_resource_identity(r_a, r_b);
                if cmp != std::cmp::Ordering::Equal {
                    return cmp;
                }
            }
            a.resources.len().cmp(&b.resources.len())
        })
        .then_with(|| {
            for (rel_a, rel_b) in a.relationships.iter().zip(b.relationships.iter()) {
                let cmp = cmp_relationship(rel_a, rel_b);
                if cmp != std::cmp::Ordering::Equal {
                    return cmp;
                }
            }
            a.relationships.len().cmp(&b.relationships.len())
        })
}

// ---------------------------------------------------------------------------
// AnswerRequest
// ---------------------------------------------------------------------------

/// A domain-level request for an explainable impact answer.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AnswerRequest {
    /// The target resource being evaluated for change or failure impact.
    pub target: ResourceIdentity,
    /// Traversal direction for relationship analysis.
    pub direction: TraversalDirection,
    /// Maximum relationship-hop depth to explore.
    pub max_depth: usize,
}

impl AnswerRequest {
    /// Construct a new `AnswerRequest`.
    pub fn new(target: ResourceIdentity, direction: TraversalDirection, max_depth: usize) -> Self {
        Self {
            target,
            direction,
            max_depth,
        }
    }

    /// Construct a standard incoming blast-radius request (upstream dependents).
    pub fn incoming(target: ResourceIdentity, max_depth: usize) -> Self {
        Self::new(target, TraversalDirection::Incoming, max_depth)
    }

    /// Construct an outgoing impact request (downstream dependencies).
    pub fn outgoing(target: ResourceIdentity, max_depth: usize) -> Self {
        Self::new(target, TraversalDirection::Outgoing, max_depth)
    }
}

// ---------------------------------------------------------------------------
// ImpactSummary
// ---------------------------------------------------------------------------

/// Deterministic aggregate facts summarizing the candidate blast radius.
///
/// Contains strictly factual counts and depths. Never calculates percentages,
/// risk scores, or UI-specific labels. The target resource is never included
/// in these counts.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct ImpactSummary {
    /// Total number of impacted resources identified by [`ImpactEngine`].
    pub impacted_count: usize,
    /// Number of directly impacted resources (depth = 1).
    pub direct_count: usize,
    /// Number of indirectly impacted resources (depth > 1).
    pub indirect_count: usize,
    /// Maximum hop depth among impacted resources (0 if no resources impacted).
    pub max_depth: usize,
}

impl ImpactSummary {
    /// Construct a new `ImpactSummary`.
    pub fn new(
        impacted_count: usize,
        direct_count: usize,
        indirect_count: usize,
        max_depth: usize,
    ) -> Self {
        Self {
            impacted_count,
            direct_count,
            indirect_count,
            max_depth,
        }
    }

    /// Return `true` if no resources were impacted.
    pub fn is_empty(&self) -> bool {
        self.impacted_count == 0
    }
}

// ---------------------------------------------------------------------------
// AnswerRelationship
// ---------------------------------------------------------------------------

/// A lightweight, answer-level relationship structure linking an infrastructure
/// edge with its derived operational state and supporting evidence IDs.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AnswerRelationship {
    /// The infrastructure relationship.
    pub relationship: Relationship,
    /// Derived operational state of the relationship (`Supported` or `Unknown`).
    pub state: RelationshipState,
    /// Supporting evidence IDs in canonical sorted order.
    pub evidence_ids: Vec<EvidenceId>,
}

impl AnswerRelationship {
    /// Construct a new `AnswerRelationship`.
    pub fn new(
        relationship: Relationship,
        state: RelationshipState,
        evidence_ids: Vec<EvidenceId>,
    ) -> Self {
        Self {
            relationship,
            state,
            evidence_ids,
        }
    }

    /// Return `true` if this relationship is confirmed by supporting evidence.
    pub fn is_supported(&self) -> bool {
        self.state.is_supported()
    }

    /// Return `true` if this relationship has unknown state.
    pub fn is_unknown(&self) -> bool {
        self.state.is_unknown()
    }
}

// ---------------------------------------------------------------------------
// ImpactPath
// ---------------------------------------------------------------------------

/// A deterministic sequence of resources and directional relationships explaining
/// how an impacted resource is connected to the target.
///
/// In both incoming and outgoing traversals, the relationship direction is strictly
/// preserved:
/// `relationships[i].source == resources[i]` and `relationships[i].target == resources[i+1]`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ImpactPath {
    /// Sequence of resource identities along the path.
    pub resources: Vec<ResourceIdentity>,
    /// Sequence of relationships connecting adjacent resources along the path.
    pub relationships: Vec<Relationship>,
}

impl ImpactPath {
    /// Construct a new `ImpactPath`.
    pub fn new(resources: Vec<ResourceIdentity>, relationships: Vec<Relationship>) -> Self {
        Self {
            resources,
            relationships,
        }
    }

    /// Number of relationship hops along this path.
    pub fn len(&self) -> usize {
        self.relationships.len()
    }

    /// Return `true` if the path contains no relationship hops.
    pub fn is_empty(&self) -> bool {
        self.relationships.is_empty()
    }

    /// The first resource in the path.
    pub fn start(&self) -> Option<&ResourceIdentity> {
        self.resources.first()
    }

    /// The last resource in the path.
    pub fn end(&self) -> Option<&ResourceIdentity> {
        self.resources.last()
    }
}

// ---------------------------------------------------------------------------
// AnswerEvidence
// ---------------------------------------------------------------------------

/// Lightweight factual evidence metadata supporting relationships in the answer.
///
/// Exposes evidence provenance, observation type, and timestamp without duplicating
/// arbitrary JSON payload data.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AnswerEvidence {
    /// Unique identifier of the observation.
    pub id: EvidenceId,
    /// Originating provider and collector.
    pub source: EvidenceSource,
    /// Timestamp when the observation was made (UTC).
    pub observed_at: DateTime<Utc>,
    /// Semantic type of the observation.
    pub observation_type: ObservationType,
}

impl AnswerEvidence {
    /// Construct a new `AnswerEvidence`.
    pub fn new(
        id: EvidenceId,
        source: EvidenceSource,
        observed_at: DateTime<Utc>,
        observation_type: ObservationType,
    ) -> Self {
        Self {
            id,
            source,
            observed_at,
            observation_type,
        }
    }

    /// Construct `AnswerEvidence` from an [`Evidence`] record.
    pub fn from_evidence(ev: &Evidence) -> Self {
        Self {
            id: ev.id,
            source: ev.source.clone(),
            observed_at: ev.observed_at,
            observation_type: ev.observation_type.clone(),
        }
    }
}

// ---------------------------------------------------------------------------
// ExplanationFact
// ---------------------------------------------------------------------------

/// Structured, factual explanation of why a resource is considered impacted.
///
/// Connects a specific path, the relationships along that path, and their supporting
/// evidence IDs. Never generates prose or templates; provides pure domain facts
/// for downstream presentation or consumption.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ExplanationFact {
    /// The impact path connecting the impacted resource and target.
    pub path: ImpactPath,
    /// The answer-level relationships along this path, in path order.
    pub relationships: Vec<AnswerRelationship>,
    /// Unique supporting evidence IDs along this path, canonically sorted.
    pub evidence_ids: Vec<EvidenceId>,
}

impl ExplanationFact {
    /// Construct a new `ExplanationFact`.
    pub fn new(
        path: ImpactPath,
        relationships: Vec<AnswerRelationship>,
        evidence_ids: Vec<EvidenceId>,
    ) -> Self {
        Self {
            path,
            relationships,
            evidence_ids,
        }
    }
}

// ---------------------------------------------------------------------------
// ImpactAnswer
// ---------------------------------------------------------------------------

/// Complete, deterministic, explainable answer to an impact analysis query.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ImpactAnswer {
    /// The target resource that was analyzed.
    pub target: ResourceIdentity,
    /// Aggregate factual summary of the blast radius.
    pub summary: ImpactSummary,
    /// Candidate impacted resources returned by [`ImpactEngine`], in depth/canonical order.
    pub impacted_resources: Vec<ImpactedResource>,
    /// Relevant relationships along the impact paths, in canonical order.
    pub relationships: Vec<AnswerRelationship>,
    /// Reconstructed deterministic impact paths connecting impacted resources and target.
    pub paths: Vec<ImpactPath>,
    /// Deduplicated supporting evidence metadata, in canonical order.
    pub evidence: Vec<AnswerEvidence>,
    /// Structured explanation facts corresponding to each impact path.
    pub explanation_facts: Vec<ExplanationFact>,
}

impl ImpactAnswer {
    /// Construct a new `ImpactAnswer`.
    pub fn new(
        target: ResourceIdentity,
        summary: ImpactSummary,
        impacted_resources: Vec<ImpactedResource>,
        relationships: Vec<AnswerRelationship>,
        paths: Vec<ImpactPath>,
        evidence: Vec<AnswerEvidence>,
        explanation_facts: Vec<ExplanationFact>,
    ) -> Self {
        Self {
            target,
            summary,
            impacted_resources,
            relationships,
            paths,
            evidence,
            explanation_facts,
        }
    }

    /// Return `true` if no resources were impacted.
    pub fn is_empty(&self) -> bool {
        self.impacted_resources.is_empty()
    }

    /// Check if a specific resource identity is present in the blast radius.
    pub fn contains(&self, resource: &ResourceIdentity) -> bool {
        self.impacted_resources
            .iter()
            .any(|r| &r.resource == resource)
    }

    /// Return the hop depth of a specific resource if impacted.
    pub fn depth_of(&self, resource: &ResourceIdentity) -> Option<usize> {
        self.impacted_resources
            .iter()
            .find(|r| &r.resource == resource)
            .map(|r| r.depth)
    }

    /// Return all paths associated with a specific resource identity.
    pub fn paths_for<'a>(&'a self, resource: &'a ResourceIdentity) -> Vec<&'a ImpactPath> {
        self.paths
            .iter()
            .filter(|p| p.resources.contains(resource))
            .collect()
    }

    /// Return all explanation facts associated with a specific resource identity.
    pub fn facts_for<'a>(&'a self, resource: &'a ResourceIdentity) -> Vec<&'a ExplanationFact> {
        self.explanation_facts
            .iter()
            .filter(|f| f.path.resources.contains(resource))
            .collect()
    }
}

// ---------------------------------------------------------------------------
// AnswerEngine
// ---------------------------------------------------------------------------

/// Stateless, deterministic Answer Engine orchestrating core engine subsystems
/// into an explainable [`ImpactAnswer`].
#[derive(Debug, Clone, Copy, Default)]
pub struct AnswerEngine;

impl AnswerEngine {
    /// Analyze candidate blast radius and build an explainable [`ImpactAnswer`].
    ///
    /// # Semantics
    /// - Operates strictly as a read-only domain layer over [`Graph`], [`ProvenanceStore`],
    ///   and [`Evidence`].
    /// - Reuses [`ImpactEngine`] directly for candidate blast-radius discovery.
    /// - Reconstructs deterministic propagating paths connecting impacted resources and target.
    /// - Extracts and deduplicates only relationships and evidence relevant to reported impact.
    /// - Derives relationship operational states using [`RelationshipStateDerivation`].
    /// - Absence of evidence leaves relationship state as [`RelationshipState::Unknown`],
    ///   never `Inactive`.
    /// - Output ordering is 100% deterministic.
    pub fn analyze(
        graph: &Graph,
        provenance: &ProvenanceStore,
        evidence_catalog: &[Evidence],
        request: &AnswerRequest,
    ) -> ImpactAnswer {
        // 1. Guard against empty graph, unknown target, or zero max_depth
        if request.max_depth == 0 || !graph.contains_resource(&request.target) {
            return ImpactAnswer::new(
                request.target.clone(),
                ImpactSummary::default(),
                Vec::new(),
                Vec::new(),
                Vec::new(),
                Vec::new(),
                Vec::new(),
            );
        }

        // 2. Delegate blast-radius calculation to ImpactEngine
        let impact_req =
            ImpactRequest::new(request.target.clone(), request.direction, request.max_depth);
        let impact_result = ImpactEngine::analyze(graph, &impact_req);

        if impact_result.is_empty() {
            return ImpactAnswer::new(
                request.target.clone(),
                ImpactSummary::default(),
                Vec::new(),
                Vec::new(),
                Vec::new(),
                Vec::new(),
                Vec::new(),
            );
        }

        // 3. Compute deterministic summary facts
        let impacted_count = impact_result.len();
        let direct_count = impact_result
            .impacted
            .iter()
            .filter(|r| r.depth == 1)
            .count();
        let indirect_count = impact_result
            .impacted
            .iter()
            .filter(|r| r.depth > 1)
            .count();
        let max_depth = impact_result
            .impacted
            .iter()
            .map(|r| r.depth)
            .max()
            .unwrap_or(0);

        let summary = ImpactSummary::new(impacted_count, direct_count, indirect_count, max_depth);

        // 4. Reconstruct deterministic propagating paths connecting impacted resources and target
        let mut paths = Self::reconstruct_paths(graph, request);
        paths.sort_by(cmp_impact_path);
        paths.dedup();

        // 5. Collect relevant relationships along paths, derive states, attach provenance
        let mut unique_rels: Vec<Relationship> = Vec::new();
        let mut seen_rels: HashSet<Relationship> = HashSet::new();

        for path in &paths {
            for rel in &path.relationships {
                if seen_rels.insert(rel.clone()) {
                    unique_rels.push(rel.clone());
                }
            }
        }
        unique_rels.sort_by(cmp_relationship);

        let mut rel_to_answer: HashMap<Relationship, AnswerRelationship> = HashMap::new();
        let mut answer_relationships: Vec<AnswerRelationship> =
            Vec::with_capacity(unique_rels.len());

        for rel in unique_rels {
            let evidence_ids = provenance.evidence_for(&rel);
            let state =
                RelationshipStateDerivation::derive_state(&rel, provenance, evidence_catalog);
            let answer_rel = AnswerRelationship::new(rel.clone(), state, evidence_ids);
            rel_to_answer.insert(rel, answer_rel.clone());
            answer_relationships.push(answer_rel);
        }

        // 6. Build AnswerEvidence for all referenced evidence IDs in relevant relationships
        let mut referenced_evidence_ids: HashSet<EvidenceId> = HashSet::new();
        for ar in &answer_relationships {
            for id in &ar.evidence_ids {
                referenced_evidence_ids.insert(*id);
            }
        }

        let mut answer_evidence_list: Vec<AnswerEvidence> = Vec::new();
        let mut seen_evidence_ids: HashSet<EvidenceId> = HashSet::new();

        for ev in evidence_catalog {
            if referenced_evidence_ids.contains(&ev.id) && seen_evidence_ids.insert(ev.id) {
                answer_evidence_list.push(AnswerEvidence::from_evidence(ev));
            }
        }
        answer_evidence_list.sort_by(|a, b| cmp_evidence_id(&a.id, &b.id));

        // 7. Construct ExplanationFacts for each path
        let mut explanation_facts: Vec<ExplanationFact> = Vec::with_capacity(paths.len());

        for path in &paths {
            let path_rels: Vec<AnswerRelationship> = path
                .relationships
                .iter()
                .filter_map(|r| rel_to_answer.get(r).cloned())
                .collect();

            let mut path_ev_set: HashSet<EvidenceId> = HashSet::new();
            for ar in &path_rels {
                for id in &ar.evidence_ids {
                    path_ev_set.insert(*id);
                }
            }
            let mut path_evidence_ids: Vec<EvidenceId> = path_ev_set.into_iter().collect();
            path_evidence_ids.sort_by(cmp_evidence_id);

            explanation_facts.push(ExplanationFact::new(
                path.clone(),
                path_rels,
                path_evidence_ids,
            ));
        }

        ImpactAnswer::new(
            request.target.clone(),
            summary,
            impact_result.impacted,
            answer_relationships,
            paths,
            answer_evidence_list,
            explanation_facts,
        )
    }

    /// Reconstruct deterministic propagating paths up to `request.max_depth`.
    fn reconstruct_paths(graph: &Graph, request: &AnswerRequest) -> Vec<ImpactPath> {
        let mut all_paths: Vec<ImpactPath> = Vec::new();

        match request.direction {
            TraversalDirection::Incoming => {
                // Incoming traversal: relationships point towards target (upstream dependents).
                // Path format: [R_k, ..., R_1, Target], rels: [R_k -> ..., R_1 -> Target].
                // Hop 1: incoming relationships into target
                let mut current_paths: Vec<ImpactPath> = Vec::new();
                for rel in graph.get_relationships_to(&request.target) {
                    if !propagates_impact(&rel.kind) {
                        continue;
                    }
                    if rel.source == request.target {
                        // Skip self-loop on target
                        continue;
                    }
                    current_paths.push(ImpactPath::new(
                        vec![rel.source.clone(), request.target.clone()],
                        vec![rel],
                    ));
                }

                all_paths.extend(current_paths.clone());

                for _ in 1..request.max_depth {
                    let mut next_paths: Vec<ImpactPath> = Vec::new();

                    for p in current_paths {
                        let head = p.resources.first().expect("path has at least 1 resource");
                        for rel in graph.get_relationships_to(head) {
                            if !propagates_impact(&rel.kind) {
                                continue;
                            }
                            // Cycle/self-loop prevention: resource must not already exist in path
                            if p.resources.contains(&rel.source) {
                                continue;
                            }

                            let mut resources = vec![rel.source.clone()];
                            resources.extend(p.resources.iter().cloned());

                            let mut relationships = vec![rel];
                            relationships.extend(p.relationships.iter().cloned());

                            next_paths.push(ImpactPath::new(resources, relationships));
                        }
                    }

                    if next_paths.is_empty() {
                        break;
                    }

                    all_paths.extend(next_paths.clone());
                    current_paths = next_paths;
                }
            }
            TraversalDirection::Outgoing => {
                // Outgoing traversal: relationships point away from target (downstream dependencies).
                // Path format: [Target, R_1, ..., R_k], rels: [Target -> R_1, ..., R_{k-1} -> R_k].
                // Hop 1: outgoing relationships from target
                let mut current_paths: Vec<ImpactPath> = Vec::new();
                for rel in graph.get_relationships_from(&request.target) {
                    if !propagates_impact(&rel.kind) {
                        continue;
                    }
                    if rel.target == request.target {
                        // Skip self-loop on target
                        continue;
                    }
                    current_paths.push(ImpactPath::new(
                        vec![request.target.clone(), rel.target.clone()],
                        vec![rel],
                    ));
                }

                all_paths.extend(current_paths.clone());

                for _ in 1..request.max_depth {
                    let mut next_paths: Vec<ImpactPath> = Vec::new();

                    for p in current_paths {
                        let tail = p.resources.last().expect("path has at least 1 resource");
                        for rel in graph.get_relationships_from(tail) {
                            if !propagates_impact(&rel.kind) {
                                continue;
                            }
                            // Cycle/self-loop prevention: resource must not already exist in path
                            if p.resources.contains(&rel.target) {
                                continue;
                            }

                            let mut resources = p.resources.clone();
                            resources.push(rel.target.clone());

                            let mut relationships = p.relationships.clone();
                            relationships.push(rel);

                            next_paths.push(ImpactPath::new(resources, relationships));
                        }
                    }

                    if next_paths.is_empty() {
                        break;
                    }

                    all_paths.extend(next_paths.clone());
                    current_paths = next_paths;
                }
            }
        }

        all_paths
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::evidence::CollectorId;
    use crate::relationship::RelationshipKind;
    use crate::resource::{Provider, ResourceKind};

    fn make_identity(name: &str) -> ResourceIdentity {
        ResourceIdentity::new(
            Provider::new("kubernetes"),
            ResourceKind::new("service"),
            name,
        )
    }

    fn make_rel(src: &str, tgt: &str, kind: RelationshipKind) -> Relationship {
        Relationship::new(make_identity(src), make_identity(tgt), kind)
    }

    fn make_evidence(provider: &str, collector: &str, subject: &ResourceIdentity) -> Evidence {
        Evidence::new(
            EvidenceSource::new(Provider::new(provider), CollectorId::new(collector)),
            Utc::now(),
            ObservationType::RUNTIME_CONNECTION,
            subject.clone(),
            serde_json::json!({ "connection": "active" }),
        )
    }

    fn assert_send_sync<T: Send + Sync>() {}

    // -----------------------------------------------------------------------
    // Static Assertions: Send + Sync
    // -----------------------------------------------------------------------
    #[test]
    fn test_send_sync_thread_safety() {
        assert_send_sync::<AnswerEngine>();
        assert_send_sync::<AnswerRequest>();
        assert_send_sync::<ImpactSummary>();
        assert_send_sync::<AnswerRelationship>();
        assert_send_sync::<ImpactPath>();
        assert_send_sync::<AnswerEvidence>();
        assert_send_sync::<ExplanationFact>();
        assert_send_sync::<ImpactAnswer>();
    }

    // -----------------------------------------------------------------------
    // 1. Simple One-Hop Impact
    // -----------------------------------------------------------------------
    #[test]
    fn test_1_simple_one_hop_impact() {
        let mut g = Graph::new();
        let mut prov = ProvenanceStore::new();

        let target = make_identity("payments-api");
        let checkout = make_identity("checkout-service");

        let rel = Relationship::new(
            checkout.clone(),
            target.clone(),
            RelationshipKind::DEPENDS_ON,
        );
        g.add_relationship(rel.clone());
        prov.add_relationship(rel.clone());

        let ev = make_evidence("kubernetes", "k8s-runtime", &checkout);
        prov.add_evidence(&rel, ev.id);
        let catalog = vec![ev.clone()];

        let req = AnswerRequest::incoming(target.clone(), 1);
        let ans = AnswerEngine::analyze(&g, &prov, &catalog, &req);

        assert_eq!(ans.target, target);
        assert_eq!(ans.summary.impacted_count, 1);
        assert_eq!(ans.summary.direct_count, 1);
        assert_eq!(ans.summary.indirect_count, 0);
        assert_eq!(ans.summary.max_depth, 1);

        assert_eq!(ans.impacted_resources.len(), 1);
        assert_eq!(ans.impacted_resources[0].resource, checkout);
        assert_eq!(ans.impacted_resources[0].depth, 1);

        assert_eq!(ans.relationships.len(), 1);
        assert_eq!(ans.relationships[0].relationship, rel);
        assert!(ans.relationships[0].is_supported());

        assert_eq!(ans.paths.len(), 1);
        assert_eq!(
            ans.paths[0].resources,
            vec![checkout.clone(), target.clone()]
        );
        assert_eq!(ans.paths[0].relationships, vec![rel.clone()]);

        assert_eq!(ans.evidence.len(), 1);
        assert_eq!(ans.evidence[0].id, ev.id);

        assert_eq!(ans.explanation_facts.len(), 1);
        assert_eq!(ans.explanation_facts[0].path, ans.paths[0]);
        assert_eq!(ans.explanation_facts[0].evidence_ids, vec![ev.id]);
    }

    // -----------------------------------------------------------------------
    // 2. Multi-Hop Impact
    // -----------------------------------------------------------------------
    #[test]
    fn test_2_multi_hop_impact() {
        let mut g = Graph::new();
        let mut prov = ProvenanceStore::new();

        let target = make_identity("payments-api");
        let checkout = make_identity("checkout-service");
        let frontend = make_identity("frontend");

        let rel1 = Relationship::new(
            checkout.clone(),
            target.clone(),
            RelationshipKind::DEPENDS_ON,
        );
        let rel2 = Relationship::new(
            frontend.clone(),
            checkout.clone(),
            RelationshipKind::DEPENDS_ON,
        );

        g.add_relationship(rel1.clone());
        g.add_relationship(rel2.clone());
        prov.add_relationship(rel1.clone());
        prov.add_relationship(rel2.clone());

        let ev1 = make_evidence("kubernetes", "k8s-runtime", &checkout);
        let ev2 = make_evidence("kubernetes", "k8s-runtime", &frontend);
        prov.add_evidence(&rel1, ev1.id);
        prov.add_evidence(&rel2, ev2.id);

        let catalog = vec![ev1.clone(), ev2.clone()];

        let req = AnswerRequest::incoming(target.clone(), 3);
        let ans = AnswerEngine::analyze(&g, &prov, &catalog, &req);

        assert_eq!(ans.summary.impacted_count, 2);
        assert_eq!(ans.summary.direct_count, 1);
        assert_eq!(ans.summary.indirect_count, 1);
        assert_eq!(ans.summary.max_depth, 2);

        assert_eq!(ans.impacted_resources.len(), 2);
        assert_eq!(ans.impacted_resources[0].resource, checkout);
        assert_eq!(ans.impacted_resources[0].depth, 1);
        assert_eq!(ans.impacted_resources[1].resource, frontend);
        assert_eq!(ans.impacted_resources[1].depth, 2);

        assert_eq!(ans.relationships.len(), 2);
        assert_eq!(ans.paths.len(), 2);
    }

    // -----------------------------------------------------------------------
    // 3. Direct vs Indirect Counts
    // -----------------------------------------------------------------------
    #[test]
    fn test_3_direct_vs_indirect_counts() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        let target = make_identity("db");

        // Direct (depth 1)
        g.add_relationship(make_rel("svc1", "db", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("svc2", "db", RelationshipKind::DEPENDS_ON));

        // Indirect (depth 2)
        g.add_relationship(make_rel("ingress1", "svc1", RelationshipKind::CALLS));
        g.add_relationship(make_rel("ingress2", "svc2", RelationshipKind::CALLS));

        // Indirect (depth 3)
        g.add_relationship(make_rel("edge", "ingress1", RelationshipKind::CALLS));

        let req = AnswerRequest::incoming(target, 5);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(ans.summary.impacted_count, 5);
        assert_eq!(ans.summary.direct_count, 2); // svc1, svc2
        assert_eq!(ans.summary.indirect_count, 3); // ingress1, ingress2, edge
        assert_eq!(ans.summary.max_depth, 3);
    }

    // -----------------------------------------------------------------------
    // 4. Maximum Depth Limiting
    // -----------------------------------------------------------------------
    #[test]
    fn test_4_maximum_depth() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        let target = make_identity("target");
        let d1 = make_identity("d1");
        let d2 = make_identity("d2");
        let d3 = make_identity("d3");

        g.add_relationship(Relationship::new(
            d1.clone(),
            target.clone(),
            RelationshipKind::DEPENDS_ON,
        ));
        g.add_relationship(Relationship::new(
            d2.clone(),
            d1.clone(),
            RelationshipKind::DEPENDS_ON,
        ));
        g.add_relationship(Relationship::new(
            d3.clone(),
            d2.clone(),
            RelationshipKind::DEPENDS_ON,
        ));

        // Max depth = 1
        let req1 = AnswerRequest::incoming(target.clone(), 1);
        let ans1 = AnswerEngine::analyze(&g, &prov, &[], &req1);
        assert_eq!(ans1.summary.impacted_count, 1);
        assert_eq!(ans1.summary.max_depth, 1);
        assert_eq!(ans1.impacted_resources.len(), 1);
        assert_eq!(ans1.impacted_resources[0].resource, d1);

        // Max depth = 2
        let req2 = AnswerRequest::incoming(target, 2);
        let ans2 = AnswerEngine::analyze(&g, &prov, &[], &req2);
        assert_eq!(ans2.summary.impacted_count, 2);
        assert_eq!(ans2.summary.max_depth, 2);
        assert_eq!(ans2.impacted_resources.len(), 2);
        assert_eq!(ans2.impacted_resources[1].resource, d2);
    }

    // -----------------------------------------------------------------------
    // 5. Empty Graph
    // -----------------------------------------------------------------------
    #[test]
    fn test_5_empty_graph() {
        let g = Graph::new();
        let prov = ProvenanceStore::new();
        let target = make_identity("nonexistent");
        let req = AnswerRequest::incoming(target.clone(), 3);

        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(ans.target, target);
        assert_eq!(ans.summary, ImpactSummary::default());
        assert!(ans.impacted_resources.is_empty());
        assert!(ans.relationships.is_empty());
        assert!(ans.paths.is_empty());
        assert!(ans.evidence.is_empty());
        assert!(ans.explanation_facts.is_empty());
        assert!(ans.is_empty());
    }

    // -----------------------------------------------------------------------
    // 6. Unknown Target
    // -----------------------------------------------------------------------
    #[test]
    fn test_6_unknown_target() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));
        let prov = ProvenanceStore::new();

        let target = make_identity("unknown");
        let req = AnswerRequest::incoming(target.clone(), 3);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(ans.target, target);
        assert_eq!(ans.summary.impacted_count, 0);
        assert!(ans.impacted_resources.is_empty());
        assert!(ans.relationships.is_empty());
        assert!(ans.paths.is_empty());
    }

    // -----------------------------------------------------------------------
    // 7. Target with No Impact
    // -----------------------------------------------------------------------
    #[test]
    fn test_7_target_with_no_impact() {
        let mut g = Graph::new();
        let target = make_identity("isolated");
        g.add_resource(target.clone());
        let prov = ProvenanceStore::new();

        let req = AnswerRequest::incoming(target.clone(), 3);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(ans.target, target);
        assert_eq!(ans.summary.impacted_count, 0);
        assert!(ans.impacted_resources.is_empty());
        assert!(ans.relationships.is_empty());
        assert!(ans.paths.is_empty());
    }

    // -----------------------------------------------------------------------
    // 8. Relationship Included in Answer
    // -----------------------------------------------------------------------
    #[test]
    fn test_8_relationship_included_in_answer() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        let target = make_identity("target");
        let caller = make_identity("caller");
        let rel = Relationship::new(caller, target.clone(), RelationshipKind::CALLS);
        g.add_relationship(rel.clone());

        let req = AnswerRequest::incoming(target, 1);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(ans.relationships.len(), 1);
        assert_eq!(ans.relationships[0].relationship, rel);
    }

    // -----------------------------------------------------------------------
    // 9. Relationship State Supported
    // -----------------------------------------------------------------------
    #[test]
    fn test_9_relationship_state_supported() {
        let mut g = Graph::new();
        let mut prov = ProvenanceStore::new();

        let target = make_identity("target");
        let caller = make_identity("caller");
        let rel = Relationship::new(caller.clone(), target.clone(), RelationshipKind::CALLS);
        g.add_relationship(rel.clone());
        prov.add_relationship(rel.clone());

        let ev = make_evidence("kubernetes", "k8s-runtime", &caller);
        prov.add_evidence(&rel, ev.id);

        let req = AnswerRequest::incoming(target, 1);
        let ans = AnswerEngine::analyze(&g, &prov, &[ev], &req);

        assert_eq!(ans.relationships.len(), 1);
        assert_eq!(ans.relationships[0].state, RelationshipState::Supported);
        assert!(ans.relationships[0].is_supported());
    }

    // -----------------------------------------------------------------------
    // 10. Relationship State Unknown (When No Evidence)
    // -----------------------------------------------------------------------
    #[test]
    fn test_10_relationship_state_unknown() {
        let mut g = Graph::new();
        let mut prov = ProvenanceStore::new();

        let target = make_identity("target");
        let caller = make_identity("caller");
        let rel = Relationship::new(caller, target.clone(), RelationshipKind::CALLS);
        g.add_relationship(rel.clone());
        prov.add_relationship(rel.clone());

        // Registered in provenance, but zero evidence associated
        let req = AnswerRequest::incoming(target, 1);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(ans.relationships.len(), 1);
        assert_eq!(ans.relationships[0].state, RelationshipState::Unknown);
        assert!(ans.relationships[0].is_unknown());
        assert!(!ans.relationships[0].is_supported());
    }

    // -----------------------------------------------------------------------
    // 11. Provenance Evidence Attached Correctly
    // -----------------------------------------------------------------------
    #[test]
    fn test_11_provenance_evidence_attached_correctly() {
        let mut g = Graph::new();
        let mut prov = ProvenanceStore::new();

        let target = make_identity("target");
        let caller = make_identity("caller");
        let rel = Relationship::new(caller.clone(), target.clone(), RelationshipKind::CALLS);
        g.add_relationship(rel.clone());
        prov.add_relationship(rel.clone());

        let ev1 = make_evidence("kubernetes", "k8s-runtime", &caller);
        let ev2 = make_evidence("kubernetes", "aws-network", &caller);
        prov.add_evidence(&rel, ev1.id);
        prov.add_evidence(&rel, ev2.id);

        let req = AnswerRequest::incoming(target, 1);
        let ans = AnswerEngine::analyze(&g, &prov, &[ev1.clone(), ev2.clone()], &req);

        assert_eq!(ans.relationships[0].evidence_ids.len(), 2);
        assert!(ans.relationships[0].evidence_ids.contains(&ev1.id));
        assert!(ans.relationships[0].evidence_ids.contains(&ev2.id));
    }

    // -----------------------------------------------------------------------
    // 12. Evidence Deduplication
    // -----------------------------------------------------------------------
    #[test]
    fn test_12_evidence_deduplication() {
        let mut g = Graph::new();
        let mut prov = ProvenanceStore::new();

        let target = make_identity("target");
        let s1 = make_identity("s1");
        let s2 = make_identity("s2");

        let rel1 = Relationship::new(s1.clone(), target.clone(), RelationshipKind::DEPENDS_ON);
        let rel2 = Relationship::new(s2.clone(), target.clone(), RelationshipKind::DEPENDS_ON);

        g.add_relationship(rel1.clone());
        g.add_relationship(rel2.clone());
        prov.add_relationship(rel1.clone());
        prov.add_relationship(rel2.clone());

        // A single evidence supports BOTH relationships
        let shared_ev = make_evidence("kubernetes", "discovery-agent", &target);
        prov.add_evidence(&rel1, shared_ev.id);
        prov.add_evidence(&rel2, shared_ev.id);

        let catalog = [shared_ev.clone()];
        let req = AnswerRequest::incoming(target, 1);
        let ans = AnswerEngine::analyze(&g, &prov, &catalog, &req);

        // Even though 2 relationships share the evidence, AnswerEvidence has only 1 deduplicated entry
        assert_eq!(ans.evidence.len(), 1);
        assert_eq!(ans.evidence[0].id, shared_ev.id);
    }

    // -----------------------------------------------------------------------
    // 13. Path Reconstruction (1 Hop)
    // -----------------------------------------------------------------------
    #[test]
    fn test_13_path_reconstruction() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        let target = make_identity("target");
        let dep = make_identity("dep");
        let rel = Relationship::new(dep.clone(), target.clone(), RelationshipKind::READS_FROM);
        g.add_relationship(rel.clone());

        let req = AnswerRequest::incoming(target.clone(), 1);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(ans.paths.len(), 1);
        let path = &ans.paths[0];
        assert_eq!(path.len(), 1);
        assert_eq!(path.resources, vec![dep, target]);
        assert_eq!(path.relationships, vec![rel]);
    }

    // -----------------------------------------------------------------------
    // 14. Multi-Hop Path (frontend -> checkout -> payments-api)
    // -----------------------------------------------------------------------
    #[test]
    fn test_14_multi_hop_path() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        let payments = make_identity("payments-api");
        let checkout = make_identity("checkout-service");
        let frontend = make_identity("frontend");

        let rel1 = Relationship::new(
            checkout.clone(),
            payments.clone(),
            RelationshipKind::DEPENDS_ON,
        );
        let rel2 = Relationship::new(
            frontend.clone(),
            checkout.clone(),
            RelationshipKind::DEPENDS_ON,
        );

        g.add_relationship(rel1.clone());
        g.add_relationship(rel2.clone());

        let req = AnswerRequest::incoming(payments.clone(), 2);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(ans.paths.len(), 2);

        // Path 1 (hop 1): checkout -> payments
        assert_eq!(
            ans.paths[0].resources,
            vec![checkout.clone(), payments.clone()]
        );
        assert_eq!(ans.paths[0].relationships, vec![rel1.clone()]);

        // Path 2 (hop 2): frontend -> checkout -> payments
        assert_eq!(
            ans.paths[1].resources,
            vec![frontend.clone(), checkout.clone(), payments.clone()]
        );
        assert_eq!(ans.paths[1].relationships, vec![rel2.clone(), rel1.clone()]);
    }

    // -----------------------------------------------------------------------
    // 15. Multiple Paths / Diamond Graph
    // -----------------------------------------------------------------------
    #[test]
    fn test_15_multiple_paths_diamond_graph() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        // Target = D
        // B -> D, C -> D
        // A -> B, A -> C
        let d = make_identity("d");
        let b = make_identity("b");
        let c = make_identity("c");
        let a = make_identity("a");

        let rel_bd = Relationship::new(b.clone(), d.clone(), RelationshipKind::CALLS);
        let rel_cd = Relationship::new(c.clone(), d.clone(), RelationshipKind::CALLS);
        let rel_ab = Relationship::new(a.clone(), b.clone(), RelationshipKind::CALLS);
        let rel_ac = Relationship::new(a.clone(), c.clone(), RelationshipKind::CALLS);

        g.add_relationship(rel_bd.clone());
        g.add_relationship(rel_cd.clone());
        g.add_relationship(rel_ab.clone());
        g.add_relationship(rel_ac.clone());

        let req = AnswerRequest::incoming(d.clone(), 2);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        // 3 impacted resources: b, c (depth 1), a (depth 2)
        assert_eq!(ans.summary.impacted_count, 3);
        assert_eq!(ans.summary.direct_count, 2);
        assert_eq!(ans.summary.indirect_count, 1);
        assert_eq!(ans.summary.max_depth, 2);

        // 4 paths:
        // [b, d]
        // [c, d]
        // [a, b, d]
        // [a, c, d]
        assert_eq!(ans.paths.len(), 4);

        let a_paths: Vec<&ImpactPath> = ans.paths_for(&a);
        assert_eq!(a_paths.len(), 2);

        assert!(a_paths
            .iter()
            .any(|p| p.resources == vec![a.clone(), b.clone(), d.clone()]));
        assert!(a_paths
            .iter()
            .any(|p| p.resources == vec![a.clone(), c.clone(), d.clone()]));
    }

    // -----------------------------------------------------------------------
    // 16. Cycle Handling
    // -----------------------------------------------------------------------
    #[test]
    fn test_16_cycle_handling() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        // Cycle: A -> B -> A, and B -> Target
        let target = make_identity("target");
        let a = make_identity("a");
        let b = make_identity("b");

        let rel_bt = Relationship::new(b.clone(), target.clone(), RelationshipKind::DEPENDS_ON);
        let rel_ab = Relationship::new(a.clone(), b.clone(), RelationshipKind::DEPENDS_ON);
        let rel_ba = Relationship::new(b.clone(), a.clone(), RelationshipKind::DEPENDS_ON);

        g.add_relationship(rel_bt);
        g.add_relationship(rel_ab);
        g.add_relationship(rel_ba);

        // Deep max depth (10) to test cycle termination
        let req = AnswerRequest::incoming(target, 10);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        // BFS candidate blast radius terminates cleanly
        assert_eq!(ans.summary.impacted_count, 2); // a and b
                                                   // Paths must not contain cycles or infinite expansions
        for path in &ans.paths {
            let unique_nodes: HashSet<_> = path.resources.iter().collect();
            assert_eq!(
                unique_nodes.len(),
                path.resources.len(),
                "path must have no duplicate nodes"
            );
        }
    }

    // -----------------------------------------------------------------------
    // 17. Deterministic Output
    // -----------------------------------------------------------------------
    #[test]
    fn test_17_deterministic_output() {
        let mut g = Graph::new();
        let mut prov = ProvenanceStore::new();

        let target = make_identity("target");
        let mut catalog = Vec::new();

        // Add nodes in unordered sequence
        for name in &["z_svc", "m_svc", "a_svc", "k_svc"] {
            let src = make_identity(name);
            let rel = Relationship::new(src.clone(), target.clone(), RelationshipKind::DEPENDS_ON);
            g.add_relationship(rel.clone());
            prov.add_relationship(rel.clone());

            let ev = make_evidence("kubernetes", "k8s", &src);
            prov.add_evidence(&rel, ev.id);
            catalog.push(ev);
        }

        let req = AnswerRequest::incoming(target.clone(), 2);
        let ans1 = AnswerEngine::analyze(&g, &prov, &catalog, &req);
        let ans2 = AnswerEngine::analyze(&g, &prov, &catalog, &req);

        assert_eq!(ans1, ans2);

        // Verify sorted order of impacted resources
        let names: Vec<&str> = ans1
            .impacted_resources
            .iter()
            .map(|r| r.resource.provider_id.as_str())
            .collect();
        assert_eq!(names, vec!["a_svc", "k_svc", "m_svc", "z_svc"]);
    }

    // -----------------------------------------------------------------------
    // 18. No Unrelated Relationships Included
    // -----------------------------------------------------------------------
    #[test]
    fn test_18_no_unrelated_relationships_included() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        let target = make_identity("target");
        let dep = make_identity("dep");
        let unrelated1 = make_identity("unrelated1");
        let unrelated2 = make_identity("unrelated2");

        // Relevant edge
        let relevant_rel = Relationship::new(dep, target.clone(), RelationshipKind::DEPENDS_ON);
        g.add_relationship(relevant_rel.clone());

        // Unrelated disconnected edge
        let unrelated_rel = Relationship::new(unrelated1, unrelated2, RelationshipKind::CALLS);
        g.add_relationship(unrelated_rel);

        let req = AnswerRequest::incoming(target, 2);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(ans.relationships.len(), 1);
        assert_eq!(ans.relationships[0].relationship, relevant_rel);
    }

    // -----------------------------------------------------------------------
    // 19. No Mutation of Graph
    // -----------------------------------------------------------------------
    #[test]
    fn test_19_no_mutation_of_graph() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        let target = make_identity("target");
        let dep = make_identity("dep");
        let rel = Relationship::new(dep, target.clone(), RelationshipKind::DEPENDS_ON);
        g.add_relationship(rel);

        let g_before = g.clone();
        let req = AnswerRequest::incoming(target, 2);
        let _ = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(g, g_before);
    }

    // -----------------------------------------------------------------------
    // 20. No Mutation of ProvenanceStore
    // -----------------------------------------------------------------------
    #[test]
    fn test_20_no_mutation_of_provenance_store() {
        let mut g = Graph::new();
        let mut prov = ProvenanceStore::new();

        let target = make_identity("target");
        let dep = make_identity("dep");
        let rel = Relationship::new(dep.clone(), target.clone(), RelationshipKind::DEPENDS_ON);
        g.add_relationship(rel.clone());
        prov.add_relationship(rel.clone());

        let ev = make_evidence("kubernetes", "k8s", &dep);
        prov.add_evidence(&rel, ev.id);

        let prov_before = prov.clone();
        let req = AnswerRequest::incoming(target, 2);
        let _ = AnswerEngine::analyze(&g, &prov, &[ev], &req);

        assert_eq!(prov, prov_before);
    }

    // -----------------------------------------------------------------------
    // 21. No Mutation of Evidence
    // -----------------------------------------------------------------------
    #[test]
    fn test_21_no_mutation_of_evidence() {
        let mut g = Graph::new();
        let mut prov = ProvenanceStore::new();

        let target = make_identity("target");
        let dep = make_identity("dep");
        let rel = Relationship::new(dep.clone(), target.clone(), RelationshipKind::DEPENDS_ON);
        g.add_relationship(rel.clone());
        prov.add_relationship(rel.clone());

        let ev = make_evidence("kubernetes", "k8s", &dep);
        prov.add_evidence(&rel, ev.id);

        let ev_before = ev.clone();
        let catalog = vec![ev];

        let req = AnswerRequest::incoming(target, 2);
        let _ = AnswerEngine::analyze(&g, &prov, &catalog, &req);

        assert_eq!(catalog[0], ev_before);
    }

    // -----------------------------------------------------------------------
    // 22. No Mutation of Relationship
    // -----------------------------------------------------------------------
    #[test]
    fn test_22_no_mutation_of_relationship() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        let target = make_identity("target");
        let dep = make_identity("dep");
        let rel = Relationship::new(dep, target.clone(), RelationshipKind::DEPENDS_ON);
        g.add_relationship(rel.clone());

        let rel_before = rel.clone();
        let req = AnswerRequest::incoming(target, 2);
        let _ = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(rel, rel_before);
    }

    // -----------------------------------------------------------------------
    // 23. Absence of Evidence Remains Unknown, Never Inactive
    // -----------------------------------------------------------------------
    #[test]
    fn test_23_absence_of_evidence_remains_unknown_never_inactive() {
        let mut g = Graph::new();
        let mut prov = ProvenanceStore::new();

        let target = make_identity("target");
        let dep = make_identity("dep");
        let rel = Relationship::new(dep, target.clone(), RelationshipKind::DEPENDS_ON);
        g.add_relationship(rel.clone());
        prov.add_relationship(rel);

        // No observations registered in catalog
        let req = AnswerRequest::incoming(target, 1);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(ans.relationships.len(), 1);
        assert_eq!(ans.relationships[0].state, RelationshipState::Unknown);
        assert!(ans.relationships[0].is_unknown());
        assert!(!ans.relationships[0].is_supported());
        assert!(!ans.relationships[0].state.is_inactive());
    }

    // -----------------------------------------------------------------------
    // 24. Same Inputs Produce Identical Answer
    // -----------------------------------------------------------------------
    #[test]
    fn test_24_same_inputs_produce_identical_answer() {
        let mut g = Graph::new();
        let mut prov = ProvenanceStore::new();

        let target = make_identity("target");
        let a = make_identity("a");
        let b = make_identity("b");
        let c = make_identity("c");

        let rel1 = Relationship::new(a.clone(), target.clone(), RelationshipKind::CALLS);
        let rel2 = Relationship::new(b.clone(), a.clone(), RelationshipKind::CALLS);
        let rel3 = Relationship::new(c.clone(), target.clone(), RelationshipKind::READS_FROM);

        g.add_relationship(rel1.clone());
        g.add_relationship(rel2.clone());
        g.add_relationship(rel3.clone());

        prov.add_relationship(rel1.clone());
        prov.add_relationship(rel2.clone());
        prov.add_relationship(rel3.clone());

        let ev1 = make_evidence("kubernetes", "k8s", &a);
        let ev2 = make_evidence("kubernetes", "k8s", &b);
        prov.add_evidence(&rel1, ev1.id);
        prov.add_evidence(&rel2, ev2.id);

        let catalog = vec![ev1, ev2];
        let req = AnswerRequest::incoming(target, 3);

        let run1 = AnswerEngine::analyze(&g, &prov, &catalog, &req);
        let run2 = AnswerEngine::analyze(&g, &prov, &catalog, &req);
        let run3 = AnswerEngine::analyze(&g, &prov, &catalog, &req);

        assert_eq!(run1, run2);
        assert_eq!(run2, run3);
    }

    // -----------------------------------------------------------------------
    // 25. Self-Loop Handling
    // -----------------------------------------------------------------------
    #[test]
    fn test_25_self_loop_handling() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        let target = make_identity("target");
        let dep = make_identity("dep");

        // Self-loop on target
        g.add_relationship(Relationship::new(
            target.clone(),
            target.clone(),
            RelationshipKind::DEPENDS_ON,
        ));
        // Normal incoming relationship
        g.add_relationship(Relationship::new(
            dep.clone(),
            target.clone(),
            RelationshipKind::DEPENDS_ON,
        ));
        // Self-loop on dep
        g.add_relationship(Relationship::new(
            dep.clone(),
            dep.clone(),
            RelationshipKind::DEPENDS_ON,
        ));

        let req = AnswerRequest::incoming(target.clone(), 3);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        // Target self-loop is excluded from impacted resources
        assert_eq!(ans.summary.impacted_count, 1);
        assert_eq!(ans.impacted_resources[0].resource, dep);
        assert!(!ans.contains(&target));

        // Path only contains [dep, target]
        assert_eq!(ans.paths.len(), 1);
        assert_eq!(ans.paths[0].resources, vec![dep, target]);
    }

    // -----------------------------------------------------------------------
    // 26. Non-Propagating Relationship Boundary
    // -----------------------------------------------------------------------
    #[test]
    fn test_26_non_propagating_relationship_boundary() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        let target = make_identity("target");
        let owner = make_identity("owner");
        let node = make_identity("node");

        // Non-propagating kinds
        g.add_relationship(Relationship::new(
            owner,
            target.clone(),
            RelationshipKind::OWNS,
        ));
        g.add_relationship(Relationship::new(
            node,
            target.clone(),
            RelationshipKind::RUNS_ON,
        ));

        let req = AnswerRequest::incoming(target.clone(), 2);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(ans.summary.impacted_count, 0);
        assert!(ans.impacted_resources.is_empty());
        assert!(ans.relationships.is_empty());
        assert!(ans.paths.is_empty());
    }

    // -----------------------------------------------------------------------
    // 27. Outgoing Traversal Direction
    // -----------------------------------------------------------------------
    #[test]
    fn test_27_outgoing_traversal_direction() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        let target = make_identity("payments-api");
        let db = make_identity("orders-db");
        let storage = make_identity("storage-volume");

        let rel1 = Relationship::new(target.clone(), db.clone(), RelationshipKind::WRITES_TO);
        let rel2 = Relationship::new(db.clone(), storage.clone(), RelationshipKind::DEPENDS_ON);

        g.add_relationship(rel1.clone());
        g.add_relationship(rel2.clone());

        let req = AnswerRequest::outgoing(target.clone(), 2);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(ans.summary.impacted_count, 2);
        assert_eq!(ans.summary.direct_count, 1); // db
        assert_eq!(ans.summary.indirect_count, 1); // storage
        assert_eq!(ans.summary.max_depth, 2);

        assert_eq!(ans.paths.len(), 2);
        // Hop 1: target -> db
        assert_eq!(ans.paths[0].resources, vec![target.clone(), db.clone()]);
        assert_eq!(ans.paths[0].relationships, vec![rel1.clone()]);
        // Hop 2: target -> db -> storage
        assert_eq!(
            ans.paths[1].resources,
            vec![target.clone(), db.clone(), storage.clone()]
        );
        assert_eq!(ans.paths[1].relationships, vec![rel1.clone(), rel2.clone()]);
    }

    // -----------------------------------------------------------------------
    // 28. Multiple Relationship Kinds Between Same Endpoints
    // -----------------------------------------------------------------------
    #[test]
    fn test_28_multiple_relationship_kinds_between_same_endpoints() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        let target = make_identity("target");
        let caller = make_identity("caller");

        let rel1 = Relationship::new(caller.clone(), target.clone(), RelationshipKind::CALLS);
        let rel2 = Relationship::new(caller.clone(), target.clone(), RelationshipKind::READS_FROM);

        g.add_relationship(rel1.clone());
        g.add_relationship(rel2.clone());

        let req = AnswerRequest::incoming(target, 1);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        // Caller is 1 impacted resource
        assert_eq!(ans.summary.impacted_count, 1);
        // Both relationships are included
        assert_eq!(ans.relationships.len(), 2);
        // Both distinct paths exist
        assert_eq!(ans.paths.len(), 2);
        assert_eq!(ans.paths[0].relationships, vec![rel1]);
        assert_eq!(ans.paths[1].relationships, vec![rel2]);
    }

    // -----------------------------------------------------------------------
    // 29. Explanation Facts Structure Verification
    // -----------------------------------------------------------------------
    #[test]
    fn test_29_explanation_facts_structure_verification() {
        let mut g = Graph::new();
        let mut prov = ProvenanceStore::new();

        let target = make_identity("payments-api");
        let checkout = make_identity("checkout-service");
        let frontend = make_identity("frontend");

        let rel1 = Relationship::new(
            checkout.clone(),
            target.clone(),
            RelationshipKind::DEPENDS_ON,
        );
        let rel2 = Relationship::new(
            frontend.clone(),
            checkout.clone(),
            RelationshipKind::DEPENDS_ON,
        );

        g.add_relationship(rel1.clone());
        g.add_relationship(rel2.clone());
        prov.add_relationship(rel1.clone());
        prov.add_relationship(rel2.clone());

        let ev1 = make_evidence("kubernetes", "k8s-runtime", &checkout);
        let ev2 = make_evidence("kubernetes", "k8s-runtime", &frontend);
        prov.add_evidence(&rel1, ev1.id);
        prov.add_evidence(&rel2, ev2.id);

        let catalog = vec![ev1.clone(), ev2.clone()];
        let req = AnswerRequest::incoming(target.clone(), 2);
        let ans = AnswerEngine::analyze(&g, &prov, &catalog, &req);

        assert_eq!(ans.explanation_facts.len(), 2);

        // Fact 1: checkout -> target
        let f1 = &ans.explanation_facts[0];
        assert_eq!(f1.path.resources, vec![checkout.clone(), target.clone()]);
        assert_eq!(f1.relationships.len(), 1);
        assert_eq!(f1.relationships[0].relationship, rel1);
        assert_eq!(f1.evidence_ids, vec![ev1.id]);

        // Fact 2: frontend -> checkout -> target
        let f2 = &ans.explanation_facts[1];
        assert_eq!(
            f2.path.resources,
            vec![frontend.clone(), checkout.clone(), target.clone()]
        );
        assert_eq!(f2.relationships.len(), 2);
        assert_eq!(f2.relationships[0].relationship, rel2);
        assert_eq!(f2.relationships[1].relationship, rel1);
        assert_eq!(f2.evidence_ids.len(), 2);

        // Query facts by resource
        let facts_checkout = ans.facts_for(&checkout);
        assert_eq!(facts_checkout.len(), 2); // present in both paths

        let facts_frontend = ans.facts_for(&frontend);
        assert_eq!(facts_frontend.len(), 1); // present only in path 2
    }

    // -----------------------------------------------------------------------
    // 30. Target Resource Not Counted in Summary or Impacted
    // -----------------------------------------------------------------------
    #[test]
    fn test_30_target_not_in_impacted_or_counts() {
        let mut g = Graph::new();
        let prov = ProvenanceStore::new();

        let target = make_identity("target");
        let dep = make_identity("dep");

        // Bidirectional relationships: dep -> target and target -> dep
        g.add_relationship(Relationship::new(
            dep.clone(),
            target.clone(),
            RelationshipKind::DEPENDS_ON,
        ));
        g.add_relationship(Relationship::new(
            target.clone(),
            dep.clone(),
            RelationshipKind::DEPENDS_ON,
        ));

        let req = AnswerRequest::incoming(target.clone(), 2);
        let ans = AnswerEngine::analyze(&g, &prov, &[], &req);

        assert_eq!(ans.summary.impacted_count, 1);
        assert_eq!(ans.impacted_resources.len(), 1);
        assert_eq!(ans.impacted_resources[0].resource, dep);
        assert!(!ans.contains(&target));
        assert_eq!(ans.depth_of(&target), None);
    }
}
