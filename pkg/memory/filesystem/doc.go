// Package filesystem provides a reusable durable filesystem memory.Store.
// The caller injects its root; it never reads application environment variables.
// Closing a component that uses the store does not delete durable memories.
package filesystem
