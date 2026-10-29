package usage

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMeasureBoundsAndSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Measure(root, 10)
	if err != nil || result.Bytes != 4 || result.Files != 1 || result.Inodes != 4 || !result.Complete {
		t.Fatalf("usage=%#v err=%v", result, err)
	}
	result, err = Measure(root, 1)
	if !errors.Is(err, ErrLimit) || result.Complete || result.Inodes != 1 {
		t.Fatalf("bounded usage=%#v err=%v", result, err)
	}
}

func TestMeasureCountsHardLinksOnce(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "original.txt")
	linked := filepath.Join(root, "linked.txt")
	if err := os.WriteFile(original, []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, linked); err != nil {
		t.Fatal(err)
	}
	result, err := Measure(root, 10)
	if err != nil || result.Bytes != 4 || result.Files != 1 || result.Inodes != 2 {
		t.Fatalf("usage=%#v err=%v; want one physical file", result, err)
	}
}
