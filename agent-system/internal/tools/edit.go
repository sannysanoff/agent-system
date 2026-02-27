package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EditTool implements file editing functionality
type EditTool struct{}

// EditParams represents parameters for file editing
type EditParams struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// NewEditTool creates a new edit tool
func NewEditTool() *EditTool {
	return &EditTool{}
}

func (t *EditTool) Name() string {
	return "edit"
}

func (t *EditTool) Description() string {
	return "Performs exact string replacements in files. You must use your Read tool at least once in the conversation " +
		"before editing. This tool will error if you attempt to edit without reading the file first. " +
		"When editing text from Read tool output, ensure you preserve the exact indentation (tabs/spaces) as it appears."
}

func (t *EditTool) Schema() *ToolSchema {
	return &ToolSchema{
		Type: "object",
		Properties: map[string]Property{
			"file_path": {
				Type:        "string",
				Description: "The absolute path to the file to modify",
			},
			"old_string": {
				Type:        "string",
				Description: "The text to replace",
			},
			"new_string": {
				Type:        "string",
				Description: "The text to replace it with (must be different from old_string)",
			},
			"replace_all": {
				Type:        "boolean",
				Description: "Replace all occurrences of old_string (default false)",
			},
		},
		Required: []string{"file_path", "old_string", "new_string"},
	}
}

func (t *EditTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var editParams EditParams
	if err := json.Unmarshal(params, &editParams); err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse parameters: %v", err),
		}, nil
	}

	// Validate parameters
	if editParams.FilePath == "" {
		return ToolResult{
			Success: false,
			Error:   "file_path is required",
		}, nil
	}

	if editParams.OldString == "" {
		return ToolResult{
			Success: false,
			Error:   "old_string is required",
		}, nil
	}

	// Convert to absolute path
	absPath, err := filepath.Abs(editParams.FilePath)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to resolve path: %v", err),
		}, nil
	}

	// Read the file
	content, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ToolResult{
				Success: false,
				Error:   fmt.Sprintf("file does not exist: %s", absPath),
			}, nil
		}
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to read file: %v", err),
		}, nil
	}

	originalContent := string(content)

	// Check if old_string exists
	if !strings.Contains(originalContent, editParams.OldString) {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("old_string not found in file: %s", absPath),
		}, nil
	}

	// Count occurrences
	occurrenceCount := strings.Count(originalContent, editParams.OldString)

	// Validate that old_string and new_string are different
	if editParams.OldString == editParams.NewString {
		return ToolResult{
			Success: false,
			Error:   "old_string and new_string must be different",
		}, nil
	}

	// Perform replacement
	var newContent string
	var replacedCount int

	if editParams.ReplaceAll {
		newContent = strings.ReplaceAll(originalContent, editParams.OldString, editParams.NewString)
		replacedCount = occurrenceCount
	} else {
		// If multiple occurrences and not replacing all, check for ambiguity
		if occurrenceCount > 1 {
			return ToolResult{
				Success: false,
				Error:   fmt.Sprintf("found %d occurrences of old_string. Use replace_all=true to replace all, or provide more context in old_string to make it unique", occurrenceCount),
			}, nil
		}
		newContent = strings.Replace(originalContent, editParams.OldString, editParams.NewString, 1)
		replacedCount = 1
	}

	// Write the file back
	if err := os.WriteFile(absPath, []byte(newContent), 0644); err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to write file: %v", err),
		}, nil
	}

	return ToolResult{
		Success: true,
		Output:  fmt.Sprintf("Successfully edited %s (%d replacement(s))", absPath, replacedCount),
		Data: map[string]interface{}{
			"path":            absPath,
			"replacements":    replacedCount,
			"occurrences":     occurrenceCount,
			"replace_all":     editParams.ReplaceAll,
			"original_length": len(originalContent),
			"new_length":      len(newContent),
		},
	}, nil
}
