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

### Settings Format

Configuration is defined in YAML format with three main sections:

#### Variables Section

Arbitrary key-value pairs that can be referenced elsewhere using `${VAR_NAME}` syntax:

```yaml
variables:
  OPENAI_URL: "https://api.openai.com/v1"
  AWS_REGION: "us-east-1"
  API_KEY: "your-key-here"
```

#### Models Section

Defines available LLM models with provider-specific settings:

```yaml
default_model: "nova-2-lite"

models:
  my-model:
    name: "Display Name"
    provider: "bedrock"           # bedrock, openai, ollama, etc.
    model_id: "model.identifier"
    api_key: "${API_KEY}"         # or api_key_env: "ENV_VAR_NAME"
    base_url: "https://api..."    # for openai provider
    region: "${AWS_REGION}"       # for bedrock
    aws_profile: "dev"            # for bedrock with SSO
    max_tokens: 32768
    temperature: 0.7
    soft_tools: true              # use soft tools mode
    cache_points: false           # enable prompt caching
```

#### Tools Section

Configures tool availability and behavior:

```yaml
tools:
  bash:
    enabled: true
    default_timeout_ms: 120000
    allowed_commands: ["git", "ls"]  # optional whitelist
    blocked_commands: ["rm", "dd"]   # optional blacklist

  grep:
    enabled: true
    max_results: 1000
    max_context_lines: 100

  glob:
    enabled: true
    max_results: 1000

  read:
    enabled: true
    read_limit: 80000

  write:
    enabled: true

  edit:
    enabled: true

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
    timeout_secs: 30
```

### File Location

Configuration files are resolved in the following order:

1. `config.yaml` - Base configuration (optional)
2. `config.local.yaml` - Local overrides (optional)

The system searches for these files in two locations (in order):

- **Current working directory** - Checked first
- **Binary directory** - Checked as fallback (where the executable is located)

If both files exist, they are merged (local overrides take precedence). Variable interpolation occurs after merging, supporting:
- Custom variables: `${MY_VAR}` from the `variables` section
- Environment variables: `${env.ENV_NAME}` from the system environment

### System Arguments

| Flag | Shorthand | Description | Default |
|------|-----------|-------------|---------|
| `-config` | | Path to configuration file | `config.yaml` |
| `-model` | `-m` | Model name to use (overrides default) | empty |
| `-workdir` | | Working directory | current directory |
| `-max-turns` | | Maximum agentic turns | `50` |
| `-p` | | Single step prompt to execute | empty |
| `-r` | | Resume conversation from session ID | empty |
| `-json` | | Output in JSON format | `false` |
| `-raw` | | Output only final response text (for scripting) | `false` |
| `-no-session` | | Disable session saving | `false` |
| `-tools` | | Tools to enable (comma-separated) | `DEFAULT` |
| `-read-limit` | | Maximum bytes to read from a file | `80000` |

#### Listing Options

- List available models: `./agent -m` (without value)
- List available tools: `./agent -tools` (without value)

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