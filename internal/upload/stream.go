// Package upload streams multipart archive uploads directly into a private
// staged object. It never spools the file part through mime/multipart's
// temporary files, so a large archive is written to disk exactly once.
package upload

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
)

var (
	// ErrMalformed reports a multipart body that cannot be parsed.
	ErrMalformed = errors.New("malformed multipart upload")
	// ErrMissingFile reports a body without the expected file part.
	ErrMissingFile = errors.New("upload file part is missing")
	// ErrUnexpectedPart reports a duplicate file part, a file in a text
	// field, a field after the file part, or too many fields.
	ErrUnexpectedPart = errors.New("unexpected multipart part")
	// ErrFieldTooLarge reports a text field above the per-field limit.
	ErrFieldTooLarge = errors.New("multipart field exceeds the size limit")
	// ErrFileTooLarge reports a file part above MaxFileBytes.
	ErrFileTooLarge = errors.New("upload exceeds the configured size limit")
)

const (
	defaultMaxFieldBytes = 64 << 10
	defaultMaxFields     = 32
	defaultCheckInterval = 256 << 20
	copyBufferBytes      = 1 << 20
)

// Options controls one streamed upload.
type Options struct {
	// FileField is the form name of the single file part.
	FileField string
	// MaxFileBytes bounds the staged object; zero or less means unbounded.
	MaxFileBytes int64
	// MaxFieldBytes bounds each text field (default 64 KiB).
	MaxFieldBytes int64
	// MaxFields bounds the number of text fields (default 32).
	MaxFields int
	// Create opens the destination for the file part. It must create a new
	// private regular file; Stream removes it on every failure path.
	Create func(filename string) (*os.File, error)
	// BeforeFile runs after the text fields that precede the file part have
	// been read and before any file byte is written, so callers can reject
	// a request without accepting the archive body.
	BeforeFile func(fields url.Values, filename string) error
	// CheckSpace runs before the first byte and then every CheckInterval
	// bytes with the number of bytes staged so far. Returning an error stops
	// the upload and removes the partial object.
	CheckSpace func(written int64) error
	// CheckInterval defaults to 256 MiB.
	CheckInterval int64
	// FieldsAfterFile accepts text fields after the file part. Callers that
	// must validate every field before admitting the body leave it false.
	FieldsAfterFile bool
}

// Result describes the staged object and the accompanying text fields.
type Result struct {
	Fields   url.Values
	Filename string
	Path     string
	Size     int64
	SHA256   string
}

// Stream reads every part from reader, writing the FileField part straight
// into the file returned by opts.Create while hashing and counting it. The
// staged file is synced and closed before Stream returns successfully.
func Stream(reader *multipart.Reader, opts Options) (*Result, error) {
	if reader == nil || opts.FileField == "" || opts.Create == nil {
		return nil, errors.New("upload stream is not configured")
	}
	maxField := opts.MaxFieldBytes
	if maxField <= 0 {
		maxField = defaultMaxFieldBytes
	}
	maxFields := opts.MaxFields
	if maxFields <= 0 {
		maxFields = defaultMaxFields
	}
	result := &Result{Fields: url.Values{}}
	fieldCount := 0
	staged := false
	success := false
	defer func() {
		if staged && !success {
			_ = os.Remove(result.Path)
		}
	}()
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, wrapReadError(err)
		}
		name := part.FormName()
		filename := part.FileName()
		switch {
		case name == opts.FileField:
			if staged || filename == "" {
				_ = part.Close()
				return nil, fmt.Errorf("%w: %q must be a single file part", ErrUnexpectedPart, name)
			}
			if opts.BeforeFile != nil {
				if err := opts.BeforeFile(result.Fields, filename); err != nil {
					_ = part.Close()
					return nil, err
				}
			}
			file, err := opts.Create(filename)
			if err != nil {
				_ = part.Close()
				return nil, fmt.Errorf("create staged upload: %w", err)
			}
			staged = true
			result.Path = file.Name()
			result.Filename = filename
			size, sum, copyErr := copyPart(file, part, opts)
			syncErr := file.Sync()
			closeErr := file.Close()
			_ = part.Close()
			if copyErr != nil {
				return nil, copyErr
			}
			if syncErr != nil {
				return nil, fmt.Errorf("sync staged upload: %w", syncErr)
			}
			if closeErr != nil {
				return nil, fmt.Errorf("close staged upload: %w", closeErr)
			}
			result.Size = size
			result.SHA256 = sum
		case filename != "":
			_ = part.Close()
			return nil, fmt.Errorf("%w: unexpected file in field %q", ErrUnexpectedPart, name)
		default:
			if staged && !opts.FieldsAfterFile {
				_ = part.Close()
				return nil, fmt.Errorf("%w: field %q must precede the %q file part", ErrUnexpectedPart, name, opts.FileField)
			}
			fieldCount++
			if fieldCount > maxFields {
				_ = part.Close()
				return nil, fmt.Errorf("%w: more than %d fields", ErrUnexpectedPart, maxFields)
			}
			value, err := io.ReadAll(io.LimitReader(part, maxField+1))
			_ = part.Close()
			if err != nil {
				return nil, wrapReadError(err)
			}
			if int64(len(value)) > maxField {
				return nil, fmt.Errorf("%w: %q", ErrFieldTooLarge, name)
			}
			result.Fields.Add(name, string(value))
		}
	}
	if !staged {
		return nil, ErrMissingFile
	}
	success = true
	return result, nil
}

func copyPart(dst io.Writer, src io.Reader, opts Options) (int64, string, error) {
	interval := opts.CheckInterval
	if interval <= 0 {
		interval = defaultCheckInterval
	}
	hasher := sha256.New()
	if opts.CheckSpace != nil {
		if err := opts.CheckSpace(0); err != nil {
			return 0, "", err
		}
	}
	buffer := make([]byte, copyBufferBytes)
	var written, nextCheck int64 = 0, interval
	for {
		n, readErr := src.Read(buffer)
		if n > 0 {
			if opts.MaxFileBytes > 0 && written+int64(n) > opts.MaxFileBytes {
				return written, "", ErrFileTooLarge
			}
			if err := writeAll(dst, hasher, buffer[:n]); err != nil {
				return written, "", fmt.Errorf("write staged upload: %w", err)
			}
			written += int64(n)
			if opts.CheckSpace != nil && written >= nextCheck {
				if err := opts.CheckSpace(written); err != nil {
					return written, "", err
				}
				nextCheck = written + interval
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, hex.EncodeToString(hasher.Sum(nil)), nil
		}
		if readErr != nil {
			return written, "", wrapReadError(readErr)
		}
	}
}

func writeAll(dst io.Writer, hasher hash.Hash, chunk []byte) error {
	if _, err := dst.Write(chunk); err != nil {
		return err
	}
	_, err := hasher.Write(chunk)
	return err
}

// wrapReadError keeps request-body limit errors visible to callers (for a
// 413 response) and classifies every other read failure as malformed.
func wrapReadError(err error) error {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		return fmt.Errorf("%w: %w", ErrFileTooLarge, err)
	}
	return fmt.Errorf("%w: %w", ErrMalformed, err)
}
