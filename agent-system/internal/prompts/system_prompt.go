package prompts

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SystemPromptBuilder builds system prompts for the agent
type SystemPromptBuilder struct {
	workingDir   string
	isGitRepo    bool
	enabledTools []string
	agentContent string
}

// NewSystemPromptBuilder creates a new system prompt builder
func NewSystemPromptBuilder(workingDir string, isGitRepo bool, enabledTools []string) *SystemPromptBuilder {
	if workingDir == "" {
		workingDir, _ = os.Getwd()
	}
	return &SystemPromptBuilder{
		workingDir:   workingDir,
		isGitRepo:    isGitRepo,
		enabledTools: enabledTools,
	}
}

// SetAgentContent sets the agent content to be injected into the system prompt
func (b *SystemPromptBuilder) SetAgentContent(content string) {
	b.agentContent = content
}

// GetWorkingDir returns the working directory
func (b *SystemPromptBuilder) GetWorkingDir() string {
	return b.workingDir
}

// BuildSystemPrompt builds the complete system prompt
func (b *SystemPromptBuilder) BuildSystemPrompt() string {
	return b.BuildSystemPromptWithContext("")
}

// BuildSystemPromptWithContext builds the complete system prompt with additional context files
func (b *SystemPromptBuilder) BuildSystemPromptWithContext(projectRoot string) string {
	// Build skills list for system-reminder (name + description only)
	skillsList := b.GetSkillsList(projectRoot)

	prompt := ""
	// Add skills in system-reminder format at the beginning
	if skillsList != "" {
		prompt += "<system-reminder>\n"
		prompt += "The following skills are available for use with the Skill tool:\n\n"
		prompt += skillsList
		prompt += "</system-reminder>\n\n"
		VerboseLog("Added skills list to system prompt")
	}

	// Build agents list for system-reminder (name + description only)
	agentsList := b.GetAgentsList(projectRoot)
	if agentsList != "" {
		prompt += "<system-reminder>\n"
		prompt += "The following agents are available for use with the Task tool:\n\n"
		prompt += agentsList
		prompt += "</system-reminder>\n\n"
		VerboseLog("Added agents list to system prompt")
	}

	// Add agent content if provided (from -a flag)
	if b.agentContent != "" {
		prompt += "<agent-persona>\n"
		prompt += b.agentContent
		prompt += "\n</agent-persona>\n\n"
		VerboseLog("Added agent content to system prompt (%d chars)", len(b.agentContent))
	}

	prompt += b.getSystemRole()
	prompt += b.getToneAndStyle()
	prompt += b.getProfessionalGuidelines()
	prompt += b.getAskingQuestionsPolicy()
	prompt += b.getDoingTasksGuidelines()
	prompt += b.getToolUsagePolicy()
	prompt += b.getCodeReferences()
	prompt += b.getEnvironmentSection()
	prompt += b.getBuiltInTools()

	if projectRoot != "" {
		VerboseLog("Building system prompt with context from: %s", projectRoot)

		// Load global CLAUDE.md
		prompt += b.LoadGlobalClaudeMD()

		// Load AGENTS.md (project/global). If present, skip project CLAUDE.md files.
		agentsContent := b.LoadAgentsMD(projectRoot)
		prompt += agentsContent
		if agentsContent == "" {
			// Load project CLAUDE.md files only when AGENTS.md missing
			prompt += b.LoadProjectClaudeMD(projectRoot)
		}
	}

	VerboseLog("Built system prompt (%d chars)", len(prompt))

	return prompt
}

// SkillInfo represents skill metadata
type SkillInfo struct {
	Name        string
	Description string
}

// GetSkillsList returns a list of available skills with names and descriptions
func (b *SystemPromptBuilder) GetSkillsList(projectRoot string) string {
	seenSkills := make(map[string]bool)
	var skills []SkillInfo

	// Check for MYAGENT_CONFIG_DIR - if set, use only that directory and disable all defaults
	if customDir := os.Getenv("MYAGENT_CONFIG_DIR"); customDir != "" {
		customSkills := b.loadSkillsInfoFromDirWithDedup(filepath.Join(customDir, "skills"), "custom", seenSkills)
		skills = append(skills, customSkills...)
	} else {
		// Default priority order (highest to lowest):
		// 1. .claude/skills (project)
		// 2. ~/.claude/skills (global)

		// 1. Load .claude/skills (project)
		if projectRoot != "" {
			projectSkills := b.loadSkillsInfoFromDirWithDedup(b.getProjectSkillsDir(projectRoot), "project", seenSkills)
			skills = append(skills, projectSkills...)
		}

		// 2. Load global skills ~/.claude/skills
		globalSkills := b.loadSkillsInfoFromDirWithDedup(b.getGlobalSkillsDir(), "global", seenSkills)
		skills = append(skills, globalSkills...)
	}

	if len(skills) == 0 {
		return ""
	}

	var lines []string
	for _, skill := range skills {
		lines = append(lines, fmt.Sprintf("- %s: %s", skill.Name, skill.Description))
	}

	return strings.Join(lines, "\n")
}

