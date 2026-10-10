package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/auth"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/state"
)

// ResourceItem models a discovered infrastructure resource for API responses.
type ResourceItem struct {
	Provider        string    `json:"provider"`
	ResourceType    string    `json:"resource_type"`
	ProviderID      string    `json:"provider_id"`
	FirstObservedAt time.Time `json:"first_observed_at"`
	LastObservedAt  time.Time `json:"last_observed_at"`
}

// ListResourcesResponse represents the payload returned by GET /api/v1/resources.
type ListResourcesResponse struct {
	WorkspaceID string         `json:"workspace_id"`
	Total       int            `json:"total"`
	Resources   []ResourceItem `json:"resources"`
}

// ResourceRelationshipItem represents an active relationship connected to an inspected resource.
type ResourceRelationshipItem struct {
	Source          state.ResourceIdentity `json:"source"`
	Target          state.ResourceIdentity `json:"target"`
	Kind            string                 `json:"kind"`
	Category        string                 `json:"category"`
	Direction       string                 `json:"direction"` // "OUTGOING" or "INCOMING"
	Peer            state.ResourceIdentity `json:"peer"`
	EvidenceIDs     []string               `json:"evidence_ids,omitempty"`
	FirstObservedAt time.Time              `json:"first_observed_at"`
	LastObservedAt  time.Time              `json:"last_observed_at"`
}

// ResourceDetailResponse represents the payload returned by GET /api/v1/resources/detail.
type ResourceDetailResponse struct {
	WorkspaceID   string                     `json:"workspace_id"`
	Resource      ResourceItem               `json:"resource"`
	Relationships []ResourceRelationshipItem `json:"relationships"`
}

// extractAndAuthorizeWorkspace validates and authorizes the target workspace ID.
// If an authorizer is provided, it delegates to AuthorizeWorkspaceAccess.
// If a configuredWS is set (e.g. single-tenant / local development), it ensures the caller
// cannot access arbitrary workspace state.
func extractAndAuthorizeWorkspace(
	r *http.Request,
	authorizer *auth.WorkspaceAuthorizer,
	configuredWS string,
) (string, error) {
	wsID := r.Header.Get("X-Workspace-ID")
	if wsID == "" {
		wsID = r.URL.Query().Get("workspace_id")
	}
	wsID = strings.TrimSpace(wsID)

	if wsID == "" {
		if configuredWS != "" {
			wsID = configuredWS
		} else {
			return "", auth.ErrInvalidWorkspaceID
		}
	}

	if !auth.IsValidUUID(wsID) {
		return "", auth.ErrInvalidWorkspaceID
	}

	if authorizer != nil {
		if err := authorizer.AuthorizeWorkspaceAccess(r.Context(), wsID); err != nil {
			return "", err
		}
	} else if configuredWS != "" {
		if wsID != configuredWS {
			// Prevent arbitrary workspace enumeration / access in configured-cluster mode
			return "", auth.ErrWorkspaceForbidden
		}
	} else {
		// Neither concrete authorizer nor configured single-tenant workspace is present.
		// UUID validation alone does not prove authorization.
		return "", auth.ErrWorkspaceForbidden
	}

	return wsID, nil
}

func handleWorkspaceAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrNotAuthenticated):
		writeErrorResponse(w, http.StatusUnauthorized, "Not authenticated", "UNAUTHORIZED")
	case errors.Is(err, auth.ErrWorkspaceNotFound):
		writeErrorResponse(w, http.StatusNotFound, "Workspace not found", "NOT_FOUND")
	case errors.Is(err, auth.ErrInvalidWorkspaceID):
		writeErrorResponse(w, http.StatusBadRequest, "Invalid or missing workspace identifier", "INVALID_WORKSPACE")
	default:
		writeErrorResponse(w, http.StatusForbidden, "Access to workspace forbidden", "FORBIDDEN")
	}
}

