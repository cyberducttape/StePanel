package archivesafe

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSniffDecidesFromContent(t *testing.T) {
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_ = w.Close()
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	if _, err := zw.Create("a.txt"); err != nil {
		t.Fatal(err)
	}
	_ = zw.Close()
	var empty bytes.Buffer
	_ = zip.NewWriter(&empty).Close()

	for name, tc := range map[string]struct {
		data []byte
		want Format
	}{
		"gzip":      {gz.Bytes(), TarGzip},
		"zip":       {zipped.Bytes(), Zip},
		"empty zip": {empty.Bytes(), Zip},
	} {
		format, reader, err := DetectReader(bytes.NewReader(tc.data))
		if err != nil || format != tc.want {
			t.Fatalf("%s: format = %q, %v", name, format, err)
		}
		// The sniffed bytes are still delivered.
		if all, _ := io.ReadAll(reader); !bytes.Equal(all, tc.data) {
			t.Fatalf("%s: detected reader lost the header bytes", name)
		}
	}
	for name, data := range map[string][]byte{"html": []byte("<html>"), "empty": nil, "plain tar": make([]byte, 512)} {
		if _, _, err := DetectReader(bytes.NewReader(data)); !errors.Is(err, ErrUnknownFormat) {
			t.Errorf("%s: err = %v, want ErrUnknownFormat", name, err)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	for in, want := range map[string]string{
		"a/b.php":      "a/b.php",
		"./a//b/":      "a/b",
		`win\path.txt`: "win/path.txt",
		"./":           ".",
		"a/./b":        "a/b",
	} {
		if got, err := NormalizeName(in); err != nil || got != want {
			t.Errorf("NormalizeName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "/etc/passwd", `\etc\passwd`, "../x", "a/../../x", "a/..", "a\x00b", "bad\xff", strings.Repeat("a", 256), strings.Repeat("a/", 2049)} {
		if _, err := NormalizeName(bad); err == nil {
			t.Errorf("NormalizeName(%q) accepted", bad)
		}
	}
}

func TestPathSetRejectsDuplicatesAndCollisions(t *testing.T) {
	type entry struct {
		name string
		kind Kind
	}
	for name, entries := range map[string][]entry{
		"duplicate file":          {{"a.php", File}, {"a.php", File}},
		"duplicate after cleanup": {{"a/b.php", File}, {"./a//b.php", File}},
		"backslash alias":         {{"a/b.php", File}, {`a\b.php`, File}},
		"directory over file":     {{"a", File}, {"a/", Directory}},
		"file over directory":     {{"a/", Directory}, {"a", File}},
		"file over implicit dir":  {{"a/b.php", File}, {"a", File}},
		"entry under a file":      {{"a", File}, {"a/b.php", File}},
		"deep entry under a file": {{"a/b", File}, {"a/b/c/d", File}},
		"root as a file":          {{"./", File}},
	} {
		set := NewPathSet()
		var err error
		for _, e := range entries {
			if _, err = set.Claim(e.name, e.kind); err != nil {
				break
			}
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	set := NewPathSet()
	for _, e := range []entry{{"./", Directory}, {"a/b.php", File}, {"a/", Directory}, {"a/", Directory}, {"a/c/", Directory}, {"a/c/d.php", File}} {
		if _, err := set.Claim(e.name, e.kind); err != nil {
			t.Fatalf("legitimate entry %q rejected: %v", e.name, err)
		}
	}
}

func TestExtractorWritesConfinedExclusiveFiles(t *testing.T) {
	dir := t.TempDir()
	x, err := OpenExtractor(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if _, err := x.Dir("public/", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := x.File("public/index.php", 0o4755, strings.NewReader("<?php"), 5); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "public", "index.php"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != 0o755 {
		t.Fatalf("mode = %v, want setuid stripped 0755", info.Mode())
	}
	if _, _, err := x.File("public/index.php", 0o644, strings.NewReader("<?php"), 5); err == nil {
		t.Fatal("second write to the same path accepted")
	}
	if _, _, err := x.File("short.txt", 0o644, strings.NewReader("abc"), 10); err == nil {
		t.Fatal("entry shorter than its header accepted")
	}
	if _, _, err := x.File("long.txt", 0o644, strings.NewReader("abcdef"), 3); err == nil {
		t.Fatal("entry longer than its header accepted")
	}
}

// A symlink planted in the destination (for example by a process racing
// the extraction) must not redirect writes outside it.
func TestExtractorDoesNotFollowPlantedSymlinks(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "planted")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "target.txt"), filepath.Join(dir, "file-link")); err != nil {
		t.Fatal(err)
	}
	x, err := OpenExtractor(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if _, _, err := x.File("planted/escape.txt", 0o644, strings.NewReader("x"), 1); err == nil {
		t.Fatal("write through a directory symlink out of the root succeeded")
	}
	if _, _, err := x.File("file-link", 0o644, strings.NewReader("x"), 1); err == nil {
		t.Fatal("write through a file symlink succeeded")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("extraction wrote outside its root: %v", entries)
	}
}

func TestReserveBlocksArchiveFromProducingAPath(t *testing.T) {
	x, err := OpenExtractor(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if err := x.Reserve("upload.tar.gz"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := x.File("./upload.tar.gz", 0o644, strings.NewReader("x"), 1); err == nil {
		t.Fatal("archive overwrote a reserved path")
	}
}

func TestTarAndZipKinds(t *testing.T) {
	for typeflag, ok := range map[byte]bool{tar.TypeReg: true, tar.TypeDir: true, tar.TypeSymlink: false, tar.TypeLink: false, tar.TypeChar: false, tar.TypeFifo: false, tar.TypeGNUSparse: false, 'Z': false} {
		if _, err := TarKind(&tar.Header{Name: "x", Typeflag: typeflag}); (err == nil) != ok {
			t.Errorf("TarKind(%q) err = %v", typeflag, err)
		}
	}
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	link := &zip.FileHeader{Name: "link"}
	link.SetMode(os.ModeSymlink | 0o777)
	for _, header := range []*zip.FileHeader{{Name: "file.txt"}, {Name: "dir/"}, link} {
		if _, err := zw.CreateHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	_ = zw.Close()
	reader, err := zip.NewReader(bytes.NewReader(zipped.Bytes()), int64(zipped.Len()))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"file.txt": true, "dir/": true, "link": false}
	for _, file := range reader.File {
		if _, err := ZipKind(file); (err == nil) != want[file.Name] {
			t.Errorf("ZipKind(%q) err = %v", file.Name, err)
		}
	}
}

func TestDirSpoolBoundsAndChecksCapacity(t *testing.T) {
	dir := t.TempDir()
	spooled, err := (DirSpool{Dir: dir}).Store(strings.NewReader("archive"), 7)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(spooled)
	if string(data) != "archive" || spooled.Size != 7 {
		t.Fatalf("spooled %q (%d bytes)", data, spooled.Size)
	}
	info, err := os.Stat(spooled.Name())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("spool file mode = %v, %v", info, err)
	}
	if err := spooled.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := (DirSpool{Dir: dir}).Store(strings.NewReader("too long"), 3); !errors.Is(err, ErrSpoolLimit) {
		t.Fatalf("over-limit spool err = %v", err)
	}
	full := errors.New("disk full")
	if _, err := (DirSpool{Dir: dir, Check: func(int64) error { return full }}).Store(strings.NewReader("x"), 10); !errors.Is(err, full) {
		t.Fatalf("capacity check err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("failed spools left files behind: %v", entries)
	}
	if _, err := (DirSpool{Dir: ""}).Store(strings.NewReader("x"), 1); err == nil {
		t.Fatal("spool without a directory accepted")
	}
}
