package config

import (
	"testing"
)

func TestValidatePort(t *testing.T) {
	cases := []struct {
		port    int
		wantErr bool
	}{
		{port: 80, wantErr: false},
		{port: 443, wantErr: false},
		{port: 8081, wantErr: false},
		{port: 65535, wantErr: false},
		{port: 0, wantErr: true},
		{port: -1, wantErr: true},
		{port: 65536, wantErr: true},
	}

	for _, tc := range cases {
		err := ValidatePort(tc.port)
		if tc.wantErr && err == nil {
			t.Errorf("ValidatePort(%d) expected error, got nil", tc.port)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("ValidatePort(%d) unexpected error: %v", tc.port, err)
		}
	}
}
