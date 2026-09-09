package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"unicode/utf8"
)

// ResponseFormatKind identifies the requested assistant output format.
type ResponseFormatKind string

const (
	ResponseFormatText       ResponseFormatKind = "text"
	ResponseFormatJSONObject ResponseFormatKind = "json_object"
	ResponseFormatJSONSchema ResponseFormatKind = "json_schema"
)

// ResponseFormat requests a provider-enforced structured response.
//
// JSONSchema is the portable, strictest form. JSON object mode is retained for
// providers and models that support valid JSON without a declared schema.
type ResponseFormat struct {
	Type   ResponseFormatKind `json:"type"`
	Name   string             `json:"name,omitempty"`
	Schema json.RawMessage    `json:"schema,omitempty"`
	Strict bool               `json:"strict,omitempty"`
}

// StructuredOutputError indicates that a provider returned output that did not
// satisfy the requested structured-output contract.
type StructuredOutputError struct {
	Provider string
	Format   ResponseFormat
	Output   json.RawMessage
	Err      error
}

func (e *StructuredOutputError) Error() string {
	if e == nil {
		return "structured output validation failed"
	}
	if e.Err != nil {
		return fmt.Sprintf("structured output validation failed: %v", e.Err)
	}
	return "structured output validation failed"
}

func (e *StructuredOutputError) Unwrap() error { return e.Err }

// SchemaMap returns the JSON schema as an object suitable for provider SDKs.
func (f ResponseFormat) SchemaMap() (map[string]any, error) {
	if len(bytes.TrimSpace(f.Schema)) == 0 {
		return nil, errors.New("response format schema is required")
	}
	var schema map[string]any
	if err := json.Unmarshal(f.Schema, &schema); err != nil {
		return nil, fmt.Errorf("invalid response schema: %w", err)
	}
	if schema == nil {
		return nil, errors.New("response format schema must be a JSON object")
	}
	return schema, nil
}

// ValidateResponseFormat validates the portable request contract before it is
// translated into a provider-specific request.
func ValidateResponseFormat(format ResponseFormat) error {
	switch format.Type {
	case ResponseFormatText:
		if len(format.Schema) != 0 || format.Name != "" || format.Strict {
			return errors.New("text response format cannot include schema, name, or strict")
		}
	case ResponseFormatJSONObject:
		if len(format.Schema) != 0 || format.Name != "" {
			return errors.New("json_object response format cannot include schema or name")
		}
	case ResponseFormatJSONSchema:
		if err := validateFormatName(format.Name); err != nil {
			return err
		}
		schema, err := format.SchemaMap()
		if err != nil {
			return err
		}
		if err := validateSchemaNode(schema, "$"); err != nil {
			return fmt.Errorf("invalid response schema: %w", err)
		}
	default:
		return fmt.Errorf("unsupported response format %q", format.Type)
	}
	return nil
}

func validateFormatName(name string) error {
	if name == "" {
		return errors.New("response schema name is required")
	}
	if len(name) > 64 {
		return errors.New("response schema name must be at most 64 characters")
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return fmt.Errorf("response schema name contains invalid character %q", r)
		}
	}
	return nil
}

