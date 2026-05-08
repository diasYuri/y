package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/yuri/y/internal/feature"
	"github.com/yuri/y/internal/storage"
	"github.com/yuri/y/internal/telemetry"
	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/tools"
)

const (
	exitCodeUsage     = 2
	exitCodeConfig    = 3
	exitCodeExecution = 4
	exitCodeCanceled  = 130

	defaultSessionBytes = 8 << 20
)

type headlessOptions struct {
	providerName string
	model        string
	apiKey       string
	systemPrompt string
	sessionDir   string
	noSession    bool
}

type headlessProviderFactory func(context.Context, *feature.Registry, headlessOptions) (agent.Provider, error)

type headlessStreamWriter struct {
	w                     io.Writer
	wroteText             bool
	lastDeltaEndedNewline bool
	writeErr              error
}

func (s *headlessStreamWriter) WriteText(text string) {
	if s == nil || s.w == nil || text == "" || s.writeErr != nil {
		return
	}
	_, err := io.WriteString(s.w, text)
	if err != nil {
		s.writeErr = err
		return
	}
	s.wroteText = true
	s.lastDeltaEndedNewline = strings.HasSuffix(text, "\n")
}

func (s *headlessStreamWriter) Reset() {
	if s == nil {
		return
	}
	s.wroteText = false
	s.lastDeltaEndedNewline = false
	s.writeErr = nil
}

func (s *headlessStreamWriter) Err() error {
	if s == nil {
		return nil
	}
	return s.writeErr
}

func (s *headlessStreamWriter) Finish() {
	if s == nil || s.w == nil {
		return
	}
	if s.wroteText && !s.lastDeltaEndedNewline {
		_, _ = io.WriteString(s.w, "\n")
	}
}

type headlessError struct {
	code int
	err  error
}

func (e *headlessError) Error() string {
	if e == nil {
		return ""
	}
	if e.err != nil {
		return e.err.Error()
	}
	return "headless command error"
}

