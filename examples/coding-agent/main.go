// coding-agent demonstrates building an agentic development application
// using the y SDK with the Anthropic provider.
//
// Usage with Anthropic API:
//
//	export ANTHROPIC_API_KEY=sk-ant-...
//	go run . "Refactor the main function to use context properly"
//
// Usage with Kimi Code (Anthropic-compatible endpoint):
//
//	export ANTHROPIC_BASE_URL=https://api.kimi.com/coding/
//	export ANTHROPIC_API_KEY=$KIMI_API_KEY
//	go run . "Explain this codebase"
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers/anthropic"
	"github.com/yuri/y/pkg/tools"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── Provider Setup ───────────────────────────────────────────────
	// Create an Anthropic provider. Supports Anthropic API or compatible
	// endpoints (e.g. Kimi Code via ANTHROPIC_BASE_URL).
	//
	// Auth priority:
	// 1. WithAPIKey option (below)
	// 2. ANTHROPIC_OAUTH_TOKEN env var (Bearer token)
	// 3. ANTHROPIC_API_KEY env var
	//
	// Base URL priority:
	// 1. WithBaseURL option (below)
	// 2. ANTHROPIC_BASE_URL env var
	fmt.Println(os.Getenv("ANTHROPIC_API_KEY"))
	fmt.Println(os.Getenv("ANTHROPIC_BASE_URL"))
	anthropicOpts := []anthropic.Option{
		anthropic.WithAPIKey(os.Getenv("ANTHROPIC_API_KEY")),
		anthropic.WithBaseURL(os.Getenv("ANTHROPIC_BASE_URL")),
	}
	provider := anthropic.New(anthropicOpts...)

	// ── Model Selection ──────────────────────────────────────────────
	// Use Claude Sonnet for Anthropic API or kimi-compatible model for
	// Kimi Code endpoint. You can also let the agent auto-select the
	// first available model from the provider.
	modelID := "kimi-k2p6"
	modelName := "Kimi K2.6"
	model := ai.Model{
		ID:        modelID,
		Name:      modelName,
		Provider:  "anthropic",
		Reasoning: true,
		Input:     []ai.InputKind{ai.InputText},
	}

	// ── Tool Registry ────────────────────────────────────────────────
	// Create a registry and register built-in y SDK tools: filesystem,
	// shell, and git. A permissive policy allows sensitive tools like
	// write_file and edit to run without interactive approval.
	workspaceRoot := mustGetwd() + "/workspace"
	registry := tools.NewRegistry(
		tools.WithPolicy(tools.PolicyFunc(func(ctx context.Context, req tools.PolicyRequest) (tools.PolicyDecision, error) {
			return tools.PolicyDecision{Kind: tools.DecisionAllow}, nil
		})),
	)
	if err := registerCodingTools(registry, workspaceRoot); err != nil {
		return fmt.Errorf("register tools: %w", err)
	}

	// ── Agent Configuration ──────────────────────────────────────────
	// Create the agent with provider, model, tools, and event handling.
	// The agent manages the transcript and runs the provider/tool loop.
	a := agent.New(provider, registry,
		agent.WithModel(model),
		agent.WithSystemPrompt(codingSystemPrompt()),
		agent.WithWorkspaceRoot(workspaceRoot),
		agent.WithMaxTurns(16),
		agent.WithToolExecutionMode(agent.ToolExecutionParallel),
		agent.WithEventSink(eventHandler()),
	)

	// ── Interactive or Single-Prompt Mode ────────────────────────────
	args := os.Args[1:]
	if len(args) > 0 {
		// Single prompt mode: run once and exit.
		prompt := strings.Join(args, " ")
		fmt.Printf("\n\x1b[1;36mUser:\x1b[0m %s\n\n", prompt)
		result, err := a.Run(ctx, prompt)
		if err != nil {
			return fmt.Errorf("agent run failed: %w", err)
		}
		printResult(result)
		return nil
	}

	// Interactive REPL mode.
	fmt.Println("\n\x1b[1;32mCoding Agent\x1b[0m — type a prompt or 'quit' to exit")
	fmt.Println("Model:", model.Name, "("+model.ID+")")
	if baseURL := os.Getenv("ANTHROPIC_BASE_URL"); baseURL != "" {
		fmt.Println("Base URL:", baseURL)
	}
	fmt.Println("Workspace:", mustGetwd())
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\x1b[1;36m>\x1b[0m ")
		if !scanner.Scan() {
			break
		}
		prompt := strings.TrimSpace(scanner.Text())
		if prompt == "" {
			continue
		}
		if prompt == "quit" || prompt == "exit" {
			fmt.Println("Goodbye!")
			break
		}
		if prompt == "transcript" {
			printTranscript(a.Transcript())
			continue
		}
		if prompt == "clear" {
			a.Reset()
			fmt.Println("Transcript cleared.")
			continue
		}

		result, err := a.Run(ctx, prompt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n\x1b[1;31mError:\x1b[0m %v\n\n", err)
			continue
		}
		printResult(result)
	}
	return scanner.Err()
}

// eventHandler returns an agent.EventSink that prints streaming output.
func eventHandler() agent.EventSink {
	return func(e agent.Event) {
		switch e.Kind {
		case agent.EventTurnStarted:
			fmt.Printf("\n\x1b[1;33m[Turn %d]\x1b[0m ", e.Turn)
		case agent.EventTextDelta:
			fmt.Print(e.TextDelta)
		case agent.EventToolStarted:
			fmt.Printf("\n\x1b[90m  [Tool: %s]\x1b[0m\n", e.ToolCall.Name)
		case agent.EventToolEnded:
			status := "done"
			if e.Err != nil {
				status = fmt.Sprintf("error: %v", e.Err)
			}
			fmt.Printf("\x1b[90m  [Tool result: %s]\x1b[0m\n", status)
		case agent.EventTurnEnded:
			fmt.Println() // newline after assistant response
		case agent.EventCompleted:
			fmt.Printf("\n\x1b[1;32m[Completed in %d turns]\x1b[0m\n", e.Turn)
		case agent.EventStateChanged:
			if e.State == agent.StateStreaming {
				fmt.Print("\x1b[1;35mAssistant:\x1b[0m ")
			}
		}
	}
}

// printResult prints the final result summary.
func printResult(result agent.RunResult) {
	fmt.Printf("\n\x1b[90m---\n")
	fmt.Printf("Turns:  %d\n", result.Turns)
	fmt.Printf("State:  %s\n", result.State)
	fmt.Printf("Stop:   %s\n", result.StopReason)
	fmt.Printf("Tokens: %d in / %d out (total: %d)\n",
		result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.TotalTokens)
	if result.Usage.Cost.Total > 0 {
		fmt.Printf("Cost:   $%.6f\n", result.Usage.Cost.Total)
	}
	fmt.Printf("---\x1b[0m\n")
}

// printTranscript prints the conversation transcript.
func printTranscript(messages []ai.Message) {
	fmt.Println("\n\x1b[1;33m=== Transcript ===\x1b[0m")
	for i, m := range messages {
		role := string(m.Role)
		content := ""
		for _, block := range m.Content {
			content += block.Text
		}
		if m.ToolResult != nil {
			role = "tool"
			for _, block := range m.ToolResult.Content {
				content += block.Text
			}
			if len(content) > 200 {
				content = content[:200] + "..."
			}
		}
		fmt.Printf("\n\x1b[90m[%d] %s:\x1b[0m %s\n", i, role, content)
	}
	fmt.Println("\n\x1b[1;33m==================\x1b[0m")
}

// codingSystemPrompt returns the system prompt optimized for coding tasks.
func codingSystemPrompt() string {
	return `You are a senior software engineer AI assistant. You help with code analysis, refactoring, testing, and implementation.

Available tools:
- read_file: Read a file's contents
- write_file: Write or overwrite a file
- list_files: List files in a directory
- search: Search for text patterns in files
- edit: Edit a file with old/new text replacement
- patch: Apply a unified diff patch
- run_command: Execute a shell command (tests, builds, git, etc.)
- git_status: Check git status
- git_diff: View git diff
- git_commit: Create a git commit

When working on tasks:
1. First understand the codebase by reading relevant files with read_file
2. Use search to find related code
3. Use run_command to run tests, builds, or any shell commands to verify changes
4. Propose changes using write_file or edit
5. Always verify your changes compile and tests pass

Keep responses concise and focused. When writing code, follow existing style and conventions in the project.`
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return wd
}
