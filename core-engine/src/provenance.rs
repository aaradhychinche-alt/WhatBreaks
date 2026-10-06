//! WhatBreaks Relationship Provenance v1
//!
//! A lightweight, deterministic, in-memory association layer connecting
//! [`Relationship`](crate::relationship::Relationship) identities with
//! [`EvidenceId`](crate::evidence::EvidenceId) observation identifiers.
//!
//! # Purpose
//!
//! Provenance answers the foundational question:
//!
//! *"Why does this relationship exist, and what factual observations support it?"*
//!
//! ```text
//! Evidence (Observation)
//!         │
//!         ▼
//!     Discovery (Derives relationships from evidence)
//!         │
//!         ▼
//!    Relationship (Semantic link between resource identities)
//!         ▲
//!         │ (supports)
//!     Provenance (Relationship ──supports── EvidenceId dual-index)
//! ```
//!
//! # Architectural Principles
//!
//! - **Separation of Concerns**: [`Relationship`](crate::relationship::Relationship)
//!   remains a lean, unpolluted semantic identity `(source, target, category, kind)`.
//!   It contains no timestamps, collectors, confidence scores, or evidence lists.
//! - **Reference-Only Storage**: [`ProvenanceStore`] stores only [`EvidenceId`] values,
//!   never full [`Evidence`](crate::evidence::Evidence) objects. The authoritative
//!   evidence records remain outside this store.
//! - **Dual-Index Structure**: Provides fast $O(1)$ lookups in both directions:
//!   - Forward: `Relationship -> HashSet<EvidenceId>`
//!   - Reverse: `EvidenceId -> HashSet<Relationship>`
//! - **Explicit Registration**: A relationship must be explicitly registered via
//!   [`ProvenanceStore::add_relationship`] before evidence can be associated with it.
//!   Unregistered relationships reject evidence additions and are never silently created.
//! - **Deterministic Queries**: All public query methods return results in canonical,
//!   deterministic order independent of internal hash iteration order.
//! - **Provider-Agnostic & In-Memory**: Contains no provider-specific logic, no database
//!   persistence, no network calls, and no async runtimes.

use std::collections::{HashMap, HashSet};

use crate::evidence::EvidenceId;
use crate::relationship::Relationship;

// ---------------------------------------------------------------------------
// Canonical Sorting Helpers
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// RelationshipEvidence (Read-Oriented Representation)
// ---------------------------------------------------------------------------

/// A read-oriented representation of a relationship and all supporting evidence IDs.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RelationshipEvidence {
    /// The infrastructure relationship.
    pub relationship: Relationship,
    /// All evidence IDs that support this relationship, in deterministic canonical order.
    pub supporting_evidence: Vec<EvidenceId>,
}

impl RelationshipEvidence {
    /// Construct a new `RelationshipEvidence` record.
    pub fn new(relationship: Relationship, supporting_evidence: Vec<EvidenceId>) -> Self {
        Self {
            relationship,
            supporting_evidence,
        }
    }

    /// Return the number of supporting evidence IDs.
    pub fn evidence_count(&self) -> usize {
        self.supporting_evidence.len()
    }

    /// Return `true` if at least one evidence ID supports this relationship.
    pub fn has_evidence(&self) -> bool {
        !self.supporting_evidence.is_empty()
    }
}

// ---------------------------------------------------------------------------
// ProvenanceStore
// ---------------------------------------------------------------------------

/// In-memory dual-index store maintaining associations between relationships and evidence IDs.
///
/// Ensures $O(1)$ forward (`Relationship -> EvidenceIds`) and reverse
/// (`EvidenceId -> Relationships`) lookups with guaranteed deterministic query ordering.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct ProvenanceStore {
    /// Forward index: Relationship -> set of supporting EvidenceIds.
    forward: HashMap<Relationship, HashSet<EvidenceId>>,
    /// Reverse index: EvidenceId -> set of Relationships it supports.
    reverse: HashMap<EvidenceId, HashSet<Relationship>>,
}

impl ProvenanceStore {
    /// Construct a new, empty `ProvenanceStore`.
    pub fn new() -> Self {
        Self {
            forward: HashMap::new(),
            reverse: HashMap::new(),
        }
    }

    /// Register a relationship in the store.
    ///
    /// A relationship must be registered before evidence can be associated with it.
    /// Registration is idempotent: returns `true` if the relationship was newly registered,
    /// or `false` if it was already present.
    pub fn add_relationship(&mut self, relationship: Relationship) -> bool {
        match self.forward.entry(relationship) {
            std::collections::hash_map::Entry::Vacant(entry) => {
                entry.insert(HashSet::new());
                true
            }
            std::collections::hash_map::Entry::Occupied(_) => false,
        }
    }