func (e *headlessError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func newHeadlessError(code int, err error) error {
	if err == nil {
		err = errors.New("headless command error")
	}
	return &headlessError{code: code, err: err}
}

func headlessExitCode(err error) int {
	if err == nil {
		return 0
	}
	var he *headlessError
	if errors.As(err, &he) && he.code != 0 {
		return he.code
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return exitCodeCanceled
	}
	return exitCodeExecution
}

func runRun(stdout, stderr io.Writer, stdin io.Reader, stdinTTY bool, args []string, info BuildInfo, compiled *feature.Registry) int {
	return runHeadlessCommand("run", stdout, stderr, stdin, stdinTTY, args, compiled, defaultHeadlessProviderFactory)
}

func runChat(stdout, stderr io.Writer, stdin io.Reader, stdinTTY bool, args []string, info BuildInfo, compiled *feature.Registry) int {
	return runHeadlessCommand("chat", stdout, stderr, stdin, stdinTTY, args, compiled, defaultHeadlessProviderFactory)
}

func runHeadlessCommand(
	mode string,
	stdout, stderr io.Writer,
	stdin io.Reader,
	stdinTTY bool,
	args []string,
	compiled *feature.Registry,
	providerFactory headlessProviderFactory,
) int {
	opts, promptArgs, ok := parseHeadlessArgs(mode, stdout, stderr, args)
	if !ok {
		return exitCodeUsage
	}

	if opts.providerName == "" {
		opts.providerName = strings.TrimSpace(os.Getenv("Y_PROVIDER"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "y %s: %v\n", mode, err)
		return exitCodeExecution
	}
	if opts.sessionDir == "" {
		opts.sessionDir = storage.DefaultAgentDir()
	}

	sessionStore := storage.NewSessionStore(opts.sessionDir)
	streamWriter := &headlessStreamWriter{w: stdout}
	teleEmitter := telemetry.DefaultEmitter
	agentOpts := []agent.Option{
		agent.WithWorkspaceRoot(cwd),
		agent.WithEventSink(func(ev agent.Event) {
			if ev.Kind == agent.EventTextDelta {
				streamWriter.WriteText(ev.TextDelta)
			}
			if ev.Kind == agent.EventTurnEnded {
				teleEmitter.Emit(telemetry.NewEvent(
					telemetry.EventAgentTurn,
					"",
					telemetry.AgentTurnPayload(ev.Turn, "", ev.Usage.InputTokens, ev.Usage.OutputTokens),
				))
			}
		}),
	}
	if opts.systemPrompt != "" {
		agentOpts = append(agentOpts, agent.WithSystemPrompt(opts.systemPrompt))
	}
	if opts.model != "" {
		agentOpts = append(agentOpts, agent.WithModel(ai.Model{ID: opts.model}))
	}

	run := func(agentInstance *agent.Agent, prompt string) (agent.RunResult, error) {
		return agentInstance.Run(ctx, prompt)
	}

	switch mode {
	case "run":
		prompt, promptErr := collectRunPrompt(stdin, stdinTTY, promptArgs)
		if promptErr != nil {
			fmt.Fprintf(stderr, "y run: %v\n", promptErr)
			return exitCodeUsage
		}
		if prompt == "" {
			fmt.Fprintln(stderr, "y run: prompt is required")
			return exitCodeUsage
		}
		provider, registry, runtimeCode := prepareHeadlessRuntime(ctx, mode, stderr, providerFactory, compiled, opts, cwd)
		if runtimeCode != 0 {
			return runtimeCode
		}
		return executeHeadlessTurn(ctx, stderr, run, provider, registry, agentOpts, sessionStore, cwd, prompt, opts.noSession, streamWriter)

	case "chat":
		inputPrompts := append([]string(nil), promptArgs...)
		if !stdinTTY {
			var promptErr error
			inputPrompts, promptErr = collectChatPrompts(stdin, promptArgs)
			if promptErr != nil {
				fmt.Fprintf(stderr, "y chat: %v\n", promptErr)
				return exitCodeExecution
			}
		}
		if stdinTTY {
			provider, registry, runtimeCode := prepareHeadlessRuntime(ctx, mode, stderr, providerFactory, compiled, opts, cwd)
			if runtimeCode != 0 {
				return runtimeCode
			}
			return runInteractiveChat(ctx, stderr, stdin, run, provider, registry, agentOpts, sessionStore, cwd, inputPrompts, opts.noSession, streamWriter)
		}
		if len(inputPrompts) == 0 {
			fmt.Fprintln(stderr, "y chat: prompt or stdin content is required")
			return exitCodeUsage
		}
		provider, registry, runtimeCode := prepareHeadlessRuntime(ctx, mode, stderr, providerFactory, compiled, opts, cwd)
		if runtimeCode != 0 {
			return runtimeCode
		}
		return executeChatPrompts(ctx, stderr, run, provider, registry, agentOpts, sessionStore, cwd, inputPrompts, opts.noSession, streamWriter)
	default:
		fmt.Fprintf(stderr, "y %s: unsupported headless mode\n", mode)
		return exitCodeUsage
	}
}

func prepareHeadlessRuntime(
	ctx context.Context,
	mode string,
	stderr io.Writer,
	providerFactory headlessProviderFactory,
	compiled *feature.Registry,
	opts headlessOptions,
	cwd string,
) (agent.Provider, *tools.Registry, int) {
	provider, err := providerFactory(ctx, compiled, opts)
	if err != nil {
		fmt.Fprintf(stderr, "y %s: %v\n", mode, err)
		return nil, nil, headlessExitCode(err)
	}
	registry, err := buildHeadlessRegistry(ctx, compiled, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "y %s: %v\n", mode, err)
		return nil, nil, headlessExitCode(err)
	}
	return provider, registry, 0
}

func executeHeadlessTurn(
	ctx context.Context,
	stderr io.Writer,
	run func(*agent.Agent, string) (agent.RunResult, error),
	provider agent.Provider,
	registry *tools.Registry,
	agentOpts []agent.Option,
	sessionStore *storage.SessionStore,
	cwd string,
	prompt string,
	noSession bool,
	streamWriter *headlessStreamWriter,
) int {
	agentInstance := agent.New(provider, registry, agentOpts...)
	streamWriter.Reset()
	result, err := run(agentInstance, prompt)
	streamWriter.Finish()
	if werr := streamWriter.Err(); werr != nil {
		return finishHeadlessError(stderr, werr)
	}
	if err != nil {
		return finishHeadlessError(stderr, err)
	}
	if !noSession {
		if err := saveHeadlessSession(ctx, sessionStore, cwd, agentInstance.Transcript()); err != nil {
			return finishHeadlessError(stderr, err)
		}
	}
	_ = result
	return 0
}

func executeChatPrompts(
	ctx context.Context,
	stderr io.Writer,
	run func(*agent.Agent, string) (agent.RunResult, error),
	provider agent.Provider,
	registry *tools.Registry,
	agentOpts []agent.Option,
	sessionStore *storage.SessionStore,
	cwd string,
	prompts []string,
	noSession bool,
	streamWriter *headlessStreamWriter,
) int {
	agentInstance := agent.New(provider, registry, agentOpts...)
	highestExit := 0
	for _, prompt := range prompts {
		streamWriter.Reset()
		result, err := run(agentInstance, prompt)
		streamWriter.Finish()
		if werr := streamWriter.Err(); werr != nil {
			return finishHeadlessError(stderr, werr)
		}
		if err != nil {
			exitCode := finishHeadlessError(stderr, err)
			if exitCode == exitCodeUsage || exitCode == exitCodeConfig || exitCode == exitCodeCanceled {
				return exitCode
			}
			if exitCode > highestExit {
				highestExit = exitCode
			}
			continue
		}
		_ = result
	}
	if !noSession {
		if err := saveHeadlessSession(ctx, sessionStore, cwd, agentInstance.Transcript()); err != nil {
			return finishHeadlessError(stderr, err)
		}
	}
	return highestExit
}

func runInteractiveChat(
	ctx context.Context,
	stderr io.Writer,
	stdin io.Reader,
	run func(*agent.Agent, string) (agent.RunResult, error),
	provider agent.Provider,
	registry *tools.Registry,
	agentOpts []agent.Option,
	sessionStore *storage.SessionStore,
	cwd string,
	initialPrompts []string,
	noSession bool,
	streamWriter *headlessStreamWriter,
) int {
	agentInstance := agent.New(provider, registry, agentOpts...)
	exitCode := 0
	for _, prompt := range initialPrompts {
		streamWriter.Reset()
		result, err := run(agentInstance, prompt)
		streamWriter.Finish()
		if werr := streamWriter.Err(); werr != nil {
			return finishHeadlessError(stderr, werr)
		}
		if err != nil {
			code := finishHeadlessError(stderr, err)
			if code == exitCodeUsage || code == exitCodeConfig || code == exitCodeCanceled {
				return code
			}
			if code > exitCode {
				exitCode = code
			}
			continue
		}
		_ = result
	}

	reader := bufio.NewReader(stdin)
	for {
		if _, err := io.WriteString(stderr, "y> "); err != nil {
			return exitCodeExecution
		}
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return finishHeadlessError(stderr, err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if errors.Is(err, io.EOF) {
				break
			}
			continue
		}
		if strings.EqualFold(line, "exit") || strings.EqualFold(line, "quit") {
			break
		}
		streamWriter.Reset()
		result, runErr := run(agentInstance, line)
		streamWriter.Finish()
		if werr := streamWriter.Err(); werr != nil {
			return finishHeadlessError(stderr, werr)
		}
		if runErr != nil {
			code := finishHeadlessError(stderr, runErr)
			if code == exitCodeUsage || code == exitCodeConfig || code == exitCodeCanceled {
				return code
			}
			if code > exitCode {
				exitCode = code
			}
			continue
		}
		_ = result
		if errors.Is(err, io.EOF) {
			break
		}
	}

	if !noSession {
		if err := saveHeadlessSession(ctx, sessionStore, cwd, agentInstance.Transcript()); err != nil {
			return finishHeadlessError(stderr, err)
		}
	}
	return exitCode
}

func finishHeadlessError(stderr io.Writer, err error) int {
	if err == nil {
		return 0
	}
	var he *headlessError
	if errors.As(err, &he) && he.code != 0 {
		fmt.Fprintln(stderr, he.err)
		return he.code
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintln(stderr, err)
		return exitCodeCanceled
	}
	fmt.Fprintln(stderr, err)
	return exitCodeExecution
}

func saveHeadlessSession(ctx context.Context, sessionStore *storage.SessionStore, cwd string, messages []ai.Message) error {
	if sessionStore == nil || len(messages) == 0 {
		return nil
	}
	_, err := sessionStore.SaveTranscript(ctx, cwd, messages, defaultSessionBytes)
	if err != nil {
		return err
	}
	return nil
}

func parseHeadlessArgs(mode string, stdout, stderr io.Writer, args []string) (headlessOptions, []string, bool) {
	opts := headlessOptions{}
	var prompts []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			prompts = append(prompts, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			prompts = append(prompts, args[i:]...)
			break
		}
		if strings.HasPrefix(arg, "--") {
			name, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
			switch name {
			case "help", "h":
				printHeadlessUsage(stdout, mode)
				return headlessOptions{}, nil, false
			case "provider":
				if !hasValue {
					if i+1 >= len(args) {
						fmt.Fprintf(stderr, "y %s: --provider requires a value\n", mode)
						return headlessOptions{}, nil, false
					}
					i++
					value = args[i]
				}
				opts.providerName = strings.TrimSpace(value)
			case "model":
				if !hasValue {
					if i+1 >= len(args) {
						fmt.Fprintf(stderr, "y %s: --model requires a value\n", mode)
						return headlessOptions{}, nil, false
					}
					i++
					value = args[i]
				}
				opts.model = strings.TrimSpace(value)
			case "api-key":
				if !hasValue {
					if i+1 >= len(args) {
						fmt.Fprintf(stderr, "y %s: --api-key requires a value\n", mode)
						return headlessOptions{}, nil, false
					}
					i++
					value = args[i]
				}
				opts.apiKey = value
			case "system-prompt":
				if !hasValue {
					if i+1 >= len(args) {
						fmt.Fprintf(stderr, "y %s: --system-prompt requires a value\n", mode)
						return headlessOptions{}, nil, false
					}
					i++
					value = args[i]
				}
				opts.systemPrompt = value
			case "session-dir":
				if !hasValue {
					if i+1 >= len(args) {
						fmt.Fprintf(stderr, "y %s: --session-dir requires a value\n", mode)
						return headlessOptions{}, nil, false
					}
					i++
					value = args[i]
				}
				opts.sessionDir = value
			case "no-session":
				if hasValue {
					fmt.Fprintf(stderr, "y %s: --no-session does not take a value\n", mode)
					return headlessOptions{}, nil, false
				}
				opts.noSession = true
			default:
				fmt.Fprintf(stderr, "y %s: unknown flag --%s\n", mode, name)
				return headlessOptions{}, nil, false
			}
			continue
		}
		switch arg {
		case "-h":
			printHeadlessUsage(stdout, mode)
			return headlessOptions{}, nil, false
		default:
			fmt.Fprintf(stderr, "y %s: unknown flag %q\n", mode, arg)
			return headlessOptions{}, nil, false
		}
	}

	return opts, prompts, true
}

