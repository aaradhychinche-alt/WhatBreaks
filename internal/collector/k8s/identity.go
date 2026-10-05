package k8s

import (
	"fmt"
	"strings"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
)

const (
	// ProviderKubernetes is the canonical provider string for WhatBreaks Kubernetes resources.
	ProviderKubernetes = "kubernetes"

	// DefaultCollectorID is the standard collector provenance name.
	DefaultCollectorID = "k8s-collector"
)

// Supported Resource Types
const (
	TypeNamespace             = "namespace"
	TypeNode                  = "node"
	TypeDeployment            = "deployment"
	TypeReplicaSet            = "replicaset"
	TypeStatefulSet           = "statefulset"
	TypeDaemonSet             = "daemonset"
	TypePod                   = "pod"
	TypeService               = "service"
	TypeIngress               = "ingress"
	TypeConfigMap             = "configmap"
	TypeSecret                = "secret"
	TypePersistentVolume      = "persistentvolume"
	TypePersistentVolumeClaim = "persistentvolumeclaim"
	TypeServiceAccount        = "serviceaccount"
)

// IsClusterScoped reports whether a Kubernetes resource type is scoped to the cluster rather than a namespace.
func IsClusterScoped(resourceType string) bool {
	switch strings.ToLower(resourceType) {
	case TypeNamespace, TypeNode, TypePersistentVolume:
		return true
	default:
		return false
	}
}

// BuildIdentity constructs a canonical ResourceIdentity adhering to the WhatBreaks model.
// For cluster-scoped resources: provider_id = "<cluster_id>/<name>" (or "<name>" if cluster_id is empty).
// For namespaced resources: provider_id = "<cluster_id>/<namespace>/<name>" (or "<namespace>/<name>" if cluster_id is empty).
func BuildIdentity(clusterID, resourceType, namespace, name string) *corev1.ResourceIdentity {
	resourceType = strings.ToLower(resourceType)
	var providerID string

	if IsClusterScoped(resourceType) || namespace == "" {
		if clusterID != "" {
			providerID = fmt.Sprintf("%s/%s", clusterID, name)
		} else {
			providerID = name
		}
	} else {
		if clusterID != "" {
			providerID = fmt.Sprintf("%s/%s/%s", clusterID, namespace, name)
		} else {
			providerID = fmt.Sprintf("%s/%s", namespace, name)
		}
	}

	return &corev1.ResourceIdentity{
		Provider:     ProviderKubernetes,
		ResourceType: resourceType,
		ProviderId:   providerID,
	}
}

// ParseProviderID extracts cluster, namespace, and resource name components from a provider_id.
func ParseProviderID(providerID string, isClusterScoped bool) (clusterID, namespace, name string, err error) {
	parts := strings.Split(providerID, "/")
	if isClusterScoped {
		switch len(parts) {
		case 1:
			return "", "", parts[0], nil
		case 2:
			return parts[0], "", parts[1], nil
		default:
			return "", "", "", fmt.Errorf("malformed cluster-scoped provider_id: %s", providerID)
		}
	}

	switch len(parts) {
	case 2:
		return "", parts[0], parts[1], nil
	case 3:
		return parts[0], parts[1], parts[2], nil
	default:
		return "", "", "", fmt.Errorf("malformed namespaced provider_id: %s", providerID)
	}
}

// BuildSource constructs an EvidenceSource record for this collector.
func BuildSource(collectorID string) *corev1.EvidenceSource {
	if collectorID == "" {
		collectorID = DefaultCollectorID
	}
	return &corev1.EvidenceSource{
		Provider:  ProviderKubernetes,
		Collector: collectorID,
	}
}