func (b *SystemPromptBuilder) getGlobalSkillsDir() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".claude", "skills")
}

func (b *SystemPromptBuilder) getProjectSkillsDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".claude", "skills")
}

func (b *SystemPromptBuilder) loadSkillsInfoFromDirWithDedup(skillsDir, skillType string, seen map[string]bool) []SkillInfo {
	var skills []SkillInfo

	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return skills
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// Skip if already loaded from higher priority source
		if seen[entry.Name()] {
			continue
		}
		seen[entry.Name()] = true

		// Look for SKILL.md in each skill directory
		skillPath := filepath.Join(skillsDir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillPath)
		if err != nil {
			continue
		}

		// Parse YAML frontmatter for name and description
		info := parseSkillMetadata(string(data), entry.Name())
		skills = append(skills, info)
		VerboseLog("Loaded %s skill info: %s - %s", skillType, info.Name, info.Description)
	}

	return skills
}

// SkillFrontmatter represents the YAML frontmatter structure for skills
type SkillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// parseSkillMetadata extracts name and description from YAML frontmatter
func parseSkillMetadata(content, defaultName string) SkillInfo {
	info := SkillInfo{Name: defaultName, Description: ""}

	// Check for YAML frontmatter
	if !strings.HasPrefix(content, "---") {
		return info
	}

	// Find the closing ---
	lines := strings.Split(content, "\n")
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			endIdx = i
			break
		}
	}

	if endIdx < 0 {
		return info
	}

	// Parse frontmatter using YAML parser
	frontmatter := strings.Join(lines[1:endIdx], "\n")

	var metadata SkillFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), &metadata); err != nil {
		// Fallback: try simple line-by-line parsing
		for _, line := range strings.Split(frontmatter, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "name:") {
				info.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			}
			if strings.HasPrefix(line, "description:") {
				info.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			}
		}
		return info
	}

	if metadata.Name != "" {
		info.Name = metadata.Name
	}
	info.Description = metadata.Description

	return info
}

// AgentInfo represents agent metadata
type AgentInfo struct {
	Name        string
	Description string
}

// GetAgentsList returns a list of available agents with names and descriptions
func (b *SystemPromptBuilder) GetAgentsList(projectRoot string) string {
	seenAgents := make(map[string]bool)
	var agents []AgentInfo

	// Check for MYAGENT_CONFIG_DIR - if set, use only that directory and disable all defaults
	if customDir := os.Getenv("MYAGENT_CONFIG_DIR"); customDir != "" {
		customAgents := b.loadAgentsInfoFromDirWithDedup(filepath.Join(customDir, "agents"), "custom", seenAgents)
		agents = append(agents, customAgents...)
	} else {
		// Default priority order (highest to lowest):
		// 1. .claude/agents (project)
		// 2. ~/.claude/agents (global)

		// 1. Load .claude/agents (project)
		if projectRoot != "" {
			projectAgents := b.loadAgentsInfoFromDirWithDedup(b.getProjectAgentsDir(projectRoot), "project", seenAgents)
			agents = append(agents, projectAgents...)
		}

		// 2. Load global agents ~/.claude/agents
		globalAgents := b.loadAgentsInfoFromDirWithDedup(b.getGlobalAgentsDir(), "global", seenAgents)
		agents = append(agents, globalAgents...)
	}

	if len(agents) == 0 {
		return ""
	}

	var lines []string
	for _, agent := range agents {
		lines = append(lines, fmt.Sprintf("- %s: %s", agent.Name, agent.Description))
	}

	return strings.Join(lines, "\n")
}

