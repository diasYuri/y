package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/diasYuri/y/pkg/ai"
	"github.com/diasYuri/y/pkg/providers"
	"github.com/diasYuri/y/pkg/telemetry"
)

// errBeforeRequestSwallowed signals that a BeforeRequest hook returned an
// error that the OnError hook decided to swallow. The agent treats it as a
// no-op turn (no message appended).
var errBeforeRequestSwallowed = errors.New("agent: before-request error swallowed")

func (a *Agent) requestAssistant(ctx context.Context, model ai.Model, turn int) (ai.Message, ai.Usage, ai.StopReason, error) {
	a.mu.Lock()
	provider := a.provider
	registry := a.registry
	systemPrompt := a.systemPrompt
	transcript := cloneMessages(a.transcript)
	sessionID := a.sessionID
	thinkingBudgets := a.thinkingBudgets
	streamDefaults := a.streamDefaults
	runID := a.runID
	idempotencyKey := a.idempotencyKey
	contextResolver := a.contextResolver
	contextRequest := a.contextRequest
	beforeRequest := a.beforeRequest
	afterRequest := a.afterRequest
	logger := a.logger
	usageObserver := a.usageObserver
	a.mu.Unlock()

	perCall := runOptionsFromContext(ctx).Stream
	if provider == nil {
		return ai.Message{}, ai.Usage{}, ai.StopReasonStop, errors.New("agent provider is nil")
	}

	builtin := providers.StreamOptions{
		SessionID:       sessionID,
		ThinkingBudgets: thinkingBudgets,
		RunID:           runID,
		TurnID:          fmt.Sprintf("%s-turn-%d", runID, turn),
	}
	if idempotencyKey == "" {
		idempotencyKey = runID
	}
	builtin.RequestID = fmt.Sprintf("%s-request-%d", runID, turn)
	builtin.IdempotencyKey = fmt.Sprintf("%s-turn-%d", idempotencyKey, turn)
	effectiveOpts := mergeStreamOptions(builtin, streamDefaults, perCall)
	if len(model.Headers) > 0 {
		if effectiveOpts.Headers == nil {
			effectiveOpts.Headers = make(map[string]string, len(model.Headers))
		}
		for key, value := range model.Headers {
			if _, exists := effectiveOpts.Headers[key]; !exists {
				effectiveOpts.Headers[key] = value
			}
		}
	}
	if contextResolver != nil {
		if contextRequest.BasePrompt == "" {
			contextRequest.BasePrompt = systemPrompt
		}
		contextRequest.Query = latestUserQuery(transcript)
		resolved, resolveErr := contextResolver.Resolve(ctx, contextRequest)
		if resolveErr != nil {
			return ai.Message{}, ai.Usage{}, ai.StopReasonStop, resolveErr
		}
		systemPrompt = resolved.SystemPrompt
	}
	requestTools := toolDescriptors(registry)
	for _, hooks := range a.hooksSnapshot() {
		if hooks.Resources != nil {
			resources, err := hooks.Resources(ctx)
			if err != nil {
				return ai.Message{}, ai.Usage{}, ai.StopReasonStop, err
			}
			transcript = append(transcript, cloneMessages(resources)...)
		}
		if hooks.Tools != nil {
			custom, err := hooks.Tools(ctx)
			if err != nil {
				return ai.Message{}, ai.Usage{}, ai.StopReasonStop, err
			}
			requestTools = append(requestTools, custom...)
		}
	}

	request := providers.StreamRequest{
		Model: model,
		Context: ai.Context{
			SchemaVersion: ai.CurrentSchemaVersion,
			SystemPrompt:  systemPrompt,
			Messages:      transcript,
			Tools:         requestTools,
		},
		Options: effectiveOpts,
	}
	requestCtx, requestSpan := a.startSpan(ctx, "provider.request",
		telemetry.Attribute{Key: "provider.id", Value: provider.ID()},
		telemetry.Attribute{Key: "model.id", Value: model.ID},
		telemetry.Attribute{Key: "turn", Value: turn},
	)
	ctx = requestCtx
	requestStartedAt := time.Now()
	defer requestSpan.End()

	runtimeHooks := a.hooksSnapshot()
	beforeRequestHooks := make([]BeforeRequestHook, 0, len(runtimeHooks)+1)
	for _, hooks := range runtimeHooks {
		if hooks.BeforeRequest != nil {
			beforeRequestHooks = append(beforeRequestHooks, hooks.BeforeRequest)
		}
	}
	if beforeRequest != nil {
		beforeRequestHooks = append(beforeRequestHooks, beforeRequest)
	}
	for _, requestHook := range beforeRequestHooks {
		hooked, err := requestHook(ctx, &request)
		if err != nil {
			requestSpan.RecordError(err)
			err = a.invokeOnError(ctx, ErrorPhaseRequest, err)
			if err == nil {
				return ai.Message{}, ai.Usage{}, ai.StopReasonStop, errBeforeRequestSwallowed
			}
			return ai.Message{}, ai.Usage{}, ai.StopReasonStop, err
		}
		if hooked != nil {
			message := hooked.Message
			if message.Timestamp.IsZero() {
				message.Timestamp = time.Now().UTC()
			}
			if message.Role == "" {
				message.Role = ai.RoleAssistant
			}
			stopReason := hooked.StopReason
			if stopReason == "" {
				stopReason = ai.StopReasonStop
			}
			if err := applyStructuredOutput(&message, request, provider.ID()); err != nil {
				err = a.invokeOnError(ctx, ErrorPhaseRequest, err)
				err = a.invokeAfterRequestHooks(ctx, runtimeHooks, afterRequest, request, message, hooked.Usage, err)
				return ai.Message{}, hooked.Usage, stopReason, err
			}
			if usageObserver != nil {
				usageObserver(UsageReported, hooked.Usage)
			}
			if hookErr := a.invokeAfterRequestHooks(ctx, runtimeHooks, afterRequest, request, message, hooked.Usage, nil); hookErr != nil {
				return ai.Message{}, hooked.Usage, stopReason, hookErr
			}
			requestSpan.SetStatus(telemetry.StatusOK, "short_circuited")
			a.recordAccounting(telemetry.Measurement{Dimensions: a.accountingDimensions(turn, model, ""), Usage: hooked.Usage, Cost: hooked.Usage.Cost.Total, Latency: time.Since(requestStartedAt)})
			return message, hooked.Usage, stopReason, nil
		}
	}

	providerCaps := providers.Capabilities{}
	if capable, ok := provider.(interface {
		Capabilities(string) providers.Capabilities
	}); ok {
		providerCaps = capable.Capabilities(model.ID)
	}
	if err := providers.ValidateStructuredOutputRequest(request, provider.ID(), providerCaps); err != nil {
		requestSpan.RecordError(err)
		err = a.invokeOnError(ctx, ErrorPhaseRequest, err)
		err = a.invokeAfterRequestHooks(ctx, runtimeHooks, afterRequest, request, ai.Message{}, ai.Usage{}, err)
		return ai.Message{}, ai.Usage{}, ai.StopReasonStop, err
	}

	a.setState(StateRequestingModel)
	stream, err := provider.Stream(ctx, request)
	if err != nil {
		requestSpan.RecordError(err)
		err = a.invokeOnError(ctx, ErrorPhaseRequest, err)
		err = a.invokeAfterRequestHooks(ctx, runtimeHooks, afterRequest, request, ai.Message{}, ai.Usage{}, err)
		return ai.Message{}, ai.Usage{}, ai.StopReasonStop, err
	}
	defer func() { _ = stream.Close() }()

	a.setState(StateStreaming)

	builder := newAssistantBuilder()
	usage := ai.Usage{}
	stopReason := ai.StopReasonStop
	usageReported := false
	responseID := ""
	firstTokenAt := time.Time{}
	var providerMetadata, responseDetails json.RawMessage
	for {
		if err := ctx.Err(); err != nil {
			requestSpan.RecordError(err)
			return ai.Message{}, usage, stopReason, err
		}
		event, err := stream.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			err = a.invokeOnError(ctx, ErrorPhaseRequest, err)
			err = a.invokeAfterRequestHooks(ctx, runtimeHooks, afterRequest, request, ai.Message{}, usage, err)
			requestSpan.RecordError(err)
			return ai.Message{}, usage, stopReason, err
		}

		switch event := event.(type) {
		case ai.TextDelta:
			if firstTokenAt.IsZero() {
				firstTokenAt = time.Now()
				requestSpan.AddEvent("first_token")
			}
			a.emit(Event{Kind: EventMessageDelta, State: StateStreaming, Turn: turn, TextDelta: event.Text})
			a.emit(Event{Kind: EventTextDelta, State: StateStreaming, Turn: turn, TextDelta: event.Text})
			builder.addText(event.Text)
		case ai.ThinkingDelta:
			a.emit(Event{Kind: EventThinkingDelta, State: StateStreaming, Turn: turn, ThinkingDelta: event.Thinking})
			builder.addThinking(event.Thinking, event.Signature)
		case ai.ImageEvent:
			builder.addImage(event)
		case ai.ToolCallEvent:
			builder.addToolCall(event)
		case ai.UsageEvent:
			usage = addUsage(usage, event.Usage)
			usageReported = true
		case ai.StopEvent:
			stopReason = event.Reason
			responseID = event.ResponseID
			providerMetadata = append([]byte(nil), event.ProviderMetadata...)
			responseDetails = append([]byte(nil), event.Details...)
		case ai.ErrorEvent:
			err := a.invokeOnError(ctx, ErrorPhaseRequest, event)
			requestSpan.RecordError(err)
			err = a.invokeAfterRequestHooks(ctx, runtimeHooks, afterRequest, request, ai.Message{}, usage, err)
			return ai.Message{}, usage, stopReason, err
		}
	}

	message, err := builder.build()
	if err != nil {
		requestSpan.RecordError(err)
		return ai.Message{}, usage, stopReason, err
	}
	if message.Timestamp.IsZero() {
		message.Timestamp = time.Now().UTC()
	}
	message.Role = ai.RoleAssistant
	message.StopReason = stopReason
	message.Provider = ai.ProviderID(provider.ID())
	message.ModelID = model.ID
	message.ResponseID = responseID
	message.ProviderMetadata = providerMetadata
	message.Details = responseDetails
	if err := applyStructuredOutput(&message, request, provider.ID()); err != nil {
		requestSpan.RecordError(err)
		hookErr := a.invokeAfterRequestHooks(ctx, runtimeHooks, afterRequest, request, message, usage, err)
		if hookErr != nil {
			err = hookErr
		}
		return ai.Message{}, usage, stopReason, err
	}

	origin := UsageReported
	if !usageReported || usage.OutputTokens == 0 {
		usage.OutputTokens = estimateTokens(message)
		origin = UsageEstimated
		if logger != nil {
			logger.Logf("agent: usage fell back to char-based estimate (turn=%d, model=%s, est=%d)",
				turn, model.ID, usage.OutputTokens)
		}
	}
	usage = withModelCost(usage, model.Cost)
	message.Usage = usage
	a.emit(Event{Kind: EventMessageCompleted, State: StateStreaming, Turn: turn, Message: message, Usage: usage})
	if usageObserver != nil {
		usageObserver(origin, usage)
	}

	if hookErr := a.invokeAfterRequestHooks(ctx, runtimeHooks, afterRequest, request, message, usage, nil); hookErr != nil {
		requestSpan.RecordError(hookErr)
		return ai.Message{}, usage, stopReason, hookErr
	}
	requestSpan.SetStatus(telemetry.StatusOK, "")
	measurement := telemetry.Measurement{Dimensions: a.accountingDimensions(turn, model, ""), Usage: usage, Cost: usage.Cost.Total, Latency: time.Since(requestStartedAt)}
	if !firstTokenAt.IsZero() {
		measurement.TimeToFirstToken = firstTokenAt.Sub(requestStartedAt)
	}
	a.recordAccounting(measurement)

	return message, usage, stopReason, nil
}

