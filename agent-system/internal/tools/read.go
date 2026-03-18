package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadTool implements file reading functionality
type ReadTool struct {
	readLimit int
}

// ReadParams represents parameters for file reading
type ReadParams struct {
	FilePath string `json:"file_path"`
	Offset   *int   `json:"offset,omitempty"`
}

// NewReadTool creates a new read tool
func NewReadTool(readLimit int) *ReadTool {
	if readLimit == 0 {
		readLimit = 80000
	}
	return &ReadTool{
		readLimit: readLimit,
	}
}

func (t *ReadTool) Name() string {
	return "read"
}

func (t *ReadTool) Description() string {
	return "Reads a file from the local filesystem. You can access any file directly by using this tool. " +
		"Assume this tool is able to read all files on the machine. It is okay to read a file that does not exist; " +
		"an error will be returned."
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
	file, err := os.Open(path)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to open file: %v", err),
		}, nil
	}
	defer file.Close()

	offset := 1
	if params.Offset != nil {
		offset = *params.Offset
	}

	var output strings.Builder
	scanner := bufio.NewScanner(file)
	currentLine := 0
	totalLines := 0
	linesReadStart := -1
	linesReadEnd := -1
	bytesRead := 0
	limitReached := false

	for scanner.Scan() {
		totalLines++
		currentLine = totalLines
		line := scanner.Text()

		if currentLine < offset {
			continue
		}

		lineLen := len(line) + 1 // +1 for newline
		if bytesRead+lineLen > t.readLimit {
			limitReached = true
			// Don't break yet, we want to count total lines
			continue
		}

		if !limitReached {
			if linesReadStart == -1 {
				linesReadStart = currentLine
			}
			linesReadEnd = currentLine
			output.WriteString(fmt.Sprintf("%d: %s\n", currentLine, line))
			bytesRead += lineLen
		}
	}

	if err := scanner.Err(); err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("error reading file: %v", err),
		}, nil
	}

	if linesReadStart == -1 && offset <= totalLines {
		// This could happen if the first line we try to read is already larger than the limit
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("line %d is too large to read (limit: %d bytes)", offset, t.readLimit),
		}, nil
	}

	summary := fmt.Sprintf("lines read: %d-%d, total lines: %d, per-read limit: %d bytes",
		linesReadStart, linesReadEnd, totalLines, t.readLimit)

	if linesReadStart == -1 {
		summary = fmt.Sprintf("no lines read, total lines: %d, per-read limit: %d bytes", totalLines, t.readLimit)
	}

	resultOutput := output.String()
	if resultOutput != "" {
		resultOutput += "\n" + summary
	} else {
		resultOutput = summary
	}

	return ToolResult{
		Success: true,
		Output:  resultOutput,
		Data: map[string]interface{}{
			"type":             "file",
			"path":             path,
			"total_lines":      totalLines,
			"offset":           offset,
			"lines_read_start": linesReadStart,
			"lines_read_end":   linesReadEnd,
			"bytes_read":       bytesRead,
			"read_limit":       t.readLimit,
		},
	}, nil
}
