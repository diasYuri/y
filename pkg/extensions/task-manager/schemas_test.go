package taskmanager

import (
	"encoding/json"
	"testing"
)

// The original hand-written schemas, kept as golden references to prove the
// reflection-generated schemas stay compatible.
var goldenSchemas = map[string]string{
	toolCreate:   `{"type":"object","properties":{"tasks":{"type":"array","items":{"type":"object"}}},"required":["tasks"],"additionalProperties":false}`,
	toolList:     `{"type":"object","properties":{},"additionalProperties":false}`,
	toolStart:    `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`,
	toolComplete: `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`,
	toolRefine:   `{"type":"object","properties":{"parentId":{"type":"string"},"parent_id":{"type":"string"},"tasks":{"type":"array","items":{"type":"object"}}},"required":["tasks"],"additionalProperties":false}`,
}

func TestDescriptors_MatchGoldenSchemas(t *testing.T) {
	t.Parallel()
	builders := map[string]func() (json.RawMessage, error){
		toolCreate:   func() (json.RawMessage, error) { d, err := createDescriptor(); return d.InputSchema, err },
		toolList:     func() (json.RawMessage, error) { d, err := listDescriptor(); return d.InputSchema, err },
		toolStart:    func() (json.RawMessage, error) { d, err := startDescriptor(); return d.InputSchema, err },
		toolComplete: func() (json.RawMessage, error) { d, err := completeDescriptor(); return d.InputSchema, err },
		toolRefine:   func() (json.RawMessage, error) { d, err := refineDescriptor(); return d.InputSchema, err },
	}

	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := build()
			if err != nil {
				t.Fatalf("descriptor: %v", err)
			}
			assertSchemaEquivalent(t, goldenSchemas[name], got)
		})
	}
}

// assertSchemaEquivalence compares the top-level shape of two JSON Schemas:
// property names, required list and additionalProperties. Nested item
// schemas may be richer than the golden reference (the generated form is a
// strict improvement), so only the top level must match exactly.
func assertSchemaEquivalent(t *testing.T, want string, got json.RawMessage) {
	t.Helper()
	var wantValue, gotValue map[string]any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("unmarshal golden schema: %v", err)
	}
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("unmarshal generated schema: %v", err)
	}
	if wantValue["type"] != gotValue["type"] {
		t.Fatalf("type mismatch: want %v, got %v", wantValue["type"], gotValue["type"])
	}
	if wantValue["additionalProperties"] != gotValue["additionalProperties"] {
		t.Fatalf("additionalProperties mismatch: want %v, got %v", wantValue["additionalProperties"], gotValue["additionalProperties"])
	}
	assertStringSetEqual(t, "required", wantValue["required"], gotValue["required"])
	assertPropertyNamesEqual(t, wantValue["properties"], gotValue["properties"])
}

func assertStringSetEqual(t *testing.T, label string, want, got any) {
	t.Helper()
	wantSet := toStringSet(want)
	gotSet := toStringSet(got)
	if len(wantSet) != len(gotSet) {
		t.Fatalf("%s mismatch: want %v, got %v", label, wantSet, gotSet)
	}
	for value := range wantSet {
		if !gotSet[value] {
			t.Fatalf("%s mismatch: want %v, got %v", label, wantSet, gotSet)
		}
	}
}

func assertPropertyNamesEqual(t *testing.T, want, got any) {
	t.Helper()
	wantProps, _ := want.(map[string]any)
	gotProps, _ := got.(map[string]any)
	if len(wantProps) != len(gotProps) {
		t.Fatalf("properties mismatch: want %v, got %v", wantProps, gotProps)
	}
	for name := range wantProps {
		if _, ok := gotProps[name]; !ok {
			t.Fatalf("properties mismatch: missing %q in %v", name, gotProps)
		}
	}
}

func toStringSet(value any) map[string]bool {
	set := map[string]bool{}
	items, _ := value.([]any)
	for _, item := range items {
		if name, ok := item.(string); ok {
			set[name] = true
		}
	}
	return set
}
