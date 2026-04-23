package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/tmc/langchaingo/llms"
)

// TaskTool implements subagent task spawning
type TaskTool struct {
	defaultModel          string
	allowedAgents         []string
	maxConcurrent         int
	agentFactory          AgentFactory
	activeAgents          map[string]*SubAgent
	parentID              string
	workingDir            string
	getAgentNames         func() []string
	loadAgentContent      func(agentName string) (string, error)
	getParentConversation func() []llms.MessageContent // Callback to get parent's conversation for fork mode
	mu                    sync.RWMutex
}

// AgentFactory is a function that creates subagents
type AgentFactory func(config SubAgentConfig) (SubAgent, error)

// SubAgent represents a subagent that can execute tasks
type SubAgent interface {
	ID() string
	Execute(ctx context.Context, prompt string, maxTurns int) (SubAgentResult, error)
}

// SubAgentResult represents the result from a subagent
type SubAgentResult struct {
	Success   bool   `json:"success"`
	Response  string `json:"response"`
	TurnsUsed int    `json:"turns_used"`
	Error     string `json:"error,omitempty"`
}

// SubAgentConfig represents configuration for creating a subagent
type SubAgentConfig struct {
	SubagentType        string        `json:"subagent_type"`
	Model               string        `json:"model,omitempty"`
	ParentID            string        `json:"parent_id,omitempty"`
	AgentContent        string        `json:"agent_content,omitempty"`
	Fork                bool          `json:"fork,omitempty"`
	InitialConversation []interface{} `json:"initial_conversation,omitempty"` // Forked conversation from parent
	ForkedPrompt        string        `json:"forked_prompt,omitempty"`        // The special prompt for fork mode
}

