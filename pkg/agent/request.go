package agent

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
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
	}
	effectiveOpts := mergeStreamOptions(builtin, streamDefaults, perCall)

	request := providers.StreamRequest{
		Model: model,
		Context: ai.Context{
			SystemPrompt: systemPrompt,
			Messages:     transcript,
			Tools:        toolDescriptors(registry),
		},
		Options: effectiveOpts,
	}

	if beforeRequest != nil {
		hooked, err := beforeRequest(ctx, &request)
		if err != nil {
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
			if usageObserver != nil {
				usageObserver(UsageReported, hooked.Usage)
			}
			if afterRequest != nil {
				if hookErr := afterRequest(ctx, request, message, hooked.Usage, nil); hookErr != nil {
					return ai.Message{}, hooked.Usage, stopReason, hookErr
				}
			}
			return message, hooked.Usage, stopReason, nil
		}
	}

	a.setState(StateRequestingModel)
	stream, err := provider.Stream(ctx, request)
	if err != nil {
		err = a.invokeOnError(ctx, ErrorPhaseRequest, err)
		if afterRequest != nil {
			err = afterRequest(ctx, request, ai.Message{}, ai.Usage{}, err)
		}
		return ai.Message{}, ai.Usage{}, ai.StopReasonStop, err
	}
	defer func() { _ = stream.Close() }()

	a.setState(StateStreaming)

	builder := newAssistantBuilder()
	usage := ai.Usage{}
	stopReason := ai.StopReasonStop
	usageReported := false
	for {
		if err := ctx.Err(); err != nil {
			return ai.Message{}, usage, stopReason, err
		}
		event, err := stream.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			err = a.invokeOnError(ctx, ErrorPhaseRequest, err)
			if afterRequest != nil {
				err = afterRequest(ctx, request, ai.Message{}, usage, err)
			}
			return ai.Message{}, usage, stopReason, err
		}

		switch event := event.(type) {
		case ai.TextDelta:
			a.emit(Event{Kind: EventTextDelta, State: StateStreaming, Turn: turn, TextDelta: event.Text})
			builder.addText(event.Text)
		case ai.ToolCallEvent:
			builder.addToolCall(event)
		case ai.UsageEvent:
			usage = addUsage(usage, event.Usage)
			usageReported = true
		case ai.StopEvent:
			stopReason = event.Reason
		case ai.ErrorEvent:
			err := a.invokeOnError(ctx, ErrorPhaseRequest, event)
			if afterRequest != nil {
				err = afterRequest(ctx, request, ai.Message{}, usage, err)
			}
			return ai.Message{}, usage, stopReason, err
		}
	}

	message, err := builder.build()
	if err != nil {
		return ai.Message{}, usage, stopReason, err
	}
	if message.Timestamp.IsZero() {
		message.Timestamp = time.Now().UTC()
	}
	message.Role = ai.RoleAssistant

	origin := UsageReported
	if !usageReported || usage.OutputTokens == 0 {
		usage.OutputTokens = estimateTokens(message)
		origin = UsageEstimated
		if logger != nil {
			logger.Logf("agent: usage fell back to char-based estimate (turn=%d, model=%s, est=%d)",
				turn, model.ID, usage.OutputTokens)
		}
	}
	if usageObserver != nil {
		usageObserver(origin, usage)
	}

	if afterRequest != nil {
		if hookErr := afterRequest(ctx, request, message, usage, nil); hookErr != nil {
			return ai.Message{}, usage, stopReason, hookErr
		}
	}

	return message, usage, stopReason, nil
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
