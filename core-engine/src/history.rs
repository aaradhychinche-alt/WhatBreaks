//! WhatBreaks Relationship History & Current-State Derivation v1
//!
//! A deterministic, read-only subsystem for evaluating the observation history
//! and deriving the current operational state of a [`Relationship`].
//!
//! # Conceptual Architecture
//!
//! ```text
//! Evidence (Immutable factual observations)
//!    │
//!    ▼
//! Provenance (Relationship ──supports── EvidenceId associations)
//!    │
//!    ▼
//! Relationship State Derivation (Derives state & chronological history)
//!    │
//!    ▼
//! Graph / Impact / "What Breaks?" (Downstream analysis & blast radius)
//! ```
//!
//! # Core Semantic Principles
//!
//! 1. **Semantic Separation**:
//!    [`Relationship`] remains a lean semantic identity `(source, target, category, kind)`.
//!    It contains no lifecycle, active/inactive flags, timestamps, or confidence scores.
//!
//! 2. **Absence of Evidence is NOT Evidence of Absence**:
//!    A relationship that lacks recent observations is NOT marked inactive. Collectors
//!    may have partial scope, network partitions, or RBAC restrictions.
//!    Therefore:
//!    ```text
//!    "not observed" != "inactive"
//!    ```
//!
//! 3. **Conservative State Model**:
//!    The v1 state model distinguishes between:
//!    - [`RelationshipState::Supported`]: Supported by at least one valid observation.
//!    - [`RelationshipState::Unknown`]: Cannot safely determine state from supplied evidence.
//!
//!    Inactivity is never assumed or guessed.
//!
//! 4. **Deterministic Chronological Ordering**:
//!    Observations are ordered strictly by [`observed_at`](Evidence::observed_at) ascending.
//!    Ties are broken deterministically by [`EvidenceId`]. Older evidence arriving after
//!    newer evidence is sorted correctly.
//!
//! 5. **No Wall-Clock Dependency**:
//!    Derivation never inspects `Utc::now()`. All results are purely functional,
//!    reproducible, and deterministic based only on supplied inputs.

use std::collections::{HashMap, HashSet};
use std::fmt;

use chrono::{DateTime, Utc};

use crate::evidence::{Evidence, EvidenceId, EvidenceSource, ObservationType};
use crate::provenance::ProvenanceStore;
use crate::relationship::Relationship;

// ---------------------------------------------------------------------------
// Canonical Sorting Helpers
// ---------------------------------------------------------------------------

#[inline]
fn cmp_observation_chronological(
    a: &RelationshipObservation,
    b: &RelationshipObservation,
) -> std::cmp::Ordering {
    a.observed_at
        .cmp(&b.observed_at)
        .then_with(|| a.evidence_id.as_uuid().cmp(&b.evidence_id.as_uuid()))
}

// ---------------------------------------------------------------------------
// RelationshipState
// ---------------------------------------------------------------------------

/// The derived current operational state of a relationship.
///
/// # Critical Architectural Invariant
/// In WhatBreaks v1, relationship state distinguishes strictly between
/// [`RelationshipState::Supported`] (confirmed by known evidence) and
/// [`RelationshipState::Unknown`] (cannot be safely determined from known evidence).
///
/// **Absence of evidence is NOT evidence of absence.**
/// A relationship with no supporting evidence is `Unknown`, NEVER `Inactive`.
/// Collectors can have partial scope, network latency, or RBAC limits.
/// Therefore, WhatBreaks does not invent an `Inactive` state unless explicit,
/// unambiguous revocation evidence exists in the domain model.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum RelationshipState {
    /// Confirmed by at least one valid observation in the supplied evidence.
    Supported,
    /// Cannot safely determine current state from supplied evidence (e.g. no known observations).
    ///
    /// # Important
    /// Callers MUST NOT interpret `Unknown` as inactive, severed, or deleted.
    Unknown,
}

impl RelationshipState {
    /// Return `true` if the relationship is supported by known evidence.
    #[inline]
    pub fn is_supported(&self) -> bool {
        matches!(self, Self::Supported)
    }

    /// Return `true` if the relationship state is unknown.
    #[inline]
    pub fn is_unknown(&self) -> bool {
        matches!(self, Self::Unknown)
    }

