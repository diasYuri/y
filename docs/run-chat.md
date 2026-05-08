# `y run` and `y chat`

`y run` and `y chat` are the headless CLI entrypoints for the Go runtime.

## `y run`

```bash
y run "Explain the current diff"
```

Behavior:

- Runs one prompt headlessly.
- Streams assistant text to `stdout` as it arrives.
- Saves the resulting transcript unless `--no-session` is set.
- Uses `--provider`, `--model`, `--api-key`, `--system-prompt`, and `--session-dir` when provided.
- Falls back to `Y_PROVIDER` or provider API key environment variables when possible.

## `y chat`

```bash
y chat
```

Behavior:

- Starts a basic stdin/stdout chat loop.
- Accepts initial prompt words on the command line.
- On a TTY, reads one prompt per line until `exit`, `quit`, or EOF.
- On piped stdin, treats each non-empty input line as a prompt.
- Saves the resulting transcript unless `--no-session` is set.

## Exit Codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `2` | Usage error, such as a missing prompt or unknown flag |
| `3` | Configuration error, such as a missing or unavailable provider |
| `4` | Execution error while running the agent, provider, or session store |
| `130` | Interrupted or canceled by signal/context |
