package k8s

import (
	"context"
	"errors"
	"sync"
	"time"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/coreclient"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/lifecycle"
	"github.com/aaradhychinche-alt/WhatBreaks/internal/logging"
)

var (
	ErrCollectorRunning   = errors.New("k8s collector: already running")
	ErrCollectorStopped   = errors.New("k8s collector: stopped")
	ErrMissingClient      = errors.New("k8s collector: client is required")
	ErrCoreClientNotBound = errors.New("k8s collector: core client is not configured")
)

// Collector coordinates read-only Kubernetes observations, normalizes them into WhatBreaks
// Evidence records, and optionally submits them to the Rust Core Engine.
type Collector struct {
	clusterID         string
	workspaceID       string
	collectorID       string
	clusterWide       bool
	namespaces        []string
	client            Client
	coreClient        *coreclient.Client
	logger            logging.Logger
	reconcileInterval time.Duration
	normalizer        *Normalizer
	tracker           *lifecycle.SimpleTracker

	mu            sync.RWMutex
	running       bool
	stopped       bool
	acceptingWork bool
	alive         bool
	ready         bool
	cancelWatch   context.CancelFunc
	stopCh        chan struct{}
}

// New constructs a Kubernetes Collector instance with provided options.
func New(opts ...Option) (*Collector, error) {
	c := &Collector{
		collectorID:       DefaultCollectorID,
		reconcileInterval: 30 * time.Second,
		tracker:           lifecycle.NewSimpleTracker(),
		stopCh:            make(chan struct{}),
	}

	for _, opt := range opts {
		opt(c)
	}

	if c.client == nil {
		return nil, ErrMissingClient
	}
	if c.logger == nil {
		c.logger = logging.NewStandardLogger(nil, logging.LevelInfo)
	}

	c.normalizer = NewNormalizer(c.clusterID, c.workspaceID, c.collectorID)
	return c, nil
}

// ---------------------------------------------------------------------------
// Lifecycle Methods (Compatible with internal/lifecycle)
// ---------------------------------------------------------------------------

// Start begins observation collection and marks the collector ready.
func (c *Collector) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return ErrCollectorRunning
	}
	if c.stopped {
		c.mu.Unlock()
		return ErrCollectorStopped
	}

	c.running = true
	c.alive = true
	c.acceptingWork = true
	c.mu.Unlock()

	c.logger.Info("Starting Kubernetes Collector",
		"cluster_id", c.clusterID,
		"workspace_id", c.workspaceID,
		"cluster_wide", c.clusterWide,
		"namespaces", c.namespaces,
	)

	// Verify Kubernetes API connectivity
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()

	if err := c.client.Ping(pingCtx); err != nil {
		c.logger.Warn("Kubernetes API ping check returned error", "error", err.Error())
		// If ping endpoint (/readyz) is unavailable on older/custom clusters, proceed with initial list
	}

	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()

	c.logger.Info("Kubernetes Collector initialized and ready")
	return nil
}

// Stop gracefully stops the collector, cancelling watches and draining work.
func (c *Collector) Stop(ctx context.Context) error {
	c.mu.Lock()
	if !c.running || c.stopped {
		c.mu.Unlock()
		return nil
	}

	c.ready = false
	c.acceptingWork = false
	c.stopped = true

	if c.cancelWatch != nil {
		c.cancelWatch()
	}
	close(c.stopCh)
	c.mu.Unlock()

	c.logger.Info("Kubernetes Collector stopping, draining active tasks")

	// Wait for any in-flight observation runs to finish
	if err := c.tracker.WaitForIdle(ctx); err != nil {
		c.logger.Warn("Wait for idle completed with error", "error", err.Error())
	}

	c.mu.Lock()
	c.alive = false
	c.running = false
	c.mu.Unlock()

	c.logger.Info("Kubernetes Collector stopped")
	return nil
}

