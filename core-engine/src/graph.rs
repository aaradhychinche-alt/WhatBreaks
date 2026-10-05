//! WhatBreaks Graph Model v1
//!
//! A lightweight, deterministic, in-memory graph abstraction storing
//! [`ResourceIdentity`] nodes and [`Relationship`] edges.
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
//!       Graph (Graph v1 — Directional Storage & Indexing)
//!         │
//!         ▼
//!     Traversal (Future Engine)
//!         │
//!         ▼
//!  Impact / Blast Radius (Future Engine)
//! ```
//!
//! # Design Principles
//!
//! - **ResourceIdentity as Node Identity**: The graph indexes nodes by
//!   [`ResourceIdentity`], not internal [`ResourceId`](crate::ResourceId) UUIDs
//!   or full [`Resource`](crate::Resource) objects.
//! - **Natural Edge Identity**: Relationships are identified by
//!   `(source, target, kind)`. Duplicate edges between the same endpoints with the
//!   same kind are prevented. Multiple relationships between the same endpoints
//!   with *different* kinds are fully supported.
//! - **Strict Directionality**: Edges are strictly directional from `source` to `target`.
//!   The graph maintains dual indexes (outgoing and incoming) so both forward and
//!   reverse dependencies can be queried in $O(1)$ without reversing or losing direction.
//! - **Automatic Node Registration**: Adding a relationship automatically registers its
//!   endpoint resource identities in the graph.
//! - **Deterministic Query Results**: All public query methods return results with
//!   deterministic ordering independent of internal hash iteration order.
//! - **Self-Loops Supported**: A resource may have a relationship targeting itself
//!   (e.g. `A --DEPENDS_ON--> A`).
//! - **Provider-Agnostic & Pure In-Memory**: Contains no provider logic, no evidence
//!   models, no database/persistence code, and no traversal/impact logic.

use std::collections::{HashMap, HashSet};

use crate::relationship::Relationship;
use crate::resource::ResourceIdentity;

// ---------------------------------------------------------------------------
// Deterministic Sorting Helpers
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

// ---------------------------------------------------------------------------
// Graph
// ---------------------------------------------------------------------------

/// In-memory graph storing [`ResourceIdentity`] nodes and [`Relationship`] edges.
///
/// Provides fast directional lookups for outgoing and incoming relationships,
/// with guaranteed deterministic query ordering.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Graph {
    /// Set of all known resource identities in the graph.
    resources: HashSet<ResourceIdentity>,
    /// Outgoing index: source -> set of relationships originating from source.
    outgoing: HashMap<ResourceIdentity, HashSet<Relationship>>,
    /// Incoming index: target -> set of relationships pointing to target.
    incoming: HashMap<ResourceIdentity, HashSet<Relationship>>,
    /// Total count of distinct relationships stored in the graph.
    relationship_count: usize,
}

impl Default for Graph {
    fn default() -> Self {
        Self::new()
    }
}

impl Graph {
    /// Construct a new, empty `Graph`.
    pub fn new() -> Self {
        Self {
            resources: HashSet::new(),
            outgoing: HashMap::new(),
            incoming: HashMap::new(),
            relationship_count: 0,
        }
    }

    /// Add a resource identity to the graph.
    ///
    /// Returns `true` if the resource was newly added, or `false` if it was already
    /// present (idempotent no-op).
    pub fn add_resource(&mut self, resource: ResourceIdentity) -> bool {
        self.resources.insert(resource)
    }

