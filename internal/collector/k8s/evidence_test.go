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

	var foundSvcRef, foundTLSRef bool
	for _, ev := range evs {
		if ev.ObservationType == ObservationResourceReference {
			var data map[string]any
			_ = json.Unmarshal(ev.Data, &data)
			if data["reference_type"] == "ingress_service_backend" {
				foundSvcRef = true
			}
			if data["reference_type"] == "ingress_tls_secret" {
				foundTLSRef = true
			}
		}
	}

	if !foundSvcRef {
		t.Errorf("expected ingress_service_backend reference")
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

