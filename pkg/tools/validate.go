package tools

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// ValidateArguments checks raw JSON arguments against a JSON Schema subset.
// It validates: type, required fields, additionalProperties, minimum/maximum.
func ValidateArguments(args json.RawMessage, schema json.RawMessage) error {
	if len(args) == 0 {
		return nil
	}
	var s schemaRoot
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}
	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		return toolError("invalid_arguments", "arguments are not valid JSON", ErrInvalidTool)
	}
	return validateValue(v, s, "")
}

type schemaRoot struct {
	Type                 string          `json:"type"`
	Properties           map[string]prop `json:"properties"`
	Required             []string        `json:"required"`
	AdditionalProperties bool            `json:"additionalProperties"`
	Minimum              *float64        `json:"minimum,omitempty"`
	Maximum              *float64        `json:"maximum,omitempty"`
}

type prop struct {
	Type    string   `json:"type"`
	Items   *prop    `json:"items,omitempty"`
	Minimum *float64 `json:"minimum,omitempty"`
	Maximum *float64 `json:"maximum,omitempty"`
}

func validateValue(val any, s schemaRoot, path string) error {
	if s.Type != "object" {
		return nil
	}
	m, ok := val.(map[string]any)
	if !ok {
		return toolError("invalid_arguments", fmt.Sprintf("expected object at %s", path), ErrInvalidTool)
	}

	// Check required fields.
	for _, req := range s.Required {
		if _, ok := m[req]; !ok {
			return toolError("invalid_arguments", fmt.Sprintf("missing required field %q", req), ErrInvalidTool)
		}
	}

	// Check additionalProperties.
	if !s.AdditionalProperties {
		for key := range m {
			if _, ok := s.Properties[key]; !ok {
				return toolError("invalid_arguments", fmt.Sprintf("unexpected field %q", key), ErrInvalidTool)
			}
		}
	}

	// Validate each property.
	for key, p := range s.Properties {
		v, ok := m[key]
		if !ok {
			continue
		}
		if err := validateProperty(v, p, key); err != nil {
			return err
		}
	}

	return nil
}

func validateProperty(val any, p prop, path string) error {
	switch p.Type {
	case "string":
		if _, ok := val.(string); !ok {
			return toolError("invalid_arguments", fmt.Sprintf("field %q must be a string", path), ErrInvalidTool)
		}
	case "integer":
		n, ok := toNumber(val)
		if !ok || !isInteger(n) {
			return toolError("invalid_arguments", fmt.Sprintf("field %q must be an integer", path), ErrInvalidTool)
		}
		if p.Minimum != nil && n < *p.Minimum {
			return toolError("invalid_arguments", fmt.Sprintf("field %q must be >= %v", path, *p.Minimum), ErrInvalidTool)
		}
		if p.Maximum != nil && n > *p.Maximum {
			return toolError("invalid_arguments", fmt.Sprintf("field %q must be <= %v", path, *p.Maximum), ErrInvalidTool)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return toolError("invalid_arguments", fmt.Sprintf("field %q must be a boolean", path), ErrInvalidTool)
		}
	case "array":
		arr, ok := val.([]any)
		if !ok {
			return toolError("invalid_arguments", fmt.Sprintf("field %q must be an array", path), ErrInvalidTool)
		}
		if p.Items != nil {
			for i, item := range arr {
				if err := validateProperty(item, *p.Items, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case "number":
		n, ok := toNumber(val)
		if !ok {
			return toolError("invalid_arguments", fmt.Sprintf("field %q must be a number", path), ErrInvalidTool)
		}
		if p.Minimum != nil && n < *p.Minimum {
			return toolError("invalid_arguments", fmt.Sprintf("field %q must be >= %v", path, *p.Minimum), ErrInvalidTool)
		}
		if p.Maximum != nil && n > *p.Maximum {
			return toolError("invalid_arguments", fmt.Sprintf("field %q must be <= %v", path, *p.Maximum), ErrInvalidTool)
		}
	case "object":
		// Nested objects not deeply validated without recursive schema.
	}
	return nil
}

func toNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func isInteger(n float64) bool {
	return n == float64(int64(n))
}

func init() {
	// Ensure json.RawMessage is handled as a string for schema validation.
	_ = reflect.TypeOf(json.RawMessage{})
}
