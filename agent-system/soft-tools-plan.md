# Soft Tools Support Implementation Plan

## Overview

Soft tools support is an alternative to native tool calling where:
- Tools are explained in the system prompt (not sent via native tool API)
- LLM outputs tool calls as JSON in the response text
- Tool call IDs are generated client-side
- Works generically across all providers (OpenAI, Anthropic, Bedrock, Ollama, etc.)

This enables tool calling for models that don't support native function calling APIs.

## Tool Call Format

### Request Format (LLM Output)

The LLM outputs tool calls as JSON objects in the response text:

```json
{"$tool_call":"tool_name","$params":{"param":value,"param2":"value2"}}
```

With optional markdown code block guards:
```
```json
{"$tool_call":"tool_name","$params":{"param":value}}
```
```

### After ID Injection

When the response from LLM is received with one or multiple tool calls, a unique call ID must be generated and embedded into the original request before processing:

```json
{"$tool_call":"tool_name","$tool_call_id":"d001e92a","$params":{"param":value}}
```

### Tool Result Response

After tool execution, the host program produces:

**Success:**
```json
{"$tool_call_result":"tool_name","$tool_call_id":"d001e92a","$result":"result text or direct json value"}
```

**Error:**
```json
{"$tool_call_error":"tool_name","$tool_call_id":"d001e92a","$error":"no such tool|malformed json|required missing parameters aaa,bbb"}
```

---

## Implementation Plan

### Phase 1: Core langchaingo Changes

#### 1.1 Add SoftTools Option (`llms/options.go`)

```go
type CallOptions struct {
    // ... existing fields ...
    
    // SoftTools when true, tools are explained in system prompt instead of
    // using native tool calling APIs. Tool calls are parsed from response JSON.
    SoftTools bool `json:"soft_tools,omitempty"`
}

// WithSoftTools enables or disables soft tools mode.
// When enabled, tools are described in the system prompt and tool calls
// are parsed from the response text as JSON objects.
func WithSoftTools(enabled bool) CallOption {
    return func(o *CallOptions) {
        o.SoftTools = enabled
    }
}
```

#### 1.2 Create SoftTools Package (`llms/softtools/softtools.go`)

```go
package softtools

import (
    "encoding/json"
    "fmt"
    "regexp"
    "strings"

    "github.com/google/uuid"
    "github.com/tmc/langchaingo/llms"
)

const toolCallIDLength = 8

// GenerateToolCallID generates a unique 8-character hex ID for a tool call.
func GenerateToolCallID() string {
    id := uuid.New().String()
    return strings.ReplaceAll(id, "-", "")[:toolCallIDLength]
}

// SoftToolCall represents a tool call parsed from LLM output.
type SoftToolCall struct {
    ToolName string                 `json:"$tool_call"`
    Params   map[string]interface{} `json:"$params,omitempty"`
    ID       string                 `json:"$tool_call_id,omitempty"`
}

// SoftToolResult represents a tool result to be sent back to the LLM.
type SoftToolResult struct {
    ToolName string `json:"$tool_call_result"`
    ID       string `json:"$tool_call_id"`
    Result   string `json:"$result,omitempty"`
}

// SoftToolError represents a tool error to be sent back to the LLM.
type SoftToolError struct {
    ToolName string `json:"$tool_call_error"`
    ID       string `json:"$tool_call_id"`
    Error    string `json:"error"`
}

// GenerateSoftToolsPrompt generates a system prompt section describing available tools.
func GenerateSoftToolsPrompt(tools []llms.Tool) string {
    if len(tools) == 0 {
        return ""
    }

    var sb strings.Builder
    sb.WriteString("## Available Tools\n\n")
    sb.WriteString("You have access to the following tools. To use a tool, output a JSON object in this format:\n\n")
    sb.WriteString("```json\n")
    sb.WriteString(`{"$tool_call":"tool_name","$params":{"param1":"value1","param2":"value2"}}`)
    sb.WriteString("\n```\n\n")
    sb.WriteString("You can make multiple tool calls by outputting multiple JSON objects.\n\n")
    sb.WriteString("### Tool Specifications\n\n")

    for _, tool := range tools {
        if tool.Function == nil {
            continue
        }
        sb.WriteString(fmt.Sprintf("#### %s\n\n", tool.Function.Name))
        if tool.Function.Description != "" {
            sb.WriteString(tool.Function.Description + "\n\n")
        }

        // Format parameters
        if tool.Function.Parameters != nil {
            sb.WriteString("**Parameters:**\n\n")
            sb.WriteString(formatParameters(tool.Function.Parameters))
            sb.WriteString("\n")
        }
    }

    sb.WriteString("### Examples\n\n")
    sb.WriteString("Example tool call:\n\n")
    sb.WriteString("```json\n")
    sb.WriteString(`{"$tool_call":"read","$params":{"filePath":"/path/to/file.txt"}}`)
    sb.WriteString("\n```\n\n")

    return sb.String()
}

