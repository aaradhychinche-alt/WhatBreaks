package logging

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestScrub_IsSensitiveKey(t *testing.T) {
	sensitive := []string{
		"password",
		"passwd",
		"db_password",
		"secret",
		"client_secret",
		"apiKey",
		"api_key",
		"accessKey",
		"access_key",
		"authorization",
		"cookie",
		"credential",
		"credentials",
		"privateKey",
		"private_key",
		"token",
		"session_token",
		"roleId",
		"role_id",
	}
	for _, key := range sensitive {
		if !IsSensitiveKey(key) {
			t.Errorf("expected key %q to be identified as sensitive", key)
		}
	}

	nonSensitive := []string{
		"id",
		"name",
		"service",
		"timestamp",
		"level",
		"message",
		"count",
		"status",
		"hostname",
	}
	for _, key := range nonSensitive {
		if IsSensitiveKey(key) {
			t.Errorf("expected key %q NOT to be identified as sensitive", key)
		}
	}
}

func TestScrub_ScrubLogString(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "innocuous text",
			input:    "Service started on port 8080",
			expected: "Service started on port 8080",
		},
		{
			name:     "bearer token embedded in sentence",
			input:    "Received request with Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.test and completed",
			expected: "Received request with Authorization: [REDACTED] and completed",
		},
		{
			name:     "cookie in log message",
			input:    "Session header Cookie: session_id=abc123xyz rejected",
			expected: "Session header Cookie: [REDACTED]",
		},
		{
			name:     "password assignment embedded",
			input:    "Database connect failed with password=supersecretpass at host db.local",
			expected: "Database connect failed with password=[REDACTED] at host db.local",
		},
		{
			name:     "embedded RSA private key PEM",
			input:    "Failed to load key: -----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0Y1...\n-----END RSA PRIVATE KEY----- from disk",
			expected: "Failed to load key: [PRIVATE_KEY_REDACTED] from disk",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScrubLogString(tc.input)
			if got != tc.expected {
				t.Fatalf("ScrubLogString(%q)\ngot:  %q\nwant: %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestScrub_ScrubBuffer(t *testing.T) {
	if got := ScrubBuffer(nil); got != nil {
		t.Errorf("expected nil for nil buffer, got %v", got)
	}

	// Innocuous binary buffer fails closed to GenericSecretRedactionPlaceholder
	buf := []byte{0x01, 0x02, 0x03, 0x04}
	if got := ScrubBuffer(buf); got != GenericSecretRedactionPlaceholder {
		t.Errorf("expected %s for binary buffer, got %v", GenericSecretRedactionPlaceholder, got)
	}

	// Buffer with private key
	keyBuf := []byte(testRsaPrivateKeyPEM)
	if got := ScrubBuffer(keyBuf); got != PrivateKeyRedactionPlaceholder {
		t.Errorf("expected %s for private key buffer, got %v", PrivateKeyRedactionPlaceholder, got)
	}
}

func TestScrub_ResolveClientIP(t *testing.T) {
	cases := []struct {
		input    any
		expected any
	}{
		{input: nil, expected: nil},
		{input: "192.168.1.1", expected: "192.168.1.1"},
		{input: "10.0.0.1, 192.168.1.1, 172.16.0.1", expected: "10.0.0.1"},
		{input: "  10.0.0.2 , 127.0.0.1", expected: "10.0.0.2"},
		{input: "", expected: nil},
		{input: "   ", expected: nil},
		{input: 12345, expected: 12345},
	}

	for _, tc := range cases {
		got := ResolveClientIP(tc.input)
		if got != tc.expected {
			t.Errorf("ResolveClientIP(%v) = %v, want %v", tc.input, got, tc.expected)
		}
	}
}

func TestScrub_RedactSensitiveFields(t *testing.T) {
	// 1. Primitive types pass through unchanged
	if got := RedactSensitiveFields(123); got != 123 {
		t.Errorf("expected 123, got %v", got)
	}
	if got := RedactSensitiveFields(true); got != true {
		t.Errorf("expected true, got %v", got)
	}
	if got := RedactSensitiveFields(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}

	// 2. Map with sensitive keys
	inputMap := map[string]any{
		"username": "alice",
		"password": "super-secret-password",
		"apiKey":   "key-12345",
		"metadata": map[string]any{
			"env":          "production",
			"db_password":  "db-pass-999",
			"normal_field": "visible",
		},
	}

	redacted := RedactSensitiveFields(inputMap).(map[string]any)
	if redacted["username"] != "alice" {
		t.Errorf("expected username alice, got %v", redacted["username"])
	}
	if redacted["password"] != GenericSecretRedactionPlaceholder {
		t.Errorf("expected password redacted, got %v", redacted["password"])
	}
	if redacted["apiKey"] != GenericSecretRedactionPlaceholder {
		t.Errorf("expected apiKey redacted, got %v", redacted["apiKey"])
	}

	nested := redacted["metadata"].(map[string]any)
	if nested["env"] != "production" {
		t.Errorf("expected env production, got %v", nested["env"])
	}
	if nested["db_password"] != GenericSecretRedactionPlaceholder {
		t.Errorf("expected db_password redacted, got %v", nested["db_password"])
	}
	if nested["normal_field"] != "visible" {
		t.Errorf("expected normal_field visible, got %v", nested["normal_field"])
	}

	// 3. Arrays and nested slices
	sliceInput := []any{
		"normal",
		map[string]any{
			"token": "secret-token-xyz",
			"id":    42,
		},
		"Authorization: Bearer secret-auth-token",
	}
	redactedSlice := RedactSensitiveFields(sliceInput).([]any)
	if redactedSlice[0] != "normal" {
		t.Errorf("expected normal, got %v", redactedSlice[0])
	}
	mItem := redactedSlice[1].(map[string]any)
	if mItem["token"] != GenericSecretRedactionPlaceholder {
		t.Errorf("expected token redacted, got %v", mItem["token"])
	}
	if mItem["id"] != 42 {
		t.Errorf("expected id 42, got %v", mItem["id"])
	}
	if redactedSlice[2] != "Authorization: [REDACTED]" {
		t.Errorf("expected bearer redacted, got %v", redactedSlice[2])
	}
}

func TestScrub_ErrorSerialization(t *testing.T) {
	err := errors.New("connection failed: password=secretpass on db.local")
	redactedErr := RedactSensitiveFields(err).(map[string]any)

	if redactedErr["name"] != "Error" {
		t.Errorf("expected name Error, got %v", redactedErr["name"])
	}
	msg, ok := redactedErr["message"].(string)
	if !ok {
		t.Fatalf("expected string message, got %T", redactedErr["message"])
	}
	if strings.Contains(msg, "secretpass") {
		t.Errorf("error message leaked password: %s", msg)
	}
	if !strings.Contains(msg, "password=[REDACTED]") {
		t.Errorf("expected password=[REDACTED] in error message: %s", msg)
	}
}

func TestScrub_CircularReferences(t *testing.T) {
	// Circular map reference
	circularMap := make(map[string]any)
	circularMap["name"] = "loop"
	circularMap["self"] = circularMap

	redacted := RedactSensitiveFields(circularMap).(map[string]any)
	if redacted["name"] != "loop" {
		t.Errorf("expected loop, got %v", redacted["name"])
	}
	if redacted["self"] != "[REDACTED:circular]" {
		t.Errorf("expected circular reference marker, got %v", redacted["self"])
	}

	// Circular slice reference
	circularSlice := make([]any, 2)
	circularSlice[0] = "item"
	circularSlice[1] = circularSlice

	redactedSlice := RedactSensitiveFields(circularSlice).([]any)
	if redactedSlice[0] != "item" {
		t.Errorf("expected item, got %v", redactedSlice[0])
	}
	if redactedSlice[1] != "[REDACTED:circular]" {
		t.Errorf("expected circular reference marker, got %v", redactedSlice[1])
	}
}

func TestScrub_MaxDepth(t *testing.T) {
	// Nest beyond MaxScrubDepth (8)
	curr := make(map[string]any)
	root := curr
	for i := 0; i < MaxScrubDepth+3; i++ {
		next := make(map[string]any)
		curr["child"] = next
		curr = next
	}
	curr["secret"] = "deep-secret"

	redacted := RedactSensitiveFields(root)
	// Traverse to depth 8
	var node any = redacted
	for i := 0; i < MaxScrubDepth; i++ {
		m, ok := node.(map[string]any)
		if !ok {
			break
		}
		node = m["child"]
	}
	// At max depth, it should be redacted placeholder
	if node != GenericSecretRedactionPlaceholder {
		t.Errorf("expected max-depth node to be %s, got %v", GenericSecretRedactionPlaceholder, node)
	}
}

func TestScrub_SanitizeLogRecord(t *testing.T) {
	record := map[string]any{
		"level":    "info",
		"service":  "wb-controller",
		"message":  "User authenticated with token passwd=pass12345",
		"password": "raw-password-in-top-level",
		"ip":       "192.168.1.100, 10.0.0.1",
		"meta": map[string]any{
			"authorization": "Bearer token123",
		},
	}

	sanitized := SanitizeLogRecord(record)

	// In record: "message" should NOT be replaced outright with [REDACTED], but its content scrubbed
	msg := sanitized["message"].(string)
	if strings.Contains(msg, "pass12345") {
		t.Errorf("expected password in message to be scrubbed: %s", msg)
	}
	if !strings.Contains(msg, "passwd=[REDACTED]") {
		t.Errorf("expected passwd=[REDACTED] in message: %s", msg)
	}

	// Top-level "password" key must be replaced unconditionally with [REDACTED]
	if sanitized["password"] != GenericSecretRedactionPlaceholder {
		t.Errorf("expected top-level password to be redacted: %v", sanitized["password"])
	}

	// Client IP must be resolved to first IP
	if sanitized["ip"] != "192.168.1.100" {
		t.Errorf("expected first IP 192.168.1.100, got %v", sanitized["ip"])
	}

	// Nested sensitive key must be redacted
	nested := sanitized["meta"].(map[string]any)
	if nested["authorization"] != GenericSecretRedactionPlaceholder {
		t.Errorf("expected nested authorization to be redacted: %v", nested["authorization"])
	}
}

func TestScrub_StructReflectScrubbing(t *testing.T) {
	type TestUser struct {
		Username string
		Password string
		Secret   string
		Age      int
	}

	user := TestUser{
		Username: "bob",
		Password: "supersecretpassword",
		Secret:   "my-api-secret",
		Age:      30,
	}

	redacted := RedactSensitiveFields(user).(map[string]any)
	if redacted["Username"] != "bob" {
		t.Errorf("expected bob, got %v", redacted["Username"])
	}
	if redacted["Password"] != GenericSecretRedactionPlaceholder {
		t.Errorf("expected password redacted, got %v", redacted["Password"])
	}
	if redacted["Secret"] != GenericSecretRedactionPlaceholder {
		t.Errorf("expected secret redacted, got %v", redacted["Secret"])
	}
	if !reflect.DeepEqual(redacted["Age"], 30) {
		t.Errorf("expected age 30, got %v", redacted["Age"])
	}
}
