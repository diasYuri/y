# Benchmark plan

Activity: `phase-0-baseline-measurements`

This plan defines how baseline and migration benchmarks should be captured so footprint regressions are visible from the first Go skeleton onward. The plan covers legacy `pi-mono`, future Go `y` builds, large command output, and TUI/non-TUI scenarios.

## Goals

- Measure cold start, RSS, heap, goroutines, output volume and command latency with repeatable commands.
- Compare legacy Node/Bun behavior with Go `y-minimal`, `y-standard`, and `y-full` builds.
- Keep measurements non-interactive, offline by default, and independent from real provider API keys.
- Catch memory regressions from TUI rendering, subprocess streaming, large file/command handling, and optional WASM extensions.

## Build matrix

| Build | Expected contents | Initial command target |
|---|---|---|
| legacy Node | Built `pi-mono/packages/coding-agent/dist/cli.js` | `legacy-help`, `legacy-rpc`, `legacy-tui-startup` |
| `y-minimal` | Core CLI, config, diagnostics, one HTTP provider stub/fake, basic fs tools | `candidate-help`, later `y run --offline` |
| `y-standard` | TUI, git, shell, filesystem, main providers | TUI startup, headless run, large command tool |
| `y-full` | Standard plus optional WASM extension host | WASM cold/lazy/warm extension scenarios |

All Go builds should use `CGO_ENABLED=0` unless a feature explicitly documents otherwise.

## Metrics

| Metric | Source | Required for |
|---|---|---|
| Cold start `elapsed_ms` | Harness wall clock | every scenario |
| Ready time `ready_ms` | Scenario-specific readiness event | RPC, TUI, future `y doctor/profile` |
| Peak and average RSS | Process-tree `ps` sampling | every scenario |
| Go heap bytes | `runtime.ReadMemStats`, `runtime/metrics`, or `y doctor --json` | Go scenarios |
| Node heap bytes | child-emitted `METRIC heap_*` only | legacy when available |
| Goroutines | Go runtime metric | Go scenarios |
| Allocs/op and B/op | `go test -bench=. -benchmem` | package benchmarks |
| Stdout/stderr bytes | Harness counters | large command scenarios |
| Exit code/signal | Harness process result | every scenario |
| Binary size | `stat` on built artifact | Go release builds |

## Scenario matrix

| Area | Scenario | TUI | Output size | Notes |
|---|---|---:|---:|---|
| CLI parse | `legacy-help`, `candidate-help` | no | small | Fast cold-start smoke. |
| Headless runtime | `legacy-rpc`, future `y run --offline --json` | no | small | Measures usable non-interactive startup. |
| TUI runtime | `legacy-tui-startup`, future `y chat --startup-benchmark` | yes | small | Must run through a pseudo-terminal without user input. |
| Large stdout | `control-large-stdout`, future shell tool scenario | no | 1 MiB, 16 MiB, 64 MiB | Verifies streaming and truncation behavior. |
| Large stderr | `control-large-stderr`, future shell tool scenario | no | 1 MiB, 16 MiB, 64 MiB | Same as stdout, separate stream. |
| Repository size | future `y run/search` fixtures | no | varies | Small, medium and large repo fixtures. |
| WASM disabled | future `y features` without `feature_wasm_ext` | no | small | Confirms no extension host overhead. |
| WASM lazy | future extension list with no module load | no | small | Measures discovery overhead only. |
| WASM loaded | future sample tool call | optional | small and 1 MiB payload | Measures host overhead, memory pages and output limits. |

## Large command plan

The Go shell tool benchmarks must use controlled commands that do not depend on external network or user input:

```bash
node -e 'process.stdout.write(Buffer.alloc(16 * 1024 * 1024, 65))'
node -e 'process.stderr.write(Buffer.alloc(16 * 1024 * 1024, 69))'
```

When the Go runtime exists, equivalent scenarios should execute through the native `run_command` tool with:

- Mandatory timeout.
- Separate stdout/stderr byte counters.
- Configured truncation limit.
- Streaming result delivery instead of accumulating the whole command output.
- A recorded path or note if full output is spilled to disk.

## Heap and memory plan

Legacy Node heap is not externally reliable, so it is informational only unless the process emits explicit `METRIC heap_*` lines. Go must expose stable heap metrics early:

```text
METRIC heap_alloc_bytes=<n>
METRIC heap_sys_bytes=<n>
METRIC goroutines=<n>
```

The preferred Go implementation path is:

- `y doctor --json` for static runtime/build facts.
- `y profile --startup --json` for startup metrics.
- Package benchmarks using `go test -bench=. -benchmem ./...`.
- Targeted memory profiles for hot packages:

```bash
go test -run=^$ -bench=BenchmarkAgentLoop -benchmem -memprofile=mem.out ./pkg/agent
go tool pprof -top mem.out
```

## Methodology

1. Run dependency checks before measurements.
2. Use at least one warmup and five measured runs for local comparison.
3. Record machine, OS, architecture, commit/build id and build tags in the run notes once buildinfo exists.
4. Keep provider/network benchmarks behind fake HTTP servers until provider phases.
5. Use isolated config/session directories for cold-start comparisons.
6. Store raw JSONL results as artifacts; summarize medians in release or phase reports.

Example baseline command:

```bash
node y/scripts/measure-baseline.mjs --all --warmup 1 --runs 5 --output y/docs/baseline/measurements-results.jsonl
```

## Acceptance gates by phase

| Phase | Gate |
|---|---|
| Fase 0 | Harness and plan exist; missing dependencies fail clearly. |
| Fase 1 | `y-minimal` builds with `CGO_ENABLED=0`; `candidate-help` records cold start and RSS. |
| Fase 3 | Shell tool benchmarks handle 16 MiB stdout/stderr without unbounded memory growth. |
| Fase 5 | TUI startup benchmark runs non-interactively and records RSS plus heap. |
| Fase 8 | WASM disabled/lazy/loaded scenarios show extension overhead separately. |
| Fase 9 | Release report includes median and peak values for all target builds. |

## Reporting format

Phase reports should include:

- Command used.
- Number of warmup and measured runs.
- Median `ready_ms` or `elapsed_ms`.
- Peak RSS range.
- Heap and goroutine values for Go.
- Output byte counts for large command scenarios.
- Any missing metrics and the reason they are missing.

No benchmark should silently skip missing local prerequisites. If a dependency is absent, record the exact script error in the phase artifact and rerun when the dependency is available.
