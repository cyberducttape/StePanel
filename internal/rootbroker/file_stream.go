package rootbroker

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

var errFileStreamTooLarge = errors.New("file content exceeds the configured size limit")

const fileStreamChunk = 32 << 10

// fileChunkReader decodes a chunked stream terminated by an explicit zero
// length frame. A dropped connection before that marker is an error, so a
// truncated request can never be mistaken for a complete upload.
type fileChunkReader struct {
	r         io.Reader
	remaining uint32
	done      bool
	initial   bool
}

// NewFileContentReader decodes one explicitly terminated file upload stream.
func NewFileContentReader(r io.Reader) io.Reader { return &fileChunkReader{r: r, initial: true} }

func (r *fileChunkReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.done {
		return 0, io.EOF
	}
	if r.remaining == 0 {
		var header [4]byte
		if r.initial {
			// json.Encoder terminates metadata with a newline; Decoder may leave
			// that separator in its read-ahead buffer.
			for {
				if _, err := io.ReadFull(r.r, header[:1]); err != nil {
					return 0, fmt.Errorf("incomplete file stream frame: %w", err)
				}
				if header[0] != ' ' && header[0] != '\t' && header[0] != '\r' && header[0] != '\n' {
					break
				}
			}
			if _, err := io.ReadFull(r.r, header[1:]); err != nil {
				return 0, fmt.Errorf("incomplete file stream frame: %w", err)
			}
			r.initial = false
		} else if _, err := io.ReadFull(r.r, header[:]); err != nil {
			return 0, fmt.Errorf("incomplete file stream frame: %w", err)
		}
		r.remaining = binary.BigEndian.Uint32(header[:])
		if r.remaining == 0 {
			r.done = true
			return 0, io.EOF
		}
		if r.remaining > fileStreamChunk {
			return 0, errors.New("file stream frame exceeds the chunk limit")
		}
	}
	if uint32(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := io.ReadFull(r.r, p)
	r.remaining -= uint32(n)
	if err != nil {
		return n, fmt.Errorf("incomplete file stream frame: %w", err)
	}
	return n, nil
}

func writeFileStream(w io.Writer, src io.Reader, maxBytes int64) (int64, error) {
	buf := make([]byte, fileStreamChunk)
	var total int64
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > maxBytes {
				return total, errFileStreamTooLarge
			}
			var header [4]byte
			binary.BigEndian.PutUint32(header[:], uint32(n))
			if _, err := w.Write(header[:]); err != nil {
				return total, err
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return total, err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				var terminator [4]byte
				_, err := w.Write(terminator[:])
				return total, err
			}
			return total, readErr
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
}
