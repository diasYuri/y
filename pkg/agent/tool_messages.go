package agent

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/yuri/y/pkg/ai"
	"github.com/yuri/y/pkg/tools"
)

func toolResultMessage(call ai.ToolCall, response tools.ToolResponse, err error) ai.Message {
	result := ai.ToolResult{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		Content:    make([]ai.ContentBlock, 0, len(response.Content)),
		IsError:    response.IsError || err != nil,
		Details:    append([]byte(nil), response.Details...),
		Metadata:   append([]byte(nil), response.Metadata...),
	}
	for _, log := range response.Logs {
		result.Logs = append(result.Logs, ai.LogEntry{Timestamp: log.Timestamp, Level: log.Level, Message: log.Message, Details: append([]byte(nil), log.Details...)})
	}
	for _, block := range response.Content {
		result.Content = append(result.Content, ai.ContentBlock{
			Type:             ai.ContentType(block.Type),
			Text:             block.Text,
			ImageData:        append([]byte(nil), block.ImageData...),
			ImageMIMEType:    block.ImageMIMEType,
			Details:          append([]byte(nil), block.Details...),
			ProviderMetadata: append([]byte(nil), block.ProviderMetadata...),
		})
	}

	if err != nil {
		result.Content = errorContent(err)
		if details := toolErrorDetails(err); len(details) > 0 {
			result.Details = details
		}
	} else if len(result.Content) == 0 {
		result.Content = []ai.ContentBlock{{Type: ai.ContentText, Text: ""}}
	}

	return ai.Message{
		Role:       ai.RoleToolResult,
		ToolResult: &result,
		Timestamp:  time.Now().UTC(),
	}
}

type toolErrorEnvelope struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func toolErrorDetails(err error) []byte {
	var toolErr *tools.Error
	if errors.As(err, &toolErr) {
		raw, marshalErr := json.Marshal(toolErrorEnvelope{Code: toolErr.Code, Message: toolErr.Error()})
		if marshalErr == nil {
			return raw
		}
	}
	raw, marshalErr := json.Marshal(toolErrorEnvelope{Message: err.Error()})
	if marshalErr != nil {
		return []byte(`{"message":"unknown tool error"}`)
	}
	return raw
}

func errorContent(err error) []ai.ContentBlock {
	if err == nil {
		return nil
	}
	return []ai.ContentBlock{{Type: ai.ContentText, Text: err.Error()}}
}
