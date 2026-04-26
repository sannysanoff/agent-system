package subagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tmc/langchaingo/llms"

	"agent-system/internal/prompts"
	"agent-system/internal/tools"
	"agent-system/internal/usage"
)

// getSessionsDir returns the directory for storing session files.
// Uses MYAGENT_SESSIONS_DIRECTORY env var if set, otherwise defaults to ~/.claude/myclaude/sessions
func getSessionsDir() string {
	if customDir := os.Getenv("MYAGENT_SESSIONS_DIRECTORY"); customDir != "" {
		return customDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "myclaude", "sessions")
}

// SubAgent implements a subagent that can execute tasks autonomously
type SubAgent struct {
	id           string
	parentID     string
	sessionID    string
	agentType    string
	llm          llms.Model
	toolRegistry *tools.ToolRegistry
	conversation []llms.MessageContent
	maxTurns     int
	turnsUsed    int
	jsonOutput   bool
	softTools    bool
	agentContent string
	fork         bool // Whether this is a forked subagent
	resume       bool // Whether this is a resumed subagent
}

// SubAgentOptions represents options for creating a subagent
type SubAgentOptions struct {
	ID                  string
	ParentID            string
	AgentType           string
	LLM                 llms.Model
	Tools               []tools.Tool
	MaxTurns            int
	JSONOutput          bool
	SoftTools           bool
	AgentContent        string
	InitialConversation []interface{} // Forked or resumed conversation (optional)
	ForkedPrompt        string        // Special prompt for fork mode (optional)
	Fork                bool          // Whether this is a forked subagent
	Resume              bool          // Whether this is a resumed subagent
}

// NewSubAgent creates a new subagent
func NewSubAgent(options SubAgentOptions) *SubAgent {
	registry := tools.NewToolRegistry()
	for _, tool := range options.Tools {
		registry.Register(tool)
	}

	id := options.ID
	if id == "" {
		id = uuid.New().String()
	}

	// Initialize conversation - either fork/resume from parent or start fresh
	var initialConv []llms.MessageContent
	if (options.Fork || options.Resume) && len(options.InitialConversation) > 0 {
		// Clone the conversation (forked from parent or loaded from session)
		initialConv = make([]llms.MessageContent, len(options.InitialConversation))
		for i, msg := range options.InitialConversation {
			if lm, ok := msg.(llms.MessageContent); ok {
				initialConv[i] = lm
			}
		}
	}

	return &SubAgent{
		id:           id,
		parentID:     options.ParentID,
		sessionID:    id,
		agentType:    options.AgentType,
		llm:          options.LLM,
		toolRegistry: registry,
		conversation: initialConv,
		maxTurns:     options.MaxTurns,
		jsonOutput:   options.JSONOutput,
		softTools:    options.SoftTools,
		agentContent: options.AgentContent,
		fork:         options.Fork,
		resume:       options.Resume,
	}
}

func (s *SubAgent) logf(role string, format string, args ...interface{}) {
	s.logfWithMeta(role, nil, nil, format, args...)
}

const maxToolResultLength = 200

func (s *SubAgent) logToolResult(toolName string, resultJSON []byte) {
	resultLen := len(resultJSON)

	// In JSON output mode, never truncate - we need valid JSON
	if s.jsonOutput || resultLen <= maxToolResultLength {
		s.logf("TOOL_RESULT", "%s\n", string(resultJSON))
	} else {
		truncated := string(resultJSON[:maxToolResultLength])
		s.logf("TOOL_RESULT", "%s... (truncated, %d bytes)\n", truncated, resultLen)
	}
}

func (s *SubAgent) logfWithMeta(role string, u *usage.Usage, meta map[string]interface{}, format string, args ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	msg := fmt.Sprintf(format, args...)

	if s.jsonOutput {
		progressData := map[string]interface{}{
			"role":          role,
			"subagent":      s.id,
			"parent_id":     s.parentID,
			"subagent_type": s.agentType,
			"content":       strings.TrimSuffix(msg, "\n"),
		}
		if u != nil {
			progressData["usage"] = u
		}
		for k, v := range meta {
			progressData[k] = v
		}

		out := map[string]interface{}{
			"timestamp": timestamp,
			"type":      "progress",
			"data":      progressData,
		}
		jsonBytes, _ := json.Marshal(out)
		fmt.Println(string(jsonBytes))
	} else {
		prefix := fmt.Sprintf("[%s] [%s] %s: ", timestamp, s.id, role)
		fmt.Print(prefix + msg)
	}
}

