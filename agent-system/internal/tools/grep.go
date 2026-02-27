package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GrepTool implements grep-like search functionality
type GrepTool struct {
	maxResults      int
	maxContextLines int
}

// GrepParams represents parameters for grep search
type GrepParams struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path,omitempty"`
	Glob            string `json:"glob,omitempty"`
	OutputMode      string `json:"output_mode,omitempty"` // content, files_with_matches, count
	Before          *int   `json:"-B,omitempty"`
	After           *int   `json:"-A,omitempty"`
	Context         *int   `json:"-C,omitempty"`
	ContextLines    *int   `json:"context,omitempty"`
	ShowLineNum     *bool  `json:"-n,omitempty"`
	CaseInsensitive bool   `json:"-i,omitempty"`
	Type            string `json:"type,omitempty"`
	HeadLimit       *int   `json:"head_limit,omitempty"`
	Offset          *int   `json:"offset,omitempty"`
	Multiline       bool   `json:"multiline,omitempty"`
}

// Match represents a single grep match
type Match struct {
	Path    string   `json:"path"`
	LineNum int      `json:"line_num"`
	Content string   `json:"content"`
	Context []string `json:"context,omitempty"`
}

// NewGrepTool creates a new grep tool
func NewGrepTool(maxResults, maxContextLines int) *GrepTool {
	if maxResults == 0 {
		maxResults = 1000
	}
	if maxContextLines == 0 {
		maxContextLines = 5
	}
	return &GrepTool{
		maxResults:      maxResults,
		maxContextLines: maxContextLines,
	}
}

func (t *GrepTool) Name() string {
	return "grep"
}

func (t *GrepTool) Description() string {
	return "A powerful search tool built on ripgrep-like functionality. Searches file contents using regular expressions."
}

func (t *GrepTool) Schema() *ToolSchema {
	return &ToolSchema{
		Type: "object",
		Properties: map[string]Property{
			"pattern": {
				Type:        "string",
				Description: "The regular expression pattern to search for in file contents",
			},
			"path": {
				Type:        "string",
				Description: "File or directory to search in. Defaults to current working directory.",
			},
			"glob": {
				Type:        "string",
				Description: "Glob pattern to filter files (e.g., '*.js', '*.{ts,tsx}')",
			},
			"output_mode": {
				Type:        "string",
				Description: "Output mode: 'content', 'files_with_matches', or 'count'. Defaults to 'files_with_matches'.",
				Enum:        []string{"content", "files_with_matches", "count"},
			},
			"-B": {
				Type:        "integer",
				Description: "Number of lines to show before each match. Requires output_mode: 'content'.",
			},
			"-A": {
				Type:        "integer",
				Description: "Number of lines to show after each match. Requires output_mode: 'content'.",
			},
			"-C": {
				Type:        "integer",
				Description: "Alias for context",
			},
			"context": {
				Type:        "integer",
				Description: "Number of lines to show before and after each match. Requires output_mode: 'content'.",
			},
			"-n": {
				Type:        "boolean",
				Description: "Show line numbers in output. Defaults to true.",
			},
			"-i": {
				Type:        "boolean",
				Description: "Case insensitive search",
			},
			"type": {
				Type:        "string",
				Description: "File type to search (e.g., js, py, rust, go, java)",
			},
			"head_limit": {
				Type:        "integer",
				Description: "Limit output to first N lines/entries. Defaults to 0 (unlimited).",
			},
			"offset": {
				Type:        "integer",
				Description: "Skip first N lines/entries before applying head_limit. Defaults to 0.",
			},
			"multiline": {
				Type:        "boolean",
				Description: "Enable multiline mode. Default: false.",
			},
		},
		Required: []string{"pattern"},
	}
}