// formatParameters formats the JSON schema parameters as readable text.
func formatParameters(params interface{}) string {
    paramsJSON, err := json.MarshalIndent(params, "", "  ")
    if err != nil {
        return fmt.Sprintf("%v", params)
    }
    return "```json\n" + string(paramsJSON) + "\n```\n"
}

// ParseSoftToolCalls extracts tool calls from LLM response text.
// Returns the parsed tool calls and the remaining text content.
func ParseSoftToolCalls(content string) ([]SoftToolCall, string) {
    var toolCalls []SoftToolCall
    var remainingText strings.Builder

    // Pattern to match tool call JSON objects
    // Matches both with and without markdown code blocks
    toolCallPattern := regexp.MustCompile(`(?s)` +
        `(?:```json\s*)?` +                                    // Optional opening code block
        `(\{[^{}]*"\$tool_call"[^{}]*\})` +                    // Tool call object
        `(?:\s*```\s*)?`)                                      // Optional closing code block

    // Also match multi-line JSON objects
    toolCallPatternMultiline := regexp.MustCompile(`(?s)` +
        `(?:```json\s*)?` +
        `(\{[^{}]*(?:\{[^{}]*\}[^{}]*)*"\$tool_call"[^{}]*(?:\{[^{}]*\}[^{}]*)*\})` +
        `(?:\s*```\s*)?`)

    // Try multiline pattern first
    matches := toolCallPatternMultiline.FindAllStringSubmatchIndex(content, -1)

    if len(matches) == 0 {
        // Fall back to simple pattern
        matches = toolCallPattern.FindAllStringSubmatchIndex(content, -1)
    }

    lastEnd := 0
    for _, match := range matches {
        if len(match) >= 4 {
            // Add text before this match
            remainingText.WriteString(content[lastEnd:match[2]])

            // Extract and parse the tool call JSON
            jsonStr := content[match[2]:match[3]]
            var tc SoftToolCall
            if err := json.Unmarshal([]byte(jsonStr), &tc); err == nil {
                // Generate ID if not present
                if tc.ID == "" {
                    tc.ID = GenerateToolCallID()
                }
                toolCalls = append(toolCalls, tc)
            }

            lastEnd = match[3]
        }
    }

    // Add remaining text after last match
    if lastEnd < len(content) {
        remainingText.WriteString(content[lastEnd:])
    }

    return toolCalls, strings.TrimSpace(remainingText.String())
}

// FormatToolResult formats a tool result for sending back to the LLM.
func FormatToolResult(toolName, callID, result string) string {
    r := SoftToolResult{
        ToolName: toolName,
        ID:       callID,
        Result:   result,
    }
    b, _ := json.Marshal(r)
    return string(b)
}

// FormatToolError formats a tool error for sending back to the LLM.
func FormatToolError(toolName, callID, errMsg string) string {
    e := SoftToolError{
        ToolName: toolName,
        ID:       callID,
        Error:    errMsg,
    }
    b, _ := json.Marshal(e)
    return string(b)
}