// TaskParams represents parameters for task execution
type TaskParams struct {
	Description     string `json:"description"`
	Prompt          string `json:"prompt"`
	SubagentType    string `json:"subagent_type"`
	Model           string `json:"model,omitempty"`
	Resume          string `json:"resume,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
	MaxTurns        *int   `json:"max_turns,omitempty"`
	Fork            bool   `json:"fork,omitempty"` // When true, subagent forks parent's conversation (gets copy with new GUID)
}

// NewTaskTool creates a new task tool
func NewTaskTool(defaultModel string, allowedAgents []string, maxConcurrent int, parentID string, factory AgentFactory, workingDir string, getAgentNames func() []string, loadAgentContent func(agentName string) (string, error), getParentConversation func() []llms.MessageContent) *TaskTool {
	if maxConcurrent == 0 {
		maxConcurrent = 5
	}
	return &TaskTool{
		defaultModel:          defaultModel,
		allowedAgents:         allowedAgents,
		maxConcurrent:         maxConcurrent,
		agentFactory:          factory,
		activeAgents:          make(map[string]*SubAgent),
		parentID:              parentID,
		workingDir:            workingDir,
		getAgentNames:         getAgentNames,
		loadAgentContent:      loadAgentContent,
		getParentConversation: getParentConversation,
	}
}

func (t *TaskTool) Name() string {
	return "task"
}

func (t *TaskTool) Description() string {
	return "Launch a new agent to handle complex, multi-step tasks autonomously. " +
		"The Task tool launches specialized agents (subprocesses) that autonomously handle complex tasks. " +
		"Final response of subagent must be passed to caller."
}

func (t *TaskTool) Schema() *ToolSchema {
	// Build enum from built-in types plus available agents from .claude/agents/
	builtinTypes := []string{"Bash", "Explore", "general-purpose", "Plan", "code-reviewer", "claude-code-guide"}

	var enumValues []string
	enumValues = append(enumValues, builtinTypes...)

	// Add dynamically discovered agents
	if t.getAgentNames != nil {
		agentNames := t.getAgentNames()
		for _, name := range agentNames {
			// Skip duplicates
			found := false
			for _, builtin := range builtinTypes {
				if builtin == name {
					found = true
					break
				}
			}
			if !found {
				enumValues = append(enumValues, name)
			}
		}
	}

	return &ToolSchema{
		Type: "object",
		Properties: map[string]Property{
			"description": {
				Type:        "string",
				Description: "A short (3-5 word) description of the task",
			},
			"prompt": {
				Type:        "string",
				Description: "The task for the agent to perform",
			},
			"subagent_type": {
				Type:        "string",
				Description: "The type of specialized agent to use for this task",
				Enum:        enumValues,
			},
			"model": {
				Type:        "string",
				Description: "Optional model to use for this agent (e.g., 'sonnet', 'opus', 'haiku')",
			},
			"resume": {
				Type:        "string",
				Description: "Optional agent ID to resume from",
			},
			"run_in_background": {
				Type:        "boolean",
				Description: "Set to true to run this agent in the background",
			},
			"max_turns": {
				Type:        "integer",
				Description: "Maximum number of agentic turns before stopping",
			},
			"fork": {
				Type:        "boolean",
				Description: "If true, subagent inherits the parent's conversation history and continues from there",
			},
		},
		Required: []string{"description", "prompt", "subagent_type"},
	}
}

func (t *TaskTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var taskParams TaskParams
	if err := json.Unmarshal(params, &taskParams); err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse parameters: %v", err),
		}, nil
	}

	// Validate parameters
	if taskParams.Description == "" {
		return ToolResult{
			Success: false,
			Error:   "description is required",
		}, nil
	}

	if taskParams.Prompt == "" {
		return ToolResult{
			Success: false,
			Error:   "prompt is required",
		}, nil
	}

	if taskParams.SubagentType == "" {
		return ToolResult{
			Success: false,
			Error:   "subagent_type is required",
		}, nil
	}

	// Check allowed agents
	if len(t.allowedAgents) > 0 {
		allowed := false
		for _, agent := range t.allowedAgents {
			if agent == taskParams.SubagentType {
				allowed = true
				break
			}
		}
		if !allowed {
			return ToolResult{
				Success: false,
				Error:   fmt.Sprintf("agent type '%s' is not allowed", taskParams.SubagentType),
			}, nil
		}
	}

	// Use default model if not specified
	model := taskParams.Model
	if model == "" {
		model = t.defaultModel
	}

	// Set max turns
	maxTurns := 50
	if taskParams.MaxTurns != nil {
		maxTurns = *taskParams.MaxTurns
	}

	// Handle resume case
	if taskParams.Resume != "" {
		return t.resumeAgent(ctx, taskParams.Resume)
	}

	// Load agent content if it's a custom agent (not a built-in type)
	var agentContent string
	builtinTypes := map[string]bool{
		"Bash":              true,
		"Explore":           true,
		"general-purpose":   true,
		"Plan":              true,
		"code-reviewer":     true,
		"claude-code-guide": true,
	}

	if !builtinTypes[taskParams.SubagentType] && t.loadAgentContent != nil {
		content, err := t.loadAgentContent(taskParams.SubagentType)
		if err == nil {
			agentContent = content
		}
	}

	// Create subagent
	agentConfig := SubAgentConfig{
		SubagentType: taskParams.SubagentType,
		Model:        model,
		ParentID:     t.parentID,
		AgentContent: agentContent,
	}

	// Handle fork mode: inherit parent's conversation
	if taskParams.Fork && t.getParentConversation != nil {
		parentConv := t.getParentConversation()
		if len(parentConv) > 0 {
			// Clone the conversation
			clonedConv := make([]interface{}, len(parentConv))
			for i, msg := range parentConv {
				clonedConv[i] = msg
			}
			agentConfig.InitialConversation = clonedConv

			// Build the special forked prompt
			forkedPrompt := fmt.Sprintf("now you act as subagent %s.\n\n<system prompt of %s>\n%s\n</system prompt>\n\nyour current task:\n\n%s",
				taskParams.SubagentType, taskParams.SubagentType, agentContent, taskParams.Prompt)
			agentConfig.ForkedPrompt = forkedPrompt
			agentConfig.Fork = true
		}
	}

	agent, err := t.agentFactory(agentConfig)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to create subagent: %v", err),
		}, nil
	}

	// Store agent if running in background
	if taskParams.RunInBackground {
		t.mu.Lock()
		t.activeAgents[agent.ID()] = &agent
		t.mu.Unlock()

		// Execute in background
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()

			// Use forked prompt if in fork mode, otherwise use regular prompt
			bgPrompt := taskParams.Prompt
			if taskParams.Fork && agentConfig.ForkedPrompt != "" {
				bgPrompt = agentConfig.ForkedPrompt
			}
			agent.Execute(ctx, bgPrompt, maxTurns)

			t.mu.Lock()
			delete(t.activeAgents, agent.ID())
			t.mu.Unlock()
		}()

		return ToolResult{
			Success: true,
			Output:  fmt.Sprintf("Agent started in background. ID: %s", agent.ID()),
			Data: map[string]interface{}{
				"agent_id":      agent.ID(),
				"background":    true,
				"description":   taskParams.Description,
				"subagent_type": taskParams.SubagentType,
			},
		}, nil
	}

	// Execute synchronously
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	// Use forked prompt if in fork mode, otherwise use regular prompt
	executePrompt := taskParams.Prompt
	if taskParams.Fork && agentConfig.ForkedPrompt != "" {
		executePrompt = agentConfig.ForkedPrompt
	}

	result, err := agent.Execute(ctx, executePrompt, maxTurns)
	if err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("agent execution failed: %v", err),
		}, nil
	}

	return ToolResult{
		Success: result.Success,
		Output:  result.Response,
		Data: map[string]interface{}{
			"agent_id":      agent.ID(),
			"success":       result.Success,
			"turns_used":    result.TurnsUsed,
			"response":      result.Response,
			"description":   taskParams.Description,
			"subagent_type": taskParams.SubagentType,
		},
	}, nil
}

func (t *TaskTool) resumeAgent(ctx context.Context, agentID string) (ToolResult, error) {
	t.mu.RLock()
	_, exists := t.activeAgents[agentID]
	t.mu.RUnlock()

	if !exists {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("agent with ID '%s' not found", agentID),
		}, nil
	}

	// Return current status
	return ToolResult{
		Success: true,
		Output:  fmt.Sprintf("Agent %s is still running in background", agentID),
		Data: map[string]interface{}{
			"agent_id":   agentID,
			"running":    true,
			"background": true,
		},
	}, nil
}
