package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func benchSearchCorpus(tb testing.TB) (string, *Registry) {
	tb.Helper()
	root := tb.TempDir()
	const fileCount = 8
	const linesPerFile = 200
	for f := 0; f < fileCount; f++ {
		var b strings.Builder
		for i := 0; i < linesPerFile; i++ {
			if i%17 == 0 {
				b.WriteString("alpha needle target ")
			}
			b.WriteString("the quick brown fox jumps over the lazy dog ")
			b.WriteString(strings.Repeat("x", (i*7)%32))
			b.WriteByte('\n')
		}
		path := filepath.Join(root, "doc"+string(rune('0'+f))+".txt")
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			tb.Fatalf("write corpus: %v", err)
		}
	}
	reg := NewRegistry()
	if err := RegisterFilesystem(reg, FilesystemOptions{WorkspaceRoot: root, Limits: ToolLimits{MaxOutputBytes: 1 << 20, MaxFileReadBytes: 1 << 20, MaxMatches: 1024, MaxLineBytes: 512}}); err != nil {
		tb.Fatalf("register fs: %v", err)
	}
	return root, reg
}

func BenchmarkSearchLiteralAcrossDir(b *testing.B) {
	_, reg := benchSearchCorpus(b)
	args := mustJSONBench(b, searchInput{Pattern: "needle", Literal: true, Limit: 1024})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := reg.Handle(context.Background(), ToolRequest{Name: "search", Arguments: args}); err != nil {
			b.Fatalf("search: %v", err)
		}
	}
}

func BenchmarkSearchLiteralIgnoreCase(b *testing.B) {
	_, reg := benchSearchCorpus(b)
	args := mustJSONBench(b, searchInput{Pattern: "NEEDLE", Literal: true, IgnoreCase: true, Limit: 1024})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := reg.Handle(context.Background(), ToolRequest{Name: "search", Arguments: args}); err != nil {
			b.Fatalf("search: %v", err)
		}
	}
}

func BenchmarkSearchRegexp(b *testing.B) {
	_, reg := benchSearchCorpus(b)
	args := mustJSONBench(b, searchInput{Pattern: "needle\\s+target", Limit: 1024})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := reg.Handle(context.Background(), ToolRequest{Name: "search", Arguments: args}); err != nil {
			b.Fatalf("search: %v", err)
		}
	}
}

func BenchmarkLiteralMatcherIgnoreCase(b *testing.B) {
	matcher, err := newMatcher("NEEDLE", true, true)
	if err != nil {
		b.Fatal(err)
	}
	line := strings.Repeat("the quick brown fox needle jumps over the lazy dog ", 4)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !matcher.Match(line) {
			b.Fatal("expected match")
		}
	}
}

func BenchmarkApplyPatchFile(b *testing.B) {
	const lines = 4000
	var orig strings.Builder
	for i := 0; i < lines; i++ {
		orig.WriteString("line ")
		orig.WriteString(strings.Repeat("x", (i*3)%24))
		orig.WriteByte('\n')
	}
	original := orig.String()
	// Build a hunk that matches the first six lines exactly so apply succeeds.
	first := []string{}
	scanner := strings.SplitAfter(original, "\n")
	for j := 0; j < 6; j++ {
		first = append(first, scanner[j])
	}
	diffLines := []string{
		"--- a/file.txt",
		"+++ b/file.txt",
		"@@ -1,6 +1,6 @@",
	}
	for j, line := range first {
		text := strings.TrimSuffix(line, "\n")
		switch j {
		case 2:
			diffLines = append(diffLines, "-"+text)
			diffLines = append(diffLines, "+"+text+"-replaced")
		default:
			diffLines = append(diffLines, " "+text)
		}
	}
	diffLines = append(diffLines, "")
	diff := strings.Join(diffLines, "\n")
	files, err := parseUnifiedPatch(diff)
	if err != nil {
		b.Fatal(err)
	}
	hunks := files[0].Hunks
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := applyPatchFile("file.txt", original, hunks); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnifiedDiff(b *testing.B) {
	var beforeB, afterB strings.Builder
	for i := 0; i < 600; i++ {
		beforeB.WriteString("line ")
		beforeB.WriteString(strings.Repeat("x", (i*3)%18))
		beforeB.WriteByte('\n')
	}
	before := beforeB.String()
	for i := 0; i < 600; i++ {
		afterB.WriteString("LINE ")
		afterB.WriteString(strings.Repeat("y", (i*5)%20))
		afterB.WriteByte('\n')
	}
	after := afterB.String()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = unifiedDiff("dir/file.txt", before, after)
	}
}

func BenchmarkSplitLinesKeepNewline(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		sb.WriteString("line ")
		sb.WriteString(strings.Repeat("x", (i*3)%24))
		sb.WriteByte('\n')
	}
	text := sb.String()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := splitLinesKeepNewline(text)
		if len(out) == 0 {
			b.Fatal("expected lines")
		}
	}
}

func BenchmarkStreamCaptureSmallChunks(b *testing.B) {
	src := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64 KiB
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := newCapture(int64(len(src)) + 16)
		if _, err := c.CopyFrom(bytes.NewReader(src)); err != nil {
			b.Fatal(err)
		}
		_ = c.String()
	}
}

func BenchmarkStreamCaptureLargeChunk(b *testing.B) {
	src := bytes.Repeat([]byte{'A'}, 1<<20) // 1 MiB
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := newCapture(int64(len(src)))
		if _, err := io.Copy(c, bytes.NewReader(src)); err != nil {
			b.Fatal(err)
		}
		_ = c.String()
	}
}

func BenchmarkStreamCaptureTruncated(b *testing.B) {
	src := bytes.Repeat([]byte{'B'}, 4<<20) // 4 MiB
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := newCapture(64 << 10) // 64 KiB cap
		if _, err := io.Copy(c, bytes.NewReader(src)); err != nil {
			b.Fatal(err)
		}
		if !c.Truncated() {
			b.Fatal("expected truncated capture")
		}
	}
}

func mustJSONBench(b *testing.B, v any) []byte {
	b.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		b.Fatalf("Marshal: %v", err)
	}
	return raw
}
