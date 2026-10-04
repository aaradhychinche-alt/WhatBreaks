package logging

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

const (
	MaxScrubDepth = 8
)

var (
	redactKeyPattern = regexp.MustCompile(`(?i)password|secret|api[-_]?key|access[-_]?key|authorization|cookie|credential|private[-_]?key|token|role[-_]?id`)

	// LogFieldOrder defines the deterministic field ordering for JSON log output.
	LogFieldOrder = []string{"level", "message", "service", "timestamp"}

	// RedactFields lists canonical sensitive field names.
	RedactFields = []string{
		"password",
		"token",
		"secret",
		"apiKey",
		"api_key",
		"accessKey",
		"access_key",
		"accessKeyId",
		"access_key_id",
		"secretAccessKey",
		"secret_access_key",
		"sessionToken",
		"session_token",
		"authorization",
		"cookie",
		"credentials",
		"privateKey",
		"private_key",
		"client_secret",
		"clientSecret",
		"roleId",
		"role_id",
		"secretId",
		"secret_id",
	}
)

// SafeErrorName returns a safe, un-tainted error name.
func SafeErrorName(err error) string {
	if err == nil {
		return ""
	}
	return "Error"
}

// IsSensitiveKey reports whether a metadata key matches sensitive field patterns.
func IsSensitiveKey(key string) bool {
	return redactKeyPattern.MatchString(key) || FieldNameLooksGenericSecret(key) || FieldNameLooksPrivateKeyMaterial(key)
}

// ScrubLogString scrubs private-key material and generic secrets from free-form text.
// If any panic occurs, it fails closed returning [REDACTED].
func ScrubLogString(value string) string {
	if len(value) == 0 {
		return value
	}

	defer func() {
		if r := recover(); r != nil {
			// Fail-closed
		}
	}()

	redactedKey, ok := RedactPrivateKeyMaterial(value).(string)
	if !ok {
		return GenericSecretRedactionPlaceholder
	}

	if ContainsPrivateKeyMaterial(redactedKey) {
		return PrivateKeyRedactionPlaceholder
	}

	redactedGeneric, ok := RedactGenericSecrets(redactedKey).(string)
	if !ok {
		return GenericSecretRedactionPlaceholder
	}

	return redactedGeneric
}

// ScrubBuffer applies fail-closed sanitization to binary data.
func ScrubBuffer(value []byte) any {
	if value == nil {
		return nil
	}
	if ContainsPrivateKeyMaterial(value) {
		return PrivateKeyRedactionPlaceholder
	}
	return GenericSecretRedactionPlaceholder
}

// ResolveClientIP normalizes client IP from string or metadata, extracting the first comma-delimited IP.
func ResolveClientIP(val any) any {
	if val == nil {
		return nil
	}
	s, ok := val.(string)
	if !ok {
		return val
	}
	parts := strings.Split(s, ",")
	first := strings.TrimSpace(parts[0])
	if first == "" {
		return nil
	}
	return first
}

// RedactSensitiveFields deep-scans and redacts data structures up to MaxScrubDepth.
func RedactSensitiveFields(value any) any {
	return redactSensitiveFieldsInternal(value, 0, make(map[uintptr]struct{}))
}

func redactSensitiveFieldsInternal(value any, depth int, seen map[uintptr]struct{}) any {
	if value == nil {
		return nil
	}
	if depth >= MaxScrubDepth {
		return GenericSecretRedactionPlaceholder
	}

	switch v := value.(type) {
	case string:
		return ScrubLogString(v)
	case []byte:
		return ScrubBuffer(v)
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return v
	case error:
		return map[string]any{
			"name":    "Error",
			"message": ScrubLogString(v.Error()),
		}
	case []any:
		valPtr := reflect.ValueOf(v).Pointer()
		if valPtr != 0 {
			if _, exists := seen[valPtr]; exists {
				return "[REDACTED:circular]"
			}
			seen[valPtr] = struct{}{}
			defer delete(seen, valPtr)
		}
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactSensitiveFieldsInternal(item, depth+1, seen)
		}
		return out
	case map[string]any:
		valPtr := reflect.ValueOf(v).Pointer()
		if valPtr != 0 {
			if _, exists := seen[valPtr]; exists {
				return "[REDACTED:circular]"
			}
			seen[valPtr] = struct{}{}
			defer delete(seen, valPtr)
		}
		out := make(map[string]any, len(v))
		for k, item := range v {
			if IsSensitiveKey(k) {
				out[k] = GenericSecretRedactionPlaceholder
			} else {
				out[k] = redactSensitiveFieldsInternal(item, depth+1, seen)
			}
		}
		return out
	}

	// Reflection fallback for arbitrary slices, arrays, maps, and structs
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return redactSensitiveFieldsInternal(rv.Elem().Interface(), depth, seen)
	case reflect.Slice, reflect.Array:
		ptr := rv.Pointer()
		if ptr != 0 {
			if _, exists := seen[ptr]; exists {
				return "[REDACTED:circular]"
			}
			seen[ptr] = struct{}{}
			defer delete(seen, ptr)
		}
		n := rv.Len()
		out := make([]any, n)
		for i := 0; i < n; i++ {
			out[i] = redactSensitiveFieldsInternal(rv.Index(i).Interface(), depth+1, seen)
		}
		return out
	case reflect.Map:
		ptr := rv.Pointer()
		if ptr != 0 {
			if _, exists := seen[ptr]; exists {
				return "[REDACTED:circular]"
			}
			seen[ptr] = struct{}{}
			defer delete(seen, ptr)
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			keyStr := fmt.Sprintf("%v", iter.Key().Interface())
			if IsSensitiveKey(keyStr) {
				out[keyStr] = GenericSecretRedactionPlaceholder
			} else {
				out[keyStr] = redactSensitiveFieldsInternal(iter.Value().Interface(), depth+1, seen)
			}
		}
		return out
	case reflect.Struct:
		// Map struct fields to map[string]any
		t := rv.Type()
		out := make(map[string]any, rv.NumField())
		for i := 0; i < rv.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			fieldName := field.Name
			if IsSensitiveKey(fieldName) {
				out[fieldName] = GenericSecretRedactionPlaceholder
			} else {
				out[fieldName] = redactSensitiveFieldsInternal(rv.Field(i).Interface(), depth+1, seen)
			}
		}
		return out
	default:
		return fmt.Sprintf("%v", value)
	}
}

// SanitizeLogValue sanitizes any value using RedactSensitiveFields.
func SanitizeLogValue(value any) any {
	return RedactSensitiveFields(value)
}

// SanitizeLogRecord sanitizes a top-level log record map.
// The "message" field is scanned for embedded content secrets rather than replaced outright.
// Any other sensitive top-level key is replaced with [REDACTED].
func SanitizeLogRecord(record map[string]any) map[string]any {
	if record == nil {
		return nil
	}

	sanitized := make(map[string]any, len(record))
	for k, v := range record {
		if k == "ip" {
			sanitized[k] = ResolveClientIP(v)
			continue
		}
		if IsSensitiveKey(k) && k != "message" {
			sanitized[k] = GenericSecretRedactionPlaceholder
			continue
		}
		sanitized[k] = SanitizeLogValue(v)
	}
	return sanitized
}