    /// Add a directional relationship to the graph.
    ///
    /// Automatically registers `relationship.source` and `relationship.target`
    /// as known resources in the graph.
    ///
    /// Duplicate edges with identical `(source, target, kind)` are ignored.
    /// Returns `true` if the relationship was newly added, or `false` if it already
    /// existed in the graph.
    pub fn add_relationship(&mut self, relationship: Relationship) -> bool {
        // Check if edge already exists in outgoing index (natural identity is source, target, kind)
        if let Some(set) = self.outgoing.get(&relationship.source) {
            if set.contains(&relationship) {
                return false;
            }
        }

        // Ensure endpoint resources are registered
        self.resources.insert(relationship.source.clone());
        self.resources.insert(relationship.target.clone());

        // Insert into incoming index
        self.incoming
            .entry(relationship.target.clone())
            .or_default()
            .insert(relationship.clone());

        // Insert into outgoing index
        self.outgoing
            .entry(relationship.source.clone())
            .or_default()
            .insert(relationship);

        self.relationship_count += 1;
        true
    }

    /// Remove a resource from the graph.
    ///
    /// If the resource exists:
    /// - It is removed from the resource set.
    /// - All outgoing relationships originating from it are removed.
    /// - All incoming relationships targeting it are removed.
    /// - Indexes and relationship counts are updated consistently.
    ///
    /// Returns `true` if the resource was removed, or `false` if it was not found.
    pub fn remove_resource(&mut self, resource: &ResourceIdentity) -> bool {
        if !self.resources.remove(resource) {
            return false;
        }

        // Remove and extract all outgoing relationships from resource
        let outgoing_rels = self.outgoing.remove(resource).unwrap_or_default();

        // Remove and extract all incoming relationships targeting resource
        let incoming_rels = self.incoming.remove(resource).unwrap_or_default();

        // Clean up outgoing relationships from target incoming sets
        for rel in &outgoing_rels {
            if rel.target != *resource {
                if let Some(set) = self.incoming.get_mut(&rel.target) {
                    set.remove(rel);
                    if set.is_empty() {
                        self.incoming.remove(&rel.target);
                    }
                }
            }
        }

        // Clean up incoming relationships from source outgoing sets
        for rel in &incoming_rels {
            if rel.source != *resource {
                if let Some(set) = self.outgoing.get_mut(&rel.source) {
                    set.remove(rel);
                    if set.is_empty() {
                        self.outgoing.remove(&rel.source);
                    }
                }
            }
        }

        // Calculate unique removed relationships (self-loops appear in both sets)
        let mut unique_removed = outgoing_rels;
        unique_removed.extend(incoming_rels);
        self.relationship_count -= unique_removed.len();

        true
    }

    /// Remove a relationship from the graph.
    ///
    /// Removes the relationship from both outgoing and incoming indexes.
    /// Endpoint resources remain in the graph.
    ///
    /// Returns `true` if the relationship was found and removed, or `false` if it
    /// was not present.
    pub fn remove_relationship(&mut self, relationship: &Relationship) -> bool {
        let removed_from_outgoing = if let Some(set) = self.outgoing.get_mut(&relationship.source) {
            let removed = set.remove(relationship);
            if set.is_empty() {
                self.outgoing.remove(&relationship.source);
            }
            removed
        } else {
            false
        };

        if !removed_from_outgoing {
            return false;
        }

        if let Some(set) = self.incoming.get_mut(&relationship.target) {
            set.remove(relationship);
            if set.is_empty() {
                self.incoming.remove(&relationship.target);
            }
        }

        self.relationship_count -= 1;
        true
    }

    /// Check if a resource identity is known to the graph.
    pub fn contains_resource(&self, resource: &ResourceIdentity) -> bool {
        self.resources.contains(resource)
    }

    /// Check if a relationship exists in the graph.
    pub fn contains_relationship(&self, relationship: &Relationship) -> bool {
        self.outgoing
            .get(&relationship.source)
            .is_some_and(|set| set.contains(relationship))
    }

    /// Return all outgoing relationships originating from `source`, in deterministic order.
    pub fn get_relationships_from(&self, source: &ResourceIdentity) -> Vec<Relationship> {
        match self.outgoing.get(source) {
            Some(rels) => {
                let mut result: Vec<Relationship> = rels.iter().cloned().collect();
                result.sort_by(cmp_relationship);
                result
            }
            None => Vec::new(),
        }
    }

