package k8s

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNormalizer_Deployment(t *testing.T) {
	normalizer := NewNormalizer("cluster-1", "ws-1", "k8s-collector")
	normalizer.now = func() time.Time {
		return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	}

	dep := &Deployment{
		ObjectMeta: ObjectMeta{
			Name:      "api-server",
			Namespace: "payments",
			Labels:    map[string]string{"app": "api"},
			OwnerReferences: []OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "CustomApp",
					Name:       "my-app",
					UID:        "app-uid-123",
				},
			},
		},
		Spec: DeploymentSpec{
			Selector: LabelSelector{
				MatchLabels: map[string]string{"app": "api"},
			},
			Template: PodTemplateSpec{
				Spec: PodSpec{
					ServiceAccountName: "api-sa",
					Containers: []Container{
						{
							Name:  "api",
							Image: "registry.internal/api:v1.2",
							Ports: []ContainerPort{
								{ContainerPort: 8080, Protocol: "TCP"},
							},
							Env: []EnvVar{
								{
									Name: "DATABASE_URL",
									ValueFrom: &EnvVarSource{
										SecretKeyRef: &SecretKeySelector{
											Name: "db-secret",
											Key:  "url",
										},
									},
								},
							},
							EnvFrom: []EnvFromSource{
								{
									ConfigMapRef: &ConfigMapEnvSource{
										Name: "app-config",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	evs := normalizer.NormalizeDeployment(dep)

	if len(evs) < 4 {
		t.Fatalf("expected at least 4 evidence records (CONFIG, OWNER, SA_REF, SECRET_REF, CM_REF), got %d", len(evs))
	}

	// 1. CONFIGURATION
	configEv := evs[0]
	if configEv.ObservationType != ObservationConfiguration {
		t.Errorf("expected CONFIGURATION, got %s", configEv.ObservationType)
	}
	if configEv.Subject.ProviderId != "cluster-1/payments/api-server" {
		t.Errorf("unexpected subject provider_id: %s", configEv.Subject.ProviderId)
	}

	// 2. Ownership reference
	var foundOwner, foundSA, foundSecret, foundCM bool
	for _, ev := range evs {
		var data map[string]any
		_ = json.Unmarshal(ev.Data, &data)

		if ev.ObservationType == ObservationOwnershipReference {
			foundOwner = true
			if data["owner_name"] != "my-app" {
				t.Errorf("expected owner_name 'my-app', got %v", data["owner_name"])
			}
		}
		if ev.ObservationType == ObservationResourceReference {
			switch data["reference_type"] {
			case "service_account_ref":
				foundSA = true
			case "secret_ref":
				foundSecret = true
			case "config_map_ref":
				foundCM = true
			}
		}
	}

	if !foundOwner {
		t.Errorf("missing ownership reference evidence")
	}
	if !foundSA {
		t.Errorf("missing service account reference evidence")
	}
	if !foundSecret {
		t.Errorf("missing secret reference evidence")
	}
	if !foundCM {
		t.Errorf("missing config map reference evidence")
	}
}

func TestNormalizer_Service_EndpointMapping(t *testing.T) {
	normalizer := NewNormalizer("prod-cluster", "ws-default", "k8s-collector")
	svc := &Service{
		ObjectMeta: ObjectMeta{
			Name:      "postgres-db",
			Namespace: "database",
		},
		Spec: ServiceSpec{
			Type:      "ClusterIP",
			ClusterIP: "10.96.10.50",
			Ports: []ServicePort{
				{Name: "postgres", Port: 5432, Protocol: "TCP"},
			},
		},
	}

	evs := normalizer.NormalizeService(svc)

	if len(evs) != 2 {
		t.Fatalf("expected 2 evidence records (1 CONFIG + 1 RESOURCE_REFERENCE), got %d", len(evs))
	}

	// Verify RESOURCE_REFERENCE contains address and port for RuntimeConnectionRule
	refEv := evs[1]
	if refEv.ObservationType != ObservationResourceReference {
		t.Errorf("expected RESOURCE_REFERENCE, got %s", refEv.ObservationType)
	}

	var data map[string]any
	if err := json.Unmarshal(refEv.Data, &data); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}

	if data["address"] != "10.96.10.50" {
		t.Errorf("expected address '10.96.10.50', got %v", data["address"])
	}
	if port, ok := data["port"].(float64); !ok || int(port) != 5432 {
		t.Errorf("expected port 5432, got %v", data["port"])
	}
}

func TestNormalizer_Ingress(t *testing.T) {
	normalizer := NewNormalizer("cluster-1", "ws-1", "k8s-collector")
	className := "nginx"
	ing := &Ingress{
		ObjectMeta: ObjectMeta{
			Name:      "web-ingress",
			Namespace: "frontend",
		},
		Spec: IngressSpec{
			IngressClassName: &className,
			DefaultBackend: &IngressBackend{
				Service: &IngressServiceBackend{
					Name: "default-svc",
					Port: IngressServiceBackendPort{Number: 80},
				},
			},
			Rules: []IngressRule{
				{
					Host: "example.com",
					HTTP: &HTTPIngressRuleValue{
						Paths: []HTTPIngressPath{
							{
								Path: "/api",
								Backend: IngressBackend{
									Service: &IngressServiceBackend{
										Name: "api-svc",
										Port: IngressServiceBackendPort{Number: 8080},
									},
								},
							},
						},
					},
				},
			},
			TLS: []IngressTLS{
				{
					Hosts:      []string{"example.com"},
					SecretName: "example-tls",
				},
			},
		},
	}

	evs := normalizer.NormalizeIngress(ing)

	var foundAPISvcRef, foundDefaultSvcRef, foundTLSRef bool
	for _, ev := range evs {
		if ev.ObservationType == ObservationResourceReference {
			var data map[string]any
			_ = json.Unmarshal(ev.Data, &data)
			if data["reference_type"] == "ingress_backend_ref" {
				if tgt, ok := data["target"].(map[string]any); ok {
					if tgt["provider_id"] == "cluster-1/frontend/api-svc" {
						foundAPISvcRef = true
					}
					if tgt["provider_id"] == "cluster-1/frontend/default-svc" {
						foundDefaultSvcRef = true
					}
				}
			}
			if data["reference_type"] == "ingress_tls_secret" {
				foundTLSRef = true
			}
		}
	}

	if !foundAPISvcRef {
		t.Errorf("expected ingress_backend_ref for api-svc")
	}
	if !foundDefaultSvcRef {
		t.Errorf("expected ingress_backend_ref for default-svc")
	}
	if !foundTLSRef {
		t.Errorf("expected ingress_tls_secret reference")
	}
}

func TestNormalizer_PVC_and_PV(t *testing.T) {
	normalizer := NewNormalizer("cluster-1", "ws-1", "k8s-collector")

	pvc := &PersistentVolumeClaim{
		ObjectMeta: ObjectMeta{
			Name:      "data-pvc",
			Namespace: "database",
		},
		Spec: PersistentVolumeClaimSpec{
			VolumeName: "pv-volume-01",
		},
	}

	evs := normalizer.NormalizePVC(pvc)
	if len(evs) != 2 {
		t.Fatalf("expected 2 evidence records for PVC, got %d", len(evs))
	}

	pv := &PersistentVolume{
		ObjectMeta: ObjectMeta{
			Name: "pv-volume-01",
		},
		Spec: PersistentVolumeSpec{
			ClaimRef: &ObjectReference{
				Namespace: "database",
				Name:      "data-pvc",
			},
		},
	}

	pvEvs := normalizer.NormalizePV(pv)
	if len(pvEvs) != 2 {
		t.Fatalf("expected 2 evidence records for PV, got %d", len(pvEvs))
	}
}

func TestNormalizer_ReplicaSet(t *testing.T) {
	normalizer := NewNormalizer("cluster-1", "ws-1", "k8s-collector")
	controller := true
	rs := &ReplicaSet{
		ObjectMeta: ObjectMeta{
			Name:      "api-server-6789abc",
			Namespace: "payments",
			OwnerReferences: []OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       "api-server",
					UID:        "deploy-uid-123",
					Controller: &controller,
				},
			},
		},
	}

	evs := normalizer.NormalizeReplicaSet(rs)
	if len(evs) != 2 {
		t.Fatalf("expected 2 evidence records (1 CONFIG + 1 OWNER), got %d", len(evs))
	}

	var foundOwner bool
	for _, ev := range evs {
		if ev.ObservationType == ObservationOwnershipReference {
			foundOwner = true
			var data map[string]any
			_ = json.Unmarshal(ev.Data, &data)
			if data["owner_name"] != "api-server" {
				t.Errorf("expected owner_name 'api-server', got %v", data["owner_name"])
			}
			if data["owner_kind"] != "Deployment" {
				t.Errorf("expected owner_kind 'Deployment', got %v", data["owner_kind"])
			}
		}
	}
	if !foundOwner {
		t.Errorf("missing ownership reference evidence in ReplicaSet normalization")
	}
}

func TestNormalizer_Pod_AllRequiredRelationships(t *testing.T) {
	normalizer := NewNormalizer("cluster-prod", "ws-test", "k8s-collector")

	pod := &Pod{
		ObjectMeta: ObjectMeta{
			Name:      "web-pod-xyz",
			Namespace: "payments",
			Labels:    map[string]string{"app": "web", "tier": "frontend"},
		},
		Spec: PodSpec{
			NodeName:           "node-worker-1",
			ServiceAccountName: "payments-sa",
			InitContainers: []Container{
				{
					Name:  "init-setup",
					Image: "busybox:latest",
					Env: []EnvVar{
						{
							Name: "INIT_CFG",
							ValueFrom: &EnvVarSource{
								ConfigMapKeyRef: &ConfigMapKeySelector{
									Name: "init-config",
									Key:  "setup.sh",
								},
							},
						},
					},
					EnvFrom: []EnvFromSource{
						{
							SecretRef: &SecretEnvSource{
								Name: "init-secret",
							},
						},
					},
				},
			},
			Containers: []Container{
				{
					Name:  "web",
					Image: "nginx:alpine",
					Ports: []ContainerPort{
						{ContainerPort: 80, Protocol: "TCP"},
					},
					Env: []EnvVar{
						{
							Name: "DATABASE_URL",
							ValueFrom: &EnvVarSource{
								SecretKeyRef: &SecretKeySelector{
									Name: "db-secret",
									Key:  "url",
								},
							},
						},
						{
							Name: "APP_ENV",
							ValueFrom: &EnvVarSource{
								ConfigMapKeyRef: &ConfigMapKeySelector{
									Name: "app-config",
									Key:  "environment",
								},
							},
						},
					},
					EnvFrom: []EnvFromSource{
						{
							ConfigMapRef: &ConfigMapEnvSource{
								Name: "shared-config",
							},
						},
						{
							SecretRef: &SecretEnvSource{
								Name: "shared-secret",
							},
						},
					},
				},
			},
			Volumes: []Volume{
				{
					Name: "data-vol",
					PersistentVolumeClaim: &PVCVolumeSource{
						ClaimName: "web-data-pvc",
					},
				},
				{
					Name: "cm-vol",
					ConfigMap: &ConfigMapVolumeSource{
						Name: "vol-config",
					},
				},
				{
					Name: "sec-vol",
					Secret: &SecretVolumeSource{
						SecretName: "vol-secret",
					},
				},
				{
					Name: "proj-vol",
					Projected: &ProjectedVolumeSource{
						Sources: []VolumeProjection{
							{
								ConfigMap: &ConfigMapProjection{
									Name: "proj-config",
								},
							},
							{
								Secret: &SecretProjection{
									Name: "proj-secret",
								},
							},
						},
					},
				},
				// Duplicate reference to test deduplication
				{
					Name: "dup-cm-vol",
					ConfigMap: &ConfigMapVolumeSource{
						Name: "app-config",
					},
				},
			},
		},
		Status: PodStatus{
			Phase: "Running",
			PodIP: "10.244.0.15",
		},
	}

	evs := normalizer.NormalizePod(pod)

	var nodeRefs, cmRefs, secRefs, pvcRefs, saRefs []string
	for _, ev := range evs {
		if ev.ObservationType == ObservationResourceReference {
			var data map[string]any
			if err := json.Unmarshal(ev.Data, &data); err != nil {
				t.Fatalf("failed to unmarshal evidence data: %v", err)
			}
			refType, _ := data["reference_type"].(string)
			targetMap, _ := data["target"].(map[string]any)
			providerID, _ := targetMap["provider_id"].(string)

			switch refType {
			case "pod_scheduled_node":
				nodeRefs = append(nodeRefs, providerID)
			case "config_map_ref":
				cmRefs = append(cmRefs, providerID)
			case "secret_ref":
				secRefs = append(secRefs, providerID)
			case "pvc_mount_ref":
				pvcRefs = append(pvcRefs, providerID)
			case "service_account_ref":
				saRefs = append(saRefs, providerID)
			}
		}
	}

	// 1. Pod -> Node (pod_scheduled_node)
	if len(nodeRefs) != 1 || nodeRefs[0] != "cluster-prod/node-worker-1" {
		t.Errorf("expected pod_scheduled_node for node-worker-1, got %v", nodeRefs)
	}

	// 2. Pod -> ConfigMap (config_map_ref)
	// Expected: init-config, app-config, shared-config, vol-config, proj-config (all unique)
	expectedCMs := map[string]bool{
		"cluster-prod/payments/init-config":   true,
		"cluster-prod/payments/app-config":    true,
		"cluster-prod/payments/shared-config": true,
		"cluster-prod/payments/vol-config":    true,
		"cluster-prod/payments/proj-config":   true,
	}
	if len(cmRefs) != len(expectedCMs) {
		t.Errorf("expected %d config_map_refs, got %d: %v", len(expectedCMs), len(cmRefs), cmRefs)
	}
	for _, r := range cmRefs {
		if !expectedCMs[r] {
			t.Errorf("unexpected config_map_ref: %s", r)
		}
	}

	// 3. Pod -> Secret (secret_ref)
	// Expected: init-secret, db-secret, shared-secret, vol-secret, proj-secret
	expectedSecs := map[string]bool{
		"cluster-prod/payments/init-secret":   true,
		"cluster-prod/payments/db-secret":     true,
		"cluster-prod/payments/shared-secret": true,
		"cluster-prod/payments/vol-secret":    true,
		"cluster-prod/payments/proj-secret":   true,
	}
	if len(secRefs) != len(expectedSecs) {
		t.Errorf("expected %d secret_refs, got %d: %v", len(expectedSecs), len(secRefs), secRefs)
	}
	for _, r := range secRefs {
		if !expectedSecs[r] {
			t.Errorf("unexpected secret_ref: %s", r)
		}
	}

	// 4. Pod -> PVC (pvc_mount_ref)
	if len(pvcRefs) != 1 || pvcRefs[0] != "cluster-prod/payments/web-data-pvc" {
		t.Errorf("expected pvc_mount_ref for web-data-pvc, got %v", pvcRefs)
	}

	// 5. Pod -> ServiceAccount (service_account_ref)
	if len(saRefs) != 1 || saRefs[0] != "cluster-prod/payments/payments-sa" {
		t.Errorf("expected service_account_ref for payments-sa, got %v", saRefs)
	}
}

func TestNormalizer_Pod_ServiceAccountDeprecatedField(t *testing.T) {
	normalizer := NewNormalizer("cluster-1", "ws-1", "k8s-collector")
	pod := &Pod{
		ObjectMeta: ObjectMeta{
			Name:      "sa-test-pod",
			Namespace: "default",
		},
		Spec: PodSpec{
			ServiceAccount: "custom-legacy-sa",
		},
	}

	evs := normalizer.NormalizePod(pod)
	var foundSA bool
	for _, ev := range evs {
		if ev.ObservationType == ObservationResourceReference {
			var data map[string]any
			_ = json.Unmarshal(ev.Data, &data)
			if data["reference_type"] == "service_account_ref" {
				foundSA = true
				if tgt, ok := data["target"].(map[string]any); ok {
					if tgt["provider_id"] != "cluster-1/default/custom-legacy-sa" {
						t.Errorf("unexpected SA provider_id: %v", tgt["provider_id"])
					}
				}
			}
		}
	}
	if !foundSA {
		t.Errorf("expected service_account_ref when using legacy serviceAccount field")
	}
}

func TestNormalizer_Pod_MalformedReferencesNoFabrication(t *testing.T) {
	normalizer := NewNormalizer("cluster-1", "ws-1", "k8s-collector")
	pod := &Pod{
		ObjectMeta: ObjectMeta{
			Name:      "empty-refs-pod",
			Namespace: "default",
		},
		Spec: PodSpec{
			NodeName:           "",
			ServiceAccountName: "",
			Containers: []Container{
				{
					Name: "c1",
					Env: []EnvVar{
						{
							Name: "EMPTY_CFG",
							ValueFrom: &EnvVarSource{
								ConfigMapKeyRef: &ConfigMapKeySelector{Name: ""},
							},
						},
						{
							Name: "EMPTY_SEC",
							ValueFrom: &EnvVarSource{
								SecretKeyRef: &SecretKeySelector{Name: ""},
							},
						},
					},
					EnvFrom: []EnvFromSource{
						{ConfigMapRef: &ConfigMapEnvSource{Name: ""}},
						{SecretRef: &SecretEnvSource{Name: ""}},
					},
				},
			},
			Volumes: []Volume{
				{
					Name:      "empty-cm",
					ConfigMap: &ConfigMapVolumeSource{Name: ""},
				},
				{
					Name:   "empty-sec",
					Secret: &SecretVolumeSource{SecretName: ""},
				},
				{
					Name:                  "empty-pvc",
					PersistentVolumeClaim: &PVCVolumeSource{ClaimName: ""},
				},
				{
					Name: "empty-proj",
					Projected: &ProjectedVolumeSource{
						Sources: []VolumeProjection{
							{ConfigMap: &ConfigMapProjection{Name: ""}},
							{Secret: &SecretProjection{Name: ""}},
						},
					},
				},
			},
		},
	}

	evs := normalizer.NormalizePod(pod)

	// Verify that NO resource references are emitted when targets have empty names
	for _, ev := range evs {
		if ev.ObservationType == ObservationResourceReference {
			var data map[string]any
			_ = json.Unmarshal(ev.Data, &data)
			refType := data["reference_type"]
			if refType == "config_map_ref" || refType == "secret_ref" ||
				refType == "pvc_mount_ref" || refType == "service_account_ref" ||
				refType == "pod_scheduled_node" {
				t.Errorf("unexpected reference %v generated for empty target name", refType)
			}
		}
	}
}

func TestNegative_ServiceSelectorDoesNotCreateCallsOrDependency(t *testing.T) {
	normalizer := NewNormalizer("cluster-1", "ws-1", "k8s-collector")
	svc := &Service{
		ObjectMeta: ObjectMeta{
			Name:      "web-service",
			Namespace: "default",
		},
		Spec: ServiceSpec{
			Selector: map[string]string{
				"app":  "web",
				"tier": "frontend",
			},
		},
	}

	evs := normalizer.NormalizeService(svc)

	// The Service normalizer must ONLY produce CONFIGURATION (no ClusterIP set here)
	if len(evs) != 1 {
		t.Fatalf("expected exactly 1 CONFIGURATION evidence, got %d", len(evs))
	}
	if evs[0].ObservationType != ObservationConfiguration {
		t.Errorf("expected CONFIGURATION, got %s", evs[0].ObservationType)
	}

	// Verify selector is present ONLY in configuration metadata, not as a target reference
	var data map[string]any
	_ = json.Unmarshal(evs[0].Data, &data)
	if _, ok := data["selector"]; !ok {
		t.Errorf("expected selector in configuration data")
	}

	// Verify ZERO RESOURCE_REFERENCE targeting Pods
	for _, ev := range evs {
		if ev.ObservationType == ObservationResourceReference {
			t.Errorf("Service must NOT emit RESOURCE_REFERENCE matching selectors to Pods")
		}
	}
}

func TestNegative_ZeroSecretCustody(t *testing.T) {
	normalizer := NewNormalizer("cluster-1", "ws-1", "k8s-collector")

	// 1. Normalize Secret: metadata only
	sec := &SecretMetadata{
		ObjectMeta: ObjectMeta{
			Name:      "super-secret",
			Namespace: "default",
		},
		Type:     "Opaque",
		KeyNames: []string{"password", "token"},
	}

	evs := normalizer.NormalizeSecret(sec)
	if len(evs) != 1 {
		t.Fatalf("expected 1 CONFIGURATION evidence, got %d", len(evs))
	}

	var secData map[string]any
	_ = json.Unmarshal(evs[0].Data, &secData)

	if _, ok := secData["data"]; ok {
		t.Errorf("SECURITY VIOLATION: Secret payload field 'data' present in evidence")
	}
	if _, ok := secData["stringData"]; ok {
		t.Errorf("SECURITY VIOLATION: Secret payload field 'stringData' present in evidence")
	}
	if _, ok := secData["values"]; ok {
		t.Errorf("SECURITY VIOLATION: Secret payload field 'values' present in evidence")
	}

	// Verify only key names are preserved
	if keyNames, ok := secData["key_names"].([]any); !ok || len(keyNames) != 2 {
		t.Errorf("expected 2 key names, got %v", secData["key_names"])
	}

	// 2. Pod referencing secret: metadata name only
	pod := &Pod{
		ObjectMeta: ObjectMeta{
			Name:      "secret-user-pod",
			Namespace: "default",
		},
		Spec: PodSpec{
			Containers: []Container{
				{
					Name: "worker",
					Env: []EnvVar{
						{
							Name: "SECRET_PASS",
							ValueFrom: &EnvVarSource{
								SecretKeyRef: &SecretKeySelector{
									Name: "super-secret",
									Key:  "password",
								},
							},
						},
					},
				},
			},
		},
	}

	podEvs := normalizer.NormalizePod(pod)
	for _, ev := range podEvs {
		if ev.ObservationType == ObservationResourceReference {
			var pData map[string]any
			_ = json.Unmarshal(ev.Data, &pData)
			if pData["reference_type"] == "secret_ref" {
				// Target must be ResourceIdentity pointing to secret
				target, ok := pData["target"].(map[string]any)
				if !ok {
					t.Fatalf("expected target in secret_ref evidence")
				}
				if target["resource_type"] != "secret" {
					t.Errorf("expected target resource_type 'secret', got %v", target["resource_type"])
				}
				// Verify no values or payloads exist in evidence data
				if _, hasVal := pData["value"]; hasVal {
					t.Errorf("SECURITY VIOLATION: Secret value leaked in pod evidence")
				}
			}
		}
	}
}