func (s *SubAgent) getSessionPath() string {
	return filepath.Join(getSessionsDir(), s.sessionID+".json")
}

func (s *SubAgent) saveSession() error {
	path := s.getSessionPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	metadata := tools.SessionMetadata{
		Version:      tools.CurrentSessionVersion,
		SubagentType: s.agentType,
		Conversation: s.conversation,
	}

	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

func (s *SubAgent) addMessage(msg llms.MessageContent) {
	s.conversation = append(s.conversation, msg)
	if err := s.saveSession(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Failed to save session: %v\n", err)
		os.Exit(1)
	}
}

// ID returns the subagent ID
func (s *SubAgent) ID() string {
	return s.id
}

// Execute runs the subagent on a task
func (s *SubAgent) Execute(ctx context.Context, prompt string, maxTurns int) (tools.SubAgentResult, error) {
	if maxTurns > 0 {
		s.maxTurns = maxTurns
	}

	// Build system prompt based on agent type (only if not forking or resuming)
	systemPrompt := ""
	if !s.fork && !s.resume {
		systemPrompt = s.buildSystemPrompt()
	}

	// Initialize conversation
	if len(s.conversation) == 0 {
		// Normal mode: start fresh with system prompt
		s.addMessage(llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt))
		s.addMessage(llms.TextParts(llms.ChatMessageTypeHuman, prompt))
	} else if s.fork || s.resume {
		// Fork or resume mode: conversation is already loaded (from parent or session file)
		// Just append the prompt as a new user message
		s.addMessage(llms.TextParts(llms.ChatMessageTypeHuman, prompt))
	}

	// Run agentic loop
	for s.turnsUsed < s.maxTurns {
		// Get available tools
		toolDefinitions := s.getToolDefinitions()

		// Call LLM
		resp, err := s.llm.GenerateContent(ctx, s.conversation,
			llms.WithTools(toolDefinitions),
		)
		if err != nil {
			return tools.SubAgentResult{
				Success:   false,
				Error:     fmt.Sprintf("LLM generation failed: %v", err),
				TurnsUsed: s.turnsUsed,
			}, nil
		}

		if len(resp.Choices) == 0 {
			return tools.SubAgentResult{
				Success:   false,
				Error:     "no response from LLM",
				TurnsUsed: s.turnsUsed,
			}, nil
		}

		choice := resp.Choices[0]
		s.turnsUsed++

		// Extract usage
		currentUsage := usage.ExtractUsage(choice.GenerationInfo)

		// Add assistant message
		// Include reasoning_content if present (required for reasoning models like DeepSeek)
		contentParts := make([]llms.ContentPart, 0)
		softToolError := len(choice.ToolCalls) == 0 && hasSoftToolMarkers(choice.Content)
		if choice.Content != "" || choice.ReasoningContent != "" {
			contentParts = append(contentParts, llms.TextContent{
				Text:             choice.Content,
				ReasoningContent: choice.ReasoningContent,
			})
			if !softToolError {
				s.logfWithMeta("ASSISTANT", &currentUsage, nil, "%s\n", choice.Content)
			}
		}
		// Always add ToolCall parts so we have the IDs for serialization
		for _, tc := range choice.ToolCalls {
			contentParts = append(contentParts, tc)
		}
		s.addMessage(llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: contentParts,
		})

		// Check for tool calls
		if len(choice.ToolCalls) == 0 {
			if softToolError {
				s.addMessage(llms.MessageContent{
					Role: llms.ChatMessageTypeTool,
					Parts: []llms.ContentPart{
						llms.ToolCallResponse{
							ToolCallID: "soft_tool_error",
							Content:    softToolFormatGuidance(),
						},
					},
				})
				continue
			}
			// No tool calls, return final response
			return tools.SubAgentResult{
				Success:   true,
				Response:  choice.Content,
				TurnsUsed: s.turnsUsed,
			}, nil
		}

		if missingResponse := s.checkMissingToolParams(choice.ToolCalls); missingResponse != "" {
			return tools.SubAgentResult{
				Success:   false,
				Response:  missingResponse,
				TurnsUsed: s.turnsUsed,
			}, nil
		}

		for _, tc := range choice.ToolCalls {
			s.logfWithMeta("TOOL_CALL", &currentUsage, map[string]interface{}{
				"tool_name":    tc.FunctionCall.Name,
				"tool_call_id": tc.ID,
			}, "%s\n", tc.FunctionCall.Arguments)
		}

		// Execute tool calls
		toolResults := s.executeToolCalls(ctx, choice.ToolCalls)

		// Add tool results to conversation
		for i, result := range toolResults {
			resultJSON, _ := json.Marshal(result.Result)
			s.logToolResult(choice.ToolCalls[i].FunctionCall.Name, resultJSON)
			s.addMessage(llms.MessageContent{

				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: choice.ToolCalls[i].ID,
						Content:    string(resultJSON),
					},
				},
			})
		}
	}

	// Max turns reached
	return tools.SubAgentResult{
		Success:   false,
		Error:     "maximum turns reached",
		TurnsUsed: s.turnsUsed,
	}, nil
}

