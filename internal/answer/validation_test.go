package answer_test

import (
	"testing"

	"github.com/aaradhychinche-alt/WhatBreaks/internal/answer"
)

func TestValidateImpactRequest_ProposedChange(t *testing.T) {
	validTarget := &answer.ResourceIdentity{
		Provider:     "kubernetes",
		ResourceType: "pod",
		ProviderID:   "backend-pod",
	}

	tests := []struct {
		name       string
		req        *answer.ImpactRequest
		wantErr    bool
		errField   string
		expectedCT answer.ChangeType
	}{
		{
			name: "no proposed change (backward compatible)",
			req: &answer.ImpactRequest{
				Target:    validTarget,
				Direction: "incoming",
				MaxDepth:  3,
			},
			wantErr: false,
		},
		{
			name: "valid DELETE uppercase",
			req: &answer.ImpactRequest{
				Target:    validTarget,
				Direction: "incoming",
				MaxDepth:  3,
				ProposedChange: &answer.ProposedChange{
					ChangeType: "DELETE",
				},
			},
			wantErr:    false,
			expectedCT: answer.ChangeTypeDelete,
		},
		{
			name: "valid update lowercase normalized",
			req: &answer.ImpactRequest{
				Target:    validTarget,
				Direction: "incoming",
				MaxDepth:  3,
				ProposedChange: &answer.ProposedChange{
					ChangeType: "update",
				},
			},
			wantErr:    false,
			expectedCT: answer.ChangeTypeUpdate,
		},
		{
			name: "valid SCALE mixed case",
			req: &answer.ImpactRequest{
				Target:    validTarget,
				Direction: "incoming",
				MaxDepth:  3,
				ProposedChange: &answer.ProposedChange{
					ChangeType: "Scale",
				},
			},
			wantErr:    false,
			expectedCT: answer.ChangeTypeScale,
		},
		{
			name: "valid REPLACE with details",
			req: &answer.ImpactRequest{
				Target:    validTarget,
				Direction: "incoming",
				MaxDepth:  3,
				ProposedChange: &answer.ProposedChange{
					ChangeType: "REPLACE",
					Details:    "rolling replacement",
				},
			},
			wantErr:    false,
			expectedCT: answer.ChangeTypeReplace,
		},
		{
			name: "empty proposed change struct normalized to nil (backward compatible)",
			req: &answer.ImpactRequest{
				Target:         validTarget,
				Direction:      "incoming",
				MaxDepth:       3,
				ProposedChange: &answer.ProposedChange{},
			},
			wantErr: false,
		},
		{
			name: "empty change type with details rejected",
			req: &answer.ImpactRequest{
				Target:    validTarget,
				Direction: "incoming",
				MaxDepth:  3,
				ProposedChange: &answer.ProposedChange{
					ChangeType: "",
					Details:    "updating database config",
				},
			},
			wantErr:  true,
			errField: "proposed_change.change_type",
		},
		{
			name: "whitespace change type with details rejected",
			req: &answer.ImpactRequest{
				Target:    validTarget,
				Direction: "incoming",
				MaxDepth:  3,
				ProposedChange: &answer.ProposedChange{
					ChangeType: "   ",
					Details:    "updating database config",
				},
			},
			wantErr:  true,
			errField: "proposed_change.change_type",
		},
		{
			name: "unsupported change type REBOOT rejected",
			req: &answer.ImpactRequest{
				Target:    validTarget,
				Direction: "incoming",
				MaxDepth:  3,
				ProposedChange: &answer.ProposedChange{
					ChangeType: "REBOOT",
				},
			},
			wantErr:  true,
			errField: "proposed_change.change_type",
		},
		{
			name: "unsupported change type MIGRATE rejected",
			req: &answer.ImpactRequest{
				Target:    validTarget,
				Direction: "incoming",
				MaxDepth:  3,
				ProposedChange: &answer.ProposedChange{
					ChangeType: "MIGRATE",
				},
			},
			wantErr:  true,
			errField: "proposed_change.change_type",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := answer.ValidateImpactRequest(tc.req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				vErr, ok := err.(*answer.ValidationError)
				if !ok {
					t.Fatalf("expected ValidationError, got %T (%v)", err, err)
				}
				if vErr.Field != tc.errField {
					t.Errorf("expected error field %q, got %q", tc.errField, vErr.Field)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.expectedCT != "" {
					if tc.req.ProposedChange.ChangeType != tc.expectedCT {
						t.Errorf("expected normalized ChangeType %q, got %q", tc.expectedCT, tc.req.ProposedChange.ChangeType)
					}
				}
			}
		})
	}
}
