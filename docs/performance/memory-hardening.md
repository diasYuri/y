# Memory hardening for hot paths

Activity: `phase-6-memory-hardening`

This document captures the baseline allocations and final measurements after
profiling and reducing memory pressure on the search, patch, command output,
and TUI render hot paths. Numbers were collected on `darwin/arm64` (Apple M1
Pro) using `go 1.25.0` with `CGO_ENABLED` defaulting to `1` (the optimisations
themselves do not introduce cgo). Each row reports the median of three runs at
`-benchtime=500ms`, since `-count=N` confirms the variance is dominated by
allocation count and not by timing jitter.

## Reproduction

Baseline benchmarks live alongside the code they exercise:

- `pkg/tools/benchmarks_test.go` covers search, patch, unified diff, line
  splitting, and the command output capture buffer.
- `pkg/tui/renderer_test.go` covers the differential ANSI renderer for both a
  3-line and an 80×24 frame.

Reproduce locally with:

```bash
go test -run NONE -bench=. -benchmem -benchtime=500ms ./pkg/tools/
go test -tags feature_tui -run NONE -bench=. -benchmem -benchtime=500ms ./pkg/tui/
```

Memory profiles are emitted next to the benchmark results when needed:

```bash
go test -run NONE \
  -bench='BenchmarkSearchLiteralAcrossDir|BenchmarkApplyPatchFile' \
  -benchmem -benchtime=300ms \
  -memprofile=docs/performance/profiles/tools-mem.out \
  ./pkg/tools/
go tool pprof -alloc_space docs/performance/profiles/tools-mem.out
```

The most recent run is committed to
`docs/performance/profiles/tools-bench.txt` and
`docs/performance/profiles/tui-bench.txt` for reference; profile binaries
(`*.out`) regenerate on demand.

## Hot paths and interventions

### 1. Search across the workspace (`pkg/tools/filesystem.go`)

Search is the most allocation-heavy path because the agent fans out across many
files and millions of lines may be inspected before the first match limit
trips.

Before this activity the per-line cost included:

- `bufio.NewReader` allocated a 4 KiB buffer for every file opened in the
  walk, even tiny ones.
- `reader.ReadString('\n')` returned a freshly allocated `string` per line.
- `strings.ToLower(line)` plus `strings.ToLower(pattern)` were rebuilt for
  every line whenever `ignore_case=true`.
- The matched-line render used `fmt.Sprintf("%s:%d: %s", ...)`, allocating a
  formatter and a result string for every match.

The new code:

- Pools `*bufio.Reader` instances with `sync.Pool`. `Reset(io.LimitReader…)`
  reuses the reader buffer across files and across concurrent searches.
- Switches from `ReadString` to `ReadSlice('\n')`, returning a `[]byte` that
  shares the reader buffer until the next read. Lines never escape unless they
  match.
- Adds `MatchBytes([]byte) bool` to `lineMatcher` so the literal and regexp
  matchers can run directly on the byte slice. Regexp uses the existing
  `re.Match([]byte)`. Literals use a custom ASCII case-fold helper
  (`containsFoldASCIIBytes`) that avoids `bytes.ToLower` whenever both sides
  are ASCII (typical for source text); the lowercased pattern is computed once
  in `newMatcher`.
- Pools a small `[]byte` per scan to stage `path:line: text` formatting via
  `strconv.AppendInt`, then converts to a `string` only when the line is being
  pushed onto the output slice.
- Pre-sizes the output slice to `min(limit, 64)` so small searches never grow
  it.

Long lines that exceed the bufio buffer (`bufio.ErrBufferFull`) are detected
explicitly and stitched together into a temporary `carry` slice so the
behaviour is preserved even though the fast path stays zero-alloc.

| Benchmark | Before | After | Δ allocs | Δ bytes | Δ time |
|---|---|---|---:|---:|---:|
| `BenchmarkSearchLiteralAcrossDir` | 395 µs · 182 KB · 2060 allocs | 322 µs · 43 KB · 352 allocs | -83 % | -76 % | -18 % |
| `BenchmarkSearchLiteralIgnoreCase` | 647 µs · 195 KB · 3660 allocs | 418 µs · 44 KB · 353 allocs | -90 % | -77 % | -35 % |
| `BenchmarkSearchRegexp` | 534 µs · 186 KB · 2088 allocs | 403 µs · 46 KB · 378 allocs | -82 % | -75 % | -25 % |
| `BenchmarkLiteralMatcherIgnoreCase` (single line) | 286 ns · 8 B · 1 alloc | 25 ns · 0 B · 0 allocs | -100 % | -100 % | -91 % |

