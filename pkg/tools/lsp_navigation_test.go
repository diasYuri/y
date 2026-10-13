package tools

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestLSPCoordinatesConvertUnicodeAndNormalizeRanges(t *testing.T) {
	line := "a😀b"
	character, err := runeColumnToUTF16(line, 3)
	if err != nil || character != 3 {
		t.Fatalf("rune column to UTF-16 = %d, %v; want 3", character, err)
	}
	if got := utf16ToRuneColumn(line, 3); got != 3 {
		t.Fatalf("UTF-16 to rune column = %d, want 3", got)
	}
	if _, err := runeColumnToUTF16(line, 5); err == nil {
		t.Fatal("out-of-range 1-based column should fail")
	}

	root := t.TempDir()
	path := filepath.Join(root, "unicode.go")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	raw := json.RawMessage(`[{"uri":"` + uri + `","range":{"start":{"line":0,"character":3},"end":{"line":0,"character":4}}}]`)
	normalized, err := normalizeLSPResult(raw, root, line+"\n", 10)
	if err != nil {
		t.Fatalf("normalize LSP result: %v", err)
	}
	var result []map[string]any
	if err := json.Unmarshal(normalized, &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0]["path"] != "unicode.go" {
		t.Fatalf("normalized location = %#v", result)
	}
	rangeValue := result[0]["range"].(map[string]any)
	start := rangeValue["start"].(map[string]any)
	if start["line"] != float64(1) || start["character"] != float64(3) {
		t.Fatalf("normalized start position = %#v", start)
	}
}
