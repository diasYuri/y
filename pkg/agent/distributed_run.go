package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/providers"
	"github.com/diasYuri/y/pkg/providers/auth"
)

func (r *AgentRunner) Run(ctx context.Context, request RunRequest) (RunResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.RunID == "" {
		request.RunID = newID()
	}
	lock := r.lockFor(request.RunID)
	lock.Lock()
	defer lock.Unlock()

	state, expectedVersion, err := r.loadInitial(ctx, request)
	if err != nil {
		return RunResponse{}, err
	}
	if request.TenantID == "" {
		request.TenantID = request.Identity.TenantID
	}
	if request.WorkspaceID == "" {
		request.WorkspaceID = request.Identity.WorkspaceID
	}
	if request.SessionID == "" {
		request.SessionID = request.Identity.SessionID
	}
	if request.Identity.TenantID != "" && request.Identity.TenantID != request.TenantID {
		return RunResponse{}, fmt.Errorf("%w: identity tenant mismatch", ErrInvalidSnapshot)
	}
	if request.Identity.WorkspaceID != "" && request.Identity.WorkspaceID != request.WorkspaceID {
		return RunResponse{}, fmt.Errorf("%w: identity workspace mismatch", ErrInvalidSnapshot)
	}
	if request.Identity.RunID != "" && request.Identity.RunID != request.RunID {
		return RunResponse{}, fmt.Errorf("%w: identity run_id mismatch", ErrInvalidSnapshot)
	}
	if err := validateSnapshotScope(state, request); err != nil {
		return RunResponse{}, err
	}
	if state.Result != nil && state.IsTerminal() {
		return RunResponse{RunID: request.RunID, Snapshot: state, Result: *state.Result}, nil
	}
	provider := r.provider
	providerID := request.ProviderID
	if request.Model.Provider != "" {
		providerID = request.Model.Provider
	}
	if providerID == "" && state.Agent.Model.Provider != "" {
		providerID = state.Agent.Model.Provider
	}
	if providerID == "" && provider != nil {
		providerID = ai.ProviderID(provider.ID())
	}
	if provider == nil || (providerID != "" && ai.ProviderID(provider.ID()) != providerID) {
		if r.providerResolver == nil {
			return RunResponse{}, fmt.Errorf("agent: provider %q is not configured", providerID)
		}
		provider, err = r.providerResolver.ResolveProvider(ctx, providerID)
		if err != nil {
			return RunResponse{}, err
		}
		if provider == nil {
			return RunResponse{}, fmt.Errorf("agent: provider %q resolver returned nil", providerID)
		}
	}
	model := request.Model
	if model.ID == "" && request.ModelID != "" {
		if r.modelCatalog == nil {
			return RunResponse{}, errors.New("agent: model catalog is not configured")
		}
		model, err = r.modelCatalog.Lookup(ctx, providerID, request.ModelID)
		if err != nil {
			return RunResponse{}, err
		}
	}
	if model.Provider != "" && model.Provider != ai.ProviderID(provider.ID()) {
		return RunResponse{}, fmt.Errorf("agent: model provider %q does not match runner provider %q", model.Provider, provider.ID())
	}
	if model.Provider == "" {
		model.Provider = ai.ProviderID(provider.ID())
	}
	apiKey := request.APIKey
	if apiKey == "" && r.credentials != nil {
		providerID := ""
		if model.Provider != "" {
			providerID = string(model.Provider)
		} else {
			providerID = provider.ID()
		}
		apiKey, err = r.credentials.ResolveAPIKey(ctx, auth.ResolveRequest{
			ProviderID: providerID,
			TenantID:   request.TenantID,
		})
		if err != nil {
			return RunResponse{}, err
		}
	}
	restoreAgent := hasDurableAgentState(state.Agent)
	recoverAgent := restoreAgent && state.Agent.State == StateFailed && state.Agent.RecoverableErrMsg != ""

	identity := clonePolicyIdentity(state.Agent.PolicyIdentity)
	if request.Identity.CallerID != "" {
		identity.CallerID = request.Identity.CallerID
	}
	if request.TenantID != "" {
		identity.TenantID = request.TenantID
	}
	if request.WorkspaceID != "" {
		identity.WorkspaceID = request.WorkspaceID
	}
	if request.SessionID != "" {
		identity.SessionID = request.SessionID
	}
	if request.Identity.RunID != "" {
		identity.RunID = request.Identity.RunID
	}
	if request.Identity.Capabilities != nil {
		identity.Capabilities = append([]string(nil), request.Identity.Capabilities...)
	}
	if identity.RunID == "" {
		identity.RunID = request.RunID
	}
	policyVersion := request.PolicyVersion
	if policyVersion == "" {
		policyVersion = state.Agent.PolicyVersion
	}
	effectiveIdempotencyKey := request.IdempotencyKey
	if effectiveIdempotencyKey == "" {
		effectiveIdempotencyKey = state.IdempotencyKey
		if effectiveIdempotencyKey == "" {
			effectiveIdempotencyKey = state.Agent.IdempotencyKey
		}
	}
	runNonce := state.Agent.RunNonce
	if runNonce == "" {
		runNonce = newID()
	}
	if state.Status == RunRunning && state.LeaseExpiresAt.After(time.Now().UTC()) && state.Owner != "" && state.Owner != r.owner {
		return RunResponse{}, ErrRunInProgress
	}
	var lease Lease
	if leaseStore, ok := r.stateStore.(LeaseStore); ok {
		lease, err = leaseStore.AcquireLease(ctx, request.RunID, r.owner, 2*time.Minute)
		if err != nil {
			return RunResponse{}, err
		}
		defer func() { _ = leaseStore.ReleaseLease(context.Background(), lease) }()
	}
	state.RunID = request.RunID
	state.SchemaVersion = CurrentSnapshotSchemaVersion
	state.Status = RunRunning
	state.Owner = r.owner
	state.LeaseExpiresAt = time.Now().UTC().Add(2 * time.Minute)
	state.IdempotencyKey = effectiveIdempotencyKey
	state.Agent.RunID = request.RunID
	state.Agent.RunNonce = runNonce
	state.Agent.PolicyIdentity = clonePolicyIdentity(identity)
	state.Agent.PolicyVersion = policyVersion
	state.Agent.IdempotencyKey = effectiveIdempotencyKey
	if !request.AuthorizationExpiresAt.IsZero() {
		state.Agent.AuthorizationExpiresAt = request.AuthorizationExpiresAt
	}
	if request.SessionID != "" {
		state.SessionID = request.SessionID
		state.Agent.SessionID = request.SessionID
		state.Agent.ContextRequest.SessionID = request.SessionID
	}
	if request.TenantID != "" {
		if state.TenantID != "" && state.TenantID != request.TenantID {
			return RunResponse{}, fmt.Errorf("%w: tenant mismatch", ErrInvalidSnapshot)
		}
		state.TenantID = request.TenantID
		state.Agent.ContextRequest.TenantID = request.TenantID
	}
	if request.WorkspaceID != "" {
		if state.WorkspaceID != "" && state.WorkspaceID != request.WorkspaceID {
			return RunResponse{}, fmt.Errorf("%w: workspace mismatch", ErrInvalidSnapshot)
		}
		state.WorkspaceID = request.WorkspaceID
		state.Agent.ContextRequest.WorkspaceID = request.WorkspaceID
	}
	if request.ProjectID != "" {
		if state.ProjectID != "" && state.ProjectID != request.ProjectID {
			return RunResponse{}, fmt.Errorf("%w: project mismatch", ErrInvalidSnapshot)
		}
		state.ProjectID = request.ProjectID
		state.Agent.ContextRequest.ProjectID = request.ProjectID
	}
	if request.RequestID != "" {
		state.Agent.ContextRequest.RequestID = request.RequestID
	}
	if model.ID != "" {
		state.Agent.Model = sanitizeModel(model)
	}
	if err := r.saveState(ctx, request.RunID, expectedVersion, state); err != nil {
		if errors.Is(err, ErrStateNotFound) {
			// NoopStore never saves state and is allowed to run without a durable checkpoint.
		} else {
			return RunResponse{}, err
		}
	}
	if state.Version == expectedVersion {
		state.Version = expectedVersion + 1
	}
	expectedVersion = state.Version

	requestOptions := []Option{WithRunID(request.RunID), WithRunNonce(runNonce)}
	if request.SessionID != "" {
		requestOptions = append(requestOptions, WithSessionID(request.SessionID))
	}
	if request.SystemPrompt != "" {
		requestOptions = append(requestOptions, WithSystemPrompt(request.SystemPrompt))
	}
	if request.WorkspaceRoot != "" {
		requestOptions = append(requestOptions, WithWorkspaceRoot(request.WorkspaceRoot))
	}
	requestOptions = append(requestOptions, WithContextIdentity(request.TenantID, request.WorkspaceID, request.ProjectID, request.SessionID, request.RequestID))
	if identity.TenantID != "" || identity.CallerID != "" || len(identity.Capabilities) > 0 {
		requestOptions = append(requestOptions, WithPolicyIdentity(identity))
	}
	if policyVersion != "" {
		requestOptions = append(requestOptions, WithPolicyVersion(policyVersion))
	}
	if !request.AuthorizationExpiresAt.IsZero() {
		requestOptions = append(requestOptions, WithAuthorizationExpiry(request.AuthorizationExpiresAt))
	}
	if effectiveIdempotencyKey != "" {
		requestOptions = append(requestOptions, WithIdempotencyKey(effectiveIdempotencyKey))
	}
	if model.ID != "" {
		requestOptions = append(requestOptions, WithModel(model))
	}
	if request.Approval != nil {
		requestOptions = append(requestOptions, WithApprovalResolution(request.Approval))
	}
	if apiKey != "" {
		requestOptions = append(requestOptions, WithStreamDefaults(providers.StreamOptions{APIKey: apiKey}))
	}
	agentOptions := append([]Option(nil), r.options...)
	agentOptions = append(agentOptions, requestOptions...)
	registry := r.registry
	if registry != nil && r.eventStore != nil {
		registry = &eventBackedToolRegistry{base: registry, events: r.eventStore, operations: r.operationStore}
	}
	ag := New(provider, registry, agentOptions...)
	if restoreAgent {
		if err := ag.Restore(state.Agent); err != nil {
			return RunResponse{}, err
		}
		// Restore is intentionally authoritative for durable settings. Request
		// identity and ephemeral credentials are the exception: they must be
		// reapplied after restore so a sparse fresh snapshot cannot erase them.
		for _, option := range requestOptions {
			if option != nil {
				option(ag)
			}
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	var monitorDone chan struct{}
	if r.control != nil {
		monitorDone = make(chan struct{})
		go r.monitorAbort(runCtx, request.RunID, cancel, monitorDone)
	}
	var leaseDone chan struct{}
	if leaseStore, ok := r.stateStore.(LeaseStore); ok && lease.RunID != "" {
		leaseDone = make(chan struct{})
		go r.monitorLease(runCtx, leaseStore, lease, cancel, leaseDone)
	}
	defer func() {
		cancel()
		if monitorDone != nil {
			<-monitorDone
		}
		if leaseDone != nil {
			<-leaseDone
		}
	}()

	var eventsMu sync.Mutex
	events := make([]EventEnvelope, 0, 16)
	sequence := uint64(0)
	var checkpointErr error
	var eventStoreErr error
	acceptEvents := true
	existing, readErr := r.eventStore.Read(ctx, request.RunID, 0)
	if readErr != nil {
		return RunResponse{}, readErr
	}
	for _, event := range existing {
		if event.Sequence > sequence {
			sequence = event.Sequence
		}
	}
	seenEventIDs := make(map[string]struct{}, len(existing))
	seenIdempotencyKeys := make(map[string]struct{}, len(existing))
	for _, event := range existing {
		if event.EventID != "" {
			seenEventIDs[event.EventID] = struct{}{}
		}
		if event.IdempotencyKey != "" {
			seenIdempotencyKeys[event.IdempotencyKey] = struct{}{}
		}
	}
	appendEnvelope := func(envelope EventEnvelope) bool {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		if envelope.EventID != "" {
			if _, exists := seenEventIDs[envelope.EventID]; exists {
				return false
			}
		}
		if envelope.IdempotencyKey != "" {
			if _, exists := seenIdempotencyKeys[envelope.IdempotencyKey]; exists {
				return false
			}
		}
		sequence++
		envelope.Sequence = sequence
		if envelope.IdempotencyKey == "" && effectiveIdempotencyKey != "" {
			envelope.IdempotencyKey = fmt.Sprintf("%s:event-%d", effectiveIdempotencyKey, sequence)
		}
		events = append(events, envelope)
		if err := r.eventStore.Append(context.Background(), request.RunID, []EventEnvelope{envelope}); err != nil {
			if eventStoreErr == nil {
				eventStoreErr = err
			}
			return true
		}
		if envelope.EventID != "" {
			seenEventIDs[envelope.EventID] = struct{}{}
		}
		if envelope.IdempotencyKey != "" {
			seenIdempotencyKeys[envelope.IdempotencyKey] = struct{}{}
		}
		return true
	}
	unsubscribe := ag.Subscribe(func(event Event) {
		switch event.Kind {
		case EventAgentStarted, EventAgentCompleted, EventAbortRequested, EventAbortCompleted,
			EventTextDelta, EventTurnEnded, EventToolEnded, EventCompleted:
			// The runner owns the canonical lifecycle envelope and normalizes
			// these legacy aliases. Direct Agent consumers still receive them.
			return
		}
		eventsMu.Lock()
		if !acceptEvents {
			eventsMu.Unlock()
			return
		}
		envelope, envelopeErr := envelopeFromEvent(event, request.RunID, sequence+1)
		eventsMu.Unlock()
		appended := false
		if envelopeErr == nil {
			appended = appendEnvelope(envelope)
		}

		if (event.Kind == EventTurnCompleted || event.Kind == EventRetryScheduled) && appended {
			checkpoint := ag.Snapshot()
			checkpoint.RunID = request.RunID
			state.Agent = checkpoint
			state.Status = RunRunning
			state.Result = nil
			state.Owner = r.owner
			state.LeaseExpiresAt = lease.ExpiresAt
			if err := r.saveState(context.Background(), request.RunID, expectedVersion, state); err != nil && !errors.Is(err, ErrStateNotFound) {
				checkpointErr = err
				return
			} else {
				state.Version = expectedVersion + 1
				expectedVersion = state.Version
				if r.transcriptStore != nil {
					if err := r.saveTranscript(context.Background(), request.RunID, state.Version, checkpoint.Transcript); err != nil && checkpointErr == nil {
						checkpointErr = err
					}
				}
				checkpointEnvelope, checkpointEnvelopeErr := NewEventEnvelope(EventStateCheckpointed, request.RunID, 0, map[string]any{
					"version": state.Version,
					"status":  RunRunning,
				})
				if checkpointEnvelopeErr == nil {
					if effectiveIdempotencyKey != "" {
						checkpointEnvelope.IdempotencyKey = fmt.Sprintf("%s:checkpoint-%d", effectiveIdempotencyKey, state.Version)
					}
					appendEnvelope(checkpointEnvelope)
				}
			}
		}
	})
	start, _ := NewEventEnvelope(EventAgentStarted, request.RunID, 0, map[string]any{"owner": r.owner})
	if effectiveIdempotencyKey != "" {
		start.IdempotencyKey = effectiveIdempotencyKey + ":agent-start"
	}
	appendEnvelope(start)

	messages := append([]ai.Message(nil), request.Messages...)
	if state.Agent.PendingApproval == nil && request.Prompt != "" {
		messages = append(messages, promptMessage(request.Prompt))
	}
	if state.Agent.PendingApproval != nil {
		messages = nil
	}
	var result RunResult
	var runErr error
	if recoverAgent {
		result, runErr = ag.Recover(runCtx)
	} else {
		result, runErr = ag.RunMessages(runCtx, messages...)
	}
	status := statusForResult(result, runErr)
	if checkpointErr != nil && runErr == nil {
		runErr = checkpointErr
		status = statusForResult(result, runErr)
	}
	state.Agent = ag.Snapshot()
	state.Agent.RunID = request.RunID
	state.Result = &result
	state.Status = status
	state.Owner = ""
	state.LeaseExpiresAt = time.Time{}
	state.UpdatedAt = time.Now().UTC()
	if status == RunAborted && r.control != nil {
		if lifecycle, ok := r.control.(controlLifecycle); ok {
			_ = lifecycle.CompleteAbort(context.Background(), request.RunID, "run aborted")
		}
	}
	if status == RunAborted {
		abortRequested, _ := NewEventEnvelope(EventAbortRequested, request.RunID, 0, map[string]any{"status": status})
		if effectiveIdempotencyKey != "" {
			abortRequested.IdempotencyKey = effectiveIdempotencyKey + ":abort-requested"
		}
		appendEnvelope(abortRequested)
	}
	if runErr != nil {
		state.Result = &result
	}
	saveErr := r.saveState(context.Background(), request.RunID, expectedVersion, state)
	if saveErr == nil || errors.Is(saveErr, ErrStateNotFound) {
		state.Version = expectedVersion + 1
	} else if runErr == nil {
		runErr = saveErr
	}
	if r.transcriptStore != nil && saveErr == nil {
		if err := r.saveTranscript(context.Background(), request.RunID, state.Version, state.Agent.Transcript); err != nil && runErr == nil {
			runErr = err
		}
	}
	checkpoint, _ := NewEventEnvelope(EventStateCheckpointed, request.RunID, 0, map[string]any{
		"version": state.Version,
		"status":  state.Status,
	})
	if effectiveIdempotencyKey != "" {
		checkpoint.IdempotencyKey = effectiveIdempotencyKey + ":terminal-checkpoint"
	}
	appendEnvelope(checkpoint)
	if status != RunRecoverable && status != RunWaitingApproval {
		endKind := EventAgentCompleted
		if status == RunAborted {
			endKind = EventAbortCompleted
		}
		end, _ := NewEventEnvelope(endKind, request.RunID, 0, map[string]any{"status": status, "error": errorString(runErr)})
		if effectiveIdempotencyKey != "" {
			end.IdempotencyKey = effectiveIdempotencyKey + ":agent-end"
		}
		appendEnvelope(end)
	}
	eventsMu.Lock()
	acceptEvents = false
	durableEventErr := eventStoreErr
	eventsMu.Unlock()
	unsubscribe()
	if durableEventErr != nil && runErr == nil {
		runErr = durableEventErr
	}
	return RunResponse{RunID: request.RunID, Snapshot: state, Result: result, Events: events}, runErr
}
