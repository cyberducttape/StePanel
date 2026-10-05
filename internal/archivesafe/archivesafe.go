// Package archivesafe is the single policy for reading untrusted archives:
// format detection, entry-type rules, path normalization, duplicate and
// collision rejection, and extraction confined to a directory.
//
// The import analyzer and the extractors use the same functions, so an
// archive the analyzer accepts is one the extractor will extract, and the
// format is decided from the archive's bytes rather than its URL or
// Content-Type.
//
// Extraction goes through os.Root, which resolves every path relative to an
// open directory handle (openat with RESOLVE_BENEATH semantics on Linux) and
// refuses to follow a symlink out of it, so a symlink planted in the
// destination between two operations cannot redirect a write. Files are
// created with O_EXCL, so an entry can never overwrite or write through
// anything that already exists.
package archivesafe

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

// Format is an archive container format.
type Format string

const (
	TarGzip Format = "tar.gz"
	Zip     Format = "zip"
)

// ErrUnknownFormat reports content that is neither gzip nor ZIP.
var ErrUnknownFormat = errors.New("archive is neither a gzip-compressed tar nor a ZIP file")

var (
	gzipMagic     = []byte{0x1f, 0x8b}
	zipLocalMagic = []byte("PK\x03\x04")
	zipEmptyMagic = []byte("PK\x05\x06")
)

// Sniff identifies the format from the first bytes of an archive.
func Sniff(prefix []byte) (Format, error) {
	switch {
	case bytes.HasPrefix(prefix, gzipMagic):
		return TarGzip, nil
	case bytes.HasPrefix(prefix, zipLocalMagic), bytes.HasPrefix(prefix, zipEmptyMagic):
		return Zip, nil
	}
	return "", ErrUnknownFormat
}

// DetectReader sniffs the format of a stream and returns a reader that still
// yields the sniffed bytes.
func DetectReader(r io.Reader) (Format, io.Reader, error) {
	buffered := bufio.NewReaderSize(r, 64)
	prefix, err := buffered.Peek(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", nil, fmt.Errorf("read archive header: %w", err)
	}
	format, sniffErr := Sniff(prefix)
	if sniffErr != nil {
		return "", nil, sniffErr
	}
	return format, buffered, nil
}

// Limits on a single entry name. Linux NAME_MAX and PATH_MAX.
const (
	maxComponentBytes = 255
	maxPathBytes      = 4096
)

// NormalizeName returns the canonical relative form of an archive entry
// name, or "." for the archive root ("./"). Backslashes are treated as
// separators (some ZIP tools write them), so "a\b" and "a/b" are the same
// path and collide.
func NormalizeName(name string) (string, error) {
	if name == "" {
		return "", errors.New("empty entry name")
	}
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("entry name %q is not valid UTF-8 text", name)
	}
	name = strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("entry %q has an absolute path", name)
	}
	if len(name) > maxPathBytes {
		return "", fmt.Errorf("entry name exceeds %d bytes", maxPathBytes)
	}
	for _, component := range strings.Split(name, "/") {
		if component == ".." {
			return "", fmt.Errorf("entry %q contains path traversal", name)
		}
		if len(component) > maxComponentBytes {
			return "", fmt.Errorf("entry %q has a component longer than %d bytes", name, maxComponentBytes)
		}
	}
	return path.Clean(name), nil
}

// Kind is what an archive entry creates.
type Kind int

const (
	Directory Kind = iota + 1
	File
)

func (k Kind) String() string {
	if k == Directory {
		return "directory"
	}
	return "file"
}

// TarKind classifies a tar entry. Only directories and regular files are
// supported; links, devices, FIFOs, sparse files and unknown types are
// rejected rather than treated as files.
func TarKind(header *tar.Header) (Kind, error) {
	switch header.Typeflag {
	case tar.TypeDir:
		return Directory, nil
	case tar.TypeReg, tar.TypeRegA:
		return File, nil
	case tar.TypeSymlink, tar.TypeLink:
		return 0, fmt.Errorf("entry %q is a link; links are not supported", header.Name)
	default:
		return 0, fmt.Errorf("entry %q has unsupported type %q", header.Name, header.Typeflag)
	}
}

// ZipKind classifies a ZIP entry with the same rules as TarKind.
func ZipKind(file *zip.File) (Kind, error) {
	mode := file.Mode()
	switch {
	case mode.IsDir():
		return Directory, nil
	case mode.IsRegular():
		return File, nil
	case mode&fs.ModeSymlink != 0:
		return 0, fmt.Errorf("entry %q is a link; links are not supported", file.Name)
	default:
		return 0, fmt.Errorf("entry %q is an unsupported special file", file.Name)
	}
}

