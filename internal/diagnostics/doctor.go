package diagnostics

import (
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/yuri/y/internal/buildinfo"
	"github.com/yuri/y/internal/feature"
)

// DoctorReport is the stable JSON schema returned by `y doctor --json`.
type DoctorReport struct {
	Status       string            `json:"status"`
	Build        buildinfo.Info    `json:"build"`
	Runtime      RuntimeReport     `json:"runtime"`
	Capabilities CapabilityReport  `json:"capabilities"`
	Checks       []DiagnosticCheck `json:"checks"`
}

// RuntimeReport describes the running binary platform.
type RuntimeReport struct {
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	Compiler  string `json:"compiler"`
}

// CapabilityReport summarizes the compiled registry without exposing config.
type CapabilityReport struct {
	CompiledCount int      `json:"compiled_count"`
	KnownCount    int      `json:"known_count"`
	Compiled      []string `json:"compiled"`
}

// DiagnosticCheck is a single doctor check result.
type DiagnosticCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Doctor builds a deterministic diagnostics report from local process state.
func Doctor(info buildinfo.Info, compiled *feature.Registry) DoctorReport {
	if info.Tags == nil {
		info.Tags = []string{}
	}

	compiledNames := compiledCapabilities(compiled)
	checks := []DiagnosticCheck{
		{
			ID:      "compiled_features_registered",
			Status:  "ok",
			Message: "compiled feature registry loaded",
		},
		{
			ID:      "runtime_without_node",
			Status:  "ok",
			Message: "main runtime is a Go binary and does not require Node or Bun",
		},
		offlineCheck(),
		telemetryCheck(),
		providerConnectivityCheck(),
		optionalFeaturesCheck(compiled),
	}
	return DoctorReport{
		Status: "ok",
		Build:  info,
		Runtime: RuntimeReport{
			GoVersion: runtime.Version(),
			GOOS:      runtime.GOOS,
			GOARCH:    runtime.GOARCH,
			Compiler:  runtime.Compiler,
		},
		Capabilities: CapabilityReport{
			CompiledCount: len(compiledNames),
			KnownCount:    len(feature.Known()),
			Compiled:      compiledNames,
		},
		Checks: checks,
	}
}

func compiledCapabilities(compiled *feature.Registry) []string {
	if compiled == nil {
		return nil
	}
	descs := compiled.Compiled()
	out := make([]string, 0, len(descs))
	for _, desc := range descs {
		out = append(out, string(desc.Kind)+":"+desc.ID)
	}
	sort.Strings(out)
	return out
}

func offlineCheck() DiagnosticCheck {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("Y_OFFLINE")))
	switch v {
	case "1", "true", "yes", "on":
		return DiagnosticCheck{ID: "offline_mode", Status: "ok", Message: "offline mode is enabled (Y_OFFLINE=" + v + ")"}
	case "0", "false", "no", "off", "":
		return DiagnosticCheck{ID: "offline_mode", Status: "ok", Message: "offline mode is disabled"}
	default:
		return DiagnosticCheck{ID: "offline_mode", Status: "warn", Message: "Y_OFFLINE has unrecognized value: " + v}
	}
}

func providerConnectivityCheck() DiagnosticCheck {
	var found []string
	var missing []string

	// Anthropic: check API key or OAuth token.
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		if len(key) > 20 {
			found = append(found, "anthropic")
		} else {
			found = append(found, "anthropic (key looks short)")
		}
	} else if os.Getenv("ANTHROPIC_OAUTH_TOKEN") != "" {
		found = append(found, "anthropic (oauth)")
	} else {
		missing = append(missing, "anthropic")
	}

	// OpenAI.
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		if strings.HasPrefix(key, "sk-") {
			found = append(found, "openai")
		} else {
			found = append(found, "openai (key format unrecognized)")
		}
	} else {
		missing = append(missing, "openai")
	}

	// Google.
	if key := os.Getenv("GOOGLE_API_KEY"); key != "" {
		found = append(found, "google")
	} else {
		missing = append(missing, "google")
	}

	// Local / OpenAI-compatible.
	if os.Getenv("OPENAI_COMPATIBLE_API_KEY") != "" || os.Getenv("Y_OPENAI_COMPATIBLE_API_KEY") != "" {
		found = append(found, "local")
	} else {
		missing = append(missing, "local")
	}

	if len(found) == 0 {
		return DiagnosticCheck{
			ID:      "provider_connectivity",
			Status:  "warn",
			Message: "no provider API keys configured (ANTHROPIC_API_KEY, ANTHROPIC_OAUTH_TOKEN, OPENAI_API_KEY, GOOGLE_API_KEY, OPENAI_COMPATIBLE_API_KEY)",
		}
	}
	msg := "configured: " + strings.Join(found, ", ")
	if len(missing) > 0 {
		msg += "; missing: " + strings.Join(missing, ", ")
	}
	return DiagnosticCheck{
		ID:      "provider_connectivity",
		Status:  "ok",
		Message: msg,
	}
}

func optionalFeaturesCheck(compiled *feature.Registry) DiagnosticCheck {
	if compiled == nil {
		return DiagnosticCheck{ID: "optional_features", Status: "ok", Message: "feature registry not available"}
	}

	optional := []string{"rpc", "lsp", "shell", "wasm_extensions"}
	var compiledList []string
	var missingList []string

	for _, id := range optional {
		if compiled.Has(feature.KindFeature, id) {
			compiledList = append(compiledList, id)
		} else {
			missingList = append(missingList, id)
		}
	}

	msg := "compiled: " + strings.Join(compiledList, ", ")
	if len(missingList) > 0 {
		msg += "; not compiled: " + strings.Join(missingList, ", ")
	}
	return DiagnosticCheck{
		ID:      "optional_features",
		Status:  "ok",
		Message: msg,
	}
}

func telemetryCheck() DiagnosticCheck {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("Y_TELEMETRY")))
	switch v {
	case "1", "true", "yes", "on":
		endpoint := os.Getenv("Y_TELEMETRY_ENDPOINT")
		msg := "telemetry is enabled (Y_TELEMETRY=" + v + ")"
		if endpoint == "" {
			msg += ", no endpoint configured"
		}
		return DiagnosticCheck{ID: "telemetry", Status: "ok", Message: msg}
	case "0", "false", "no", "off", "":
		return DiagnosticCheck{ID: "telemetry", Status: "ok", Message: "telemetry is disabled"}
	default:
		return DiagnosticCheck{ID: "telemetry", Status: "warn", Message: "Y_TELEMETRY has unrecognized value: " + v}
	}
}