    /// Explicitly documents and verifies that `Unknown` does NOT mean inactive.
    ///
    /// In v1, this always returns `false` because inactivity cannot be inferred
    /// from absence of evidence.
    #[inline]
    pub fn is_inactive(&self) -> bool {
        false
    }

    /// Return the string representation of the state.
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Supported => "Supported",
            Self::Unknown => "Unknown",
        }
    }
}

impl fmt::Display for RelationshipState {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

// ---------------------------------------------------------------------------
// RelationshipObservation
// ---------------------------------------------------------------------------

/// A single factual observation record supporting a relationship, ordered in history.
///
/// Captures observation provenance and timestamp without copying the full
/// arbitrary JSON payload of [`Evidence`], maintaining a lean memory footprint.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RelationshipObservation {
    /// Unique identifier of the supporting observation.
    pub evidence_id: EvidenceId,
    /// Timestamp when the observation was made (UTC).
    pub observed_at: DateTime<Utc>,
    /// Where the observation originated (provider + collector).
    pub source: EvidenceSource,
    /// Semantic nature of the observation (e.g. `RUNTIME_CONNECTION`).
    pub observation_type: ObservationType,
}

impl RelationshipObservation {
    /// Construct a new `RelationshipObservation`.
    pub fn new(
        evidence_id: EvidenceId,
        observed_at: DateTime<Utc>,
        source: EvidenceSource,
        observation_type: ObservationType,
    ) -> Self {
        Self {
            evidence_id,
            observed_at,
            source,
            observation_type,
        }
    }

    /// Construct a `RelationshipObservation` from an [`Evidence`] record.
    pub fn from_evidence(evidence: &Evidence) -> Self {
        Self {
            evidence_id: evidence.id,
            observed_at: evidence.observed_at,
            source: evidence.source.clone(),
            observation_type: evidence.observation_type.clone(),
        }
    }
}

// ---------------------------------------------------------------------------
// RelationshipHistory
// ---------------------------------------------------------------------------

/// Chronological observation history and derived state for a single relationship.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RelationshipHistory {
    /// The relationship being evaluated.
    relationship: Relationship,
    /// Chronological list of supporting observations (oldest to newest).
    observations: Vec<RelationshipObservation>,
}

impl RelationshipHistory {
    /// Construct a new `RelationshipHistory` from a relationship and a list of observations.
    ///
    /// Deduplicates observations by [`EvidenceId`] and sorts them strictly chronologically
    /// ascending, breaking timestamp ties deterministically by [`EvidenceId`].
    pub fn new(relationship: Relationship, observations: Vec<RelationshipObservation>) -> Self {
        let mut seen = HashSet::new();
        let mut unique: Vec<RelationshipObservation> = Vec::with_capacity(observations.len());
        for obs in observations {
            if seen.insert(obs.evidence_id) {
                unique.push(obs);
            }
        }
        unique.sort_by(cmp_observation_chronological);
        Self {
            relationship,
            observations: unique,
        }
    }

    /// Construct an empty `RelationshipHistory` with state `Unknown`.
    pub fn empty(relationship: Relationship) -> Self {
        Self {
            relationship,
            observations: Vec::new(),
        }
    }

    /// Return a reference to the relationship.
    pub fn relationship(&self) -> &Relationship {
        &self.relationship
    }

    /// Return the chronological list of supporting observations (oldest to newest).
    pub fn observations(&self) -> &[RelationshipObservation] {
        &self.observations
    }

    /// Return the number of distinct supporting observations.
    pub fn observation_count(&self) -> usize {
        self.observations.len()
    }

    /// Return `true` if there are no supporting observations.
    pub fn is_empty(&self) -> bool {
        self.observations.is_empty()
    }

    /// Return all supporting evidence IDs in chronological order.
    pub fn evidence_ids(&self) -> Vec<EvidenceId> {
        self.observations.iter().map(|o| o.evidence_id).collect()
    }

    /// Return the oldest (first) supporting observation, if any.
    pub fn oldest_observation(&self) -> Option<&RelationshipObservation> {
        self.observations.first()
    }

    /// Return the newest (latest) supporting observation, if any.
    pub fn newest_observation(&self) -> Option<&RelationshipObservation> {
        self.observations.last()
    }