// ConvertToLLMToolCalls converts soft tool calls to llms.ToolCall format.
func ConvertToLLMToolCalls(softCalls []SoftToolCall) []llms.ToolCall {
    result := make([]llms.ToolCall, len(softCalls))
    for i, sc := range softCalls {
        paramsJSON, _ := json.Marshal(sc.Params)
        result[i] = llms.ToolCall{
            ID:   sc.ID,
            Type: "function",
            FunctionCall: &llms.FunctionCall{
                Name:      sc.ToolName,
                Arguments: string(paramsJSON),
            },
        }
    }
    return result
}
```

#### 1.3 Provider-Specific Changes

##### OpenAI Provider (`llms/openai/openaillm.go`)

Modify `GenerateContent`:

```go
func (o *LLM) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
    // ... existing setup ...
    
    opts := llms.CallOptions{}
    for _, opt := range options {
        opt(&opts)
    }

    // Handle soft tools mode
    if opts.SoftTools && len(opts.Tools) > 0 {
        // Generate soft tools prompt
        softToolsPrompt := softtools.GenerateSoftToolsPrompt(opts.Tools)
        
        // Inject into first system message or create one
        messages = injectSoftToolsPrompt(messages, softToolsPrompt)
        
        // Clear tools so they aren't sent via native API
        opts.Tools = nil
        opts.ToolChoice = nil
    }

    // ... rest of existing implementation ...
}

// injectSoftToolsPrompt adds soft tools prompt to system message
func injectSoftToolsPrompt(messages []llms.MessageContent, prompt string) []llms.MessageContent {
    result := make([]llms.MessageContent, len(messages))
    injected := false

    for i, msg := range messages {
        if msg.Role == llms.ChatMessageTypeSystem && !injected {
            // Prepend to existing system message
            newParts := []llms.ContentPart{
                llms.TextContent{Text: prompt + "\n\n"},
            }
            newParts = append(newParts, msg.Parts...)
            result[i] = llms.MessageContent{
                Role:  msg.Role,
                Parts: newParts,
            }
            injected = true
        } else {
            result[i] = msg
        }
    }

    // If no system message exists, create one
    if !injected {
        result = append([]llms.MessageContent{{
            Role:  llms.ChatMessageTypeSystem,
            Parts: []llms.ContentPart{llms.TextContent{Text: prompt}},
        }}, result...)
    }

    return result
}
```

After receiving response:

```go
// Parse soft tool calls from response if in soft tools mode
if opts.SoftTools {
    softCalls, remainingText := softtools.ParseSoftToolCalls(choice.Content)
    if len(softCalls) > 0 {
        choice.ToolCalls = softtools.ConvertToLLMToolCalls(softCalls)
        choice.Content = remainingText
    }
}
```

##### Anthropic Provider (`llms/anthropic/anthropicllm.go`)

Similar changes:
1. Check `opts.SoftTools` at start of `generateMessagesContent`
2. Inject soft tools prompt into system prompt
3. Don't send `tools` to API
4. Parse response for soft tool calls

##### Bedrock Provider (`llms/bedrock/internal/bedrockclient/`)

For Bedrock, handle in `createAnthropicCompletion` and similar provider-specific functions:
1. Check `options.SoftTools`
2. Append soft tools prompt to system
3. Skip native tools configuration
4. Parse response for soft tool calls

##### Ollama Provider (`llms/ollama/`)

Similar pattern as OpenAI.

---

### Phase 2: Agent-System Configuration

#### 2.1 Update Config Structure (`internal/config/config.go`)

```go
type ModelConfig struct {
    Name        string  `yaml:"name"`
    Provider    string  `yaml:"provider"`
    ModelID     string  `yaml:"model_id"`
    APIKey      string  `yaml:"api_key"`
    BaseURL     string  `yaml:"base_url"`
    MaxTokens   int     `yaml:"max_tokens"`
    Temperature float64 `yaml:"temperature"`
    Region      string  `yaml:"region"`
    
    // SoftTools enables soft tools mode for this model
    // When true, tools are explained in system prompt instead of native API
    SoftTools   bool    `yaml:"soft_tools"`
}
```

#### 2.2 Update Agent (`internal/agent/agent.go`)

```go
func createLLM(modelConfig config.ModelConfig) (llms.Model, error) {
    // ... existing provider switch ...
    
    case "openai":
        opts := []openai.Option{
            openai.WithModel(modelConfig.ModelID),
            openai.WithToken(modelConfig.APIKey),
        }
        if modelConfig.BaseURL != "" {
            opts = append(opts, openai.WithBaseURL(modelConfig.BaseURL))
        }
        // Store soft_tools flag in client for use in GenerateContent
        if modelConfig.SoftTools {
            opts = append(opts, openai.WithSoftTools(true))
        }
        return openai.New(opts...)
    // ... similar for other providers ...
}
```

In the agentic loop:

```go
func (a *Agent) runAgenticLoop(ctx context.Context) error {
    // ... existing code ...
    
    callOpts := []llms.CallOption{
        llms.WithTemperature(float64(a.modelConfig.Temperature)),
        llms.WithMaxTokens(a.modelConfig.MaxTokens),
    }
    
    // Use soft tools if configured
    if a.modelConfig.SoftTools {
        callOpts = append(callOpts, llms.WithSoftTools(true))
        callOpts = append(callOpts, llms.WithTools(toolDefinitions))
    } else {
        callOpts = append(callOpts, llms.WithTools(toolDefinitions))
    }
    
    resp, err := a.llm.GenerateContent(ctx, a.conversation, callOpts...)
    // ...
}
```

#### 2.3 Example Configuration (`config.yaml`)

```yaml
models:
  gpt-4:
    name: "gpt-4"
    provider: "openai"
    model_id: "gpt-4"
    api_key: "${env.OPENAI_API_KEY}"
    soft_tools: false  # Use native tool calling

  local-llama:
    name: "Local Llama"
    provider: "openai"
    model_id: "llama3"
    base_url: "http://localhost:11434/v1"
    soft_tools: true  # Use soft tools (model doesn't support native tools)

  bedrock-claude:
    name: "Bedrock Claude"
    provider: "bedrock"
    model_id: "anthropic.claude-3-sonnet-20240229-v1:0"
    soft_tools: false  # Claude supports native tools
