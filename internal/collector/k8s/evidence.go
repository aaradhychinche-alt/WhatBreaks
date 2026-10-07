package k8s

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	corev1 "github.com/aaradhychinche-alt/WhatBreaks/gen/go/wb/core/v1"
)

// Standard Observation Types matching WhatBreaks Core Engine
const (
	ObservationConfiguration      = "CONFIGURATION"
	ObservationRuntimeConnection  = "RUNTIME_CONNECTION"
	ObservationResourceReference  = "RESOURCE_REFERENCE"
	ObservationOwnershipReference = "OWNERSHIP_REFERENCE"
	ObservationNetwork            = "NETWORK_OBSERVATION"
)

// GenerateEvidenceID creates a UUIDv4 string.
func GenerateEvidenceID() string {
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // Version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // Variant RFC4122
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

// DeterministicEvidenceID creates a reproducible UUID-formatted ID based on seed components.
func DeterministicEvidenceID(parts ...string) string {
	h := hex.EncodeToString([]byte(fmt.Sprintf("%v", parts)))
	if len(h) < 32 {
		h = fmt.Sprintf("%032s", h)
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// ---------------------------------------------------------------------------
// Normalizer transforms raw Kubernetes API objects into WhatBreaks Evidence
// ---------------------------------------------------------------------------

type Normalizer struct {
	clusterID   string
	workspaceID string
	collectorID string
	now         func() time.Time
}

// NewNormalizer constructs a Normalizer.
func NewNormalizer(clusterID, workspaceID, collectorID string) *Normalizer {
	if collectorID == "" {
		collectorID = DefaultCollectorID
	}
	return &Normalizer{
		clusterID:   clusterID,
		workspaceID: workspaceID,
		collectorID: collectorID,
		now:         time.Now,
	}
}

func (n *Normalizer) observedAt() string {
	return n.now().UTC().Format(time.RFC3339)
}

func (n *Normalizer) source() *corev1.EvidenceSource {
	return BuildSource(n.collectorID)
}

func (n *Normalizer) buildEvidence(
	obsType string,
	subject *corev1.ResourceIdentity,
	data any,
) *corev1.Evidence {
	dataBytes, _ := json.Marshal(data)
	return &corev1.Evidence{
		Id:              GenerateEvidenceID(),
		Source:          n.source(),
		ObservedAt:      n.observedAt(),
		ObservationType: obsType,
		Subject:         subject,
		Data:            dataBytes,
	}
}

// ---------------------------------------------------------------------------
// Resource Normalization Methods
// ---------------------------------------------------------------------------

// NormalizeNamespace produces evidence for a Namespace.
func (n *Normalizer) NormalizeNamespace(ns *Namespace) []*corev1.Evidence {
	subject := BuildIdentity(n.clusterID, TypeNamespace, "", ns.ObjectMeta.Name)
	data := map[string]any{
		"name":       ns.ObjectMeta.Name,
		"phase":      ns.Status.Phase,
		"labels":     ns.ObjectMeta.Labels,
		"cluster_id": n.clusterID,
	}
	return []*corev1.Evidence{
		n.buildEvidence(ObservationConfiguration, subject, data),
	}
}

// NormalizeNode produces evidence for a Node.
func (n *Normalizer) NormalizeNode(node *Node) []*corev1.Evidence {
	subject := BuildIdentity(n.clusterID, TypeNode, "", node.ObjectMeta.Name)
	data := map[string]any{
		"name":       node.ObjectMeta.Name,
		"addresses":  node.Status.Addresses,
		"labels":     node.ObjectMeta.Labels,
		"cluster_id": n.clusterID,
	}

	evs := []*corev1.Evidence{
		n.buildEvidence(ObservationConfiguration, subject, data),
	}

	// Emit ResourceReference mapping for node IP addresses
	for _, addr := range node.Status.Addresses {
		if addr.Address != "" {
			refData := map[string]any{
				"address":        addr.Address,
				"address_type":   addr.Type,
				"reference_type": "node_address",
			}
			evs = append(evs, n.buildEvidence(ObservationResourceReference, subject, refData))
		}
	}

	return evs
}

// NormalizeDeployment produces evidence for a Deployment.
func (n *Normalizer) NormalizeDeployment(dep *Deployment) []*corev1.Evidence {
	subject := BuildIdentity(n.clusterID, TypeDeployment, dep.ObjectMeta.Namespace, dep.ObjectMeta.Name)

	containerImages := make([]string, 0, len(dep.Spec.Template.Spec.Containers))
	containerPorts := make([]map[string]any, 0)
	configMapRefs := make([]string, 0)
	secretRefs := make([]string, 0)

	for _, c := range dep.Spec.Template.Spec.Containers {
		containerImages = append(containerImages, c.Image)
		for _, p := range c.Ports {
			containerPorts = append(containerPorts, map[string]any{
				"container": c.Name,
				"port":      p.ContainerPort,
				"protocol":  p.Protocol,
			})
		}
		for _, e := range c.Env {
			if e.ValueFrom != nil {
				if e.ValueFrom.ConfigMapKeyRef != nil && e.ValueFrom.ConfigMapKeyRef.Name != "" {
					configMapRefs = append(configMapRefs, e.ValueFrom.ConfigMapKeyRef.Name)
				}
				if e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name != "" {
					secretRefs = append(secretRefs, e.ValueFrom.SecretKeyRef.Name)
				}
			}
		}
		for _, ef := range c.EnvFrom {
			if ef.ConfigMapRef != nil && ef.ConfigMapRef.Name != "" {
				configMapRefs = append(configMapRefs, ef.ConfigMapRef.Name)
			}
			if ef.SecretRef != nil && ef.SecretRef.Name != "" {
				secretRefs = append(secretRefs, ef.SecretRef.Name)
			}
		}
	}

	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.ConfigMap != nil && v.ConfigMap.Name != "" {
			configMapRefs = append(configMapRefs, v.ConfigMap.Name)
		}
		if v.Secret != nil && v.Secret.SecretName != "" {
			secretRefs = append(secretRefs, v.Secret.SecretName)
		}
	}

	configData := map[string]any{
		"replicas":        dep.Spec.Replicas,
		"selector":        dep.Spec.Selector.MatchLabels,
		"images":          containerImages,
		"ports":           containerPorts,
		"service_account": dep.Spec.Template.Spec.ServiceAccountName,
		"cluster_id":      n.clusterID,
	}

	evs := []*corev1.Evidence{
		n.buildEvidence(ObservationConfiguration, subject, configData),
	}

	// Owner references
	for _, owner := range dep.ObjectMeta.OwnerReferences {
		ownerIdentity := BuildIdentity(n.clusterID, owner.Kind, dep.ObjectMeta.Namespace, owner.Name)
		ownerData := map[string]any{
			"owner":                ownerIdentity,
			"owner_kind":           owner.Kind,
			"owner_name":           owner.Name,
			"owner_uid":            owner.UID,
			"controller":           owner.Controller != nil && *owner.Controller,
			"block_owner_deletion": owner.BlockOwnerDeletion != nil && *owner.BlockOwnerDeletion,
		}
		evs = append(evs, n.buildEvidence(ObservationOwnershipReference, subject, ownerData))
	}

	// ConfigMap references
	seenCM := make(map[string]bool)
	for _, cmName := range configMapRefs {
		if !seenCM[cmName] {
			seenCM[cmName] = true
			targetCM := BuildIdentity(n.clusterID, TypeConfigMap, dep.ObjectMeta.Namespace, cmName)
			refData := map[string]any{
				"reference_type": "config_map_ref",
				"target":         targetCM,
			}
			evs = append(evs, n.buildEvidence(ObservationResourceReference, subject, refData))
		}
	}

	// Secret references (name only!)
	seenSec := make(map[string]bool)
	for _, secName := range secretRefs {
		if !seenSec[secName] {
			seenSec[secName] = true
			targetSec := BuildIdentity(n.clusterID, TypeSecret, dep.ObjectMeta.Namespace, secName)
			refData := map[string]any{
				"reference_type": "secret_ref",
				"target":         targetSec,
			}
			evs = append(evs, n.buildEvidence(ObservationResourceReference, subject, refData))
		}
	}

	// ServiceAccount reference
	if sa := dep.Spec.Template.Spec.ServiceAccountName; sa != "" && sa != "default" {
		targetSA := BuildIdentity(n.clusterID, TypeServiceAccount, dep.ObjectMeta.Namespace, sa)
		refData := map[string]any{
			"reference_type": "service_account_ref",
			"target":         targetSA,
		}
		evs = append(evs, n.buildEvidence(ObservationResourceReference, subject, refData))
	}

	return evs
}

// NormalizeReplicaSet produces evidence for a ReplicaSet.
func (n *Normalizer) NormalizeReplicaSet(rs *ReplicaSet) []*corev1.Evidence {
	subject := BuildIdentity(n.clusterID, TypeReplicaSet, rs.ObjectMeta.Namespace, rs.ObjectMeta.Name)

	configData := map[string]any{
		"replicas":   rs.Spec.Replicas,
		"selector":   rs.Spec.Selector.MatchLabels,
		"cluster_id": n.clusterID,
	}

	evs := []*corev1.Evidence{
		n.buildEvidence(ObservationConfiguration, subject, configData),
	}

	// Owner references (e.g. ReplicaSet owned by Deployment)
	for _, owner := range rs.ObjectMeta.OwnerReferences {
		ownerIdentity := BuildIdentity(n.clusterID, owner.Kind, rs.ObjectMeta.Namespace, owner.Name)
		ownerData := map[string]any{
			"owner":                ownerIdentity,
			"owner_kind":           owner.Kind,
			"owner_name":           owner.Name,
			"owner_uid":            owner.UID,
			"controller":           owner.Controller != nil && *owner.Controller,
			"block_owner_deletion": owner.BlockOwnerDeletion != nil && *owner.BlockOwnerDeletion,
		}
		evs = append(evs, n.buildEvidence(ObservationOwnershipReference, subject, ownerData))
	}

	return evs
}

// NormalizeService produces evidence for a Service.
func (n *Normalizer) NormalizeService(svc *Service) []*corev1.Evidence {
	subject := BuildIdentity(n.clusterID, TypeService, svc.ObjectMeta.Namespace, svc.ObjectMeta.Name)

	ports := make([]map[string]any, 0, len(svc.Spec.Ports))
	for _, p := range svc.Spec.Ports {
		ports = append(ports, map[string]any{
			"name":        p.Name,
			"port":        p.Port,
			"target_port": p.TargetPort,
			"protocol":    p.Protocol,
			"node_port":   p.NodePort,
		})
	}

	configData := map[string]any{
		"type":        svc.Spec.Type,
		"cluster_ip":  svc.Spec.ClusterIP,
		"cluster_ips": svc.Spec.ClusterIPs,
		"ports":       ports,
		"selector":    svc.Spec.Selector,
		"cluster_id":  n.clusterID,
	}

	evs := []*corev1.Evidence{
		n.buildEvidence(ObservationConfiguration, subject, configData),
	}

	// Crucial for Core Engine's RuntimeConnectionRule:
	// A Service with a ClusterIP provides a network endpoint that downstream
	// clients connect to. Emit RESOURCE_REFERENCE on the Service subject
	// with {"address": cluster_ip, "port": port}.
	if svc.Spec.ClusterIP != "" && svc.Spec.ClusterIP != "None" {
		for _, p := range svc.Spec.Ports {
			if p.Port > 0 {
				refData := map[string]any{
					"address":        svc.Spec.ClusterIP,
					"port":           p.Port,
					"protocol":       p.Protocol,
					"service_name":   svc.ObjectMeta.Name,
					"reference_type": "service_endpoint",
				}
				evs = append(evs, n.buildEvidence(ObservationResourceReference, subject, refData))
			}
		}
	}

	return evs
}

// NormalizeIngress produces evidence for an Ingress.
func (n *Normalizer) NormalizeIngress(ing *Ingress) []*corev1.Evidence {
	subject := BuildIdentity(n.clusterID, TypeIngress, ing.ObjectMeta.Namespace, ing.ObjectMeta.Name)

	rules := make([]map[string]any, 0, len(ing.Spec.Rules))
	servicesReferenced := make([]string, 0)
	tlsSecretsReferenced := make([]string, 0)

	for _, rule := range ing.Spec.Rules {
		ruleInfo := map[string]any{
			"host": rule.Host,
		}
		if rule.HTTP != nil {
			paths := make([]map[string]any, 0, len(rule.HTTP.Paths))
			for _, p := range rule.HTTP.Paths {
				pathInfo := map[string]any{
					"path":      p.Path,
					"path_type": p.PathType,
				}
				if p.Backend.Service != nil {
					pathInfo["service_name"] = p.Backend.Service.Name
					pathInfo["service_port"] = p.Backend.Service.Port.Number
					servicesReferenced = append(servicesReferenced, p.Backend.Service.Name)
				}
				paths = append(paths, pathInfo)
			}
			ruleInfo["paths"] = paths
		}
		rules = append(rules, ruleInfo)
	}

	for _, tls := range ing.Spec.TLS {
		if tls.SecretName != "" {
			tlsSecretsReferenced = append(tlsSecretsReferenced, tls.SecretName)
		}
	}

	configData := map[string]any{
		"ingress_class": ing.Spec.IngressClassName,
		"rules":         rules,
		"cluster_id":    n.clusterID,
	}

	evs := []*corev1.Evidence{
		n.buildEvidence(ObservationConfiguration, subject, configData),
	}

	// Service references
	seenSvc := make(map[string]bool)
	for _, svcName := range servicesReferenced {
		if !seenSvc[svcName] {
			seenSvc[svcName] = true
			targetSvc := BuildIdentity(n.clusterID, TypeService, ing.ObjectMeta.Namespace, svcName)
			refData := map[string]any{
				"reference_type": "ingress_service_backend",
				"target":         targetSvc,
			}
			evs = append(evs, n.buildEvidence(ObservationResourceReference, subject, refData))
		}
	}

	// TLS secret references (names only!)
	seenSec := make(map[string]bool)
	for _, secName := range tlsSecretsReferenced {
		if !seenSec[secName] {
			seenSec[secName] = true
			targetSec := BuildIdentity(n.clusterID, TypeSecret, ing.ObjectMeta.Namespace, secName)
			refData := map[string]any{
				"reference_type": "ingress_tls_secret",
				"target":         targetSec,
			}
			evs = append(evs, n.buildEvidence(ObservationResourceReference, subject, refData))
		}
	}

	return evs
}

// NormalizePod produces evidence for a Pod.
func (n *Normalizer) NormalizePod(pod *Pod) []*corev1.Evidence {
	subject := BuildIdentity(n.clusterID, TypePod, pod.ObjectMeta.Namespace, pod.ObjectMeta.Name)

	containerPorts := make([]map[string]any, 0)
	configMapRefs := make([]string, 0)
	secretRefs := make([]string, 0)
	pvcRefs := make([]string, 0)

	for _, c := range pod.Spec.Containers {
		for _, p := range c.Ports {
			containerPorts = append(containerPorts, map[string]any{
				"container":      c.Name,
				"container_port": p.ContainerPort,
				"host_port":      p.HostPort,
				"protocol":       p.Protocol,
			})
		}
		for _, e := range c.Env {
			if e.ValueFrom != nil {
				if e.ValueFrom.ConfigMapKeyRef != nil && e.ValueFrom.ConfigMapKeyRef.Name != "" {
					configMapRefs = append(configMapRefs, e.ValueFrom.ConfigMapKeyRef.Name)
				}
				if e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name != "" {
					secretRefs = append(secretRefs, e.ValueFrom.SecretKeyRef.Name)
				}
			}
		}
		for _, ef := range c.EnvFrom {
			if ef.ConfigMapRef != nil && ef.ConfigMapRef.Name != "" {
				configMapRefs = append(configMapRefs, ef.ConfigMapRef.Name)
			}
			if ef.SecretRef != nil && ef.SecretRef.Name != "" {
				secretRefs = append(secretRefs, ef.SecretRef.Name)
			}
		}
	}

	for _, v := range pod.Spec.Volumes {
		if v.ConfigMap != nil && v.ConfigMap.Name != "" {
			configMapRefs = append(configMapRefs, v.ConfigMap.Name)
		}
		if v.Secret != nil && v.Secret.SecretName != "" {
			secretRefs = append(secretRefs, v.Secret.SecretName)
		}
		if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName != "" {
			pvcRefs = append(pvcRefs, v.PersistentVolumeClaim.ClaimName)
		}
	}

	configData := map[string]any{
		"node_name":       pod.Spec.NodeName,
		"phase":           pod.Status.Phase,
		"pod_ip":          pod.Status.PodIP,
		"host_ip":         pod.Status.HostIP,
		"service_account": pod.Spec.ServiceAccountName,
		"ports":           containerPorts,
		"cluster_id":      n.clusterID,
	}

	evs := []*corev1.Evidence{
		n.buildEvidence(ObservationConfiguration, subject, configData),
	}

	// Owner references (e.g. Pod owned by ReplicaSet)
	for _, owner := range pod.ObjectMeta.OwnerReferences {
		ownerIdentity := BuildIdentity(n.clusterID, owner.Kind, pod.ObjectMeta.Namespace, owner.Name)
		ownerData := map[string]any{
			"owner":                ownerIdentity,
			"owner_kind":           owner.Kind,
			"owner_name":           owner.Name,
			"owner_uid":            owner.UID,
			"controller":           owner.Controller != nil && *owner.Controller,
			"block_owner_deletion": owner.BlockOwnerDeletion != nil && *owner.BlockOwnerDeletion,
		}
		evs = append(evs, n.buildEvidence(ObservationOwnershipReference, subject, ownerData))
	}

	// Pod IP Endpoint mapping for RuntimeConnectionRule
	if pod.Status.PodIP != "" {
		for _, cp := range containerPorts {
			if port, ok := cp["container_port"].(int); ok && port > 0 {
				proto, _ := cp["protocol"].(string)
				refData := map[string]any{
					"address":        pod.Status.PodIP,
					"port":           port,
					"protocol":       proto,
					"reference_type": "pod_endpoint",
				}
				evs = append(evs, n.buildEvidence(ObservationResourceReference, subject, refData))
			}
		}
	}

	// Node reference
	if pod.Spec.NodeName != "" {
		targetNode := BuildIdentity(n.clusterID, TypeNode, "", pod.Spec.NodeName)
		refData := map[string]any{
			"reference_type": "pod_scheduled_node",
			"target":         targetNode,
		}
		evs = append(evs, n.buildEvidence(ObservationResourceReference, subject, refData))
	}

	// ServiceAccount reference
	if sa := pod.Spec.ServiceAccountName; sa != "" && sa != "default" {
		targetSA := BuildIdentity(n.clusterID, TypeServiceAccount, pod.ObjectMeta.Namespace, sa)
		refData := map[string]any{
			"reference_type": "service_account_ref",
			"target":         targetSA,
		}
		evs = append(evs, n.buildEvidence(ObservationResourceReference, subject, refData))
	}

	// PVC references
	for _, pvc := range pvcRefs {
		targetPVC := BuildIdentity(n.clusterID, TypePersistentVolumeClaim, pod.ObjectMeta.Namespace, pvc)
		refData := map[string]any{
			"reference_type": "pvc_mount_ref",
			"target":         targetPVC,
		}
		evs = append(evs, n.buildEvidence(ObservationResourceReference, subject, refData))
	}

	return evs
}

// NormalizeConfigMap produces evidence for a ConfigMap.
func (n *Normalizer) NormalizeConfigMap(cm *ConfigMap) []*corev1.Evidence {
	subject := BuildIdentity(n.clusterID, TypeConfigMap, cm.ObjectMeta.Namespace, cm.ObjectMeta.Name)

	keys := make([]string, 0, len(cm.Data))
	for k := range cm.Data {
		keys = append(keys, k)
	}

	data := map[string]any{
		"name":       cm.ObjectMeta.Name,
		"namespace":  cm.ObjectMeta.Namespace,
		"keys":       keys,
		"cluster_id": n.clusterID,
	}

	return []*corev1.Evidence{
		n.buildEvidence(ObservationConfiguration, subject, data),
	}
}

// NormalizeSecret produces evidence for a Secret strictly containing metadata and key names.
// HARD SECURITY BOUNDARY: No secret values or stringData ever enter evidence.
func (n *Normalizer) NormalizeSecret(sec *SecretMetadata) []*corev1.Evidence {
	subject := BuildIdentity(n.clusterID, TypeSecret, sec.ObjectMeta.Namespace, sec.ObjectMeta.Name)

	data := map[string]any{
		"name":       sec.ObjectMeta.Name,
		"namespace":  sec.ObjectMeta.Namespace,
		"type":       sec.Type,
		"key_names":  sec.KeyNames,
		"cluster_id": n.clusterID,
	}

	return []*corev1.Evidence{
		n.buildEvidence(ObservationConfiguration, subject, data),
	}
}

// NormalizePVC produces evidence for a PersistentVolumeClaim.
func (n *Normalizer) NormalizePVC(pvc *PersistentVolumeClaim) []*corev1.Evidence {
	subject := BuildIdentity(n.clusterID, TypePersistentVolumeClaim, pvc.ObjectMeta.Namespace, pvc.ObjectMeta.Name)

	data := map[string]any{
		"name":          pvc.ObjectMeta.Name,
		"namespace":     pvc.ObjectMeta.Namespace,
		"volume_name":   pvc.Spec.VolumeName,
		"storage_class": pvc.Spec.StorageClassName,
		"access_modes":  pvc.Spec.AccessModes,
		"phase":         pvc.Status.Phase,
		"cluster_id":    n.clusterID,
	}

	evs := []*corev1.Evidence{
		n.buildEvidence(ObservationConfiguration, subject, data),
	}

	if pvc.Spec.VolumeName != "" {
		targetPV := BuildIdentity(n.clusterID, TypePersistentVolume, "", pvc.Spec.VolumeName)
		refData := map[string]any{
			"reference_type": "bound_persistent_volume",
			"target":         targetPV,
		}
		evs = append(evs, n.buildEvidence(ObservationResourceReference, subject, refData))
	}

	return evs
}

// NormalizePV produces evidence for a PersistentVolume.
func (n *Normalizer) NormalizePV(pv *PersistentVolume) []*corev1.Evidence {
	subject := BuildIdentity(n.clusterID, TypePersistentVolume, "", pv.ObjectMeta.Name)

	data := map[string]any{
		"name":          pv.ObjectMeta.Name,
		"storage_class": pv.Spec.StorageClassName,
		"capacity":      pv.Spec.Capacity,
		"access_modes":  pv.Spec.AccessModes,
		"phase":         pv.Status.Phase,
		"cluster_id":    n.clusterID,
	}

	evs := []*corev1.Evidence{
		n.buildEvidence(ObservationConfiguration, subject, data),
	}

	if pv.Spec.ClaimRef != nil && pv.Spec.ClaimRef.Name != "" {
		targetPVC := BuildIdentity(n.clusterID, TypePersistentVolumeClaim, pv.Spec.ClaimRef.Namespace, pv.Spec.ClaimRef.Name)
		refData := map[string]any{
			"reference_type": "bound_claim",
			"target":         targetPVC,
		}
		evs = append(evs, n.buildEvidence(ObservationResourceReference, subject, refData))
	}

	return evs
}