    /// Associate an evidence ID with a registered relationship.
    ///
    /// # Semantics
    /// - If `relationship` is NOT registered in the store, returns `false` and does NOT
    ///   create the relationship.
    /// - Association is idempotent: returns `true` if the evidence was newly associated,
    ///   or `false` if this evidence ID was already associated with the relationship.
    /// - Updates both forward and reverse indexes consistently.
    pub fn add_evidence(&mut self, relationship: &Relationship, evidence_id: EvidenceId) -> bool {
        match self.forward.get_mut(relationship) {
            Some(evidence_set) => {
                if evidence_set.insert(evidence_id) {
                    self.reverse
                        .entry(evidence_id)
                        .or_default()
                        .insert(relationship.clone());
                    true
                } else {
                    false
                }
            }
            None => false,
        }
    }

    /// Return all evidence IDs supporting `relationship`, in deterministic canonical order.
    ///
    /// Returns an empty vector if `relationship` is not registered or has no associated evidence.
    pub fn evidence_for(&self, relationship: &Relationship) -> Vec<EvidenceId> {
        match self.forward.get(relationship) {
            Some(set) => {
                let mut list: Vec<EvidenceId> = set.iter().copied().collect();
                list.sort_by(cmp_evidence_id);
                list
            }
            None => Vec::new(),
        }
    }

    /// Return all relationships supported by `evidence_id`, in deterministic canonical order.
    ///
    /// Returns an empty vector if `evidence_id` is unknown or supports no relationships.
    pub fn relationships_for_evidence(&self, evidence_id: &EvidenceId) -> Vec<Relationship> {
        match self.reverse.get(evidence_id) {
            Some(set) => {
                let mut list: Vec<Relationship> = set.iter().cloned().collect();
                list.sort_by(cmp_relationship);
                list
            }
            None => Vec::new(),
        }
    }

    /// Check if a relationship is registered in the store.
    pub fn contains_relationship(&self, relationship: &Relationship) -> bool {
        self.forward.contains_key(relationship)
    }

    /// Check if a specific evidence ID is associated with a relationship.
    pub fn contains_evidence(&self, relationship: &Relationship, evidence_id: &EvidenceId) -> bool {
        self.forward
            .get(relationship)
            .is_some_and(|set| set.contains(evidence_id))
    }

    /// Remove a relationship and all its provenance associations from the store.
    ///
    /// # Semantics
    /// - Removes the relationship from the forward index.
    /// - Removes the relationship from the reverse index for each associated evidence ID.
    /// - If an evidence ID was shared with other relationships, its associations with
    ///   those other relationships remain intact.
    /// - Does not delete evidence IDs themselves.
    /// - Returns `true` if the relationship was found and removed, or `false` if not present.
    pub fn remove_relationship(&mut self, relationship: &Relationship) -> bool {
        match self.forward.remove(relationship) {
            Some(evidence_set) => {
                for evidence_id in evidence_set {
                    if let Some(rel_set) = self.reverse.get_mut(&evidence_id) {
                        rel_set.remove(relationship);
                        if rel_set.is_empty() {
                            self.reverse.remove(&evidence_id);
                        }
                    }
                }
                true
            }
            None => false,
        }
    }

    /// Clear all relationships and provenance associations from the store.
    pub fn clear(&mut self) {
        self.forward.clear();
        self.reverse.clear();
    }

    /// Return the number of registered relationships in the store.
    pub fn len(&self) -> usize {
        self.forward.len()
    }

    /// Return `true` if the store contains no registered relationships.
    pub fn is_empty(&self) -> bool {
        self.forward.is_empty()
    }

    /// Return the number of registered relationships.
    pub fn relationship_count(&self) -> usize {
        self.forward.len()
    }

    /// Return the number of supporting evidence IDs for a registered relationship.
    pub fn evidence_count_for(&self, relationship: &Relationship) -> usize {
        self.forward.get(relationship).map_or(0, |set| set.len())
    }

    /// Return a [`RelationshipEvidence`] view for `relationship` if it is registered.
    pub fn relationship_evidence(
        &self,
        relationship: &Relationship,
    ) -> Option<RelationshipEvidence> {
        if self.contains_relationship(relationship) {
            Some(RelationshipEvidence::new(
                relationship.clone(),
                self.evidence_for(relationship),
            ))
        } else {
            None
        }
    }

    /// Return all registered relationships with their supporting evidence, in deterministic order.
    pub fn all_relationship_evidence(&self) -> Vec<RelationshipEvidence> {
        let mut relationships: Vec<Relationship> = self.forward.keys().cloned().collect();
        relationships.sort_by(cmp_relationship);

        relationships
            .into_iter()
            .map(|rel| {
                let evidence = self.evidence_for(&rel);
                RelationshipEvidence::new(rel, evidence)
            })
            .collect()
    }

