//! WhatBreaks Traversal Engine v1
//!
//! A lightweight, deterministic, in-memory graph traversal layer operating on
//! [`Graph`](crate::graph::Graph) and [`ResourceIdentity`](crate::resource::ResourceIdentity).
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
//!     Traversal (Traversal v1 — Directional Reachability & Hop Depth)
//!         │
//!         ▼
//!  Impact / Blast Radius (Future Engine)
//! ```
//!
//! # Design Principles
//!
//! - **ResourceIdentity-Based**: Operates strictly on [`ResourceIdentity`], not internal
//!   UUIDs or full [`Resource`](crate::Resource) objects.
//! - **Bounded Breadth-First Search (BFS)**: Uses level-by-level BFS with visited tracking
//!   to guarantee shortest relationship-hop depth for each reachable resource.
//! - **Strict Directionality**: Distinguishes [`TraversalDirection::Outgoing`] (`source -> target`)
//!   from [`TraversalDirection::Incoming`] (`target -> source`). Direction is never reversed
//!   implicitly.
//! - **Cycle & Self-Loop Safe**: Visited set prevents infinite loops and guarantees termination
//!   on cyclic graphs and self-loops.
//! - **Deterministic Ordering**: Nodes discovered at the same depth are returned in canonical
//!   [`ResourceIdentity`] sort order `(provider, resource_type, provider_id)`.
//! - **Read-Only**: Traversal borrows the graph immutably (`&Graph`) and never mutates graph state.
//! - **No Impact / Blast-Radius Logic**: Answers only reachability within $N$ hops.
//!   Domain impact scoring, blast radius calculation, and risk evaluation belong to downstream engines.

use std::collections::HashSet;

use crate::graph::Graph;
use crate::resource::ResourceIdentity;

// ---------------------------------------------------------------------------
// Sorting Helper
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

// ---------------------------------------------------------------------------
// TraversalDirection
// ---------------------------------------------------------------------------

/// The direction of relationship traversal in the graph.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum TraversalDirection {
    /// Follow relationships in their forward direction (`source -> target`).
    Outgoing,
    /// Follow relationships in their reverse direction (`target -> source`).
    Incoming,
}

// ---------------------------------------------------------------------------
// TraversalNode
// ---------------------------------------------------------------------------

/// A node visited during traversal, recording the resource identity and its
/// minimum hop distance (`depth`) from the start resource.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TraversalNode {
    /// The identity of the visited resource.
    pub resource: ResourceIdentity,
    /// Minimum relationship hop distance from the start resource.
    /// The start resource itself is always at `depth = 0`.
    pub depth: usize,
}

impl TraversalNode {
    /// Construct a new `TraversalNode`.
    pub fn new(resource: ResourceIdentity, depth: usize) -> Self {
        Self { resource, depth }
    }
}

// ---------------------------------------------------------------------------
// TraversalResult
// ---------------------------------------------------------------------------

/// The result of a graph traversal, containing all reachable nodes with their depths.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct TraversalResult {
    /// The visited nodes in traversal order (breadth-first, deterministically sorted per depth).
    pub nodes: Vec<TraversalNode>,
}

impl TraversalResult {
    /// Construct a new `TraversalResult` from a vector of `TraversalNode`s.
    pub fn new(nodes: Vec<TraversalNode>) -> Self {
        Self { nodes }
    }

    /// Return the number of nodes in the traversal result.
    pub fn len(&self) -> usize {
        self.nodes.len()
    }

    /// Return `true` if the traversal result contains no nodes.
    pub fn is_empty(&self) -> bool {
        self.nodes.is_empty()
    }

    /// Return an iterator over the traversal nodes.
    pub fn iter(&self) -> std::slice::Iter<'_, TraversalNode> {
        self.nodes.iter()
    }

    /// Return a slice of the traversal nodes.
    pub fn as_slice(&self) -> &[TraversalNode] {
        &self.nodes
    }

    /// Return all resource identities in the traversal result, preserving traversal order.
    pub fn resources(&self) -> Vec<ResourceIdentity> {
        self.nodes.iter().map(|n| n.resource.clone()).collect()
    }

    /// Check if a specific resource identity was reached during traversal.
    pub fn contains(&self, resource: &ResourceIdentity) -> bool {
        self.nodes.iter().any(|n| &n.resource == resource)
    }

    /// Return the depth of a specific resource if it was reached during traversal.
    pub fn depth_of(&self, resource: &ResourceIdentity) -> Option<usize> {
        self.nodes
            .iter()
            .find(|n| &n.resource == resource)
            .map(|n| n.depth)
    }
}

