package stepanel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneGitReleasesKeepsNewestRollback(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		path := filepath.Join(root, ".stepanel-previous-"+string(rune('a'+i)))
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "index.html"), []byte("ok"), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneGitReleases(root, 1); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 || entries[0].Name() != ".stepanel-previous-c" {
		t.Fatalf("retained=%v", entries)
	}
}