// GetAvailableAgentNames returns a list of available agent names for task tool enum
func (b *SystemPromptBuilder) GetAvailableAgentNames(projectRoot string) []string {
	seenAgents := make(map[string]bool)
	var names []string

	// Check for MYAGENT_CONFIG_DIR
	if customDir := os.Getenv("MYAGENT_CONFIG_DIR"); customDir != "" {
		dir := filepath.Join(customDir, "agents")
		names = append(names, b.loadAgentNamesFromDir(dir, seenAgents)...)
	} else {
		// Project agents
		if projectRoot != "" {
			dir := b.getProjectAgentsDir(projectRoot)
			names = append(names, b.loadAgentNamesFromDir(dir, seenAgents)...)
		}

		// Global agents
		dir := b.getGlobalAgentsDir()
		names = append(names, b.loadAgentNamesFromDir(dir, seenAgents)...)
	}

	return names
}

func (b *SystemPromptBuilder) getGlobalAgentsDir() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".claude", "agents")
}

func (b *SystemPromptBuilder) getProjectAgentsDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".claude", "agents")
}

func (b *SystemPromptBuilder) loadAgentsInfoFromDirWithDedup(agentsDir, agentType string, seen map[string]bool) []AgentInfo {
	var agents []AgentInfo

	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return agents
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

		// Skip if already loaded from higher priority source
		if seen[agentName] {
			continue
		}
		seen[agentName] = true

		// Look for agent file
		agentPath := filepath.Join(agentsDir, name)
		data, err := os.ReadFile(agentPath)
		if err != nil {
			continue
		}

		// Parse YAML frontmatter for name and description
		info := b.parseAgentMetadata(string(data), agentName)
		agents = append(agents, info)
		VerboseLog("Loaded %s agent info: %s - %s", agentType, info.Name, info.Description)
	}

	return agents
}

func (b *SystemPromptBuilder) loadAgentNamesFromDir(agentsDir string, seen map[string]bool) []string {
	var names []string

	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return names
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

		// Skip if already seen
		if seen[agentName] {
			continue
		}
		seen[agentName] = true
		names = append(names, agentName)
	}

	return names
}

// parseAgentMetadata extracts name and description from YAML frontmatter
func (b *SystemPromptBuilder) parseAgentMetadata(content, defaultName string) AgentInfo {
	info := AgentInfo{Name: defaultName, Description: ""}

	// Check for YAML frontmatter
	if !strings.HasPrefix(content, "---") {
		return info
	}

	// Find the closing ---
	lines := strings.Split(content, "\n")
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			endIdx = i
			break
		}
	}

	if endIdx < 0 {
		return info
	}

	// Parse frontmatter using YAML parser
	frontmatter := strings.Join(lines[1:endIdx], "\n")

	type AgentFrontmatter struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}

	var metadata AgentFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), &metadata); err != nil {
		// Fallback: try simple line-by-line parsing
		for _, line := range strings.Split(frontmatter, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "name:") {
				info.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			}
			if strings.HasPrefix(line, "description:") {
				info.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			}
		}
		return info
	}

	if metadata.Name != "" {
		info.Name = metadata.Name
	}
	info.Description = metadata.Description

	return info
}

func (b *SystemPromptBuilder) getSystemRole() string {
	return `You are Agent-System, an interactive CLI agent that helps users with software engineering tasks.
Use the instructions below and the tools available to you to assist the user.

`
}

func (b *SystemPromptBuilder) getToneAndStyle() string {
	return `# Tone and style
- Only use emojis if the user explicitly requests it. Avoid using emojis in all
  communication unless asked.
- Your responses should be short and concise. You can use Github-flavored markdown for
  formatting, and will be rendered in a monospace font using the CommonMark specification.
- Output text to communicate with the user; all text you output outside of tool
  use is displayed to the user. Only use tools to complete tasks. Never use tools
  like Bash or code comments as a means to communicate with the user during the session.
- NEVER create files unless they're absolutely necessary for achieving your goal. ALWAYS prefer
  editing an existing file to creating a new one. This includes markdown files.

`
}

func (b *SystemPromptBuilder) getProfessionalGuidelines() string {
	return `# Professional productivity
Prioritize technical accuracy and truthfulness over validating the user's beliefs.
Focus on facts and problem-solving, providing direct, objective technical info
without any unnecessary superlatives, praise, or emotional validation. It is best for
the user if the agent honestly applies the same rigorous standards to all ideas and disagrees
when necessary, even if it may not be what the user wants to hear. Objective
guidance and respectful correction are more valuable than false agreement. Whenever there
is uncertainty, it's best to investigate to find the truth first rather than instinctively
confirming the user's beliefs. Avoid using over-the-top validation or excessive
praise when responding to users such as "You're absolutely right" or similar phrases.

`
}

