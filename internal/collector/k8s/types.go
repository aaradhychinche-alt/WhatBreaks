package k8s

import (
	"encoding/json"
)

// ---------------------------------------------------------------------------
// Standard Kubernetes Object Metadata
// ---------------------------------------------------------------------------

// ObjectMeta encapsulates standard Kubernetes metadata fields.
type ObjectMeta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace,omitempty"`
	UID               string            `json:"uid,omitempty"`
	ResourceVersion   string            `json:"resourceVersion,omitempty"`
	Generation        int64             `json:"generation,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	OwnerReferences   []OwnerReference  `json:"ownerReferences,omitempty"`
}

// OwnerReference contains information to locate the owning object.
type OwnerReference struct {
	APIVersion         string `json:"apiVersion"`
	Kind               string `json:"kind"`
	Name               string `json:"name"`
	UID                string `json:"uid"`
	Controller         *bool  `json:"controller,omitempty"`
	BlockOwnerDeletion *bool  `json:"blockOwnerDeletion,omitempty"`
}

// LabelSelector represents a Kubernetes label selector.
type LabelSelector struct {
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// ---------------------------------------------------------------------------
// Core Resource Definitions
// ---------------------------------------------------------------------------

// Namespace represents a Kubernetes namespace.
type Namespace struct {
	ObjectMeta ObjectMeta      `json:"metadata"`
	Status     NamespaceStatus `json:"status,omitempty"`
}

type NamespaceStatus struct {
	Phase string `json:"phase,omitempty"`
}

// Node represents a Kubernetes cluster worker or control plane node.
type Node struct {
	ObjectMeta ObjectMeta `json:"metadata"`
	Status     NodeStatus `json:"status,omitempty"`
}

type NodeStatus struct {
	Addresses []NodeAddress `json:"addresses,omitempty"`
}

type NodeAddress struct {
	Type    string `json:"type"` // InternalIP, ExternalIP, Hostname
	Address string `json:"address"`
}

// Pod represents a Kubernetes pod instance.
type Pod struct {
	ObjectMeta ObjectMeta `json:"metadata"`
	Spec       PodSpec    `json:"spec,omitempty"`
	Status     PodStatus  `json:"status,omitempty"`
}

type PodSpec struct {
	NodeName           string      `json:"nodeName,omitempty"`
	ServiceAccountName string      `json:"serviceAccountName,omitempty"`
	Containers         []Container `json:"containers,omitempty"`
	Volumes            []Volume    `json:"volumes,omitempty"`
}

type PodStatus struct {
	Phase  string `json:"phase,omitempty"` // Pending, Running, Succeeded, Failed, Unknown
	PodIP  string `json:"podIP,omitempty"`
	HostIP string `json:"hostIP,omitempty"`
}

type Container struct {
	Name         string          `json:"name"`
	Image        string          `json:"image"`
	Ports        []ContainerPort `json:"ports,omitempty"`
	Env          []EnvVar        `json:"env,omitempty"`
	EnvFrom      []EnvFromSource `json:"envFrom,omitempty"`
	VolumeMounts []VolumeMount   `json:"volumeMounts,omitempty"`
}

type ContainerPort struct {
	Name          string `json:"name,omitempty"`
	ContainerPort int    `json:"containerPort"`
	HostPort      int    `json:"hostPort,omitempty"`
	Protocol      string `json:"protocol,omitempty"` // TCP, UDP
}

type EnvVar struct {
	Name      string        `json:"name"`
	Value     string        `json:"value,omitempty"`
	ValueFrom *EnvVarSource `json:"valueFrom,omitempty"`
}

type EnvVarSource struct {
	ConfigMapKeyRef *ConfigMapKeySelector `json:"configMapKeyRef,omitempty"`
	SecretKeyRef    *SecretKeySelector    `json:"secretKeyRef,omitempty"`
}

type ConfigMapKeySelector struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// SecretKeySelector contains metadata references to a secret key without secret data.
type SecretKeySelector struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type EnvFromSource struct {
	ConfigMapRef *ConfigMapEnvSource `json:"configMapRef,omitempty"`
	SecretRef    *SecretEnvSource    `json:"secretRef,omitempty"`
}

type ConfigMapEnvSource struct {
	Name string `json:"name"`
}

// SecretEnvSource contains a reference to a secret by name only.
type SecretEnvSource struct {
	Name string `json:"name"`
}

type Volume struct {
	Name                  string                 `json:"name"`
	ConfigMap             *ConfigMapVolumeSource `json:"configMap,omitempty"`
	Secret                *SecretVolumeSource    `json:"secret,omitempty"`
	PersistentVolumeClaim *PVCVolumeSource       `json:"persistentVolumeClaim,omitempty"`
}

type ConfigMapVolumeSource struct {
	Name string `json:"name"`
}

// SecretVolumeSource contains a reference to a secret volume by name only.
type SecretVolumeSource struct {
	SecretName string `json:"secretName"`
}

type PVCVolumeSource struct {
	ClaimName string `json:"claimName"`
}

type VolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}

// Deployment represents a Kubernetes Deployment workload.
type Deployment struct {
	ObjectMeta ObjectMeta     `json:"metadata"`
	Spec       DeploymentSpec `json:"spec,omitempty"`
}

type DeploymentSpec struct {
	Replicas *int32          `json:"replicas,omitempty"`
	Selector LabelSelector   `json:"selector"`
	Template PodTemplateSpec `json:"template"`
}

type PodTemplateSpec struct {
	ObjectMeta ObjectMeta `json:"metadata"`
	Spec       PodSpec    `json:"spec"`
}

// ReplicaSet represents a Kubernetes ReplicaSet workload.
type ReplicaSet struct {
	ObjectMeta ObjectMeta       `json:"metadata"`
	Spec       ReplicaSetSpec   `json:"spec,omitempty"`
	Status     ReplicaSetStatus `json:"status,omitempty"`
}

type ReplicaSetSpec struct {
	Replicas *int32          `json:"replicas,omitempty"`
	Selector LabelSelector   `json:"selector"`
	Template PodTemplateSpec `json:"template,omitempty"`
}

type ReplicaSetStatus struct {
	Replicas int32 `json:"replicas,omitempty"`
}

// Service represents a Kubernetes networking Service.
type Service struct {
	ObjectMeta ObjectMeta  `json:"metadata"`
	Spec       ServiceSpec `json:"spec,omitempty"`
}

type ServiceSpec struct {
	Type       string            `json:"type,omitempty"` // ClusterIP, NodePort, LoadBalancer, ExternalName
	ClusterIP  string            `json:"clusterIP,omitempty"`
	ClusterIPs []string          `json:"clusterIPs,omitempty"`
	Ports      []ServicePort     `json:"ports,omitempty"`
	Selector   map[string]string `json:"selector,omitempty"`
}

type ServicePort struct {
	Name       string      `json:"name,omitempty"`
	Protocol   string      `json:"protocol,omitempty"`
	Port       int         `json:"port"`
	TargetPort IntOrString `json:"targetPort,omitempty"`
	NodePort   int         `json:"nodePort,omitempty"`
}

// IntOrString handles targetPort which can be either integer or string name.
type IntOrString struct {
	Type   int    // 0 = int, 1 = string
	IntVal int    `json:"intVal,omitempty"`
	StrVal string `json:"strVal,omitempty"`
}

func (ios *IntOrString) UnmarshalJSON(data []byte) error {
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		ios.Type = 0
		ios.IntVal = n
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		ios.Type = 1
		ios.StrVal = s
		return nil
	}
	return nil
}

func (ios IntOrString) MarshalJSON() ([]byte, error) {
	if ios.Type == 0 {
		return json.Marshal(ios.IntVal)
	}
	return json.Marshal(ios.StrVal)
}

// Ingress represents a Kubernetes networking Ingress.
type Ingress struct {
	ObjectMeta ObjectMeta  `json:"metadata"`
	Spec       IngressSpec `json:"spec,omitempty"`
}

type IngressSpec struct {
	IngressClassName *string       `json:"ingressClassName,omitempty"`
	Rules            []IngressRule `json:"rules,omitempty"`
	TLS              []IngressTLS  `json:"tls,omitempty"`
}

type IngressRule struct {
	Host string                `json:"host,omitempty"`
	HTTP *HTTPIngressRuleValue `json:"http,omitempty"`
}

type HTTPIngressRuleValue struct {
	Paths []HTTPIngressPath `json:"paths"`
}

type HTTPIngressPath struct {
	Path     string         `json:"path,omitempty"`
	PathType string         `json:"pathType,omitempty"`
	Backend  IngressBackend `json:"backend"`
}

type IngressBackend struct {
	Service *IngressServiceBackend `json:"service,omitempty"`
}

type IngressServiceBackend struct {
	Name string                    `json:"name"`
	Port IngressServiceBackendPort `json:"port"`
}

type IngressServiceBackendPort struct {
	Name   string `json:"name,omitempty"`
	Number int    `json:"number,omitempty"`
}

type IngressTLS struct {
	Hosts      []string `json:"hosts,omitempty"`
	SecretName string   `json:"secretName,omitempty"`
}

// ConfigMap represents a Kubernetes ConfigMap.
type ConfigMap struct {
	ObjectMeta ObjectMeta        `json:"metadata"`
	Data       map[string]string `json:"data,omitempty"`
}

// SecretMetadata represents Kubernetes Secret metadata strictly without secret payloads.
// HARD SECURITY BOUNDARY: No Data or StringData fields exist in this struct.
type SecretMetadata struct {
	ObjectMeta ObjectMeta `json:"metadata"`
	Type       string     `json:"type,omitempty"`
	KeyNames   []string   `json:"keyNames,omitempty"`
}

// PersistentVolumeClaim represents a storage claim.
type PersistentVolumeClaim struct {
	ObjectMeta ObjectMeta                  `json:"metadata"`
	Spec       PersistentVolumeClaimSpec   `json:"spec,omitempty"`
	Status     PersistentVolumeClaimStatus `json:"status,omitempty"`
}

type PersistentVolumeClaimSpec struct {
	VolumeName       string   `json:"volumeName,omitempty"`
	StorageClassName *string  `json:"storageClassName,omitempty"`
	AccessModes      []string `json:"accessModes,omitempty"`
}

type PersistentVolumeClaimStatus struct {
	Phase string `json:"phase,omitempty"` // Pending, Bound, Lost
}

// PersistentVolume represents a cluster storage volume.
type PersistentVolume struct {
	ObjectMeta ObjectMeta             `json:"metadata"`
	Spec       PersistentVolumeSpec   `json:"spec,omitempty"`
	Status     PersistentVolumeStatus `json:"status,omitempty"`
}

type PersistentVolumeSpec struct {
	StorageClassName string            `json:"storageClassName,omitempty"`
	Capacity         map[string]string `json:"capacity,omitempty"`
	AccessModes      []string          `json:"accessModes,omitempty"`
	ClaimRef         *ObjectReference  `json:"claimRef,omitempty"`
}

type ObjectReference struct {
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	UID       string `json:"uid,omitempty"`
}

type PersistentVolumeStatus struct {
	Phase string `json:"phase,omitempty"` // Available, Bound, Released, Failed
}

// ---------------------------------------------------------------------------
// Watch Event Stream
// ---------------------------------------------------------------------------

// WatchEventType identifies the mutation type of a watch event.
type WatchEventType string

const (
	WatchAdded    WatchEventType = "ADDED"
	WatchModified WatchEventType = "MODIFIED"
	WatchDeleted  WatchEventType = "DELETED"
	WatchError    WatchEventType = "ERROR"
)

// WatchEvent encapsulates a streaming Kubernetes watch event.
type WatchEvent struct {
	Type   WatchEventType  `json:"type"`
	Object json.RawMessage `json:"object"`
}

// Status represents a Kubernetes API error/status response.
type Status struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
	Code    int    `json:"code"`
}

// ---------------------------------------------------------------------------
// Generic Resource List Envelopes
// ---------------------------------------------------------------------------

type ListMeta struct {
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Continue        string `json:"continue,omitempty"`
}

type ResourceList[T any] struct {
	Metadata ListMeta `json:"metadata"`
	Items    []T      `json:"items"`
}
