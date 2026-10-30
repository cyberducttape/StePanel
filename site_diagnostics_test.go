package stepanel

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInspectFilesystemCapacityIncludesEvidence(t *testing.T) {
	root := t.TempDir()
	got := inspectFilesystemCapacity("test volume", root)
	if got["status"] != "healthy" {
		t.Fatalf("status = %v, want healthy; diagnostic: %#v", got["status"], got)
	}
	for _, key := range []string{"available_bytes", "total_bytes", "available_inodes", "total_inodes", "collected_at", "detail"} {
		if _, ok := got[key]; !ok {
			t.Errorf("capacity evidence missing %q: %#v", key, got)
		}
	}
	if at, ok := got["collected_at"].(time.Time); !ok || at.IsZero() {
		t.Errorf("collected_at = %#v, want timestamp", got["collected_at"])
	}
}

func TestInspectFilesystemCapacityUnknownPathIsExplicit(t *testing.T) {
	got := inspectFilesystemCapacity("backup volume", filepath.Join(t.TempDir(), "missing"))
	if got["status"] != "unknown" {
		t.Fatalf("status = %v, want unknown", got["status"])
	}
	if got["detail"] == "" || got["collected_at"] == nil {
		t.Fatalf("unavailable probe must carry explanation and time: %#v", got)
	}
}

func TestOpenManagedVhostRejectsPathAndSymlink(t *testing.T) {
	root := t.TempDir()
	name := "site-demo-example_test.conf"
	want := "example.test {\n}\n"
	if err := os.WriteFile(filepath.Join(root, name), []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	file, _, err := openManagedVhost(root, name)
	if err != nil {
		t.Fatalf("open managed vhost: %v", err)
	}
	got, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || string(got) != want {
		t.Fatalf("read managed vhost: got %q, err %v", got, err)
	}
	if _, _, err := openManagedVhost(root, "../outside.conf"); err == nil {
		t.Fatal("path traversal name was accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside.conf")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "site-demo-outside.conf")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openManagedVhost(root, "site-demo-outside.conf"); err == nil {
		t.Fatal("symlinked vhost was accepted")
	}
}
