package auth

const (
	WorkerRole  = "admin"
	WorkerEmail = "worker@internal"
)

// TokenValidator evaluates client authorization credentials.
// Maintained for backward compatibility with Step 5A scaffolding.
type TokenValidator interface {
	ValidateToken(token string) (bool, error)
}

// SimpleWorkerAuthenticator is an alias for TokenWorkerAuthenticator for backward compatibility with Step 5A.
type SimpleWorkerAuthenticator = TokenWorkerAuthenticator

// NewSimpleWorkerAuthenticator creates a WorkerAuthenticator for the configured key.
func NewSimpleWorkerAuthenticator(expectedKey string) *SimpleWorkerAuthenticator {
	return NewWorkerAuthenticator(expectedKey)
}