    /// Return all registered relationships in deterministic canonical order.
    pub fn registered_relationships(&self) -> Vec<Relationship> {
        let mut list: Vec<Relationship> = self.forward.keys().cloned().collect();
        list.sort_by(cmp_relationship);
        list
    }

    /// Return all distinct evidence IDs currently associated with at least one relationship,
    /// in deterministic canonical order.
    pub fn all_evidence_ids(&self) -> Vec<EvidenceId> {
        let mut list: Vec<EvidenceId> = self.reverse.keys().copied().collect();
        list.sort_by(cmp_evidence_id);
        list
    }

    /// Verify bidirectional consistency between forward and reverse indexes.
    ///
    /// Returns `Ok(())` if indexes are fully consistent, or `Err` with a diagnostic message.
    pub fn check_invariants(&self) -> Result<(), String> {
        // 1. Every (R, E) in forward must exist in reverse
        for (rel, ev_set) in &self.forward {
            for ev_id in ev_set {
                match self.reverse.get(ev_id) {
                    Some(rels) if rels.contains(rel) => {}
                    Some(_) => {
                        return Err(format!(
                            "Invariant violation: forward index has relationship {:?} with evidence {:?}, but reverse index entry does not contain this relationship",
                            rel, ev_id
                        ));
                    }
                    None => {
                        return Err(format!(
                            "Invariant violation: forward index has relationship {:?} with evidence {:?}, but reverse index has no entry for this evidence",
                            rel, ev_id
                        ));
                    }
                }
            }
        }

        // 2. Every (E, R) in reverse must exist in forward
        for (ev_id, rel_set) in &self.reverse {
            if rel_set.is_empty() {
                return Err(format!(
                    "Invariant violation: reverse index has an empty relationship set for evidence {:?}",
                    ev_id
                ));
            }
            for rel in rel_set {
                match self.forward.get(rel) {
                    Some(evs) if evs.contains(ev_id) => {}
                    Some(_) => {
                        return Err(format!(
                            "Invariant violation: reverse index has evidence {:?} associated with relationship {:?}, but forward index does not contain this evidence",
                            ev_id, rel
                        ));
                    }
                    None => {
                        return Err(format!(
                            "Invariant violation: reverse index has evidence {:?} associated with unregistered relationship {:?}",
                            ev_id, rel
                        ));
                    }
                }
            }
        }

        Ok(())
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::graph::Graph;
    use crate::relationship::{RelationshipCategory, RelationshipKind};
    use crate::resource::{Provider, ResourceIdentity, ResourceKind};

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
    // 1. Empty store
    // -----------------------------------------------------------------------
    #[test]
    fn test_empty_store() {
        let store = ProvenanceStore::new();
        assert!(store.is_empty());
        assert_eq!(store.len(), 0);
        assert_eq!(store.relationship_count(), 0);
        assert!(store.registered_relationships().is_empty());
        assert!(store.all_evidence_ids().is_empty());
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 2. New store is empty
    // -----------------------------------------------------------------------
    #[test]
    fn test_new_store_is_empty() {
        let store_new = ProvenanceStore::new();
        let store_default = ProvenanceStore::default();
        assert_eq!(store_new, store_default);
        assert!(store_default.is_empty());
    }

    // -----------------------------------------------------------------------
    // 3. Add relationship
    // -----------------------------------------------------------------------
    #[test]
    fn test_add_relationship() {
        let mut store = ProvenanceStore::new();
        let rel = make_rel("a", "b", RelationshipKind::CALLS);

        assert!(store.add_relationship(rel.clone()));
        assert_eq!(store.len(), 1);
        assert!(store.contains_relationship(&rel));
        assert!(store.evidence_for(&rel).is_empty());
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 4. Duplicate relationship
    // -----------------------------------------------------------------------
    #[test]
    fn test_duplicate_relationship() {
        let mut store = ProvenanceStore::new();
        let rel = make_rel("a", "b", RelationshipKind::CALLS);

        assert!(store.add_relationship(rel.clone()));
        assert!(!store.add_relationship(rel.clone()));
        assert_eq!(store.len(), 1);
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 5. Add evidence
    // -----------------------------------------------------------------------
    #[test]
    fn test_add_evidence() {
        let mut store = ProvenanceStore::new();
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let e1 = EvidenceId::new();

        store.add_relationship(rel.clone());
        assert!(store.add_evidence(&rel, e1));

        assert!(store.contains_evidence(&rel, &e1));
        assert_eq!(store.evidence_for(&rel), vec![e1]);
        assert_eq!(store.relationships_for_evidence(&e1), vec![rel]);
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 6. Duplicate evidence association
    // -----------------------------------------------------------------------
    #[test]
    fn test_duplicate_evidence_association() {
        let mut store = ProvenanceStore::new();
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let e1 = EvidenceId::new();

        store.add_relationship(rel.clone());
        assert!(store.add_evidence(&rel, e1));
        assert!(!store.add_evidence(&rel, e1));

        assert_eq!(store.evidence_for(&rel).len(), 1);
        assert_eq!(store.evidence_for(&rel), vec![e1]);
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 7. Multiple evidence for one relationship
    // -----------------------------------------------------------------------
    #[test]
    fn test_multiple_evidence_for_one_relationship() {
        let mut store = ProvenanceStore::new();
        let rel = make_rel("api", "db", RelationshipKind::DEPENDS_ON);
        let e1 = EvidenceId::new();
        let e2 = EvidenceId::new();
        let e3 = EvidenceId::new();

        store.add_relationship(rel.clone());
        store.add_evidence(&rel, e1);
        store.add_evidence(&rel, e2);
        store.add_evidence(&rel, e3);

        let evs = store.evidence_for(&rel);
        assert_eq!(evs.len(), 3);
        assert!(evs.contains(&e1));
        assert!(evs.contains(&e2));
        assert!(evs.contains(&e3));
        assert_eq!(store.evidence_count_for(&rel), 3);
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 8. Multiple relationships supported by one evidence
    // -----------------------------------------------------------------------
    #[test]
    fn test_multiple_relationships_supported_by_one_evidence() {
        let mut store = ProvenanceStore::new();
        let r1 = make_rel("a", "b", RelationshipKind::CALLS);
        let r2 = make_rel("c", "d", RelationshipKind::DEPENDS_ON);
        let e1 = EvidenceId::new();

        store.add_relationship(r1.clone());
        store.add_relationship(r2.clone());

        assert!(store.add_evidence(&r1, e1));
        assert!(store.add_evidence(&r2, e1));

        let rels = store.relationships_for_evidence(&e1);
        assert_eq!(rels.len(), 2);
        assert!(rels.contains(&r1));
        assert!(rels.contains(&r2));
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 9. Evidence lookup
    // -----------------------------------------------------------------------
    #[test]
    fn test_evidence_lookup() {
        let mut store = ProvenanceStore::new();
        let rel = make_rel("web", "cache", RelationshipKind::READS_FROM);
        let e1 = EvidenceId::new();

        store.add_relationship(rel.clone());
        store.add_evidence(&rel, e1);

        assert_eq!(store.evidence_for(&rel), vec![e1]);
    }

    // -----------------------------------------------------------------------
    // 10. Reverse evidence lookup
    // -----------------------------------------------------------------------
    #[test]
    fn test_reverse_evidence_lookup() {
        let mut store = ProvenanceStore::new();
        let rel = make_rel("worker", "queue", RelationshipKind::WRITES_TO);
        let e1 = EvidenceId::new();

        store.add_relationship(rel.clone());
        store.add_evidence(&rel, e1);

        assert_eq!(store.relationships_for_evidence(&e1), vec![rel]);
    }

    // -----------------------------------------------------------------------
    // 11. Unknown relationship rejects evidence
    // -----------------------------------------------------------------------
    #[test]
    fn test_unknown_relationship_rejects_evidence() {
        let mut store = ProvenanceStore::new();
        let unreg = make_rel("ghost_src", "ghost_tgt", RelationshipKind::CALLS);
        let e1 = EvidenceId::new();

        // Relationship not registered
        assert!(!store.add_evidence(&unreg, e1));
        assert_eq!(store.len(), 0);
        assert!(!store.contains_relationship(&unreg));
        assert!(store.relationships_for_evidence(&e1).is_empty());
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 12. Unknown relationship does not get implicitly created
    // -----------------------------------------------------------------------
    #[test]
    fn test_unknown_relationship_does_not_get_implicitly_created() {
        let mut store = ProvenanceStore::new();
        let unreg = make_rel("ghost", "phantom", RelationshipKind::DEPENDS_ON);
        let e1 = EvidenceId::new();

        let _ = store.add_evidence(&unreg, e1);

        assert!(store.is_empty());
        assert!(!store.contains_relationship(&unreg));
        assert!(store.evidence_for(&unreg).is_empty());
    }

    // -----------------------------------------------------------------------
    // 13. Unknown evidence query returns empty
    // -----------------------------------------------------------------------
    #[test]
    fn test_unknown_evidence_query_returns_empty() {
        let store = ProvenanceStore::new();
        let unknown_e = EvidenceId::new();

        assert!(store.relationships_for_evidence(&unknown_e).is_empty());
    }

    // -----------------------------------------------------------------------
    // 14. Unknown relationship query returns empty
    // -----------------------------------------------------------------------
    #[test]
    fn test_unknown_relationship_query_returns_empty() {
        let store = ProvenanceStore::new();
        let unknown_r = make_rel("x", "y", RelationshipKind::CALLS);

        assert!(store.evidence_for(&unknown_r).is_empty());
        assert!(!store.contains_relationship(&unknown_r));
        assert_eq!(store.evidence_count_for(&unknown_r), 0);
        assert_eq!(store.relationship_evidence(&unknown_r), None);
    }

    // -----------------------------------------------------------------------
    // 15. Remove relationship
    // -----------------------------------------------------------------------
    #[test]
    fn test_remove_relationship() {
        let mut store = ProvenanceStore::new();
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let e1 = EvidenceId::new();

        store.add_relationship(rel.clone());
        store.add_evidence(&rel, e1);

        assert!(store.remove_relationship(&rel));
        assert_eq!(store.len(), 0);
        assert!(!store.contains_relationship(&rel));
        // Second removal returns false
        assert!(!store.remove_relationship(&rel));
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 16. Removal cleans forward index
    // -----------------------------------------------------------------------
    #[test]
    fn test_removal_cleans_forward_index() {
        let mut store = ProvenanceStore::new();
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let e1 = EvidenceId::new();

        store.add_relationship(rel.clone());
        store.add_evidence(&rel, e1);

        store.remove_relationship(&rel);
        assert!(store.evidence_for(&rel).is_empty());
        assert!(!store.contains_evidence(&rel, &e1));
    }

    // -----------------------------------------------------------------------
    // 17. Removal cleans reverse index
    // -----------------------------------------------------------------------
    #[test]
    fn test_removal_cleans_reverse_index() {
        let mut store = ProvenanceStore::new();
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let e1 = EvidenceId::new();

        store.add_relationship(rel.clone());
        store.add_evidence(&rel, e1);

        store.remove_relationship(&rel);
        assert!(store.relationships_for_evidence(&e1).is_empty());
        assert!(store.all_evidence_ids().is_empty());
    }

    // -----------------------------------------------------------------------
    // 18. Clear store
    // -----------------------------------------------------------------------
    #[test]
    fn test_clear_store() {
        let mut store = ProvenanceStore::new();
        let r1 = make_rel("a", "b", RelationshipKind::CALLS);
        let r2 = make_rel("c", "d", RelationshipKind::DEPENDS_ON);
        let e1 = EvidenceId::new();
        let e2 = EvidenceId::new();

        store.add_relationship(r1.clone());
        store.add_relationship(r2.clone());
        store.add_evidence(&r1, e1);
        store.add_evidence(&r2, e2);

        store.clear();

        assert!(store.is_empty());
        assert_eq!(store.len(), 0);
        assert!(store.evidence_for(&r1).is_empty());
        assert!(store.evidence_for(&r2).is_empty());
        assert!(store.relationships_for_evidence(&e1).is_empty());
        assert!(store.relationships_for_evidence(&e2).is_empty());
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 19. Deterministic evidence ordering
    // -----------------------------------------------------------------------
    #[test]
    fn test_deterministic_evidence_ordering() {
        let mut store = ProvenanceStore::new();
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        store.add_relationship(rel.clone());

        // Generate multiple evidence IDs
        let mut ev_ids: Vec<EvidenceId> = (0..10).map(|_| EvidenceId::new()).collect();
        for &id in &ev_ids {
            store.add_evidence(&rel, id);
        }

        // Expected sorted order by UUID
        ev_ids.sort_by(cmp_evidence_id);

        let query1 = store.evidence_for(&rel);
        let query2 = store.evidence_for(&rel);

        assert_eq!(query1, ev_ids);
        assert_eq!(query1, query2);
    }

    // -----------------------------------------------------------------------
    // 20. Deterministic relationship ordering
    // -----------------------------------------------------------------------
    #[test]
    fn test_deterministic_relationship_ordering() {
        let mut store = ProvenanceStore::new();
        let e1 = EvidenceId::new();

        // Insert in reverse canonical order: z, m, b
        let r_z = make_rel("z", "target", RelationshipKind::CALLS);
        let r_m = make_rel("m", "target", RelationshipKind::CALLS);
        let r_b = make_rel("b", "target", RelationshipKind::CALLS);

        store.add_relationship(r_z.clone());
        store.add_relationship(r_m.clone());
        store.add_relationship(r_b.clone());

        store.add_evidence(&r_z, e1);
        store.add_evidence(&r_m, e1);
        store.add_evidence(&r_b, e1);

        let rels = store.relationships_for_evidence(&e1);
        assert_eq!(rels.len(), 3);
        assert_eq!(rels[0], r_b);
        assert_eq!(rels[1], r_m);
        assert_eq!(rels[2], r_z);
    }

    // -----------------------------------------------------------------------
    // 21. Different relationship kinds remain distinct
    // -----------------------------------------------------------------------
    #[test]
    fn test_different_relationship_kinds_remain_distinct() {
        let mut store = ProvenanceStore::new();
        let r_calls = make_rel("a", "b", RelationshipKind::CALLS);
        let r_deps = make_rel("a", "b", RelationshipKind::DEPENDS_ON);

        assert!(store.add_relationship(r_calls.clone()));
        assert!(store.add_relationship(r_deps.clone()));
        assert_eq!(store.len(), 2);

        let e1 = EvidenceId::new();
        let e2 = EvidenceId::new();

        store.add_evidence(&r_calls, e1);
        store.add_evidence(&r_deps, e2);

        assert_eq!(store.evidence_for(&r_calls), vec![e1]);
        assert_eq!(store.evidence_for(&r_deps), vec![e2]);
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 22. Same endpoints + different kinds
    // -----------------------------------------------------------------------
    #[test]
    fn test_same_endpoints_different_kinds_separate_provenance() {
        let mut store = ProvenanceStore::new();
        let r1 = make_rel("svc", "db", RelationshipKind::READS_FROM);
        let r2 = make_rel("svc", "db", RelationshipKind::WRITES_TO);

        store.add_relationship(r1.clone());
        store.add_relationship(r2.clone());

        let e_read = EvidenceId::new();
        let e_write = EvidenceId::new();

        store.add_evidence(&r1, e_read);
        store.add_evidence(&r2, e_write);

        assert_eq!(store.evidence_for(&r1), vec![e_read]);
        assert_eq!(store.evidence_for(&r2), vec![e_write]);
        assert!(!store.contains_evidence(&r1, &e_write));
        assert!(!store.contains_evidence(&r2, &e_read));
    }

    // -----------------------------------------------------------------------
    // 23. Same EvidenceId supporting multiple relationships
    // -----------------------------------------------------------------------
    #[test]
    fn test_same_evidence_id_supporting_multiple_relationships() {
        let mut store = ProvenanceStore::new();
        let r1 = make_rel("a", "b", RelationshipKind::CALLS);
        let r2 = make_rel("b", "c", RelationshipKind::CALLS);
        let r3 = make_rel("c", "d", RelationshipKind::CALLS);
        let shared_evidence = EvidenceId::new();

        store.add_relationship(r1.clone());
        store.add_relationship(r2.clone());
        store.add_relationship(r3.clone());

        store.add_evidence(&r1, shared_evidence);
        store.add_evidence(&r2, shared_evidence);
        store.add_evidence(&r3, shared_evidence);

        let rels = store.relationships_for_evidence(&shared_evidence);
        assert_eq!(rels.len(), 3);
        assert!(rels.contains(&r1));
        assert!(rels.contains(&r2));
        assert!(rels.contains(&r3));
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 24. Repeated add/remove operations
    // -----------------------------------------------------------------------
    #[test]
    fn test_repeated_add_remove_operations() {
        let mut store = ProvenanceStore::new();
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let e1 = EvidenceId::new();

        for _ in 0..5 {
            assert!(store.add_relationship(rel.clone()));
            assert!(store.add_evidence(&rel, e1));
            assert!(store.check_invariants().is_ok());

            assert!(store.remove_relationship(&rel));
            assert!(store.check_invariants().is_ok());
            assert!(store.is_empty());
        }
    }

    // -----------------------------------------------------------------------
    // 25. Large association set
    // -----------------------------------------------------------------------
    #[test]
    fn test_large_association_set() {
        let mut store = ProvenanceStore::new();
        let count = 50;
        let mut rels = Vec::new();
        let mut evs = Vec::new();

        for i in 0..count {
            let rel = make_rel(
                &format!("src_{}", i),
                &format!("tgt_{}", i),
                RelationshipKind::CALLS,
            );
            store.add_relationship(rel.clone());
            rels.push(rel);

            let ev = EvidenceId::new();
            evs.push(ev);
        }

        // Each relationship gets 2 evidence IDs
        for i in 0..count {
            store.add_evidence(&rels[i], evs[i]);
            store.add_evidence(&rels[i], evs[(i + 1) % count]);
        }

        assert_eq!(store.len(), count);
        assert_eq!(store.all_evidence_ids().len(), count);
        assert!(store.check_invariants().is_ok());

        for rel in &rels {
            assert_eq!(store.evidence_count_for(rel), 2);
        }
    }

    // -----------------------------------------------------------------------
    // 26. Forward/reverse index consistency
    // -----------------------------------------------------------------------
    #[test]
    fn test_forward_reverse_consistency() {
        let mut store = ProvenanceStore::new();
        let r1 = make_rel("a", "b", RelationshipKind::CALLS);
        let r2 = make_rel("b", "c", RelationshipKind::CALLS);
        let e1 = EvidenceId::new();
        let e2 = EvidenceId::new();

        store.add_relationship(r1.clone());
        store.add_relationship(r2.clone());

        store.add_evidence(&r1, e1);
        store.add_evidence(&r1, e2);
        store.add_evidence(&r2, e2);

        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 27. Relationship object remains unchanged
    // -----------------------------------------------------------------------
    #[test]
    fn test_relationship_object_remains_unchanged() {
        let mut store = ProvenanceStore::new();
        let src = make_identity("source");
        let tgt = make_identity("target");
        let rel = Relationship::new(src.clone(), tgt.clone(), RelationshipKind::DEPENDS_ON);
        let e1 = EvidenceId::new();

        store.add_relationship(rel.clone());
        store.add_evidence(&rel, e1);

        let retrieved = &store.relationships_for_evidence(&e1)[0];
        assert_eq!(retrieved.source, src);
        assert_eq!(retrieved.target, tgt);
        assert_eq!(retrieved.category, RelationshipCategory::Dependency);
        assert_eq!(retrieved.kind, RelationshipKind::DEPENDS_ON);
    }

    // -----------------------------------------------------------------------
    // 28. Graph remains completely independent
    // -----------------------------------------------------------------------
    #[test]
    fn test_graph_remains_completely_independent() {
        let mut graph = Graph::new();
        let rel = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        graph.add_relationship(rel.clone());

        let initial_resources = graph.resources();
        let initial_relationships = graph.relationships();
        let initial_res_count = graph.resource_count();
        let initial_rel_count = graph.relationship_count();

        // Independent provenance operations
        let mut store = ProvenanceStore::new();
        store.add_relationship(rel.clone());
        let e1 = EvidenceId::new();
        store.add_evidence(&rel, e1);
        store.remove_relationship(&rel);
        store.clear();

        // Graph must remain identical
        assert_eq!(graph.resources(), initial_resources);
        assert_eq!(graph.relationships(), initial_relationships);
        assert_eq!(graph.resource_count(), initial_res_count);
        assert_eq!(graph.relationship_count(), initial_rel_count);
        assert!(graph.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 29. Evidence contents are not stored
    // -----------------------------------------------------------------------
    #[test]
    fn test_evidence_contents_are_not_stored() {
        // Proves ProvenanceStore only deals with EvidenceId, not full Evidence
        let mut store = ProvenanceStore::new();
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let eid = EvidenceId::new();

        store.add_relationship(rel.clone());
        store.add_evidence(&rel, eid);

        assert_eq!(store.evidence_for(&rel), vec![eid]);
    }

    // -----------------------------------------------------------------------
    // 30. Send + Sync
    // -----------------------------------------------------------------------
    #[test]
    fn test_send_sync_thread_safety() {
        fn assert_send_sync<T: Send + Sync>() {}
        assert_send_sync::<RelationshipEvidence>();
        assert_send_sync::<ProvenanceStore>();
    }

    // -----------------------------------------------------------------------
    // 31. Read-oriented helper methods
    // -----------------------------------------------------------------------
    #[test]
    fn test_relationship_evidence_helpers() {
        let mut store = ProvenanceStore::new();
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        let e1 = EvidenceId::new();

        store.add_relationship(rel.clone());
        store.add_evidence(&rel, e1);

        let view = store.relationship_evidence(&rel).unwrap();
        assert_eq!(view.relationship, rel);
        assert_eq!(view.supporting_evidence, vec![e1]);
        assert_eq!(view.evidence_count(), 1);
        assert!(view.has_evidence());

        let all = store.all_relationship_evidence();
        assert_eq!(all.len(), 1);
        assert_eq!(all[0], view);
    }

    // -----------------------------------------------------------------------
    // CRITICAL TEST 1: Association and deterministic ordering
    //
    // Create: API ──DEPENDS_ON──> Database
    // Create EvidenceIds: E1, E2
    // Register R.
    // Associate: R ← E1, R ← E2
    // Expected: evidence_for(R) returns E1, E2 with deterministic ordering.
    // -----------------------------------------------------------------------
    #[test]
    fn test_critical_1_association_and_deterministic_ordering() {
        let mut store = ProvenanceStore::new();
        let api = make_custom_identity("kubernetes", "service", "api");
        let db = make_custom_identity("aws", "rds", "database");
        let r = Relationship::new(api, db, RelationshipKind::DEPENDS_ON);

        let e1 = EvidenceId::new();
        let e2 = EvidenceId::new();

        assert!(store.add_relationship(r.clone()));
        assert!(store.add_evidence(&r, e1));
        assert!(store.add_evidence(&r, e2));

        let mut expected = vec![e1, e2];
        expected.sort_by(cmp_evidence_id);

        let actual = store.evidence_for(&r);
        assert_eq!(actual, expected);
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // CRITICAL TEST 2: Reverse Lookup
    //
    // Create:
    // R1 = A ──CALLS──────> B
    // R2 = C ──DEPENDS_ON─> D
    // Create: E1
    // Associate: R1 ← E1, R2 ← E1
    // Then: relationships_for_evidence(E1) must return R1, R2 deterministically.
    // -----------------------------------------------------------------------
    #[test]
    fn test_critical_2_reverse_lookup() {
        let mut store = ProvenanceStore::new();
        let r1 = make_rel("a", "b", RelationshipKind::CALLS);
        let r2 = make_rel("c", "d", RelationshipKind::DEPENDS_ON);
        let e1 = EvidenceId::new();

        store.add_relationship(r1.clone());
        store.add_relationship(r2.clone());

        store.add_evidence(&r1, e1);
        store.add_evidence(&r2, e1);

        let mut expected = vec![r1.clone(), r2.clone()];
        expected.sort_by(cmp_relationship);

        let actual = store.relationships_for_evidence(&e1);
        assert_eq!(actual, expected);
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // CRITICAL TEST 3: Unknown Relationship
    //
    // Do NOT register: R = A ──DEPENDS_ON──> B
    // Call: add_evidence(&R, E1)
    // Expected: false, store remains empty, no implicit relationship creation.
    // -----------------------------------------------------------------------
    #[test]
    fn test_critical_3_unknown_relationship() {
        let mut store = ProvenanceStore::new();
        let r = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        let e1 = EvidenceId::new();

        let result = store.add_evidence(&r, e1);
        assert!(!result);

        assert!(store.is_empty());
        assert_eq!(store.len(), 0);
        assert!(!store.contains_relationship(&r));
        assert!(store.evidence_for(&r).is_empty());
        assert!(store.relationships_for_evidence(&e1).is_empty());
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // CRITICAL TEST 4: Removal
    //
    // Create: R = A ──DEPENDS_ON──> B
    // E1 supports R.
    // Remove R.
    // Expected:
    // evidence_for(R) = []
    // relationships_for_evidence(E1) = []
    // E1 itself is NOT deleted.
    // -----------------------------------------------------------------------
    #[test]
    fn test_critical_4_removal() {
        let mut store = ProvenanceStore::new();
        let r = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        let e1 = EvidenceId::new();

        store.add_relationship(r.clone());
        store.add_evidence(&r, e1);

        assert!(store.remove_relationship(&r));

        assert!(store.evidence_for(&r).is_empty());
        assert!(store.relationships_for_evidence(&e1).is_empty());
        assert!(!store.contains_relationship(&r));
        assert_eq!(store.len(), 0);
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // CRITICAL TEST 5: Shared Evidence
    //
    // Create:
    // R1 = A ──CALLS──────> B
    // R2 = C ──DEPENDS_ON─> D
    // E1 supports both.
    // Remove R1.
    // Expected: E1 still supports R2.
    // Removing one relationship must never accidentally remove the shared
    // EvidenceId association from another relationship.
    // -----------------------------------------------------------------------
    #[test]
    fn test_critical_5_shared_evidence() {
        let mut store = ProvenanceStore::new();
        let r1 = make_rel("a", "b", RelationshipKind::CALLS);
        let r2 = make_rel("c", "d", RelationshipKind::DEPENDS_ON);
        let e1 = EvidenceId::new();

        store.add_relationship(r1.clone());
        store.add_relationship(r2.clone());

        store.add_evidence(&r1, e1);
        store.add_evidence(&r2, e1);

        // Remove R1
        assert!(store.remove_relationship(&r1));

        // R1 is gone
        assert!(store.evidence_for(&r1).is_empty());

        // E1 must still support R2
        assert_eq!(store.relationships_for_evidence(&e1), vec![r2.clone()]);
        assert_eq!(store.evidence_for(&r2), vec![e1]);
        assert_eq!(store.len(), 1);
        assert!(store.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // CRITICAL TEST 6: Graph Independence
    //
    // Create a Graph containing: A ──DEPENDS_ON──> B
    // Create a ProvenanceStore containing the same relationship.
    // Mutate ProvenanceStore.
    // Verify Graph resource count, relationship count, and invariants are unchanged.
    // Provenance must not own or mutate Graph state.
    // -----------------------------------------------------------------------
    #[test]
    fn test_critical_6_graph_independence() {
        let mut graph = Graph::new();
        let r = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        graph.add_relationship(r.clone());

        let orig_res_count = graph.resource_count();
        let orig_rel_count = graph.relationship_count();
        let orig_resources = graph.resources();
        let orig_relationships = graph.relationships();

        let mut store = ProvenanceStore::new();
        store.add_relationship(r.clone());
        let e1 = EvidenceId::new();
        let e2 = EvidenceId::new();
        store.add_evidence(&r, e1);
        store.add_evidence(&r, e2);
        store.remove_relationship(&r);
        store.clear();

        // Assert Graph is completely unaffected
        assert_eq!(graph.resource_count(), orig_res_count);
        assert_eq!(graph.relationship_count(), orig_rel_count);
        assert_eq!(graph.resources(), orig_resources);
        assert_eq!(graph.relationships(), orig_relationships);
        assert!(graph.check_invariants().is_ok());
    }
}