```

---

### Phase 3: Testing

#### 3.1 Unit Tests (`llms/softtools/softtools_test.go`)

```go
package softtools

import (
    "testing"

    "github.com/tmc/langchaingo/llms"
)

func TestGenerateToolCallID(t *testing.T) {
    id1 := GenerateToolCallID()
    id2 := GenerateToolCallID()
    
    if len(id1) != 8 {
        t.Errorf("Expected ID length 8, got %d", len(id1))
    }
    if id1 == id2 {
        t.Error("IDs should be unique")
    }
}

func TestParseSoftToolCalls(t *testing.T) {
    tests := []struct {
        name          string
        input         string
        expectedCalls int
        expectedNames []string
    }{
        {
            name:          "simple tool call",
            input:         `{"$tool_call":"read","$params":{"filePath":"/test.txt"}}`,
            expectedCalls: 1,
            expectedNames: []string{"read"},
        },
        {
            name: "tool call in markdown",
            input: "Here's the file:\n```json\n{\"$tool_call\":\"read\",\"$params\":{\"filePath\":\"/test.txt\"}}\n```\nDone",
            expectedCalls: 1,
            expectedNames: []string{"read"},
        },
        {
            name: "multiple tool calls",
            input: `{"$tool_call":"read","$params":{"filePath":"/a.txt"}}
{"$tool_call":"read","$params":{"filePath":"/b.txt"}}`,
            expectedCalls: 2,
            expectedNames: []string{"read", "read"},
        },
        {
            name: "tool call with text before and after",
            input: "Let me read that file.\n{\"$tool_call\":\"read\",\"$params\":{\"filePath\":\"/test.txt\"}}\nHere's what I found.",
            expectedCalls: 1,
            expectedNames: []string{"read"},
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            calls, remaining := ParseSoftToolCalls(tt.input)
            if len(calls) != tt.expectedCalls {
                t.Errorf("Expected %d calls, got %d", tt.expectedCalls, len(calls))
            }
            for i, name := range tt.expectedNames {
                if i < len(calls) && calls[i].ToolName != name {
                    t.Errorf("Call %d: expected name %s, got %s", i, name, calls[i].ToolName)
                }
            }
            if len(calls) > 0 && calls[0].ID == "" {
                t.Error("Tool call ID should be generated")
            }
            _ = remaining
        })
    }
}

