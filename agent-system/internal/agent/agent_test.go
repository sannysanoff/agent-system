package agent

import (
	"testing"

	"agent-system/internal/config"
)

func TestComputeEnabledTools(t *testing.T) {
	// Test with CLI override
	enabled := computeEnabledTools([]string{"bash", "read"}, &config.AgentConfig{})
	if len(enabled) != 2 {
		t.Errorf("Expected 2 tools, got %d", len(enabled))
	}
	if !contains(enabled, "bash") || !contains(enabled, "read") {
		t.Error("Expected bash and read to be enabled")
	}

	// Test with "none"
	enabled = computeEnabledTools([]string{"none"}, &config.AgentConfig{})
	if len(enabled) != 0 {
		t.Errorf("Expected 0 tools with 'none', got %d", len(enabled))
	}

	// Test with "empty"
	enabled = computeEnabledTools([]string{"empty"}, &config.AgentConfig{})
	if len(enabled) != 0 {
		t.Errorf("Expected 0 tools with 'empty', got %d", len(enabled))
	}
}

func TestComputeEnabledToolsFromConfig(t *testing.T) {
	cfg := &config.AgentConfig{}
	cfg.Tools.Bash.Enabled = true
	cfg.Tools.Read.Enabled = true
	cfg.Tools.Write.Enabled = false
	cfg.Tools.Glob.Enabled = false
	cfg.Tools.Grep.Enabled = false
	cfg.Tools.AskUser.Enabled = false
	cfg.Tools.WebFetch.Enabled = false
	cfg.Tools.WebSearch.Enabled = false
	cfg.Tools.Edit.Enabled = false

	enabled := computeEnabledTools(nil, cfg)

	// Should have bash, read, task, skill
	if len(enabled) != 4 {
		t.Errorf("Expected 4 tools (bash, read + task, skill), got %d: %v", len(enabled), enabled)
	}
	if !contains(enabled, "bash") {
		t.Error("Expected bash to be enabled")
	}
	if !contains(enabled, "read") {
		t.Error("Expected read to be enabled")
	}
	if !contains(enabled, "task") {
		t.Error("Expected task to be enabled")
	}
	if !contains(enabled, "skill") {
		t.Error("Expected skill to be enabled")
	}
	if contains(enabled, "glob") {
		t.Error("Expected glob to NOT be enabled")
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
