package importer

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cyberducttape/StePanel/internal/archivesafe"
)

type tarEntry struct {
	name     string
	typeflag byte
	data     string
}

func buildTarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: e.typeflag, Mode: 0o644, Size: int64(len(e.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func buildZip(t *testing.T, names ...string) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(name, "/") {
			if _, err := io.WriteString(w, "content of "+name); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// A tar that names a file twice could have its first copy inspected and its
// second copy installed. Both the analyzer and the executor reject it, as
// they do every other collision.
func TestAnalyzerAndExecutorRejectDuplicateAndCollidingPaths(t *testing.T) {
	reg, dir := byte(tar.TypeReg), byte(tar.TypeDir)
	tars := map[string][]byte{
		"duplicate file":      buildTarGz(t, tarEntry{"wp-config.php", reg, "safe"}, tarEntry{"wp-config.php", reg, "replaced"}),
		"aliased duplicate":   buildTarGz(t, tarEntry{"a/index.php", reg, "1"}, tarEntry{"./a//index.php", reg, "2"}),
		"directory over file": buildTarGz(t, tarEntry{"cache", reg, "x"}, tarEntry{"cache/", dir, ""}),
		"entry under a file":  buildTarGz(t, tarEntry{"cache", reg, "x"}, tarEntry{"cache/page.html", reg, "y"}),
	}
	for name, data := range tars {
		t.Run("tar "+name, func(t *testing.T) {
			if err := (&Analyzer{}).inspectTarGz(context.Background(), bytes.NewReader(data), "wp-config.php", &ArchiveInspection{}); err == nil {
				t.Error("analyzer accepted the archive")
			}
			if err := (&Executor{}).extractTarGz(bytes.NewReader(data), &ImportJob{WebRoot: t.TempDir()}, func(*ImportJob) {}); err == nil {
				t.Error("executor accepted the archive")
			}
		})
	}
	zips := map[string][]byte{
		"backslash alias":     buildZip(t, "a/index.php", `a\index.php`),
		"file over directory": buildZip(t, "uploads/", "uploads"),
	}
	for name, data := range zips {
		t.Run("zip "+name, func(t *testing.T) {
			reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if err := (&Analyzer{}).inspectZip(context.Background(), reader, "wp-config.php", &ArchiveInspection{}); err == nil {
				t.Error("analyzer accepted the archive")
			}
			if err := (&Executor{}).extractZip(bytes.NewReader(data), &ImportJob{WebRoot: t.TempDir()}, func(*ImportJob) {}); err == nil {
				t.Error("executor accepted the archive")
			}
		})
	}
}

// The analyzer used to reject links only implicitly (it ignored them) while
// the executor refused them, so inspection could approve an archive that
// extraction then failed. Both now apply the same entry rules.
func TestAnalyzerRejectsEntriesTheExecutorRejects(t *testing.T) {
	data := buildTarGz(t, tarEntry{"index.php", tar.TypeReg, "<?php"}, tarEntry{"link", tar.TypeSymlink, ""})
	if err := (&Analyzer{}).inspectTarGz(context.Background(), bytes.NewReader(data), "wp-config.php", &ArchiveInspection{}); err == nil {
		t.Fatal("analyzer accepted a symlink entry")
	}
}

func servingFetcher(body []byte, contentType string) *ArchiveFetcher {
	return &ArchiveFetcher{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{contentType}},
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
		}, nil
	})}}
}

// The executor used to choose the format from a ".zip" URL suffix, so a ZIP
// behind a URL without one was fed to the gzip reader after the analyzer had
// approved it as a ZIP. Both now decide from the archive's bytes.
func TestExecutorDetectsFormatFromContentNotURL(t *testing.T) {
	for name, tc := range map[string]struct {
		url  string
		data []byte
	}{
		"zip without extension": {"https://downloads.example/archive?id=7", buildZip(t, "wp-config.php")},
		"tar.gz named .zip":     {"https://downloads.example/archive.zip", buildTarGz(t, tarEntry{"wp-config.php", tar.TypeReg, "<?php"})},
		"zip named .tar.gz":     {"https://downloads.example/archive.tar.gz", buildZip(t, "wp-config.php")},
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "site")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			executor := &Executor{fetcher: servingFetcher(tc.data, "application/octet-stream")}
			if err := executor.extractArchive(context.Background(), tc.url, &ImportJob{WebRoot: root}, func(*ImportJob) {}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "wp-config.php")); err != nil {
				t.Fatal("archive was not extracted: ", err)
			}
		})
	}
}

// ZIP archives are spooled beside the extraction root (or into the
// control plane's import root), never into the system temporary directory,
// and the spool is removed afterwards.
func TestZipSpoolAvoidsSystemTempDirectory(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	parent := t.TempDir()
	root := filepath.Join(parent, "site")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (&Executor{}).extractZip(bytes.NewReader(buildZip(t, "index.php")), &ImportJob{WebRoot: root}, func(*ImportJob) {}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("spool file left beside the extraction root: %v", entries)
	}

	spoolDir := t.TempDir()
	var checked bool
	spool := archivesafe.DirSpool{Dir: spoolDir, Check: func(int64) error { checked = true; return nil }}
	root2 := filepath.Join(parent, "site2")
	if err := os.Mkdir(root2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (&Executor{}).WithSpool(spool).extractZip(bytes.NewReader(buildZip(t, "index.php")), &ImportJob{WebRoot: root2}, func(*ImportJob) {}); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("configured spool's capacity check was not used")
	}

	data := buildZip(t, "wp-config.php")
	analyzer := &Analyzer{httpClient: servingFetcher(data, "application/zip").httpClient}
	if _, err := analyzer.InspectArchive(context.Background(), "https://downloads.example/archive.zip", "wp-config.php"); err == nil {
		t.Fatal("analyzer inspected a ZIP without a configured spool")
	}
	if _, err := analyzer.WithSpool(spool).InspectArchive(context.Background(), "https://downloads.example/archive", "wp-config.php"); err != nil {
		t.Fatalf("analyzer with spool: %v", err)
	}
}
