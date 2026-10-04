package auth

import (
	"testing"
)

func TestConstantTimeCompare(t *testing.T) {
	cases := []struct {
		name     string
		a        string
		b        string
		expected bool
	}{
		{
			name:     "identical strings",
			a:        "secret-key-12345",
			b:        "secret-key-12345",
			expected: true,
		},
		{
			name:     "different lengths",
			a:        "secret",
			b:        "secret-key",
			expected: false,
		},
		{
			name:     "same length different content",
			a:        "secret-key-1",
			b:        "secret-key-2",
			expected: false,
		},
		{
			name:     "both empty",
			a:        "",
			b:        "",
			expected: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ConstantTimeCompare(tc.a, tc.b)
			if got != tc.expected {
				t.Fatalf("ConstantTimeCompare(%q, %q) = %v; expected %v", tc.a, tc.b, got, tc.expected)
			}
		})
	}
}

func TestSimpleWorkerAuthenticator(t *testing.T) {
	auth := NewSimpleWorkerAuthenticator("correct-worker-secret")

	if !auth.AuthenticateBearer("correct-worker-secret") {
		t.Errorf("expected bearer token to authenticate successfully")
	}

	if auth.AuthenticateBearer("wrong-worker-secret") {
		t.Errorf("expected wrong token to fail authentication")
	}

	if auth.AuthenticateBearer("") {
		t.Errorf("expected empty token to fail authentication")
	}
}
