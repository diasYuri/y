package wasm

import "time"

// Limits captures the runtime quota that the host enforces on a single
// extension call. Values of zero pick up the host defaults defined on the
// Manager Config.
type Limits struct {
	// TimeoutMS bounds wall-clock execution time. The host cancels the
	// guest call when the timeout fires; trapping at that point becomes a
	// regular ExtensionError instead of a process-level panic.
	TimeoutMS uint32
	// MemoryPages caps the maximum number of 64 KiB pages the guest may
	// allocate. The runtime is configured with this value so out-of-memory
	// situations are turned into traps the host can recover from.
	MemoryPages uint32
	// MaxInputBytes bounds the JSON envelope sent to the guest.
	MaxInputBytes uint32
	// MaxOutputBytes bounds the JSON envelope returned by the guest. The
	// host stops reading after this many bytes and reports a structured
	// error.
	MaxOutputBytes uint32
	// MaxLogBytes caps the cumulative bytes accepted from pi_host_log.
	MaxLogBytes uint32
	// MaxHostCalls bounds how many pi_host_call invocations a single tool
	// call may issue.
	MaxHostCalls uint32
	// Fuel is the optional WebAssembly fuel ceiling. Zero disables fuel
	// metering. wazero does not yet expose first-class fuel support, so
	// the host enforces it by counting host calls/elapsed time. The field
	// is preserved for future engines.
	Fuel uint64
}

// DefaultLimits returns the host-side fallbacks. They are conservative
// because every guest is treated as untrusted by default.
func DefaultLimits() Limits {
	return Limits{
		TimeoutMS:      5000,
		MemoryPages:    256,
		MaxInputBytes:  1 << 20,
		MaxOutputBytes: 1 << 20,
		MaxLogBytes:    64 * 1024,
		MaxHostCalls:   128,
	}
}

// Apply overlays the receiver with the supplied non-zero overrides and
// returns the merged limits.
func (l Limits) Apply(over Limits) Limits {
	out := l
	if over.TimeoutMS != 0 {
		out.TimeoutMS = over.TimeoutMS
	}
	if over.MemoryPages != 0 {
		out.MemoryPages = over.MemoryPages
	}
	if over.MaxInputBytes != 0 {
		out.MaxInputBytes = over.MaxInputBytes
	}
	if over.MaxOutputBytes != 0 {
		out.MaxOutputBytes = over.MaxOutputBytes
	}
	if over.MaxLogBytes != 0 {
		out.MaxLogBytes = over.MaxLogBytes
	}
	if over.MaxHostCalls != 0 {
		out.MaxHostCalls = over.MaxHostCalls
	}
	if over.Fuel != 0 {
		out.Fuel = over.Fuel
	}
	return out
}

// Timeout returns the TimeoutMS as a Duration with a sane minimum so a
// caller cannot accidentally zero out the budget.
func (l Limits) Timeout() time.Duration {
	if l.TimeoutMS == 0 {
		return 5 * time.Second
	}
	return time.Duration(l.TimeoutMS) * time.Millisecond
}

// Snapshot returns a LimitsSnapshot suitable for the init envelope sent to
// the guest.
func (l Limits) Snapshot() LimitsSnapshot {
	return LimitsSnapshot{
		MaxInputBytes:  l.MaxInputBytes,
		MaxOutputBytes: l.MaxOutputBytes,
		TimeoutMS:      l.TimeoutMS,
	}
}

// limitForManifest merges manifest hints with the host defaults.
func limitForManifest(host Limits, m Manifest) Limits {
	out := host
	if v := m.Runtime.MemoryPages; v != 0 {
		out.MemoryPages = v
	}
	if v := m.Runtime.TimeoutMS; v != 0 {
		out.TimeoutMS = v
	}
	if v := m.Runtime.Fuel; v != 0 {
		out.Fuel = v
	}
	return out
}
