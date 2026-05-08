package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateArgumentsValid(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"count":{"type":"integer","minimum":1}},"required":["name"],"additionalProperties":false}`)
	args := json.RawMessage(`{"name":"hello","count":5}`)
	if err := ValidateArguments(args, schema); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateArgumentsMissingRequired(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	args := json.RawMessage(`{"count":5}`)
	err := ValidateArguments(args, schema)
	if err == nil {
		t.Fatal("expected error for missing required field")
	}
	if !strings.Contains(err.Error(), "missing required") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateArgumentsWrongType(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}}}`)
	args := json.RawMessage(`{"count":"five"}`)
	err := ValidateArguments(args, schema)
	if err == nil {
		t.Fatal("expected error for wrong type")
	}
	if !strings.Contains(err.Error(), "must be an integer") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateArgumentsMinimum(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer","minimum":1}}}`)
	args := json.RawMessage(`{"count":0}`)
	err := ValidateArguments(args, schema)
	if err == nil {
		t.Fatal("expected error for below minimum")
	}
	if !strings.Contains(err.Error(), ">=") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateArgumentsMaximum(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer","maximum":100}}}`)
	args := json.RawMessage(`{"count":101}`)
	err := ValidateArguments(args, schema)
	if err == nil {
		t.Fatal("expected error for above maximum")
	}
	if !strings.Contains(err.Error(), "<=") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateArgumentsAdditionalProperties(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"additionalProperties":false}`)
	args := json.RawMessage(`{"name":"hello","extra":"x"}`)
	err := ValidateArguments(args, schema)
	if err == nil {
		t.Fatal("expected error for additional property")
	}
	if !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateArgumentsBoolean(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"flag":{"type":"boolean"}}}`)
	args := json.RawMessage(`{"flag":true}`)
	if err := ValidateArguments(args, schema); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateArgumentsArray(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"items":{"type":"array","items":{"type":"string"}}}}`)
	args := json.RawMessage(`{"items":["a","b"]}`)
	if err := ValidateArguments(args, schema); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateArgumentsArrayWrongItemType(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"items":{"type":"array","items":{"type":"string"}}}}`)
	args := json.RawMessage(`{"items":["a",1]}`)
	err := ValidateArguments(args, schema)
	if err == nil {
		t.Fatal("expected error for wrong item type")
	}
}

func TestValidateArgumentsEmpty(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)
	if err := ValidateArguments(nil, schema); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateArgumentsStringInSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`)
	args := json.RawMessage(`{"message":"hello world"}`)
	if err := ValidateArguments(args, schema); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
