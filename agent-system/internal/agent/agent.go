package agent

import (
	"agent-system/pkg/llm/bedrock_invoke"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
	"github.com/tmc/langchaingo/llms/bedrock"
	"github.com/tmc/langchaingo/llms/ollama"
	"github.com/tmc/langchaingo/llms/openai"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	"agent-system/internal/config"
	"agent-system/internal/prompts"
	"agent-system/internal/subagent"
	"agent-system/internal/tools"
	"agent-system/internal/usage"
)

// getSessionsDir returns the directory for storing session files.
// Uses MYAGENT_SESSIONS_DIRECTORY env var if set, otherwise defaults to ~/.claude/myclaude/sessions
func getSessionsDir() string {
	customDir := os.Getenv("MYAGENT_SESSIONS_DIRECTORY")
	prompts.VerboseLog("MYAGENT_SESSIONS_DIRECTORY env var: %q", customDir)
	if customDir != "" {
		prompts.VerboseLog("Using custom sessions directory: %s", customDir)
		return customDir
	}
	home, _ := os.UserHomeDir()
	defaultDir := filepath.Join(home, ".claude", "myclaude", "sessions")
	prompts.VerboseLog("Using default sessions directory: %s", defaultDir)
	return defaultDir
}

// Agent represents the main agent with tools and LLM
type Agent struct {
	config           *config.AgentConfig
	modelConfig      config.ModelConfig
	modelKey         string
	llm              llms.Model
	toolRegistry     *tools.ToolRegistry
	promptBuilder    *prompts.SystemPromptBuilder
	conversation     []llms.MessageContent
	maxTurns         int
	currentTurn      int
	sessionID        string
	jsonOutput       bool
	rawOutput        bool
	noSession        bool
	readLimit        int
	debugPrompt      bool
	outputFile       string
	finalResponse    string
	launchMode       string // Track launch mode: "new", "last", or "restore"
	historyLoaded    bool   // Track if history was successfully loaded
}

// AgentOptions represents options for creating an agent
type AgentOptions struct {
	ConfigPath   string
	ModelName    string
	WorkingDir   string
	IsGitRepo    bool
	MaxTurns     int
	SessionID    string
	JSONOutput   bool
	RawOutput    bool
	NoSession    bool
	EnabledTools []string
	ReadLimit    int
	AgentContent string // Content from -a agent file
	DebugPrompt  bool   // Dump system prompt and exit
	OutputFile   string // Write final response to file
}

