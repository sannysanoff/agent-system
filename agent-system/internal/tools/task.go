package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tmc/langchaingo/llms"
)

// SessionMetadata represents the wrapper format for session files (version 1)
type SessionMetadata struct {
	Version      int                   `json:"version"`
	SubagentType string                `json:"subagent_type"`
	Conversation []llms.MessageContent `json:"conversation"`
}

const CurrentSessionVersion = 1

// getSessionsDir returns the directory for storing session files.
// Uses MYAGENT_SESSIONS_DIRECTORY env var if set, otherwise defaults to ~/.claude/myclaude/sessions
func getSessionsDir() string {
	if customDir := os.Getenv("MYAGENT_SESSIONS_DIRECTORY"); customDir != "" {
		return customDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "myclaude", "sessions")
}

// LoadSessionMetadata loads and validates a session file, returning the metadata.
// This is used to validate subagent_type before resuming a session.
func LoadSessionMetadata(sessionsDir, sessionID string) (*SessionMetadata, error) {
	path := getSessionPathForID(sessionsDir, sessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	var metadata SessionMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse session file: %w", err)
	}

	if metadata.Version != CurrentSessionVersion {
		return nil, fmt.Errorf("incompatible format version (expected %d, got %d)", CurrentSessionVersion, metadata.Version)
	}

	return &metadata, nil
}

func getSessionPathForID(sessionsDir, sessionID string) string {
	return filepath.Join(sessionsDir, sessionID+".json")
}

// Global registry for tracking active resume instances (system-wide)
// This prevents parallel tool calls from resuming the same instance
var (
	activeResumes     = make(map[string]bool)
	activeResumesMu   sync.Mutex
)

// TaskTool implements subagent task spawning
type TaskTool struct {
	defaultModel          string
	allowedAgents         []string
	maxConcurrent         int
	agentFactory          AgentFactory
	parentID              string
	workingDir            string
	getAgentNames         func() []string
	loadAgentContent      func(agentName string) (string, error)
	getParentConversation func() []llms.MessageContent // Callback to get parent's conversation for fork mode
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
	ID                  string        `json:"id,omitempty"`                   // Optional ID (for resume, uses existing session ID)
	SubagentType        string        `json:"subagent_type"`
	Model               string        `json:"model,omitempty"`
	ParentID            string        `json:"parent_id,omitempty"`
	AgentContent        string        `json:"agent_content,omitempty"`
	Fork                bool          `json:"fork,omitempty"`
	Resume              bool          `json:"resume,omitempty"`               // Whether this is a resumed subagent
	ResumeInstanceID    string        `json:"resume_instance_id,omitempty"`   // Session ID to resume from
	InitialConversation []interface{} `json:"initial_conversation,omitempty"` // Forked or resumed conversation
	ForkedPrompt        string        `json:"forked_prompt,omitempty"`        // The special prompt for fork mode
}

// TaskParams represents parameters for task execution
type TaskParams struct {
	Description      string `json:"description"`
	Prompt           string `json:"prompt"`
	SubagentType     string `json:"subagent_type"`
	Model            string `json:"model,omitempty"`
	MaxTurns         *int   `json:"max_turns,omitempty"`
	Fork             bool   `json:"fork,omitempty"`             // When true, subagent forks parent's conversation (gets copy with new GUID)
	ResumeInstanceID string `json:"resume_instance_id,omitempty"` // When set, resume existing subagent session
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
			"max_turns": {
				Type:        "integer",
				Description: "Maximum number of agentic turns before stopping",
			},
			"fork": {
				Type:        "boolean",
				Description: "If true, subagent inherits the parent's conversation history and continues from there",
			},
			"resume_instance_id": {
				Type:        "string",
				Description: "Resume an existing subagent session by its ID. The prompt will be added as a new user message to the existing conversation.",
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

	// Determine if we're resuming, forking, or starting fresh
	isResuming := taskParams.ResumeInstanceID != ""
	isForking := taskParams.Fork && !isResuming // Resume takes precedence over fork

	// Handle resume mode: load existing session
	if isResuming {
		resumeID := taskParams.ResumeInstanceID

		// Check for parallel resume attempts (global protection)
		activeResumesMu.Lock()
		if activeResumes[resumeID] {
			activeResumesMu.Unlock()
			return ToolResult{
				Success: false,
				Error:   "cannot resume same instances in parallel tool calls, use different instances or sequential tool calls",
			}, nil
		}
		// Register as active
		activeResumes[resumeID] = true
		activeResumesMu.Unlock()

		// Ensure cleanup happens even on panic
		defer func() {
			activeResumesMu.Lock()
			delete(activeResumes, resumeID)
			activeResumesMu.Unlock()
		}()

		// Determine sessions directory (same logic as subagent)
		sessionsDir := getSessionsDir()

		// Load session metadata
		metadata, err := LoadSessionMetadata(sessionsDir, resumeID)
		if err != nil {
			return ToolResult{
				Success: false,
				Error:   fmt.Sprintf("failed to resume session: %v", err),
			}, nil
		}

		// Validate subagent_type matches
		if metadata.SubagentType != taskParams.SubagentType {
			return ToolResult{
				Success: false,
				Error:   fmt.Sprintf("subagent_type mismatch: session has '%s', requested '%s'", metadata.SubagentType, taskParams.SubagentType),
			}, nil
		}

		// Set up resume configuration
		// Convert conversation to []interface{}
		clonedConv := make([]interface{}, len(metadata.Conversation))
		for i, msg := range metadata.Conversation {
			clonedConv[i] = msg
		}
		agentConfig.InitialConversation = clonedConv
		agentConfig.Resume = true
		agentConfig.ResumeInstanceID = resumeID
		agentConfig.ID = resumeID // Use the same session ID for continuity
	} else if isForking && t.getParentConversation != nil {
		// Handle fork mode: inherit parent's conversation
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

	// Execute synchronously with detached context to prevent parent cancellation
	// from killing this subagent when other parallel subagents complete
	ctx = context.WithoutCancel(ctx)
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
			"resume_instance_id": agent.ID(),
			"success":            result.Success,
			"fork":               taskParams.Fork,
			"turns_used":         result.TurnsUsed,
			"response":           result.Response,
			"description":        taskParams.Description,
			"subagent_type":      taskParams.SubagentType,
		},
	}, nil
}
