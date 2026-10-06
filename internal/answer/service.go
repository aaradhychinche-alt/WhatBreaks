package answer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CoreClient defines the gRPC transport contract required by the AnswerService.
type CoreClient interface {
	AnalyzeImpact(ctx context.Context, req *corev1.AnalyzeImpactRequest, opts ...grpc.CallOption) (*corev1.AnalyzeImpactResponse, error)
}

// Service defines the application-level impact analysis operations.
type Service interface {
	AnalyzeImpact(ctx context.Context, req ImpactRequest) (*ImpactAnswer, error)
}

type service struct {
	client CoreClient
	logger logging.Logger
}

// NewService constructs an AnswerService backed by the given CoreClient and Logger.
func NewService(client CoreClient, logger logging.Logger) Service {
	if logger == nil {
		logger = logging.NewJSONLogger(nil, logging.LevelInfo, "answer-service")
	}
	return &service{
		client: client,
		logger: logger,
	}
}

// AnalyzeImpact orchestrates the validation, protobuf translation, gRPC invocation,
// error translation, and response transformation for impact queries.
func (s *service) AnalyzeImpact(ctx context.Context, req ImpactRequest) (*ImpactAnswer, error) {
	if err := ValidateImpactRequest(&req); err != nil {
		return nil, err
	}

	start := time.Now()
	protoReq := &corev1.AnalyzeImpactRequest{
		Target: &corev1.ResourceIdentity{
			Provider:     req.Target.Provider,
			ResourceType: req.Target.ResourceType,
			ProviderId:   req.Target.ProviderID,
		},
		Direction: strings.ToLower(strings.TrimSpace(req.Direction)),
		MaxDepth:  req.MaxDepth,
	}

	protoResp, err := s.client.AnalyzeImpact(ctx, protoReq)
	duration := time.Since(start)

	if err != nil {
		mappedErr := mapGRPCError(ctx, err)
		s.logger.Warn("Core engine impact query failed",
			"target_provider", req.Target.Provider,
			"target_type", req.Target.ResourceType,
			"target_id", req.Target.ProviderID,
			"direction", req.Direction,
			"max_depth", req.MaxDepth,
			"duration_ms", duration.Milliseconds(),
			"error", mappedErr.Error(),
		)
		return nil, mappedErr
	}

	if protoResp == nil || protoResp.Target == nil || protoResp.Summary == nil {
		s.logger.Error("Core engine returned incomplete response",
			"target_provider", req.Target.Provider,
			"target_type", req.Target.ResourceType,
			"target_id", req.Target.ProviderID,
		)
		return nil, ErrMalformedCoreResponse
	}

	answer := transformProtoResponse(protoResp)

	s.logger.Info("Analyzed impact successfully",
		"target_provider", answer.Target.Provider,
		"target_type", answer.Target.ResourceType,
		"target_id", answer.Target.ProviderID,
		"direction", req.Direction,
		"max_depth", req.MaxDepth,
		"duration_ms", duration.Milliseconds(),
		"impacted_count", answer.Summary.ImpactedCount,
		"direct_count", answer.Summary.DirectCount,
		"indirect_count", answer.Summary.IndirectCount,
	)

	return answer, nil
}

func mapGRPCError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return ErrRequestCanceled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrCoreTimeout
	}

	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("transport failure: %w", err)
	}

	switch st.Code() {
	case codes.Canceled:
		return ErrRequestCanceled
	case codes.DeadlineExceeded:
		return ErrCoreTimeout
	case codes.Unavailable, codes.FailedPrecondition:
		return ErrCoreUnavailable
	case codes.InvalidArgument:
		return &ValidationError{
			Field:   "core",
			Message: st.Message(),
		}
	default:
		return fmt.Errorf("core error (%s): %s", st.Code(), st.Message())
	}
}

