package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// TypedHandler is a tool handler whose arguments are decoded into a concrete
// struct before invocation. Returning an error produces a tool-level error
// response (visible to the model), not a run failure.
type TypedHandler[A any] func(ctx context.Context, args A) (string, error)

// TypedOption customizes a tool registered with RegisterTyped.
type TypedOption func(*ToolDescriptor)

// WithTypedCapabilities sets the capabilities required to run the tool.
func WithTypedCapabilities(capabilities ...Capability) TypedOption {
	return func(desc *ToolDescriptor) { desc.Capabilities = append([]Capability(nil), capabilities...) }
}

// WithTypedLimits sets the tool limits.
func WithTypedLimits(limits ToolLimits) TypedOption {
	return func(desc *ToolDescriptor) { desc.Limits = limits }
}

// WithTypedSensitive marks the tool as sensitive for policy purposes.
func WithTypedSensitive() TypedOption {
	return func(desc *ToolDescriptor) { desc.Sensitive = true }
}

// WithTypedExecutionMode sets the tool execution mode.
func WithTypedExecutionMode(mode ExecutionMode) TypedOption {
	return func(desc *ToolDescriptor) { desc.ExecutionMode = mode }
}

// RegisterTyped registers a tool whose argument struct A is reflected into a
// JSON Schema and decoded automatically before fn runs.
//
// Schema generation rules for A (a struct, or a pointer to one):
//   - exported fields become "properties", named by the `json` tag
//     (falling back to the field name);
//   - a field is listed in "required" when its `json` tag lacks
//     `omitempty`, or when the tag `required:"true"` is present;
//   - the tag `desc:"..."` documents the field, `enum:"a,b,c"` restricts a
//     string field to a fixed set;
//   - supported field types: string, bool, all int/uint kinds, float32/64,
//     slices, maps with string keys, nested structs (depth-capped), and
//     json.RawMessage or interface values (free-form).
//
// The root schema always sets "additionalProperties": false.
func RegisterTyped[A any](registry *Registry, name, description string, fn TypedHandler[A], opts ...TypedOption) error {
	if registry == nil {
		return fmt.Errorf("tool registry is nil")
	}
	if fn == nil {
		return toolError("invalid_tool", fmt.Sprintf("tool %q has nil handler", name), ErrInvalidTool)
	}
	var zero A
	schema, err := SchemaFor(reflect.TypeOf(&zero).Elem())
	if err != nil {
		return toolError("invalid_tool", fmt.Sprintf("tool %q: %v", name, err), ErrInvalidTool)
	}
	desc := ToolDescriptor{Name: name, Description: description, InputSchema: schema}
	for _, opt := range opts {
		opt(&desc)
	}
	handler := ToolHandlerFunc(func(ctx context.Context, req ToolRequest) (ToolResponse, error) {
		var args A
		if len(req.Arguments) > 0 {
			if err := json.Unmarshal(req.Arguments, &args); err != nil {
				return ToolResponse{}, toolError("invalid_args", fmt.Sprintf("tool %q: decode arguments: %v", name, err), ErrInvalidTool)
			}
		}
		text, err := fn(ctx, args)
		if err != nil {
			return ToolResponse{}, err
		}
		return ToolResponse{Content: []ContentBlock{{Type: ContentText, Text: text}}}, nil
	})
	return registry.Add(desc, handler)
}

// maxSchemaDepth bounds nested struct recursion in generated schemas.
const maxSchemaDepth = 8

// SchemaFor generates a JSON Schema for the given struct type using the same
// rules documented on RegisterTyped.
func SchemaFor(typ reflect.Type) (json.RawMessage, error) {
	if typ == nil {
		return nil, fmt.Errorf("schema: nil type")
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, fmt.Errorf("schema: root type must be a struct, got %s", typ.Kind())
	}
	schema, err := schemaForType(typ, 0)
	if err != nil {
		return nil, err
	}
	object := schema.(map[string]any)
	object["additionalProperties"] = false
	return json.Marshal(object)
}

func schemaForType(typ reflect.Type, depth int) (any, error) {
	if depth > maxSchemaDepth {
		return nil, fmt.Errorf("schema: nesting deeper than %d levels", maxSchemaDepth)
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	switch typ {
	case reflect.TypeOf(json.RawMessage{}):
		return map[string]any{}, nil
	}

	switch typ.Kind() {
	case reflect.Interface:
		return map[string]any{}, nil
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Slice, reflect.Array:
		items, err := schemaForType(typ.Elem(), depth+1)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Map:
		if typ.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("schema: map keys must be strings, got %s", typ.Key().Kind())
		}
		values, err := schemaForType(typ.Elem(), depth+1)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": values}, nil
	case reflect.Struct:
		return schemaForStruct(typ, depth)
	}
	return nil, fmt.Errorf("schema: unsupported field type %s", typ)
}

func schemaForStruct(typ reflect.Type, depth int) (map[string]any, error) {
	properties := map[string]any{}
	var required []string
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name, options := fieldName(field)
		if name == "-" {
			continue
		}
		property, err := schemaForType(field.Type, depth+1)
		if err != nil {
			return nil, fmt.Errorf("schema: field %s.%s: %w", typ.Name(), field.Name, err)
		}
		if propertyMap, ok := property.(map[string]any); ok {
			if desc := field.Tag.Get("desc"); desc != "" {
				propertyMap["description"] = desc
			}
			if enum := field.Tag.Get("enum"); enum != "" {
				var values []any
				for _, value := range strings.Split(enum, ",") {
					values = append(values, strings.TrimSpace(value))
				}
				propertyMap["enum"] = values
			}
		}
		properties[name] = property
		if field.Tag.Get("required") == "true" || !options["omitempty"] {
			required = append(required, name)
		}
	}
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema, nil
}

func fieldName(field reflect.StructField) (string, map[string]bool) {
	tag := field.Tag.Get("json")
	options := map[string]bool{}
	if tag == "" {
		return field.Name, options
	}
	parts := strings.Split(tag, ",")
	for _, option := range parts[1:] {
		options[option] = true
	}
	name := parts[0]
	if name == "" {
		name = field.Name
	}
	return name, options
}
