package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jedib0t/go-pretty/table"

	"agent-system/internal/agent"
	"agent-system/internal/config"
	"agent-system/internal/prompts"
)

func main() {
	var (
		configPath     = flag.String("config", "config.yaml", "Path to configuration file")
		modelName      = flag.String("model", "", "Model name to use (overrides default)")
		shortModelName = flag.String("m", "", "Model name to use (shorthand)")
		workingDir     = flag.String("workdir", "", "Working directory (defaults to current)")
		maxTurns       = flag.Int("max-turns", 50, "Maximum agentic turns")
		prompt         = flag.String("p", "", "Single step prompt to execute")
		resumeID       = flag.String("r", "", "Resume conversation from session ID")
		jsonOutput     = flag.Bool("json", false, "Output in JSON format")
		rawOutput      = flag.Bool("raw", false, "Output only the final assistant response text (for scripting)")
		noNoSession    = flag.Bool("no-session", false, "Disable session saving (not implemented yet, but for pi compatibility)")
		tools          = flag.String("tools", "DEFAULT", "Tools to enable (comma separated, for pi compatibility). Pass empty string to list tools.")
		readLimit      = flag.Int("read-limit", 80000, "Maximum bytes to read from a file")
		agentName      = flag.String("a", "", "Agent name to load default prompt from .claude/agents/ (use -a alone to list agents)")
		debugPrompt    = flag.Bool("debug-prompt", false, "Dump system prompt and initial user prompt, then exit")
		outputFile     = flag.String("out", "", "Also write final assistant response to file (no thinking, no tool calls). Does not suppress stdout.")
	)

	// Custom flag parsing for -tools
	for i, arg := range os.Args {
		if arg == "-tools" || arg == "--tools" {
			if i+1 >= len(os.Args) || strings.HasPrefix(os.Args[i+1], "-") {
				// No value provided for -tools, list tools
				listTools(*configPath)
				return
			}
		}
	}

	// Check if -m flag is provided without value (for listing models)
	if isListModelsFlag() {
		if err := listModels(*configPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error listing models: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Check if -a flag is provided (for agents)
	agentResult := checkAgentFlag(*agentName, *workingDir)
	if agentResult == "LIST" {
		// List agents and exit
		if err := listAgents(*workingDir); err != nil {
			fmt.Fprintf(os.Stderr, "Error listing agents: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if agentResult == "INVALID" {
		// Invalid agent name provided - fail hard
		os.Exit(1)
	}

	flag.Parse()

	_ = *noNoSession

	selectedModel := *modelName
	if *shortModelName != "" {
		selectedModel = *shortModelName
	}

	// Parse prompt: if starts with @, read from file
	promptContent := *prompt
	if strings.HasPrefix(promptContent, "@") {
		filename := promptContent[1:]
		data, err := os.ReadFile(filename)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading prompt file %s: %v\n", filename, err)
			os.Exit(1)
		}
		promptContent = string(data)
	}

	// Determine working directory
	wd := *workingDir
	if wd == "" {
		var err error
		wd, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting working directory: %v\n", err)
			os.Exit(1)
		}
	}

	// Check if it's a git repo
	isGitRepo := checkIsGitRepo(wd)

	// Resolve config path: first check current dir, then binary's directory
	configPathAbs, err := resolveConfigPath(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving config path: %v\n", err)
		os.Exit(1)
	}

	// Load agent content if specified
	var agentContent string
	if *agentName != "" {
		content, err := loadAgentContent(*agentName, wd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading agent '%s': %v\n", *agentName, err)
			os.Exit(1)
		}
		agentContent = content
		if !*jsonOutput && !*rawOutput {
			fmt.Printf("Loaded agent: %s\n", *agentName)
		}
	}

	// Create agent options
	var enabledTools []string
	if *tools != "DEFAULT" {
		enabledTools = strings.Split(*tools, ",")
		for i, t := range enabledTools {
			enabledTools[i] = strings.TrimSpace(t)
		}
	}

	options := agent.AgentOptions{
		ConfigPath:   configPathAbs,
		ModelName:    selectedModel,
		WorkingDir:   wd,
		IsGitRepo:    isGitRepo,
		MaxTurns:     *maxTurns,
		SessionID:    *resumeID,
		JSONOutput:   *jsonOutput,
		RawOutput:    *rawOutput,
		NoSession:    *noNoSession,
		EnabledTools: enabledTools,
		ReadLimit:    *readLimit,
		AgentContent: agentContent,
		DebugPrompt:  *debugPrompt,
		OutputFile:   *outputFile,
	}

	// Create agent
	if !*jsonOutput && !*rawOutput {
		if *prompt == "" && *resumeID == "" {
			fmt.Println("Initializing agent system...")
			fmt.Printf("Config: %s\n", configPathAbs)
			fmt.Printf("Working directory: %s\n", wd)
		}

		if *resumeID != "" {
			fmt.Printf("Resuming session: %s\n", *resumeID)
		}
	}

	a, err := agent.NewAgent(options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating agent: %v\n", err)
		os.Exit(1)
	}

	if *resumeID == "last" && !*jsonOutput && !*rawOutput {
		fmt.Printf("Resumed last session: %s\n", a.GetSessionID())
	}

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupts
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		if *prompt == "" {
			fmt.Println("\nReceived interrupt signal. Shutting down...")
		}
		cancel()
	}()

	// If prompt is provided, run as single step
	if *prompt != "" {
		if err := a.RunSingle(ctx, promptContent); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if !*rawOutput && !*jsonOutput {
			fmt.Printf("\nTo resume this conversation: ./main -m %s -r %s\n", a.GetSessionID(), a.GetSessionID())
		}
		return
	}

	// Run the agent interactively
	if err := a.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error running agent: %v\n", err)
		os.Exit(1)
	}
}

