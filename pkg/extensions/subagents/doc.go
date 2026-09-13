// Package subagents provides a native subagent manager for y agents.
//
// A Manager creates isolated child agent sessions, bounds their execution
// with a fixed worker pool, and exposes lifecycle tools for the parent agent.
// Ephemeral children run in memory with agent.Agent. Durable children use an
// agent.AgentRunner supplied by the host so state, events, leases, and
// recovery remain owned by the application's persistence configuration.
package subagents