func (b *SystemPromptBuilder) getAskingQuestionsPolicy() string {
	return `# Asking questions as you work
Use the method of asking user when you need to ask the user questions for
clarification, want to validate assumptions, or need to make a decision you're
unsure about.

`
}

func (b *SystemPromptBuilder) getDoingTasksGuidelines() string {
	return `# Doing tasks
The user will primarily request you perform software engineering tasks. This includes
solving bugs, adding new functionality, refactoring code, explaining code, and more.
For these tasks the following steps are recommended:

- NEVER propose changes to code you haven't read. If a user asks about or wants
  you to modify a file, read it first. Understand existing code before suggesting
  modifications.
- Be careful not to introduce security vulnerabilities such as command injection, XSS, SQL
  injection, and other OWASP top 10 vulnerabilities. If you notice that you wrote
  insecure code, immediately fix it.
- Avoid over-engineering. Only make changes that are directly requested or clearly necessary.
  Keep solutions simple and focused.
  - Don't add features, refactor code, or make "improvements" beyond what was asked.
    A bug fix doesn't need surrounding code cleaned up. A simple feature doesn't
    need extra configurability. Don't add docstrings, comments, or type annotations
    to code you didn't change. Only add comments where the logic isn't self-evident.
  - Don't add error handling, fallbacks, or validation for scenarios that can't happen.
    Trust internal code and framework guarantees. Only validate at system boundaries
    (user input, external APIs). Don't use feature flags or backwards-compatibility
    shims when you can just change the code.
  - Don't create helpers, utilities, or abstractions for one-time operations. Don't
    design for hypothetical future requirements. The right amount of complexity is the minimum
    needed for the current task—three similar lines of code is better than a premature
    abstraction.
- Avoid backwards-compatibility hacks like renaming unused _vars, re-exporting types,
  adding // removed comments for removed code, etc. If something is unused, delete
  it completely.
- Tool results and user messages may include <system-reminder> tags.
  <system-reminder> tags contain useful information and reminders. They are
  automatically added by the system, and bear no direct relation to the specific tool
  results or user messages in which they appear.
- The conversation has unlimited context through automatic summarization.

`
}

func (b *SystemPromptBuilder) getToolUsagePolicy() string {
	return `# Tool usage policy
- When doing file search, prefer to use Task tool in order to reduce context usage.
- You should proactively use Task tool with specialized agents when the task at hand
  matches the agent's description.
- You can call multiple tools in a single response. If you intend to call multiple
  tools and there are no dependencies between them, make all independent tool calls in
  parallel. Maximize use of parallel tool calls where possible to increase efficiency.
  However, if some tool calls depend on previous calls to inform dependent values, do
  NOT call these tools in parallel and instead call them sequentially. For instance, if
  one operation must complete before another starts, run these operations sequentially
  instead. Never use placeholders or guess missing parameters in tool calls.
- If the user specifies that they want you to run tools "in parallel", you
  MUST send a single message with multiple tool use content blocks. For example, if you
  need to launch both a build-validator agent and a test-runner agent in parallel,
  send a single message with both tool calls.
- Use specialized tools instead of bash commands when possible, as this provides a better
  user experience. For file operations, use dedicated tools: Read for reading files
  instead of cat/head/tail, Edit for editing instead of sed/awk, and Write for
  creating files instead of cat with heredoc or echo redirection. Reserve bash tools
  exclusively for actual system commands and terminal operations that require shell execution.
  NEVER use bash echo or other command-line tools to communicate thoughts,
  explanations, or instructions to the user. Output all communication directly in your
  response text instead.
- VERY IMPORTANT: When exploring the codebase to gather context or to answer a
  question that is not a needle query for a specific file/class/function, it is CRITICAL
  that you use the Task tool with subagent_type=Explore instead of running search
  commands directly.

`
}

func (b *SystemPromptBuilder) getCodeReferences() string {
	return `# Code References
When referencing specific functions or pieces of code include the pattern
file_path:line_number to allow the user to easily navigate to the source code
location.

Example:
user: Where are errors from client handled?
assistant: Clients are marked as failed in the connectToServer function in
  src/services/process.ts:712.

`
}

