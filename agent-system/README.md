# Agent System

A general-purpose agent system built with Go and langchaingo, featuring an agentic loop with parallel tool execution, subagents, and embedded prompts from the context-building specification.

## Features

- **Modular Tool System**: Implements core tools (Bash, Read, Write, Edit, Glob, Grep, AskUser, WebFetch, WebSearch)
- **Parallel Tool Execution**: Tools can run in parallel using goroutines with configurable concurrency limits
- **Subagent Support**: Task tool allows spawning specialized subagents for complex tasks
- **Configurable Models**: Initialize models from YAML config file supporting multiple providers (OpenAI, Anthropic, Ollama)
- **Embedded Prompts**: System prompts based on the context-building.md specification
- **Agentic Loop**: Main loop handles tool calls, executes them, and passes results back to the LLM

## Installation

```bash
go get github.com/tmc/langchaingo
go mod tidy
```

## Configuration

Create a `config.yaml` file:

```yaml
default_model: "gpt-4"

models:
  gpt-4:
    name: "gpt-4"
    provider: "openai"
    model_id: "gpt-4"
    api_key_env: "OPENAI_API_KEY"
    max_tokens: 4096
    temperature: 0.7

tools:
  bash:
    enabled: true
    default_timeout_ms: 120000
  
  read:
    enabled: true
    max_lines: 2000
  
  write:
    enabled: true
  
  edit:
    enabled: true
  
  glob:
    enabled: true
    max_results: 1000
  
  grep:
    enabled: true
    max_results: 1000
  
  task:
    enabled: true
    default_model: "gpt-4"
    max_concurrent: 5
  
  ask_user:
    enabled: true
  
  webfetch:
    enabled: true
    timeout_secs: 30
  
  websearch:
    enabled: true
    max_results: 8
```

## Usage

### Run the Agent

```bash
go run cmd/agent/main.go -config config.yaml -model gpt-4
```

### Command Line Options

- `-config`: Path to configuration file (default: "config.yaml")
- `-model`: Model name to use (overrides default)
- `-workdir`: Working directory (defaults to current)
- `-max-turns`: Maximum agentic turns (default: 50)

### Interactive Commands

Once running, you can:
- Type queries and the agent will respond
- Use `/help` to see available commands
- Type `exit` or `quit` to end the session

## Architecture

### Core Components

1. **Agent** (`internal/agent/`)
   - Main agent loop implementation
   - Conversation management
   - Tool orchestration

2. **Tools** (`internal/tools/`)
   - Tool interface and registry
   - Parallel execution support
   - Individual tool implementations

3. **Subagents** (`internal/subagent/`)
   - Subagent execution
   - Specialized agent types (Bash, Explore, Plan, etc.)
   - Task tool integration

4. **Configuration** (`internal/config/`)
   - YAML-based configuration
   - Model management
   - Tool settings

5. **Prompts** (`internal/prompts/`)
   - System prompt builder
   - Context assembly from context-building.md
   - Professional guidelines and policies

### Tool Implementations

#### Bash Tool
Executes shell commands with configurable timeout, working directory support, and command filtering.

#### Read Tool
Reads files with support for:
- Line offsets and limits
- Directory listing
- PDF support (placeholder)

#### Write Tool
Writes content to files with automatic directory creation and overwrite protection.

#### Edit Tool
Performs exact string replacements in files with:
- Context matching
- Replace all option
- Multi-occurrence detection

#### Glob Tool
Fast file pattern matching using doublestar library with:
- Glob patterns (e.g., `**/*.go`)
- Result limiting
- Modification time sorting

#### Grep Tool
Regex-based file search with:
- Content, files_with_matches, and count output modes
- Context lines (-A, -B, -C)
- Case insensitive search
- File type filtering

#### Task Tool
Spawns subagents for complex tasks:
- Multiple agent types (Bash, Explore, Plan, etc.)
- Background execution support
- Resume capability
- Parallel subagent execution

#### AskUserQuestion Tool
Interactive user questioning with:
- Multiple choice support
- Free text input
- Multi-select options

#### WebFetch Tool
Fetches content from URLs with:
- Format conversion (text, markdown, HTML)
- Query-based content extraction
- Timeout configuration

#### WebSearch Tool
Web search integration (requires API configuration):
- Multiple provider support
- Configurable result counts
- Result formatting

## System Prompts

The system implements prompts based on the context-building.md specification:

- System role definition
- Tone and style guidelines
- Professional productivity guidelines
- Time estimation policies
- Tool usage policies
- Code reference format
- Environment information

## Parallel Execution

Tools execute in parallel using goroutines with a semaphore-based limiter (default: 10 concurrent calls). This allows:
- Faster completion of independent tasks
- Efficient use of system resources
- Configurable concurrency limits

## Subagent Types

The Task tool supports multiple subagent types:

- **Bash**: Command execution specialist
- **Explore**: Fast codebase exploration
- **Plan**: Software architecture planning
- **general-purpose**: Research and multi-step tasks
- **code-reviewer**: Code review specialist
- **claude-code-guide**: Documentation specialist

## Extending the System

### Adding a New Tool

1. Create a new file in `internal/tools/`
2. Implement the `Tool` interface:
   - `Name() string`
   - `Description() string`
   - `Schema() *ToolSchema`
   - `Execute(ctx context.Context, params json.RawMessage) (ToolResult, error)`
3. Register in `registerTools()` function in `internal/agent/agent.go`

### Adding a New Model Provider

1. Implement LLM creation in `createLLM()` function in `internal/agent/agent.go`
2. Add provider-specific configuration to `config.yaml`
3. Install the provider's langchaingo subpackage

## License

MIT License

## Contributing

Contributions are welcome! Please ensure:
- Code follows Go conventions
- Tests are included for new features
- Documentation is updated
- Configuration examples are provided