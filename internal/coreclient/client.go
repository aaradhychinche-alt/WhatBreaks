// Package coreclient provides a lightweight gRPC transport client for the
// WhatBreaks Rust Core Engine Discovery Service.
//
// It acts solely as a transport adapter and does not implement or duplicate
// any domain rules or discovery logic, preserving Rust as the authoritative engine.
package coreclient

import (
	"context"
	"fmt"
	"strings"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// DefaultAddress is the default host:port address of the WhatBreaks Core Engine gRPC server.
const DefaultAddress = "127.0.0.1:50051"

// Client wraps the generated DiscoveryServiceClient and AnswerServiceClient and their underlying gRPC connection.
type Client struct {
	conn         *grpc.ClientConn
	client       corev1.DiscoveryServiceClient
	answerClient corev1.AnswerServiceClient
}

// Connect establishes a gRPC connection to the WhatBreaks Core Engine server.
// If target is empty, DefaultAddress ("127.0.0.1:50051") is used.
// By default, insecure credentials are used for local communication; additional DialOptions
// can be supplied by the caller.
func Connect(ctx context.Context, target string, opts ...grpc.DialOption) (*Client, error) {
	if strings.TrimSpace(target) == "" {
		target = DefaultAddress
	}

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	dialOpts = append(dialOpts, opts...)

	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create grpc client for %s: %w", target, err)
	}

	return &Client{
		conn:         conn,
		client:       corev1.NewDiscoveryServiceClient(conn),
		answerClient: corev1.NewAnswerServiceClient(conn),
	}, nil
}

// NewFromConn constructs a Client using an existing *grpc.ClientConn.
func NewFromConn(conn *grpc.ClientConn) *Client {
	return &Client{
		conn:         conn,
		client:       corev1.NewDiscoveryServiceClient(conn),
		answerClient: corev1.NewAnswerServiceClient(conn),
	}
}

// RunDiscovery sends a RunDiscoveryRequest to the Rust Core Engine DiscoveryService.
// It returns the RunDiscoveryResponse or the gRPC status error without modifying status codes.
func (c *Client) RunDiscovery(ctx context.Context, req *corev1.RunDiscoveryRequest, opts ...grpc.CallOption) (*corev1.RunDiscoveryResponse, error) {
	if c == nil || c.client == nil {
		return nil, status.Error(codes.FailedPrecondition, "client is not connected")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request cannot be nil")
	}

	return c.client.RunDiscovery(ctx, req, opts...)
}

// RunDiscoveryWithEvidence is a convenience helper that wraps evidence in a RunDiscoveryRequest.
func (c *Client) RunDiscoveryWithEvidence(ctx context.Context, evidence []*corev1.Evidence, opts ...grpc.CallOption) (*corev1.RunDiscoveryResponse, error) {
	return c.RunDiscovery(ctx, &corev1.RunDiscoveryRequest{Evidence: evidence}, opts...)
}

// Conn returns the underlying *grpc.ClientConn.
func (c *Client) Conn() *grpc.ClientConn {
	return c.conn
}

// DiscoveryServiceClient returns the raw generated DiscoveryServiceClient interface.
func (c *Client) DiscoveryServiceClient() corev1.DiscoveryServiceClient {
	return c.client
}

// AnalyzeImpact sends an AnalyzeImpactRequest to the Rust Core Engine AnswerService.
// It returns the AnalyzeImpactResponse or the gRPC status error without modifying status codes.
func (c *Client) AnalyzeImpact(ctx context.Context, req *corev1.AnalyzeImpactRequest, opts ...grpc.CallOption) (*corev1.AnalyzeImpactResponse, error) {
	if c == nil || c.answerClient == nil {
		return nil, status.Error(codes.FailedPrecondition, "client is not connected")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request cannot be nil")
	}

	return c.answerClient.AnalyzeImpact(ctx, req, opts...)
}

// LoadState sends a LoadStateRequest to the Rust Core Engine AnswerService to populate the in-memory graph.
// It returns the LoadStateResponse or the gRPC status error without modifying status codes.
func (c *Client) LoadState(ctx context.Context, req *corev1.LoadStateRequest, opts ...grpc.CallOption) (*corev1.LoadStateResponse, error) {
	if c == nil || c.answerClient == nil {
		return nil, status.Error(codes.FailedPrecondition, "client is not connected")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request cannot be nil")
	}

	return c.answerClient.LoadState(ctx, req, opts...)
}

// AnswerServiceClient returns the raw generated AnswerServiceClient interface.
func (c *Client) AnswerServiceClient() corev1.AnswerServiceClient {
	return c.answerClient
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
