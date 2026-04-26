# Agents Reference

This document catalogs reference projects and implementations relevant to building agent systems.

## This Project: Agent System

A general-purpose agent system built with Go and langchaingo, featuring an agentic loop with parallel tool execution, subagents, and configurable prompts.

### Project Structure

| Directory | Purpose |
|-----------|---------|
| `cmd/agent/` | Main entry point (main.go) |
| `internal/agent/` | Main agent loop implementation |
| `internal/tools/` | Tool implementations (Bash, Read, Write, Edit, Glob, Grep, Task, Ask, WebFetch, WebSearch) |
| `internal/subagent/` | Subagent execution framework |
| `internal/config/` | YAML-based configuration system |
| `internal/prompts/` | System prompt builder |
| `internal/usage/` | LLM usage tracking |
| `pkg/llm/` | LLM provider integrations |

### Key Concepts

#### Tool Interface
All tools implement the `Tool` interface:
- `Name()` - Unique tool identifier
- `Description()` - Tool purpose for LLM
- `Schema()` - JSON schema for parameters
- `Execute()` - Tool execution logic

#### Tool Registry
Central registry (`ToolRegistry`) manages tool registration and execution:
- Thread-safe tool storage
- Parallel execution with semaphore-based limiting (default: 10 concurrent)
- Converts tools to langchaingo format

#### Agent Loop
The main agentic loop (`internal/agent/agent.go`):
1. Receives user input
2. Calls LLM with available tools
3. Executes tool calls in parallel
4. Returns results to LLM
5. Continues until final response or max turns reached

#### Subagent System
Task tool spawns specialized subagents:
- **Fork Mode**: Subagent inherits parent's conversation history (`fork=true`)
- **Agent Types**: Bash, Explore, Plan, general-purpose, code-reviewer, claude-code-guide
- **Session Persistence**: Conversations saved to `~/.claude/myclaude/sessions/`

#### Configuration
YAML-based configuration (`config.yaml`):
- **Variables Section**: Key-value pairs with `${VAR}` interpolation
- **Models Section**: LLM provider settings (OpenAI, Anthropic, Bedrock, Ollama)
- **Tools Section**: Tool enablement and settings

#### Session Management
- Sessions stored as JSON files
- Resume with `-r <session_id>`
- Environment variable `MYAGENT_SESSIONS_DIRECTORY` overrides default path

## LangChain Go (langchaingo)

**Location**: `../ref/langchaingo/`
**Repository**: https://github.com/tmc/langchaingo
**Documentation**: https://tmc.github.io/langchaingo/docs/
**API Reference**: https://pkg.go.dev/github.com/tmc/langchaingo

### Overview

LangChain Go is the Go language implementation of the [LangChain](https://github.com/langchain-ai/langchain) framework. It provides a comprehensive set of tools for building applications with Large Language Models (LLMs) through composability.

### Project Structure

| Directory | Purpose |
|-----------|---------|
| `agents/` | Agent implementations (conversational, MRKL, OpenAI Functions) |
| `chains/` | Chain implementations for composing LLM calls |
| `tools/` | Tool implementations (calculator, search, database, etc.) |
| `llms/` | LLM provider integrations (OpenAI, Anthropic, Ollama, etc.) |
| `prompts/` | Prompt template management |
| `memory/` | Memory components for conversation history |
| `embeddings/` | Embedding model integrations |
| `vectorstores/` | Vector database integrations |
| `documentloaders/` | Document loading utilities |
| `textsplitter/` | Text splitting utilities |
| `outputparser/` | Output parsing utilities |
| `callbacks/` | Callback handlers for observability |
| `examples/` | Example implementations |

### Key Components for Agent Systems

#### Agent Types
- **Conversational Agent**: Maintains conversation history
- **MRKL Agent**: Uses ReAct pattern with tools
- **OpenAI Functions Agent**: Leverages OpenAI function calling

#### Tool Implementations Available
- Calculator (arithmetic operations)
- Search tools (DuckDuckGo, SerpAPI, Perplexity, Wikipedia)
- Database tools (SQL database queries)
- Web scraper
- Zapier integration

#### Chain Patterns
- Sequential chains (multi-step workflows)
- Constitutional chains (self-critique and refinement)
- Retrieval QA (RAG implementations)
- Conversational Retrieval QA
- Map-reduce patterns
- Summarization chains

### Integration with This Project

This agent-system project uses langchaingo for:
- LLM abstraction layer
- Tool execution framework
- Agent loop patterns
- Model provider integrations

See the project [README.md](./README.md) for specific implementation details and configuration.

### Full Documentation

For complete documentation, examples, and API reference, see the [langchaingo README.md](../ref/langchaingo/README.md) in the reference directory.