// NewAgent creates a new agent with the specified options
func NewAgent(options AgentOptions) (*Agent, error) {
	// Load configuration
	cfg, err := config.LoadConfig(options.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	// Get model configuration
	modelConfig, err := cfg.GetModel(options.ModelName)
	if err != nil {
		return nil, fmt.Errorf("failed to get model config: %w", err)
	}

	// Create LLM
	llm, err := createLLM(modelConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM: %w", err)
	}

	// Determine which tools are enabled based on CLI and config
	enabledToolsList := computeEnabledTools(options.EnabledTools, cfg)

	// Create prompt builder with enabled tools and agent content
	promptBuilder := prompts.NewSystemPromptBuilder(options.WorkingDir, options.IsGitRepo, enabledToolsList)
	promptBuilder.SetAgentContent(options.AgentContent)

	// Create tool registry
	toolRegistry := tools.NewToolRegistry()

	// Set sessions directory for large output file storage
	toolRegistry.SetSessionsDir(getSessionsDir())

	// Create agent
	agent := &Agent{
		config:        cfg,
		modelConfig:   modelConfig,
		modelKey:      options.ModelName,
		llm:           llm,
		toolRegistry:  toolRegistry,
		promptBuilder: promptBuilder,
		conversation:  make([]llms.MessageContent, 0),
		maxTurns:      options.MaxTurns,
		currentTurn:   0,
		sessionID:     options.SessionID,
		jsonOutput:    options.JSONOutput,
		rawOutput:     options.RawOutput,
		noSession:     options.NoSession,
		readLimit:     options.ReadLimit,
		debugPrompt:   options.DebugPrompt,
		outputFile:    options.OutputFile,
	}

	if agent.sessionID == "" {
		agent.sessionID = uuid.New().String()
		agent.launchMode = "new"
		agent.historyLoaded = false
	} else if agent.sessionID == "last" {
		lastID, err := findLastSession()
		if err != nil {
			// No sessions found, silently start a new session
			agent.sessionID = uuid.New().String()
			agent.launchMode = "new"
			agent.historyLoaded = false
		} else {
			agent.sessionID = lastID
			agent.launchMode = "last"
			if err := agent.loadSession(); err != nil {
				return nil, fmt.Errorf("failed to load last session %s: %w", agent.sessionID, err)
			}
			agent.historyLoaded = true
		}
	} else {
		agent.launchMode = "restore"
		if err := agent.loadSession(); err != nil {
			return nil, fmt.Errorf("failed to load session: %w", err)
		}
		agent.historyLoaded = true
	}

	// Register tools based on configuration
	agent.registerTools(toolRegistry, cfg, options.WorkingDir, true, enabledToolsList)

	if agent.maxTurns == 0 {
		agent.maxTurns = 50
	}

	// Log launch info in JSON mode
	if agent.jsonOutput {
		agent.logLaunchInfo()
	}

	return agent, nil
}

func (a *Agent) logf(role string, format string, args ...interface{}) {
	if a.rawOutput {
		return
	}
	a.logfWithUsage(role, nil, format, args...)
}

const maxToolResultLength = 200

func (a *Agent) logToolResult(toolName string, resultJSON []byte) {
	resultLen := len(resultJSON)

	// In JSON output mode, never truncate - we need valid JSON
	if a.jsonOutput || resultLen <= maxToolResultLength {
		a.logf("TOOL_RESULT", "%s\n", string(resultJSON))
	} else {
		truncated := string(resultJSON[:maxToolResultLength])
		a.logf("TOOL_RESULT", "%s... (truncated, %d bytes)\n", truncated, resultLen)
	}
}

func (a *Agent) logfWithUsage(role string, u *usage.Usage, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if a.rawOutput && role != "ASSISTANT" {
		return
	}
	if a.rawOutput && role == "ASSISTANT" {
		fmt.Print(stripReasoningTags(msg))
		return
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	if a.jsonOutput {
		progressData := map[string]interface{}{
			"role":    role,
			"content": strings.TrimSuffix(msg, "\n"),
		}
		if u != nil {
			progressData["usage"] = u
		}
		out := map[string]interface{}{
			"timestamp": timestamp,
			"type":      "progress",
			"data":      progressData,
		}
		jsonBytes, _ := json.Marshal(out)
		fmt.Println(string(jsonBytes))
	} else {
		prefix := fmt.Sprintf("[%s] %s: ", timestamp, role)
		fmt.Print(prefix + msg)
		// Print usage info in non-JSON mode for every LLM response
		if u != nil && (u.InputTokens > 0 || u.OutputTokens > 0) {
			if u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 {
				fmt.Printf("[%s] USAGE: input=%d output=%d cache_read=%d cache_write=%d\n",
					timestamp, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
			} else {
				fmt.Printf("[%s] USAGE: input=%d output=%d\n",
					timestamp, u.InputTokens, u.OutputTokens)
			}
		}
	}
}

func (a *Agent) logJSONProgress(role string, data interface{}) {
	a.logJSONProgressWithMeta(role, data, nil)
}

func (a *Agent) logJSONProgressWithMeta(role string, data interface{}, meta map[string]interface{}) {
	if !a.jsonOutput {
		return
	}
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	progressData := map[string]interface{}{
		"role": role,
		"raw":  data,
	}
	if role == "TOOL_RESULT" || role == "TOOL_CALL" || role == "ASSISTANT" || role == "USER" {
		if s, ok := data.(string); ok {
			progressData["content"] = strings.TrimSuffix(s, "\n")
		}
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
}

func (a *Agent) logEndTurn() {
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	out := map[string]interface{}{
		"timestamp": timestamp,
		"type":      "endturn",
		"data": map[string]interface{}{
			"session_id": a.sessionID,
			"model":      a.modelKey,
		},
	}
	jsonBytes, _ := json.Marshal(out)
	fmt.Println(string(jsonBytes))
}

func (a *Agent) logLaunchInfo() {
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	out := map[string]interface{}{
		"timestamp": timestamp,
		"type":      "launch",
		"data": map[string]interface{}{
			"session_id":     a.sessionID,
			"history_file":   a.getSessionPath(),
			"launch_mode":    a.launchMode,
			"history_loaded": a.historyLoaded,
		},
	}
	jsonBytes, _ := json.Marshal(out)
	fmt.Println(string(jsonBytes))
}

func (a *Agent) getSessionPath() string {
	return filepath.Join(getSessionsDir(), a.sessionID+".json")
}

func (a *Agent) saveSession() error {
	if a.noSession {
		return nil
	}
	path := a.getSessionPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(a.conversation, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

func (a *Agent) loadSession() error {
	path := a.getSessionPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Log to stderr even in JSON mode
	fmt.Fprintf(os.Stderr, "restoring session from %s\n", path)

	return json.Unmarshal(data, &a.conversation)
}

func (a *Agent) GetSessionID() string {
	return a.sessionID
}

func findLastSession() (string, error) {
	sessionsDir := getSessionsDir()
	prompts.VerboseLog("findLastSession: searching in directory: %s", sessionsDir)
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no sessions found")
		}
		return "", err
	}

	var lastSession string
	var lastTime time.Time

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if strings.Contains(entry.Name(), "-subagent") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().After(lastTime) {
			lastTime = info.ModTime()
			lastSession = strings.TrimSuffix(entry.Name(), ".json")
		}
	}

	if lastSession == "" {
		prompts.VerboseLog("findLastSession: no valid sessions found in %s", sessionsDir)
		return "", fmt.Errorf("no valid sessions found")
	}

	prompts.VerboseLog("findLastSession: found last session: %s", lastSession)
	return lastSession, nil
}

// computeEnabledTools determines which tools are enabled based on CLI override and config
func computeEnabledTools(cliEnabledTools []string, cfg *config.AgentConfig) []string {
	// If CLI specifies tools, use those (unless it's "none" or "empty")
	if len(cliEnabledTools) > 0 {
		if len(cliEnabledTools) == 1 && (cliEnabledTools[0] == "none" || cliEnabledTools[0] == "empty") {
			return []string{}
		}
		return cliEnabledTools
	}

	// Otherwise, use config-based enabling
	var enabled []string
	if cfg.Tools.Bash.Enabled {
		enabled = append(enabled, "bash")
	}
	if cfg.Tools.Read.Enabled {
		enabled = append(enabled, "read")
	}
	if cfg.Tools.Write.Enabled {
		enabled = append(enabled, "write")
	}
	if cfg.Tools.Edit.Enabled {
		enabled = append(enabled, "edit")
	}
	if cfg.Tools.Glob.Enabled {
		enabled = append(enabled, "glob")
	}
	if cfg.Tools.Grep.Enabled {
		enabled = append(enabled, "grep")
	}
	if cfg.Tools.AskUser.Enabled {
		enabled = append(enabled, "ask_user_question")
	}
	if cfg.Tools.WebFetch.Enabled {
		enabled = append(enabled, "webfetch")
	}
	if cfg.Tools.WebSearch.Enabled {
		enabled = append(enabled, "websearch")
	}
	// task and skill are always enabled (they're core functionality)
	enabled = append(enabled, "task", "skill")

	return enabled
}

// registerTools registers all available tools with the registry
func (a *Agent) registerTools(registry *tools.ToolRegistry, cfg *config.AgentConfig, workingDir string, includeAskUser bool, enabledTools []string) {
	// If enabledTools is not empty, only tools in the list are enabled
	isToolEnabled := func(name string, configEnabled bool) bool {
		if len(enabledTools) > 0 {
			if len(enabledTools) == 1 && (enabledTools[0] == "none" || enabledTools[0] == "empty") {
				return false
			}
			for _, t := range enabledTools {
				if t == name {
					return true
				}
			}
			return false
		}
		return configEnabled
	}

	// Bash tool
	if isToolEnabled("bash", cfg.Tools.Bash.Enabled) {
		bashTool := tools.NewBashTool(
			workingDir,
			cfg.Tools.Bash.DefaultTimeout,
			cfg.Tools.Bash.AllowedCommands,
			cfg.Tools.Bash.BlockedCommands,
		)
		registry.Register(bashTool)
	}

	// Read tool
	if isToolEnabled("read", cfg.Tools.Read.Enabled) {
		readLimit := a.config.Tools.Read.ReadLimit
		if a.readLimit != 0 {
			readLimit = a.readLimit
		}
		readTool := tools.NewReadTool(readLimit)
		registry.Register(readTool)
	}

	// Write tool
	if isToolEnabled("write", cfg.Tools.Write.Enabled) {
		writeTool := tools.NewWriteTool()
		registry.Register(writeTool)
	}

	// Edit tool
	if isToolEnabled("edit", cfg.Tools.Edit.Enabled) {
		editTool := tools.NewEditTool()
		registry.Register(editTool)
	}

	// Glob tool
	if isToolEnabled("glob", cfg.Tools.Glob.Enabled) {
		globTool := tools.NewGlobTool(cfg.Tools.Glob.MaxResults)
		registry.Register(globTool)
	}

	// Grep tool
	if isToolEnabled("grep", cfg.Tools.Grep.Enabled) {
		grepTool := tools.NewGrepTool(cfg.Tools.Grep.MaxResults, cfg.Tools.Grep.MaxContextLines)
		registry.Register(grepTool)
	}

	// AskUser tool
	if includeAskUser && isToolEnabled("ask_user", cfg.Tools.AskUser.Enabled) {
		askTool := tools.NewAskUserTool()
		registry.Register(askTool)
	}

	// WebFetch tool
	if isToolEnabled("webfetch", cfg.Tools.WebFetch.Enabled) {
		webfetchTool := tools.NewWebFetchTool(cfg.Tools.WebFetch.TimeoutSecs)
		registry.Register(webfetchTool)
	}

	// WebSearch tool
	if isToolEnabled("websearch", cfg.Tools.WebSearch.Enabled) {
		websearchTool := tools.NewWebSearchTool(
			cfg.Tools.WebSearch.MaxResults,
			cfg.Tools.WebSearch.TimeoutSecs,
			"", // API endpoint would come from env or config
			"", // API key would come from env
		)
		registry.Register(websearchTool)
	}

	// Skill tool
	if isToolEnabled("skill", cfg.Tools.Skill.Enabled) {
		skillTool := tools.NewSkillTool(workingDir)
		registry.Register(skillTool)
	}

	// Task tool
	if isToolEnabled("task", cfg.Tools.Task.Enabled) {
		// Create callbacks for dynamic agent loading
		getAgentNames := func() []string {
			return a.promptBuilder.GetAvailableAgentNames(workingDir)
		}

		loadAgentContent := func(agentName string) (string, error) {
			return a.loadAgentContent(agentName, workingDir)
		}

		taskTool := tools.NewTaskTool(
			cfg.Tools.Task.DefaultModel,
			cfg.Tools.Task.AllowedAgents,
			cfg.Tools.Task.MaxConcurrent,
			a.sessionID,
			func(config tools.SubAgentConfig) (tools.SubAgent, error) {
				// We reuse the same LLM for subagents for now, or create a new one if model is specified
				subLLM := a.llm
				if config.Model != "" {
					// Need to find model config by name or shorthand
					mCfg, err := a.config.GetModel(config.Model)
					if err == nil {
						subLLM, _ = createLLM(mCfg)
					}
				}

				// Create subagent tools (disable AskUser for subagents)
				subRegistry := tools.NewToolRegistry()
				a.registerTools(subRegistry, a.config, workingDir, false, enabledTools)

			subOpts := subagent.SubAgentOptions{
				ID:                  config.ID,
				ParentID:            config.ParentID,
				AgentType:           config.SubagentType,
				LLM:                 subLLM,
				Tools:               subRegistry.GetAll(),
				MaxTurns:            50,
				JSONOutput:          a.jsonOutput,
				SoftTools:           a.modelConfig.SoftTools,
				AgentContent:        config.AgentContent,
				InitialConversation: config.InitialConversation,
				ForkedPrompt:        config.ForkedPrompt,
				Fork:                config.Fork,
				Resume:              config.Resume,
			}
				return subagent.NewSubAgent(subOpts), nil
			},
			workingDir,
			getAgentNames,
			loadAgentContent,
			func() []llms.MessageContent {
				return a.conversation
			},
		)
		registry.Register(taskTool)
	}
}

// loadAgentContent loads agent content from .claude/agents/ directories
func (a *Agent) loadAgentContent(agentName string, workingDir string) (string, error) {
	// Check for MYAGENT_CONFIG_DIR - if set, use only that directory
	if customDir := os.Getenv("MYAGENT_CONFIG_DIR"); customDir != "" {
		agentPath := filepath.Join(customDir, "agents", agentName+".md")
		data, err := os.ReadFile(agentPath)
		if err == nil {
			return string(data), nil
		}
		return "", fmt.Errorf("agent '%s' not found in %s", agentName, agentPath)
	}

	// Try project agents first
	if workingDir != "" {
		agentPath := filepath.Join(workingDir, ".claude", "agents", agentName+".md")
		data, err := os.ReadFile(agentPath)
		if err == nil {
			return string(data), nil
		}
	}

	// Try global agents
	homeDir, _ := os.UserHomeDir()
	agentPath := filepath.Join(homeDir, ".claude", "agents", agentName+".md")
	data, err := os.ReadFile(agentPath)
	if err == nil {
		return string(data), nil
	}

	return "", fmt.Errorf("agent '%s' not found in any agents directory", agentName)
}

// createLLM creates a langchaingo LLM from configuration
func createLLM(modelConfig config.ModelConfig) (llms.Model, error) {
	switch modelConfig.Provider {
	case "openai":
		opts := []openai.Option{
			openai.WithModel(modelConfig.ModelID),
			openai.WithToken(modelConfig.APIKey),
		}
		if modelConfig.BaseURL != "" {
			opts = append(opts, openai.WithBaseURL(modelConfig.BaseURL))
		}
		// Set HTTP timeout (default 180s)
		timeoutSecs := modelConfig.TimeoutSecs
		if timeoutSecs == 0 {
			timeoutSecs = 180
		}
		httpClient := &http.Client{
			Timeout: time.Duration(timeoutSecs) * time.Second,
		}
		opts = append(opts, openai.WithHTTPClient(httpClient))
		return openai.New(opts...)
	case "anthropic":
		opts := []anthropic.Option{
			anthropic.WithModel(modelConfig.ModelID),
			anthropic.WithToken(modelConfig.APIKey),
		}
		if modelConfig.BaseURL != "" {
			opts = append(opts, anthropic.WithBaseURL(modelConfig.BaseURL))
		}
		return anthropic.New(opts...)
	case "bedrock":
		opts := []bedrock.Option{
			bedrock.WithModel(modelConfig.ModelID),
		}
		// If model ID is an ARN, explicitly set provider to anthropic
		if strings.HasPrefix(modelConfig.ModelID, "arn:aws:bedrock") {
			opts = append(opts, bedrock.WithModelProvider("anthropic"))
		}
		// Enable cache points if configured
		if modelConfig.CachePoints {
			opts = append(opts, bedrock.WithCachePoints(true))
		}
		ctx := context.Background()
		awsOpts := []func(*awsconfig.LoadOptions) error{}
		if modelConfig.Region != "" {
			awsOpts = append(awsOpts, awsconfig.WithRegion(modelConfig.Region))
		}
		if modelConfig.AWSProfile != "" {
			awsOpts = append(awsOpts, awsconfig.WithSharedConfigProfile(modelConfig.AWSProfile))
		}
		if modelConfig.AccessKey != "" {
			awsOpts = append(awsOpts, awsconfig.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(modelConfig.AccessKey, modelConfig.SecretKey, modelConfig.SessionToken),
			))
		}
		cfg, err := awsconfig.LoadDefaultConfig(ctx, awsOpts...)
		if err != nil {
			return nil, fmt.Errorf("failed to load AWS config: %w", err)
		}
		client := bedrockruntime.NewFromConfig(cfg)
		opts = append(opts, bedrock.WithClient(client))
		return bedrock.New(opts...)
	case "bedrock-invoke":
		return bedrock_invoke.New(modelConfig)
	case "ollama":
		opts := []ollama.Option{
			ollama.WithModel(modelConfig.ModelID),
		}
		if modelConfig.BaseURL != "" {
			opts = append(opts, ollama.WithServerURL(modelConfig.BaseURL))
		}
		return ollama.New(opts...)
	default:
		return nil, fmt.Errorf("unknown provider: %s", modelConfig.Provider)
	}
}

func (a *Agent) addMessage(msg llms.MessageContent) {
	a.conversation = append(a.conversation, msg)
	if prompts.IsVerbose() && len(msg.Parts) > 0 {
		if textPart, ok := msg.Parts[0].(llms.TextContent); ok {
			prompts.VerboseLog("Added %s message to conversation (%d chars)", msg.Role, len(textPart.Text))
		}
	}
	if err := a.saveSession(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Failed to save session: %v\n", err)
		os.Exit(1)
	}
}

// Run starts the agentic loop
func (a *Agent) Run(ctx context.Context) error {
	// Print welcome message
	fmt.Println("╔════════════════════════════════════════════════════════╗")
	fmt.Println("║              Agent System v1.0                         ║")
	fmt.Println("║  Type 'exit' or 'quit' to end the session              ║")
	fmt.Println("╚════════════════════════════════════════════════════════╝")
	fmt.Println()

	// Build system prompt if conversation is empty
	if len(a.conversation) == 0 {
		systemPrompt := a.promptBuilder.BuildSystemPromptWithContext(a.promptBuilder.GetWorkingDir())
		a.addMessage(llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt))
	}

	// Debug mode: dump system prompt and wait for user input to show user prompt
	if a.debugPrompt {
		fmt.Println("\n═══════════════════════════════════════════════════════════════════")
		fmt.Println("SYSTEM PROMPT DEBUG DUMP")
		fmt.Println("═══════════════════════════════════════════════════════════════════")
		for _, msg := range a.conversation {
			if msg.Role == llms.ChatMessageTypeSystem {
				fmt.Println(msg.Parts[0])
			}
		}
		fmt.Println("═══════════════════════════════════════════════════════════════════")
		fmt.Println("Enter your prompt (then debug mode will exit without calling LLM):")
	}

	// Create reader for user input
	reader := bufio.NewReader(os.Stdin)

	for a.currentTurn < a.maxTurns {
		fmt.Print("\n> ")

		// Read user input
		input, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("failed to read input: %w", err)
		}

		input = strings.TrimSpace(input)

		// Debug mode: dump user prompt and exit
		if a.debugPrompt {
			fmt.Println("\n═══════════════════════════════════════════════════════════════════")
			fmt.Println("USER PROMPT:")
			fmt.Println("═══════════════════════════════════════════════════════════════════")
			fmt.Println(input)
			fmt.Println("═══════════════════════════════════════════════════════════════════")
			fmt.Println("[DEBUG MODE: Exiting without calling LLM]")
			return nil
		}

		// Check for exit commands
		if input == "exit" || input == "quit" {
			fmt.Println("Goodbye!")
			return nil
		}

		// Check for help command
		if input == "/help" {
			a.printHelp()
			continue
		}

		// Skip empty input
		if input == "" {
			continue
		}

		a.logf("USER", "%s\n", input)

		// Process user message
		a.addMessage(llms.TextParts(llms.ChatMessageTypeHuman, input))

		// Run agentic loop
		err = a.runAgenticLoop(ctx)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		a.currentTurn++
		if a.jsonOutput {
			a.logEndTurn()
		} else {
			fmt.Printf("\nTo resume this conversation: ./main -m %s -r %s\n", a.modelKey, a.sessionID)
		}
	}

	fmt.Println("\nMaximum turns reached. Session ended.")
	return nil
}

