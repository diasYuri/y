package sse

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestReadDataSimple(t *testing.T) {
	input := "data: hello\n\n"
	r := bufio.NewReader(strings.NewReader(input))
	got, err := ReadData(r, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q, want hello", string(got))
	}
}

func TestReadDataMultiLine(t *testing.T) {
	input := "data: line1\ndata: line2\n\n"
	r := bufio.NewReader(strings.NewReader(input))
	got, err := ReadData(r, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "line1\nline2" {
		t.Fatalf("got %q, want 'line1\\nline2'", string(got))
	}
}

func TestReadDataIgnoresComments(t *testing.T) {
	input := ":comment\ndata: hello\n\n"
	r := bufio.NewReader(strings.NewReader(input))
	got, err := ReadData(r, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q, want hello", string(got))
	}
}

func TestReadDataIgnoresOtherFields(t *testing.T) {
	input := "event: message\ndata: hello\nid: 1\n\n"
	r := bufio.NewReader(strings.NewReader(input))
	got, err := ReadData(r, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q, want hello", string(got))
	}
}

func TestReadDataEOFWithData(t *testing.T) {
	input := "data: hello"
	r := bufio.NewReader(strings.NewReader(input))
	_, err := ReadData(r, 1024)
	// EOF without trailing newline is returned as error
	// (data is not considered a complete event).
	if err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestReadDataEOFFWithoutData(t *testing.T) {
	input := ""
	r := bufio.NewReader(strings.NewReader(input))
	_, err := ReadData(r, 1024)
	if err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestReadDataExceedsLimit(t *testing.T) {
	input := "data: hello world\n\n"
	r := bufio.NewReader(strings.NewReader(input))
	_, err := ReadData(r, 5)
	if err == nil {
		t.Fatal("expected error for exceeding limit")
	}
}

func TestReadDataMultipleEvents(t *testing.T) {
	input := "data: first\n\ndata: second\n\n"
	r := bufio.NewReader(strings.NewReader(input))

	got1, err := ReadData(r, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got1) != "first" {
		t.Fatalf("event1 = %q, want first", string(got1))
	}

	got2, err := ReadData(r, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got2) != "second" {
		t.Fatalf("event2 = %q, want second", string(got2))
	}
}

func TestReadDataCRLF(t *testing.T) {
	input := "data: hello\r\n\r\n"
	r := bufio.NewReader(strings.NewReader(input))
	got, err := ReadData(r, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q, want hello", string(got))
	}
}

func TestReadLineSimple(t *testing.T) {
	input := "hello\nworld\n"
	r := bufio.NewReader(strings.NewReader(input))
	got, err := readLine(r, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("got %q, want 'hello\\n'", string(got))
	}
}

func TestReadLineExceedsLimit(t *testing.T) {
	input := "hello world\n"
	r := bufio.NewReader(strings.NewReader(input))
	_, err := readLine(r, 5)
	if err == nil {
		t.Fatal("expected error for exceeding limit")
	}
}

func TestReadLineBufferFull(t *testing.T) {
	// Create input larger than bufio default buffer (4K)
	large := bytes.Repeat([]byte("x"), 8192)
	input := append(large, '\n')
	r := bufio.NewReader(bytes.NewReader(input))
	got, err := readLine(r, 16384)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(input) {
		t.Fatalf("len = %d, want %d", len(got), len(input))
	}
}
