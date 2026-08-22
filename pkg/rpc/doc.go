// Package rpc provides a JSON-RPC server for headless programmatic access to y.
//
// It exposes the agent loop over HTTP so editors, scripts, and other tools can
// send prompts and receive streaming responses without the TUI.
//
// The server speaks JSON-RPC 2.0 over HTTP POST. Streaming responses are
// delivered via a Server-Sent Events endpoint.
//
// Builds without feature_rpc expose the same API and return ErrUnavailable
// from server operations.
package rpc
