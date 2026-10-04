package database

import (
	"testing"
)

func TestHash32_MatchesJavaScriptAlgorithm(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected int32
	}{
		{
			name:     "empty string",
			input:    "",
			expected: 0,
		},
		{
			name:     "test-lock string",
			input:    "test-lock",
			expected: -1226527354,
		},
		{
			name:     "whatbreaks:discovery string",
			input:    "whatbreaks:discovery",
			expected: -2033551410,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := Hash32(tc.input)
			if got != tc.expected {
				t.Fatalf("Hash32(%q) = %d; expected %d", tc.input, got, tc.expected)
			}
		})
	}
}
