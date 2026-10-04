package config

import (
	"testing"
)

func TestValidatePort(t *testing.T) {
	testCases := []struct {
		name    string
		port    int
		wantErr bool
	}{
		{"boundary min valid", 1, false},
		{"standard http", 80, false},
		{"standard https", 443, false},
		{"controller default health", 8080, false},
		{"boundary max valid", 65535, false},
		{"below min", 0, true},
		{"negative", -1, true},
		{"above max", 65536, true},
		{"large invalid", 99999, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePort(tc.port)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidatePort(%d) expected error, got nil", tc.port)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidatePort(%d) unexpected error: %v", tc.port, err)
			}
		})
	}
}

func TestParsePort(t *testing.T) {
	testCases := []struct {
		name         string
		value        string
		defaultPort  int
		expectedPort int
		expectCode   string
	}{
		{"empty string uses default", "", 8080, 8080, ""},
		{"whitespace uses default", "   ", 8080, 8080, ""},
		{"valid port 1", "1", 8080, 1, ""},
		{"valid port 65535", "65535", 8080, 65535, ""},
		{"valid port with whitespace", " 8081 ", 8080, 8081, ""},
		{"port 0 invalid", "0", 8080, 0, CodeInvalidPort},
		{"port 65536 invalid", "65536", 8080, 0, CodeInvalidPort},
		{"negative invalid", "-5", 8080, 0, CodeInvalidPort},
		{"non-numeric invalid", "http", 8080, 0, CodeInvalidPort},
		{"mixed digits and letters", "8080a", 8080, 0, CodeInvalidPort},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePort(tc.value, "TEST_PORT", tc.defaultPort)
			if tc.expectCode != "" {
				if err == nil {
					t.Fatalf("ParsePort(%q) expected error code %s, got nil", tc.value, tc.expectCode)
				}
				cfgErr, ok := err.(*ControllerConfigError)
				if !ok {
					t.Fatalf("expected *ControllerConfigError, got %T: %v", err, err)
				}
				if cfgErr.Code != tc.expectCode {
					t.Fatalf("expected error code %s, got %s", tc.expectCode, cfgErr.Code)
				}
				if cfgErr.Field != "TEST_PORT" {
					t.Fatalf("expected error field TEST_PORT, got %s", cfgErr.Field)
				}
			} else {
				if err != nil {
					t.Fatalf("ParsePort(%q) unexpected error: %v", tc.value, err)
				}
				if got != tc.expectedPort {
					t.Fatalf("ParsePort(%q) = %d; expected %d", tc.value, got, tc.expectedPort)
				}
			}
		})
	}
}
