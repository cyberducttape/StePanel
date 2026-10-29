package stepanel

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

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