// runAgenticLoop runs the main agent loop
func (a *Agent) runAgenticLoop(ctx context.Context) error {
	for {
		// Get available tools for the LLM
		toolDefinitions := a.getToolDefinitions()

		// Call LLM
		callOpts := []llms.CallOption{
			llms.WithTools(toolDefinitions),
			llms.WithTemperature(float64(a.modelConfig.Temperature)),
			llms.WithMaxTokens(a.modelConfig.MaxTokens),
		}

		// Enable soft tools if configured
		if a.modelConfig.SoftTools {
			callOpts = append(callOpts, llms.WithSoftTools(true))
		}

		resp, err := a.llm.GenerateContent(ctx, a.conversation, callOpts...)
		if err != nil {
			return fmt.Errorf("LLM generation failed: %w", err)
		}

		// Process response
		if len(resp.Choices) == 0 {
			return fmt.Errorf("no response from LLM")
		}

		choice := resp.Choices[0]

		// Extract usage
		currentUsage := usage.ExtractUsage(choice.GenerationInfo)

		// Add assistant message to conversation
		// Include reasoning_content if present (required for reasoning models like DeepSeek)
		contentParts := make([]llms.ContentPart, 0)
		if choice.Content != "" || choice.ReasoningContent != "" {
			// For reasoning models, we must pass back reasoning_content even if empty
			// Use "hmmm..." as fallback if ForceReasoningContent is enabled and content is empty
			reasoningContent := choice.ReasoningContent
			if a.modelConfig.ForceReasoningContent && reasoningContent == "" {
				reasoningContent = "hmmm..."
			}
			contentParts = append(contentParts, llms.TextContent{
				Text:             choice.Content,
				ReasoningContent: reasoningContent,
			})
		}
		// For soft tools, embed tool calls in content as text instead of separate parts
		if a.modelConfig.SoftTools && len(choice.ToolCalls) > 0 {
			var sb strings.Builder
			sb.WriteString(choice.Content)
			for _, tc := range choice.ToolCalls {
				if tc.FunctionCall != nil {
					// Append tool_call_id to the existing content (which already has the JSON)
					sb.WriteString(" as tool_call_id=")
					sb.WriteString(tc.ID)
				}
			}
			// Include reasoning content for soft tools mode as well
			reasoningContent := choice.ReasoningContent
			if a.modelConfig.ForceReasoningContent && reasoningContent == "" {
				reasoningContent = "hmmm..."
			}
			contentParts = []llms.ContentPart{llms.TextContent{
				Text:             sb.String(),
				ReasoningContent: reasoningContent,
			}}
		} else {
			// Always add ToolCall parts so we have the IDs for serialization
			for _, tc := range choice.ToolCalls {
				contentParts = append(contentParts, tc)
			}
		}
		a.addMessage(llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: contentParts,
		})

		// Print assistant response (always show usage)
		if choice.Content != "" {
			a.logfWithUsage("ASSISTANT", &currentUsage, "%s\n", choice.Content)
		} else {
			a.logfWithUsage("ASSISTANT", &currentUsage, "\n")
		}

		// Check for tool calls
		if len(choice.ToolCalls) == 0 {
			// No tool calls, we're done - capture final response
			a.finalResponse = stripReasoningTags(choice.Content)
			break
		}

		for _, tc := range choice.ToolCalls {
			if a.rawOutput {
				// skip
			} else if a.jsonOutput {
				a.logJSONProgressWithMeta("TOOL_CALL", tc.FunctionCall.Arguments, map[string]interface{}{
					"tool_name":    tc.FunctionCall.Name,
					"tool_call_id": tc.ID,
				})
			} else {
				a.logf("TOOL_CALL", "%s(%s)\n", tc.FunctionCall.Name, tc.FunctionCall.Arguments)
			}
		}

		// Execute tool calls in parallel and wait for ALL to complete
		toolResults := a.executeToolCalls(ctx, choice.ToolCalls)

		// Check for malformed tool calls and provide correction feedback
		hasMalformedCalls := false
		correctionMessages := []string{}

		for i, result := range toolResults {
			toolCall := choice.ToolCalls[i]

			// Check for malformed tool call errors
			if !result.Result.Success && a.shouldProvideCorrection(result.Result.Error) {
				hasMalformedCalls = true
				correction := a.generateToolCallCorrection(toolCall, result.Result.Error)
				correctionMessages = append(correctionMessages, correction)
			}
		}

		// If we have malformed calls, add correction message to conversation and continue loop
		if hasMalformedCalls {
			correctionText := strings.Join(correctionMessages, "\n\n")
			a.logf("CORRECTION", "Providing tool call format correction to model\n")

			a.addMessage(llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.TextContent{Text: correctionText},
				},
			})
			continue // Continue the loop to give the model another chance
		}

		// Add ALL tool results to conversation as a batch
		// This ensures the LLM waits for all parallel tool calls to complete
		// before making its next decision
		for i, result := range toolResults {
			resultJSON, _ := json.Marshal(result.Result)
			// For task tool, if it was a success, try to extract the subagent ID for JSON logging
			if choice.ToolCalls[i].FunctionCall.Name == "task" && a.jsonOutput {
				var data map[string]interface{}
				if err := json.Unmarshal(resultJSON, &data); err == nil {
					if agentID, ok := data["resume_instance_id"].(string); ok {
						// Log a spawn event explicitly linked before the result
						timestamp := time.Now().Format("2006-01-02 15:04:05.000")
						spawnOut := map[string]interface{}{
							"timestamp": timestamp,
							"type":      "progress",
							"data": map[string]interface{}{
								"role":         "SUBAGENT_SPAWNED",
								"content":      agentID,
								"parent_id":    a.sessionID,
								"subagent":     agentID,
								"tool_call_id": choice.ToolCalls[i].ID,
							},
						}
						spawnBytes, _ := json.Marshal(spawnOut)
						fmt.Println(string(spawnBytes))

						a.logJSONProgressWithMeta("TOOL_RESULT", string(resultJSON), map[string]interface{}{
							"subagent":     agentID,
							"tool_name":    "task",
							"tool_call_id": choice.ToolCalls[i].ID,
						})
					} else {
						a.logJSONProgressWithMeta("TOOL_RESULT", string(resultJSON), map[string]interface{}{
							"tool_name":    choice.ToolCalls[i].FunctionCall.Name,
							"tool_call_id": choice.ToolCalls[i].ID,
						})
					}
				} else {
					a.logJSONProgressWithMeta("TOOL_RESULT", string(resultJSON), map[string]interface{}{
						"tool_name":    choice.ToolCalls[i].FunctionCall.Name,
						"tool_call_id": choice.ToolCalls[i].ID,
					})
				}
			} else if a.jsonOutput {
				a.logJSONProgressWithMeta("TOOL_RESULT", string(resultJSON), map[string]interface{}{
					"tool_name":    choice.ToolCalls[i].FunctionCall.Name,
					"tool_call_id": choice.ToolCalls[i].ID,
				})
			} else {
				a.logToolResult(choice.ToolCalls[i].FunctionCall.Name, resultJSON)
			}
			a.addMessage(llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: choice.ToolCalls[i].ID,
						Name:       choice.ToolCalls[i].FunctionCall.Name,
						Content:    string(resultJSON),
					},
				},
			})
		}
	}

	return nil
}

