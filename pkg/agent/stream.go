package agent

import (
	"bytes"
	"errors"
	"sort"
	"time"

	"github.com/diasYuri/y/pkg/ai"
)

type assistantBuilder struct {
	text      bytes.Buffer
	thinking  bytes.Buffer
	signature string
	content   []ai.ContentBlock
	toolCalls map[int]*pendingToolCall
}

type pendingToolCall struct {
	call      ai.ToolCall
	arguments bytes.Buffer
	seenDelta bool
	complete  bool
}

func newAssistantBuilder() *assistantBuilder {
	return &assistantBuilder{toolCalls: make(map[int]*pendingToolCall)}
}

func (b *assistantBuilder) addText(text string) {
	if text != "" {
		b.text.WriteString(text)
	}
}

func (b *assistantBuilder) addThinking(thinking, signature string) {
	if thinking != "" {
		b.thinking.WriteString(thinking)
	}
	if signature != "" {
		b.signature = signature
	}
}

func (b *assistantBuilder) addImage(event ai.ImageEvent) {
	b.content = append(b.content, ai.ContentBlock{
		Type:             ai.ContentImage,
		ImageData:        append([]byte(nil), event.Data...),
		ImageMIMEType:    event.MIMEType,
		ProviderMetadata: append([]byte(nil), event.ProviderMetadata...),
	})
}

func (b *assistantBuilder) addToolCall(event ai.ToolCallEvent) {
	call := b.toolCalls[event.ContentIndex]
	if call == nil {
		call = &pendingToolCall{}
		b.toolCalls[event.ContentIndex] = call
	}
	if event.ToolCall.ID != "" {
		call.call.ID = event.ToolCall.ID
	}
	if event.ToolCall.Name != "" {
		call.call.Name = event.ToolCall.Name
	}
	if event.ToolCall.ThoughtSignature != "" {
		call.call.ThoughtSignature = event.ToolCall.ThoughtSignature
	}
	if len(event.ToolCall.Details) > 0 {
		call.call.Details = append([]byte(nil), event.ToolCall.Details...)
	}
	if len(event.ToolCall.Arguments) > 0 {
		call.call.Arguments = append([]byte(nil), event.ToolCall.Arguments...)
	}
	if len(event.ArgumentsDelta) > 0 {
		_, _ = call.arguments.Write(event.ArgumentsDelta)
		call.seenDelta = true
	}
	if event.Complete {
		call.complete = true
		if len(call.call.Arguments) == 0 && call.seenDelta {
			call.call.Arguments = append([]byte(nil), call.arguments.Bytes()...)
		}
	}
}

func (b *assistantBuilder) build() (ai.Message, error) {
	toolCalls := make([]ai.ToolCall, 0, len(b.toolCalls))
	indexes := make([]int, 0, len(b.toolCalls))
	for index := range b.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		call, err := b.toolCalls[index].finalize()
		if err != nil {
			return ai.Message{}, err
		}
		toolCalls = append(toolCalls, call)
	}

	content := make([]ai.ContentBlock, 0, 2+len(b.content))
	content = append(content, b.content...)
	if thinking := b.thinking.String(); thinking != "" || b.signature != "" {
		content = append(content, ai.ContentBlock{Type: ai.ContentThinking, Thinking: thinking, Signature: b.signature})
	}
	if text := b.text.String(); text != "" {
		content = append(content, ai.ContentBlock{Type: ai.ContentText, Text: text})
	}
	return ai.Message{
		Role:      ai.RoleAssistant,
		Content:   content,
		ToolCalls: toolCalls,
		Timestamp: time.Now().UTC(),
	}, nil
}

func (p *pendingToolCall) finalize() (ai.ToolCall, error) {
	if !p.complete && p.seenDelta {
		return ai.ToolCall{}, errors.New("incomplete tool call stream")
	}
	if p.call.Name == "" {
		return ai.ToolCall{}, errors.New("tool call missing name")
	}
	if len(p.call.Arguments) == 0 && p.seenDelta {
		p.call.Arguments = append([]byte(nil), p.arguments.Bytes()...)
	}
	if len(p.call.Arguments) == 0 && !p.complete {
		return ai.ToolCall{}, errors.New("tool call missing complete event")
	}
	return p.call, nil
}
