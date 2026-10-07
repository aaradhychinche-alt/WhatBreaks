package state

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// MemoryStore provides a thread-safe in-memory implementation of Store.
// Useful for fast unit tests, development, and disconnected runtime scenarios.
type MemoryStore struct {
	mu            sync.RWMutex
	resources     map[string]map[string]Resource
	evidence      map[string]map[string]Evidence
	relationships map[string]map[string]Relationship
	relToEvidence map[string]map[string][]string
	evToRel       map[string]map[string][]RelationshipKey
	conflicts     map[string][]DiscoveryConflict
}

// NewMemoryStore initializes an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		resources:     make(map[string]map[string]Resource),
		evidence:      make(map[string]map[string]Evidence),
		relationships: make(map[string]map[string]Relationship),
		relToEvidence: make(map[string]map[string][]string),
		evToRel:       make(map[string]map[string][]RelationshipKey),
		conflicts:     make(map[string][]DiscoveryConflict),
	}
}

func (s *MemoryStore) SaveResources(ctx context.Context, workspaceID string, resources []Resource) error {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return ErrInvalidWorkspace
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	wsMap, ok := s.resources[ws]
	if !ok {
		wsMap = make(map[string]Resource)
		s.resources[ws] = wsMap
	}

	for _, r := range resources {
		r.WorkspaceID = ws
		key := r.Identity.String()
		existing, found := wsMap[key]
		if !found {
			wsMap[key] = r
		} else {
			// Update last_observed_at without modifying first_observed_at
			if r.LastObservedAt.After(existing.LastObservedAt) {
				existing.LastObservedAt = r.LastObservedAt
			}
			wsMap[key] = existing
		}
	}
	return nil
}

func (s *MemoryStore) SaveEvidence(ctx context.Context, workspaceID string, evidence []Evidence) error {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return ErrInvalidWorkspace
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	wsMap, ok := s.evidence[ws]
	if !ok {
		wsMap = make(map[string]Evidence)
		s.evidence[ws] = wsMap
	}

	for _, ev := range evidence {
		ev.WorkspaceID = ws
		// Immutable: do not overwrite existing historical observation
		if _, exists := wsMap[ev.ID]; !exists {
			wsMap[ev.ID] = ev
		}
	}
	return nil
}

func (s *MemoryStore) SaveRelationships(ctx context.Context, workspaceID string, relationships []Relationship) error {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return ErrInvalidWorkspace
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	wsMap, ok := s.relationships[ws]
	if !ok {
		wsMap = make(map[string]Relationship)
		s.relationships[ws] = wsMap
	}

	for _, rel := range relationships {
		rel.WorkspaceID = ws
		key := rel.Key().String()
		existing, found := wsMap[key]
		if !found {
			wsMap[key] = rel
		} else {
			if rel.LastObservedAt.After(existing.LastObservedAt) {
				existing.LastObservedAt = rel.LastObservedAt
			}
			wsMap[key] = existing
		}
	}
	return nil
}

func (s *MemoryStore) SaveProvenance(ctx context.Context, workspaceID string, associations []ProvenanceAssociation) error {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return ErrInvalidWorkspace
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	relMap, ok := s.relToEvidence[ws]
	if !ok {
		relMap = make(map[string][]string)
		s.relToEvidence[ws] = relMap
	}

	evMap, ok := s.evToRel[ws]
	if !ok {
		evMap = make(map[string][]RelationshipKey)
		s.evToRel[ws] = evMap
	}

	for _, assoc := range associations {
		rKey := assoc.Relationship.String()
		evID := assoc.EvidenceID

		// Forward: rel -> evidence (idempotent)
		existingEvs := relMap[rKey]
		hasEv := false
		for _, e := range existingEvs {
			if e == evID {
				hasEv = true
				break
			}
		}
		if !hasEv {
			relMap[rKey] = append(existingEvs, evID)
		}

		// Reverse: evidence -> rel (idempotent)
		existingRels := evMap[evID]
		hasRel := false
		for _, r := range existingRels {
			if r.String() == rKey {
				hasRel = true
				break
			}
		}
		if !hasRel {
			evMap[evID] = append(existingRels, assoc.Relationship)
		}
	}
	return nil
}