// ValidateStructuredOutput validates output JSON against the requested
// response format. Text mode is intentionally a no-op.
func ValidateStructuredOutput(output json.RawMessage, format ResponseFormat) error {
	if err := ValidateResponseFormat(format); err != nil {
		return err
	}
	if format.Type == ResponseFormatText {
		return nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("output is not valid JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("output contains more than one JSON value")
	} else if !errors.Is(err, io.EOF) {
		// Decode a second value only to reject trailing non-whitespace bytes.
		return fmt.Errorf("output has trailing data: %w", err)
	}
	if format.Type == ResponseFormatJSONObject {
		if _, ok := value.(map[string]any); !ok {
			return errors.New("output must be a JSON object")
		}
		return nil
	}
	schema, err := format.SchemaMap()
	if err != nil {
		return err
	}
	return validateValueAgainstSchema(value, schema, "$")
}

func validateSchemaNode(schema map[string]any, path string) error {
	if schema == nil {
		return fmt.Errorf("%s must be an object", path)
	}
	if _, ok := schema["oneOf"]; ok {
		return fmt.Errorf("%s.oneOf is not supported", path)
	}
	if _, ok := schema["anyOf"]; ok {
		return fmt.Errorf("%s.anyOf is not supported", path)
	}
	if _, ok := schema["allOf"]; ok {
		return fmt.Errorf("%s.allOf is not supported", path)
	}
	if rawType, ok := schema["type"]; ok {
		switch value := rawType.(type) {
		case string:
			if !validJSONType(value) {
				return fmt.Errorf("%s.type %q is unsupported", path, value)
			}
		case []any:
			for _, item := range value {
				name, ok := item.(string)
				if !ok || !validJSONType(name) {
					return fmt.Errorf("%s.type contains an unsupported value", path)
				}
			}
		default:
			return fmt.Errorf("%s.type must be a string or array", path)
		}
	}
	if rawProperties, ok := schema["properties"]; ok {
		properties, ok := rawProperties.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.properties must be an object", path)
		}
		for name, raw := range properties {
			node, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.properties.%s must be an object", path, name)
			}
			if err := validateSchemaNode(node, path+".properties."+name); err != nil {
				return err
			}
		}
	}
	if rawRequired, ok := schema["required"]; ok {
		required, ok := rawRequired.([]any)
		if !ok {
			return fmt.Errorf("%s.required must be an array", path)
		}
		properties, _ := schema["properties"].(map[string]any)
		for _, raw := range required {
			name, ok := raw.(string)
			if !ok || name == "" {
				return fmt.Errorf("%s.required contains a non-string name", path)
			}
			if properties != nil {
				if _, exists := properties[name]; !exists {
					return fmt.Errorf("%s.required references unknown property %q", path, name)
				}
			}
		}
	}
	if rawAdditional, ok := schema["additionalProperties"]; ok {
		if _, ok := rawAdditional.(bool); !ok {
			return fmt.Errorf("%s.additionalProperties must be boolean", path)
		}
	}
	if rawItems, ok := schema["items"]; ok {
		items, ok := rawItems.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.items must be an object", path)
		}
		if err := validateSchemaNode(items, path+".items"); err != nil {
			return err
		}
	}
	if rawEnum, ok := schema["enum"]; ok {
		if values, ok := rawEnum.([]any); !ok || len(values) == 0 {
			return fmt.Errorf("%s.enum must be a non-empty array", path)
		}
	}
	for _, key := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum"} {
		if value, ok := schema[key]; ok {
			if _, ok := value.(float64); !ok {
				return fmt.Errorf("%s.%s must be numeric", path, key)
			}
		}
	}
	for _, key := range []string{"minLength", "maxLength", "minItems", "maxItems"} {
		if value, ok := schema[key]; ok {
			n, ok := value.(float64)
			if !ok || n < 0 || math.Trunc(n) != n {
				return fmt.Errorf("%s.%s must be a non-negative integer", path, key)
			}
		}
	}
	if pattern, ok := schema["pattern"].(string); ok {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("%s.pattern is invalid: %w", path, err)
		}
	}
	return nil
}

func validJSONType(value string) bool {
	switch value {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return true
	default:
		return false
	}
}

