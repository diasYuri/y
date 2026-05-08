package wasm

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseManifestValidExample(t *testing.T) {
	const src = `id = "example.search"
name = "Example Search"
version = "0.1.0"
api_version = "pi.wasm.v1"
entry = "module.wasm"

[runtime]
min_y_version = "0.1.0"
memory_pages = 128
timeout_ms = 2000
fuel = 5000000

[capabilities]
y_tools = true
filesystem = false
network = false

[[tools]]
name = "example_search"
description = "Searches using the extension index."
input_schema = "schemas/example_search.input.json"
output_schema = "schemas/example_search.output.json"

[[tools]]
name = "example_index"
description = "Re-builds the search index."
`
	m, err := Parse("inline", strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := m.Validate("inline"); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if m.ID != "example.search" {
		t.Errorf("ID = %q, want %q", m.ID, "example.search")
	}
	if m.Runtime.MemoryPages != 128 {
		t.Errorf("MemoryPages = %d, want 128", m.Runtime.MemoryPages)
	}
	if !m.Capabilities.YTools {
		t.Errorf("expected y_tools capability to be true")
	}
	if m.Capabilities.Network {
		t.Errorf("expected network capability to be false")
	}
	if got, want := len(m.Tools), 2; got != want {
		t.Fatalf("tools count = %d, want %d", got, want)
	}
	if m.Tools[0].Name != "example_search" {
		t.Errorf("tool[0].Name = %q", m.Tools[0].Name)
	}
}

func TestManifestValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "missing id",
			src: `name = "x"
version = "0.1.0"
api_version = "pi.wasm.v1"
entry = "module.wasm"`,
			want: "id",
		},
		{
			name: "wrong api version",
			src: `id = "ext"
name = "x"
version = "0.1.0"
api_version = "pi.wasm.v0"
entry = "module.wasm"`,
			want: "api_version",
		},
		{
			name: "absolute entry",
			src: `id = "ext"
name = "x"
version = "0.1.0"
api_version = "pi.wasm.v1"
entry = "/abs/module.wasm"`,
			want: "entry",
		},
		{
			name: "non-wasm entry",
			src: `id = "ext"
name = "x"
version = "0.1.0"
api_version = "pi.wasm.v1"
entry = "module.txt"`,
			want: "entry",
		},
		{
			name: "duplicate tool name",
			src: `id = "ext"
name = "x"
version = "0.1.0"
api_version = "pi.wasm.v1"
entry = "module.wasm"

[[tools]]
name = "dup"

[[tools]]
name = "dup"
`,
			want: "tools[1].name",
		},
		{
			name: "memory pages too high",
			src: `id = "ext"
name = "x"
version = "0.1.0"
api_version = "pi.wasm.v1"
entry = "module.wasm"

[runtime]
memory_pages = 999999
`,
			want: "runtime.memory_pages",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Parse("inline", strings.NewReader(tc.src))
			if err != nil {
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("parse error %q does not mention field %q", err, tc.want)
				}
				return
			}
			err = m.Validate("inline")
			if err == nil {
				t.Fatalf("expected validation error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention field %q", err, tc.want)
			}
			var me *ManifestError
			if !errors.As(err, &me) {
				t.Fatalf("error is not a *ManifestError: %T", err)
			}
		})
	}
}

func TestManifestParserRejectsUnknownSection(t *testing.T) {
	src := `id = "ext"
[unknown]
foo = "bar"
`
	_, err := Parse("inline", strings.NewReader(src))
	if err == nil {
		t.Fatal("expected parse error for unknown section")
	}
	if !strings.Contains(err.Error(), "unsupported section") {
		t.Fatalf("error %q does not flag unknown section", err)
	}
}

func TestManifestParserRejectsUnknownTopLevelKey(t *testing.T) {
	src := `id = "ext"
unknown_key = "value"
`
	_, err := Parse("inline", strings.NewReader(src))
	if err == nil {
		t.Fatal("expected parse error for unknown key")
	}
	if !strings.Contains(err.Error(), "unknown_key") {
		t.Fatalf("error %q does not name the offending key", err)
	}
}

func TestManifestParserRejectsBadValueType(t *testing.T) {
	cases := []string{
		`id = ext`, // string without quotes
		"id = \"ext\"\n[runtime]\nmemory_pages = x", // not a uint
		"id = \"ext\"\n[capabilities]\ny_tools = x", // not a bool
	}
	for _, src := range cases {
		_, err := Parse("inline", strings.NewReader(src))
		if err == nil {
			t.Fatalf("expected parse error for source: %q", src)
		}
	}
}

func TestManifestEntryPath(t *testing.T) {
	info := ExtensionInfo{
		Manifest: Manifest{Entry: "module.wasm"},
		Dir:      "/tmp/ext",
	}
	got := info.EntryPath()
	want := filepath.Join("/tmp/ext", "module.wasm")
	if got != want {
		t.Fatalf("EntryPath() = %q, want %q", got, want)
	}
}
