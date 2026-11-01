package stepanel

import (
	"fmt"
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

// Retention keeps the newest prefix allowed by every rule at once, never
// drops the newest previous tree, and removes only what the rules exclude.
func TestPruneReleasesAppliesCountAgeAndBytesTogether(t *testing.T) {
	cases := []struct {
		name     string
		retain   int
		maxAge   time.Duration
		maxBytes int64
		want     int
	}{
		{"count", 3, 0, 1 << 40, 3},
		{"bytes trim the count prefix", 3, 0, 25, 2},
		{"newest survives an impossible byte limit", 3, 0, 1, 1},
		{"age", 5, 150 * time.Minute, 1 << 40, 2},
		{"tightest rule wins", 5, 150 * time.Minute, 15, 1},
		{"all kept", 10, 0, 1 << 40, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			// Five 10-byte releases, newest (4) one hour old, oldest five hours.
			for i := 0; i < 5; i++ {
				dir := filepath.Join(root, fmt.Sprintf(".stepanel-previous-%d", i))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("0123456789"), 0o644); err != nil {
					t.Fatal(err)
				}
				modified := time.Now().Add(-time.Duration(5-i) * time.Hour)
				if err := os.Chtimes(dir, modified, modified); err != nil {
					t.Fatal(err)
				}
			}
			if err := pruneReleasesWithPrefix(root, ".stepanel-previous-", tc.retain, tc.maxAge, tc.maxBytes); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != tc.want {
				t.Fatalf("kept %d releases, want %d", len(entries), tc.want)
			}
			// The kept releases are the newest ones.
			for _, entry := range entries {
				var index int
				if _, err := fmt.Sscanf(entry.Name(), ".stepanel-previous-%d", &index); err != nil || index < 5-tc.want {
					t.Fatalf("kept %s, not one of the newest %d", entry.Name(), tc.want)
				}
			}
		})
	}
}
