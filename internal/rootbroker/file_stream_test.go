package rootbroker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFileChunkReaderRequiresExplicitTerminator(t *testing.T) {
	var framed bytes.Buffer
	if _, err := writeFileStream(&framed, strings.NewReader("hello"), 10); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), framed.Bytes()...)
	got, err := io.ReadAll(NewFileContentReader(bytes.NewReader(raw)))
	if err != nil || string(got) != "hello" {
		t.Fatalf("decoded stream = %q, %v", got, err)
	}
	truncated := raw[:len(raw)-4] // remove the zero-length terminator
	if _, err := io.ReadAll(NewFileContentReader(bytes.NewReader(truncated))); err == nil {
		t.Fatal("stream without terminator was accepted")
	}
}

func TestFileChunkReaderRejectsOversizedFrame(t *testing.T) {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], fileStreamChunk+1)
	if _, err := io.ReadAll(NewFileContentReader(bytes.NewReader(header[:]))); err == nil {
		t.Fatal("oversized frame was accepted")
	}
}

func TestWriteFileStreamRejectsOversizedSource(t *testing.T) {
	var framed bytes.Buffer
	_, err := writeFileStream(&framed, strings.NewReader("12345"), 4)
	if !errors.Is(err, errFileStreamTooLarge) {
		t.Fatalf("writeFileStream error = %v, want oversize rejection", err)
	}
}