// Close is an alias for Stop with a default 10s timeout, matching the lifecycle port contract.
func (c *Collector) Close(ctx context.Context) error {
	return c.Stop(ctx)
}

// StopAcceptingWork immediately flips readiness to false.
func (c *Collector) StopAcceptingWork(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ready = false
	c.acceptingWork = false
	return nil
}

// IsAlive reports whether the collector process is running.
func (c *Collector) IsAlive() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.alive
}

// IsReady reports whether the collector is healthy and connected to Kubernetes.
func (c *Collector) IsReady() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ready
}

// WaitForIdle waits for in-flight collection tasks to complete.
func (c *Collector) WaitForIdle(ctx context.Context) error {
	return c.tracker.WaitForIdle(ctx)
}

// WorkspaceID returns the tenant workspace UUID configured for this collector.
func (c *Collector) WorkspaceID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.workspaceID
}

// ClusterID returns the cluster identifier configured for this collector.
func (c *Collector) ClusterID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.clusterID
}

// Ping verifies connectivity to the underlying Kubernetes API server.
func (c *Collector) Ping(ctx context.Context) error {
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()
	if client == nil {
		return ErrMissingClient
	}
	return client.Ping(ctx)
}

// ---------------------------------------------------------------------------
// Core Collection Sweep (Read-Only)
// ---------------------------------------------------------------------------