func transformProtoResponse(resp *corev1.AnalyzeImpactResponse) *ImpactAnswer {
	target := transformResourceIdentity(resp.Target)

	summary := ImpactSummary{
		ImpactedCount: resp.Summary.ImpactedCount,
		DirectCount:   resp.Summary.DirectCount,
		IndirectCount: resp.Summary.IndirectCount,
		MaxDepth:      resp.Summary.MaxDepth,
	}

	impactedResources := make([]ImpactedResource, 0, len(resp.ImpactedResources))
	for _, ir := range resp.ImpactedResources {
		if ir != nil && ir.Resource != nil {
			impactedResources = append(impactedResources, ImpactedResource{
				Resource: transformResourceIdentity(ir.Resource),
				Depth:    ir.Depth,
			})
		}
	}

	relationships := make([]AnswerRelationship, 0, len(resp.Relationships))
	for _, r := range resp.Relationships {
		if r != nil && r.Relationship != nil {
			relationships = append(relationships, transformAnswerRelationship(r))
		}
	}

	paths := make([]ImpactPath, 0, len(resp.Paths))
	for _, p := range resp.Paths {
		if p != nil {
			paths = append(paths, transformImpactPath(p))
		}
	}

	evidence := make([]AnswerEvidence, 0, len(resp.Evidence))
	for _, e := range resp.Evidence {
		if e != nil {
			ev := AnswerEvidence{
				ID:              e.Id,
				ObservedAt:      e.ObservedAt,
				ObservationType: e.ObservationType,
			}
			if e.Source != nil {
				ev.Source = EvidenceSource{
					Provider:  e.Source.Provider,
					Collector: e.Source.Collector,
				}
			}
			evidence = append(evidence, ev)
		}
	}

	explanationFacts := make([]ExplanationFact, 0, len(resp.ExplanationFacts))
	for _, fact := range resp.ExplanationFacts {
		if fact != nil {
			ef := ExplanationFact{
				EvidenceIDs: copyStringSlice(fact.EvidenceIds),
			}
			if fact.Path != nil {
				ef.Path = transformImpactPath(fact.Path)
			}
			ef.Relationships = make([]AnswerRelationship, 0, len(fact.Relationships))
			for _, rel := range fact.Relationships {
				if rel != nil && rel.Relationship != nil {
					ef.Relationships = append(ef.Relationships, transformAnswerRelationship(rel))
				}
			}
			explanationFacts = append(explanationFacts, ef)
		}
	}

	return &ImpactAnswer{
		Target:            target,
		Summary:           summary,
		ImpactedResources: impactedResources,
		Relationships:     relationships,
		Paths:             paths,
		Evidence:          evidence,
		ExplanationFacts:  explanationFacts,
	}
}

func transformResourceIdentity(proto *corev1.ResourceIdentity) ResourceIdentity {
	if proto == nil {
		return ResourceIdentity{}
	}
	return ResourceIdentity{
		Provider:     proto.Provider,
		ResourceType: proto.ResourceType,
		ProviderID:   proto.ProviderId,
	}
}

func transformRelationship(proto *corev1.Relationship) Relationship {
	if proto == nil {
		return Relationship{}
	}
	return Relationship{
		Source:   transformResourceIdentity(proto.Source),
		Target:   transformResourceIdentity(proto.Target),
		Kind:     proto.Kind,
		Category: proto.Category,
	}
}

func transformAnswerRelationship(proto *corev1.AnswerRelationship) AnswerRelationship {
	if proto == nil {
		return AnswerRelationship{
			EvidenceIDs: make([]string, 0),
		}
	}
	return AnswerRelationship{
		Relationship: transformRelationship(proto.Relationship),
		State:        strings.ToLower(proto.State),
		EvidenceIDs:  copyStringSlice(proto.EvidenceIds),
	}
}

func transformImpactPath(proto *corev1.ImpactPath) ImpactPath {
	if proto == nil {
		return ImpactPath{
			Resources:     make([]ResourceIdentity, 0),
			Relationships: make([]Relationship, 0),
		}
	}

	res := make([]ResourceIdentity, 0, len(proto.Resources))
	for _, r := range proto.Resources {
		if r != nil {
			res = append(res, transformResourceIdentity(r))
		}
	}

	rels := make([]Relationship, 0, len(proto.Relationships))
	for _, r := range proto.Relationships {
		if r != nil {
			rels = append(rels, transformRelationship(r))
		}
	}

	return ImpactPath{
		Resources:     res,
		Relationships: rels,
	}
}

func copyStringSlice(src []string) []string {
	if src == nil {
		return make([]string, 0)
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}
