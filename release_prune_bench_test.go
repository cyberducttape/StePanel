package stepanel

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makePruneReleases lays out releases previous trees of filesPerRelease
// files each, oldest first.
func makePruneReleases(b *testing.B, root string, releases, filesPerRelease int) {
	b.Helper()
	base := time.Now().Add(-time.Hour)
	for r := 0; r < releases; r++ {
		dir := filepath.Join(root, fmt.Sprintf(".stepanel-previous-%04d", r))
		for f := 0; f < filesPerRelease; f++ {
			sub := filepath.Join(dir, fmt.Sprintf("d%02d", f%32))
			if err := os.MkdirAll(sub, 0o755); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("f%05d.php", f)), []byte("<?php echo 1;"), 0o644); err != nil {
				b.Fatal(err)
			}
		}
		modified := base.Add(time.Duration(r) * time.Minute)
		if err := os.Chtimes(dir, modified, modified); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPruneReleasesSteadyState is one deployment's retention pass:
// retain+1 previous trees, one of which is pruned.
func BenchmarkPruneReleasesSteadyState(b *testing.B) {
	for _, files := range []int{2000, 20000} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				root := b.TempDir()
				makePruneReleases(b, root, 4, files)
				b.StartTimer()
				if err := pruneReleasesWithPrefix(root, ".stepanel-previous-", 3, 0, 1<<40); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkPruneReleasesBacklog prunes an accumulated backlog of releases.
func BenchmarkPruneReleasesBacklog(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		root := b.TempDir()
		makePruneReleases(b, root, 20, 5000)
		b.StartTimer()
		if err := pruneReleasesWithPrefix(root, ".stepanel-previous-", 3, 0, 1<<40); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPruneReleaseWalkVersusRemove separates one sizing walk from one
// removal of the same 20,000-file release.
func BenchmarkPruneReleaseWalkVersusRemove(b *testing.B) {
	b.Run("size-walk", func(b *testing.B) {
		root := b.TempDir()
		makePruneReleases(b, root, 1, 20000)
		dir := filepath.Join(root, ".stepanel-previous-0000")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := gitReleaseSize(dir); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("remove", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			root := b.TempDir()
			makePruneReleases(b, root, 1, 20000)
			b.StartTimer()
			if err := os.RemoveAll(filepath.Join(root, ".stepanel-previous-0000")); err != nil {
				b.Fatal(err)
			}
		}
	})
}
