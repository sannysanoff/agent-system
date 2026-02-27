package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// WebSearchTool implements web search functionality
type WebSearchTool struct {
	maxResults  int
	timeout     time.Duration
	apiEndpoint string
	apiKey      string
}

// WebSearchParams represents parameters for web search
type WebSearchParams struct {
	Query      string      `json:"query"`
	NumResults interface{} `json:"num_results,omitempty"`
	Timeout    interface{} `json:"timeout,omitempty"`
}

// SearchResult represents a single search result
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Source  string `json:"source"`
}

// NewWebSearchTool creates a new web search tool
func NewWebSearchTool(maxResults, timeoutSecs int, apiEndpoint, apiKey string) *WebSearchTool {
	if maxResults == 0 {
		maxResults = 8
	}
	if timeoutSecs == 0 {
		timeoutSecs = 30
	}
	return &WebSearchTool{
		maxResults:  maxResults,
		timeout:     time.Duration(timeoutSecs) * time.Second,
		apiEndpoint: apiEndpoint,
		apiKey:      apiKey,
	}
}

func (t *WebSearchTool) Name() string {
	return "websearch"
}

func (t *WebSearchTool) Description() string {
	return "Search the web using Exa AI or similar search service. Performs real-time web searches and can " +
		"scrape content from specific URLs. Provides up-to-date information for current events and recent data."
}

func (t *WebSearchTool) Schema() *ToolSchema {
	return &ToolSchema{
		Type: "object",
		Properties: map[string]Property{
			"query": {
				Type:        "string",
				Description: "Search query to find relevant information",
			},
			"num_results": {
				Type:        "number",
				Description: "Number of search results to return",
			},
			"timeout": {
				Type:        "number",
				Description: "Optional timeout in seconds (max 120)",
			},
		},
		Required: []string{"query"},
	}
}

func (t *WebSearchTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var searchParams WebSearchParams
	if err := json.Unmarshal(params, &searchParams); err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse parameters: %v", err),
		}, nil
	}

	// Validate parameters
	if searchParams.Query == "" {
		return ToolResult{
			Success: false,
			Error:   "query is required",
		}, nil
	}

	// Set defaults
	numResults := t.maxResults
	if searchParams.NumResults != nil {
		if n, ok := toInt(searchParams.NumResults); ok && n > 0 {
			numResults = n
			if numResults > 50 {
				numResults = 50
			}
		}
	}

	timeout := t.timeout
	if searchParams.Timeout != nil {
		if n, ok := toInt(searchParams.Timeout); ok && n > 0 {
			timeout = time.Duration(n) * time.Second
			if timeout > 2*time.Minute {
				timeout = 2 * time.Minute
			}
		}
	}

	// Perform search
	results, err := t.search(ctx, searchParams.Query, numResults, timeout)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("search failed: %v", err),
		}, nil
	}

	// Build output
	var output string
	if len(results) == 0 {
		output = "No results found."
	} else {
		output = fmt.Sprintf("Found %d result(s) for '%s':\n\n", len(results), searchParams.Query)
		for i, result := range results {
			output += fmt.Sprintf("%d. **%s**\n   URL: %s\n   %s\n\n", i+1, result.Title, result.URL, result.Snippet)
		}
	}

	return ToolResult{
		Success: true,
		Output:  output,
		Data: map[string]interface{}{
			"query":       searchParams.Query,
			"num_results": len(results),
			"results":     results,
		},
	}, nil
}

func (t *WebSearchTool) search(ctx context.Context, query string, numResults int, timeout time.Duration) ([]SearchResult, error) {
	// If we have an API endpoint, use it (e.g., Exa AI, Serper, etc.)
	if t.apiEndpoint != "" && t.apiKey != "" {
		return t.searchWithAPI(ctx, query, numResults, timeout)
	}

	// Otherwise, return a message indicating that search requires configuration
	return nil, fmt.Errorf("web search requires API configuration. Please set api_endpoint and api_key in config")
}

func toInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	case string:
		var i int
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil {
			return i, true
		}
	}
	return 0, false
}

func (t *WebSearchTool) searchWithAPI(ctx context.Context, query string, numResults int, timeout time.Duration) ([]SearchResult, error) {
	// Create HTTP client
	client := &http.Client{
		Timeout: timeout,
	}

	// Build request URL
	params := url.Values{}
	params.Set("q", query)
	params.Set("num", fmt.Sprintf("%d", numResults))

	reqURL := fmt.Sprintf("%s?%s", t.apiEndpoint, params.Encode())

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	// Set headers
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("Accept", "application/json")

	// Execute request
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %s", resp.Status)
	}

	// Parse response
	var apiResponse struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
			Source  string `json:"source"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResponse); err != nil {
		return nil, err
	}

	// Convert to SearchResult
	var results []SearchResult
	for _, r := range apiResponse.Results {
		results = append(results, SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Snippet,
			Source:  r.Source,
		})
	}

	return results, nil
}
