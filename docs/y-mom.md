# y-mom

`y-mom` is the optional Slack-driven product. It compiles to its own binary in
`cmd/y-mom` and reuses `pkg/agent` plus the providers and tools registered in
the rest of the y monorepo.

## Build

```bash
cd y
CGO_ENABLED=0 go build ./cmd/y-mom
```

The binary embeds the `feature_mom` capability description from
`internal/feature/compiled_mom.go`. Production builds should pass the
`feature_mom` build tag together with the providers it needs:

```bash
go build -tags "feature_mom feature_anthropic" ./cmd/y-mom
```

## CLI

```bash
y-mom [options] <working-directory>
y-mom --download <channel-id>
```

Options:

| Flag | Description |
|------|-------------|
| `--sandbox=host` | Run tools directly on the host (not recommended). |
| `--sandbox=docker:<container>` | Use a long-running Docker container. |
| `--download <id>` | Backfill a Slack channel into the working directory. |
| `-h`, `--help` | Show usage. |
| `-v`, `--version` | Show build version. |

Environment variables:

| Variable | Purpose |
|----------|---------|
| `MOM_SLACK_APP_TOKEN` | Slack app-level token (`xapp-...`). |
| `MOM_SLACK_BOT_TOKEN` | Slack bot user OAuth token (`xoxb-...`). |
| `ANTHROPIC_API_KEY` | Preferred provider credential. |
| `OPENAI_API_KEY` | Alternative provider credential. |
| `GOOGLE_API_KEY` | Alternative provider credential. |
| `Y_MOM_PROVIDER` | Override the auto-selected provider id. |

`pkg/mom.LoadEnvConfig` reads these values; `EnvConfig.Validate` is invoked
before the server boots. If neither tokens nor provider keys are present the
binary refuses to start with a clear error message.

## Working directory layout

```
<workdir>/
├── MEMORY.md             # global memory shared across channels
├── settings.json         # workspace settings (compaction, limits, ...)
├── events/               # JSON event files watched by EventsWatcher
├── skills/               # global custom CLI skills (markdown SKILL.md)
└── <channel-id>/
    ├── MEMORY.md         # channel-specific memory
    ├── log.jsonl         # appended message history (source of truth)
    ├── attachments/      # downloaded user files
    └── skills/           # channel-specific skills
```

`pkg/mom.ChannelStore` owns the on-disk format and is concurrency-safe. It
deduplicates messages within a configurable window (default 60s) and downloads
attachments asynchronously through an injectable `AttachmentDownloader` (the
default is `HTTPDownloader`, tests use `FakeDownloader`).

## Events

`pkg/mom.EventsWatcher` polls `<workdir>/events` and dispatches synthetic
Slack events to the running server. Three event types are supported:

```json
{"type":"immediate","channelId":"C1","text":"go"}
{"type":"one-shot","channelId":"C1","text":"alarm","at":"2026-05-01T09:00:00Z"}
{"type":"periodic","channelId":"C1","text":"tick","schedule":"0 9 * * 1-5","timezone":"UTC"}
```

The watcher uses a small built-in cron parser (`pkg/mom.ParseCron`) so the
binary stays free of third-party dependencies. Stale immediate events whose
modtime predates the watcher startup time are deleted without firing.

## Sandbox

`pkg/mom.Sandbox` abstracts shell execution.

* `HostSandbox` runs commands directly through `/bin/sh -c` (or `cmd /C` on
  Windows) with bounded stdout/stderr buffers.
* `DockerSandbox` wraps the same logic with `docker exec <container> sh -c`.
* `FakeSandbox` is a deterministic in-memory implementation used by tests.

`ValidateSandbox` performs a best-effort liveness check before the server
starts: it confirms `docker --version` works and `docker inspect -f
{{.State.Running}} <container>` reports `true` for Docker mode.

## Slack connector

The Slack interaction surface is captured by `pkg/mom.Connector`:

```go
type Connector interface {
    Start(ctx, dispatcher EventDispatcher) error
    Stop() error
    BotUserID() string
    PostMessage(ctx, channel, text) (ts string, error)
    UpdateMessage(ctx, channel, ts, text) error
    // ... and the rest of the Slack API surface used by y-mom
}
```

`pkg/mom.FakeConnector` is the only implementation shipped in this build; it
records every API call, returns synthetic timestamps, and exposes
`PushEvent`/`PushSynthetic` so tests (and the placeholder `cmd/y-mom`
runtime) can drive arbitrary inbound traffic without contacting Slack. A real
Socket Mode implementation can be added later as `pkg/mom/connector_slack.go`
behind a build tag (see *Future work*).

## Server lifecycle

`pkg/mom.Server` is the orchestrator. It:

1. Calls `Connector.Start` and registers itself as the dispatcher.
2. Maintains per-channel state (running flag, current `SlackContext`,
   pending stop request).
3. Maintains a small per-channel FIFO that runs at most one agent run at a
   time. The default queue limit is `5`; further events are dropped with a
   logged warning.
4. Recognises the literal "stop" command (case-insensitive) and aborts the
   active runner via `AgentRunner.Abort`.
5. Forwards user-provided text to `pkg/agent.Agent` via `AgentRunner.Run`.
   The runner publishes the final assistant text through `SlackContext` so
   posts and updates flow back through the connector.

The `BuildAgent func(channelID string) (*agent.Agent, error)` injection point
lets each channel use a different system prompt, transcript, or tool set. The
binary ships a stub builder that wires `providers.NewFakeProvider` so the
process can boot without provider credentials during local experimentation.

## Tests

All tests use only fakes. Run them with:

```bash
go test ./pkg/mom/...
```

Notable coverage:

* `cron_test.go` — wildcard / step / list / range / weekday-range cases.
* `store_test.go` — log dedupe window, attachment download via
  `FakeDownloader`, last timestamp accessor.
* `events_test.go` — immediate / one-shot / periodic dispatch using a fake
  clock and `recordingBus`.
* `sandbox_test.go` — host sandbox executes real `echo`, output truncation
  honours `MaxOutput`, fake sandbox queues responses.
* `runner_test.go` — `AgentRunner` posts initial reply, updates with the
  final assistant text, and propagates `Abort` through the run.
* `server_test.go` — full pipeline using `FakeConnector` to push user and
  synthetic events.

## Command parity

The Go server reproduces the core message routing from the TypeScript
implementation. The following interaction patterns are supported:

| Command | Status | Notes |
|---------|--------|-------|
| Regular messages | Supported | Forwarded to `pkg/agent.Agent` via `AgentRunner.Run`. |
| `stop` | Supported | Recognised case-insensitively via `LooksLikeStop`. Aborts the active runner. |
| `status` | Not ported | Would report channel state (running/idle, queue depth). |
| `help` | Not ported | Would list available commands and runtime info. |
| `history` | Not ported | Would return recent `log.jsonl` entries. |
| `memory` | Not ported | Would read/write channel `MEMORY.md`. |
| `settings` | Not ported | Would read workspace `settings.json`. |

Legacy slash commands from `pi-mono/packages/mom` that are not listed above
were considered niche or workspace-specific. They are tracked here for
completeness; re-add them as concrete Slack workspaces require them.

## Future work

* Real Slack Socket Mode + Web API connector (`feature_mom_slack_live` build
  tag).
* `--download <channel>` mode for backfilling channels into the workspace.
* Skill loader and memory injection in the system prompt builder.
* Integration with `pkg/coding` so y-mom delegates to the same tool set as
  `y` and `y chat`.