    /// Return all incoming relationships targeting `target`, in deterministic order.
    pub fn get_relationships_to(&self, target: &ResourceIdentity) -> Vec<Relationship> {
        match self.incoming.get(target) {
            Some(rels) => {
                let mut result: Vec<Relationship> = rels.iter().cloned().collect();
                result.sort_by(cmp_relationship);
                result
            }
            None => Vec::new(),
        }
    }

    /// Return all unique neighbor resource identities that `source` points to
    /// (destinations of outgoing relationships), in deterministic sorted order.
    pub fn neighbors_from(&self, source: &ResourceIdentity) -> Vec<ResourceIdentity> {
        match self.outgoing.get(source) {
            Some(rels) => {
                let mut neighbors: Vec<ResourceIdentity> =
                    rels.iter().map(|r| r.target.clone()).collect();
                neighbors.sort_by(cmp_resource_identity);
                neighbors.dedup();
                neighbors
            }
            None => Vec::new(),
        }
    }

    /// Return all unique neighbor resource identities that point to `target`
    /// (origins of incoming relationships), in deterministic sorted order.
    pub fn neighbors_to(&self, target: &ResourceIdentity) -> Vec<ResourceIdentity> {
        match self.incoming.get(target) {
            Some(rels) => {
                let mut neighbors: Vec<ResourceIdentity> =
                    rels.iter().map(|r| r.source.clone()).collect();
                neighbors.sort_by(cmp_resource_identity);
                neighbors.dedup();
                neighbors
            }
            None => Vec::new(),
        }
    }

    /// Return the total count of known resources in the graph.
    pub fn resource_count(&self) -> usize {
        self.resources.len()
    }

    /// Return the total count of relationships in the graph.
    pub fn relationship_count(&self) -> usize {
        self.relationship_count
    }

    /// Return `true` if the graph contains no resources.
    pub fn is_empty(&self) -> bool {
        self.resources.is_empty()
    }

    /// Return all known resource identities in the graph, in deterministic sorted order.
    pub fn resources(&self) -> Vec<ResourceIdentity> {
        let mut list: Vec<ResourceIdentity> = self.resources.iter().cloned().collect();
        list.sort_by(cmp_resource_identity);
        list
    }

    /// Return all relationships in the graph, in deterministic sorted order.
    pub fn relationships(&self) -> Vec<Relationship> {
        let mut list: Vec<Relationship> = self
            .outgoing
            .values()
            .flat_map(|set| set.iter().cloned())
            .collect();
        list.sort_by(cmp_relationship);
        list
    }

    /// Clear all resources and relationships from the graph.
    pub fn clear(&mut self) {
        self.resources.clear();
        self.outgoing.clear();
        self.incoming.clear();
        self.relationship_count = 0;
    }