// isListModelsFlag checks if -m flag is provided without a value
func isListModelsFlag() bool {
	args := os.Args[1:] // Skip program name
	for i, arg := range args {
		if arg == "-m" {
			// Check if there's a next argument and it's not a flag
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				return false // -m has a value
			}
			return true // -m without value or at end
		}
	}
	return false
}

// checkAgentFlag checks if -a flag is provided and returns action needed
// Returns: "LIST" to list agents, "VALID" to use provided agent, "INVALID" for invalid agent, "NONE" to skip
func checkAgentFlag(agentName string, workingDir string) string {
	args := os.Args[1:]
	for i, arg := range args {
		if arg == "-a" || arg == "--agent" {
			// Check if there's a next argument and it's not a flag
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				// Agent name provided - validate it exists
				providedName := args[i+1]
				if !agentExists(providedName, workingDir) {
					fmt.Fprintf(os.Stderr, "Error: Agent '%s' not found\n\n", providedName)
					return "INVALID" // Invalid agent - fail hard
				}
				return "VALID"
			}
			return "LIST" // -a without value
		}
	}
	return "NONE" // -a not provided
}

// getAgentsDirs returns all agents directories in priority order
// Priority: 1. MYAGENT_CONFIG_DIR/agents (if set, disables defaults)
//           2. .claude/agents (project)
//           3. ~/.claude/agents (global)
func getAgentsDirs(workingDir string) []string {
	// Check for MYAGENT_CONFIG_DIR - if set, use only that directory
	if customDir := os.Getenv("MYAGENT_CONFIG_DIR"); customDir != "" {
		return []string{filepath.Join(customDir, "agents")}
	}

	var dirs []string

	// 1. .claude/agents (project)
	if workingDir != "" {
		dirs = append(dirs, filepath.Join(workingDir, ".claude", "agents"))
	}

	// 2. ~/.claude/agents (global)
	homeDir, _ := os.UserHomeDir()
	dirs = append(dirs, filepath.Join(homeDir, ".claude", "agents"))

	return dirs
}

// findAgentFile searches for an agent file across all agent directories
// Returns the full path if found, empty string if not found
func findAgentFile(agentName string, workingDir string) string {
	agentsDirs := getAgentsDirs(workingDir)
	for _, dir := range agentsDirs {
		agentPath := filepath.Join(dir, agentName+".md")
		if _, err := os.Stat(agentPath); err == nil {
			return agentPath
		}
	}
	return ""
}

// agentExists checks if an agent file exists in any of the agent directories
func agentExists(agentName string, workingDir string) bool {
	return findAgentFile(agentName, workingDir) != ""
}

// listAgents lists all available agents from all directories
func listAgents(workingDir string) error {
	agentsDirs := getAgentsDirs(workingDir)

	// Collect agents from all directories with deduplication
	seenAgents := make(map[string]bool)
	var agents []struct {
		name        string
		description string
		source      string
	}

	for _, dir := range agentsDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			// Directory doesn't exist or can't be read, skip
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".md") {
				continue
			}

			agentName := strings.TrimSuffix(name, ".md")
			// Skip if already seen (higher priority)
			if seenAgents[agentName] {
				continue
			}
			seenAgents[agentName] = true

			description := getAgentDescription(filepath.Join(dir, name))

			// Determine source label
			source := "project"
			if strings.Contains(dir, os.Getenv("MYAGENT_CONFIG_DIR")) && os.Getenv("MYAGENT_CONFIG_DIR") != "" {
				source = "custom"
			} else if strings.HasPrefix(dir, os.Getenv("HOME")) {
				source = "global"
			}

			agents = append(agents, struct {
				name        string
				description string
				source      string
			}{agentName, description, source})
		}
	}

	if len(agents) == 0 {
		fmt.Println("No agents found.")
		for _, dir := range agentsDirs {
			fmt.Printf("  Checked: %s\n", dir)
		}
		return nil
	}

	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.AppendHeader(table.Row{"Agent Name", "Description", "Source"})

	for _, agent := range agents {
		t.AppendRow(table.Row{agent.name, agent.description, agent.source})
	}

	t.Render()
	return nil
}

