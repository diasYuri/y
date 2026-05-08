package diagnostics

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yuri/y/internal/buildinfo"
	"github.com/yuri/y/internal/feature"
)

func TestDoctorReportJSONShapeIsStable(t *testing.T) {
	reg := feature.NewRegistry()
	if err := feature.RegisterCompiledFeatures(reg); err != nil {
		t.Fatalf("RegisterCompiledFeatures returned error: %v", err)
	}

	report := Doctor(buildinfo.Info{Version: "test", Commit: "abc", Date: "2026-01-02"}, reg)
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}

	var decoded struct {
		Status string `json:"status"`
		Build  struct {
			Version string   `json:"version"`
			Commit  string   `json:"commit"`
			Date    string   `json:"date"`
			Tags    []string `json:"tags"`
		} `json:"build"`
		Runtime struct {
			GoVersion string `json:"go_version"`
			GOOS      string `json:"goos"`
			GOARCH    string `json:"goarch"`
			Compiler  string `json:"compiler"`
		} `json:"runtime"`
		Capabilities struct {
			CompiledCount int      `json:"compiled_count"`
			KnownCount    int      `json:"known_count"`
			Compiled      []string `json:"compiled"`
		} `json:"capabilities"`
		Checks []DiagnosticCheck `json:"checks"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if decoded.Status != "ok" {
		t.Fatalf("status = %q, want ok", decoded.Status)
	}
	if decoded.Build.Version != "test" || decoded.Build.Commit != "abc" || decoded.Build.Date != "2026-01-02" {
		t.Fatalf("unexpected build block: %#v", decoded.Build)
	}
	if decoded.Build.Tags == nil {
		t.Fatalf("build tags decoded as nil, want stable empty array")
	}
	if decoded.Runtime.GoVersion == "" || decoded.Runtime.GOOS == "" || decoded.Runtime.GOARCH == "" || decoded.Runtime.Compiler == "" {
		t.Fatalf("runtime block has empty fields: %#v", decoded.Runtime)
	}
	if decoded.Capabilities.CompiledCount != len(decoded.Capabilities.Compiled) {
		t.Fatalf("compiled_count = %d, compiled len = %d", decoded.Capabilities.CompiledCount, len(decoded.Capabilities.Compiled))
	}
	if len(decoded.Checks) == 0 {
		t.Fatalf("checks empty")
	}

	// Verify expected check IDs exist.
	expectedIDs := map[string]bool{
		"compiled_features_registered": false,
		"runtime_without_node":         false,
		"offline_mode":                 false,
		"telemetry":                    false,
		"provider_connectivity":        false,
		"optional_features":            false,
	}
	for _, check := range decoded.Checks {
		if _, ok := expectedIDs[check.ID]; ok {
			expectedIDs[check.ID] = true
		}
	}
	for id, found := range expectedIDs {
		if !found {
			t.Fatalf("expected check %q not found", id)
		}
	}
}

func TestProviderConnectivityCheck(t *testing.T) {
	check := providerConnectivityCheck()
	// Status depends on environment variables; just verify it returns something.
	if check.ID != "provider_connectivity" {
		t.Fatalf("id = %q, want provider_connectivity", check.ID)
	}
	if check.Status != "ok" && check.Status != "warn" {
		t.Fatalf("status = %q, want ok or warn", check.Status)
	}
	if !strings.Contains(check.Message, "configured:") && !strings.Contains(check.Message, "no provider API keys") {
		t.Fatalf("message = %q, expected provider info", check.Message)
	}
}

func TestOptionalFeaturesCheck(t *testing.T) {
	reg := feature.NewRegistry()
	_ = feature.RegisterCompiledFeatures(reg)
	check := optionalFeaturesCheck(reg)
	if check.Status != "ok" {
		t.Fatalf("status = %q, want ok", check.Status)
	}
	if !strings.Contains(check.Message, "compiled:") {
		t.Fatalf("message = %q, want 'compiled:'", check.Message)
	}
}

func TestOptionalFeaturesCheckNilRegistry(t *testing.T) {
	check := optionalFeaturesCheck(nil)
	if check.Status != "ok" {
		t.Fatalf("status = %q, want ok", check.Status)
	}
	if !strings.Contains(check.Message, "not available") {
		t.Fatalf("message = %q, want 'not available'", check.Message)
	}
}
