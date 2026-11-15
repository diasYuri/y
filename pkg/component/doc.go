// Package component defines the public, harness-neutral component contract
// used to compose Y applications.
//
// A component declares the services it requires and provides before its
// factory runs. Runtime validates the complete composition, starts components
// in dependency order, and invokes their tracked cleanups in reverse order.
// The package deliberately does not depend on agent, tools, provider, or
// application packages.
package component
