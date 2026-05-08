# Sessions and Transcripts

`y` stores user state under `~/.pi/agent/` by default, or the path pointed to by `Y_CODING_AGENT_DIR`.

Paths exposed by `internal/storage`:

- `config.toml` for declarative config
- `auth.json` for auth state
- `sessions/` for saved transcripts

Each workspace gets its own session folder:

```text
~/.pi/agent/sessions/--Users-yuri-git-other-pi-migration-y--
```

## Session Format

Session files are JSONL. The first line is a `session` header and the remaining lines are transcript entries.

```json
{"type":"session","version":1,"id":"...","cwd":"/path/to/workspace","timestamp":"2026-05-01T12:00:00Z"}
{"type":"message","id":"...","timestamp":"2026-05-01T12:00:01Z","message":{"role":"user","content":[{"type":"text","text":"hello"}],"timestamp":"2026-05-01T12:00:01Z"}}
{"type":"truncation","dropped_entries":4,"dropped_bytes":12345,"max_bytes":8388608,"first_kept_id":"..."}
```

Notes:

- `y session show` prints the raw JSONL transcript.
- `y session list` shows one line per session with the stored byte size, message count, and modification time.
- When `max_session_bytes` is set in config, the oldest transcript entries are dropped before writing the session file.
- The truncation marker documents how much history was removed.
