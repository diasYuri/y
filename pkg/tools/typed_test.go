package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type typedArgs struct {
	Path    string   `json:"path" desc:"File path to read" required:"true"`
	Limit   int      `json:"limit,omitempty" desc:"Max lines"`
	Verbose bool     `json:"verbose,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Mode    string   `json:"mode,omitempty" enum:"fast,slow"`
	Nested  struct {
		Inner string `json:"inner"`
	} `json:"nested,omitempty"`
	Meta json.RawMessage `json:"meta,omitempty"`
}

type typedMapArgs struct {
	Values map[string]any `json:"values"`
}

func schemaOf(t *testing.T, name string) map[string]any {
	t.Helper()
	registry := NewRegistry()
	if err := RegisterTyped(registry, name, "test tool", func(ctx context.Context, args typedArgs) (string, error) {
		return "ok", nil
	}); err != nil {
		t.Fatalf("RegisterTyped: %v", err)
	}
	desc, _, ok := registry.Get(name)
	if !ok {
		t.Fatalf("tool %q not registered", name)
	}
	var schema map[string]any
	if err := json.Unmarshal(desc.InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	return schema
}

func TestSchemaFor_GeneratesExpectedSchema(t *testing.T) {
	t.Parallel()
	schema := schemaOf(t, "typed_test")

	if schema["type"] != "object" {
		t.Fatalf("expected object schema, got %v", schema["type"])
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("expected additionalProperties false, got %v", schema["additionalProperties"])
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties object, got %T", schema["properties"])
	}
	for _, name := range []string{"path", "limit", "verbose", "tags", "mode", "nested", "meta"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("missing property %q in %v", name, properties)
		}
	}
	if properties["path"].(map[string]any)["type"] != "string" {
		t.Fatalf("path should be string: %v", properties["path"])
	}
	if properties["path"].(map[string]any)["description"] != "File path to read" {
		t.Fatalf("path description missing: %v", properties["path"])
	}
	if properties["limit"].(map[string]any)["type"] != "integer" {
		t.Fatalf("limit should be integer: %v", properties["limit"])
	}
	if properties["verbose"].(map[string]any)["type"] != "boolean" {
		t.Fatalf("verbose should be boolean: %v", properties["verbose"])
	}
	tags := properties["tags"].(map[string]any)
	if tags["type"] != "array" || tags["items"].(map[string]any)["type"] != "string" {
		t.Fatalf("tags should be array of string: %v", tags)
	}
	mode := properties["mode"].(map[string]any)
	enum, ok := mode["enum"].([]any)
	if !ok || len(enum) != 2 || enum[0] != "fast" || enum[1] != "slow" {
		t.Fatalf("mode enum mismatch: %v", mode["enum"])
	}
	nested := properties["nested"].(map[string]any)
	if nested["type"] != "object" {
		t.Fatalf("nested should be object: %v", nested)
	}

	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("expected required list, got %T", schema["required"])
	}
	// Only "path" (required:"true" and no omitempty) and "nested.inner"'s
	// parent rules apply: nested has omitempty so it is optional; inner has
	// no omitempty but required applies to root fields only.
	if len(required) != 1 || required[0] != "path" {
		t.Fatalf("expected required=[path], got %v", required)
	}
}

func TestSchemaFor_NonStructRoot(t *testing.T) {
	t.Parallel()
	if _, err := SchemaFor(reflect.TypeOf("")); err == nil {
		t.Fatal("expected error for non-struct root")
	}
	if _, err := SchemaFor(reflect.TypeOf(map[string]int{})); err == nil {
		t.Fatal("expected error for map root")
	}
}

func TestRegisterTyped_InterfaceValuedMap(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	if err := RegisterTyped(registry, "typed_map", "map", func(ctx context.Context, args typedMapArgs) (string, error) {
		return "ok", nil
	}); err != nil {
		t.Fatalf("RegisterTyped: %v", err)
	}

	desc, _, ok := registry.Get("typed_map")
	if !ok {
		t.Fatal("typed map tool not registered")
	}
	var schema map[string]any
	if err := json.Unmarshal(desc.InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	properties := schema["properties"].(map[string]any)
	values := properties["values"].(map[string]any)
	if values["type"] != "object" {
		t.Fatalf("values should be an object: %v", values)
	}
	if additional, ok := values["additionalProperties"].(map[string]any); !ok || len(additional) != 0 {
		t.Fatalf("interface-valued map should allow free-form values: %v", values["additionalProperties"])
	}
}

func TestRegisterTyped_HandlerDecodesArguments(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	var got typedArgs
	err := RegisterTyped(registry, "typed_echo", "echo", func(ctx context.Context, args typedArgs) (string, error) {
		got = args
		return "done", nil
	})
	if err != nil {
		t.Fatalf("RegisterTyped: %v", err)
	}
	_, handler, _ := registry.Get("typed_echo")
	resp, err := handler.Handle(context.Background(), ToolRequest{
		Name:      "typed_echo",
		Arguments: json.RawMessage(`{"path":"a.go","limit":10,"mode":"fast"}`),
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got.Path != "a.go" || got.Limit != 10 || got.Mode != "fast" {
		t.Fatalf("decoded args mismatch: %+v", got)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "done" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestRegisterTyped_InvalidArguments(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	if err := RegisterTyped(registry, "typed_bad", "bad", func(ctx context.Context, args typedArgs) (string, error) {
		return "", nil
	}); err != nil {
		t.Fatalf("RegisterTyped: %v", err)
	}
	_, handler, _ := registry.Get("typed_bad")
	_, err := handler.Handle(context.Background(), ToolRequest{
		Name:      "typed_bad",
		Arguments: json.RawMessage(`{"path": 42}`),
	})
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("expected ErrInvalidTool, got %v", err)
	}
}

func TestRegisterTyped_HandlerErrorPropagates(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	wantErr := errors.New("boom")
	if err := RegisterTyped(registry, "typed_err", "err", func(ctx context.Context, args typedArgs) (string, error) {
		return "", wantErr
	}); err != nil {
		t.Fatalf("RegisterTyped: %v", err)
	}
	_, handler, _ := registry.Get("typed_err")
	if _, err := handler.Handle(context.Background(), ToolRequest{Name: "typed_err"}); !errors.Is(err, wantErr) {
		t.Fatalf("expected handler error, got %v", err)
	}
}

func TestRegisterTyped_NilHandler(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	if err := RegisterTyped[typedArgs](registry, "typed_nil", "nil", nil); !errors.Is(err, ErrInvalidTool) {
		t.Fatalf("expected ErrInvalidTool, got %v", err)
	}
}

func TestRegisterTyped_NilRegistry(t *testing.T) {
	t.Parallel()
	err := RegisterTyped(nil, "typed_nil_registry", "nil registry", func(ctx context.Context, args typedArgs) (string, error) {
		return "", nil
	})
	if err == nil || err.Error() != "tool registry is nil" {
		t.Fatalf("expected nil registry error, got %v", err)
	}
}

func TestRegisterTyped_Options(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	err := RegisterTyped(registry, "typed_opts", "opts", func(ctx context.Context, args typedArgs) (string, error) {
		return "", nil
	},
		WithTypedCapabilities(CapabilityFilesystemRead),
		WithTypedSensitive(),
		WithTypedExecutionMode(ExecutionSequential),
		WithTypedLimits(ToolLimits{MaxOutputBytes: 1024}),
	)
	if err != nil {
		t.Fatalf("RegisterTyped: %v", err)
	}
	desc, _, _ := registry.Get("typed_opts")
	if !desc.Sensitive || desc.ExecutionMode != ExecutionSequential {
		t.Fatalf("options not applied: %+v", desc)
	}
	if len(desc.Capabilities) != 1 || desc.Capabilities[0] != CapabilityFilesystemRead {
		t.Fatalf("capabilities not applied: %+v", desc)
	}
	if desc.Limits.MaxOutputBytes != 1024 {
		t.Fatalf("limits not applied: %+v", desc)
	}
}
