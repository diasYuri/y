# Reusable local SDK services

The v1.3 SDK additions are application-neutral and keep `pkg` independent from
the CLI. Consumers compose through `runtime.Harness` and their own factories.

## OAuth

`pkg/providers/oauth.Client` exposes device authorization and PKCE/loopback
authorization-code flows plus refresh. Supply `Config` endpoints, a provider-approved
client ID, an HTTP client and a `Notify(context.Context, Notice)` callback.
`Now` and context-aware `Sleep` are injectable for fixtures. Notifications contain
the URL and user code, never the private device code. No terminal, browser or
credential-store interaction occurs in this package. The caller owns all storage
and UI. Tokens use `providers/auth.Credential`, whose secret fields are excluded
from JSON snapshots. An absent refresh token in a refresh response preserves the
previous token. Errors omit token response bodies; endpoint redirects cannot
cross origins. HTTPS is required except on loopback HTTP fixtures/callbacks.

## SQLite and filesystem memory

`pkg/storage/sqlite.Open(ctx, path)` opens a pure-Go local database implementing
`agent.StateStore`, `EventStore`, `OperationStore` and `TranscriptStore`. Snapshot
and transcript commit together under optimistic versions. Events have per-run
sequence/dedup indexes. Operation claims are transactional across connections;
pending effects never automatically replay. `Operations` inspects them and explicit
`CompleteOperation` records reconciliation. `ReleaseOperation` only releases a
claim when authorization prevented the effect.

`Store.Sessions()` implements the separate session/branch contracts, whose
`SaveTranscript` signature differs from the agent transcript contract. It supports
listing, unambiguous resolution, forks/clones, names/labels and message retrieval.
`PutMetadata`/`GetMetadata` persist caller-owned JSON execution metadata. Schema
version 1 is migrated transactionally; future schema versions fail closed.
The host supplies its path; no Y environment defaults are read. `Close` leaves
durable data intact. WAL/FULL synchronous writes are used; no generic rollback of
external effects or arbitrary exactly-once semantics are claimed.

`pkg/memory/filesystem.NewFilesystemStore(root)` exposes the existing Markdown/index
adapter with injected root, readiness/version checks and atomic index replacement.
Native file locking protects independent instances on Unix and Windows. It owns
no background worker and never deletes memories during cleanup. The old internal
FilesystemStore name remains a compatibility alias, not a second implementation.

## Task snapshots and active runner controls

`task-manager.Manager.Snapshot/Restore` preserve IDs, hierarchy, ordering, progress,
allocator and completion-gate state. Restore validates version, uniqueness, parents,
cycles and completed-ancestor consistency before atomically replacing the tree.

`AgentRunner.Subscribe` streams canonical envelopes after persistence; callbacks
are synchronous, ordered per run and must return promptly. They may use active
`Steer`, `FollowUp`, `CancelRun` and subscription controls, but must not recursively
call Run or wait for that run to complete. Different runs may call a sink concurrently.
Steering/follow-ups retain the underlying Agent's semantics and are checkpointed.
Controls reject runs that are no longer active. `Close(ctx)` rejects new runs,
cancels active contexts and waits for final checkpoints; resource ownership stays
with the caller's component scopes.

Captured `RuntimeView` registries inherit the runner's durable operation wrapper.
An integration test uses a real immutable registry snapshot and observes operation
Begin/Complete. A pending unknown result stops execution with `ErrOperationInDoubt`
instead of being offered to the model as an ordinary tool error it might retry.
