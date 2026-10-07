package state

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/database"
)

// PostgresStore implements Store backed by PostgreSQL.
type PostgresStore struct {
	db database.DB
}

// NewPostgresStore creates a PostgresStore backed by database.DB.
func NewPostgresStore(db database.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// EnsureSchema creates the necessary state tables if they do not already exist.
func EnsureSchema(ctx context.Context, db database.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS state_resources (
		workspace_id TEXT NOT NULL,
		provider TEXT NOT NULL,
		resource_type TEXT NOT NULL,
		provider_id TEXT NOT NULL,
		first_observed_at TIMESTAMPTZ NOT NULL,
		last_observed_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (workspace_id, provider, resource_type, provider_id)
	);

	CREATE TABLE IF NOT EXISTS state_evidence (
		workspace_id TEXT NOT NULL,
		id TEXT NOT NULL,
		source_provider TEXT NOT NULL,
		source_collector TEXT NOT NULL,
		observed_at TIMESTAMPTZ NOT NULL,
		observation_type TEXT NOT NULL,
		subject_provider TEXT NOT NULL,
		subject_resource_type TEXT NOT NULL,
		subject_provider_id TEXT NOT NULL,
		data BYTEA NOT NULL,
		PRIMARY KEY (workspace_id, id)
	);

	CREATE TABLE IF NOT EXISTS state_relationships (
		workspace_id TEXT NOT NULL,
		source_provider TEXT NOT NULL,
		source_resource_type TEXT NOT NULL,
		source_provider_id TEXT NOT NULL,
		target_provider TEXT NOT NULL,
		target_resource_type TEXT NOT NULL,
		target_provider_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		category TEXT NOT NULL,
		first_observed_at TIMESTAMPTZ NOT NULL,
		last_observed_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (workspace_id, source_provider, source_resource_type, source_provider_id, target_provider, target_resource_type, target_provider_id, kind)
	);

	CREATE TABLE IF NOT EXISTS state_provenance (
		workspace_id TEXT NOT NULL,
		source_provider TEXT NOT NULL,
		source_resource_type TEXT NOT NULL,
		source_provider_id TEXT NOT NULL,
		target_provider TEXT NOT NULL,
		target_resource_type TEXT NOT NULL,
		target_provider_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		evidence_id TEXT NOT NULL,
		PRIMARY KEY (workspace_id, source_provider, source_resource_type, source_provider_id, target_provider, target_resource_type, target_provider_id, kind, evidence_id)
	);

	CREATE TABLE IF NOT EXISTS state_conflicts (
		id BIGSERIAL PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		description TEXT NOT NULL,
		observed_at TIMESTAMPTZ NOT NULL
	);
	`
	_, err := db.Exec(ctx, schema)
	return err
}

func (s *PostgresStore) SaveResources(ctx context.Context, workspaceID string, resources []Resource) error {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return ErrInvalidWorkspace
	}
	if len(resources) == 0 {
		return nil
	}

	return s.db.WithTransaction(ctx, func(ctx context.Context, tx database.Tx) error {
		query := `
		INSERT INTO state_resources (workspace_id, provider, resource_type, provider_id, first_observed_at, last_observed_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (workspace_id, provider, resource_type, provider_id)
		DO UPDATE SET last_observed_at = GREATEST(state_resources.last_observed_at, EXCLUDED.last_observed_at);
		`
		for _, r := range resources {
			_, err := tx.Exec(ctx, query, ws, r.Identity.Provider, r.Identity.ResourceType, r.Identity.ProviderID, r.FirstObservedAt, r.LastObservedAt)
			if err != nil {
				return fmt.Errorf("failed to save resource %s: %w", r.Identity, err)
			}
		}
		return nil
	})
}

func (s *PostgresStore) SaveEvidence(ctx context.Context, workspaceID string, evidence []Evidence) error {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return ErrInvalidWorkspace
	}
	if len(evidence) == 0 {
		return nil
	}

	return s.db.WithTransaction(ctx, func(ctx context.Context, tx database.Tx) error {
		query := `
		INSERT INTO state_evidence (workspace_id, id, source_provider, source_collector, observed_at, observation_type, subject_provider, subject_resource_type, subject_provider_id, data)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (workspace_id, id) DO NOTHING;
		`
		for _, ev := range evidence {
			_, err := tx.Exec(ctx, query, ws, ev.ID, ev.Source.Provider, ev.Source.Collector, ev.ObservedAt, ev.ObservationType, ev.Subject.Provider, ev.Subject.ResourceType, ev.Subject.ProviderID, ev.Data)
			if err != nil {
				return fmt.Errorf("failed to save evidence %s: %w", ev.ID, err)
			}
		}
		return nil
	})
}

func (s *PostgresStore) SaveRelationships(ctx context.Context, workspaceID string, relationships []Relationship) error {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return ErrInvalidWorkspace
	}
	if len(relationships) == 0 {
		return nil
	}

	return s.db.WithTransaction(ctx, func(ctx context.Context, tx database.Tx) error {
		query := `
		INSERT INTO state_relationships (workspace_id, source_provider, source_resource_type, source_provider_id, target_provider, target_resource_type, target_provider_id, kind, category, first_observed_at, last_observed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (workspace_id, source_provider, source_resource_type, source_provider_id, target_provider, target_resource_type, target_provider_id, kind)
		DO UPDATE SET last_observed_at = GREATEST(state_relationships.last_observed_at, EXCLUDED.last_observed_at);
		`
		for _, rel := range relationships {
			_, err := tx.Exec(ctx, query, ws,
				rel.Source.Provider, rel.Source.ResourceType, rel.Source.ProviderID,
				rel.Target.Provider, rel.Target.ResourceType, rel.Target.ProviderID,
				rel.Kind, rel.Category, rel.FirstObservedAt, rel.LastObservedAt)
			if err != nil {
				return fmt.Errorf("failed to save relationship %s: %w", rel.Key(), err)
			}
		}
		return nil
	})
}

func (s *PostgresStore) SaveProvenance(ctx context.Context, workspaceID string, associations []ProvenanceAssociation) error {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return ErrInvalidWorkspace
	}
	if len(associations) == 0 {
		return nil
	}

	return s.db.WithTransaction(ctx, func(ctx context.Context, tx database.Tx) error {
		query := `
		INSERT INTO state_provenance (workspace_id, source_provider, source_resource_type, source_provider_id, target_provider, target_resource_type, target_provider_id, kind, evidence_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (workspace_id, source_provider, source_resource_type, source_provider_id, target_provider, target_resource_type, target_provider_id, kind, evidence_id) DO NOTHING;
		`
		for _, assoc := range associations {
			_, err := tx.Exec(ctx, query, ws,
				assoc.Relationship.Source.Provider, assoc.Relationship.Source.ResourceType, assoc.Relationship.Source.ProviderID,
				assoc.Relationship.Target.Provider, assoc.Relationship.Target.ResourceType, assoc.Relationship.Target.ProviderID,
				assoc.Relationship.Kind, assoc.EvidenceID)
			if err != nil {
				return fmt.Errorf("failed to save provenance association: %w", err)
			}
		}
		return nil
	})
}

func (s *PostgresStore) SaveConflict(ctx context.Context, conflict DiscoveryConflict) error {
	ws := strings.TrimSpace(conflict.WorkspaceID)
	if ws == "" {
		return ErrInvalidWorkspace
	}
	query := `INSERT INTO state_conflicts (workspace_id, description, observed_at) VALUES ($1, $2, $3)`
	_, err := s.db.Exec(ctx, query, ws, conflict.Description, conflict.ObservedAt)
	return err
}

func (s *PostgresStore) GetResource(ctx context.Context, workspaceID string, identity ResourceIdentity) (*Resource, error) {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return nil, ErrInvalidWorkspace
	}

	query := `
	SELECT provider, resource_type, provider_id, first_observed_at, last_observed_at
	FROM state_resources
	WHERE workspace_id = $1 AND provider = $2 AND resource_type = $3 AND provider_id = $4;
	`
	row := s.db.QueryRow(ctx, query, ws, identity.Provider, identity.ResourceType, identity.ProviderID)
	var r Resource
	r.WorkspaceID = ws
	err := row.Scan(&r.Identity.Provider, &r.Identity.ResourceType, &r.Identity.ProviderID, &r.FirstObservedAt, &r.LastObservedAt)
	if err != nil {
		return nil, ErrResourceNotFound
	}
	return &r, nil
}

func (s *PostgresStore) ListResources(ctx context.Context, workspaceID string) ([]Resource, error) {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return nil, ErrInvalidWorkspace
	}

	query := `
	SELECT provider, resource_type, provider_id, first_observed_at, last_observed_at
	FROM state_resources
	WHERE workspace_id = $1
	ORDER BY provider, resource_type, provider_id;
	`
	rows, err := s.db.Query(ctx, query, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Resource
	for rows.Next() {
		var r Resource
		r.WorkspaceID = ws
		if err := rows.Scan(&r.Identity.Provider, &r.Identity.ResourceType, &r.Identity.ProviderID, &r.FirstObservedAt, &r.LastObservedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListRelationships(ctx context.Context, workspaceID string) ([]Relationship, error) {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return nil, ErrInvalidWorkspace
	}

	query := `
	SELECT source_provider, source_resource_type, source_provider_id,
	       target_provider, target_resource_type, target_provider_id,
	       kind, category, first_observed_at, last_observed_at
	FROM state_relationships
	WHERE workspace_id = $1
	ORDER BY source_provider, source_resource_type, source_provider_id, target_provider, target_resource_type, target_provider_id, kind;
	`
	rows, err := s.db.Query(ctx, query, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Relationship
	for rows.Next() {
		var rel Relationship
		rel.WorkspaceID = ws
		err := rows.Scan(
			&rel.Source.Provider, &rel.Source.ResourceType, &rel.Source.ProviderID,
			&rel.Target.Provider, &rel.Target.ResourceType, &rel.Target.ProviderID,
			&rel.Kind, &rel.Category, &rel.FirstObservedAt, &rel.LastObservedAt,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListEvidence(ctx context.Context, workspaceID string) ([]Evidence, error) {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return nil, ErrInvalidWorkspace
	}

	query := `
	SELECT id, source_provider, source_collector, observed_at, observation_type,
	       subject_provider, subject_resource_type, subject_provider_id, data
	FROM state_evidence
	WHERE workspace_id = $1
	ORDER BY observed_at, id;
	`
	rows, err := s.db.Query(ctx, query, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Evidence
	for rows.Next() {
		var ev Evidence
		ev.WorkspaceID = ws
		err := rows.Scan(
			&ev.ID, &ev.Source.Provider, &ev.Source.Collector, &ev.ObservedAt, &ev.ObservationType,
			&ev.Subject.Provider, &ev.Subject.ResourceType, &ev.Subject.ProviderID, &ev.Data,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetProvenanceForRelationship(ctx context.Context, workspaceID string, rel RelationshipKey) ([]string, error) {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return nil, ErrInvalidWorkspace
	}

	query := `
	SELECT evidence_id
	FROM state_provenance
	WHERE workspace_id = $1
	  AND source_provider = $2 AND source_resource_type = $3 AND source_provider_id = $4
	  AND target_provider = $5 AND target_resource_type = $6 AND target_provider_id = $7
	  AND kind = $8
	ORDER BY evidence_id;
	`
	rows, err := s.db.Query(ctx, query, ws,
		rel.Source.Provider, rel.Source.ResourceType, rel.Source.ProviderID,
		rel.Target.Provider, rel.Target.ResourceType, rel.Target.ProviderID,
		rel.Kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetRelationshipsForEvidence(ctx context.Context, workspaceID string, evidenceID string) ([]RelationshipKey, error) {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return nil, ErrInvalidWorkspace
	}

	query := `
	SELECT source_provider, source_resource_type, source_provider_id,
	       target_provider, target_resource_type, target_provider_id, kind
	FROM state_provenance
	WHERE workspace_id = $1 AND evidence_id = $2
	ORDER BY source_provider, source_resource_type, source_provider_id, target_provider, target_resource_type, target_provider_id, kind;
	`
	rows, err := s.db.Query(ctx, query, ws, evidenceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RelationshipKey
	for rows.Next() {
		var k RelationshipKey
		err := rows.Scan(
			&k.Source.Provider, &k.Source.ResourceType, &k.Source.ProviderID,
			&k.Target.Provider, &k.Target.ResourceType, &k.Target.ProviderID,
			&k.Kind,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *PostgresStore) LoadAssembledState(ctx context.Context, workspaceID string) (*AssembledState, error) {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return nil, ErrInvalidWorkspace
	}

	resources, err := s.ListResources(ctx, ws)
	if err != nil {
		return nil, err
	}

	relationships, err := s.ListRelationships(ctx, ws)
	if err != nil {
		return nil, err
	}

	evidence, err := s.ListEvidence(ctx, ws)
	if err != nil {
		return nil, err
	}

	// Query all provenance associations for the workspace
	provQuery := `
	SELECT source_provider, source_resource_type, source_provider_id,
	       target_provider, target_resource_type, target_provider_id, kind, evidence_id
	FROM state_provenance
	WHERE workspace_id = $1
	ORDER BY evidence_id;
	`
	rows, err := s.db.Query(ctx, provQuery, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	prov := make(map[RelationshipKey][]string)
	for _, rel := range relationships {
		prov[rel.Key()] = []string{}
	}

	for rows.Next() {
		var k RelationshipKey
		var evID string
		err := rows.Scan(
			&k.Source.Provider, &k.Source.ResourceType, &k.Source.ProviderID,
			&k.Target.Provider, &k.Target.ResourceType, &k.Target.ProviderID,
			&k.Kind, &evID,
		)
		if err != nil {
			return nil, err
		}
		prov[k] = append(prov[k], evID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for k := range prov {
		sort.Strings(prov[k])
	}

	return &AssembledState{
		WorkspaceID:   ws,
		Resources:     resources,
		Relationships: relationships,
		Evidence:      evidence,
		Provenance:    prov,
	}, nil
}

func (s *PostgresStore) DeleteWorkspaceState(ctx context.Context, workspaceID string) error {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return ErrInvalidWorkspace
	}

	return s.db.WithTransaction(ctx, func(ctx context.Context, tx database.Tx) error {
		tables := []string{"state_conflicts", "state_provenance", "state_relationships", "state_evidence", "state_resources"}
		for _, t := range tables {
			if _, err := tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE workspace_id = $1", t), ws); err != nil {
				return err
			}
		}
		return nil
	})
}
