package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// BashTool implements bash command execution
type BashTool struct {
	workingDir      string
	defaultTimeout  time.Duration
	allowedCommands []string
	blockedCommands []string
}

// BashParams represents parameters for bash command execution
type BashParams struct {
	Command                   string `json:"command"`
	Timeout                   *int   `json:"timeout,omitempty"`
	Description               string `json:"description,omitempty"`
	Workdir                   string `json:"workdir,omitempty"`
	RunInBackground           bool   `json:"run_in_background,omitempty"`
	DangerouslyDisableSandbox bool   `json:"dangerously_disable_sandbox,omitempty"`
}

// NewBashTool creates a new bash tool
func NewBashTool(workingDir string, defaultTimeoutMs int, allowedCommands, blockedCommands []string) *BashTool {
	if workingDir == "" {
		workingDir, _ = os.Getwd()
	}

	return &BashTool{
		workingDir:      workingDir,
		defaultTimeout:  time.Duration(defaultTimeoutMs) * time.Millisecond,
		allowedCommands: allowedCommands,
		blockedCommands: blockedCommands,
	}
}

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) Description() string {
	return "Executes a given bash command with optional timeout. Working directory persists between commands. " +
		"The shell environment is initialized from the user's profile (bash or zsh)."
}

func (t *BashTool) Schema() *ToolSchema {
	return &ToolSchema{
		Type: "object",
		Properties: map[string]Property{
			"command": {
				Type:        "string",
				Description: "The command to execute",
			},
			"timeout": {
				Type:        "integer",
				Description: "Optional timeout in milliseconds (max 7200000)",
			},
			"description": {
				Type:        "string",
				Description: "Clear, concise description of what this command does in active voice (5-10 words)",
			},
			"workdir": {
				Type:        "string",
				Description: "Working directory to run the command in. If not specified, uses the current working directory.",
			},
			"run_in_background": {
				Type:        "boolean",
				Description: "Set to true to run this command in the background",
			},
			"dangerously_disable_sandbox": {
				Type:        "boolean",
				Description: "Set this to true to dangerously override sandbox mode",
			},
		},
		Required: []string{"command"},
	}
}

func (t *BashTool) Execute(ctx context.Context, params json.RawMessage) (ToolResult, error) {
	var bashParams BashParams
	if err := json.Unmarshal(params, &bashParams); err != nil {
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse parameters: %v", err),
		}, nil
	}

	// Validate command
	if bashParams.Command == "" {
		return ToolResult{
			Success: false,
			Error:   "command is required",
		}, nil
	}

	// Check blocked commands
	if len(t.blockedCommands) > 0 {
		cmd := strings.Fields(bashParams.Command)[0]
		for _, blocked := range t.blockedCommands {
			if cmd == blocked {
				return ToolResult{
					Success: false,
					Error:   fmt.Sprintf("command '%s' is blocked", cmd),
				}, nil
			}
		}
	}

	// Check allowed commands
	if len(t.allowedCommands) > 0 {
		cmd := strings.Fields(bashParams.Command)[0]
		allowed := false
		for _, allowedCmd := range t.allowedCommands {
			if cmd == allowedCmd {
				allowed = true
				break
			}
		}
		if !allowed {
			return ToolResult{
				Success: false,
				Error:   fmt.Sprintf("command '%s' is not in allowed list", cmd),
			}, nil
		}
	}

	// Determine working directory
	workdir := t.workingDir
	if bashParams.Workdir != "" {
		workdir = bashParams.Workdir
	}

	// Determine timeout
	timeout := t.defaultTimeout
	if bashParams.Timeout != nil && *bashParams.Timeout > 0 {
		timeout = time.Duration(*bashParams.Timeout) * time.Millisecond
		if timeout > 2*time.Hour {
			timeout = 2 * time.Hour
		}
	}

	// Create command context with timeout
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Setup shell execution
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}

	// Create command
	cmd := exec.CommandContext(cmdCtx, shell, "-c", bashParams.Command)
	cmd.Dir = workdir
	cmd.Env = os.Environ()

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run command
	err := cmd.Run()

	output := stdout.String()
	stderrStr := stderr.String()

	// Handle errors
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return ToolResult{
				Success: false,
				Error:   fmt.Sprintf("command timed out after %v", timeout),
				Output:  output,
			}, nil
		}

		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("command failed: %v", err),
			Output:  output,
		}, nil
	}

	result := ToolResult{
		Success: true,
		Output:  output,
		Data: map[string]interface{}{
			"stdout":    output,
			"stderr":    stderrStr,
			"exit_code": 0,
		},
	}

	if stderrStr != "" {
		result.Data.(map[string]interface{})["stderr"] = stderrStr
	}

	return result, nil
}
