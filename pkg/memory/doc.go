// Package memory defines the transport-neutral contracts and data validation
// for y's persistent-memory extension. Operational implementations belong to
// an extension or deployment adapter, not to this SDK package.
//
// Memory is historical, user-controlled data. It is deliberately separate
// from agent instructions, the active transcript, and compaction summaries.
package memory
