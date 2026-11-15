package openai

import (
	"encoding/json"
	"testing"

	"github.com/diasYuri/y/pkg/ai"
	"github.com/openai/openai-go/v3/responses"
)

func TestToolNameSurvivesArgumentsEventsWithoutName(t *testing.T) {
	consume := newOpenAIConsumer()
	fixtures := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item-1","call_id":"call-1","name":"write_file","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"item-1","delta":"{\"path\":\"proof.txt\"}"}`,
		`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"item-1","arguments":"{\"path\":\"proof.txt\"}"}`,
	}
	var final ai.ToolCallEvent
	for _, raw := range fixtures {
		var event responses.ResponseStreamEventUnion
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		for _, normalized := range consume(event) {
			if call, ok := normalized.(ai.ToolCallEvent); ok {
				if call.ToolCall.Name != "write_file" {
					t.Fatal("tool identity was lost in streaming")
				}
				final = call
			}
		}
	}
	if !final.Complete || final.ToolCall.ID != "call-1|item-1" || string(final.ToolCall.Arguments) != `{"path":"proof.txt"}` {
		t.Fatalf("bad final call: %#v", final)
	}
}
