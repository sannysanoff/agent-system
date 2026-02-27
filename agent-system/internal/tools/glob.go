package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// GlobTool implements file globbing functionality
type GlobTool struct {
	maxResults int
}

// GlobParams represents parameters for file globbing
type GlobParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

// FileInfo represents information about a matched file
type FileInfo struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"`
	IsDir   bool   `json:"is_dir"`
}

// NewGlobTool creates a new glob tool
func NewGlobTool(maxResults int) *GlobTool {
	if maxResults == 0 {
		maxResults = 1000
	}
	return &GlobTool{
		maxResults: maxResults,
	}
}

func (t *GlobTool) Name() string {
	return "glob"
}

func (t *GlobTool) Description() string {
	return "Fast file pattern matching tool that works with any codebase size. Supports glob patterns like " +
		"'**/*.js' or 'src/**/*.ts'. Returns matching file paths sorted by modification time."
}

func (t *GlobTool) Schema() *ToolSchema {
	return &ToolSchema{
		Type: "object",
		Properties: map[string]Property{
			"pattern": {
				Type:        "string",
				Description: "The glob pattern to match files against (e.g., '**/*.go', 'src/**/*.ts')",
			},
			"path": {
				Type:        "string",
				Description: "The directory to search in. If not specified, the current working directory will be used.",
			},
		},
		Required: []string{"pattern"},
	}
}

func (t *GlobTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var globParams GlobParams
	if err := json.Unmarshal(params, &globParams); err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse parameters: %v", err),
		}, nil
	}

	// Validate parameters
	if globParams.Pattern == "" {
		return ToolResult{
			Success: false,
			Error:   "pattern is required",
		}, nil
	}

	// Determine search path
	searchPath := globParams.Path
	if searchPath == "" {
		searchPath, _ = os.Getwd()
	}

	// Convert to absolute path
	absPath, err := filepath.Abs(searchPath)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to resolve path: %v", err),
		}, nil
	}

	// Ensure the path exists and is a directory
	info, err := os.Stat(absPath)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("path does not exist: %v", err),
		}, nil
	}

	if !info.IsDir() {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("path is not a directory: %s", absPath),
		}, nil
	}

	// Perform glob matching
	matches, err := doublestar.Glob(os.DirFS(absPath), globParams.Pattern)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to match pattern: %v", err),
		}, nil
	}

	// Collect file information
	var files []FileInfo
	for _, match := range matches {
		fullPath := filepath.Join(absPath, match)
		info, err := os.Stat(fullPath)
		if err != nil {
			continue // Skip files we can't stat
		}

		files = append(files, FileInfo{
			Path:    fullPath,
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
			IsDir:   info.IsDir(),
		})
	}

	// Sort by modification time (newest first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime > files[j].ModTime
	})

	// Apply limit
	truncated := false
	if len(files) > t.maxResults {
		files = files[:t.maxResults]
		truncated = true
	}

	// Build output
	var output strings.Builder
	output.WriteString(fmt.Sprintf("Found %d matching file(s) in %s:\n\n", len(files), absPath))

	for _, file := range files {
		prefix := "📄"
		if file.IsDir {
			prefix = "📁"
		}
		output.WriteString(fmt.Sprintf("%s %s\n", prefix, file.Path))
	}

	if truncated {
		output.WriteString(fmt.Sprintf("\n(Results truncated to %d files. Use a more specific pattern to narrow results.)", t.maxResults))
	}

	return ToolResult{
		Success: true,
		Output:  output.String(),
		Data: map[string]interface{}{
			"matches":     files,
			"count":       len(files),
			"total_found": len(matches),
			"truncated":   truncated,
			"pattern":     globParams.Pattern,
			"search_path": absPath,
		},
	}, nil
}
