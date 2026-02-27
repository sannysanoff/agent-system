package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Tool represents a tool/instrument that the agent can use
type Tool interface {
	Name() string
	Description() string
	Schema() *ToolSchema
	Execute(ctx context.Context, params json.RawMessage) (ToolResult, error)
}

// ToolSchema represents the JSON schema for tool parameters
type ToolSchema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required,omitempty"`
}

// Property represents a single property in the tool schema
type Property struct {
	Type        string                 `json:"type"`
	Description string                 `json:"description"`
	Enum        []string               `json:"enum,omitempty"`
	Items       *Property              `json:"items,omitempty"`
	Properties  map[string]interface{} `json:"properties,omitempty"`
	Required    []string               `json:"required,omitempty"`
}

// ToolResult represents the result of a tool execution
type ToolResult struct {
	Success bool        `json:"success"`
	Tool    string      `json:"tool,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	Output  string      `json:"output,omitempty"`
}

// ToolCall represents a single tool call request
type ToolCall struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Params json.RawMessage `json:"params"`
}

// ToolRegistry manages all available tools
type ToolRegistry struct {
	tools map[string]Tool
	mu    sync.RWMutex
}

// NewToolRegistry creates a new tool registry
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry
func (r *ToolRegistry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

// Get retrieves a tool by name
func (r *ToolRegistry) Get(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, exists := r.tools[name]
	if !exists {
		return nil, fmt.Errorf("tool '%s' not found", name)
	}
	return tool, nil
}

// GetAll returns all registered tools
func (r *ToolRegistry) GetAll() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	return tools
}

// ExecuteTool executes a single tool call
func (r *ToolRegistry) ExecuteTool(ctx context.Context, call ToolCall) (ToolResult, error) {
	tool, err := r.Get(call.Name)
	if err != nil {
		return ToolResult{
			Success: false,
			Tool:    call.Name,
			Error:   err.Error(),
		}, nil
	}

	result, execErr := tool.Execute(ctx, call.Params)
	result.Tool = call.Name
	return result, execErr
}

// ToolCallResult represents the result of a tool call with its ID
type ToolCallResult struct {
	CallID string
	Result ToolResult
	Error  error
}

// ExecuteToolsParallel executes multiple tool calls in parallel
func (r *ToolRegistry) ExecuteToolsParallel(ctx context.Context, calls []ToolCall) []ToolCallResult {
	results := make([]ToolCallResult, len(calls))
	var wg sync.WaitGroup

	// Create a buffered channel to limit concurrency
	semaphore := make(chan struct{}, 10)

	for i, call := range calls {
		wg.Add(1)
		go func(index int, toolCall ToolCall) {
			defer wg.Done()

			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			result, err := r.ExecuteTool(ctx, toolCall)
			results[index] = ToolCallResult{
				CallID: toolCall.ID,
				Result: result,
				Error:  err,
			}
		}(i, call)
	}

	wg.Wait()
	return results
}

// ToLangchainTools converts all tools to langchaingo tools
func (r *ToolRegistry) ToLangchainTools() []Tool {
	return r.GetAll()
}
