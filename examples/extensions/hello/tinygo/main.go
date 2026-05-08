//go:build tinygo.wasm

// Package main is the TinyGo entry point for the Hello example extension.
//
// It is compiled to WebAssembly with `tinygo build -target=wasi`. The
// regular `go build` toolchain ignores this file because of the
// tinygo.wasm build tag — only TinyGo defines that tag.
//
// The implementation deliberately uses encoding/json to keep the example
// approachable. Production extensions are free to swap in a smaller
// allocator or hand-rolled encoder; the host only cares about the JSON on
// the wire.
package main

import (
	"encoding/json"
	"strings"
	"unsafe"
)

// envelope mirrors the wasm.Envelope JSON shape from the host. It is kept
// minimal because a TinyGo extension cannot depend on the host's Go
// packages.
type envelope struct {
	APIVersion  string          `json:"api_version"`
	RequestID   string          `json:"request_id,omitempty"`
	ExtensionID string          `json:"extension_id,omitempty"`
	Kind        string          `json:"kind"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

type response struct {
	RequestID string          `json:"request_id,omitempty"`
	OK        bool            `json:"ok"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     *errorObject    `json:"error,omitempty"`
}

type errorObject struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type initResponse struct {
	Tools []toolDescriptor `json:"tools,omitempty"`
}

type toolDescriptor struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type toolCallRequest struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type toolCallResponse struct {
	Content []contentBlock `json:"content,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type sayArgs struct {
	Name string `json:"name"`
}

func main() {}

//export pi_extension_abi_version
func ABIVersion() uint32 { return 1 }

//export pi_extension_init
func Init(ptr, length uint32) uint64 {
	_ = readBuffer(ptr, length)
	body, err := json.Marshal(initResponse{
		Tools: []toolDescriptor{
			{Name: "hello_say", Description: "Returns a greeting for the supplied name."},
		},
	})
	if err != nil {
		return marshalResponse(response{OK: false, Error: &errorObject{Code: "internal", Message: err.Error()}})
	}
	return marshalResponse(response{OK: true, Payload: body})
}

//export pi_extension_handle
func Handle(ptr, length uint32) uint64 {
	raw := readBuffer(ptr, length)
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return marshalResponse(response{OK: false, Error: &errorObject{Code: "invalid_argument", Message: err.Error()}})
	}
	if env.Kind != "tool_call" {
		return marshalResponse(response{
			RequestID: env.RequestID,
			OK:        false,
			Error:     &errorObject{Code: "unsupported_host_op", Message: "unsupported envelope kind " + env.Kind},
		})
	}
	var call toolCallRequest
	if err := json.Unmarshal(env.Payload, &call); err != nil {
		return marshalResponse(response{
			RequestID: env.RequestID,
			OK:        false,
			Error:     &errorObject{Code: "invalid_argument", Message: err.Error()},
		})
	}
	if call.Tool != "hello_say" {
		return marshalResponse(response{
			RequestID: env.RequestID,
			OK:        false,
			Error:     &errorObject{Code: "tool_not_found", Message: call.Tool},
		})
	}
	var args sayArgs
	if len(call.Arguments) > 0 {
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return marshalResponse(response{
				RequestID: env.RequestID,
				OK:        false,
				Error:     &errorObject{Code: "invalid_argument", Message: err.Error()},
			})
		}
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		name = "world"
	}
	body, err := json.Marshal(toolCallResponse{
		Content: []contentBlock{{Type: "text", Text: "hello, " + name + "!"}},
	})
	if err != nil {
		return marshalResponse(response{
			RequestID: env.RequestID,
			OK:        false,
			Error:     &errorObject{Code: "internal", Message: err.Error()},
		})
	}
	return marshalResponse(response{RequestID: env.RequestID, OK: true, Payload: body})
}

//export pi_extension_shutdown
func Shutdown(ptr, length uint32) uint64 {
	_ = readBuffer(ptr, length)
	return marshalResponse(response{OK: true})
}

//export pi_extension_free
func Free(ptr, length uint32) {
	if ptr == 0 || length == 0 {
		return
	}
	// TinyGo's runtime owns the memory; nothing to do beyond ignoring the
	// hint. Production extensions that allocate manually should release
	// the buffer here.
}

// readBuffer copies a guest memory slice into a Go slice safely.
func readBuffer(ptr, length uint32) []byte {
	if ptr == 0 || length == 0 {
		return nil
	}
	p := unsafe.Pointer(uintptr(ptr))
	src := unsafe.Slice((*byte)(p), int(length))
	out := make([]byte, len(src))
	copy(out, src)
	return out
}

// marshalResponse encodes the response and returns the (ptr, len) pair the
// host expects. The buffer is intentionally leaked: pi_extension_free is
// invoked by the host with the same pointer and length so TinyGo's GC
// reclaims it on the next collection cycle.
func marshalResponse(r response) uint64 {
	body, err := json.Marshal(r)
	if err != nil {
		return 0
	}
	if len(body) == 0 {
		return 0
	}
	ptr := uint32(uintptr(unsafe.Pointer(&body[0])))
	return (uint64(ptr) << 32) | uint64(uint32(len(body)))
}
