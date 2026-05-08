//go:build feature_wasm_ext

// Package wasmtest produces tiny but ABI-compliant WebAssembly modules used
// by both the wasm package's unit tests and the example extension's
// regression test. Generating the bytes in-process keeps these test paths
// independent of TinyGo or any external toolchain.
package wasmtest

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// ABI export names are duplicated here so that this internal helper does
// not import the parent wasm package. Doing so would create an import cycle
// when the wasm package itself uses wasmtest in its tests.
const (
	exportInit       = "pi_extension_init"
	exportHandle     = "pi_extension_handle"
	exportShutdown   = "pi_extension_shutdown"
	exportFree       = "pi_extension_free"
	exportMalloc     = "malloc"
	exportABIVersion = "pi_extension_abi_version"
)

// Builder assembles a hand-rolled WebAssembly module. It only supports the
// fragments the test fixtures need; it is not a general-purpose encoder.
type Builder struct {
	types       []funcType
	funcs       []function
	exports     []export
	memoryPages uint32
}

type funcType struct {
	params  []byte
	results []byte
}

type function struct {
	typeIndex uint32
	body      []byte
	locals    []localDecl
}

type localDecl struct {
	count    uint32
	valueTyp byte
}

type export struct {
	name  string
	kind  byte
	index uint32
}

// New returns a Builder pre-populated with a single 1-page memory.
func New() *Builder {
	return &Builder{memoryPages: 1}
}

// SetMemoryPages overrides the default memory size in 64 KiB pages.
func (b *Builder) SetMemoryPages(pages uint32) {
	if pages == 0 {
		pages = 1
	}
	b.memoryPages = pages
}

// AddType registers a function signature and returns its index.
func (b *Builder) AddType(params, results []byte) uint32 {
	b.types = append(b.types, funcType{params: params, results: results})
	return uint32(len(b.types) - 1)
}

// AddFunc adds a function with the given type index and body. It returns
// the function's index in the module.
func (b *Builder) AddFunc(typeIndex uint32, body []byte) uint32 {
	b.funcs = append(b.funcs, function{typeIndex: typeIndex, body: body})
	return uint32(len(b.funcs) - 1)
}

// Export registers an export entry for an existing function or memory.
func (b *Builder) Export(name string, kind byte, index uint32) {
	b.exports = append(b.exports, export{name: name, kind: kind, index: index})
}

// Build emits the binary representation of the module.
func (b *Builder) Build() []byte {
	var out bytes.Buffer
	out.Write([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00})

	if len(b.types) > 0 {
		var sec bytes.Buffer
		writeUVarint(&sec, uint64(len(b.types)))
		for _, t := range b.types {
			sec.WriteByte(0x60)
			writeUVarint(&sec, uint64(len(t.params)))
			sec.Write(t.params)
			writeUVarint(&sec, uint64(len(t.results)))
			sec.Write(t.results)
		}
		writeSection(&out, 1, sec.Bytes())
	}

	if len(b.funcs) > 0 {
		var sec bytes.Buffer
		writeUVarint(&sec, uint64(len(b.funcs)))
		for _, f := range b.funcs {
			writeUVarint(&sec, uint64(f.typeIndex))
		}
		writeSection(&out, 3, sec.Bytes())
	}

	{
		var sec bytes.Buffer
		writeUVarint(&sec, 1)
		sec.WriteByte(0x00)
		writeUVarint(&sec, uint64(b.memoryPages))
		writeSection(&out, 5, sec.Bytes())
	}

	if len(b.exports) > 0 {
		var sec bytes.Buffer
		writeUVarint(&sec, uint64(len(b.exports)))
		for _, e := range b.exports {
			writeUVarint(&sec, uint64(len(e.name)))
			sec.WriteString(e.name)
			sec.WriteByte(e.kind)
			writeUVarint(&sec, uint64(e.index))
		}
		writeSection(&out, 7, sec.Bytes())
	}

	if len(b.funcs) > 0 {
		var sec bytes.Buffer
		writeUVarint(&sec, uint64(len(b.funcs)))
		for _, f := range b.funcs {
			var fnBody bytes.Buffer
			writeUVarint(&fnBody, uint64(len(f.locals)))
			for _, l := range f.locals {
				writeUVarint(&fnBody, uint64(l.count))
				fnBody.WriteByte(l.valueTyp)
			}
			fnBody.Write(f.body)
			writeUVarint(&sec, uint64(fnBody.Len()))
			sec.Write(fnBody.Bytes())
		}
		writeSection(&out, 10, sec.Bytes())
	}

	return out.Bytes()
}

// AppendDataSection adds a passive data initializer placing data at the
// supplied memory offset.
func AppendDataSection(module []byte, offset uint32, data []byte) []byte {
	if len(data) == 0 {
		return module
	}
	var sec bytes.Buffer
	writeUVarint(&sec, 1)
	sec.WriteByte(0x00)
	sec.WriteByte(OpI32Const)
	writeSVarint(&sec, int64(offset))
	sec.WriteByte(OpEnd)
	writeUVarint(&sec, uint64(len(data)))
	sec.Write(data)
	var full bytes.Buffer
	full.Write(module)
	writeSection(&full, 11, sec.Bytes())
	return full.Bytes()
}