func latestUserQuery(messages []ai.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != ai.RoleUser {
			continue
		}
		var builder strings.Builder
		for _, block := range messages[i].Content {
			if block.Type == ai.ContentText {
				builder.WriteString(block.Text)
				builder.WriteByte(' ')
			}
		}
		return strings.TrimSpace(builder.String())
	}
	return ""
}

func applyStructuredOutput(message *ai.Message, request providers.StreamRequest, providerID string) error {
	format := request.Options.ResponseFormat
	if format == nil || format.Type == ai.ResponseFormatText {
		return nil
	}
	var text strings.Builder
	for _, block := range message.Content {
		if block.Type != ai.ContentText {
			continue
		}
		text.WriteString(block.Text)
	}
	raw := json.RawMessage(text.String())
	if err := ai.ValidateStructuredOutput(raw, *format); err != nil {
		return &ai.StructuredOutputError{
			Provider: providerID,
			Format:   *format,
			Output:   append(json.RawMessage(nil), raw...),
			Err:      err,
		}
	}
	message.StructuredOutput = append(json.RawMessage(nil), raw...)
	return nil
}

func (a *Agent) invokeAfterRequestHooks(ctx context.Context, hooks []RuntimeHooks, configured AfterRequestHook, request providers.StreamRequest, message ai.Message, usage ai.Usage, requestErr error) error {
	for _, runtimeHook := range hooks {
		if runtimeHook.AfterRequest != nil {
			if err := runtimeHook.AfterRequest(ctx, request, message, usage, requestErr); err != nil {
				return err
			}
		}
	}
	if configured != nil {
		return configured(ctx, request, message, usage, requestErr)
	}
	return requestErr
}

func estimateTokens(message ai.Message) int64 {
	var chars int64
	for _, block := range message.Content {
		chars += int64(len(block.Text))
	}
	for _, toolCall := range message.ToolCalls {
		chars += int64(len(toolCall.Name))
		chars += int64(len(toolCall.Arguments))
	}
	// Heuristic: 4 ASCII chars ~ 1 token, 1 non-ASCII char ~ 1 token.
	return chars/4 + 1
}

func withModelCost(usage ai.Usage, cost ai.Cost) ai.Usage {
	if usage.Cost.Total != 0 || (cost.Input == 0 && cost.Output == 0 && cost.CacheRead == 0 && cost.CacheWrite == 0) {
		return usage
	}
	usage.Cost.Input = float64(usage.InputTokens) * cost.Input / 1_000_000
	usage.Cost.Output = float64(usage.OutputTokens) * cost.Output / 1_000_000
	usage.Cost.CacheRead = float64(usage.CacheReadTokens) * cost.CacheRead / 1_000_000
	usage.Cost.CacheWrite = float64(usage.CacheWriteTokens) * cost.CacheWrite / 1_000_000
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
	return usage
}