func collectRunPrompt(stdin io.Reader, stdinTTY bool, prompts []string) (string, error) {
	if len(prompts) > 0 {
		return strings.Join(prompts, " "), nil
	}
	if stdinTTY {
		return "", errors.New("prompt is required")
	}
	if stdin == nil {
		return "", nil
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func collectChatPrompts(stdin io.Reader, prompts []string) ([]string, error) {
	out := append([]string(nil), prompts...)
	if stdin == nil {
		return out, nil
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			out = append(out, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func buildHeadlessRegistry(ctx context.Context, compiled *feature.Registry, cwd string) (*tools.Registry, error) {
	return buildRuntimeRegistry(ctx, compiled, cwd, tools.WorkspacePolicy(), nil)
}

func printHeadlessUsage(w io.Writer, mode string) {
	fmt.Fprintln(w, "Usage:")
	switch mode {
	case "chat":
		fmt.Fprintln(w, "  y chat [prompt...]")
		fmt.Fprintln(w, "  y chat --provider <name> [prompt...]")
	default:
		fmt.Fprintln(w, "  y run <prompt>")
		fmt.Fprintln(w, "  y run --provider <name> <prompt>")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Options:")
	fmt.Fprintln(w, "  --provider <name>       Select provider explicitly.")
	fmt.Fprintln(w, "  --model <id>            Select a specific model ID.")
	fmt.Fprintln(w, "  --api-key <key>         Override provider API key.")
	fmt.Fprintln(w, "  --system-prompt <text>   Override the system prompt.")
	fmt.Fprintln(w, "  --session-dir <dir>     Override the session storage directory.")
	fmt.Fprintln(w, "  --no-session            Disable session persistence.")
}