// buildSystemPrompt builds the system prompt based on agent type
func (s *SubAgent) buildSystemPrompt() string {
	// If custom agent content is provided, use it
	if s.agentContent != "" {
		return `You are a specialized subagent for complex tasks.
You have access to tools and should use them to complete the assigned task.
Return a clear, concise final response when complete.

<agent-persona>
` + s.agentContent + `
</agent-persona>
`
	}

	basePrompt := `You are a specialized subagent for complex tasks.
You have access to tools and should use them to complete the assigned task.
Return a clear, concise final response when complete.

`

	switch s.agentType {
	case "Bash":
		return basePrompt + `You are a Bash command execution specialist.
Focus on executing shell commands safely and efficiently.
Provide clear output and handle errors appropriately.
`
	case "Explore":
		return basePrompt + `You are a fast codebase exploration specialist.
Quickly search and explore codebases to find patterns, files, and answer questions.
Use glob and grep tools extensively to find relevant code.
Return findings as soon as you have sufficient information.
`
	case "Plan":
		return basePrompt + `You are a software architecture planning specialist.
Help plan software designs, architectures, and implementation strategies.
Provide detailed technical recommendations and best practices.
`
	case "general-purpose":
		return basePrompt + `You are a general-purpose research and execution agent.
Handle a wide variety of tasks efficiently.
Use appropriate tools and provide thorough results.
`
	case "code-reviewer":
		return basePrompt + `You are a code review specialist.
Review code for quality, security, performance, and best practices.
Provide specific, actionable feedback with file:line references.
`
	case "claude-code-guide":
		return basePrompt + `You are a Claude Code and Agent SDK documentation specialist.
Help with agent system usage, tool documentation, and best practices.
Provide accurate technical guidance.
`
	default:
		return basePrompt + `You are a general-purpose subagent.
Complete the assigned task using available tools.
`
	}
}

// getToolDefinitions converts registered tools to langchaingo tool definitions
func (s *SubAgent) getToolDefinitions() []llms.Tool {
	tools := s.toolRegistry.GetAll()
	definitions := make([]llms.Tool, len(tools))

	for i, tool := range tools {
		schema := tool.Schema()
		definitions[i] = llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters:  schema,
			},
		}
	}

	return definitions
}

// ToolResultWithName represents a tool result with name
type ToolResultWithName struct {
	Name   string
	Result tools.ToolResult
}

// executeToolCalls executes tool calls and returns results
func (s *SubAgent) executeToolCalls(ctx context.Context, toolCalls []llms.ToolCall) []ToolResultWithName {
	calls := make([]tools.ToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		params := normalizeToolParams(tc.FunctionCall.Name, []byte(tc.FunctionCall.Arguments))
		calls[i] = tools.ToolCall{
			ID:     tc.ID,
			Name:   tc.FunctionCall.Name,
			Params: params,
		}
	}

	results := s.toolRegistry.ExecuteToolsParallel(ctx, calls)

	toolResults := make([]ToolResultWithName, len(results))
	for i, result := range results {
		toolResults[i] = ToolResultWithName{
			Name:   calls[i].Name,
			Result: result.Result,
		}
	}

	return toolResults
}

func (s *SubAgent) checkMissingToolParams(toolCalls []llms.ToolCall) string {
	for _, tc := range toolCalls {
		tool, err := s.toolRegistry.Get(tc.FunctionCall.Name)
		if err != nil {
			continue
		}
		schema := tool.Schema()
		if schema == nil || len(schema.Required) == 0 {
			continue
		}
		params := normalizeToolParams(tc.FunctionCall.Name, []byte(tc.FunctionCall.Arguments))
		missing := findMissingRequiredParams(schema.Required, params)
		if len(missing) == 0 {
			continue
		}
		return buildToolCallExample(tc.FunctionCall.Name, schema.Required)
	}
	return ""
}