func (b *SystemPromptBuilder) getEnvironmentSection() string {
	return fmt.Sprintf(`## Environment
Working directory: %s
Is directory a git repository: %v
Platform: %s
OS Version: %s

Today's date: %s

`, b.workingDir, b.isGitRepo, runtime.GOOS, runtime.GOARCH, time.Now().Format("2006-01-02"))
}

func (b *SystemPromptBuilder) getBuiltInTools() string {
	// Define all available tools with their descriptions
	allTools := []struct {
		name        string
		description string
	}{
		{"bash", "Executes bash commands with optional timeout"},
		{"read", "Reads files from the filesystem with optional offset/limit"},
		{"write", "Writes files to the filesystem"},
		{"edit", "Performs exact string replacements in files"},
		{"glob", "Fast file pattern matching with glob patterns"},
		{"grep", "Powerful regex search tool"},
		{"task", "Launch subagents for complex tasks"},
		{"skill", "Execute a skill to load specialized domain knowledge"},
		{"ask_user_question", "Ask the user questions during execution"},
		{"webfetch", "Fetch content from URLs"},
		{"websearch", "Search the web for information"},
	}

	// Filter to only enabled tools
	var enabled []struct {
		name        string
		description string
	}
	for _, tool := range allTools {
		if b.isToolEnabled(tool.name) {
			enabled = append(enabled, tool)
		}
	}

	if len(enabled) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Available Tools\n\n")
	sb.WriteString("The following tools are available for use:\n\n")
	for i, tool := range enabled {
		sb.WriteString(fmt.Sprintf("%d. **%s** - %s\n", i+1, tool.name, tool.description))
	}
	sb.WriteString("\nEach tool has specific parameters - refer to the tool schema for details.\n\n")
	return sb.String()
}

// isToolEnabled checks if a tool is in the enabled list
// If enabledTools is empty, all tools are considered enabled
func (b *SystemPromptBuilder) isToolEnabled(name string) bool {
	if len(b.enabledTools) == 0 {
		return true
	}
	for _, enabled := range b.enabledTools {
		if enabled == name {
			return true
		}
	}
	return false
}

// LoadProjectClaudeMD loads project-specific CLAUDE.md if it exists
func (b *SystemPromptBuilder) LoadProjectClaudeMD(projectRoot string) string {
	content := ""

	// Try to load CLAUDE.md
	claudePath := filepath.Join(projectRoot, "CLAUDE.md")
	if data, err := os.ReadFile(claudePath); err == nil {
		content += "## Project CLAUDE.md\n\n" + string(data) + "\n\n"
		VerboseLog("Loaded project CLAUDE.md: %s (%d bytes)", claudePath, len(data))
	}

	// Try to load CLAUDE.local.md
	localPath := filepath.Join(projectRoot, "CLAUDE.local.md")
	if data, err := os.ReadFile(localPath); err == nil {
		content += "## Project CLAUDE.local.md\n\n" + string(data) + "\n\n"
		VerboseLog("Loaded project CLAUDE.local.md: %s (%d bytes)", localPath, len(data))
	}

	return content
}

// LoadGlobalClaudeMD loads global CLAUDE.md if it exists
func (b *SystemPromptBuilder) LoadGlobalClaudeMD() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	claudePath := filepath.Join(homeDir, ".claude", "CLAUDE.md")
	if data, err := os.ReadFile(claudePath); err == nil {
		VerboseLog("Loaded global CLAUDE.md: %s (%d bytes)", claudePath, len(data))
		return "## Global CLAUDE.md\n\n" + string(data) + "\n\n"
	}

	return ""
}

// LoadAgentsMD loads AGENTS.md if it exists
func (b *SystemPromptBuilder) LoadAgentsMD(projectRoot string) string {
	agentsPath := filepath.Join(projectRoot, "AGENTS.md")
	if data, err := os.ReadFile(agentsPath); err == nil {
		VerboseLog("Loaded AGENTS.md: %s (%d bytes)", agentsPath, len(data))
		return "## AGENTS.md\n\n" + string(data) + "\n\n"
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	globalAgentsPath := filepath.Join(homeDir, ".claude", "AGENTS.md")
	if data, err := os.ReadFile(globalAgentsPath); err == nil {
		VerboseLog("Loaded global AGENTS.md: %s (%d bytes)", globalAgentsPath, len(data))
		return "## Global AGENTS.md\n\n" + string(data) + "\n\n"
	}

	return ""
}