impl IntoIterator for TraversalResult {
    type Item = TraversalNode;
    type IntoIter = std::vec::IntoIter<TraversalNode>;

    fn into_iter(self) -> Self::IntoIter {
        self.nodes.into_iter()
    }
}

impl<'a> IntoIterator for &'a TraversalResult {
    type Item = &'a TraversalNode;
    type IntoIter = std::slice::Iter<'a, TraversalNode>;

    fn into_iter(self) -> Self::IntoIter {
        self.nodes.iter()
    }
}

// ---------------------------------------------------------------------------
// Traversal Engine
// ---------------------------------------------------------------------------

/// Stateless graph traversal engine.
#[derive(Debug, Clone, Copy, Default)]
pub struct Traversal;

impl Traversal {
    /// Traverse the graph starting from `start` in the specified `direction`,
    /// up to a maximum relationship hop distance of `max_depth`.
    ///
    /// # Semantics
    /// - If `start` is not present in `graph`, returns an empty [`TraversalResult`].
    /// - The starting resource is always included at `depth = 0`.
    /// - `max_depth = 0` returns only `start` at `depth = 0`.
    /// - Follows edges strictly according to `direction`:
    ///   - [`TraversalDirection::Outgoing`]: follows `source -> target` edges.
    ///   - [`TraversalDirection::Incoming`]: follows `target -> source` edges.
    /// - Breadth-first exploration ensures each visited node is recorded at its
    ///   minimum hop distance from `start`.
    /// - Cycles and self-loops terminate cleanly; no node is visited more than once.
    /// - Nodes discovered at the same depth are sorted deterministically by
    ///   `(provider, resource_type, provider_id)`.
    /// - Does not mutate `graph`.
    pub fn traverse(
        graph: &Graph,
        start: &ResourceIdentity,
        direction: TraversalDirection,
        max_depth: usize,
    ) -> TraversalResult {
        if !graph.contains_resource(start) {
            return TraversalResult::new(Vec::new());
        }

        let mut visited: HashSet<ResourceIdentity> = HashSet::new();
        let mut nodes: Vec<TraversalNode> = Vec::new();

        visited.insert(start.clone());
        nodes.push(TraversalNode::new(start.clone(), 0));

        let mut current_level = vec![start.clone()];

        for current_depth in 0..max_depth {
            let mut next_level: Vec<ResourceIdentity> = Vec::new();

            for node in &current_level {
                let neighbors = match direction {
                    TraversalDirection::Outgoing => graph.neighbors_from(node),
                    TraversalDirection::Incoming => graph.neighbors_to(node),
                };

                for neighbor in neighbors {
                    if !visited.contains(&neighbor) {
                        visited.insert(neighbor.clone());
                        next_level.push(neighbor);
                    }
                }
            }

            if next_level.is_empty() {
                break;
            }

            // Ensure deterministic ordering for all nodes at this depth
            next_level.sort_by(cmp_resource_identity);

            let depth = current_depth + 1;
            for resource in &next_level {
                nodes.push(TraversalNode::new(resource.clone(), depth));
            }

            current_level = next_level;
        }

        TraversalResult::new(nodes)
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
        let start = make_identity("a");
        let res = Traversal::traverse(&g, &start, TraversalDirection::Outgoing, 5);
        assert!(res.is_empty());
        assert_eq!(res.len(), 0);
        assert!(res.nodes.is_empty());
    }

    // -----------------------------------------------------------------------
    // 2. Single resource
    // -----------------------------------------------------------------------
    #[test]
    fn test_single_resource() {
        let mut g = Graph::new();
        let a = make_identity("a");
        g.add_resource(a.clone());

        let res = Traversal::traverse(&g, &a, TraversalDirection::Outgoing, 5);
        assert_eq!(res.len(), 1);
        assert_eq!(res.nodes[0].resource, a);
        assert_eq!(res.nodes[0].depth, 0);

        let res_inc = Traversal::traverse(&g, &a, TraversalDirection::Incoming, 5);
        assert_eq!(res_inc.len(), 1);
        assert_eq!(res_inc.nodes[0].resource, a);
        assert_eq!(res_inc.nodes[0].depth, 0);
    }

