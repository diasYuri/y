package feature

import (
	"strings"
	"testing"
)

func TestRegisterCompiledFeaturesIncludesCoreCommands(t *testing.T) {
	reg := NewRegistry()
	if err := RegisterCompiledFeatures(reg); err != nil {
		t.Fatalf("RegisterCompiledFeatures returned error: %v", err)
	}

	if !reg.Has(KindCommand, "features") {
		t.Fatalf("compiled registry missing features command")
	}
	if !reg.Has(KindCommand, "config.validate") {
		t.Fatalf("compiled registry missing config.validate command")
	}
	if !reg.Has(KindCommand, "session.list") || !reg.Has(KindCommand, "session.show") {
		t.Fatalf("compiled registry missing session commands")
	}
}

func TestRegistryRejectsDuplicates(t *testing.T) {
	reg := NewRegistry()
	if err := reg.AddCommand("features", "List features."); err != nil {
		t.Fatalf("AddCommand returned error: %v", err)
	}
	if err := reg.AddCommand("features", "List features."); err == nil {
		t.Fatalf("duplicate AddCommand returned nil error")
	}
}

func TestKnownCapabilitiesAreStable(t *testing.T) {
	known := Known()
	if len(known) == 0 {
		t.Fatalf("Known returned no capabilities")
	}
	for i := 1; i < len(known); i++ {
		prev := known[i-1]
		cur := known[i]
		if prev.Kind > cur.Kind || prev.Kind == cur.Kind && prev.ID > cur.ID {
			t.Fatalf("Known is not sorted at %d: %#v before %#v", i, prev, cur)
		}
	}
}

func TestNewRegistryEmpty(t *testing.T) {
	reg := NewRegistry()
	if reg == nil {
		t.Fatal("NewRegistry returned nil")
	}
	if len(reg.Compiled()) != 0 {
		t.Fatalf("new registry has compiled entries: %v", reg.Compiled())
	}
}

func TestAddFeatureProviderTool(t *testing.T) {
	reg := NewRegistry()
	if err := reg.AddFeature("rpc", "feature_rpc", "desc"); err != nil {
		t.Fatalf("AddFeature: %v", err)
	}
	if err := reg.AddProvider("anthropic", "feature_anthropic", "desc"); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	if err := reg.AddTool("edit", "feature_fs", "desc"); err != nil {
		t.Fatalf("AddTool: %v", err)
	}
	if !reg.Has(KindFeature, "rpc") {
		t.Fatal("Has(feature, rpc) = false")
	}
	if !reg.Has(KindProvider, "anthropic") {
		t.Fatal("Has(provider, anthropic) = false")
	}
	if !reg.Has(KindTool, "edit") {
		t.Fatal("Has(tool, edit) = false")
	}
}

func TestAddCommand(t *testing.T) {
	reg := NewRegistry()
	if err := reg.AddCommand("chat", "Chat command."); err != nil {
		t.Fatalf("AddCommand: %v", err)
	}
	if !reg.Has(KindCommand, "chat") {
		t.Fatal("Has(command, chat) = false")
	}
}

func TestAddEmptyID(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Add(Descriptor{Kind: KindFeature, ID: ""}); err == nil {
		t.Fatal("Add with empty ID returned nil error")
	}
}

func TestAddEmptyKind(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Add(Descriptor{ID: "x"}); err == nil {
		t.Fatal("Add with empty Kind returned nil error")
	}
}

func TestAddUnknownCapability(t *testing.T) {
	reg := NewRegistry()
	err := reg.Add(Descriptor{Kind: KindFeature, ID: "nonexistent"})
	if err == nil {
		t.Fatal("Add with unknown ID returned nil error")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("error = %q, want containing 'unknown'", err.Error())
	}
}

func TestHasNilRegistry(t *testing.T) {
	var reg *Registry
	if reg.Has(KindFeature, "anything") {
		t.Fatal("Has on nil registry = true")
	}
}

func TestHasNilCapabilities(t *testing.T) {
	reg := &Registry{}
	if reg.Has(KindFeature, "anything") {
		t.Fatal("Has on nil capabilities = true")
	}
}

func TestHasMissing(t *testing.T) {
	reg := NewRegistry()
	_ = reg.AddCommand("features", "desc")
	if reg.Has(KindCommand, "missing") {
		t.Fatal("Has for missing command = true")
	}
	if reg.Has(KindFeature, "features") {
		t.Fatal("Has for wrong kind = true")
	}
}

func TestCompiledNilRegistry(t *testing.T) {
	var reg *Registry
	if reg.Compiled() != nil {
		t.Fatal("Compiled on nil registry != nil")
	}
}

func TestCompiledEmpty(t *testing.T) {
	reg := NewRegistry()
	if len(reg.Compiled()) != 0 {
		t.Fatalf("Compiled on empty registry len = %d", len(reg.Compiled()))
	}
}

func TestCompiledSorting(t *testing.T) {
	reg := NewRegistry()
	_ = reg.AddCommand("run", "r")
	_ = reg.AddCommand("chat", "c")
	_ = reg.AddFeature("rpc", "feature_rpc", "r")

	compiled := reg.Compiled()
	if len(compiled) != 3 {
		t.Fatalf("len(compiled) = %d, want 3", len(compiled))
	}
	// Should be sorted by Kind then ID.
	if compiled[0].ID != "chat" {
		t.Fatalf("compiled[0].ID = %q, want chat", compiled[0].ID)
	}
	if compiled[1].ID != "run" {
		t.Fatalf("compiled[1].ID = %q, want run", compiled[1].ID)
	}
	if compiled[2].ID != "rpc" {
		t.Fatalf("compiled[2].ID = %q, want rpc", compiled[2].ID)
	}
}

func TestStatusCompiledState(t *testing.T) {
	reg := NewRegistry()
	_ = reg.AddCommand("features", "List features.")

	status := reg.Status()
	if len(status) == 0 {
		t.Fatal("Status returned empty")
	}

	var found bool
	for _, s := range status {
		if s.ID == "features" {
			found = true
			if !s.Compiled {
				t.Fatal("features status.Compiled = false, want true")
			}
		}
		if s.ID == "doctor" {
			if s.Compiled {
				t.Fatal("doctor status.Compiled = true, want false")
			}
		}
	}
	if !found {
		t.Fatal("features not found in status")
	}
}

func TestStatusSorting(t *testing.T) {
	reg := NewRegistry()
	status := reg.Status()
	for i := 1; i < len(status); i++ {
		prev := status[i-1]
		cur := status[i]
		if prev.Kind > cur.Kind || prev.Kind == cur.Kind && prev.ID > cur.ID {
			t.Fatalf("Status not sorted at %d: %#v before %#v", i, prev, cur)
		}
	}
}

func TestIsKnown(t *testing.T) {
	if !IsKnown(KindCommand, "features") {
		t.Fatal("IsKnown(command, features) = false")
	}
	if IsKnown(KindCommand, "nonexistent") {
		t.Fatal("IsKnown(command, nonexistent) = true")
	}
	if IsKnown(KindFeature, "features") {
		t.Fatal("IsKnown(feature, features) = true (wrong kind)")
	}
}

func TestKnownReturnsCopy(t *testing.T) {
	k1 := Known()
	k2 := Known()
	if len(k1) != len(k2) {
		t.Fatal("Known lengths differ")
	}
	// Should be independent copies.
	if &k1[0] == &k2[0] {
		t.Fatal("Known returns same slice backing")
	}
}
