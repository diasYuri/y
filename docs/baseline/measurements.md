# Baseline measurements harness

Activity: `phase-0-baseline-measurements`

This document defines the reproducible measurement harness used before and during the Go migration. The harness lives at `scripts/measure-baseline.mjs` and uses only Node standard-library APIs plus local platform commands such as `ps` and, for non-interactive TUI startup, `script`.

The script does not build or modify `pi-mono`. Legacy scenarios require an already built `pi-mono/packages/coding-agent/dist/cli.js`; if that file is missing, the script exits with a clear dependency error.

## What the harness records

Each measured run writes one JSONL record with:

| Field | Meaning |
|---|---|
| `scenario` | Stable scenario id. |
| `mode` | `headless`, `tui`, or `large-command`. |
| `metrics.elapsed_ms` | Wall-clock process lifetime. |
| `metrics.ready_ms` | Time to first readiness signal when the scenario has one. |
| `metrics.peak_rss_kb` | Peak sampled RSS for the process tree. |
| `metrics.avg_rss_kb` | Average sampled RSS for the process tree. |
| `metrics.stdout_bytes` / `metrics.stderr_bytes` | Total output volume observed by the harness. |
| `parsed_metrics` | Any child-emitted `METRIC name=value` lines. |
| `notes` | Missing or partial measurement notes, including heap availability. |

RSS is sampled with `ps -A -o pid= -o ppid= -o rss=` and summed over the process tree rooted at the measured child. Heap is not inferable reliably from outside the process; the script records heap values when a child process emits `METRIC heap_*=` lines. The future Go runtime should expose heap through `y doctor --json`, `y profile`, or benchmark output so these fields become populated.

## Scenarios

| Scenario | Runtime | TUI | Purpose |
|---|---|---|---|
| `legacy-help` | `pi-mono` built CLI under Node | no | Cold start for CLI parse/help path. |
| `legacy-rpc` | `pi-mono` built CLI under Node | no | Cold start to usable RPC state via `get_state`. |
| `legacy-tui-startup` | `pi-mono` built CLI under Node through `script(1)` | yes | Non-interactive TUI startup using `PI_STARTUP_BENCHMARK=1`. |
| `control-large-stdout` | local Node child | no | Harness control for large stdout streaming. |
| `control-large-stderr` | local Node child | no | Harness control for large stderr streaming. |
| `candidate-help` | future `y` binary | no | Placeholder for Go binary cold start once Fase 1 creates it. |

The control large-output scenarios do not claim product parity. They prove that the harness can stream and count large stdout/stderr without buffering the full payload in memory.

## Commands

List scenarios:

```bash
node y/scripts/measure-baseline.mjs --list
```

Check dependencies for a scenario:

```bash
node y/scripts/measure-baseline.mjs --check --scenario legacy-rpc
```

Run all currently non-optional scenarios:

```bash
node y/scripts/measure-baseline.mjs --all --runs 5 --warmup 1
```

Run large-output controls with a smaller payload:

```bash
node y/scripts/measure-baseline.mjs --scenario control-large-stdout --scenario control-large-stderr --large-output-bytes 1048576
```

Measure a future Go binary:

```bash
node y/scripts/measure-baseline.mjs --scenario candidate-help --candidate-bin ./dist/y
```

Default output is `docs/baseline/measurements-results.jsonl`. Use `--output <path>` to write a run-specific file, or `--no-write` for local smoke checks.

## Dependency failure behavior

The script fails before starting a selected scenario when any local requirement is missing. Examples:

| Missing item | Expected error shape |
|---|---|
| Built legacy CLI | `legacy built CLI not found: .../pi-mono/packages/coding-agent/dist/cli.js` |
| Pseudo-terminal command | `script pseudo-terminal command not found on PATH: script` |
| RSS sampler | `ps exists at ... but cannot sample process-tree RSS: ...` or `ps for RSS sampling not found on PATH: ps` |
| Future binary | `candidate y binary from --candidate-bin is not configured` |

This is intentional: dependency problems should be fixed or documented before comparing baseline numbers.

## Current limitations

- Heap is only recorded when the measured process emits `METRIC heap_*` lines. Legacy `pi-mono` does not expose stable heap metrics externally.
- `legacy-tui-startup` measures RSS for the process tree below `script(1)` because a pseudo-terminal wrapper is required for non-interactive TUI startup.
- The harness does not invoke LLM providers or require API keys. Provider streaming and token usage benchmarks are planned for provider phases with fake servers.
- The harness does not install dependencies, run `npm install`, or build the TypeScript workspace.
