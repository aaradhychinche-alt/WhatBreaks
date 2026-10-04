package auth

import (
	"crypto/subtle"
)

const (
	WorkerRole  = "admin"
	WorkerEmail = "worker@internal"
)

// ConstantTimeCompare performs a timing-attack-resistant comparison of two strings.
// It verifies length equality before delegating to subtle.ConstantTimeCompare.
func ConstantTimeCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// WorkerAuthenticator validates internal worker requests using timing-safe bearer token matching.
type WorkerAuthenticator interface {
	AuthenticateBearer(token string) bool
}

// TokenValidator evaluates client authorization credentials.
type TokenValidator interface {
	ValidateToken(token string) (bool, error)
}

// SimpleWorkerAuthenticator is a reference implementation matching internal-worker-auth.js.
type SimpleWorkerAuthenticator struct {
	expectedKey string
}

// NewSimpleWorkerAuthenticator creates a WorkerAuthenticator for the configured key.
func NewSimpleWorkerAuthenticator(expectedKey string) *SimpleWorkerAuthenticator {
	return &SimpleWorkerAuthenticator{
		expectedKey: expectedKey,
	}
}

func (a *SimpleWorkerAuthenticator) AuthenticateBearer(token string) bool {
	if a.expectedKey == "" || token == "" {
		return false
	}
	return ConstantTimeCompare(token, a.expectedKey)
}