func TestGenerateSoftToolsPrompt(t *testing.T) {
    tools := []llms.Tool{
        {
            Type: "function",
            Function: &llms.FunctionDefinition{
                Name:        "read",
                Description: "Read a file from the filesystem",
                Parameters: map[string]interface{}{
                    "type": "object",
                    "properties": map[string]interface{}{
                        "filePath": map[string]interface{}{
                            "type":        "string",
                            "description": "Path to the file to read",
                        },
                    },
                    "required": []string{"filePath"},
                },
            },
        },
    }

    prompt := GenerateSoftToolsPrompt(tools)
    
    if !contains(prompt, "$tool_call") {
        t.Error("Prompt should contain tool call format")
    }
    if !contains(prompt, "read") {
        t.Error("Prompt should contain tool name")
    }
    if !contains(prompt, "filePath") {
        t.Error("Prompt should contain parameter name")
    }
}

func contains(s, substr string) bool {
    return len(s) > 0 && len(substr) > 0 && 
           (s == substr || len(s) > len(substr) && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
    for i := 0; i <= len(s)-len(substr); i++ {
        if s[i:i+len(substr)] == substr {
            return true
        }
    }
    return false
}
```

#### 3.2 Integration Tests

Test with actual LLM calls to verify:
1. Soft tools prompt is correctly injected
2. Tool calls are parsed correctly from response
3. Tool results are formatted correctly
4. Multi-turn conversations work

---

## File Changes Summary

### New Files

| File | Description |
|------|-------------|
| `llms/softtools/softtools.go` | Core soft tools implementation |
| `llms/softtools/softtools_test.go` | Unit tests |

### Modified Files

| File | Changes |
|------|---------|
| `llms/options.go` | Add `SoftTools` field and `WithSoftTools()` option |
| `llms/openai/openaillm.go` | Handle soft tools mode |
| `llms/anthropic/anthropicllm.go` | Handle soft tools mode |
| `llms/bedrock/internal/bedrockclient/provider_anthropic.go` | Handle soft tools for Bedrock Anthropic |
| `llms/ollama/ollamallm.go` | Handle soft tools mode |
| `agent-system/internal/config/config.go` | Add `SoftTools` field to ModelConfig |
| `agent-system/internal/agent/agent.go` | Pass soft tools option to LLM |

---

## Implementation Order

1. **Core package**: Create `llms/softtools/` package with prompt generation and parsing
2. **Options**: Add `SoftTools` to `llms/options.go`
3. **OpenAI provider**: Implement soft tools in OpenAI provider (most common use case)
4. **Tests**: Write comprehensive tests
5. **Other providers**: Extend to Anthropic, Bedrock, Ollama
6. **Agent system**: Add configuration and integration

---

## Edge Cases to Handle

1. **Malformed JSON**: Skip unparseable tool calls, log warning
2. **Missing parameters**: Include in error message
3. **Unknown tool**: Return appropriate error
4. **Nested JSON in params**: Support complex parameter values
5. **Streaming**: Parse tool calls from accumulated stream
6. **Multi-turn**: Preserve tool call IDs across conversation
7. **Empty response**: Handle gracefully
8. **Mixed content**: Text before/after tool calls preserved

---

## Benefits

1. **Universal Compatibility**: Works with any model that can output JSON
2. **No Provider Lock-in**: Same tool interface across all providers
3. **Transparent**: Tool definitions visible in system prompt
4. **Debuggable**: Easy to see what tools are available and what was called
5. **Extensible**: Easy to add new tool call formats or prompts
