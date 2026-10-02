package importer

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTarGz(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tarWriter := tar.NewWriter(gz)
	entries := []struct {
		name string
		mode int64
		data string
	}{
		{"wp-content/", 0755, ""},
		{"wp-admin/index.php", 0644, "<?php echo 'ok';"},
		{"wp-config.php", 0600, "<?php define('DB_NAME', 'site_db'); define('DB_USER', 'site_user');"},
		{"backup.sql", 0644, "CREATE TABLE posts (id INT);"},
	}
	for _, entry := range entries {
		typeflag := byte(tar.TypeReg)
		if strings.HasSuffix(entry.name, "/") {
			typeflag = tar.TypeDir
		}
		h := &tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(entry.data)), Typeflag: typeflag}
		if err := tarWriter.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tarWriter, entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func makeZip(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for name, content := range map[string]string{
		"wp-config.php":                  "<?php define('DB_NAME', 'zip_db'); define('DB_USER', 'zip_user');",
		"wp-content/plugins/example.php": "<?php echo 'plugin';",
		"database.sql.gz":                "compressed dump marker",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestAnalyzerInspectsTarGzAndZipEntries(t *testing.T) {
	analyzer := &Analyzer{}
	for _, tc := range []struct {
		name string
		data []byte
		zip  bool
	}{
		{"tar.gz", makeTarGz(t), false},
		{"zip", makeZip(t), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inspection := &ArchiveInspection{Issues: []ImportIssue{}}
			if tc.zip {
				path := t.TempDir() + "/archive.zip"
				if err := os.WriteFile(path, tc.data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := analyzer.inspectZip(context.Background(), path, "wp-config.php", inspection); err != nil {
					t.Fatal(err)
				}
			} else if err := analyzer.inspectTarGz(context.Background(), bytes.NewReader(tc.data), "wp-config.php", inspection); err != nil {
				t.Fatal(err)
			}
			if inspection.Structure.TotalFiles == 0 || !inspection.Structure.HasWordPressCore || !inspection.Structure.HasDatabase {
				t.Fatalf("inspection missed archive structure: %+v", inspection.Structure)
			}
			if inspection.Requirements.DatabaseName == "" || inspection.Requirements.DatabaseUser == "" {
				t.Fatalf("database requirements missing: %+v", inspection.Requirements)
			}
		})
	}
}

func TestArchiveSafetyHelpersAndLimits(t *testing.T) {
	for _, path := range []string{"../escape", "/absolute", "a/../../escape"} {
		if _, err := safeTarExtractPath(t.TempDir(), path); err == nil {
			t.Errorf("safeTarExtractPath accepted %q", path)
		}
	}
	if _, err := safeTarExtractPath(t.TempDir(), "public/index.php"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "/etc/passwd", "../config.php"} {
		if _, err := safeConfigPath(t.TempDir(), path); err == nil {
			t.Errorf("safeConfigPath accepted %q", path)
		}
	}
	if _, err := safeConfigPath(t.TempDir(), "wp-config.php"); err != nil {
		t.Fatal(err)
	}
	if formatBytes(1024) != "1.0 KB" || formatBytes(1024*1024) != "1.0 MB" {
		t.Fatal("formatBytes did not use expected units")
	}
	if got := maxInt64(0, 7); got != 7 || min(2, 3) != 2 {
		t.Fatal("integer helpers returned unexpected values")
	}
	if !CheckDiskSpace(t.TempDir(), 1, 1).HasSufficientSpace {
		t.Fatal("small temporary import should have sufficient space")
	}
}

func TestExecutorExtractsArchivesAndRejectsSpecialEntries(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		zip  bool
	}{
		{"tar.gz", makeTarGz(t), false},
		{"zip", makeZip(t), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			job := &ImportJob{WebRoot: root}
			executor := NewExecutor()
			var err error
			if tc.zip {
				err = executor.extractZip(bytes.NewReader(tc.data), job, func(*ImportJob) {})
			} else {
				err = executor.extractTarGz(bytes.NewReader(tc.data), job, func(*ImportJob) {})
			}
			if err != nil {
				t.Fatal(err)
			}
			if job.FilesExtracted == 0 {
				t.Fatal("archive extraction did not write files")
			}
			if _, err := os.Stat(filepath.Join(root, "wp-config.php")); err != nil {
				t.Fatal("config file was not extracted: ", err)
			}
		})
	}

	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	job := &ImportJob{WebRoot: t.TempDir()}
	if err := (&Executor{}).extractTarGz(bytes.NewReader(archive.Bytes()), job, func(*ImportJob) {}); err != nil {
		t.Fatal("symlink entry should be skipped: ", err)
	}
	if _, err := os.Lstat(filepath.Join(job.WebRoot, "escape")); !os.IsNotExist(err) {
		t.Fatal("symlink entry escaped or was materialized")
	}
	var special bytes.Buffer
	gz = gzip.NewWriter(&special)
	tw = tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "device", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (&Executor{}).extractTarGz(bytes.NewReader(special.Bytes()), &ImportJob{WebRoot: t.TempDir()}, func(*ImportJob) {}); err == nil {
		t.Fatal("unsupported device entry was accepted")
	}
}

func TestExtractZipRejectsOversizedDeclaredEntry(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	data := []byte("not actually oversized")
	writer, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "oversized",
		Method:             zip.Store,
		UncompressedSize64: maxIndividualFileSize + 1,
		CompressedSize64:   uint64(len(data)),
		CRC32:              crc32.ChecksumIEEE(data),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	err = (&Executor{}).extractZip(bytes.NewReader(archive.Bytes()), &ImportJob{WebRoot: t.TempDir()}, func(*ImportJob) {})
	if err == nil {
		t.Fatal("ZIP entry over the individual size limit was accepted")
	}
}

func TestArchiveFetcherRejectsBadResponsesAndLimitsBody(t *testing.T) {
	newFetcher := func(response *http.Response, requestErr error) *ArchiveFetcher {
		return &ArchiveFetcher{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response, requestErr
		})}}
	}
	base := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("payload")), Header: make(http.Header)}
	if _, err := newFetcher(base, nil).FetchArchive(context.Background(), "http://example.com/archive.zip", 100); err == nil {
		t.Fatal("FetchArchive accepted HTTP URL")
	}
	if _, err := newFetcher(&http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("no"))}, nil).FetchArchive(context.Background(), "https://example.com/archive.zip", 100); err == nil {
		t.Fatal("FetchArchive accepted non-200 response")
	}
	tooLarge := &http.Response{StatusCode: http.StatusOK, ContentLength: 101, Body: io.NopCloser(strings.NewReader("payload"))}
	if _, err := newFetcher(tooLarge, nil).FetchArchive(context.Background(), "https://example.com/archive.zip", 100); err == nil {
		t.Fatal("FetchArchive accepted oversized Content-Length")
	}
	body, err := newFetcher(base, nil).FetchArchive(context.Background(), "https://example.com/archive.zip", 4)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(body)
	_ = body.Close()
	if readErr != nil || string(data) != "payl" {
		t.Fatalf("limited body = %q, err=%v", data, readErr)
	}
	if _, err := newFetcher(nil, errors.New("transport failed")).FetchArchive(context.Background(), "https://example.com/archive.zip", 100); err == nil {
		t.Fatal("FetchArchive hid transport error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