    /// Return the timestamp of the earliest known observation.
    pub fn first_observed_at(&self) -> Option<DateTime<Utc>> {
        self.observations.first().map(|o| o.observed_at)
    }

    /// Return the timestamp of the latest known observation.
    pub fn last_observed_at(&self) -> Option<DateTime<Utc>> {
        self.observations.last().map(|o| o.observed_at)
    }

    /// Derive the current state of this relationship.
    ///
    /// - If at least one valid observation supports the relationship: [`RelationshipState::Supported`].
    /// - If no observations support the relationship: [`RelationshipState::Unknown`].
    ///
    /// # Critical Invariant
    /// A relationship with zero observations returns `Unknown`, NOT `Inactive`.
    pub fn state(&self) -> RelationshipState {
        if self.observations.is_empty() {
            RelationshipState::Unknown
        } else {
            RelationshipState::Supported
        }
    }

    /// Return `true` if the relationship is supported by known observations.
    pub fn is_supported(&self) -> bool {
        self.state().is_supported()
    }

    /// Return `true` if the relationship's state is unknown.
    pub fn is_unknown(&self) -> bool {
        self.state().is_unknown()
    }
}

// ---------------------------------------------------------------------------
// RelationshipStateDerivation
// ---------------------------------------------------------------------------

/// Stateless engine for deriving relationship observation history and current state.
#[derive(Debug, Clone, Copy, Default)]
pub struct RelationshipStateDerivation;

impl RelationshipStateDerivation {
    /// Derive relationship observation history and state using a [`ProvenanceStore`]
    /// and a catalog of available [`Evidence`] records.
    ///
    /// # Semantics
    /// - Identifies all `EvidenceId`s associated with `relationship` in `provenance`.
    /// - Matches each `EvidenceId` with the corresponding record in `evidence_catalog`.
    /// - Builds a chronologically sorted, deduplicated [`RelationshipHistory`].
    /// - If `relationship` is not in `provenance` or has no matching evidence, returns
    ///   an empty history with state [`RelationshipState::Unknown`].
    /// - Read-only: does not mutate `relationship`, `provenance`, or `evidence_catalog`.
    pub fn derive_history(
        relationship: &Relationship,
        provenance: &ProvenanceStore,
        evidence_catalog: &[Evidence],
    ) -> RelationshipHistory {
        let evidence_ids = provenance.evidence_for(relationship);
        if evidence_ids.is_empty() {
            return RelationshipHistory::empty(relationship.clone());
        }

        let id_set: HashSet<EvidenceId> = evidence_ids.into_iter().collect();

        let observations: Vec<RelationshipObservation> = evidence_catalog
            .iter()
            .filter(|ev| id_set.contains(&ev.id))
            .map(RelationshipObservation::from_evidence)
            .collect();

        RelationshipHistory::new(relationship.clone(), observations)
    }

    /// Derive current state for a relationship.
    pub fn derive_state(
        relationship: &Relationship,
        provenance: &ProvenanceStore,
        evidence_catalog: &[Evidence],
    ) -> RelationshipState {
        Self::derive_history(relationship, provenance, evidence_catalog).state()
    }

    /// Derive history and state directly from an explicit list of observations.
    pub fn derive_from_observations(
        relationship: Relationship,
        observations: Vec<RelationshipObservation>,
    ) -> RelationshipHistory {
        RelationshipHistory::new(relationship, observations)
    }

