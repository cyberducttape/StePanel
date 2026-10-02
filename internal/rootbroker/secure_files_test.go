package rootbroker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSecureSealTreeRejectsSymlinkWithoutChangingTarget(t *testing.T) {
	root := t.TempDir()
	public := filepath.Join(root, "public")
	if err := os.Mkdir(public, 0750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(public, "link")); err != nil {
		t.Fatal(err)
	}

	if err := secureSealTree(context.Background(), public); err == nil {
		t.Fatal("secureSealTree accepted a symlink")
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("outside target mode = %o, want 600", got)
	}
}

func TestValidateFilePathRejectsIntermediateSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "customer")); err != nil {
		t.Fatal(err)
	}
	validator := NewValidator(root)
	if err := validator.ValidateFilePath(root, filepath.Join("customer", "public")); err == nil {
		t.Fatal("ValidateFilePath accepted an intermediate symlink")
	}
}

func TestValidateDumpPathRejectsIntermediateSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	dump := filepath.Join(outside, "dump.sql")
	if err := os.WriteFile(dump, []byte("SELECT 1;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "staging")); err != nil {
		t.Fatal(err)
	}

	validator := NewValidator(root)
	if err := validator.validateDumpPath(filepath.Join(root, "staging", "dump.sql")); err == nil {
		t.Fatal("validateDumpPath accepted a symlinked parent")
	}
}