// ToolResultWithName represents a tool result with the tool name
type ToolResultWithName struct {
	Name   string
	Result tools.ToolResult
}

// executeToolCalls executes tool calls and returns results
func (a *Agent) executeToolCalls(ctx context.Context, toolCalls []llms.ToolCall) []ToolResultWithName {
	// Convert llms.ToolCall to tools.ToolCall
	calls := make([]tools.ToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		calls[i] = tools.ToolCall{
			ID:     tc.ID,
			Name:   tc.FunctionCall.Name,
			Params: []byte(tc.FunctionCall.Arguments),
		}
	}

	// Execute tools in parallel
	results := a.toolRegistry.ExecuteToolsParallel(ctx, calls)

	// Convert results
	toolResults := make([]ToolResultWithName, len(results))
	for i, result := range results {
		toolResults[i] = ToolResultWithName{
			Name:   calls[i].Name,
			Result: result.Result,
		}
	}

	return toolResults
}

// shouldProvideCorrection determines if an error warrants providing correction feedback to the model
func (a *Agent) shouldProvideCorrection(errorMsg string) bool {
	if errorMsg == "" {
		return false
	}

	errorMsg = strings.ToLower(errorMsg)

	// Check for common malformed tool call errors
	malformedIndicators := []string{
		"malformed json",
		"invalid json",
		"failed to parse",
		"missing parameter",
		"required parameter",
		"unexpected end of json",
		"invalid character",
		"no such tool",
	}

	for _, indicator := range malformedIndicators {
		if strings.Contains(errorMsg, indicator) {
			return true
		}
	}

	return false
}

