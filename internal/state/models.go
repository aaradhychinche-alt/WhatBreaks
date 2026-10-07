package state

import (
	"fmt"
	"time"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
)

// ResourceIdentity uniquely identifies an infrastructure resource across providers.
// Provider-agnostic: does not contain Kubernetes-specific identity fields.
type ResourceIdentity struct {
	Provider     string `json:"provider"`
	ResourceType string `json:"resource_type"`
	ProviderID   string `json:"provider_id"`
}

// String returns a canonical representation of the resource identity.
func (r ResourceIdentity) String() string {
	return fmt.Sprintf("%s:%s:%s", r.Provider, r.ResourceType, r.ProviderID)
}

// Resource represents a registered infrastructure resource with observation lifecycle timestamps.
type Resource struct {
	WorkspaceID     string           `json:"workspace_id"`
	Identity        ResourceIdentity `json:"identity"`
	FirstObservedAt time.Time        `json:"first_observed_at"`
	LastObservedAt  time.Time        `json:"last_observed_at"`
}

// EvidenceSource identifies the origin of an evidence observation.
type EvidenceSource struct {
	Provider  string `json:"provider"`
	Collector string `json:"collector"`
}

// Evidence is an immutable historical observation.
type Evidence struct {
	WorkspaceID     string           `json:"workspace_id"`
	ID              string           `json:"id"`
	Source          EvidenceSource   `json:"source"`
	ObservedAt      time.Time        `json:"observed_at"`
	ObservationType string           `json:"observation_type"`
	Subject         ResourceIdentity `json:"subject"`
	Data            []byte           `json:"data"`
}

// RelationshipKey identifies a relationship naturally by source, target, and kind.
type RelationshipKey struct {
	Source ResourceIdentity `json:"source"`
	Target ResourceIdentity `json:"target"`
	Kind   string           `json:"kind"`
}

// String returns a canonical string representation for hashing and debugging.
func (k RelationshipKey) String() string {
	return fmt.Sprintf("%s -[%s]-> %s", k.Source.String(), k.Kind, k.Target.String())
}

// Relationship represents a semantic directed link between two infrastructure resources.
type Relationship struct {
	WorkspaceID     string           `json:"workspace_id"`
	Source          ResourceIdentity `json:"source"`
	Target          ResourceIdentity `json:"target"`
	Kind            string           `json:"kind"`
	Category        string           `json:"category"`
	FirstObservedAt time.Time        `json:"first_observed_at"`
	LastObservedAt  time.Time        `json:"last_observed_at"`
}

// Key returns the natural identity key of the relationship.
func (r Relationship) Key() RelationshipKey {
	return RelationshipKey{
		Source: r.Source,
		Target: r.Target,
		Kind:   r.Kind,
	}
}

// ProvenanceAssociation models the many-to-many link between a relationship and a supporting evidence ID.
type ProvenanceAssociation struct {
	WorkspaceID  string          `json:"workspace_id"`
	Relationship RelationshipKey `json:"relationship"`
	EvidenceID   string          `json:"evidence_id"`
}

// DiscoveryConflict records a contradiction observed during discovery.
type DiscoveryConflict struct {
	WorkspaceID string    `json:"workspace_id"`
	Description string    `json:"description"`
	ObservedAt  time.Time `json:"observed_at"`
}

// AssembledState represents the complete materialized domain snapshot for a specific workspace.
type AssembledState struct {
	WorkspaceID   string                       `json:"workspace_id"`
	Resources     []Resource                   `json:"resources"`
	Relationships []Relationship               `json:"relationships"`
	Evidence      []Evidence                   `json:"evidence"`
	Provenance    map[RelationshipKey][]string `json:"provenance"`
}

// ObservationBatch packages normalized evidence records for ingestion into a specific workspace.
type ObservationBatch struct {
	WorkspaceID string             `json:"workspace_id"`
	Evidence    []*corev1.Evidence `json:"evidence"`
}
