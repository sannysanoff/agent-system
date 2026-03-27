package prompts

import (
	"strings"
	"testing"
)

func TestGetBuiltInTools(t *testing.T) {
	// Test with no enabled tools (should show all)
	builder := NewSystemPromptBuilder("/tmp", false, nil)
	prompt := builder.getBuiltInTools()

	if !strings.Contains(prompt, "**bash**") {
		t.Error("Should contain bash tool")
	}
	if !strings.Contains(prompt, "**glob**") {
		t.Error("Should contain glob tool")
	}
	if !strings.Contains(prompt, "11.") {
		t.Error("Should have 11 tools when none specified")
	}
}

func TestGetBuiltInToolsFiltered(t *testing.T) {
	// Test with specific enabled tools
	builder := NewSystemPromptBuilder("/tmp", false, []string{"bash", "read", "write"})
	prompt := builder.getBuiltInTools()

	if !strings.Contains(prompt, "**bash**") {
		t.Error("Should contain bash tool")
	}
	if !strings.Contains(prompt, "**read**") {
		t.Error("Should contain read tool")
	}
	if !strings.Contains(prompt, "**write**") {
		t.Error("Should contain write tool")
	}
	if strings.Contains(prompt, "**glob**") {
		t.Error("Should NOT contain glob tool")
	}
	if strings.Contains(prompt, "**grep**") {
		t.Error("Should NOT contain grep tool")
	}
	// Should only have 3 tools numbered
	if strings.Contains(prompt, "4.") {
		t.Error("Should only have 3 tools, not 4")
	}
}

func TestGetBuiltInToolsEmpty(t *testing.T) {
	// Test with empty enabled tools (should show all)
	builder := NewSystemPromptBuilder("/tmp", false, []string{})
	prompt := builder.getBuiltInTools()

	if !strings.Contains(prompt, "**bash**") {
		t.Error("Should contain bash tool when empty list")
	}
	if !strings.Contains(prompt, "**glob**") {
		t.Error("Should contain glob tool when empty list")
	}
}

func TestIsToolEnabled(t *testing.T) {
	builder := NewSystemPromptBuilder("/tmp", false, []string{"bash", "read"})

	if !builder.isToolEnabled("bash") {
		t.Error("bash should be enabled")
	}
	if !builder.isToolEnabled("read") {
		t.Error("read should be enabled")
	}
	if builder.isToolEnabled("glob") {
		t.Error("glob should NOT be enabled")
	}
	if builder.isToolEnabled("write") {
		t.Error("write should NOT be enabled")
	}
}

func TestIsToolEnabledEmptyList(t *testing.T) {
	builder := NewSystemPromptBuilder("/tmp", false, []string{})

	if !builder.isToolEnabled("bash") {
		t.Error("bash should be enabled when list is empty")
	}
	if !builder.isToolEnabled("glob") {
		t.Error("glob should be enabled when list is empty")
	}
}