func (s *MemoryStore) SaveConflict(ctx context.Context, conflict DiscoveryConflict) error {
	ws := strings.TrimSpace(conflict.WorkspaceID)
	if ws == "" {
		return ErrInvalidWorkspace
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	conflict.WorkspaceID = ws
	s.conflicts[ws] = append(s.conflicts[ws], conflict)
	return nil
}

func (s *MemoryStore) GetResource(ctx context.Context, workspaceID string, identity ResourceIdentity) (*Resource, error) {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return nil, ErrInvalidWorkspace
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	wsMap, ok := s.resources[ws]
	if !ok {
		return nil, ErrResourceNotFound
	}

	res, found := wsMap[identity.String()]
	if !found {
		return nil, ErrResourceNotFound
	}
	return &res, nil
}

func (s *MemoryStore) ListResources(ctx context.Context, workspaceID string) ([]Resource, error) {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return nil, ErrInvalidWorkspace
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	wsMap := s.resources[ws]
	out := make([]Resource, 0, len(wsMap))
	for _, r := range wsMap {
		out = append(out, r)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Identity.String() < out[j].Identity.String()
	})
	return out, nil
}

func (s *MemoryStore) ListRelationships(ctx context.Context, workspaceID string) ([]Relationship, error) {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return nil, ErrInvalidWorkspace
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	wsMap := s.relationships[ws]
	out := make([]Relationship, 0, len(wsMap))
	for _, r := range wsMap {
		out = append(out, r)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Key().String() < out[j].Key().String()
	})
	return out, nil
}

func (s *MemoryStore) ListEvidence(ctx context.Context, workspaceID string) ([]Evidence, error) {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return nil, ErrInvalidWorkspace
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	wsMap := s.evidence[ws]
	out := make([]Evidence, 0, len(wsMap))
	for _, e := range wsMap {
		out = append(out, e)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].ObservedAt.Equal(out[j].ObservedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].ObservedAt.Before(out[j].ObservedAt)
	})
	return out, nil
}

func (s *MemoryStore) GetProvenanceForRelationship(ctx context.Context, workspaceID string, rel RelationshipKey) ([]string, error) {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return nil, ErrInvalidWorkspace
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	wsMap := s.relToEvidence[ws]
	if wsMap == nil {
		return []string{}, nil
	}

	evs := wsMap[rel.String()]
	out := make([]string, len(evs))
	copy(out, evs)
	sort.Strings(out)
	return out, nil
}

func (s *MemoryStore) GetRelationshipsForEvidence(ctx context.Context, workspaceID string, evidenceID string) ([]RelationshipKey, error) {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return nil, ErrInvalidWorkspace
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	wsMap := s.evToRel[ws]
	if wsMap == nil {
		return []RelationshipKey{}, nil
	}

	rels := wsMap[evidenceID]
	out := make([]RelationshipKey, len(rels))
	copy(out, rels)
	sort.Slice(out, func(i, j int) bool {
		return out[i].String() < out[j].String()
	})
	return out, nil
}

func (s *MemoryStore) LoadAssembledState(ctx context.Context, workspaceID string) (*AssembledState, error) {
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

	s.mu.RLock()
	defer s.mu.RUnlock()

	prov := make(map[RelationshipKey][]string)
	wsProv := s.relToEvidence[ws]
	for _, rel := range relationships {
		k := rel.Key()
		evs := wsProv[k.String()]
		sortedEvs := make([]string, len(evs))
		copy(sortedEvs, evs)
		sort.Strings(sortedEvs)
		prov[k] = sortedEvs
	}

	return &AssembledState{
		WorkspaceID:   ws,
		Resources:     resources,
		Relationships: relationships,
		Evidence:      evidence,
		Provenance:    prov,
	}, nil
}

func (s *MemoryStore) DeleteWorkspaceState(ctx context.Context, workspaceID string) error {
	ws := strings.TrimSpace(workspaceID)
	if ws == "" {
		return ErrInvalidWorkspace
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.resources, ws)
	delete(s.evidence, ws)
	delete(s.relationships, ws)
	delete(s.relToEvidence, ws)
	delete(s.evToRel, ws)
	delete(s.conflicts, ws)

	return nil
}
