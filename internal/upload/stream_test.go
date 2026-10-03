package upload

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type part struct {
	name, filename, body string
}

func multipartBody(t *testing.T, parts ...part) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for _, p := range parts {
		var target io.Writer
		var err error
		if p.filename != "" {
			target, err = writer.CreateFormFile(p.name, p.filename)
		} else {
			target, err = writer.CreateFormField(p.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(target, p.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body, writer.Boundary()
}

func reader(t *testing.T, parts ...part) *multipart.Reader {
	body, boundary := multipartBody(t, parts...)
	return multipart.NewReader(body, boundary)
}

func createIn(t *testing.T, dir string, created *int) func(string) (*os.File, error) {
	t.Helper()
	return func(string) (*os.File, error) {
		*created++
		return os.CreateTemp(dir, "upload-*")
	}
}

func entries(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestStreamStagesFileOnceWithFieldsAndHash(t *testing.T) {
	dir := t.TempDir()
	created := 0
	payload := strings.Repeat("archive-bytes-", 1000)
	var seen url.Values
	result, err := Stream(reader(t, part{name: "site", body: "example"}, part{name: "backup", filename: "site.wpress", body: payload}), Options{
		FileField:     "backup",
		MaxFileBytes:  int64(len(payload)),
		Create:        createIn(t, dir, &created),
		BeforeFile:    func(fields url.Values, _ string) error { seen = fields; return nil },
		CheckInterval: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(payload))
	if result.Size != int64(len(payload)) || result.SHA256 != hex.EncodeToString(sum[:]) || result.Filename != "site.wpress" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if seen.Get("site") != "example" || result.Fields.Get("site") != "example" {
		t.Fatalf("fields were not delivered before the file: %v / %v", seen, result.Fields)
	}
	staged, err := os.ReadFile(result.Path)
	if err != nil || string(staged) != payload {
		t.Fatalf("staged content mismatch: %v", err)
	}
	if created != 1 || len(entries(t, dir)) != 1 {
		t.Fatalf("expected exactly one staged object, created=%d entries=%d", created, len(entries(t, dir)))
	}
}

func TestStreamRejectsOversizedFileAndRemovesPartialObject(t *testing.T) {
	dir := t.TempDir()
	created := 0
	_, err := Stream(reader(t, part{name: "backup", filename: "a.tar.gz", body: strings.Repeat("x", 4096)}), Options{
		FileField: "backup", MaxFileBytes: 1024, Create: createIn(t, dir, &created),
	})
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("err = %v, want ErrFileTooLarge", err)
	}
	if len(entries(t, dir)) != 0 {
		t.Fatal("partial object was left behind")
	}
}

func TestStreamBeforeFileRejectionWritesNothing(t *testing.T) {
	dir := t.TempDir()
	created := 0
	rejection := errors.New("rejected")
	_, err := Stream(reader(t, part{name: "confirm", body: "no"}, part{name: "backup", filename: "a.wpress", body: "data"}), Options{
		FileField:  "backup",
		Create:     createIn(t, dir, &created),
		BeforeFile: func(url.Values, string) error { return rejection },
	})
	if !errors.Is(err, rejection) || created != 0 {
		t.Fatalf("err = %v created = %d, want rejection before any file is created", err, created)
	}
}

func TestStreamSpaceCheckFailureRemovesPartialObject(t *testing.T) {
	dir := t.TempDir()
	created := 0
	full := errors.New("disk full")
	checks := 0
	_, err := Stream(reader(t, part{name: "backup", filename: "a.tar.gz", body: strings.Repeat("x", 8192)}), Options{
		FileField:     "backup",
		Create:        createIn(t, dir, &created),
		CheckInterval: 1,
		CheckSpace: func(written int64) error {
			checks++
			if written > 0 {
				return full
			}
			return nil
		},
	})
	if !errors.Is(err, full) || checks < 2 {
		t.Fatalf("err = %v checks = %d, want space failure after streaming began", err, checks)
	}
	if len(entries(t, dir)) != 0 {
		t.Fatal("partial object was left behind")
	}
}

func TestStreamRejectsMalformedPartLayouts(t *testing.T) {
	tests := []struct {
		name  string
		parts []part
		opts  Options
		want  error
	}{
		{name: "missing file", parts: []part{{name: "site", body: "x"}}, want: ErrMissingFile},
		{name: "duplicate file", parts: []part{{name: "backup", filename: "a", body: "1"}, {name: "backup", filename: "b", body: "2"}}, want: ErrUnexpectedPart},
		{name: "file field without filename", parts: []part{{name: "backup", body: "inline"}}, want: ErrUnexpectedPart},
		{name: "file in text field", parts: []part{{name: "site", filename: "x", body: "1"}, {name: "backup", filename: "a", body: "1"}}, want: ErrUnexpectedPart},
		{name: "field after file", parts: []part{{name: "backup", filename: "a", body: "1"}, {name: "confirm", body: "late"}}, want: ErrUnexpectedPart},
		{name: "oversized field", parts: []part{{name: "site", body: strings.Repeat("x", 65)}, {name: "backup", filename: "a", body: "1"}}, opts: Options{MaxFieldBytes: 64}, want: ErrFieldTooLarge},
		{name: "too many fields", parts: []part{{name: "a", body: "1"}, {name: "b", body: "2"}, {name: "backup", filename: "a", body: "1"}}, opts: Options{MaxFields: 1}, want: ErrUnexpectedPart},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			created := 0
			opts := test.opts
			opts.FileField = "backup"
			opts.Create = createIn(t, dir, &created)
			if _, err := Stream(reader(t, test.parts...), opts); !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
			if len(entries(t, dir)) != 0 {
				t.Fatal("rejected upload left a staged object")
			}
		})
	}
}

func TestStreamAcceptsFieldsAfterFileWhenAllowed(t *testing.T) {
	dir := t.TempDir()
	created := 0
	result, err := Stream(reader(t, part{name: "backup", filename: "a", body: "1"}, part{name: "note", body: "late"}), Options{
		FileField: "backup", Create: createIn(t, dir, &created), FieldsAfterFile: true,
	})
	if err != nil || result.Fields.Get("note") != "late" {
		t.Fatalf("result = %+v err = %v", result, err)
	}
}

func TestStreamReportsRequestBodyLimitAsTooLarge(t *testing.T) {
	dir := t.TempDir()
	created := 0
	body, boundary := multipartBody(t, part{name: "backup", filename: "a", body: strings.Repeat("x", 4096)})
	limited := http.MaxBytesReader(httptest.NewRecorder(), io.NopCloser(body), 1024)
	_, err := Stream(multipart.NewReader(limited, boundary), Options{FileField: "backup", Create: createIn(t, dir, &created)})
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("err = %v, want ErrFileTooLarge", err)
	}
	if len(entries(t, filepath.Clean(dir))) != 0 {
		t.Fatal("truncated upload left a staged object")
	}
}
