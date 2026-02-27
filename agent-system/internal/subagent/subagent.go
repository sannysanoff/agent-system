package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tmc/langchaingo/llms"

	"agent-system/internal/tools"
	"agent-system/internal/usage"
)

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
}

// SubAgentOptions represents options for creating a subagent
type SubAgentOptions struct {
	ID         string
	ParentID   string
	AgentType  string
	LLM        llms.Model
	Tools      []tools.Tool
	MaxTurns   int
	JSONOutput bool
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

	return &SubAgent{
		id:           id,
		parentID:     options.ParentID,
		sessionID:    fmt.Sprintf("%s-subagent-%s", options.ParentID, id),
		agentType:    options.AgentType,
		llm:          options.LLM,
		toolRegistry: registry,
		conversation: make([]llms.MessageContent, 0),
		maxTurns:     options.MaxTurns,
		turnsUsed:    0,
		jsonOutput:   options.JSONOutput,
	}
}

func (s *SubAgent) logf(role string, format string, args ...interface{}) {
	s.logfWithMeta(role, nil, nil, format, args...)
}

const maxToolResultLength = 200

func (s *SubAgent) logToolResult(toolName string, resultJSON []byte) {
	resultLen := len(resultJSON)

	if resultLen <= maxToolResultLength {
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
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "myclaude", "sessions", s.sessionID+".json")
}

func (s *SubAgent) saveSession() error {
	path := s.getSessionPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s.conversation, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

func (s *SubAgent) addMessage(msg llms.MessageContent) {
	s.conversation = append(s.conversation, msg)
	s.saveSession()
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

	// Build system prompt based on agent type
	systemPrompt := s.buildSystemPrompt()

	// Initialize conversation
	if len(s.conversation) == 0 {
		s.addMessage(llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt))
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
		contentParts := make([]llms.ContentPart, 0)
		if choice.Content != "" {
			contentParts = append(contentParts, llms.TextContent{Text: choice.Content})
			s.logfWithMeta("ASSISTANT", &currentUsage, nil, "%s\n", choice.Content)
		}
		for _, tc := range choice.ToolCalls {
			contentParts = append(contentParts, tc)
		}
		s.addMessage(llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: contentParts,
		})

		// Check for tool calls
		if len(choice.ToolCalls) == 0 {
			// No tool calls, return final response
			return tools.SubAgentResult{
				Success:   true,
				Response:  choice.Content,
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
		calls[i] = tools.ToolCall{
			ID:     tc.ID,
			Name:   tc.FunctionCall.Name,
			Params: []byte(tc.FunctionCall.Arguments),
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
