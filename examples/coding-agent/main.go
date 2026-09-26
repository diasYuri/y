// coding-agent demonstrates building an agentic development application
// using the y SDK with the OPENAI provider.
//
// Usage with OPENAI API:
//
//	export OPENAI_API_KEY=sk-ant-...
//	go run . "Refactor the main function to use context properly"
//
// Usage with Kimi Code (OPENAI-compatible endpoint):
//
//	export OPENAI_BASE_URL=https://api.kimi.com/coding/
//	export OPENAI_API_KEY=$KIMI_API_KEY
//	go run . "Explain this codebase"
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/diasYuri/y/pkg/agent"
	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/coding"
	"github.com/diasYuri/y/pkg/tools"
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

	// ── Quickstart Setup ─────────────────────────────────────────────
	// coding.NewAgent wires the provider, model catalog, built-in tools
	// (filesystem, shell, git) and the agent loop from a declarative
	// config. Auth priority:
	// 1. AgentConfig.APIKey (below)
	// 2. OPENAI_API_KEY / OPENAI_OAUTH_TOKEN env vars
	//
	// Base URL priority:
	// 1. AgentConfig.BaseURL (below)
	// 2. OPENAI_BASE_URL env var
	workspaceRoot := filepath.Join(mustGetwd(), "workspace")
	a, err := coding.NewAgent(ctx, coding.AgentConfig{
		ProviderID:    "openai",
		APIKey:        os.Getenv("OPENROUTER_API_KEY"),
		BaseURL:       os.Getenv("OPENROUTER_BASE_URL"),
		ModelID:       "nex-agi/nex-n2.5-mini:free",
		WorkspaceRoot: workspaceRoot,
		MaxTurns:      16,
		// A permissive policy allows sensitive tools like write_file and
		// edit to run without interactive approval. Suitable for local
		// development; the default tools.WorkspacePolicy() is stricter.
		Policy: tools.PolicyFunc(func(ctx context.Context, req tools.PolicyRequest) (tools.PolicyDecision, error) {
			return tools.PolicyDecision{Kind: tools.DecisionAllow}, nil
		}),
	},
		agent.WithToolExecutionMode(agent.ToolExecutionParallel),
		agent.WithEventSink(eventHandler()),
	)
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}

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
	if baseURL := os.Getenv("OPENAI_BASE_URL"); baseURL != "" {
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

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return wd
}