// generateToolCallCorrection generates a correction message for malformed tool calls
func (a *Agent) generateToolCallCorrection(toolCall llms.ToolCall, errorMsg string) string {
	tool, err := a.toolRegistry.Get(toolCall.FunctionCall.Name)
	if err != nil {
		return fmt.Sprintf("Unknown tool '%s'. Please check available tools.", toolCall.FunctionCall.Name)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Your tool call to '%s' was malformed: %s\n\n", toolCall.FunctionCall.Name, errorMsg))
	sb.WriteString("Please correct your tool call format. The correct format is:\n\n")

	if a.modelConfig.SoftTools {
		// Soft tools format
		sb.WriteString("For soft tools, use JSON format:\n")
		sb.WriteString(fmt.Sprintf(`{"$tool_call":"%s","$params":{...}}`, toolCall.FunctionCall.Name))
		sb.WriteString("\n\nOr XML format:\n")
		sb.WriteString(fmt.Sprintf(`<tool_call>
<function=%s>
<parameter=param_name>value</parameter=param_name>
</function=%s>
</tool_call>`, toolCall.FunctionCall.Name, toolCall.FunctionCall.Name))
	} else {
		// Native tools format
		sb.WriteString("For native tools, ensure your parameters are valid JSON matching the tool schema.")
	}

	sb.WriteString("\n\nTool Schema:\n")
	schema := tool.Schema()
	schemaJSON, _ := json.MarshalIndent(schema, "", "  ")
	sb.WriteString(string(schemaJSON))

	sb.WriteString(fmt.Sprintf("\n\nExample for '%s':\n", toolCall.FunctionCall.Name))

	// Try to provide a simple example based on schema
	if schema != nil && schema.Properties != nil {
		example := make(map[string]interface{})
		for paramName, paramSchema := range schema.Properties {
			if paramSchema.Required != nil && len(paramSchema.Required) > 0 {
				// For required parameters, provide example values
				switch paramName {
				case "filePath":
					example[paramName] = "/path/to/file.txt"
				case "content":
					example[paramName] = "example content"
				case "pattern":
					example[paramName] = "*.go"
				case "command":
					example[paramName] = "ls -la"
				default:
					example[paramName] = "example_value"
				}
			}
		}

		if len(example) > 0 {
			if a.modelConfig.SoftTools {
				exampleJSON, _ := json.Marshal(map[string]interface{}{
					"$tool_call": toolCall.FunctionCall.Name,
					"$params":    example,
				})
				sb.WriteString("```json\n")
				sb.WriteString(string(exampleJSON))
				sb.WriteString("\n```")
			} else {
				exampleJSON, _ := json.Marshal(example)
				sb.WriteString("Parameters: ")
				sb.WriteString(string(exampleJSON))
			}
		}
	}

	return sb.String()
}

// getToolDefinitions converts registered tools to langchaingo tool definitions
func (a *Agent) getToolDefinitions() []llms.Tool {
	tools := a.toolRegistry.GetAll()
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

// RunSingle runs a single prompt and returns
func (a *Agent) RunSingle(ctx context.Context, prompt string) error {
	// Build system prompt if conversation is empty
	if len(a.conversation) == 0 {
		systemPrompt := a.promptBuilder.BuildSystemPromptWithContext(a.promptBuilder.GetWorkingDir())
		a.addMessage(llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt))
	}

	// Debug mode: dump system prompt and user prompt, then exit
	if a.debugPrompt {
		return a.dumpPromptsAndExit(prompt)
	}

	a.logf("USER", "%s\n", prompt)

	// Add user message
	a.addMessage(llms.TextParts(llms.ChatMessageTypeHuman, prompt))

	// Run agentic loop
	err := a.runAgenticLoop(ctx)
	if err != nil {
		return err
	}

	// Write final response to file if outputFile is specified
	if a.outputFile != "" && a.finalResponse != "" {
		if err := os.WriteFile(a.outputFile, []byte(a.finalResponse), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing output file: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "\nOutput written to: %s\n", a.outputFile)
		}
	}

	if a.jsonOutput {
		a.logEndTurn()
	} else if !a.rawOutput {
		fmt.Printf("\nTo resume this conversation: ./main -m %s -r %s\n", a.modelKey, a.sessionID)
	}
	return nil
}