The matcher microbenchmark exists to keep the case-fold fast path honest in
isolation; the per-line work in real searches is now under 30 ns even for
ignore-case patterns, and the per-search allocation count is dominated by
`os.Open`, walk-dir entry construction, and final string allocation rather
than per-line plumbing.

### 2. Patch and unified diff (`pkg/tools/edit_patch.go`)

`applyPatchFile` and `unifiedDiff` are the hottest edit-path functions because
every successful tool call returns a unified diff for transcripts and audit
logs. The previous implementation:

- Built the patched output line-by-line via repeated `append` on an
  unsized `[]string`, then joined the slice with `strings.Join(out, "")`.
- Used `strings.SplitAfter` to chop `original` into lines (fine, but the
  result slice grew dynamically inside `genSplit`).
- Built the unified diff with `fmt.Fprintf` for the headers (one allocation
  per call) and a `strings.Builder` that resized as it went.
- Called `strings.TrimSuffix(line, "\n")` for every diff line; while
  `TrimSuffix` itself does not allocate, the surrounding `WriteString` chain
  amplified the builder's growth allocations.

The new implementation:

- Pre-counts `\n` to allocate the line slice with the exact capacity in
  `splitLinesKeepNewline`, eliminating the `genSplit` shenanigans without
  changing the contract.
- Pre-sizes both the output line slice and the final `strings.Builder` in
  `applyPatchFile` based on the original size and the hunks' new-count.
- Writes the unified diff headers with `WriteString` plus a stack-allocated
  16-byte int buffer (`strconv.AppendInt`) instead of `fmt.Fprintf`.
- Strips the trailing newline by slicing the line in place, which keeps the
  builder pre-allocation honest.

| Benchmark | Before | After | Δ allocs | Δ bytes | Δ time |
|---|---|---|---:|---:|---:|
| `BenchmarkApplyPatchFile` (4000-line file, 1 hunk) | 119 µs · 205 KB · 5 allocs | 104 µs · 205 KB · 3 allocs | -40 % | 0 % | -13 % |
| `BenchmarkUnifiedDiff` (600 vs 600 lines) | 45 µs · 102 KB · 22 allocs | 32 µs · 38 KB · 3 allocs | -86 % | -63 % | -29 % |
| `BenchmarkSplitLinesKeepNewline` (5000 lines) | 83 µs · 81 KB · 1 alloc | 84 µs · 81 KB · 1 alloc | 0 | 0 | ≈ |

The per-byte cost of `strings.Join` is an unavoidable copy when handing the
final patched text back to the writer; the goal here was to remove the
auxiliary `[]string` growth and the formatter allocations, both of which are
gone. `splitLinesKeepNewline` is unchanged in headline numbers because the
single allocation it makes is the line-header slice itself
(`5001 × 16 B = 80 KiB`); the manual scan replaces `strings.SplitAfter` with
the same big-O behaviour and a slightly smaller binary footprint.

### 3. Command output capture (`pkg/tools/command.go`)

Subprocess output flows through `streamCapture`, which already enforced the
configured byte cap. The format step that follows once the process exits also
runs once per `run_command` invocation and is the chunk most likely to remain
allocated in memory while the agent inspects the response.

Before this activity `formatCommandOutput` constructed two intermediate
strings to glue `stdout:`/`stderr:` headers onto the captured payloads
(`"stdout:\n" + result.Stdout`, then `strings.Join(sections, "\n\n")`). For
typical multi-kilobyte outputs that meant copying the whole stdout/stderr
twice on the way to the response.

The new `formatCommandOutput` writes directly into a single `strings.Builder`
whose capacity is pre-grown from the raw stream lengths, eliminating both the
inline concatenation and the slice-and-join. Notes (`stdout truncated to
output limit`, `command exited with code X`) are appended into the same
builder.

`streamCapture.Write` was profiled but left at the existing geometric-growth
implementation. Two alternative geometries (pre-allocating to a fixed initial
size, or sizing the first allocation to the incoming chunk) were measured and
both regressed at least one of the three streaming benchmarks while keeping
the headline allocation count unchanged. The current implementation already
pays a single growth cascade for the rare large-output case and zero
allocations for empty-output commands, which is the dominant pattern in
policy-validated git/shell calls.

