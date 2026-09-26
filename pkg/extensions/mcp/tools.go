package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/diasYuri/y/pkg/tools"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const emptyObjectSchema = `{"type":"object","properties":{}}`

type toolRegistration struct {
	descriptor tools.ToolDescriptor
	handler    tools.ToolHandler
}

func registerTools(registry *tools.Registry, servers []*serverSession) error {
	registrations := make([]toolRegistration, 0)
	for _, server := range servers {
		for _, remote := range server.tools {
			server, remote := server, remote
			registrations = append(registrations, toolRegistration{
				descriptor: remote.descriptor,
				handler: tools.ToolHandlerFunc(func(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
					return callRemoteTool(ctx, server, remote.name, req.Arguments)
				}),
			})
		}
		registrations = append(registrations,
			toolRegistration{
				descriptor: tools.ToolDescriptor{
					Name:        "mcp." + server.name + ".resources.list",
					Description: "List resources available from MCP server " + server.name + ".",
					InputSchema: json.RawMessage(emptyObjectSchema),
				},
				handler: tools.ToolHandlerFunc(func(ctx context.Context, _ tools.ToolRequest) (tools.ToolResponse, error) {
					return listResources(ctx, server)
				}),
			},
			toolRegistration{
				descriptor: tools.ToolDescriptor{
					Name:        "mcp." + server.name + ".resources.templates.list",
					Description: "List resource templates available from MCP server " + server.name + ".",
					InputSchema: json.RawMessage(emptyObjectSchema),
				},
				handler: tools.ToolHandlerFunc(func(ctx context.Context, _ tools.ToolRequest) (tools.ToolResponse, error) {
					return listResourceTemplates(ctx, server)
				}),
			},
			toolRegistration{
				descriptor: tools.ToolDescriptor{
					Name:        "mcp." + server.name + ".resources.read",
					Description: "Read one resource by URI from MCP server " + server.name + ".",
					InputSchema: json.RawMessage(`{"type":"object","properties":{"uri":{"type":"string"}},"required":["uri"]}`),
				},
				handler: tools.ToolHandlerFunc(func(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
					return readResource(ctx, server, req.Arguments)
				}),
			},
			toolRegistration{
				descriptor: tools.ToolDescriptor{
					Name:        "mcp." + server.name + ".prompts.list",
					Description: "List prompt templates available from MCP server " + server.name + ".",
					InputSchema: json.RawMessage(emptyObjectSchema),
				},
				handler: tools.ToolHandlerFunc(func(ctx context.Context, _ tools.ToolRequest) (tools.ToolResponse, error) {
					return listPrompts(ctx, server)
				}),
			},
			toolRegistration{
				descriptor: tools.ToolDescriptor{
					Name:        "mcp." + server.name + ".prompts.get",
					Description: "Get a prompt template from MCP server " + server.name + ".",
					InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"arguments":{"type":"object","additionalProperties":{"type":"string"}}},"required":["name"]}`),
				},
				handler: tools.ToolHandlerFunc(func(ctx context.Context, req tools.ToolRequest) (tools.ToolResponse, error) {
					return getPrompt(ctx, server, req.Arguments)
				}),
			},
		)
	}

	seen := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		name := registration.descriptor.Name
		if _, exists := seen[name]; exists {
			return fmt.Errorf("mcp: multiple server tools map to %q", name)
		}
		seen[name] = struct{}{}
		if _, _, exists := registry.Get(name); exists {
			return fmt.Errorf("mcp: tool %q is already registered", name)
		}
	}
	for _, registration := range registrations {
		if err := registry.Add(registration.descriptor, registration.handler); err != nil {
			return fmt.Errorf("mcp: register tool %q: %w", registration.descriptor.Name, err)
		}
	}
	return nil
}

func callRemoteTool(ctx context.Context, server *serverSession, name string, raw json.RawMessage) (tools.ToolResponse, error) {
	arguments, err := decodeObject(raw)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("mcp: decode arguments for %q: %w", name, err)
	}
	result, err := server.session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("mcp: call %s.%s: %w", server.name, name, err)
	}
	return toolResponse(result)
}

func listResources(ctx context.Context, server *serverSession) (tools.ToolResponse, error) {
	resources := make([]*mcpsdk.Resource, 0)
	for resource, err := range server.session.Resources(ctx, nil) {
		if err != nil {
			return tools.ToolResponse{}, fmt.Errorf("mcp: list resources from %q: %w", server.name, err)
		}
		resources = append(resources, resource)
	}
	return jsonResponse(resources)
}

func listResourceTemplates(ctx context.Context, server *serverSession) (tools.ToolResponse, error) {
	templates := make([]*mcpsdk.ResourceTemplate, 0)
	for template, err := range server.session.ResourceTemplates(ctx, nil) {
		if err != nil {
			return tools.ToolResponse{}, fmt.Errorf("mcp: list resource templates from %q: %w", server.name, err)
		}
		templates = append(templates, template)
	}
	return jsonResponse(templates)
}

func readResource(ctx context.Context, server *serverSession, raw json.RawMessage) (tools.ToolResponse, error) {
	var args struct {
		URI string `json:"uri"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return tools.ToolResponse{}, fmt.Errorf("mcp: decode resource URI: %w", err)
	}
	if strings.TrimSpace(args.URI) == "" {
		return tools.ToolResponse{}, errors.New("mcp: resource URI is required")
	}
	result, err := server.session.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: args.URI})
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("mcp: read resource from %q: %w", server.name, err)
	}
	rawResult, err := json.Marshal(result)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("mcp: encode resource result: %w", err)
	}
	response := tools.ToolResponse{Details: rawResult}
	for _, content := range result.Contents {
		response.Content = append(response.Content, resourceContentBlocks(content)...)
	}
	return response, nil
}

