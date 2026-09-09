package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// QueueMessage is the smallest common denominator of NATS, Kafka and cloud
// queue deliveries. NATS adapters normally implement Reply; Kafka adapters
// normally use Publish with a response topic/correlation key instead.
type QueueMessage struct {
	Body  []byte
	Reply func(context.Context, []byte) error
}

// Publisher publishes a response to an application-selected destination.
type Publisher interface {
	Publish(context.Context, []byte) error
}

// PublisherFunc adapts a function to Publisher.
type PublisherFunc func(context.Context, []byte) error

func (f PublisherFunc) Publish(ctx context.Context, body []byte) error { return f(ctx, body) }

// QueueAdapter converts a durable queue delivery into one versioned command.
// It intentionally does not import a broker SDK; NATS and Kafka integrations
// remain independent adapters that only need to map their delivery object to
// QueueMessage and acknowledge it after Handle returns successfully.
type QueueAdapter struct {
	Handler   Handler
	Publisher Publisher
}

// Handle decodes, dispatches and serializes one delivery. A malformed command
// gets a structured response (and can therefore be acknowledged instead of
// becoming a poison-message retry loop).
func (a QueueAdapter) Handle(ctx context.Context, message QueueMessage) error {
	if a.Handler == nil {
		return errors.New("runtime queue adapter has nil handler")
	}
	var command Command
	if err := json.Unmarshal(message.Body, &command); err != nil {
		return a.respond(ctx, message, Response{SchemaVersion: ProtocolVersion, Accepted: false, Error: &CommandError{Code: "invalid_command", Message: err.Error()}})
	}
	response, err := dispatch(ctx, a.Handler, command)
	if err != nil && response.Error == nil {
		response = Response{SchemaVersion: ProtocolVersion, CommandID: command.ID, RunID: command.RunID, Accepted: false, Error: &CommandError{Code: "dispatch_failed", Message: err.Error()}}
	}
	return a.respond(ctx, message, response)
}

func (a QueueAdapter) respond(ctx context.Context, message QueueMessage, response Response) error {
	raw, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("marshal runtime queue response: %w", err)
	}
	if message.Reply != nil {
		return message.Reply(ctx, raw)
	}
	if a.Publisher != nil {
		return a.Publisher.Publish(ctx, raw)
	}
	return nil
}

// NATSAdapter is the broker-independent bridge used by a NATS subscription.
// Map msg.Data and msg.Respond into QueueMessage, then acknowledge according
// to the subscription's delivery policy after Handle succeeds.
type NATSAdapter = QueueAdapter

// KafkaAdapter is the broker-independent bridge used by a Kafka consumer.
// Configure Publisher for a response topic (or omit it for one-way commands)
// and commit the source offset only after Handle succeeds.
type KafkaAdapter = QueueAdapter
