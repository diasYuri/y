# Coding Agent Example

A complete, working example of building an agentic development application using the `y` SDK with the Anthropic provider.

## What it demonstrates

- Provider configuration (Anthropic Messages API)
- Agent creation with custom tools
- Streaming event handling
- Tool execution with filesystem and command tools
- Session management and transcript handling
- Real-time output streaming to the terminal

## Prerequisites

- Go 1.24+
- Anthropic API key (`ANTHROPIC_API_KEY` environment variable)

## Running

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run .
```

## Project Structure

```
coding-agent/
├── main.go      # Entry point: provider setup, agent configuration, run loop
├── tools.go     # Custom tool definitions (file read, command exec, code analysis)
├── go.mod       # Module definition (replace points to y)
└── README.md    # This file
```

## Architecture

```
User Prompt
     |
     v
+---------+     +-------------------+     +------------------+
| Agent   | --> | Anthropic Provider| --> | Claude API       |
| (y SDK) |     | (Streaming SSE)   |     | (Messages API)   |
+---------+     +-------------------+     +------------------+
     |
     | Events (TextDelta, ToolCall, Usage, Stop)
     v
+---------+     +-------------------+
| Terminal| <-- | Custom Tools      |
| Output  |     | (Registry)        |
+---------+     +-------------------+
```

## Adapting to Other Providers

### OpenAI-Compatible (for Kimi Code, local models, etc.)

Replace the Anthropic provider setup with:

```go
import "github.com/yuri/y/pkg/providers/openai_compatible"

provider := openai_compatible.New(
    openai_compatible.WithBaseURL("https://api.moonshot.cn/v1"), // Kimi
    openai_compatible.WithAPIKey(os.Getenv("KIMI_API_KEY")),
)

model := ai.Model{
    ID:       "kimi-code",
    Provider: "openai_compatible",
    BaseURL:  "https://api.moonshot.cn/v1",
}
```

### Google Gemini

```go
import "github.com/yuri/y/pkg/providers/google"

provider := google.New(
    google.WithAPIKey(os.Getenv("GEMINI_API_KEY")),
)
```

## Key SDK Concepts

1. **Provider** (`pkg/providers`) — Normalizes streaming HTTP APIs into `ai.Event` types.
2. **Agent** (`pkg/agent`) — Runs the provider/tool loop, manages transcript, handles tool execution.
3. **Tools** (`pkg/tools`) — Register filesystem, command, git, and custom tools.
4. **AI Types** (`pkg/ai`) — Normalized events: `TextDelta`, `ToolCallEvent`, `UsageEvent`, `StopEvent`.
