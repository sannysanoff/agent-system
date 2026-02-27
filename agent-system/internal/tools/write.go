package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteTool implements file writing functionality
type WriteTool struct{}

// WriteParams represents parameters for file writing
type WriteParams struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// NewWriteTool creates a new write tool
func NewWriteTool() *WriteTool {
	return &WriteTool{}
}

func (t *WriteTool) Name() string {
	return "write"
}

func (t *WriteTool) Description() string {
	return "Writes a file to the local filesystem. This tool will overwrite the existing file if there is one at the provided path. " +
		"Always prefer editing existing files in the codebase. NEVER create files unless they're absolutely necessary."
}

func (t *WriteTool) Schema() *ToolSchema {
	return &ToolSchema{
		Type: "object",
		Properties: map[string]Property{
			"file_path": {
				Type:        "string",
				Description: "The absolute path to the file to write (must be absolute, not relative)",
			},
			"content": {
				Type:        "string",
				Description: "The content to write to the file",
			},
		},
		Required: []string{"file_path", "content"},
	}
}

func (t *WriteTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var writeParams WriteParams
	if err := json.Unmarshal(params, &writeParams); err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse parameters: %v", err),
		}, nil
	}

	// Validate parameters
	if writeParams.FilePath == "" {
		return ToolResult{
			Success: false,
			Error:   "file_path is required",
		}, nil
	}

	// Convert to absolute path
	absPath, err := filepath.Abs(writeParams.FilePath)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to resolve path: %v", err),
		}, nil
	}

	// Check if file already exists
	fileExists := false
	if _, err := os.Stat(absPath); err == nil {
		fileExists = true
	}

	// Create parent directories if they don't exist
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to create parent directories: %v", err),
		}, nil
	}

	// Write the file
	if err := os.WriteFile(absPath, []byte(writeParams.Content), 0644); err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to write file: %v", err),
		}, nil
	}

	action := "created"
	if fileExists {
		action = "overwritten"
	}

	return ToolResult{
		Success: true,
		Output:  fmt.Sprintf("File %s: %s", action, absPath),
		Data: map[string]interface{}{
			"path":          absPath,
			"action":        action,
			"bytes_written": len(writeParams.Content),
			"existed":       fileExists,
		},
	}, nil
}