func (t *GrepTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var grepParams GrepParams
	if err := json.Unmarshal(params, &grepParams); err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse parameters: %v", err),
		}, nil
	}

	// Validate parameters
	if grepParams.Pattern == "" {
		return ToolResult{
			Success: false,
			Error:   "pattern is required",
		}, nil
	}

	// Set defaults
	if grepParams.OutputMode == "" {
		grepParams.OutputMode = "files_with_matches"
	}
	if grepParams.ShowLineNum == nil {
		showLineNum := true
		grepParams.ShowLineNum = &showLineNum
	}

	// Determine context lines
	contextLines := 0
	if grepParams.ContextLines != nil {
		contextLines = *grepParams.ContextLines
	} else if grepParams.Context != nil {
		contextLines = *grepParams.Context
	} else {
		before, after := 0, 0
		if grepParams.Before != nil {
			before = *grepParams.Before
		}
		if grepParams.After != nil {
			after = *grepParams.After
		}
		if before > 0 || after > 0 {
			contextLines = before
			if after > contextLines {
				contextLines = after
			}
		}
	}

	// Limit context lines
	if contextLines > t.maxContextLines {
		contextLines = t.maxContextLines
	}

	// Determine search path
	searchPath := grepParams.Path
	if searchPath == "" {
		searchPath, _ = os.Getwd()
	}

	// Compile regex
	flags := ""
	if grepParams.CaseInsensitive {
		flags = "(?i)"
	}
	if grepParams.Multiline {
		flags += "(?s)"
	}

	regex, err := regexp.Compile(flags + grepParams.Pattern)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("invalid regex pattern: %v", err),
		}, nil
	}

	// Collect matches
	var matches []Match
	filesWithMatches := make(map[string]bool)

	// Walk directory or search single file
	info, err := os.Stat(searchPath)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("path does not exist: %v", err),
		}, nil
	}

	if info.IsDir() {
		err = filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Continue walking
			}

			// Skip directories
			if info.IsDir() {
				return nil
			}

			// Check file type filter
			if grepParams.Type != "" {
				ext := strings.TrimPrefix(filepath.Ext(path), ".")
				if ext != grepParams.Type {
					return nil
				}
			}

			// Check glob filter
			if grepParams.Glob != "" {
				matched, _ := filepath.Match(grepParams.Glob, filepath.Base(path))
				if !matched {
					return nil
				}
			}

			// Search file
			fileMatches := t.searchFile(path, regex, contextLines, grepParams.ShowLineNum)
			matches = append(matches, fileMatches...)

			if len(fileMatches) > 0 {
				filesWithMatches[path] = true
			}

			// Limit results
			if len(matches) >= t.maxResults {
				return filepath.SkipDir
			}

			return nil
		})
	} else {
		// Single file
		matches = t.searchFile(searchPath, regex, contextLines, grepParams.ShowLineNum)
		if len(matches) > 0 {
			filesWithMatches[searchPath] = true
		}
	}

	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("search failed: %v", err),
		}, nil
	}

	// Handle offset and head_limit
	offset := 0
	if grepParams.Offset != nil {
		offset = *grepParams.Offset
	}

	headLimit := len(matches)
	if grepParams.HeadLimit != nil && *grepParams.HeadLimit > 0 {
		headLimit = *grepParams.HeadLimit
	}

	if offset >= len(matches) {
		matches = []Match{}
	} else {
		end := offset + headLimit
		if end > len(matches) {
			end = len(matches)
		}
		matches = matches[offset:end]
	}

	// Format output based on output_mode
	var output string
	switch grepParams.OutputMode {
	case "count":
		output = fmt.Sprintf("Found %d matches in %d file(s)", len(matches), len(filesWithMatches))

	case "files_with_matches":
		var files []string
		for file := range filesWithMatches {
			files = append(files, file)
		}
		output = fmt.Sprintf("Found matches in %d file(s):\n\n%s", len(files), strings.Join(files, "\n"))

	case "content":
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d matches:\n\n", len(matches)))

		for _, match := range matches {
			if *grepParams.ShowLineNum {
				sb.WriteString(fmt.Sprintf("%s:%d: %s\n", match.Path, match.LineNum, match.Content))
			} else {
				sb.WriteString(fmt.Sprintf("%s: %s\n", match.Path, match.Content))
			}

			// Write context if available
			for _, ctx := range match.Context {
				sb.WriteString(fmt.Sprintf("   %s\n", ctx))
			}
		}

		output = sb.String()
	}

	return ToolResult{
		Success: true,
		Output:  output,
		Data: map[string]interface{}{
			"matches":            matches,
			"files_with_matches": len(filesWithMatches),
			"total_matches":      len(matches),
			"pattern":            grepParams.Pattern,
			"path":               searchPath,
		},
	}, nil
}

func (t *GrepTool) searchFile(path string, regex *regexp.Regexp, contextLines int, showLineNum *bool) []Match {
	var matches []Match

	file, err := os.Open(path)
	if err != nil {
		return matches
	}
	defer file.Close()

	// Read all lines
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return matches
	}

	// Search each line
	for i, line := range lines {
		if regex.MatchString(line) {
			match := Match{
				Path:    path,
				LineNum: i + 1,
				Content: line,
			}

			// Collect context lines
			if contextLines > 0 {
				start := i - contextLines
				if start < 0 {
					start = 0
				}
				end := i + contextLines + 1
				if end > len(lines) {
					end = len(lines)
				}

				for j := start; j < end; j++ {
					if j != i {
						prefix := "  "
						if *showLineNum {
							match.Context = append(match.Context, fmt.Sprintf("%s%d: %s", prefix, j+1, lines[j]))
						} else {
							match.Context = append(match.Context, prefix+lines[j])
						}
					}
				}
			}

			matches = append(matches, match)
		}
	}

	return matches
}
