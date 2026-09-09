package runtime

import "github.com/yuri/y/pkg/agent"

// Public aliases keep the distributed contract discoverable under pkg/runtime
// while the execution implementation remains in pkg/agent.
type Snapshot = agent.Snapshot
type StateStore = agent.StateStore
type EventStore = agent.EventStore
type TranscriptStore = agent.TranscriptStore
type RunControl = agent.RunControl
type ControlState = agent.ControlState
type EventEnvelope = agent.EventEnvelope
type EventKind = agent.EventKind
type Event = agent.Event
type AgentSnapshot = agent.AgentSnapshot
type PendingApproval = agent.PendingApproval
type RunResult = agent.RunResult
type RunRequest = agent.RunRequest
type RunResponse = agent.RunResponse
type AgentRunner = agent.AgentRunner
type RunnerOption = agent.RunnerOption
type ModelCatalog = agent.ModelCatalog
type CredentialResolver = agent.CredentialResolver
type ProviderResolver = agent.ProviderResolver
type ProviderResolverFunc = agent.ProviderResolverFunc
type OperationStore = agent.OperationStore
type OperationReconciler = agent.OperationReconciler
type OperationRecord = agent.OperationRecord
type OperationState = agent.OperationState
type InMemoryStateStore = agent.InMemoryStateStore
type InMemoryEventStore = agent.InMemoryEventStore
type InMemoryRunControl = agent.InMemoryRunControl
type JSONLStore = agent.JSONLStore
type NoopStore = agent.NoopStore
type Lease = agent.Lease
type LeaseStore = agent.LeaseStore
type RunStatus = agent.RunStatus
type State = agent.State
type ControlStatus = agent.ControlStatus

const (
	CurrentSnapshotSchemaVersion  = agent.CurrentSnapshotSchemaVersion
	CurrentEventSchemaVersion     = agent.CurrentEventSchemaVersion
	CurrentOperationSchemaVersion = agent.CurrentOperationSchemaVersion
	RunPending                    = agent.RunPending
	RunRunning                    = agent.RunRunning
	RunCompleted                  = agent.RunCompleted
	RunFailed                     = agent.RunFailed
	RunAborted                    = agent.RunAborted
	RunRecoverable                = agent.RunRecoverable
	RunWaitingApproval            = agent.RunWaitingApproval
	StateIdle                     = agent.StateIdle
	StateSelectingModel           = agent.StateSelectingModel
	StateRequestingModel          = agent.StateRequestingModel
	StateStreaming                = agent.StateStreaming
	StateExecutingTools           = agent.StateExecutingTools
	StateCompleted                = agent.StateCompleted
	StateCanceled                 = agent.StateCanceled
	StateFailed                   = agent.StateFailed
	StateWaitingApproval          = agent.StateWaitingApproval
	OperationPending              = agent.OperationPending
	OperationCompleted            = agent.OperationCompleted
	OperationFailed               = agent.OperationFailed
	ControlNone                   = agent.ControlNone
	ControlRequested              = agent.ControlRequested
	ControlObserved               = agent.ControlObserved
	ControlCompleted              = agent.ControlCompleted
	EventAgentStarted             = agent.EventAgentStarted
	EventAgentCompleted           = agent.EventAgentCompleted
	EventAgentSettled             = agent.EventAgentSettled
	EventMessageStarted           = agent.EventMessageStarted
	EventMessageDelta             = agent.EventMessageDelta
	EventMessageCompleted         = agent.EventMessageCompleted
	EventThinkingDelta            = agent.EventThinkingDelta
	EventTurnStarted              = agent.EventTurnStarted
	EventTurnCompleted            = agent.EventTurnCompleted
	EventToolStarted              = agent.EventToolStarted
	EventToolProgress             = agent.EventToolProgress
	EventToolCompleted            = agent.EventToolCompleted
	EventRetryScheduled           = agent.EventRetryScheduled
	EventCompactionStarted        = agent.EventCompactionStarted
	EventCompactionCompleted      = agent.EventCompactionCompleted
	EventStateCheckpointed        = agent.EventStateCheckpointed
	EventAbortRequested           = agent.EventAbortRequested
	EventAbortCompleted           = agent.EventAbortCompleted
)

var NewSnapshot = agent.NewSnapshot
var NewEventEnvelope = agent.NewEventEnvelope
var NewAgentRunner = agent.NewAgentRunner
var NewInMemoryStateStore = agent.NewInMemoryStateStore
var NewInMemoryEventStore = agent.NewInMemoryEventStore
var NewInMemoryRunControl = agent.NewInMemoryRunControl
var NewJSONLStore = agent.NewJSONLStore
var EventEnvelopeFromEvent = agent.EventEnvelopeFromEvent

var (
	ErrStateNotFound             = agent.ErrStateNotFound
	ErrVersionConflict           = agent.ErrVersionConflict
	ErrEventOutOfOrder           = agent.ErrEventOutOfOrder
	ErrDuplicateEvent            = agent.ErrDuplicateEvent
	ErrRunInProgress             = agent.ErrRunInProgress
	ErrInvalidSnapshot           = agent.ErrInvalidSnapshot
	ErrInvalidEvent              = agent.ErrInvalidEvent
	ErrLeaseHeld                 = agent.ErrLeaseHeld
	ErrLeaseNotFound             = agent.ErrLeaseNotFound
	ErrIdempotencyConflict       = agent.ErrIdempotencyConflict
	ErrOperationInDoubt          = agent.ErrOperationInDoubt
	ErrOperationNotFound         = agent.ErrOperationNotFound
	ErrOperationStoreUnavailable = agent.ErrOperationStoreUnavailable
	ErrApprovalPending           = agent.ErrApprovalPending
	ErrTranscriptNotFound        = agent.ErrTranscriptNotFound
)

var WithRunnerStateStore = agent.WithRunnerStateStore
var WithRunnerEventStore = agent.WithRunnerEventStore
var WithRunnerControl = agent.WithRunnerControl
var WithRunnerModelCatalog = agent.WithRunnerModelCatalog
var WithRunnerCredentialResolver = agent.WithRunnerCredentialResolver
var WithRunnerProviderResolver = agent.WithRunnerProviderResolver
var WithRunnerOperationStore = agent.WithRunnerOperationStore
var WithRunnerTranscriptStore = agent.WithRunnerTranscriptStore
var WithAuthorizationExpiry = agent.WithAuthorizationExpiry
var WithRunnerAgentOptions = agent.WithRunnerAgentOptions
var WithRunnerPollInterval = agent.WithRunnerPollInterval
var WithRunnerOwner = agent.WithRunnerOwner
var WithRunnerTracer = agent.WithRunnerTracer
var WithRunNonce = agent.WithRunNonce
