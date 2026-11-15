# Effects and durable state

Y distinguishes component cleanup from data rollback. A component can own
resources and registrations whose lifetime ends with the component, but that
ownership does not extend to durable user data or actions already observed by
other systems.

## Four effect classes

| Class | Examples | Component shutdown |
| --- | --- | --- |
| Managed acquisition | Process, connection, worker, subscription, tool or hook registration | Release through a registered cleanup; continue after failures and report all cleanup errors. |
| Durable state | Transcript, session, checkpoint, memory, run event or application record | Preserve. The store and application own retention, migration and explicit deletion. |
| External emission | File write, message send, payment, API call or other action observed outside Y | Do not claim rollback. Use the operation's normal idempotency, policy, approval and audit path. |
| Compensation | Domain operation that corrects an earlier external emission | Run as a separate authorized operation with its own stable identity and idempotency key, linked to the original operation where supported. |

`component.Scope.Track` and `tools.Registry.AddCloser` represent managed
acquisitions. Their cleanup functions release resources and registrations; they
must not delete durable data or pretend to retract an external emission. Scope
cleanup runs in LIFO order, attempts every registered cleanup and returns the
combined errors. Registry shutdown follows the same error-reporting rule.

## Durable data ownership

Durable stores own records independently from the component that currently
accesses them. Removing or replacing a store adapter closes its live resources
but leaves the records in place. Deletion is an explicit store or application
operation, such as `memory.Store.Forget`, with its own authorization and audit
requirements. Retention and schema migration are contracts of the application
and store, not side effects of component shutdown.

Y's session, snapshot, event, transcript and operation stores are durable
boundaries. In particular, an in-doubt side-effecting operation is reconciled
through its idempotency identity; removing a component does not replay or erase
it. Memory read fail-open behavior is also distinct from persistence readiness:
it can allow recall to degrade, while writes continue returning storage errors.

## Persistence availability

Each persistence component chooses `required` or `degraded` in its own
configuration. `required` fails component construction/activation when the
backend readiness check fails. `degraded` permits startup and reports its
current persistence status through the component API. The generic component
runtime does not make storage policy decisions. Later storage failures remain
observable as degraded status; mutating operations return their errors.

The memory extension exposes `persistence_failure` with `required` as its
default. It checks readiness before starting its worker. Stores can implement
`memory.ReadinessChecker`; existing `memory.Store` implementations remain
compatible and use their read-only `Version` operation as the readiness check.
The memory extension's `fail_open` setting controls context-source read
behavior only; it does not hide failed writes or imply that a backend passed
readiness.

## Emissions and compensation

Tool calls and other emissions continue through Y's existing policy boundary.
`ToolRequest` carries operation identity and idempotency metadata; the
distributed operation store records pending/completed outcomes; policy and
approval decisions remain attached to the request; and audit records describe
the operation. Those mechanisms provide authorization and replay safety, not
generic undo.

An application that supports compensation models it as another operation in
the same domain. It gives that operation a stable idempotency identity, links
it to the original operation when possible, and subjects it to policy,
approval and audit. Component cleanup does not run it implicitly.

The system boundary is therefore explicit: Y can promise release for tracked
acquisitions; it preserves durable records; and it does not promise to retract
emissions that crossed the boundary.
