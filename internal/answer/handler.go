package answer

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/auth"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

// HandlerOption configures an Answer HTTP Handler.
type HandlerOption func(*Handler)

// WithAuthorizer configures a WorkspaceAuthorizer and workspace ID extractor on the handler.
func WithAuthorizer(authorizer *auth.WorkspaceAuthorizer, getWSID func(*http.Request) string) HandlerOption {
	return func(h *Handler) {
		h.authorizer = authorizer
		if getWSID != nil {
			h.getWSID = getWSID
		}
	}
}

// WithLogger sets the structured logger for the handler.
func WithLogger(logger logging.Logger) HandlerOption {
	return func(h *Handler) {
		h.logger = logger
	}
}

// Handler handles HTTP requests for impact investigation (POST /api/v1/impact).
type Handler struct {
	service    Service
	logger     logging.Logger
	authorizer *auth.WorkspaceAuthorizer
	getWSID    func(*http.Request) string
}

// NewHandler constructs an impact analysis HTTP handler.
func NewHandler(service Service, opts ...HandlerOption) *Handler {
	h := &Handler{
		service: service,
		getWSID: defaultGetWorkspaceID,
	}
	for _, opt := range opts {
		opt(h)
	}
	if h.logger == nil {
		h.logger = logging.NewJSONLogger(nil, logging.LevelInfo, "answer-handler")
	}
	return h
}

// ServeHTTP handles the POST /api/v1/impact request pipeline.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrorResponse(w, http.StatusMethodNotAllowed, "Method not allowed", string(CodeBadRequest))
		return
	}

	// 1. Enforce Workspace Authorization if authorizer configured
	if h.authorizer != nil {
		wsID := h.getWSID(r)
		if wsID != "" {
			if err := h.authorizer.AuthorizeWorkspaceAccess(r.Context(), wsID); err != nil {
				switch {
				case errors.Is(err, auth.ErrNotAuthenticated):
					writeErrorResponse(w, http.StatusUnauthorized, "Not authenticated", string(CodeUnauthorized))
				case errors.Is(err, auth.ErrWorkspaceNotFound):
					writeErrorResponse(w, http.StatusNotFound, "Workspace not found", string(CodeNotFound))
				case errors.Is(err, auth.ErrInvalidWorkspaceID):
					writeErrorResponse(w, http.StatusBadRequest, "Invalid workspace ID", string(CodeBadRequest))
				default:
					writeErrorResponse(w, http.StatusForbidden, "Access to workspace forbidden", string(CodeForbidden))
				}
				return
			}
		}
	}

	// 2. Decode incoming JSON payload
	var req ImpactRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, "Invalid or malformed JSON request body", string(CodeBadRequest))
		return
	}

	wsID := h.getWSID(r)
	if wsID != "" && req.WorkspaceID == "" {
		req.WorkspaceID = wsID
	}

	// 3. Validate request schema and boundaries
	if err := ValidateImpactRequest(&req); err != nil {
		writeErrorResponse(w, http.StatusBadRequest, err.Error(), string(CodeValidationError))
		return
	}

	// 4. Delegate to AnswerService application boundary
	resp, err := h.service.AnalyzeImpact(r.Context(), req)
	if err != nil {
		var vErr *ValidationError
		switch {
		case errors.As(err, &vErr):
			writeErrorResponse(w, http.StatusBadRequest, vErr.Error(), string(CodeValidationError))
		case errors.Is(err, ErrCoreUnavailable):
			writeErrorResponse(w, http.StatusServiceUnavailable, "Core engine is currently unavailable", string(CodeCoreUnavailable))
		case errors.Is(err, ErrCoreTimeout):
			writeErrorResponse(w, http.StatusGatewayTimeout, "Core engine request timed out", string(CodeCoreTimeout))
		case errors.Is(err, ErrRequestCanceled):
			writeErrorResponse(w, 499, "Request was cancelled", string(CodeRequestCanceled))
		case errors.Is(err, ErrMalformedCoreResponse):
			writeErrorResponse(w, http.StatusInternalServerError, "Internal translation failure", string(CodeInternalError))
		default:
			writeErrorResponse(w, http.StatusInternalServerError, "Internal server error", string(CodeInternalError))
		}
		return
	}

	// 5. Emit stable JSON response
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func defaultGetWorkspaceID(r *http.Request) string {
	if ws := r.Header.Get("X-Workspace-ID"); ws != "" {
		return ws
	}
	return r.URL.Query().Get("workspace_id")
}

func writeErrorResponse(w http.ResponseWriter, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error: msg,
		Code:  code,
	})
}
