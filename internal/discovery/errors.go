package discovery

import "errors"

var (
	// ErrDiscoveryInProgress indicates a discovery sweep is already running for the target cluster.
	ErrDiscoveryInProgress = errors.New("discovery sweep is currently in progress for this cluster")

	// ErrCollectorNotFound indicates no collector is registered for the requested cluster ID.
	ErrCollectorNotFound = errors.New("no collector configured for the requested cluster")

	// ErrCollectorUnavailable indicates no collector is configured or enabled.
	ErrCollectorUnavailable = errors.New("discovery collector is not configured or enabled")

	// ErrReconcilerUnavailable indicates no state reconciler is configured.
	ErrReconcilerUnavailable = errors.New("state reconciler is not configured")

	// ErrWorkspaceRequired indicates workspace ID was not specified in the request.
	ErrWorkspaceRequired = errors.New("workspace ID is required")

	// ErrClusterRequired indicates cluster ID could not be determined automatically.
	ErrClusterRequired = errors.New("cluster ID is required")

	// ErrWorkspaceMismatch indicates the target workspace does not match the collector's workspace binding.
	ErrWorkspaceMismatch = errors.New("discovery collector is not configured for this workspace")

	// ErrClusterUnreachable indicates the cluster failed the connectivity reachability check.
	ErrClusterUnreachable = errors.New("cluster reachability check failed")

	// ErrCollectionFailed indicates observation collection failed.
	ErrCollectionFailed = errors.New("failed to collect observations from cluster")

	// ErrReconciliationFailed indicates reconciling observations with state failed.
	ErrReconciliationFailed = errors.New("failed to reconcile observations")

	// ErrMaterializationFailed indicates state materialization into Rust Core Engine failed.
	ErrMaterializationFailed = errors.New("failed to materialize state into core engine")
)