// PathSet records every path an archive produces and rejects collisions:
// a file named twice (which would let a later entry replace content after
// it was inspected), a file and a directory with the same name, and an
// entry beneath a path that is a file. A directory may be named more than
// once, including after it was created implicitly as a parent.
type PathSet struct {
	kinds map[string]Kind
}

// NewPathSet returns an empty set.
func NewPathSet() *PathSet {
	return &PathSet{kinds: map[string]Kind{}}
}

// Claim normalizes name and records it as kind. It returns the normalized
// path ("." for the archive root, which only a directory may name).
func (p *PathSet) Claim(name string, kind Kind) (string, error) {
	clean, err := NormalizeName(name)
	if err != nil {
		return "", err
	}
	if clean == "." {
		if kind != Directory {
			return "", fmt.Errorf("entry %q names the archive root as a file", name)
		}
		return clean, nil
	}
	// Every ancestor must be (or become) a directory. A recorded directory's
	// own ancestors were checked when it was recorded.
	for dir := path.Dir(clean); dir != "."; dir = path.Dir(dir) {
		existing := p.kinds[dir]
		if existing == File {
			return "", fmt.Errorf("entry %q is inside %q, which the archive already created as a file", name, dir)
		}
		if existing == Directory {
			break
		}
		p.kinds[dir] = Directory
	}
	switch existing := p.kinds[clean]; {
	case existing == 0:
		p.kinds[clean] = kind
	case existing == Directory && kind == Directory:
	default:
		return "", fmt.Errorf("entry %q collides with an earlier %s at %q", name, existing, clean)
	}
	return clean, nil
}

// Extractor writes entries beneath one directory.
type Extractor struct {
	root  *os.Root
	paths *PathSet
	// ParentMode is the mode of directories created implicitly as parents.
	ParentMode os.FileMode
}

// OpenExtractor confines extraction to dir, which must exist.
func OpenExtractor(dir string) (*Extractor, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open extraction root: %w", err)
	}
	return &Extractor{root: root, paths: NewPathSet(), ParentMode: 0o700}, nil
}

// Close releases the directory handle.
func (x *Extractor) Close() error {
	return x.root.Close()
}

// Reserve claims a path that the archive must not produce (for example the
// staged upload itself, when it lives in the destination).
func (x *Extractor) Reserve(name string) error {
	_, err := x.paths.Claim(name, File)
	return err
}

// Dir creates a directory entry with the permission bits of mode.
func (x *Extractor) Dir(name string, mode os.FileMode) (string, error) {
	clean, err := x.paths.Claim(name, Directory)
	if err != nil {
		return "", err
	}
	if clean == "." {
		return clean, nil
	}
	if err := x.root.MkdirAll(clean, x.ParentMode); err != nil {
		return "", fmt.Errorf("create directory %q: %w", clean, err)
	}
	if err := x.root.Chmod(clean, mode.Perm()); err != nil {
		return "", fmt.Errorf("set permissions of %q: %w", clean, err)
	}
	return clean, nil
}

// File creates a regular file entry from exactly size bytes of src, with the
// permission bits of mode. It never replaces an existing path.
func (x *Extractor) File(name string, mode os.FileMode, src io.Reader, size int64) (string, int64, error) {
	if size < 0 {
		return "", 0, fmt.Errorf("entry %q has a negative size", name)
	}
	clean, err := x.paths.Claim(name, File)
	if err != nil {
		return "", 0, err
	}
	if parent := path.Dir(clean); parent != "." {
		if err := x.root.MkdirAll(parent, x.ParentMode); err != nil {
			return "", 0, fmt.Errorf("create parent of %q: %w", clean, err)
		}
	}
	out, err := x.root.OpenFile(clean, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("create %q: %w", clean, err)
	}
	// Read one byte past the declared size to detect an entry that is
	// longer than its header claims.
	written, copyErr := io.Copy(out, io.LimitReader(src, size+1))
	var chmodErr error
	if copyErr == nil && written == size {
		// Through the descriptor, so the mode lands on the file just written.
		chmodErr = out.Chmod(mode.Perm())
	}
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		return "", written, fmt.Errorf("write %q: %w", clean, copyErr)
	case written != size:
		return "", written, fmt.Errorf("entry %q is %d bytes, but its header declares %d", clean, written, size)
	case chmodErr != nil:
		return "", written, fmt.Errorf("set permissions of %q: %w", clean, chmodErr)
	case closeErr != nil:
		return "", written, fmt.Errorf("finalize %q: %w", clean, closeErr)
	}
	return clean, written, nil
}