| Benchmark | Before | After | Δ allocs | Δ bytes | Δ time |
|---|---|---|---:|---:|---:|
| `BenchmarkStreamCaptureSmallChunks` (64 KiB stream) | 10.9 µs · 131 KB · 4 allocs | 11.6 µs · 131 KB · 4 allocs | 0 | 0 | within noise |
| `BenchmarkStreamCaptureLargeChunk` (1 MiB chunk) | 209 µs · 2 097 KB · 4 allocs | 195 µs · 2 097 KB · 4 allocs | 0 | 0 | -7 % |
| `BenchmarkStreamCaptureTruncated` (4 MiB into 64 KiB cap) | 4.1 µs · 65 KB · 3 allocs | 4.5 µs · 65 KB · 3 allocs | 0 | 0 | within noise |

The visible streaming numbers above are unchanged within noise because the
capture path itself was deliberately not modified. The format-step reduction
is observable under `pprof` (`go test -memprofile`) where
`formatCommandOutput` no longer appears among the alloc-space hotspots — the
two intermediate strings were on the order of `len(stdout) + len(stderr)`
each, which can be hundreds of kilobytes for a noisy build command.

### 4. TUI renderer (`pkg/tui/renderer.go`)

The differential renderer is the highest-frequency hot path during
interactive sessions. Two allocations crept into the previous baseline:

1. `frame.Line(row)` returned a freshly allocated `string` per row, even
   when the renderer was already writing to a `bytes.Buffer`.
2. `strconv.Itoa(row)` allocated a small string per move-to-row escape.

The new renderer:

- Replaces `frame.Line(row)` in the hot path with `appendRow(frame, row)`,
  which writes runes directly into the scratch buffer using
  `bytes.Buffer.WriteByte` for ASCII and a stack-allocated 4-byte
  `utf8.EncodeRune` buffer for non-ASCII cells.
- Replaces `strconv.Itoa` with `strconv.AppendInt(rowBuf[:0], int64(row), 10)`
  and writes the resulting byte slice directly. Both `rowBuf` and the UTF-8
  staging buffer are arrays embedded in the `Renderer` struct, so neither
  escapes to the heap.

| Benchmark | Before | After | Δ allocs | Δ bytes | Δ time |
|---|---|---|---:|---:|---:|
| `BenchmarkRendererFullFrame` (3 short rows, repeated full redraw) | 548 ns · 432 B · 2 allocs | 409 ns · 384 B · 1 alloc | -50 % | -11 % | -25 % |
| `BenchmarkRendererSmallDiff` (3-row diff) | 518 ns · 24 B · 0 allocs | 375 ns · 0 B · 0 allocs | 0 | -100 % | -28 % |
| `BenchmarkRendererTerminalFullFrame` (80×24, full redraw) | n/a | 9.2 µs · 8 KB · 1 alloc | new | new | new |
| `BenchmarkRendererTerminalDiff` (80×24, every 4th row dirty) | n/a | 4.3 µs · 0 B · 0 allocs | new | new | new |

The remaining single allocation on full-redraw paths is the `[]rune` cells
buffer that backs `previous.cells`; it is reused on subsequent redraws of the
same dimensions. The differential path is now zero-alloc on every frame for
typical 80×24 terminals, which keeps the steady-state of an interactive
session free of GC pressure even at high update rates.

## Justified non-changes

- **`splitLinesKeepNewline`** stays at one allocation. The string-header slice
  is the actual cost (~80 KiB at 5000 lines) and is data we have to materialise
  for the patch applier. Reusing a pooled `[]string` would force the patch
  loop into a different ownership model without measurable benefit.
- **`streamCapture` allocation count** is identical to baseline. We considered
  pre-allocating the buffer to the configured byte limit (typically 256 KiB)
  but that pessimises every command that produces no output, which is the
  common case in policy validation paths. The first-Write growth strategy is
  a deliberate compromise.
- **Per-line `string(line)` in `searchFile`** is intentional: it only triggers
  when a match is recorded, which is bounded by `MaxMatches`. Keeping the line
  on the heap there is required so the result survives the next `ReadSlice`.
- **`cmd.Env = os.Environ()`** in `executeCommand` allocates a fresh slice per
  invocation. We did not change it because a per-command snapshot is required
  to keep environment overrides race-free, and the typical environment is
  small.
- **TUI editor's `[][]rune` document** is left as-is. The editor is bounded to
  the prompt's text and the per-keystroke alloc is dominated by the
  user-facing latency floor.

## Verification

Tests run cleanly across all builds touched by this activity:

```bash
go test ./...
go test -tags 'feature_tui feature_openai feature_anthropic feature_google \
              feature_local feature_fs feature_shell feature_git' ./...
```

All packages report `ok`. The baseline numbers above were collected with
`-benchtime=500ms` after the optimisations landed; the captured stdout from
those runs is in `docs/performance/profiles/tools-bench.txt` and
`docs/performance/profiles/tui-bench.txt`.