    /// Verify internal consistency invariants of the graph data structure.
    ///
    /// Returns `Ok(())` if all invariants hold, or `Err(String)` describing the violation:
    /// - Every endpoint of every relationship must be in `resources`.
    /// - Every relationship in `outgoing` must appear symmetrically in `incoming`.
    /// - Every relationship in `incoming` must appear symmetrically in `outgoing`.
    /// - Total unique edges in `outgoing` must equal `incoming` and `relationship_count`.
    /// - No empty set entries in indexes.
    pub fn check_invariants(&self) -> Result<(), String> {
        let mut outgoing_total = 0;
        for (source, rels) in &self.outgoing {
            if rels.is_empty() {
                return Err(format!(
                    "outgoing index contains empty set for {:?}",
                    source
                ));
            }
            if !self.resources.contains(source) {
                return Err(format!(
                    "outgoing source {:?} is not registered in resources",
                    source
                ));
            }
            for rel in rels {
                if rel.source != *source {
                    return Err(format!(
                        "relationship {:?} in outgoing map for {:?} has mismatched source",
                        rel, source
                    ));
                }
                if !self.resources.contains(&rel.target) {
                    return Err(format!(
                        "relationship target {:?} is not registered in resources",
                        rel.target
                    ));
                }
                let in_incoming = self
                    .incoming
                    .get(&rel.target)
                    .is_some_and(|set| set.contains(rel));
                if !in_incoming {
                    return Err(format!(
                        "relationship {:?} in outgoing is missing from incoming index",
                        rel
                    ));
                }
                outgoing_total += 1;
            }
        }

        let mut incoming_total = 0;
        for (target, rels) in &self.incoming {
            if rels.is_empty() {
                return Err(format!(
                    "incoming index contains empty set for {:?}",
                    target
                ));
            }
            if !self.resources.contains(target) {
                return Err(format!(
                    "incoming target {:?} is not registered in resources",
                    target
                ));
            }
            for rel in rels {
                if rel.target != *target {
                    return Err(format!(
                        "relationship {:?} in incoming map for {:?} has mismatched target",
                        rel, target
                    ));
                }
                if !self.resources.contains(&rel.source) {
                    return Err(format!(
                        "relationship source {:?} is not registered in resources",
                        rel.source
                    ));
                }
                let in_outgoing = self
                    .outgoing
                    .get(&rel.source)
                    .is_some_and(|set| set.contains(rel));
                if !in_outgoing {
                    return Err(format!(
                        "relationship {:?} in incoming is missing from outgoing index",
                        rel
                    ));
                }
                incoming_total += 1;
            }
        }

        if outgoing_total != incoming_total {
            return Err(format!(
                "index count mismatch: outgoing={} incoming={}",
                outgoing_total, incoming_total
            ));
        }

        if outgoing_total != self.relationship_count {
            return Err(format!(
                "relationship_count mismatch: tracked={} actual={}",
                self.relationship_count, outgoing_total
            ));
        }

        if self.resources.len() != self.resource_count() {
            return Err(format!(
                "resource_count mismatch: tracked={} actual={}",
                self.resource_count(),
                self.resources.len()
            ));
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
    use crate::relationship::{RelationshipCategory, RelationshipKind};
    use crate::resource::{Provider, ResourceKind};

    fn make_identity(name: &str) -> ResourceIdentity {
        ResourceIdentity::new(Provider::new("kubernetes"), ResourceKind::new("pod"), name)
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
        assert_eq!(g.resource_count(), 0);
        assert_eq!(g.relationship_count(), 0);
        assert!(g.is_empty());
        assert!(!g.contains_resource(&make_identity("a")));
        assert!(!g.contains_relationship(&make_rel("a", "b", RelationshipKind::DEPENDS_ON)));
        assert!(g.resources().is_empty());
        assert!(g.relationships().is_empty());
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 2. Add resource
    // -----------------------------------------------------------------------
    #[test]
    fn test_add_resource() {
        let mut g = Graph::new();
        let a = make_identity("a");
        assert!(g.add_resource(a.clone()));
        assert_eq!(g.resource_count(), 1);
        assert!(g.contains_resource(&a));
        assert!(!g.is_empty());
        assert_eq!(g.resources(), vec![a]);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 3. Duplicate resource
    // -----------------------------------------------------------------------
    #[test]
    fn test_duplicate_resource() {
        let mut g = Graph::new();
        let a = make_identity("a");
        assert!(g.add_resource(a.clone()));
        assert!(!g.add_resource(a.clone())); // second add is no-op
        assert_eq!(g.resource_count(), 1);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 4. Add relationship
    // -----------------------------------------------------------------------
    #[test]
    fn test_add_relationship() {
        let mut g = Graph::new();
        let rel = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        assert!(g.add_relationship(rel.clone()));
        assert_eq!(g.relationship_count(), 1);
        assert!(g.contains_relationship(&rel));
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 5. Relationship automatically registers endpoint resources
    // -----------------------------------------------------------------------
    #[test]
    fn test_relationship_automatically_registers_endpoints() {
        let mut g = Graph::new();
        let a = make_identity("a");
        let b = make_identity("b");
        assert!(!g.contains_resource(&a));
        assert!(!g.contains_resource(&b));

        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        assert!(g.add_relationship(rel));

        assert!(g.contains_resource(&a));
        assert!(g.contains_resource(&b));
        assert_eq!(g.resource_count(), 2);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 6. Outgoing relationship query
    // -----------------------------------------------------------------------
    #[test]
    fn test_outgoing_relationship_query() {
        let mut g = Graph::new();
        let rel = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        g.add_relationship(rel.clone());

        let out = g.get_relationships_from(&make_identity("a"));
        assert_eq!(out, vec![rel]);
    }

    // -----------------------------------------------------------------------
    // 7. Incoming relationship query
    // -----------------------------------------------------------------------
    #[test]
    fn test_incoming_relationship_query() {
        let mut g = Graph::new();
        let rel = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        g.add_relationship(rel.clone());

        let incoming = g.get_relationships_to(&make_identity("b"));
        assert_eq!(incoming, vec![rel]);
    }

    // -----------------------------------------------------------------------
    // 8. Direction preservation
    // -----------------------------------------------------------------------
    #[test]
    fn test_direction_preservation() {
        let mut g = Graph::new();
        let a = make_identity("a");
        let b = make_identity("b");
        let rel = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        g.add_relationship(rel.clone());

        // A -> B: A has outgoing, B has incoming
        assert_eq!(g.get_relationships_from(&a), vec![rel.clone()]);
        assert_eq!(g.get_relationships_to(&b), vec![rel]);

        // Direction must NOT be reversed
        assert!(g.get_relationships_from(&b).is_empty());
        assert!(g.get_relationships_to(&a).is_empty());

        assert_eq!(g.neighbors_from(&a), vec![b.clone()]);
        assert_eq!(g.neighbors_to(&b), vec![a.clone()]);
        assert!(g.neighbors_from(&b).is_empty());
        assert!(g.neighbors_to(&a).is_empty());
    }

    // -----------------------------------------------------------------------
    // 9. Duplicate relationship prevention
    // -----------------------------------------------------------------------
    #[test]
    fn test_duplicate_relationship_prevention() {
        let mut g = Graph::new();
        let rel = make_rel("a", "b", RelationshipKind::CALLS);
        assert!(g.add_relationship(rel.clone()));
        assert!(!g.add_relationship(rel.clone())); // duplicate add
        assert_eq!(g.relationship_count(), 1);
        assert_eq!(g.resource_count(), 2);
        assert_eq!(g.get_relationships_from(&make_identity("a")).len(), 1);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 10. Multiple outgoing relationships
    // -----------------------------------------------------------------------
    #[test]
    fn test_multiple_outgoing_relationships() {
        let mut g = Graph::new();
        let rel1 = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        let rel2 = make_rel("a", "c", RelationshipKind::CALLS);
        g.add_relationship(rel1);
        g.add_relationship(rel2);

        assert_eq!(g.relationship_count(), 2);
        assert_eq!(g.resource_count(), 3);
        let out = g.get_relationships_from(&make_identity("a"));
        assert_eq!(out.len(), 2);

        let neighbors = g.neighbors_from(&make_identity("a"));
        assert_eq!(neighbors, vec![make_identity("b"), make_identity("c")]);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 11. Multiple incoming relationships
    // -----------------------------------------------------------------------
    #[test]
    fn test_multiple_incoming_relationships() {
        let mut g = Graph::new();
        let rel1 = make_rel("a", "c", RelationshipKind::DEPENDS_ON);
        let rel2 = make_rel("b", "c", RelationshipKind::CALLS);
        g.add_relationship(rel1);
        g.add_relationship(rel2);

        assert_eq!(g.relationship_count(), 2);
        assert_eq!(g.resource_count(), 3);
        let inc = g.get_relationships_to(&make_identity("c"));
        assert_eq!(inc.len(), 2);

        let neighbors = g.neighbors_to(&make_identity("c"));
        assert_eq!(neighbors, vec![make_identity("a"), make_identity("b")]);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 12. Multiple relationships between same resources with different kinds
    // -----------------------------------------------------------------------
    #[test]
    fn test_multiple_relationships_different_kinds() {
        let mut g = Graph::new();
        let r1 = make_rel("a", "b", RelationshipKind::CALLS);
        let r2 = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        assert!(g.add_relationship(r1.clone()));
        assert!(g.add_relationship(r2.clone()));

        assert_eq!(g.relationship_count(), 2);
        assert_eq!(g.resource_count(), 2);

        let out = g.get_relationships_from(&make_identity("a"));
        assert_eq!(out.len(), 2);
        assert!(out.contains(&r1));
        assert!(out.contains(&r2));

        // Neighbors deduplicates identical targets
        let neighbors = g.neighbors_from(&make_identity("a"));
        assert_eq!(neighbors, vec![make_identity("b")]);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 13. Remove relationship
    // -----------------------------------------------------------------------
    #[test]
    fn test_remove_relationship() {
        let mut g = Graph::new();
        let rel = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        g.add_relationship(rel.clone());

        assert!(g.remove_relationship(&rel));
        assert_eq!(g.relationship_count(), 0);
        assert!(!g.contains_relationship(&rel));

        // Removing non-existent relationship is safe and returns false
        assert!(!g.remove_relationship(&rel));

        // Endpoint resources remain in the graph
        assert!(g.contains_resource(&make_identity("a")));
        assert!(g.contains_resource(&make_identity("b")));
        assert_eq!(g.resource_count(), 2);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 14. Remove resource
    // -----------------------------------------------------------------------
    #[test]
    fn test_remove_resource() {
        let mut g = Graph::new();
        let a = make_identity("a");
        g.add_resource(a.clone());

        assert!(g.remove_resource(&a));
        assert_eq!(g.resource_count(), 0);
        assert!(!g.contains_resource(&a));

        // Removing non-existent resource is safe and returns false
        assert!(!g.remove_resource(&a));
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 15. Removing resource removes incoming relationships
    // -----------------------------------------------------------------------
    #[test]
    fn test_removing_resource_removes_incoming_relationships() {
        let mut g = Graph::new();
        let rel = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        g.add_relationship(rel);

        // Remove target B
        assert!(g.remove_resource(&make_identity("b")));
        assert_eq!(g.relationship_count(), 0);
        assert!(g.get_relationships_from(&make_identity("a")).is_empty());
        assert!(g.neighbors_from(&make_identity("a")).is_empty());
        assert!(g.contains_resource(&make_identity("a")));
        assert!(!g.contains_resource(&make_identity("b")));
        assert_eq!(g.resource_count(), 1);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 16. Removing resource removes outgoing relationships
    // -----------------------------------------------------------------------
    #[test]
    fn test_removing_resource_removes_outgoing_relationships() {
        let mut g = Graph::new();
        let rel = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        g.add_relationship(rel);

        // Remove source A
        assert!(g.remove_resource(&make_identity("a")));
        assert_eq!(g.relationship_count(), 0);
        assert!(g.get_relationships_to(&make_identity("b")).is_empty());
        assert!(g.neighbors_to(&make_identity("b")).is_empty());
        assert!(!g.contains_resource(&make_identity("a")));
        assert!(g.contains_resource(&make_identity("b")));
        assert_eq!(g.resource_count(), 1);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 17. Removing resource leaves other relationships intact
    // -----------------------------------------------------------------------
    #[test]
    fn test_removing_resource_leaves_other_relationships_intact() {
        let mut g = Graph::new();
        let r1 = make_rel("a", "b", RelationshipKind::DEPENDS_ON);
        let r2 = make_rel("c", "d", RelationshipKind::CALLS);
        g.add_relationship(r1);
        g.add_relationship(r2.clone());

        assert_eq!(g.relationship_count(), 2);
        assert_eq!(g.resource_count(), 4);

        // Remove A
        assert!(g.remove_resource(&make_identity("a")));
        assert_eq!(g.relationship_count(), 1);
        assert_eq!(g.resource_count(), 3);

        // C -> D must still be fully intact
        assert_eq!(
            g.get_relationships_from(&make_identity("c")),
            vec![r2.clone()]
        );
        assert_eq!(g.get_relationships_to(&make_identity("d")), vec![r2]);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 18. Self-loop behavior
    // -----------------------------------------------------------------------
    #[test]
    fn test_self_loop_behavior() {
        let mut g = Graph::new();
        let loop_rel = make_rel("a", "a", RelationshipKind::DEPENDS_ON);
        assert!(g.add_relationship(loop_rel.clone()));

        assert_eq!(g.resource_count(), 1);
        assert_eq!(g.relationship_count(), 1);
        assert!(g.contains_resource(&make_identity("a")));
        assert!(g.contains_relationship(&loop_rel));

        assert_eq!(
            g.get_relationships_from(&make_identity("a")),
            vec![loop_rel.clone()]
        );
        assert_eq!(
            g.get_relationships_to(&make_identity("a")),
            vec![loop_rel.clone()]
        );
        assert_eq!(
            g.neighbors_from(&make_identity("a")),
            vec![make_identity("a")]
        );
        assert_eq!(
            g.neighbors_to(&make_identity("a")),
            vec![make_identity("a")]
        );
        assert!(g.check_invariants().is_ok());

        // Removing resource A with self-loop
        assert!(g.remove_resource(&make_identity("a")));
        assert_eq!(g.resource_count(), 0);
        assert_eq!(g.relationship_count(), 0);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 19. Deterministic query ordering
    // -----------------------------------------------------------------------
    #[test]
    fn test_deterministic_query_ordering() {
        let mut g = Graph::new();
        // Insert in reverse/random order
        g.add_relationship(make_rel("a", "z", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("a", "m", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));

        let out = g.get_relationships_from(&make_identity("a"));
        // Expect sorted by (source, target, kind):
        // a -> b CALLS
        // a -> b DEPENDS_ON
        // a -> m DEPENDS_ON
        // a -> z DEPENDS_ON
        assert_eq!(out.len(), 4);
        assert_eq!(out[0].target.provider_id, "b");
        assert_eq!(out[0].kind, RelationshipKind::CALLS);
        assert_eq!(out[1].target.provider_id, "b");
        assert_eq!(out[1].kind, RelationshipKind::DEPENDS_ON);
        assert_eq!(out[2].target.provider_id, "m");
        assert_eq!(out[3].target.provider_id, "z");

        // Neighbors sorted and deduplicated
        let neighbors = g.neighbors_from(&make_identity("a"));
        assert_eq!(
            neighbors,
            vec![make_identity("b"), make_identity("m"), make_identity("z")]
        );
    }

    // -----------------------------------------------------------------------
    // 20. Empty query for unknown resource
    // -----------------------------------------------------------------------
    #[test]
    fn test_empty_query_for_unknown_resource() {
        let g = Graph::new();
        let unknown = make_identity("non_existent");
        assert!(g.get_relationships_from(&unknown).is_empty());
        assert!(g.get_relationships_to(&unknown).is_empty());
        assert!(g.neighbors_from(&unknown).is_empty());
        assert!(g.neighbors_to(&unknown).is_empty());
    }

    // -----------------------------------------------------------------------
    // 21. Relationship count correctness
    // -----------------------------------------------------------------------
    #[test]
    fn test_relationship_count_correctness() {
        let mut g = Graph::new();
        assert_eq!(g.relationship_count(), 0);

        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        assert_eq!(g.relationship_count(), 1);

        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS)); // duplicate
        assert_eq!(g.relationship_count(), 1);

        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON)); // distinct kind
        assert_eq!(g.relationship_count(), 2);

        g.add_relationship(make_rel("b", "b", RelationshipKind::OWNS)); // self-loop
        assert_eq!(g.relationship_count(), 3);

        g.remove_relationship(&make_rel("a", "b", RelationshipKind::CALLS));
        assert_eq!(g.relationship_count(), 2);

        g.remove_resource(&make_identity("b")); // removes A -> B (DEPENDS_ON) and B -> B (OWNS)
        assert_eq!(g.relationship_count(), 0);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 22. Resource count correctness
    // -----------------------------------------------------------------------
    #[test]
    fn test_resource_count_correctness() {
        let mut g = Graph::new();
        assert_eq!(g.resource_count(), 0);

        g.add_resource(make_identity("a"));
        assert_eq!(g.resource_count(), 1);

        g.add_resource(make_identity("a")); // duplicate
        assert_eq!(g.resource_count(), 1);

        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS)); // adds B
        assert_eq!(g.resource_count(), 2);

        g.add_relationship(make_rel("c", "d", RelationshipKind::DEPENDS_ON)); // adds C, D
        assert_eq!(g.resource_count(), 4);

        g.remove_resource(&make_identity("c"));
        assert_eq!(g.resource_count(), 3);

        g.clear();
        assert_eq!(g.resource_count(), 0);
        assert!(g.is_empty());
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 23. Repeated add/remove operations
    // -----------------------------------------------------------------------
    #[test]
    fn test_repeated_add_remove_operations() {
        let mut g = Graph::new();
        let rel = make_rel("a", "b", RelationshipKind::DEPENDS_ON);

        for _ in 0..5 {
            assert!(g.add_relationship(rel.clone()));
            assert_eq!(g.relationship_count(), 1);
            assert_eq!(g.resource_count(), 2);
            assert!(g.check_invariants().is_ok());

            assert!(g.remove_relationship(&rel));
            assert_eq!(g.relationship_count(), 0);
            assert_eq!(g.resource_count(), 2); // endpoints remain
            assert!(g.check_invariants().is_ok());
        }

        assert!(g.remove_resource(&make_identity("a")));
        assert_eq!(g.resource_count(), 1);
        assert!(g.remove_resource(&make_identity("b")));
        assert_eq!(g.resource_count(), 0);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 24. Graph consistency after multiple mutations
    // -----------------------------------------------------------------------
    #[test]
    fn test_graph_consistency_after_multiple_mutations() {
        let mut g = Graph::new();

        // Build a mesh
        for i in 0..5 {
            for j in 0..5 {
                let rel = make_rel(
                    &format!("node-{}", i),
                    &format!("node-{}", j),
                    RelationshipKind::DEPENDS_ON,
                );
                g.add_relationship(rel);
            }
        }

        assert_eq!(g.resource_count(), 5);
        assert_eq!(g.relationship_count(), 25);
        assert!(g.check_invariants().is_ok());

        // Remove node-2 (removes 5 outgoing + 5 incoming - 1 self-loop = 9 edges)
        assert!(g.remove_resource(&make_identity("node-2")));
        assert_eq!(g.resource_count(), 4);
        assert_eq!(g.relationship_count(), 16);
        assert!(g.check_invariants().is_ok());

        // Remove an edge
        assert!(g.remove_relationship(&make_rel("node-0", "node-1", RelationshipKind::DEPENDS_ON)));
        assert_eq!(g.relationship_count(), 15);
        assert!(g.check_invariants().is_ok());

        // Add custom category relationship
        let custom_rel = Relationship::with_category(
            make_identity("node-0"),
            make_identity("node-3"),
            RelationshipKind::new("PEERS_WITH"),
            RelationshipCategory::Network,
        );
        assert!(g.add_relationship(custom_rel.clone()));
        assert_eq!(g.relationship_count(), 16);
        assert!(g.contains_relationship(&custom_rel));
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 25. Thread safety (Send + Sync)
    // -----------------------------------------------------------------------
    #[test]
    fn test_send_sync_thread_safety() {
        fn assert_send_sync<T: Send + Sync>() {}
        assert_send_sync::<Graph>();
    }
}