    /// Derive histories and states for all registered relationships in `provenance`,
    /// returned in deterministic canonical relationship order.
    pub fn derive_all(
        provenance: &ProvenanceStore,
        evidence_catalog: &[Evidence],
    ) -> Vec<RelationshipHistory> {
        // Build fast lookup index for the evidence catalog: O(N)
        let evidence_map: HashMap<EvidenceId, &Evidence> =
            evidence_catalog.iter().map(|ev| (ev.id, ev)).collect();

        let registered = provenance.registered_relationships();

        registered
            .into_iter()
            .map(|rel| {
                let ev_ids = provenance.evidence_for(&rel);
                let observations: Vec<RelationshipObservation> = ev_ids
                    .into_iter()
                    .filter_map(|id| {
                        evidence_map
                            .get(&id)
                            .map(|&ev| RelationshipObservation::from_evidence(ev))
                    })
                    .collect();
                RelationshipHistory::new(rel, observations)
            })
            .collect()
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;
    use serde_json::json;

    use crate::evidence::{CollectorId, ObservationType};
    use crate::graph::Graph;
    use crate::relationship::RelationshipKind;
    use crate::resource::{Provider, ResourceIdentity, ResourceKind};

    fn make_identity(name: &str) -> ResourceIdentity {
        ResourceIdentity::new(Provider::new("kubernetes"), ResourceKind::new("pod"), name)
    }

    fn make_rel(src: &str, tgt: &str, kind: RelationshipKind) -> Relationship {
        Relationship::new(make_identity(src), make_identity(tgt), kind)
    }

    fn make_evidence(id: EvidenceId, timestamp_secs: i64) -> Evidence {
        Evidence {
            id,
            source: EvidenceSource::new(
                Provider::new("kubernetes"),
                CollectorId::from_static("test"),
            ),
            observed_at: Utc.timestamp_opt(timestamp_secs, 0).unwrap(),
            observation_type: ObservationType::RUNTIME_CONNECTION,
            subject: make_identity("subject"),
            data: json!({"test": true}),
        }
    }

    fn make_obs(id: EvidenceId, timestamp_secs: i64) -> RelationshipObservation {
        RelationshipObservation::new(
            id,
            Utc.timestamp_opt(timestamp_secs, 0).unwrap(),
            EvidenceSource::new(
                Provider::new("kubernetes"),
                CollectorId::from_static("test"),
            ),
            ObservationType::RUNTIME_CONNECTION,
        )
    }

    // -----------------------------------------------------------------------
    // 1. Relationship with one supporting observation
    // -----------------------------------------------------------------------
    #[test]
    fn test_relationship_with_one_supporting_observation() {
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let e1_id = EvidenceId::new();
        let e1 = make_evidence(e1_id, 1000);

        let mut prov = ProvenanceStore::new();
        prov.add_relationship(rel.clone());
        prov.add_evidence(&rel, e1_id);

        let history = RelationshipStateDerivation::derive_history(&rel, &prov, &[e1]);

        assert_eq!(history.relationship(), &rel);
        assert_eq!(history.observation_count(), 1);
        assert!(!history.is_empty());
        assert_eq!(history.state(), RelationshipState::Supported);
        assert!(history.is_supported());
        assert!(!history.is_unknown());
        assert_eq!(
            history.first_observed_at(),
            Some(Utc.timestamp_opt(1000, 0).unwrap())
        );
        assert_eq!(
            history.last_observed_at(),
            Some(Utc.timestamp_opt(1000, 0).unwrap())
        );
    }

    // -----------------------------------------------------------------------
    // 2. Relationship with multiple observations
    // -----------------------------------------------------------------------
    #[test]
    fn test_relationship_with_multiple_observations() {
        let rel = make_rel("api", "db", RelationshipKind::DEPENDS_ON);
        let e1_id = EvidenceId::new();
        let e2_id = EvidenceId::new();
        let e3_id = EvidenceId::new();

        let e1 = make_evidence(e1_id, 100);
        let e2 = make_evidence(e2_id, 200);
        let e3 = make_evidence(e3_id, 300);

        let mut prov = ProvenanceStore::new();
        prov.add_relationship(rel.clone());
        prov.add_evidence(&rel, e1_id);
        prov.add_evidence(&rel, e2_id);
        prov.add_evidence(&rel, e3_id);

        let history = RelationshipStateDerivation::derive_history(&rel, &prov, &[e1, e2, e3]);

        assert_eq!(history.observation_count(), 3);
        assert_eq!(history.state(), RelationshipState::Supported);
        assert_eq!(
            history.first_observed_at(),
            Some(Utc.timestamp_opt(100, 0).unwrap())
        );
        assert_eq!(
            history.last_observed_at(),
            Some(Utc.timestamp_opt(300, 0).unwrap())
        );
        assert_eq!(history.oldest_observation().unwrap().evidence_id, e1_id);
        assert_eq!(history.newest_observation().unwrap().evidence_id, e3_id);
    }

    // -----------------------------------------------------------------------
    // 3. Chronological ordering (oldest to newest)
    // -----------------------------------------------------------------------
    #[test]
    fn test_chronological_ordering() {
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let e1 = make_obs(EvidenceId::new(), 100);
        let e2 = make_obs(EvidenceId::new(), 200);
        let e3 = make_obs(EvidenceId::new(), 300);

        // Supply observations in random order: e2, e3, e1
        let history = RelationshipStateDerivation::derive_from_observations(
            rel,
            vec![e2.clone(), e3.clone(), e1.clone()],
        );

        let obs = history.observations();
        assert_eq!(obs.len(), 3);
        assert_eq!(obs[0], e1);
        assert_eq!(obs[1], e2);
        assert_eq!(obs[2], e3);
    }

    // -----------------------------------------------------------------------
    // 4. Identical timestamps (tie-breaker determinism)
    // -----------------------------------------------------------------------
    #[test]
    fn test_identical_timestamps() {
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let id1 = EvidenceId::new();
        let id2 = EvidenceId::new();
        let id3 = EvidenceId::new();

        // Same timestamp: 500
        let o1 = make_obs(id1, 500);
        let o2 = make_obs(id2, 500);
        let o3 = make_obs(id3, 500);

        let h1 = RelationshipStateDerivation::derive_from_observations(
            rel.clone(),
            vec![o1.clone(), o2.clone(), o3.clone()],
        );
        let h2 = RelationshipStateDerivation::derive_from_observations(
            rel,
            vec![o3.clone(), o1.clone(), o2.clone()],
        );

        assert_eq!(h1.observations(), h2.observations());
        assert_eq!(h1, h2);
    }

    // -----------------------------------------------------------------------
    // 5. Duplicate evidence IDs
    // -----------------------------------------------------------------------
    #[test]
    fn test_duplicate_evidence_ids() {
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let id1 = EvidenceId::new();
        let o1_a = make_obs(id1, 100);
        let o1_b = make_obs(id1, 100);

        let history = RelationshipStateDerivation::derive_from_observations(rel, vec![o1_a, o1_b]);

        assert_eq!(history.observation_count(), 1);
        assert_eq!(history.evidence_ids(), vec![id1]);
    }

    // -----------------------------------------------------------------------
    // 6. Older evidence arriving after newer evidence
    // -----------------------------------------------------------------------
    #[test]
    fn test_older_evidence_arriving_after_newer_evidence() {
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let newer = make_obs(EvidenceId::new(), 2000);
        let older = make_obs(EvidenceId::new(), 1000);

        // Newer arrived first, older arrived later
        let history = RelationshipStateDerivation::derive_from_observations(
            rel,
            vec![newer.clone(), older.clone()],
        );

        // Chronological order must place older first
        assert_eq!(history.observations()[0], older);
        assert_eq!(history.observations()[1], newer);
        assert_eq!(
            history.first_observed_at(),
            Some(Utc.timestamp_opt(1000, 0).unwrap())
        );
        assert_eq!(
            history.last_observed_at(),
            Some(Utc.timestamp_opt(2000, 0).unwrap())
        );
    }

    // -----------------------------------------------------------------------
    // 7. Relationship with no evidence
    // -----------------------------------------------------------------------
    #[test]
    fn test_relationship_with_no_evidence() {
        let rel = make_rel("isolated", "service", RelationshipKind::CALLS);
        let prov = ProvenanceStore::new();

        let history = RelationshipStateDerivation::derive_history(&rel, &prov, &[]);

        assert_eq!(history.relationship(), &rel);
        assert_eq!(history.observation_count(), 0);
        assert!(history.is_empty());
        assert_eq!(history.first_observed_at(), None);
        assert_eq!(history.last_observed_at(), None);
        assert_eq!(history.oldest_observation(), None);
        assert_eq!(history.newest_observation(), None);

        // State is Unknown, NOT Inactive!
        assert_eq!(history.state(), RelationshipState::Unknown);
        assert!(history.is_unknown());
        assert!(!history.is_supported());
        assert!(!history.state().is_inactive());
    }

    // -----------------------------------------------------------------------
    // 8. Evidence associated with multiple relationships
    // -----------------------------------------------------------------------
    #[test]
    fn test_evidence_associated_with_multiple_relationships() {
        let r1 = make_rel("a", "b", RelationshipKind::CALLS);
        let r2 = make_rel("c", "d", RelationshipKind::DEPENDS_ON);
        let eid = EvidenceId::new();
        let ev = make_evidence(eid, 500);

        let mut prov = ProvenanceStore::new();
        prov.add_relationship(r1.clone());
        prov.add_relationship(r2.clone());
        prov.add_evidence(&r1, eid);
        prov.add_evidence(&r2, eid);

        let h1 = RelationshipStateDerivation::derive_history(&r1, &prov, std::slice::from_ref(&ev));
        let h2 = RelationshipStateDerivation::derive_history(&r2, &prov, &[ev]);

        assert_eq!(h1.state(), RelationshipState::Supported);
        assert_eq!(h2.state(), RelationshipState::Supported);
        assert_eq!(h1.evidence_ids(), vec![eid]);
        assert_eq!(h2.evidence_ids(), vec![eid]);
    }

    // -----------------------------------------------------------------------
    // 9. Multiple relationships sharing evidence
    // -----------------------------------------------------------------------
    #[test]
    fn test_multiple_relationships_sharing_evidence_derive_all() {
        let r1 = make_rel("a", "b", RelationshipKind::CALLS);
        let r2 = make_rel("c", "d", RelationshipKind::DEPENDS_ON);
        let shared_id = EvidenceId::new();
        let ev = make_evidence(shared_id, 750);

        let mut prov = ProvenanceStore::new();
        prov.add_relationship(r1.clone());
        prov.add_relationship(r2.clone());
        prov.add_evidence(&r1, shared_id);
        prov.add_evidence(&r2, shared_id);

        let all = RelationshipStateDerivation::derive_all(&prov, &[ev]);

        assert_eq!(all.len(), 2);
        assert!(all.iter().all(|h| h.is_supported()));
        assert!(all
            .iter()
            .all(|h| h.last_observed_at() == Some(Utc.timestamp_opt(750, 0).unwrap())));
    }

    // -----------------------------------------------------------------------
    // 10. Deterministic output
    // -----------------------------------------------------------------------
    #[test]
    fn test_deterministic_output() {
        let r = make_rel("a", "b", RelationshipKind::CALLS);
        let e1 = make_obs(EvidenceId::new(), 100);
        let e2 = make_obs(EvidenceId::new(), 200);
        let e3 = make_obs(EvidenceId::new(), 300);

        let run1 = RelationshipStateDerivation::derive_from_observations(
            r.clone(),
            vec![e3.clone(), e1.clone(), e2.clone()],
        );
        let run2 = RelationshipStateDerivation::derive_from_observations(
            r.clone(),
            vec![e1.clone(), e2.clone(), e3.clone()],
        );
        let run3 = RelationshipStateDerivation::derive_from_observations(
            r,
            vec![e2.clone(), e3.clone(), e1.clone()],
        );

        assert_eq!(run1, run2);
        assert_eq!(run2, run3);
    }

    // -----------------------------------------------------------------------
    // 11. Empty input
    // -----------------------------------------------------------------------
    #[test]
    fn test_empty_input() {
        let prov = ProvenanceStore::new();
        let all = RelationshipStateDerivation::derive_all(&prov, &[]);

        assert!(all.is_empty());
    }

    // -----------------------------------------------------------------------
    // 12. Unknown state is not treated as inactive
    // -----------------------------------------------------------------------
    #[test]
    fn test_unknown_state_is_not_treated_as_inactive() {
        let state = RelationshipState::Unknown;

        assert!(state.is_unknown());
        assert!(!state.is_supported());
        // Inactivity is NEVER inferred
        assert!(!state.is_inactive());
        assert_eq!(state.as_str(), "Unknown");
        assert_eq!(format!("{}", state), "Unknown");
    }

    // -----------------------------------------------------------------------
    // 13. Derivation does not mutate Relationship
    // -----------------------------------------------------------------------
    #[test]
    fn test_derivation_does_not_mutate_relationship() {
        let src = make_identity("svc_a");
        let tgt = make_identity("svc_b");
        let rel = Relationship::new(src.clone(), tgt.clone(), RelationshipKind::CALLS);

        let orig_identity = rel.identity();
        let orig_category = rel.category;
        let orig_kind = rel.kind.clone();

        let prov = ProvenanceStore::new();
        let history = RelationshipStateDerivation::derive_history(&rel, &prov, &[]);

        assert_eq!(history.relationship().identity(), orig_identity);
        assert_eq!(history.relationship().category, orig_category);
        assert_eq!(history.relationship().kind, orig_kind);
    }

    // -----------------------------------------------------------------------
    // 14. Derivation does not mutate Evidence
    // -----------------------------------------------------------------------
    #[test]
    fn test_derivation_does_not_mutate_evidence() {
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let eid = EvidenceId::new();
        let ev = make_evidence(eid, 1234);

        let orig_id = ev.id;
        let orig_time = ev.observed_at;
        let orig_data = ev.data.clone();

        let mut prov = ProvenanceStore::new();
        prov.add_relationship(rel.clone());
        prov.add_evidence(&rel, eid);

        let _ = RelationshipStateDerivation::derive_history(&rel, &prov, std::slice::from_ref(&ev));

        assert_eq!(ev.id, orig_id);
        assert_eq!(ev.observed_at, orig_time);
        assert_eq!(ev.data, orig_data);
    }

    // -----------------------------------------------------------------------
    // 15. Derivation does not mutate ProvenanceStore
    // -----------------------------------------------------------------------
    #[test]
    fn test_derivation_does_not_mutate_provenance_store() {
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let eid = EvidenceId::new();
        let ev = make_evidence(eid, 100);

        let mut prov = ProvenanceStore::new();
        prov.add_relationship(rel.clone());
        prov.add_evidence(&rel, eid);

        let orig_rel_count = prov.relationship_count();
        let orig_evidence = prov.evidence_for(&rel);

        let _ = RelationshipStateDerivation::derive_history(&rel, &prov, &[ev]);

        assert_eq!(prov.relationship_count(), orig_rel_count);
        assert_eq!(prov.evidence_for(&rel), orig_evidence);
        assert!(prov.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 16. Derivation does not mutate Graph
    // -----------------------------------------------------------------------
    #[test]
    fn test_derivation_does_not_mutate_graph() {
        let mut graph = Graph::new();
        let rel = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        graph.add_relationship(rel.clone());

        let initial_resources = graph.resources();
        let initial_relationships = graph.relationships();
        let initial_res_count = graph.resource_count();
        let initial_rel_count = graph.relationship_count();

        let prov = ProvenanceStore::new();
        let _ = RelationshipStateDerivation::derive_history(&rel, &prov, &[]);

        assert_eq!(graph.resources(), initial_resources);
        assert_eq!(graph.relationships(), initial_relationships);
        assert_eq!(graph.resource_count(), initial_res_count);
        assert_eq!(graph.relationship_count(), initial_rel_count);
        assert!(graph.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // CRITICAL TEST: Absence of evidence != Inactive
    // -----------------------------------------------------------------------
    #[test]
    fn test_critical_absence_of_evidence_not_inactive() {
        let rel = make_rel("frontend", "backend", RelationshipKind::CALLS);
        let mut prov = ProvenanceStore::new();
        prov.add_relationship(rel.clone());

        // Zero observations known for this relationship
        let history = RelationshipStateDerivation::derive_history(&rel, &prov, &[]);

        // Must evaluate to Unknown
        assert_eq!(history.state(), RelationshipState::Unknown);
        assert!(history.is_unknown());
        assert!(!history.is_supported());

        // Under NO circumstances is it inactive
        assert!(!history.state().is_inactive());
    }

    // -----------------------------------------------------------------------
    // 17. Send + Sync compile-time verification
    // -----------------------------------------------------------------------
    #[test]
    fn test_send_sync_thread_safety() {
        fn assert_send_sync<T: Send + Sync>() {}
        assert_send_sync::<RelationshipState>();
        assert_send_sync::<RelationshipObservation>();
        assert_send_sync::<RelationshipHistory>();
        assert_send_sync::<RelationshipStateDerivation>();
    }

    // -----------------------------------------------------------------------
    // 18. Supported state accessors
    // -----------------------------------------------------------------------
    #[test]
    fn test_supported_state_accessors() {
        let state = RelationshipState::Supported;
        assert!(state.is_supported());
        assert!(!state.is_unknown());
        assert!(!state.is_inactive());
        assert_eq!(state.as_str(), "Supported");
        assert_eq!(format!("{}", state), "Supported");
    }
}
