package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// WebFetchTool implements web page fetching functionality
type WebFetchTool struct {
	timeout time.Duration
}

// WebFetchParams represents parameters for web fetching
type WebFetchParams struct {
	URL     string `json:"url"`
	Format  string `json:"format,omitempty"` // markdown, text, or html
	Timeout *int   `json:"timeout,omitempty"`
	Query   string `json:"query,omitempty"`
}

// NewWebFetchTool creates a new web fetch tool
func NewWebFetchTool(timeoutSecs int) *WebFetchTool {
	if timeoutSecs == 0 {
		timeoutSecs = 30
	}
	return &WebFetchTool{
		timeout: time.Duration(timeoutSecs) * time.Second,
	}
}

func (t *WebFetchTool) Name() string {
	return "webfetch"
}

func (t *WebFetchTool) Description() string {
	return "Fetches content from a specified URL. Takes a URL and optional format as input, " +
		"fetches the URL content, converts to requested format (markdown by default), " +
		"and returns the content in the specified format."
}

func (t *WebFetchTool) Schema() *ToolSchema {
	return &ToolSchema{
		Type: "object",
		Properties: map[string]Property{
			"url": {
				Type:        "string",
				Description: "The URL to fetch content from",
			},
			"format": {
				Type:        "string",
				Description: "Format to return content in: 'text', 'markdown', or 'html'. Defaults to 'markdown'.",
				Enum:        []string{"text", "markdown", "html"},
			},
			"timeout": {
				Type:        "integer",
				Description: "Optional timeout in seconds (max 120)",
			},
			"query": {
				Type:        "string",
				Description: "Optional search terms to extract specific content from the page",
			},
		},
		Required: []string{"url"},
	}
}

func (t *WebFetchTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var fetchParams WebFetchParams
	if err := json.Unmarshal(params, &fetchParams); err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse parameters: %v", err),
		}, nil
	}

	// Validate URL
	if fetchParams.URL == "" {
		return ToolResult{
			Success: false,
			Error:   "url is required",
		}, nil
	}

	// Parse and validate URL
	parsedURL, err := url.Parse(fetchParams.URL)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("invalid URL: %v", err),
		}, nil
	}

	// Ensure HTTPS
	if parsedURL.Scheme == "" {
		parsedURL.Scheme = "https"
	}
	if parsedURL.Scheme == "http" {
		parsedURL.Scheme = "https"
	}

	// Set default format
	format := fetchParams.Format
	if format == "" {
		format = "markdown"
	}

	// Set timeout
	timeout := t.timeout
	if fetchParams.Timeout != nil && *fetchParams.Timeout > 0 {
		timeout = time.Duration(*fetchParams.Timeout) * time.Second
		if timeout > 2*time.Minute {
			timeout = 2 * time.Minute
		}
	}

	// Create HTTP client
	client := &http.Client{
		Timeout: timeout,
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", parsedURL.String(), nil)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to create request: %v", err),
		}, nil
	}

	// Set headers
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Agent-System/1.0)")

	// Execute request
	resp, err := client.Do(req)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to fetch URL: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("HTTP error: %s", resp.Status),
		}, nil
	}

	// Read body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to read response: %v", err),
		}, nil
	}

	// Convert to requested format
	var content string
	switch format {
	case "html":
		content = string(body)
	case "text":
		content = htmlToText(string(body))
	case "markdown":
		content = htmlToMarkdown(string(body))
	default:
		content = string(body)
	}

	// Extract specific content if query is provided
	if fetchParams.Query != "" {
		content = extractRelevantContent(content, fetchParams.Query)
	}

	// Truncate if too long
	if len(content) > 100000 {
		content = content[:100000] + "\n\n[Content truncated due to length]"
	}

	return ToolResult{
		Success: true,
		Output:  content,
		Data: map[string]interface{}{
			"url":          parsedURL.String(),
			"format":       format,
			"status_code":  resp.StatusCode,
			"content_type": resp.Header.Get("Content-Type"),
			"length":       len(content),
		},
	}, nil
}

// htmlToText converts HTML to plain text
func htmlToText(html string) string {
	// Remove script and style tags
	scriptRegex := regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</\1>`)
	html = scriptRegex.ReplaceAllString(html, "")

	// Remove HTML tags
	tagRegex := regexp.MustCompile(`<[^>]+>`)
	text := tagRegex.ReplaceAllString(html, "")

	// Decode HTML entities
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = strings.ReplaceAll(text, "&#39;", "'")
	text = strings.ReplaceAll(text, "&nbsp;", " ")

	// Normalize whitespace
	text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)

	return text
}

// htmlToMarkdown converts HTML to markdown
func htmlToMarkdown(html string) string {
	// First convert to text, then add simple markdown formatting
	text := htmlToText(html)

	// Basic formatting - this is a simplified conversion
	// In a real implementation, you'd use a proper HTML parser
	return text
}

// extractRelevantContent extracts content relevant to the query
func extractRelevantContent(content string, query string) string {
	// Simple extraction - find paragraphs containing query terms
	lines := strings.Split(content, "\n")
	var relevant []string

	queryLower := strings.ToLower(query)
	queryTerms := strings.Fields(queryLower)

	for _, line := range lines {
		lineLower := strings.ToLower(line)
		for _, term := range queryTerms {
			if strings.Contains(lineLower, term) {
				relevant = append(relevant, line)
				break
			}
		}
	}

	if len(relevant) > 0 {
		return strings.Join(relevant, "\n")
	}

	return content
}
