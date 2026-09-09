package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestJSONLAdapterDispatchesVersionedCommand(t *testing.T) {
	command, err := NewCommand(CommandGetState, map[string]string{"view": "full"})
	if err != nil {
		t.Fatal(err)
	}
	command.ID, command.RunID = "cmd-1", "run-1"
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	adapter := JSONLAdapter{Handler: HandlerFunc(func(_ context.Context, got Command) (Response, error) {
		if got.Kind != CommandGetState || got.RunID != "run-1" {
			t.Fatalf("command = %#v", got)
		}
		return Response{Accepted: true, Payload: json.RawMessage(`{"state":"idle"}`)}, nil
	})}
	if err := adapter.Serve(context.Background(), bytes.NewReader(append(raw, '\n')), &output); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Accepted || response.CommandID != "cmd-1" || response.RunID != "run-1" {
		t.Fatalf("response = %#v", response)
	}
}

func TestJSONLAdapterReturnsInvalidCommandResponse(t *testing.T) {
	var output bytes.Buffer
	adapter := JSONLAdapter{Handler: HandlerFunc(func(context.Context, Command) (Response, error) {
		t.Fatal("handler should not run")
		return Response{}, nil
	})}
	if err := adapter.Serve(context.Background(), bytes.NewBufferString(`{"schema_version":99,"kind":"abort"}`+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != "invalid_command" {
		t.Fatalf("response = %#v", response)
	}
}
