package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecretSafety_NeverSerializesPayloads(t *testing.T) {
	superSecretPassword := "SUPER_CONFIDENTIAL_DB_PASSWORD_12345!"
	privateKeyPEM := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0...\n-----END RSA PRIVATE KEY-----"
	serviceAccountToken := "eyJhbGciOiJSUzI1NiIsImtpZCI6IiJ9.eyJpc3MiOiJrdWJlcm5ldGVzIn0.token"

	// Mock Kubernetes API returning secrets containing sensitive payloads in .data and .stringData
	mockK8sServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/secrets") {
			resp := map[string]any{
				"metadata": map[string]any{
					"resourceVersion": "100",
				},
				"items": []map[string]any{
					{
						"metadata": map[string]any{
							"name":      "db-credentials",
							"namespace": "default",
							"uid":       "sec-uid-1",
						},
						"type": "Opaque",
						"data": map[string]string{
							"password":    superSecretPassword,
							"private.key": privateKeyPEM,
							"sa.token":    serviceAccountToken,
						},
						"stringData": map[string]string{
							"raw_pass": superSecretPassword,
						},
					},
					{
						"metadata": map[string]any{
							"name":      "tls-cert",
							"namespace": "default",
							"uid":       "sec-uid-2",
						},
						"type": "kubernetes.io/tls",
						"data": map[string]string{
							"tls.crt": "CERT_DATA",
							"tls.key": privateKeyPEM,
						},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer mockK8sServer.Close()

	client, err := NewRESTClient(ClientConfig{
		BaseURL: mockK8sServer.URL,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	// Fetch secrets using the read-only client
	ctx := context.Background()
	secrets, err := client.ListSecretsMetadata(ctx, "default")
	if err != nil {
		t.Fatalf("failed to list secrets metadata: %v", err)
	}

	if len(secrets) != 2 {
		t.Fatalf("expected 2 secrets, got %d", len(secrets))
	}

	// Verify SecretMetadata contains only key names, not values
	sec := secrets[0]
	if sec.ObjectMeta.Name != "db-credentials" {
		t.Errorf("expected secret name 'db-credentials', got %q", sec.ObjectMeta.Name)
	}
	if len(sec.KeyNames) != 3 {
		t.Errorf("expected 3 key names, got %d: %v", len(sec.KeyNames), sec.KeyNames)
	}

	// Normalize to Evidence
	normalizer := NewNormalizer("test-cluster", "test-workspace", "test-collector")
	evidenceList := normalizer.NormalizeSecret(&sec)

	if len(evidenceList) != 1 {
		t.Fatalf("expected 1 evidence record, got %d", len(evidenceList))
	}

	ev := evidenceList[0]

	// CRITICAL HARD ASSERTION: Secret material must NEVER be present anywhere in the Evidence
	if bytes.Contains(ev.Data, []byte(superSecretPassword)) {
		t.Fatalf("SECURITY VIOLATION: secret password leaked into Evidence.Data: %s", string(ev.Data))
	}
	if bytes.Contains(ev.Data, []byte(privateKeyPEM)) {
		t.Fatalf("SECURITY VIOLATION: private key PEM leaked into Evidence.Data: %s", string(ev.Data))
	}
	if bytes.Contains(ev.Data, []byte(serviceAccountToken)) {
		t.Fatalf("SECURITY VIOLATION: service account token leaked into Evidence.Data: %s", string(ev.Data))
	}

	// Check that only key names are present in JSON
	var parsedData map[string]any
	if err := json.Unmarshal(ev.Data, &parsedData); err != nil {
		t.Fatalf("failed to parse evidence JSON: %v", err)
	}

	if parsedData["name"] != "db-credentials" {
		t.Errorf("expected name 'db-credentials', got %v", parsedData["name"])
	}
	keyNames, ok := parsedData["key_names"].([]any)
	if !ok || len(keyNames) != 3 {
		t.Errorf("expected key_names list of length 3, got: %v", parsedData["key_names"])
	}
}

func TestSecretSafety_WorkloadReferencesNameOnly(t *testing.T) {
	dep := &Deployment{
		ObjectMeta: ObjectMeta{
			Name:      "web-app",
			Namespace: "production",
		},
		Spec: DeploymentSpec{
			Template: PodTemplateSpec{
				Spec: PodSpec{
					Containers: []Container{
						{
							Name:  "web",
							Image: "nginx:1.25",
							Env: []EnvVar{
								{
									Name: "DATABASE_PASSWORD",
									ValueFrom: &EnvVarSource{
										SecretKeyRef: &SecretKeySelector{
											Name: "db-secret",
											Key:  "password",
										},
									},
								},
							},
						},
					},
					Volumes: []Volume{
						{
							Name: "tls-certs",
							Secret: &SecretVolumeSource{
								SecretName: "web-tls-cert",
							},
						},
					},
				},
			},
		},
	}

	normalizer := NewNormalizer("cluster-1", "ws-1", "k8s-collector")
	evs := normalizer.NormalizeDeployment(dep)

	// Verify all secret references only reference the name of the secret
	foundSecretRef := false
	for _, ev := range evs {
		if ev.ObservationType == ObservationResourceReference {
			var data map[string]any
			_ = json.Unmarshal(ev.Data, &data)
			if data["reference_type"] == "secret_ref" {
				foundSecretRef = true
				target, ok := data["target"].(map[string]any)
				if !ok {
					t.Fatalf("malformed target in secret_ref: %v", data)
				}
				if target["resource_type"] != "secret" {
					t.Errorf("expected resource_type secret, got %v", target["resource_type"])
				}
				providerID := target["provider_id"].(string)
				if !strings.HasSuffix(providerID, "/production/db-secret") && !strings.HasSuffix(providerID, "/production/web-tls-cert") {
					t.Errorf("unexpected target provider_id: %s", providerID)
				}
			}
		}
	}

	if !foundSecretRef {
		t.Errorf("expected to find secret_ref evidence for deployment")
	}
}
