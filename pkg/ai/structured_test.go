package ai

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateStructuredOutput(t *testing.T) {
	format := ResponseFormat{
		Type:   ResponseFormatJSONSchema,
		Name:   "person",
		Strict: true,
		Schema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"age":{"type":"integer","minimum":0},"tags":{"type":"array","items":{"type":"string"}}},"required":["name","age"],"additionalProperties":false}`),
	}

	if err := ValidateResponseFormat(format); err != nil {
		t.Fatalf("ValidateResponseFormat: %v", err)
	}
	if err := ValidateStructuredOutput(json.RawMessage(`{"name":"Ada","age":37,"tags":["math"]}`), format); err != nil {
		t.Fatalf("ValidateStructuredOutput valid value: %v", err)
	}
	if err := ValidateStructuredOutput(json.RawMessage(`{"name":"Ada"}`), format); err == nil {
		t.Fatal("ValidateStructuredOutput accepted missing required property")
	}
	if err := ValidateStructuredOutput(json.RawMessage(`{"name":"Ada","age":-1}`), format); err == nil {
		t.Fatal("ValidateStructuredOutput accepted value below minimum")
	}
}

func TestValidateStructuredOutputJSONObject(t *testing.T) {
	format := ResponseFormat{Type: ResponseFormatJSONObject}
	if err := ValidateStructuredOutput(json.RawMessage(`{"ok":true}`), format); err != nil {
		t.Fatalf("valid object: %v", err)
	}
	if err := ValidateStructuredOutput(json.RawMessage(`[]`), format); err == nil {
		t.Fatal("accepted non-object JSON mode output")
	}
}

func TestValidateResponseFormatRejectsUnsupportedSchemaComposition(t *testing.T) {
	err := ValidateResponseFormat(ResponseFormat{
		Type:   ResponseFormatJSONSchema,
		Name:   "invalid",
		Schema: json.RawMessage(`{"oneOf":[{"type":"string"}]}`),
	})
	if err == nil {
		t.Fatal("accepted unsupported schema composition")
	}
}

func TestStructuredOutputError(t *testing.T) {
	base := errors.New("invalid output")
	err := &StructuredOutputError{Provider: "test", Output: json.RawMessage(`nope`), Err: base}
	if !errors.Is(err, base) {
		t.Fatal("StructuredOutputError does not unwrap cause")
	}
	if err.Error() == "" {
		t.Fatal("StructuredOutputError has empty message")
	}
}
