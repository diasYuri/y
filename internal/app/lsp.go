//go:build feature_lsp

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/yuri/y/pkg/lsp"
)

func runLSP(stdout, stderr io.Writer, args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printLSPUsage(stdout)
		return 0
	}

	if len(args) == 0 {
		fmt.Fprintln(stderr, "y lsp: language server command is required")
		printLSPUsage(stderr)
		return exitCodeUsage
	}

	serverCmd := args[0]
	serverArgs := args[1:]

	// Resolve the root URI.
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "y lsp: %v\n", err)
		return exitCodeExecution
	}
	rootURI := "file://" + cwd

	// Start the language server subprocess.
	cmd := exec.Command(serverCmd, serverArgs...)
	cmd.Stderr = stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(stderr, "y lsp: failed to create stdin pipe: %v\n", err)
		return exitCodeExecution
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(stderr, "y lsp: failed to create stdout pipe: %v\n", err)
		return exitCodeExecution
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "y lsp: failed to start %s: %v\n", serverCmd, err)
		return exitCodeExecution
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	client := lsp.NewClient(stdin, stdoutPipe)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fmt.Fprintf(stdout, "Connecting to %s...\n", serverCmd)

	result, err := client.Initialize(ctx, rootURI)
	if err != nil {
		fmt.Fprintf(stderr, "y lsp: initialize failed: %v\n", err)
		return exitCodeExecution
	}

	fmt.Fprintln(stdout, "Initialized successfully")
	var pretty map[string]any
	if err := json.Unmarshal(result, &pretty); err == nil {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(pretty)
	} else {
		fmt.Fprintln(stdout, string(result))
	}

	// Shutdown gracefully.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := client.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(stderr, "y lsp: shutdown: %v\n", err)
	}

	return 0
}

func runLSPHover(stdout, stderr io.Writer, args []string) int {
	if len(args) < 3 {
		fmt.Fprintln(stderr, "y lsp hover: usage: y lsp hover <file> <line> <character>")
		return exitCodeUsage
	}

	file := args[0]
	line, err := strconv.Atoi(args[1])
	if err != nil {
		fmt.Fprintf(stderr, "y lsp hover: invalid line number: %v\n", err)
		return exitCodeUsage
	}
	char, err := strconv.Atoi(args[2])
	if err != nil {
		fmt.Fprintf(stderr, "y lsp hover: invalid character position: %v\n", err)
		return exitCodeUsage
	}

	// For now, hover requires the server to be already running.
	// Full implementation would reuse a persistent connection.
	fmt.Fprintf(stderr, "y lsp hover: not yet implemented (requires persistent LSP server connection)\n")
	_ = file
	_ = line
	_ = char
	return 1
}

func printLSPUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  y lsp <server-cmd> [args...]  Start a language server and initialize.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  y lsp gopls")
	fmt.Fprintln(w, "  y lsp typescript-language-server --stdio")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The command starts the language server, sends an initialize")
	fmt.Fprintln(w, "request with the current working directory as rootUri, prints")
	fmt.Fprintln(w, "the server capabilities, and then shuts down gracefully.")
}

// normalizeFileURI ensures a file path is absolute before converting to a file:// URI.
func normalizeFileURI(path string) string {
	if strings.HasPrefix(path, "file://") {
		return path
	}
	abs, err := os.Getwd()
	if err == nil && !strings.HasPrefix(path, "/") {
		path = abs + "/" + path
	}
	return "file://" + path
}
