package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ProtocolVersion is the version of transport-neutral runtime commands.
const ProtocolVersion uint32 = 1

// CommandKind identifies a command accepted by a runtime adapter.
type CommandKind string

const (
	CommandStartRun         CommandKind = "start_run"
	CommandSendMessage      CommandKind = "send_message"
	CommandSteer            CommandKind = "steer"
	CommandFollowUp         CommandKind = "follow_up"
	CommandAbort            CommandKind = "abort"
	CommandRetry            CommandKind = "retry"
	CommandCompact          CommandKind = "compact"
	CommandGetState         CommandKind = "get_state"
	CommandGetMessages      CommandKind = "get_messages"
	CommandGetEvents        CommandKind = "get_events"
	CommandSetModel         CommandKind = "set_model"
	CommandSetThinkingLevel CommandKind = "set_thinking_level"
	CommandFork             CommandKind = "fork"
	CommandClone            CommandKind = "clone"
	CommandCheckpoint       CommandKind = "checkpoint"
)

// Command is the only payload a transport adapter needs to understand. The
// typed payload belongs to the handler; JSONL, HTTP, gRPC and queues can all
// carry the same envelope unchanged.
type Command struct {
	SchemaVersion  uint32          `json:"schema_version"`
	ID             string          `json:"id"`
	Kind           CommandKind     `json:"kind"`
	RunID          string          `json:"run_id,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	TenantID       string          `json:"tenant_id,omitempty"`
	CorrelationID  string          `json:"correlation_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	SentAt         time.Time       `json:"sent_at"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

// Response is a serializable command result. Error is structured so remote
// clients never need to parse Go error strings.
type Response struct {
	SchemaVersion uint32          `json:"schema_version"`
	CommandID     string          `json:"command_id"`
	RunID         string          `json:"run_id,omitempty"`
	Accepted      bool            `json:"accepted"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Error         *CommandError   `json:"error,omitempty"`
}

// CommandError is the transport-safe error representation.
type CommandError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

// Handler dispatches a command. Implementations own state, authorization and
// persistence; transport adapters intentionally own none of those concerns.
type Handler interface {
	Dispatch(context.Context, Command) (Response, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(context.Context, Command) (Response, error)

func (f HandlerFunc) Dispatch(ctx context.Context, command Command) (Response, error) {
	return f(ctx, command)
}

// NewCommand serializes payload and initializes version/timestamp fields.
func NewCommand(kind CommandKind, payload any) (Command, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Command{}, fmt.Errorf("marshal %s command payload: %w", kind, err)
	}
	return Command{SchemaVersion: ProtocolVersion, Kind: kind, SentAt: time.Now().UTC(), Payload: raw}, nil
}

// Validate rejects malformed or unknown command envelopes before a handler
// sees them.
func (c Command) Validate() error {
	if c.SchemaVersion == 0 {
		return errors.New("runtime command schema version is required")
	}
	if c.SchemaVersion != ProtocolVersion {
		return fmt.Errorf("unsupported runtime command schema version %d", c.SchemaVersion)
	}
	if !validCommandKind(c.Kind) {
		return fmt.Errorf("unsupported runtime command kind %q", c.Kind)
	}
	return nil
}

func validCommandKind(kind CommandKind) bool {
	switch kind {
	case CommandStartRun, CommandSendMessage, CommandSteer, CommandFollowUp, CommandAbort,
		CommandRetry, CommandCompact, CommandGetState, CommandGetMessages, CommandGetEvents,
		CommandSetModel, CommandSetThinkingLevel, CommandFork, CommandClone, CommandCheckpoint:
		return true
	default:
		return false
	}
}

// JSONLAdapter is a stdin/stdout-friendly protocol adapter. It reads exactly
// one JSON command per line and emits exactly one JSON response per line.
// Individual handler failures are returned as structured responses so a long
// lived stream is not lost to one bad request.
type JSONLAdapter struct {
	Handler Handler
}

// Serve reads until EOF or context cancellation.
func (a JSONLAdapter) Serve(ctx context.Context, reader io.Reader, writer io.Writer) error {
	if a.Handler == nil {
		return errors.New("runtime JSONL adapter has nil handler")
	}
	if reader == nil || writer == nil {
		return errors.New("runtime JSONL adapter requires reader and writer")
	}
	scanner := bufio.NewScanner(reader)
	// Commands can carry message blocks and tool arguments; avoid Scanner's
	// tiny default token cap while retaining a bounded line-oriented protocol.
	scanner.Buffer(make([]byte, 4096), 8<<20)
	encoder := json.NewEncoder(writer)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var command Command
		if err := json.Unmarshal([]byte(line), &command); err != nil {
			if err := encoder.Encode(Response{SchemaVersion: ProtocolVersion, Accepted: false, Error: &CommandError{Code: "invalid_command", Message: err.Error()}}); err != nil {
				return err
			}
			continue
		}
		response, err := dispatch(ctx, a.Handler, command)
		if err != nil && response.Error == nil {
			response = Response{SchemaVersion: ProtocolVersion, CommandID: command.ID, RunID: command.RunID, Accepted: false, Error: &CommandError{Code: "dispatch_failed", Message: err.Error()}}
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func dispatch(ctx context.Context, handler Handler, command Command) (Response, error) {
	if err := command.Validate(); err != nil {
		return Response{SchemaVersion: ProtocolVersion, CommandID: command.ID, RunID: command.RunID, Accepted: false, Error: &CommandError{Code: "invalid_command", Message: err.Error()}}, err
	}
	response, err := handler.Dispatch(ctx, command)
	if response.SchemaVersion == 0 {
		response.SchemaVersion = ProtocolVersion
	}
	if response.CommandID == "" {
		response.CommandID = command.ID
	}
	if response.RunID == "" {
		response.RunID = command.RunID
	}
	return response, err
}