// WebAssembly value type encodings.
const (
	ValI32 = 0x7f
	ValI64 = 0x7e
)

// Common opcodes used by the prebuilt bodies.
const (
	OpEnd         = 0x0b
	OpUnreachable = 0x00
	OpI32Const    = 0x41
	OpI64Const    = 0x42
)

// I32ConstBody returns a function body that yields value as an i32 result.
func I32ConstBody(value uint32) []byte {
	var buf bytes.Buffer
	buf.WriteByte(OpI32Const)
	writeSVarint(&buf, int64(int32(value)))
	buf.WriteByte(OpEnd)
	return buf.Bytes()
}

// I64ZeroBody returns a function body that yields the i64 zero result.
func I64ZeroBody() []byte {
	var buf bytes.Buffer
	buf.WriteByte(OpI64Const)
	writeSVarint(&buf, 0)
	buf.WriteByte(OpEnd)
	return buf.Bytes()
}

// PackedReturnBody assembles a body that returns the (ptr<<32 | len) pair
// expected by the ABI as a single i64 constant.
func PackedReturnBody(ptr, length uint32) []byte {
	packed := (uint64(ptr) << 32) | uint64(length)
	var buf bytes.Buffer
	buf.WriteByte(OpI64Const)
	writeSVarint(&buf, int64(packed))
	buf.WriteByte(OpEnd)
	return buf.Bytes()
}

// BuildABIToolModule returns a WASM module that satisfies the y.wasm.v1 ABI
// by echoing the supplied response bytes from a fixed memory window. The
// response payload must fit inside one page so the data section can place
// it deterministically.
func BuildABIToolModule(response []byte) []byte {
	if len(response) > 0xFF {
		panic(fmt.Sprintf("response too long: %d bytes", len(response)))
	}
	const responseOffset = 0x8000
	const requestArena = 0x0100

	b := New()
	b.SetMemoryPages(2)

	tHandle := b.AddType([]byte{ValI32, ValI32}, []byte{ValI64})
	tFree := b.AddType([]byte{ValI32, ValI32}, nil)
	tMalloc := b.AddType([]byte{ValI32}, []byte{ValI32})
	tAbi := b.AddType(nil, []byte{ValI32})

	handleBody := PackedReturnBody(uint32(responseOffset), uint32(len(response)))
	fnHandle := b.AddFunc(tHandle, handleBody)
	fnInit := b.AddFunc(tHandle, PackedReturnBody(uint32(responseOffset), uint32(len(response))))
	fnShutdown := b.AddFunc(tHandle, I64ZeroBody())
	fnFree := b.AddFunc(tFree, []byte{OpEnd})
	fnMalloc := b.AddFunc(tMalloc, I32ConstBody(requestArena))
	fnAbi := b.AddFunc(tAbi, I32ConstBody(1))

	b.Export("memory", 0x02, 0)
	b.Export(exportInit, 0x00, fnInit)
	b.Export(exportHandle, 0x00, fnHandle)
	b.Export(exportShutdown, 0x00, fnShutdown)
	b.Export(exportFree, 0x00, fnFree)
	b.Export(exportMalloc, 0x00, fnMalloc)
	b.Export(exportABIVersion, 0x00, fnAbi)

	out := b.Build()
	return AppendDataSection(out, responseOffset, response)
}

// BuildABITrappingModule returns a guest whose pi_extension_handle traps
// using the unreachable opcode. The other ABI exports are valid so the
// loader still accepts the module.
func BuildABITrappingModule() []byte {
	b := New()
	b.SetMemoryPages(1)

	tHandle := b.AddType([]byte{ValI32, ValI32}, []byte{ValI64})
	tFree := b.AddType([]byte{ValI32, ValI32}, nil)
	tMalloc := b.AddType([]byte{ValI32}, []byte{ValI32})

	fnHandle := b.AddFunc(tHandle, []byte{OpUnreachable, OpEnd})
	fnInit := b.AddFunc(tHandle, I64ZeroBody())
	fnShutdown := b.AddFunc(tHandle, I64ZeroBody())
	fnFree := b.AddFunc(tFree, []byte{OpEnd})
	fnMalloc := b.AddFunc(tMalloc, I32ConstBody(0x1000))

	b.Export("memory", 0x02, 0)
	b.Export(exportInit, 0x00, fnInit)
	b.Export(exportHandle, 0x00, fnHandle)
	b.Export(exportShutdown, 0x00, fnShutdown)
	b.Export(exportFree, 0x00, fnFree)
	b.Export(exportMalloc, 0x00, fnMalloc)

	return b.Build()
}

func writeSection(w *bytes.Buffer, id byte, payload []byte) {
	w.WriteByte(id)
	writeUVarint(w, uint64(len(payload)))
	w.Write(payload)
}

func writeUVarint(w *bytes.Buffer, v uint64) {
	var buf [binary.MaxVarintLen64]byte
	n := 0
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		buf[n] = b
		n++
		if v == 0 {
			break
		}
	}
	w.Write(buf[:n])
}

func writeSVarint(w *bytes.Buffer, v int64) {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			w.WriteByte(b)
			return
		}
		w.WriteByte(b | 0x80)
	}
}
