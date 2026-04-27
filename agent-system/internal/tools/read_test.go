package tools

import (
	"testing"
)

func TestTruncateLongLine(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "short line - no truncation",
			input:    "short line",
			expected: "short line",
		},
		{
			name:     "exactly at threshold - no truncation",
			input:    string(make([]byte, 5*1024)), // exactly 5KB
			expected: string(make([]byte, 5*1024)),
		},
		{
			name:  "long line - truncation",
			input: string(make([]byte, 6000)), // 6000 chars > 5KB
			expected: string(make([]byte, 4*1024)) + "...(middle of line truncated, full length=6000 chars)..." +
				string(make([]byte, 1*1024)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateLongLine(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("truncateLongLine() length = %d, want %d", len(result), len(tt.expected))
			}
			// Check for truncation indicator in long lines
			if len(tt.input) > singleLineThreshold {
				if !contains(result, "...(middle of line truncated") {
					t.Errorf("truncateLongLine() missing truncation indicator for long line")
				}
				if !contains(result, "full length=") {
					t.Errorf("truncateLongLine() missing length info for long line")
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