// handleListResources handles GET /api/v1/resources.
func (s *Server) handleListResources() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErrorResponse(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
			return
		}

		if s.cfg.Store == nil {
			writeErrorResponse(w, http.StatusServiceUnavailable, "State persistence store is not configured", "STORE_UNAVAILABLE")
			return
		}

		wsID, err := extractAndAuthorizeWorkspace(r, s.cfg.WorkspaceAuthorizer, s.cfg.ConfiguredWorkspaceID)
		if err != nil {
			handleWorkspaceAuthError(w, err)
			return
		}

		resources, err := s.cfg.Store.ListResources(r.Context(), wsID)
		if err != nil {
			s.logger.Error("Failed to list resources from store", "workspace_id", wsID, "error", err)
			writeErrorResponse(w, http.StatusInternalServerError, "Failed to retrieve resource inventory", "INTERNAL_ERROR")
			return
		}

		typeFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
		if typeFilter == "" {
			typeFilter = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("resource_type")))
		}
		searchFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))

		items := make([]ResourceItem, 0, len(resources))
		for _, res := range resources {
			if typeFilter != "" && strings.ToLower(res.Identity.ResourceType) != typeFilter {
				continue
			}
			if searchFilter != "" && !strings.Contains(strings.ToLower(res.Identity.ProviderID), searchFilter) {
				continue
			}
			items = append(items, ResourceItem{
				Provider:        res.Identity.Provider,
				ResourceType:    res.Identity.ResourceType,
				ProviderID:      res.Identity.ProviderID,
				FirstObservedAt: res.FirstObservedAt,
				LastObservedAt:  res.LastObservedAt,
			})
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ListResourcesResponse{
			WorkspaceID: wsID,
			Total:       len(items),
			Resources:   items,
		})
	}
}

// handleResourceDetail handles GET /api/v1/resources/detail.
func (s *Server) handleResourceDetail() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeErrorResponse(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
			return
		}

		if s.cfg.Store == nil {
			writeErrorResponse(w, http.StatusServiceUnavailable, "State persistence store is not configured", "STORE_UNAVAILABLE")
			return
		}

		wsID, err := extractAndAuthorizeWorkspace(r, s.cfg.WorkspaceAuthorizer, s.cfg.ConfiguredWorkspaceID)
		if err != nil {
			handleWorkspaceAuthError(w, err)
			return
		}

		provider := strings.TrimSpace(r.URL.Query().Get("provider"))
		resourceType := strings.TrimSpace(r.URL.Query().Get("resource_type"))
		providerID := strings.TrimSpace(r.URL.Query().Get("provider_id"))

		if provider == "" || resourceType == "" || providerID == "" {
			writeErrorResponse(w, http.StatusBadRequest, "Query parameters 'provider', 'resource_type', and 'provider_id' are required", "VALIDATION_ERROR")
			return
		}

		identity := state.ResourceIdentity{
			Provider:     provider,
			ResourceType: resourceType,
			ProviderID:   providerID,
		}

		res, err := s.cfg.Store.GetResource(r.Context(), wsID, identity)
		if err != nil {
			if errors.Is(err, state.ErrResourceNotFound) {
				writeErrorResponse(w, http.StatusNotFound, "Resource not found in workspace", "RESOURCE_NOT_FOUND")
				return
			}
			s.logger.Error("Failed to get resource from store", "workspace_id", wsID, "identity", identity, "error", err)
			writeErrorResponse(w, http.StatusInternalServerError, "Failed to retrieve resource details", "INTERNAL_ERROR")
			return
		}

		// Retrieve all relationships for workspace and filter connected edges
		allRels, err := s.cfg.Store.ListRelationships(r.Context(), wsID)
		if err != nil {
			s.logger.Error("Failed to list relationships from store", "workspace_id", wsID, "error", err)
			writeErrorResponse(w, http.StatusInternalServerError, "Failed to retrieve resource relationships", "INTERNAL_ERROR")
			return
		}

		relItems := make([]ResourceRelationshipItem, 0)
		for _, rel := range allRels {
			var direction string
			var peer state.ResourceIdentity

			if rel.Source == identity {
				direction = "OUTGOING"
				peer = rel.Target
			} else if rel.Target == identity {
				direction = "INCOMING"
				peer = rel.Source
			} else {
				continue
			}

			evidenceIDs, err := s.cfg.Store.GetProvenanceForRelationship(r.Context(), wsID, rel.Key())
			if err != nil {
				s.logger.Warn("Failed to resolve provenance for relationship", "rel", rel.Key(), "error", err)
			}

			relItems = append(relItems, ResourceRelationshipItem{
				Source:          rel.Source,
				Target:          rel.Target,
				Kind:            rel.Kind,
				Category:        rel.Category,
				Direction:       direction,
				Peer:            peer,
				EvidenceIDs:     evidenceIDs,
				FirstObservedAt: rel.FirstObservedAt,
				LastObservedAt:  rel.LastObservedAt,
			})
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ResourceDetailResponse{
			WorkspaceID: wsID,
			Resource: ResourceItem{
				Provider:        res.Identity.Provider,
				ResourceType:    res.Identity.ResourceType,
				ProviderID:      res.Identity.ProviderID,
				FirstObservedAt: res.FirstObservedAt,
				LastObservedAt:  res.LastObservedAt,
			},
			Relationships: relItems,
		})
	}
}
