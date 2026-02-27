package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadTool implements file reading functionality
type ReadTool struct {
	maxLines  int
	chunkSize int
}

// ReadParams represents parameters for file reading
type ReadParams struct {
	FilePath string `json:"file_path"`
	Offset   *int   `json:"offset,omitempty"`
	Limit    *int   `json:"limit,omitempty"`
	Pages    string `json:"pages,omitempty"` // For PDF files: "1-5", "3", "10-20"
}

// NewReadTool creates a new read tool
func NewReadTool(maxLines, chunkSize int) *ReadTool {
	if maxLines == 0 {
		maxLines = 2000
	}
	if chunkSize == 0 {
		chunkSize = 200
	}
	return &ReadTool{
		maxLines:  maxLines,
		chunkSize: chunkSize,
	}
}

func (t *ReadTool) Name() string {
	return "read"
}

func (t *ReadTool) Description() string {
	return "Reads a file from the local filesystem. You can access any file directly by using this tool. " +
		"Assume this tool is able to read all files on the machine. It is okay to read a file that does not exist; " +
		"an error will be returned. Supports reading specific line ranges."
}

func (t *ReadTool) Schema() *ToolSchema {
	return &ToolSchema{
		Type: "object",
		Properties: map[string]Property{
			"file_path": {
				Type:        "string",
				Description: "The absolute path to the file to read",
			},
			"offset": {
				Type:        "integer",
				Description: "The line number to start reading from (1-indexed). Only provide if the file is too large to read at once.",
			},
			"limit": {
				Type:        "integer",
				Description: "The number of lines to read. Only provide if the file is too large to read at once.",
			},
			"pages": {
				Type:        "string",
				Description: "Page range for PDF files (e.g., '1-5', '3', '10-20'). Only applicable to PDF files. Maximum 20 pages per request.",
			},
		},
		Required: []string{"file_path"},
	}
}

func (t *ReadTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var readParams ReadParams
	if err := json.Unmarshal(params, &readParams); err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse parameters: %v", err),
		}, nil
	}

	// Validate file path
	if readParams.FilePath == "" {
		return ToolResult{
			Success: false,
			Error:   "file_path is required",
		}, nil
	}

	// Convert to absolute path
	absPath, err := filepath.Abs(readParams.FilePath)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to resolve path: %v", err),
		}, nil
	}

	// Check if file exists
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ToolResult{
				Success: false,
				Error:   fmt.Sprintf("file does not exist: %s", absPath),
			}, nil
		}
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to access file: %v", err),
		}, nil
	}

	// Handle directories
	if info.IsDir() {
		return t.readDirectory(absPath)
	}

	// Handle PDF files
	ext := strings.ToLower(filepath.Ext(absPath))
	if ext == ".pdf" && readParams.Pages != "" {
		return t.readPDFFile(absPath, readParams.Pages)
	}

	// Handle regular files
	return t.readTextFile(absPath, readParams)
}

func (t *ReadTool) readDirectory(path string) (ToolResult, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to read directory: %v", err),
		}, nil
	}

	var output strings.Builder
	output.WriteString(fmt.Sprintf("Directory: %s\n\n", path))

	for _, entry := range entries {
		prefix := "  "
		if entry.IsDir() {
			prefix = "📁 "
		} else {
			prefix = "📄 "
		}
		output.WriteString(fmt.Sprintf("%s%s\n", prefix, entry.Name()))
	}

	return ToolResult{
		Success: true,
		Output:  output.String(),
		Data: map[string]interface{}{
			"type":        "directory",
			"path":        path,
			"entry_count": len(entries),
		},
	}, nil
}

func (t *ReadTool) readTextFile(path string, params ReadParams) (ToolResult, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to read file: %v", err),
		}, nil
	}

	// Convert to lines
	lines := strings.Split(string(content), "\n")
	totalLines := len(lines)

	// Determine offset and limit
	offset := 1
	if params.Offset != nil {
		offset = *params.Offset
	}

	limit := t.maxLines
	if params.Limit != nil {
		limit = *params.Limit
	}

	// Validate range
	if offset < 1 {
		offset = 1
	}
	if offset > totalLines {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("offset %d exceeds file length of %d lines", offset, totalLines),
		}, nil
	}

	// Calculate end
	end := offset + limit - 1
	if end > totalLines {
		end = totalLines
	}

	// Extract lines
	var output strings.Builder
	for i := offset - 1; i < end; i++ {
		lineNum := i + 1
		output.WriteString(fmt.Sprintf("%d: %s\n", lineNum, lines[i]))
	}

	result := ToolResult{
		Success: true,
		Output:  output.String(),
		Data: map[string]interface{}{
			"type":        "file",
			"path":        path,
			"total_lines": totalLines,
			"offset":      offset,
			"limit":       limit,
			"lines_read":  end - offset + 1,
		},
	}

	// Add truncation notice
	if end < totalLines {
		result.Data.(map[string]interface{})["truncated"] = true
		result.Data.(map[string]interface{})["remaining_lines"] = totalLines - end
	}

	return result, nil
}

func (t *ReadTool) readPDFFile(path string, pages string) (ToolResult, error) {
	// For now, return an error indicating PDF parsing is not implemented
	// In a full implementation, you'd use a PDF library like pdfcpu or unidoc
	return ToolResult{
		Success: false,
		Error:   "PDF reading not yet implemented. Use the file_path parameter to read other file types.",
	}, nil
}