// getAgentDescription extracts description from agent file
// Looks for YAML frontmatter description field, or first heading
func getAgentDescription(agentPath string) string {
	data, err := os.ReadFile(agentPath)
	if err != nil {
		return ""
	}

	content := string(data)

	// Check for YAML frontmatter description
	if strings.HasPrefix(content, "---") {
		endIdx := strings.Index(content[3:], "---")
		if endIdx != -1 {
			frontmatter := content[3 : endIdx+3]
			for _, line := range strings.Split(frontmatter, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "description:") {
					desc := strings.TrimSpace(strings.TrimPrefix(line, "description:"))
					// Remove quotes if present
					desc = strings.Trim(desc, `"`)
					return desc
				}
			}
		}
	}

	// Fallback: look for first heading
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}

	return ""
}

// loadAgentContent loads the full content of an agent file from the first found location
func loadAgentContent(agentName string, workingDir string) (string, error) {
	agentPath := findAgentFile(agentName, workingDir)
	if agentPath == "" {
		return "", fmt.Errorf("agent '%s' not found in any agents directory", agentName)
	}
	data, err := os.ReadFile(agentPath)
	if err != nil {
		return "", fmt.Errorf("failed to read agent file: %w", err)
	}
	return string(data), nil
}

// listModels loads config and lists all available models
func listModels(configPath string) error {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.AppendHeader(table.Row{"ID", "Name", "Provider", "Base URL", "Native Model Name", "Soft Tools"})

	for name, model := range cfg.Models {
		baseURL := model.BaseURL
		if baseURL == "" {
			baseURL = "-"
		}
		t.AppendRow(table.Row{name, model.Name, model.Provider, baseURL, model.ModelID, model.SoftTools})
	}

	t.Render()
	return nil
}

// listTools loads config and lists all available tools and their enabled status
func listTools(configPath string) error {
	configPathAbs, _ := resolveConfigPath(configPath)
	cfg, err := config.LoadConfig(configPathAbs)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.AppendHeader(table.Row{"Tool Name", "Enabled (Config)"})

	t.AppendRow(table.Row{"bash", cfg.Tools.Bash.Enabled})
	t.AppendRow(table.Row{"read", cfg.Tools.Read.Enabled})
	t.AppendRow(table.Row{"write", cfg.Tools.Write.Enabled})
	t.AppendRow(table.Row{"edit", cfg.Tools.Edit.Enabled})
	t.AppendRow(table.Row{"glob", cfg.Tools.Glob.Enabled})
	t.AppendRow(table.Row{"grep", cfg.Tools.Grep.Enabled})
	t.AppendRow(table.Row{"task", cfg.Tools.Task.Enabled})
	t.AppendRow(table.Row{"ask_user", cfg.Tools.AskUser.Enabled})
	t.AppendRow(table.Row{"webfetch", cfg.Tools.WebFetch.Enabled})
	t.AppendRow(table.Row{"websearch", cfg.Tools.WebSearch.Enabled})
	t.AppendRow(table.Row{"skill", cfg.Tools.Skill.Enabled})

	t.Render()
	return nil
}

// checkIsGitRepo checks if the directory is a git repository
func checkIsGitRepo(dir string) bool {
	gitDir := filepath.Join(dir, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// resolveConfigPath resolves config file path in order:
// 1. Current working directory
// 2. User config directory (~/.myagent/)
// 3. Directory where the binary is located
func resolveConfigPath(configFile string) (string, error) {
	// If it's an absolute path, use it directly
	if filepath.IsAbs(configFile) {
		return filepath.Abs(configFile)
	}

	// Try current working directory first
	cwd, err := os.Getwd()
	if err == nil {
		cwdPath := filepath.Join(cwd, configFile)
		if _, err := os.Stat(cwdPath); err == nil {
			if prompts.IsVerbose() {
				fmt.Printf("[VERBOSE] Using config from current dir: %s\n", cwdPath)
			}
			return filepath.Abs(cwdPath)
		}
	}

	// Try user config directory ~/.myagent/
	if homeDir, err := os.UserHomeDir(); err == nil {
		userConfigDir := filepath.Join(homeDir, ".myagent")
		userConfigPath := filepath.Join(userConfigDir, configFile)
		if _, err := os.Stat(userConfigPath); err == nil {
			if prompts.IsVerbose() {
				fmt.Printf("[VERBOSE] Using config from user dir: %s\n", userConfigPath)
			}
			return filepath.Abs(userConfigPath)
		}
	}

	// Try binary's directory (resolve symlinks first)
	exePath, err := os.Executable()
	if err == nil {
		// Resolve any symlinks to get the actual binary path
		resolvedPath, err := filepath.EvalSymlinks(exePath)
		if err == nil {
			binDir := filepath.Dir(resolvedPath)
			binPath := filepath.Join(binDir, configFile)
			if _, err := os.Stat(binPath); err == nil {
				if prompts.IsVerbose() {
					fmt.Printf("[VERBOSE] Using config from binary dir: %s\n", binPath)
				}
				return filepath.Abs(binPath)
			}
		}
	}

	// Fallback to current directory path
	return filepath.Abs(filepath.Join(cwd, configFile))
}
