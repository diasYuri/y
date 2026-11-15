# Changelog

## [1.2.0] - 2026-11-15

### Added

- Add public component contracts, scoped ownership, transactional activation, versioned service dependencies, and runtime generation snapshots.
- Add a public harness API and configurable component composition shared by headless and RPC runtimes.
- Integrate WASM extensions with scoped tools, lifecycle hooks, JSON services, schema validation, explicit capability grants, and harness policy.
- Document component contracts, durable-state boundaries, runtime composition, and WASM configuration, with a public SDK example.

### Fixed

- Drain in-flight operations before disposing component generations and WASM modules, preserving tools advertised to each agent turn.
- Enforce independent WASM memory limits, queued-call cancellation, runtime compatibility checks, and explicit rejection of unsupported fuel metering.
- Improve cleanup, persistence readiness reporting, and lint compliance across runtime adapters.
- Install the lint tool before release validation.

## [1.1.0] - 2026-10-17

### Added

- Add an MCP extension for connecting to remote servers over stdio and Streamable HTTP, exposing their tools, resources, and prompts through Y.
- Add code investigation tools for code search, Git history and navigation, and language server capabilities. See [the code navigation guide](docs/code-navigation.md).

### Improved

- Expand tool metadata and support explicit approval overrides for sensitive tools.
- Clean up runtime and language server resources during shutdown, and improve coding quickstart support for investigation workflows.
