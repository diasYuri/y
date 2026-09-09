package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/providers"
	"github.com/yuri/y/pkg/providers/providertest"
	"github.com/yuri/y/pkg/tools"
)

func testResponseFormat() *ai.ResponseFormat {
	return &ai.ResponseFormat{
		Type:   ai.ResponseFormatJSONSchema,
		Name:   "answer",
		Strict: true,
		Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`),
	}
}

func structuredFakeProvider(output string) *providertest.FakeProvider {
	return providertest.NewFakeProvider(
		providertest.WithFakeModels(ai.Model{
			ID:       "fake-1",
			Provider: "fake",
			Capabilities: ai.ModelCapabilities{
				StructuredOutput: true,
				Streaming:        true,
			},
		}),
		providertest.WithFakeResponses(providertest.FakeResponse{Events: []ai.Event{
			ai.TextDelta{Text: output},
			ai.StopEvent{Reason: ai.StopReasonStop},
		}}),
	)
}

func TestRunWithOptionsStructuredOutput(t *testing.T) {
	provider := structuredFakeProvider(`{"answer":"ok"}`)
	a := New(provider, tools.NewRegistry())
	result, err := a.RunWithOptions(context.Background(), "return an answer", RunOptions{
		Stream: providers.StreamOptions{ResponseFormat: testResponseFormat()},
	})
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	if len(result.Messages) == 0 {
		t.Fatal("RunWithOptions returned no messages")
	}
	got := result.Messages[len(result.Messages)-1]
	if string(got.StructuredOutput) != `{"answer":"ok"}` {
		t.Fatalf("StructuredOutput = %s", got.StructuredOutput)
	}
	if len(got.Content) == 0 || got.Content[0].Text != `{"answer":"ok"}` {
		t.Fatalf("Content = %#v, want compatibility text", got.Content)
	}
}

func TestRunWithOptionsStructuredOutputRejectsInvalidResponse(t *testing.T) {
	provider := structuredFakeProvider(`{"answer":42}`)
	a := New(provider, tools.NewRegistry())
	_, err := a.RunWithOptions(context.Background(), "return an answer", RunOptions{
		Stream: providers.StreamOptions{ResponseFormat: testResponseFormat()},
	})
	var structuredErr *ai.StructuredOutputError
	if !errors.As(err, &structuredErr) {
		t.Fatalf("error = %v, want StructuredOutputError", err)
	}
	if string(structuredErr.Output) != `{"answer":42}` {
		t.Fatalf("preserved output = %s", structuredErr.Output)
	}
}

func TestRunWithOptionsStructuredOutputRequiresCapability(t *testing.T) {
	provider := providertest.NewFakeProvider(providertest.WithFakeResponses(providertest.FakeResponse{Events: []ai.Event{
		ai.TextDelta{Text: `{"answer":"ok"}`},
		ai.StopEvent{Reason: ai.StopReasonStop},
	}}))
	a := New(provider, tools.NewRegistry())
	_, err := a.RunWithOptions(context.Background(), "return an answer", RunOptions{
		Stream: providers.StreamOptions{ResponseFormat: testResponseFormat()},
	})
	if !errors.Is(err, providers.ErrStructuredOutputUnsupported) {
		t.Fatalf("error = %v, want ErrStructuredOutputUnsupported", err)
	}
}

func TestStreamOptionsPreserveStructuredOutputAcrossSnapshot(t *testing.T) {
	format := testResponseFormat()
	snapshot := streamOptionsToSnapshot(providers.StreamOptions{ResponseFormat: format})
	options := streamOptionsFromSnapshot(snapshot)
	if options.ResponseFormat == nil || string(options.ResponseFormat.Schema) != string(format.Schema) {
		t.Fatalf("snapshot response format = %#v", options.ResponseFormat)
	}
	if options.ResponseFormat == format {
		t.Fatal("snapshot reused response format pointer")
	}
}
