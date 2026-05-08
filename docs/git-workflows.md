# Git Workflow Documentation

This document describes how the `y` CLI interacts with git worktrees and the safety rules that protect against accidental broad staging.

## Safety Rules

### No Broad Staging by Default

The `git_commit` tool **never** runs `git add -A` or any equivalent broad staging command. Committing without explicit paths is an error. This prevents the agent from accidentally committing unrelated changes that happen to exist in the worktree.

### Explicit Paths Required

Every call to `git_commit` must include the `paths` array. The tool stages only those paths with `git add -- <paths...>` before running `git commit -m <message>`.

Example of a valid call:

```json
{
  "message": "fix typo in readme",
  "paths": ["README.md"]
}
```

Example of an invalid call (rejected by the tool):

```json
{
  "message": "fix typo in readme"
}
```

The tool returns an error:

```
git_commit requires explicit paths; broad staging (git add -A) is not allowed
```

### Dirty Worktree Protection

When the worktree contains modifications to files not listed in `paths`, those files remain untouched. Tests verify that:

1. Only the specified files are staged and committed.
2. Other modified tracked files stay modified.
3. Untracked files remain untracked.

This is intentional. The agent must be explicit about what it wants to commit.

## Tools

| Tool | Capability | Needs Approval | Notes |
|---|---|---|---|
| `git_status` | `git.read` | No | Short status with branch info. |
| `git_diff` | `git.read` | No | Supports `cached` and `paths` filters. |
| `git_commit` | `git.write` | Yes | Requires explicit `paths`; never stages everything. |

## Worktree Scenarios

### Scenario 1: Clean Commit of Specific Files

The agent modifies `src/main.go` and `src/utils.go`, then commits only `src/main.go`:

```json
{
  "message": "add logging to main",
  "paths": ["src/main.go"]
}
```

Result: `src/utils.go` remains modified but unstaged.

### Scenario 2: Commit Rejected Due to Missing Paths

The agent tries to commit without specifying paths:

```json
{
  "message": " assorted fixes"
}
```

Result: Tool returns `invalid_arguments` error. The agent must list the files.

### Scenario 3: Unrelated Changes in Worktree

The worktree has modifications in `a.txt`, `b.txt`, and a new untracked `scratch.tmp`. The agent commits only `a.txt`:

```json
{
  "message": "update a",
  "paths": ["a.txt"]
}
```

Result: `b.txt` stays modified, `scratch.tmp` stays untracked.

## Policy Integration

All git write operations (`git_commit`) pass through the policy gate. The tool is marked `Sensitive`, so it requires an explicit approval resolution before execution. Read operations (`git_status`, `git_diff`) do not require approval.

## Error Codes

| Code | When | Retryable |
|---|---|---|
| `invalid_arguments` | Missing message or paths | No |
| `policy_denied` | Policy gate rejected the operation | No |
| `approval_required` | Sensitive tool invoked without approval | Yes (after user approves) |
