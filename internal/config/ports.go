package config

import (
	"errors"
	"strconv"
	"strings"
)

var (
	ErrInvalidPort = errors.New("port must be an integer between 1 and 65535")
)

// ValidatePort validates that the given port is within the valid TCP port range [1, 65535].
func ValidatePort(port int) error {
	if port < 1 || port > 65535 {
		return ErrInvalidPort
	}
	return nil
}

// ParsePort parses a port string and validates that it falls within [1, 65535].
// An empty value returns the default port.
// Non-numeric or out-of-range values return a ControllerConfigError.
func ParsePort(value string, field string, defaultPort int) (int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return defaultPort, nil
	}

	// Must contain only digits
	for _, ch := range trimmed {
		if ch < '0' || ch > '9' {
			return 0, &ControllerConfigError{
				Code:  CodeInvalidPort,
				Field: field,
			}
		}
	}

	port, err := strconv.Atoi(trimmed)
	if err != nil || port < 1 || port > 65535 {
		return 0, &ControllerConfigError{
			Code:  CodeInvalidPort,
			Field: field,
		}
	}

	return port, nil
}
