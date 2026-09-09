package runtime

import (
	"context"
	"encoding/json"
	"testing"
)

func TestQueueAdapterRepliesWithCommandResponse(t *testing.T) {
	command, err := NewCommand(CommandCheckpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	command.ID, command.RunID = "cmd", "run"
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	var replied Response
	adapter := QueueAdapter{Handler: HandlerFunc(func(_ context.Context, got Command) (Response, error) {
		if got.Kind != CommandCheckpoint {
			t.Fatalf("command = %#v", got)
		}
		return Response{Accepted: true}, nil
	})}
	err = adapter.Handle(context.Background(), QueueMessage{Body: body, Reply: func(_ context.Context, raw []byte) error { return json.Unmarshal(raw, &replied) }})
	if err != nil {
		t.Fatal(err)
	}
	if !replied.Accepted || replied.CommandID != "cmd" {
		t.Fatalf("response = %#v", replied)
	}
}
