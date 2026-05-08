package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/yuri/y/internal/buildinfo"
	"github.com/yuri/y/pkg/agent"
	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/mom"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/tools"
)

func main() {
	os.Exit(run(os.Stdout, os.Stderr, os.Args[1:], os.Getenv))
}

func run(stdout, stderr io.Writer, argv []string, getenv func(string) string) int {
	args, err := mom.ParseCLIArgs(argv)
	if err != nil {
		fmt.Fprintln(stderr, "y-mom:", err)
		fmt.Fprint(stderr, mom.HelpText)
		return 2
	}
	if args.ShowHelp {
		fmt.Fprint(stdout, mom.HelpText)
		return 0
	}
	if args.ShowVersion {
		fmt.Fprintln(stdout, buildinfo.Current().Version)
		return 0
	}
	if args.WorkingDir == "" && args.DownloadChannel == "" {
		fmt.Fprint(stderr, mom.HelpText)
		return 2
	}

	envCfg := mom.LoadEnvConfig(getenv)
	if args.DownloadChannel != "" {
		fmt.Fprintln(stderr, "y-mom: --download is not yet supported in the Go binary; use the legacy CLI for now.")
		return 1
	}
	if err := envCfg.Validate(); err != nil {
		fmt.Fprintln(stderr, "y-mom:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := mom.ValidateSandbox(ctx, args.Sandbox); err != nil {
		fmt.Fprintln(stderr, "y-mom:", err)
		return 1
	}

	store, err := mom.NewChannelStore(mom.StoreConfig{WorkingDir: args.WorkingDir, BotToken: envCfg.SlackBotToken})
	if err != nil {
		fmt.Fprintln(stderr, "y-mom:", err)
		return 1
	}
	sandbox, err := mom.NewSandbox(args.Sandbox)
	if err != nil {
		fmt.Fprintln(stderr, "y-mom:", err)
		return 1
	}

	connector := mom.NewFakeConnector(envCfg.SlackBotToken, nil, nil)
	eventsDir, err := store.EventsDir()
	if err != nil {
		fmt.Fprintln(stderr, "y-mom:", err)
		return 1
	}

	bus := &lazyBus{}
	watcher := mom.NewEventsWatcher(eventsDir, bus, mom.SystemClock())
	server, err := mom.NewServer(mom.HandlerConfig{
		WorkingDir:   args.WorkingDir,
		SandboxCfg:   args.Sandbox,
		Connector:    connector,
		Store:        store,
		Sandbox:      sandbox,
		Logger:       stderr,
		Clock:        mom.SystemClock(),
		BuildAgent:   newDefaultAgentBuilder(envCfg, args.WorkingDir),
		Watcher:      watcher,
		QueueLimit:   5,
		IdleResponse: "_Already working. Say `@mom stop` to cancel._",
		StopResponse: "_Nothing running_",
	})
	if err != nil {
		fmt.Fprintln(stderr, "y-mom:", err)
		return 1
	}
	bus.target = server

	fmt.Fprintf(stdout, "y-mom %s\n", buildinfo.Current().Version)
	fmt.Fprintf(stdout, "  working dir: %s\n", args.WorkingDir)
	fmt.Fprintf(stdout, "  sandbox: %s\n", args.Sandbox.String())
	fmt.Fprintln(stdout, "  Slack connector: stub (no real socket-mode binding in this build)")
	fmt.Fprintln(stdout, "  events dir:", eventsDir)
	fmt.Fprintln(stdout, "  ready; waiting for SIGINT/SIGTERM")

	if err := server.Start(ctx); err != nil {
		fmt.Fprintln(stderr, "y-mom:", err)
		return 1
	}
	defer func() { _ = server.Stop() }()

	<-ctx.Done()
	if err := server.Stop(); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "y-mom:", err)
	}
	return 0
}

func newDefaultAgentBuilder(env mom.EnvConfig, workingDir string) func(string) (*agent.Agent, error) {
	provider := providers.NewFakeProvider(providers.WithFakeID(env.DefaultProviderID))
	registry := tools.NewRegistry()
	return func(channelID string) (*agent.Agent, error) {
		ag := agent.New(provider, registry,
			agent.WithSystemPrompt(buildSystemPrompt(env, workingDir, channelID)),
			agent.WithWorkspaceRoot(filepath.Join(workingDir, channelID)),
			agent.WithModel(ai.Model{ID: "y-mom-stub", Provider: ai.ProviderID(env.DefaultProviderID)}),
		)
		return ag, nil
	}
}

func buildSystemPrompt(env mom.EnvConfig, workingDir, channelID string) string {
	return fmt.Sprintf(
		"You are y-mom, a Slack bot.\nWorking directory: %s\nChannel: %s\nProvider: %s\n",
		workingDir, channelID, env.DefaultProviderID,
	)
}

// lazyBus delays binding the events watcher to the server until both have
// been constructed.
type lazyBus struct {
	target mom.EventsBus
}

func (b *lazyBus) DispatchSyntheticEvent(event mom.SlackEvent) bool {
	if b == nil || b.target == nil {
		return false
	}
	return b.target.DispatchSyntheticEvent(event)
}
