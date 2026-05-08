//go:build feature_rpc

package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yuri/y/internal/feature"
	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/rpc"
)

func runRPC(stdout, stderr io.Writer, args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printRPCUsage(stdout)
		return 0
	}

	opts := headlessOptions{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--provider":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "y rpc: --provider requires a value")
				return exitCodeUsage
			}
			i++
			opts.providerName = args[i]
		case "--model":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "y rpc: --model requires a value")
				return exitCodeUsage
			}
			i++
			opts.model = args[i]
		case "--api-key":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "y rpc: --api-key requires a value")
				return exitCodeUsage
			}
			i++
			opts.apiKey = args[i]
		case "--system-prompt":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "y rpc: --system-prompt requires a value")
				return exitCodeUsage
			}
			i++
			opts.systemPrompt = args[i]
		case "--addr":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "y rpc: --addr requires a value")
				return exitCodeUsage
			}
			i++
			// addr is parsed below from opts or env
		default:
			if strings.HasPrefix(arg, "-") {
				fmt.Fprintf(stderr, "y rpc: unknown flag %q\n", arg)
				return exitCodeUsage
			}
			fmt.Fprintf(stderr, "y rpc: unexpected argument %q\n", arg)
			return exitCodeUsage
		}
	}

	ctx := context.Background()
	compiled := feature.NewRegistry()
	_ = feature.RegisterCompiledFeatures(compiled)

	provider, err := defaultHeadlessProviderFactory(ctx, compiled, opts)
	if err != nil {
		fmt.Fprintf(stderr, "y rpc: %v\n", err)
		return headlessExitCode(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "y rpc: %v\n", err)
		return exitCodeExecution
	}

	registry, err := buildHeadlessRegistry(ctx, compiled, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "y rpc: %v\n", err)
		return headlessExitCode(err)
	}

	addr := os.Getenv("Y_RPC_ADDR")
	if addr == "" {
		addr = ":8081"
	}

	cfg := rpc.ServerConfig{
		Addr:         addr,
		Log:          stderr,
		Provider:     provider,
		ToolRegistry: registry,
		Model:        ai.Model{ID: opts.model},
		SystemPrompt: opts.systemPrompt,
	}
	server := rpc.NewServer(cfg)

	fmt.Fprintf(stdout, "y rpc server listening on %s\n", addr)
	fmt.Fprintln(stdout, "endpoints: POST /rpc  GET /events  GET /health")

	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(stderr, "y rpc: server error: %v\n", err)
		return exitCodeExecution
	}
	return 0
}

func printRPCUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  y rpc [--addr <host:port>] [--provider <name>] [--model <id>] [--api-key <key>]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Options:")
	fmt.Fprintln(w, "  --addr <host:port>      Bind address (default :8081, env Y_RPC_ADDR).")
	fmt.Fprintln(w, "  --provider <name>       Select provider explicitly.")
	fmt.Fprintln(w, "  --model <id>            Select a specific model ID.")
	fmt.Fprintln(w, "  --api-key <key>         Override provider API key.")
	fmt.Fprintln(w, "  --system-prompt <text>  Override the system prompt.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Endpoints:")
	fmt.Fprintln(w, "  POST /rpc    JSON-RPC 2.0 endpoint (methods: chat, models, tools, transcript, clear)")
	fmt.Fprintln(w, "  GET /events  Server-Sent Events stream")
	fmt.Fprintln(w, "  GET /health  Health check")
}

// Ensure agent.Provider interface is satisfied by the RPC server provider.
var _ agent.Provider = (agent.Provider)(nil)
