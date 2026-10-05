package k8s

import (
	"time"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/coreclient"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

// Option configures the Kubernetes Collector.
type Option func(*Collector)

// WithClusterID sets the cluster identifier for multi-tenancy and resource identity scoping.
func WithClusterID(clusterID string) Option {
	return func(c *Collector) {
		if clusterID != "" {
			c.clusterID = clusterID
		}
	}
}

// WithWorkspaceID sets the workspace UUID for tenant isolation.
func WithWorkspaceID(workspaceID string) Option {
	return func(c *Collector) {
		if workspaceID != "" {
			c.workspaceID = workspaceID
		}
	}
}

// WithCollectorID overrides the default provenance collector ID.
func WithCollectorID(collectorID string) Option {
	return func(c *Collector) {
		if collectorID != "" {
			c.collectorID = collectorID
		}
	}
}

// WithNamespaces specifies the target namespaces to observe.
func WithNamespaces(namespaces ...string) Option {
	return func(c *Collector) {
		c.namespaces = namespaces
	}
}

// WithClusterWide enables or disables cluster-wide resource collection.
func WithClusterWide(enabled bool) Option {
	return func(c *Collector) {
		c.clusterWide = enabled
	}
}

// WithClient injects the read-only Kubernetes Client.
func WithClient(client Client) Option {
	return func(c *Collector) {
		if client != nil {
			c.client = client
		}
	}
}

// WithCoreClient binds the gRPC Core Engine client for direct discovery submission.
func WithCoreClient(coreClient *coreclient.Client) Option {
	return func(c *Collector) {
		c.coreClient = coreClient
	}
}

// WithLogger sets the structured logger for the collector.
func WithLogger(logger logging.Logger) Option {
	return func(c *Collector) {
		if logger != nil {
			c.logger = logger
		}
	}
}

// WithReconcileInterval configures the periodic reconciliation interval.
func WithReconcileInterval(interval time.Duration) Option {
	return func(c *Collector) {
		if interval > 0 {
			c.reconcileInterval = interval
		}
	}
}