func validateValueAgainstSchema(value any, schema map[string]any, path string) error {
	if rawConst, ok := schema["const"]; ok && !jsonEqual(value, rawConst) {
		return fmt.Errorf("%s must equal const", path)
	}
	if rawEnum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range rawEnum {
			if jsonEqual(value, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is not one of enum values", path)
		}
	}
	if rawType, ok := schema["type"]; ok && !matchesJSONType(value, rawType) {
		return fmt.Errorf("%s has the wrong type", path)
	}
	switch typed := value.(type) {
	case map[string]any:
		if rawRequired, ok := schema["required"].([]any); ok {
			for _, raw := range rawRequired {
				name, valid := raw.(string)
				if !valid {
					return fmt.Errorf("%s.required contains a non-string name", path)
				}
				if _, exists := typed[name]; !exists {
					return fmt.Errorf("%s is missing required property %q", path, name)
				}
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
			for name := range typed {
				if _, exists := properties[name]; !exists {
					return fmt.Errorf("%s contains unexpected property %q", path, name)
				}
			}
		}
		for name, raw := range properties {
			if child, exists := typed[name]; exists {
				if node, ok := raw.(map[string]any); ok {
					if err := validateValueAgainstSchema(child, node, path+"."+name); err != nil {
						return err
					}
				}
			}
		}
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for i, item := range typed {
				if err := validateValueAgainstSchema(item, items, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case string:
		length := utf8.RuneCountInString(typed)
		if err := validateIntegerLimit(schema, "minLength", float64(length), path); err != nil {
			return err
		}
		if err := validateMaxLimit(schema, "maxLength", float64(length), path); err != nil {
			return err
		}
		if pattern, ok := schema["pattern"].(string); ok {
			matched, _ := regexp.MatchString(pattern, typed)
			if !matched {
				return fmt.Errorf("%s does not match pattern", path)
			}
		}
	case json.Number:
		n, err := typed.Float64()
		if err != nil {
			return fmt.Errorf("%s is not numeric", path)
		}
		for _, key := range []string{"minimum", "exclusiveMinimum"} {
			if err := validateMinNumber(schema, key, n, path); err != nil {
				return err
			}
		}
		for _, key := range []string{"maximum", "exclusiveMaximum"} {
			if err := validateMaxNumber(schema, key, n, path); err != nil {
				return err
			}
		}
	}
	if items, ok := value.([]any); ok {
		if err := validateIntegerLimit(schema, "minItems", float64(len(items)), path); err != nil {
			return err
		}
		if err := validateMaxLimit(schema, "maxItems", float64(len(items)), path); err != nil {
			return err
		}
	}
	return nil
}

func matchesJSONType(value any, raw any) bool {
	types := []string{}
	switch typed := raw.(type) {
	case string:
		types = []string{typed}
	case []any:
		for _, item := range typed {
			if name, ok := item.(string); ok {
				types = append(types, name)
			}
		}
	}
	for _, name := range types {
		if (name == "object" && value != nil && reflectMap(value)) ||
			(name == "array" && reflectSlice(value)) ||
			(name == "string" && isString(value)) ||
			(name == "number" && isNumber(value)) ||
			(name == "integer" && isIntegerValue(value)) ||
			(name == "boolean" && isBool(value)) ||
			(name == "null" && value == nil) {
			return true
		}
	}
	return len(types) == 0
}

func reflectMap(value any) bool   { _, ok := value.(map[string]any); return ok }
func reflectSlice(value any) bool { _, ok := value.([]any); return ok }
func isString(value any) bool     { _, ok := value.(string); return ok }
func isBool(value any) bool       { _, ok := value.(bool); return ok }
func isNumber(value any) bool     { _, ok := value.(json.Number); return ok }
func isIntegerValue(value any) bool {
	n, ok := value.(json.Number)
	if !ok {
		return false
	}
	f, err := n.Float64()
	return err == nil && math.Trunc(f) == f
}

func jsonEqual(a, b any) bool {
	if left, ok := numericValue(a); ok {
		if right, ok := numericValue(b); ok {
			return left == right
		}
	}
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		n, err := typed.Float64()
		return n, err == nil
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

func validateIntegerLimit(schema map[string]any, key string, value float64, path string) error {
	if limit, ok := schema[key].(float64); ok && value < limit {
		return fmt.Errorf("%s is below %s", path, key)
	}
	return nil
}

func validateMaxLimit(schema map[string]any, key string, value float64, path string) error {
	if limit, ok := schema[key].(float64); ok && value > limit {
		return fmt.Errorf("%s is above %s", path, key)
	}
	return nil
}

func validateMinNumber(schema map[string]any, key string, value float64, path string) error {
	if limit, ok := schema[key].(float64); ok {
		if key == "exclusiveMinimum" && value <= limit {
			return fmt.Errorf("%s must be greater than %s", path, key)
		}
		if key == "minimum" && value < limit {
			return fmt.Errorf("%s is below %s", path, key)
		}
	}
	return nil
}

func validateMaxNumber(schema map[string]any, key string, value float64, path string) error {
	if limit, ok := schema[key].(float64); ok {
		if key == "exclusiveMaximum" && value >= limit {
			return fmt.Errorf("%s must be less than %s", path, key)
		}
		if key == "maximum" && value > limit {
			return fmt.Errorf("%s is above %s", path, key)
		}
	}
	return nil
}
