package prompts

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// SystemPromptBuilder builds system prompts for the agent
type SystemPromptBuilder struct {
	workingDir string
	isGitRepo  bool
}

// NewSystemPromptBuilder creates a new system prompt builder
func NewSystemPromptBuilder(workingDir string, isGitRepo bool) *SystemPromptBuilder {
	if workingDir == "" {
		workingDir, _ = os.Getwd()
	}
	return &SystemPromptBuilder{
		workingDir: workingDir,
		isGitRepo:  isGitRepo,
	}
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

	prompt += b.getSystemRole()
	prompt += b.getToneAndStyle()
	prompt += b.getProfessionalGuidelines()
	prompt += b.getTimeEstimationPolicy()
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
	// Priority order (highest to lowest):
	// 1. skills/ (project-local shorthand)
	// 2. .claude/skills (project)
	// 3. ~/.claude/skills (global)

	seenSkills := make(map[string]bool)
	var skills []SkillInfo

	// 1. Load project-local skills/ directory
	if projectRoot != "" {
		localSkills := b.loadSkillsInfoFromDirWithDedup(projectRoot+"/skills", "local", seenSkills)
		skills = append(skills, localSkills...)
	}

	// 2. Load .claude/skills (project)
	if projectRoot != "" {
		projectSkills := b.loadSkillsInfoFromDirWithDedup(b.getProjectSkillsDir(projectRoot), "project", seenSkills)
		skills = append(skills, projectSkills...)
	}

	// 3. Load global skills ~/.claude/skills
	globalSkills := b.loadSkillsInfoFromDirWithDedup(b.getGlobalSkillsDir(), "global", seenSkills)
	skills = append(skills, globalSkills...)

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

	// Parse frontmatter lines
	frontmatter := strings.Join(lines[1:endIdx], "\n")

	// Extract name
	if strings.Contains(frontmatter, "name:") {
		for _, line := range strings.Split(frontmatter, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "name:") {
				info.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
				break
			}
		}
	}

	// Extract description
	if strings.Contains(frontmatter, "description:") {
		for _, line := range strings.Split(frontmatter, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "description:") {
				info.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
				break
			}
		}
	}

	return info
}

func (b *SystemPromptBuilder) getSystemRole() string {
	return `You are Agent-System, an interactive CLI agent that helps users with software engineering tasks.
Use the instructions below and the tools available to you to assist the user.

IMPORTANT: Assist with authorized security testing, defensive security, CTF challenges,
and educational contexts. Refuse requests for destructive techniques, DoS attacks, mass
targeting, supply chain compromise, or detection evasion for malicious purposes.
Dual-use security tools (C2 frameworks, credential testing, exploit development) require
clear authorization context: pentesting engagements, CTF competitions, security research, or
defensive use cases.

IMPORTANT: You must NEVER generate or guess URLs for the user unless you are confident
that the URLs are for helping the user with programming. You may use URLs provided
by the user in their messages or local files.

If user asks for help or wants to give feedback inform them of the following:
- /help: Get help with using the agent system
- To give feedback, users should report issues at the project repository

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
- Do not use a colon before tool calls. Your tool calls may not be shown directly
  in the output, so text like "Let me read the file:" followed by a read tool call
  should just be "Let me read the file." with a period.

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

func (b *SystemPromptBuilder) getTimeEstimationPolicy() string {
	return `# No time estimates
Never give time estimates or predictions about how long tasks will take, whether for
your own work or for users planning their projects. Avoid phrases like "this will
take me a few minutes," "should be done in about 5 minutes," "this is a
quick fix," "this will take 2-3 weeks," or "we can do this later."
Focus on what needs to be done, not how long it might take. Break work into
actionable steps and let users judge timing for themselves.

`
}

func (b *SystemPromptBuilder) getAskingQuestionsPolicy() string {
	return `# Asking questions as you work
You have access to AskUserQuestion tool to ask the user questions when you need
clarification, want to validate assumptions, or need to make a decision you're
unsure about. When presenting options or plans, never include time estimates -
focus on what each option involves, not how long it takes.

Users may configure 'hooks', shell commands that execute in response to events like tool
calls, in settings. Treat feedback from hooks as coming from the user. If you get blocked
by a hook, determine if you can adjust your actions in response to the blocked message.
If not, ask the user to check their hooks configuration.

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
- Use AskUserQuestion tool to ask questions, clarify and gather information as needed.
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
	return `## Available Tools

The following tools are available for use:

1. **bash** - Executes bash commands with optional timeout
2. **read** - Reads files from the filesystem with optional offset/limit
3. **write** - Writes files to the filesystem
4. **edit** - Performs exact string replacements in files
5. **glob** - Fast file pattern matching with glob patterns
6. **grep** - Powerful regex search tool
7. **task** - Launch subagents for complex tasks
8. **skill** - Execute a skill to load specialized domain knowledge
9. **ask_user_question** - Ask the user questions during execution
10. **webfetch** - Fetch content from URLs
11. **websearch** - Search the web for information

Each tool has specific parameters - refer to the tool schema for details.

`
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