// dumpPromptsAndExit dumps the system prompt and user prompt, then exits without calling LLM
func (a *Agent) dumpPromptsAndExit(userPrompt string) error {
	fmt.Println("\n═══════════════════════════════════════════════════════════════════")
	fmt.Println("SYSTEM PROMPT DEBUG DUMP")
	fmt.Println("═══════════════════════════════════════════════════════════════════")
	for _, msg := range a.conversation {
		if msg.Role == llms.ChatMessageTypeSystem {
			fmt.Println(msg.Parts[0])
		}
	}
	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Println("USER PROMPT:")
	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Println(userPrompt)
	fmt.Println("═══════════════════════════════════════════════════════════════════")
	fmt.Println("[DEBUG MODE: Exiting without calling LLM]")
	return nil
}

// printHelp prints the help message
// stripReasoningTags removes <reasoning>...</reasoning> blocks from model output.
func stripReasoningTags(s string) string {
	for {
		start := strings.Index(s, "<reasoning>")
		if start == -1 {
			break
		}
		end := strings.Index(s[start:], "</reasoning>")
		if end == -1 {
			s = s[:start]
			break
		}
		s = s[:start] + s[start+end+len("</reasoning>"):]
	}
	return strings.TrimSpace(s) + "\n"
}

func (a *Agent) printHelp() {
	fmt.Println("\nAvailable commands:")
	fmt.Println("  /help        - Show this help message")
	fmt.Println("  exit / quit  - End the session")
	fmt.Println()
	fmt.Println("Available tools:")
	for _, tool := range a.toolRegistry.GetAll() {
		fmt.Printf("  - %s: %s\n", tool.Name(), tool.Description())
	}
}
