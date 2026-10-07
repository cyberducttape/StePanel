package backup

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateTreeReturnsRegularFileSize(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.php"), []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := ValidateTree(root, 10); err != nil || got != 4 {
		t.Fatalf("ValidateTree = (%d, %v), want (4, nil)", got, err)
	}
}

func TestValidateTreeRejectsInvalidArguments(t *testing.T) {
	if _, err := ValidateTree("", 100); err == nil {
		t.Fatal("ValidateTree accepted an empty root")
	}
	if _, err := ValidateTree(t.TempDir(), 0); err == nil {
		t.Fatal("ValidateTree accepted a non-positive maximum")
	}
	if _, err := ValidateTree(filepath.Join(t.TempDir(), "missing"), 100); err == nil {
		t.Fatal("ValidateTree accepted a missing root")
	}
}

func TestValidateTreeRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "uploads")); err != nil {
		t.Fatal(err)
	}
	_, err := ValidateTree(root, 100)
	if err == nil || !strings.Contains(err.Error(), "must not contain symlinks") {
		t.Fatalf("ValidateTree error = %v, want explicit symlink policy", err)
	}
}

func TestValidateTreeRejectsSpecialFile(t *testing.T) {
	root := t.TempDir()
	listener, err := net.Listen("unix", filepath.Join(root, "socket"))
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer listener.Close()
	_, err = ValidateTree(root, 100)
	if err == nil || !strings.Contains(err.Error(), "unsupported special file") {
		t.Fatalf("ValidateTree error = %v, want special-file rejection", err)
	}
}

func TestValidateTreeRejectsSizeLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large"), []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateTree(root, 4); err == nil || !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Fatalf("ValidateTree error = %v, want size-limit rejection", err)
	}
}