func hasSoftToolMarkers(content string) bool {
	return strings.Contains(content, "<tool_call>") || strings.Contains(content, "\"$tool_call\"")
}

func softToolFormatGuidance() string {
	return "Tool call format invalid. Please respond with a JSON tool call like:\n\n```json\n{\"$tool_call\":\"bash\",\"$params\":{\"command\":\"...\",\"description\":\"...\"}}\n```\n\nConstruct the query inside the JSON string."
}

func findMissingRequiredParams(required []string, params []byte) []string {
	if len(required) == 0 {
		return nil
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(params, &payload); err != nil {
		return required
	}
	var missing []string
	for _, key := range required {
		value, ok := payload[key]
		if !ok {
			missing = append(missing, key)
			continue
		}
		if str, ok := value.(string); ok && strings.TrimSpace(str) == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

func buildToolCallExample(toolName string, required []string) string {
	params := make(map[string]string, len(required))
	for _, key := range required {
		params[key] = "..."
	}
	example := map[string]interface{}{
		"$tool_call": toolName,
		"$params":    params,
	}
	serialized, _ := json.Marshal(example)
	return fmt.Sprintf("Tool call is missing required parameters. Please respond with a JSON tool call like:\n\n```json\n%s\n```", string(serialized))
}

func normalizeToolParams(toolName string, params []byte) []byte {
	trimmed := bytes.TrimSpace(params)
	prompts.VerboseLog("tool params raw for %s: %q", toolName, string(trimmed))
	if len(trimmed) == 0 {
		return params
	}
	if json.Valid(trimmed) {
		return params
	}
	if trimmed[0] == '"' {
		var decoded string
		if err := json.Unmarshal(trimmed, &decoded); err != nil {
			return params
		}
		decodedBytes := bytes.TrimSpace([]byte(decoded))
		if len(decodedBytes) == 0 {
			return params
		}
		if json.Valid(decodedBytes) {
			prompts.VerboseLog("tool params normalized from %q to %q", string(trimmed), string(decodedBytes))
			return decodedBytes
		}
		recovered := attemptRecoverToolParams(toolName, string(decodedBytes))
		if recovered != "" && json.Valid([]byte(recovered)) {
			prompts.VerboseLog("tool params recovered for %s: %q", toolName, recovered)
			return []byte(recovered)
		}
		return params
	}
	recovered := attemptRecoverToolParams(toolName, string(trimmed))
	if recovered != "" && json.Valid([]byte(recovered)) {
		prompts.VerboseLog("tool params recovered for %s: %q", toolName, recovered)
		return []byte(recovered)
	}
	return params
}

func attemptRecoverToolParams(toolName, raw string) string {
	if toolName != "bash" {
		return ""
	}

	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if start := strings.Index(s, "{"); start != -1 {
		if end := strings.LastIndex(s, "}"); end != -1 && end > start {
			s = s[start : end+1]
		}
	}

	command, ok := parseJSONField(s, "command")
	if !ok || command == "" {
		return ""
	}

	description := ""
	if idx := strings.Index(raw, "<parameter=description>"); idx != -1 {
		description = strings.TrimSpace(raw[idx+len("<parameter=description>"):])
	}
	if description == "" {
		if descValue, ok := parseJSONField(s, "description"); ok {
			description = descValue
		}
	}

	payload := map[string]string{
		"command": command,
	}
	if description != "" {
		payload["description"] = description
	}
	serialized, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(serialized)
}

func parseJSONField(input, key string) (string, bool) {
	idx := strings.Index(input, key)
	if idx == -1 {
		return "", false
	}
	colon := strings.Index(input[idx:], ":")
	if colon == -1 {
		return "", false
	}
	start := idx + colon + 1
	for start < len(input) && (input[start] == ' ' || input[start] == '\n' || input[start] == '\t') {
		start++
	}
	if start >= len(input) || input[start] != '"' {
		return "", false
	}
	value, _, ok := parseJSONString(input, start)
	return value, ok
}

func parseJSONString(input string, start int) (string, int, bool) {
	if start >= len(input) || input[start] != '"' {
		return "", start, false
	}
	var b strings.Builder
	for i := start + 1; i < len(input); i++ {
		switch input[i] {
		case '\\':
			if i+1 >= len(input) {
				return "", i + 1, false
			}
			b.WriteByte(input[i+1])
			i++
		case '"':
			return b.String(), i + 1, true
		default:
			b.WriteByte(input[i])
		}
	}
	return "", len(input), false
}
