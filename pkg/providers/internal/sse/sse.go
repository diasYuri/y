// Package sse contains small streaming helpers shared by HTTP providers.
package sse

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
)

const defaultMaxEventBytes int64 = 1 << 20

// ReadData reads one Server-Sent Events data payload without retaining prior
// events. Multi-line data fields are joined with newlines.
func ReadData(r *bufio.Reader, maxEventBytes int64) ([]byte, error) {
	if maxEventBytes <= 0 {
		maxEventBytes = defaultMaxEventBytes
	}
	var data bytes.Buffer
	for {
		line, err := readLine(r, maxEventBytes)
		if err != nil {
			if err == io.EOF && data.Len() > 0 {
				return data.Bytes(), nil
			}
			return nil, err
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			return data.Bytes(), nil
		}
		if bytes.HasPrefix(line, []byte(":")) {
			continue
		}
		name, value, ok := bytes.Cut(line, []byte(":"))
		if !ok || !bytes.Equal(name, []byte("data")) {
			continue
		}
		value = bytes.TrimPrefix(value, []byte(" "))
		if int64(data.Len()+len(value)+1) > maxEventBytes {
			return nil, fmt.Errorf("SSE event exceeds %d bytes", maxEventBytes)
		}
		if data.Len() > 0 {
			data.WriteByte('\n')
		}
		data.Write(value)
	}
}

func readLine(r *bufio.Reader, maxEventBytes int64) ([]byte, error) {
	var out bytes.Buffer
	for {
		part, err := r.ReadSlice('\n')
		if len(part) > 0 {
			if int64(out.Len()+len(part)) > maxEventBytes {
				return nil, fmt.Errorf("SSE line exceeds %d bytes", maxEventBytes)
			}
			out.Write(part)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return out.Bytes(), err
		}
		return out.Bytes(), nil
	}
}
