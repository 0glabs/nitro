// Copyright 2021-2024, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

//go:build wasm
// +build wasm

package arbcompress

import (
	"fmt"
	"unsafe"

	"github.com/offchainlabs/nitro/arbutil"
)

//go:wasmimport arbcompress brotli_compress
func brotliCompress(inBuf unsafe.Pointer, inBufLen uint32, outBuf unsafe.Pointer, outBufLen unsafe.Pointer, level, windowSize uint32) BrotliStatus

//go:wasmimport arbcompress brotli_decompress
func brotliDecompress(inBuf unsafe.Pointer, inBufLen uint32, outBuf unsafe.Pointer, outBufLen unsafe.Pointer) BrotliStatus

func Decompress(input []byte, maxSize int) ([]byte, error) {
	outBuf := make([]byte, maxSize)
	outLen := uint32(len(outBuf))
	status := brotliDecompress(
		arbutil.SliceToUnsafePointer(input), uint32(len(input)), arbutil.SliceToUnsafePointer(outBuf), unsafe.Pointer(&outLen),
	)
	if status != BrotliSuccess {
		return nil, fmt.Errorf("failed decompression")
	}
	return outBuf[:outLen], nil
}

func compressLevel(input []byte, level int) ([]byte, error) {
	maxOutSize := compressedBufferSizeFor(len(input))
	outBuf := make([]byte, maxOutSize)
	outLen := uint32(len(outBuf))
	status := brotliCompress(
		arbutil.SliceToUnsafePointer(input), uint32(len(input)),
		arbutil.SliceToUnsafePointer(outBuf), unsafe.Pointer(&outLen),
		uint32(level),
		WINDOW_SIZE,
	)
	if status != BrotliSuccess {
		return nil, fmt.Errorf("failed compression")
	}
	return outBuf[:outLen], nil
}