func listPrompts(ctx context.Context, server *serverSession) (tools.ToolResponse, error) {
	prompts := make([]*mcpsdk.Prompt, 0)
	for prompt, err := range server.session.Prompts(ctx, nil) {
		if err != nil {
			return tools.ToolResponse{}, fmt.Errorf("mcp: list prompts from %q: %w", server.name, err)
		}
		prompts = append(prompts, prompt)
	}
	return jsonResponse(prompts)
}

func getPrompt(ctx context.Context, server *serverSession, raw json.RawMessage) (tools.ToolResponse, error) {
	var args struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments,omitempty"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return tools.ToolResponse{}, fmt.Errorf("mcp: decode prompt arguments: %w", err)
	}
	if strings.TrimSpace(args.Name) == "" {
		return tools.ToolResponse{}, errors.New("mcp: prompt name is required")
	}
	result, err := server.session.GetPrompt(ctx, &mcpsdk.GetPromptParams{Name: args.Name, Arguments: args.Arguments})
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("mcp: get prompt from %q: %w", server.name, err)
	}
	rawResult, err := json.Marshal(result)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("mcp: encode prompt result: %w", err)
	}
	response := tools.ToolResponse{Details: rawResult}
	for _, message := range result.Messages {
		if message == nil {
			continue
		}
		blocks := contentBlocks(message.Content)
		if len(blocks) == 0 {
			continue
		}
		role := string(message.Role)
		if blocks[0].Type == tools.ContentText {
			blocks[0].Text = "[" + role + "] " + blocks[0].Text
		} else {
			response.Content = append(response.Content, tools.ContentBlock{Type: tools.ContentText, Text: "[" + role + "]"})
		}
		response.Content = append(response.Content, blocks...)
	}
	return response, nil
}

func toolResponse(result *mcpsdk.CallToolResult) (tools.ToolResponse, error) {
	if result == nil {
		return tools.ToolResponse{}, errors.New("mcp: server returned an empty tool result")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("mcp: encode tool result: %w", err)
	}
	response := tools.ToolResponse{IsError: result.IsError, Details: raw}
	for _, content := range result.Content {
		response.Content = append(response.Content, contentBlocks(content)...)
	}
	if len(response.Content) == 0 && result.StructuredContent != nil {
		structured, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return tools.ToolResponse{}, fmt.Errorf("mcp: encode structured tool result: %w", err)
		}
		response.Content = append(response.Content, tools.ContentBlock{Type: tools.ContentText, Text: string(structured)})
	}
	return response, nil
}

func contentBlocks(content mcpsdk.Content) []tools.ContentBlock {
	if content == nil {
		return nil
	}
	switch value := content.(type) {
	case *mcpsdk.TextContent:
		return []tools.ContentBlock{{Type: tools.ContentText, Text: value.Text}}
	case *mcpsdk.ImageContent:
		return []tools.ContentBlock{{Type: tools.ContentImage, ImageData: append([]byte(nil), value.Data...), ImageMIMEType: value.MIMEType}}
	case *mcpsdk.EmbeddedResource:
		return resourceContentBlocks(value.Resource)
	default:
		raw, err := content.MarshalJSON()
		if err != nil {
			return []tools.ContentBlock{{Type: tools.ContentText, Text: fmt.Sprintf("MCP content could not be encoded: %v", err)}}
		}
		return []tools.ContentBlock{{Type: tools.ContentText, Text: string(raw)}}
	}
}

func resourceContentBlocks(content *mcpsdk.ResourceContents) []tools.ContentBlock {
	if content == nil {
		return nil
	}
	if content.Blob != nil {
		if strings.HasPrefix(strings.ToLower(content.MIMEType), "image/") {
			return []tools.ContentBlock{{Type: tools.ContentImage, ImageData: append([]byte(nil), content.Blob...), ImageMIMEType: content.MIMEType}}
		}
		encoded := base64.StdEncoding.EncodeToString(content.Blob)
		return []tools.ContentBlock{{Type: tools.ContentText, Text: fmt.Sprintf("Resource %s (%s), base64: %s", content.URI, content.MIMEType, encoded)}}
	}
	return []tools.ContentBlock{{Type: tools.ContentText, Text: content.Text}}
}

func jsonResponse(value any) (tools.ToolResponse, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return tools.ToolResponse{}, fmt.Errorf("mcp: encode result: %w", err)
	}
	return tools.ToolResponse{
		Content: []tools.ContentBlock{{Type: tools.ContentText, Text: string(raw)}},
		Details: raw,
	}, nil
}

func decodeObject(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	var value map[string]any
	if err := decodeArgs(raw, &value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("arguments must be a JSON object")
	}
	return value, nil
}

func decodeArgs(raw json.RawMessage, destination any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("arguments are required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("arguments contain more than one JSON value")
		}
		return fmt.Errorf("arguments have trailing data: %w", err)
	}
	return nil
}