// Collect executes a complete read-only sweep of Kubernetes infrastructure,
// returning normalized, immutable WhatBreaks Evidence records.
func (c *Collector) Collect(ctx context.Context) ([]*corev1.Evidence, error) {
	c.mu.RLock()
	if c.stopped {
		c.mu.RUnlock()
		return nil, ErrCollectorStopped
	}
	c.mu.RUnlock()

	var evidenceList []*corev1.Evidence
	var collectErr error

	err := c.tracker.TrackWork(ctx, "k8s-collect-sweep", func(trackCtx context.Context) error {
		var allEvidence []*corev1.Evidence

		// 1. Cluster-scoped resources (if ClusterWide is enabled)
		if c.clusterWide {
			// Namespaces
			namespaces, err := c.client.ListNamespaces(trackCtx)
			if err != nil {
				c.logger.Warn("Failed to list namespaces", "error", err.Error())
			} else {
				for i := range namespaces {
					allEvidence = append(allEvidence, c.normalizer.NormalizeNamespace(&namespaces[i])...)
				}
			}

			// Nodes
			nodes, err := c.client.ListNodes(trackCtx)
			if err != nil {
				c.logger.Warn("Failed to list nodes", "error", err.Error())
			} else {
				for i := range nodes {
					allEvidence = append(allEvidence, c.normalizer.NormalizeNode(&nodes[i])...)
				}
			}

			// PersistentVolumes
			pvs, err := c.client.ListPVs(trackCtx)
			if err != nil {
				c.logger.Warn("Failed to list persistent volumes", "error", err.Error())
			} else {
				for i := range pvs {
					allEvidence = append(allEvidence, c.normalizer.NormalizePV(&pvs[i])...)
				}
			}
		}

		// 2. Determine target namespaces for namespaced resources
		targetNamespaces := c.namespaces
		if len(targetNamespaces) == 0 {
			// If no specific namespaces configured, default to cluster-wide list (namespace="")
			targetNamespaces = []string{""}
		}

		// 3. Collect namespaced resources
		for _, ns := range targetNamespaces {
			select {
			case <-trackCtx.Done():
				return trackCtx.Err()
			default:
			}

			// Deployments
			deployments, err := c.client.ListDeployments(trackCtx, ns)
			if err != nil {
				c.logger.Warn("Failed to list deployments", "namespace", ns, "error", err.Error())
			} else {
				for i := range deployments {
					allEvidence = append(allEvidence, c.normalizer.NormalizeDeployment(&deployments[i])...)
				}
			}

			// ReplicaSets
			replicaSets, err := c.client.ListReplicaSets(trackCtx, ns)
			if err != nil {
				c.logger.Warn("Failed to list replica sets", "namespace", ns, "error", err.Error())
			} else {
				for i := range replicaSets {
					allEvidence = append(allEvidence, c.normalizer.NormalizeReplicaSet(&replicaSets[i])...)
				}
			}

			// Services
			services, err := c.client.ListServices(trackCtx, ns)
			if err != nil {
				c.logger.Warn("Failed to list services", "namespace", ns, "error", err.Error())
			} else {
				for i := range services {
					allEvidence = append(allEvidence, c.normalizer.NormalizeService(&services[i])...)
				}
			}

			// Ingresses
			ingresses, err := c.client.ListIngresses(trackCtx, ns)
			if err != nil {
				c.logger.Warn("Failed to list ingresses", "namespace", ns, "error", err.Error())
			} else {
				for i := range ingresses {
					allEvidence = append(allEvidence, c.normalizer.NormalizeIngress(&ingresses[i])...)
				}
			}

			// Pods
			pods, err := c.client.ListPods(trackCtx, ns)
			if err != nil {
				c.logger.Warn("Failed to list pods", "namespace", ns, "error", err.Error())
			} else {
				for i := range pods {
					allEvidence = append(allEvidence, c.normalizer.NormalizePod(&pods[i])...)
				}
			}

			// ConfigMaps
			configMaps, err := c.client.ListConfigMaps(trackCtx, ns)
			if err != nil {
				c.logger.Warn("Failed to list config maps", "namespace", ns, "error", err.Error())
			} else {
				for i := range configMaps {
					allEvidence = append(allEvidence, c.normalizer.NormalizeConfigMap(&configMaps[i])...)
				}
			}

			// ServiceAccounts
			serviceAccounts, err := c.client.ListServiceAccounts(trackCtx, ns)
			if err != nil {
				c.logger.Warn("Failed to list service accounts", "namespace", ns, "error", err.Error())
			} else {
				for i := range serviceAccounts {
					allEvidence = append(allEvidence, c.normalizer.NormalizeServiceAccount(&serviceAccounts[i])...)
				}
			}

			// Secrets (METADATA ONLY - SECRET SAFETY GUARANTEE)
			secrets, err := c.client.ListSecretsMetadata(trackCtx, ns)
			if err != nil {
				c.logger.Warn("Failed to list secret metadata", "namespace", ns, "error", err.Error())
			} else {
				for i := range secrets {
					allEvidence = append(allEvidence, c.normalizer.NormalizeSecret(&secrets[i])...)
				}
			}

			// PVCs
			pvcs, err := c.client.ListPVCs(trackCtx, ns)
			if err != nil {
				c.logger.Warn("Failed to list PVCs", "namespace", ns, "error", err.Error())
			} else {
				for i := range pvcs {
					allEvidence = append(allEvidence, c.normalizer.NormalizePVC(&pvcs[i])...)
				}
			}
		}

		evidenceList = allEvidence
		return nil
	})

	if err != nil {
		collectErr = err
	}

	return evidenceList, collectErr
}

// SubmitToCore sends collected Evidence to the Rust Core Engine DiscoveryService.
func (c *Collector) SubmitToCore(
	ctx context.Context,
	evidenceList []*corev1.Evidence,
) (*corev1.RunDiscoveryResponse, error) {
	c.mu.RLock()
	client := c.coreClient
	c.mu.RUnlock()

	if client == nil {
		return nil, ErrCoreClientNotBound
	}

	if len(evidenceList) == 0 {
		return &corev1.RunDiscoveryResponse{}, nil
	}

	c.logger.Info("Submitting evidence batch to Core Engine",
		"evidence_count", len(evidenceList),
		"cluster_id", c.clusterID,
	)

	return client.RunDiscoveryWithEvidence(ctx, evidenceList)
}
