package state

import (
	"context"
	"errors"
)

var (
	// ErrResourceNotFound is returned when a requested resource is not present.
	ErrResourceNotFound = errors.New("state store: resource not found")

	// ErrRelationshipNotFound is returned when a requested relationship is not present.
	ErrRelationshipNotFound = errors.New("state store: relationship not found")

	// ErrInvalidWorkspace is returned when workspace ID is missing or invalid.
	ErrInvalidWorkspace = errors.New("state store: workspace_id cannot be empty")
)

// Store defines the persistent storage contract for universal WhatBreaks state.
// All operations are explicitly partitioned by workspace_id to guarantee multi-tenant isolation.
type Store interface {
	// SaveResources registers or updates resources within the specified workspace.
	SaveResources(ctx context.Context, workspaceID string, resources []Resource) error

	// SaveEvidence stores immutable evidence observations within the specified workspace.
	SaveEvidence(ctx context.Context, workspaceID string, evidence []Evidence) error

	// SaveRelationships stores discovered relationships within the specified workspace.
	SaveRelationships(ctx context.Context, workspaceID string, relationships []Relationship) error

	// SaveProvenance records many-to-many associations between relationships and evidence IDs.
	SaveProvenance(ctx context.Context, workspaceID string, associations []ProvenanceAssociation) error

	// SaveConflict records a discovery contradiction for diagnostics.
	SaveConflict(ctx context.Context, conflict DiscoveryConflict) error

	// GetResource retrieves a specific resource by identity within the workspace.
	GetResource(ctx context.Context, workspaceID string, identity ResourceIdentity) (*Resource, error)

	// GetRelationship retrieves a specific relationship by key within the workspace.
	GetRelationship(ctx context.Context, workspaceID string, rel RelationshipKey) (*Relationship, error)

	// ListResources returns all resources registered in the workspace.
	ListResources(ctx context.Context, workspaceID string) ([]Resource, error)

	// ListRelationships returns all relationships active in the workspace.
	ListRelationships(ctx context.Context, workspaceID string) ([]Relationship, error)

	// ListEvidence returns all historical evidence observations in the workspace.
	ListEvidence(ctx context.Context, workspaceID string) ([]Evidence, error)

	// GetProvenanceForRelationship returns all evidence IDs supporting a specific relationship.
	GetProvenanceForRelationship(ctx context.Context, workspaceID string, rel RelationshipKey) ([]string, error)

	// GetRelationshipsForEvidence returns all relationships supported by an evidence ID.
	GetRelationshipsForEvidence(ctx context.Context, workspaceID string, evidenceID string) ([]RelationshipKey, error)

	// LoadAssembledState reconstructs the complete domain state snapshot for a workspace.
	LoadAssembledState(ctx context.Context, workspaceID string) (*AssembledState, error)

	// DeleteWorkspaceState clears all stored state for a workspace.
	DeleteWorkspaceState(ctx context.Context, workspaceID string) error
}