    // -----------------------------------------------------------------------
    // 3. Unknown start resource
    // -----------------------------------------------------------------------
    #[test]
    fn test_unknown_start_resource() {
        let mut g = Graph::new();
        g.add_resource(make_identity("a"));

        let unknown = make_identity("missing");
        let res = Traversal::traverse(&g, &unknown, TraversalDirection::Outgoing, 5);
        assert!(res.is_empty());
        assert_eq!(res.len(), 0);
    }

    // -----------------------------------------------------------------------
    // 4. Starting node appears at depth 0
    // -----------------------------------------------------------------------
    #[test]
    fn test_starting_node_appears_at_depth_0() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));

        let res = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 1);
        assert_eq!(res.nodes[0].resource, make_identity("a"));
        assert_eq!(res.nodes[0].depth, 0);
    }

    // -----------------------------------------------------------------------
    // 5. Simple outgoing traversal
    // -----------------------------------------------------------------------
    #[test]
    fn test_simple_outgoing_traversal() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));

        let res = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 1);
        assert_eq!(res.len(), 2);
        assert_eq!(res.nodes[0].resource, make_identity("a"));
        assert_eq!(res.nodes[0].depth, 0);
        assert_eq!(res.nodes[1].resource, make_identity("b"));
        assert_eq!(res.nodes[1].depth, 1);
    }

    // -----------------------------------------------------------------------
    // 6. Simple incoming traversal
    // -----------------------------------------------------------------------
    #[test]
    fn test_simple_incoming_traversal() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));

        let res = Traversal::traverse(&g, &make_identity("b"), TraversalDirection::Incoming, 1);
        assert_eq!(res.len(), 2);
        assert_eq!(res.nodes[0].resource, make_identity("b"));
        assert_eq!(res.nodes[0].depth, 0);
        assert_eq!(res.nodes[1].resource, make_identity("a"));
        assert_eq!(res.nodes[1].depth, 1);
    }

    // -----------------------------------------------------------------------
    // 7. Multi-level outgoing traversal
    // -----------------------------------------------------------------------
    #[test]
    fn test_multi_level_outgoing_traversal() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "c", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("c", "d", RelationshipKind::DEPENDS_ON));

        let res = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 3);
        assert_eq!(res.len(), 4);
        assert_eq!(res.nodes[0], TraversalNode::new(make_identity("a"), 0));
        assert_eq!(res.nodes[1], TraversalNode::new(make_identity("b"), 1));
        assert_eq!(res.nodes[2], TraversalNode::new(make_identity("c"), 2));
        assert_eq!(res.nodes[3], TraversalNode::new(make_identity("d"), 3));
    }

    // -----------------------------------------------------------------------
    // 8. Multi-level incoming traversal
    // -----------------------------------------------------------------------
    #[test]
    fn test_multi_level_incoming_traversal() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "c", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("c", "d", RelationshipKind::DEPENDS_ON));

        let res = Traversal::traverse(&g, &make_identity("d"), TraversalDirection::Incoming, 3);
        assert_eq!(res.len(), 4);
        assert_eq!(res.nodes[0], TraversalNode::new(make_identity("d"), 0));
        assert_eq!(res.nodes[1], TraversalNode::new(make_identity("c"), 1));
        assert_eq!(res.nodes[2], TraversalNode::new(make_identity("b"), 2));
        assert_eq!(res.nodes[3], TraversalNode::new(make_identity("a"), 3));
    }

    // -----------------------------------------------------------------------
    // 9. max_depth = 0
    // -----------------------------------------------------------------------
    #[test]
    fn test_max_depth_zero() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "c", RelationshipKind::CALLS));

        let res = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 0);
        assert_eq!(res.len(), 1);
        assert_eq!(res.nodes[0], TraversalNode::new(make_identity("a"), 0));
    }

    // -----------------------------------------------------------------------
    // 10. max_depth = 1
    // -----------------------------------------------------------------------
    #[test]
    fn test_max_depth_one() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "c", RelationshipKind::CALLS));

        let res = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 1);
        assert_eq!(res.len(), 2);
        assert_eq!(res.nodes[0], TraversalNode::new(make_identity("a"), 0));
        assert_eq!(res.nodes[1], TraversalNode::new(make_identity("b"), 1));
    }

    // -----------------------------------------------------------------------
    // 11. max_depth limiting deeper nodes
    // -----------------------------------------------------------------------
    #[test]
    fn test_max_depth_limiting_deeper_nodes() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "c", RelationshipKind::CALLS));
        g.add_relationship(make_rel("c", "d", RelationshipKind::CALLS));
        g.add_relationship(make_rel("d", "e", RelationshipKind::CALLS));

        let res = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 2);
        assert_eq!(res.len(), 3);
        assert_eq!(
            res.resources(),
            vec![make_identity("a"), make_identity("b"), make_identity("c")]
        );
        assert_eq!(res.depth_of(&make_identity("d")), None);
        assert_eq!(res.depth_of(&make_identity("e")), None);
    }

    // -----------------------------------------------------------------------
    // 12. Direction preservation
    // -----------------------------------------------------------------------
    #[test]
    fn test_direction_preservation() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "c", RelationshipKind::DEPENDS_ON));

        // Outgoing from C: C has no outgoing edges
        let res_out_c =
            Traversal::traverse(&g, &make_identity("c"), TraversalDirection::Outgoing, 2);
        assert_eq!(res_out_c.len(), 1);
        assert_eq!(res_out_c.nodes[0].resource, make_identity("c"));

        // Incoming from A: A has no incoming edges
        let res_in_a =
            Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Incoming, 2);
        assert_eq!(res_in_a.len(), 1);
        assert_eq!(res_in_a.nodes[0].resource, make_identity("a"));
    }

    // -----------------------------------------------------------------------
    // 13. Cycle termination
    // -----------------------------------------------------------------------
    #[test]
    fn test_cycle_termination() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "c", RelationshipKind::CALLS));
        g.add_relationship(make_rel("c", "a", RelationshipKind::CALLS)); // Cycle

        let res = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 10);
        assert_eq!(res.len(), 3);
        assert_eq!(res.nodes[0], TraversalNode::new(make_identity("a"), 0));
        assert_eq!(res.nodes[1], TraversalNode::new(make_identity("b"), 1));
        assert_eq!(res.nodes[2], TraversalNode::new(make_identity("c"), 2));
    }

    // -----------------------------------------------------------------------
    // 14. Self-loop handling
    // -----------------------------------------------------------------------
    #[test]
    fn test_self_loop_handling() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "a", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));

        let res = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 5);
        assert_eq!(res.len(), 2);
        assert_eq!(res.nodes[0], TraversalNode::new(make_identity("a"), 0));
        assert_eq!(res.nodes[1], TraversalNode::new(make_identity("b"), 1));
    }

    // -----------------------------------------------------------------------
    // 15. Diamond graph deduplication
    // -----------------------------------------------------------------------
    #[test]
    fn test_diamond_graph_deduplication() {
        let mut g = Graph::new();
        //   A
        //  / \
        // B   C
        //  \ /
        //   D
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("a", "c", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "d", RelationshipKind::DEPENDS_ON));
        g.add_relationship(make_rel("c", "d", RelationshipKind::DEPENDS_ON));

        let res = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 5);
        assert_eq!(res.len(), 4);
        assert_eq!(res.nodes[0], TraversalNode::new(make_identity("a"), 0));
        assert_eq!(res.nodes[1], TraversalNode::new(make_identity("b"), 1));
        assert_eq!(res.nodes[2], TraversalNode::new(make_identity("c"), 1));
        assert_eq!(res.nodes[3], TraversalNode::new(make_identity("d"), 2));

        // D must appear exactly once at depth 2
        assert_eq!(res.depth_of(&make_identity("d")), Some(2));
    }

    // -----------------------------------------------------------------------
    // 16. Multiple relationship kinds between same resources
    // -----------------------------------------------------------------------
    #[test]
    fn test_multiple_relationship_kinds_between_same_resources() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("a", "b", RelationshipKind::DEPENDS_ON));

        let res = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 2);
        assert_eq!(res.len(), 2);
        assert_eq!(res.nodes[0], TraversalNode::new(make_identity("a"), 0));
        assert_eq!(res.nodes[1], TraversalNode::new(make_identity("b"), 1));
    }

    // -----------------------------------------------------------------------
    // 17. Deterministic ordering
    // -----------------------------------------------------------------------
    #[test]
    fn test_deterministic_ordering() {
        let mut g = Graph::new();
        // Insert neighbors in reverse order: z, m, b
        g.add_relationship(make_rel("a", "z", RelationshipKind::CALLS));
        g.add_relationship(make_rel("a", "m", RelationshipKind::CALLS));
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));

        let res = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 1);
        assert_eq!(res.len(), 4);
        assert_eq!(res.nodes[0].resource.provider_id, "a");
        // Nodes at depth 1 must be sorted deterministically: b, m, z
        assert_eq!(res.nodes[1].resource.provider_id, "b");
        assert_eq!(res.nodes[2].resource.provider_id, "m");
        assert_eq!(res.nodes[3].resource.provider_id, "z");
    }

    // -----------------------------------------------------------------------
    // 18. Repeated traversal produces identical results
    // -----------------------------------------------------------------------
    #[test]
    fn test_repeated_traversal_produces_identical_results() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("a", "c", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "d", RelationshipKind::CALLS));
        g.add_relationship(make_rel("c", "e", RelationshipKind::CALLS));

        let run1 = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 2);
        let run2 = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 2);
        let run3 = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 2);

        assert_eq!(run1, run2);
        assert_eq!(run2, run3);
    }

    // -----------------------------------------------------------------------
    // 19. Multiple branches at same depth
    // -----------------------------------------------------------------------
    #[test]
    fn test_multiple_branches_at_same_depth() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b1", RelationshipKind::CALLS));
        g.add_relationship(make_rel("a", "b2", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b1", "c1", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b2", "c2", RelationshipKind::CALLS));

        let res = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 2);
        assert_eq!(res.len(), 5);
        assert_eq!(res.nodes[0].resource, make_identity("a"));
        assert_eq!(res.nodes[0].depth, 0);
        assert_eq!(res.nodes[1].resource, make_identity("b1"));
        assert_eq!(res.nodes[1].depth, 1);
        assert_eq!(res.nodes[2].resource, make_identity("b2"));
        assert_eq!(res.nodes[2].depth, 1);
        assert_eq!(res.nodes[3].resource, make_identity("c1"));
        assert_eq!(res.nodes[3].depth, 2);
        assert_eq!(res.nodes[4].resource, make_identity("c2"));
        assert_eq!(res.nodes[4].depth, 2);
    }

    // -----------------------------------------------------------------------
    // 20. Disconnected resources are not included
    // -----------------------------------------------------------------------
    #[test]
    fn test_disconnected_resources_are_not_included() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel(
            "isolated_1",
            "isolated_2",
            RelationshipKind::CALLS,
        ));

        let res = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 5);
        assert_eq!(res.len(), 2);
        assert!(res.contains(&make_identity("a")));
        assert!(res.contains(&make_identity("b")));
        assert!(!res.contains(&make_identity("isolated_1")));
        assert!(!res.contains(&make_identity("isolated_2")));
    }

    // -----------------------------------------------------------------------
    // 21. Removing graph resources is respected
    // -----------------------------------------------------------------------
    #[test]
    fn test_removing_graph_resources_is_respected() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "c", RelationshipKind::CALLS));

        // Before removal
        let before = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 2);
        assert_eq!(before.len(), 3);

        // Remove intermediate node B
        g.remove_resource(&make_identity("b"));

        // After removal: B and edge A->B, B->C are gone
        let after = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 2);
        assert_eq!(after.len(), 1);
        assert_eq!(after.nodes[0].resource, make_identity("a"));
    }

    // -----------------------------------------------------------------------
    // 22. Empty result after querying unknown resource
    // -----------------------------------------------------------------------
    #[test]
    fn test_empty_result_after_querying_unknown_resource() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));

        let res = Traversal::traverse(&g, &make_identity("ghost"), TraversalDirection::Outgoing, 2);
        assert!(res.is_empty());
        assert_eq!(res.len(), 0);
    }

    // -----------------------------------------------------------------------
    // 23. Large/simple chain traversal
    // -----------------------------------------------------------------------
    #[test]
    fn test_large_chain_traversal() {
        let mut g = Graph::new();
        let chain_len = 50;
        for i in 0..chain_len {
            g.add_relationship(make_rel(
                &format!("node_{}", i),
                &format!("node_{}", i + 1),
                RelationshipKind::DEPENDS_ON,
            ));
        }

        let start = make_identity("node_0");
        let res = Traversal::traverse(&g, &start, TraversalDirection::Outgoing, 25);
        assert_eq!(res.len(), 26); // depth 0 through 25
        for i in 0..=25 {
            assert_eq!(res.nodes[i].depth, i);
            assert_eq!(res.nodes[i].resource, make_identity(&format!("node_{}", i)));
        }

        // Depth capping
        let res_capped = Traversal::traverse(&g, &start, TraversalDirection::Outgoing, 10);
        assert_eq!(res_capped.len(), 11);
        assert_eq!(res_capped.nodes.last().unwrap().depth, 10);
    }

    // -----------------------------------------------------------------------
    // 24. Incoming reverse traversal for future blast-radius use
    // -----------------------------------------------------------------------
    #[test]
    fn test_incoming_reverse_traversal_architecture_example() {
        let mut g = Graph::new();
        let frontend = make_custom_identity("kubernetes", "deployment", "frontend");
        let api = make_custom_identity("kubernetes", "service", "api");
        let db = make_custom_identity("aws", "rds", "database");

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

        // Outgoing from Frontend: Frontend -> API -> Database
        let out_res = Traversal::traverse(&g, &frontend, TraversalDirection::Outgoing, 2);
        assert_eq!(out_res.len(), 3);
        assert_eq!(out_res.nodes[0], TraversalNode::new(frontend.clone(), 0));
        assert_eq!(out_res.nodes[1], TraversalNode::new(api.clone(), 1));
        assert_eq!(out_res.nodes[2], TraversalNode::new(db.clone(), 2));

        // Incoming from Database (Reverse Dependency / Blast Radius): DB <- API <- Frontend
        let in_res = Traversal::traverse(&g, &db, TraversalDirection::Incoming, 2);
        assert_eq!(in_res.len(), 3);
        assert_eq!(in_res.nodes[0], TraversalNode::new(db.clone(), 0));
        assert_eq!(in_res.nodes[1], TraversalNode::new(api.clone(), 1));
        assert_eq!(in_res.nodes[2], TraversalNode::new(frontend.clone(), 2));
    }

    // -----------------------------------------------------------------------
    // 25. Traversal does not mutate Graph
    // -----------------------------------------------------------------------
    #[test]
    fn test_traversal_does_not_mutate_graph() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));
        g.add_relationship(make_rel("b", "c", RelationshipKind::CALLS));

        let initial_resources = g.resources();
        let initial_relationships = g.relationships();
        let initial_res_count = g.resource_count();
        let initial_rel_count = g.relationship_count();

        let _ = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 5);
        let _ = Traversal::traverse(&g, &make_identity("c"), TraversalDirection::Incoming, 5);
        let _ = Traversal::traverse(
            &g,
            &make_identity("unknown"),
            TraversalDirection::Outgoing,
            5,
        );

        assert_eq!(g.resources(), initial_resources);
        assert_eq!(g.relationships(), initial_relationships);
        assert_eq!(g.resource_count(), initial_res_count);
        assert_eq!(g.relationship_count(), initial_rel_count);
        assert!(g.check_invariants().is_ok());
    }

    // -----------------------------------------------------------------------
    // 26. Thread safety (Send + Sync)
    // -----------------------------------------------------------------------
    #[test]
    fn test_send_sync_thread_safety() {
        fn assert_send_sync<T: Send + Sync>() {}
        assert_send_sync::<Traversal>();
        assert_send_sync::<TraversalDirection>();
        assert_send_sync::<TraversalNode>();
        assert_send_sync::<TraversalResult>();
    }

    // -----------------------------------------------------------------------
    // 27. Result convenience methods
    // -----------------------------------------------------------------------
    #[test]
    fn test_traversal_result_methods() {
        let mut g = Graph::new();
        g.add_relationship(make_rel("a", "b", RelationshipKind::CALLS));

        let res = Traversal::traverse(&g, &make_identity("a"), TraversalDirection::Outgoing, 1);
        assert!(!res.is_empty());
        assert_eq!(res.len(), 2);
        assert!(res.contains(&make_identity("a")));
        assert!(res.contains(&make_identity("b")));
        assert!(!res.contains(&make_identity("c")));
        assert_eq!(res.depth_of(&make_identity("a")), Some(0));
        assert_eq!(res.depth_of(&make_identity("b")), Some(1));
        assert_eq!(res.depth_of(&make_identity("c")), None);

        // Iteration
        let count = res.iter().count();
        assert_eq!(count, 2);
        let into_count = res.into_iter().count();
        assert_eq!(into_count, 2);
    }
}
