package rootbroker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// makeSealTree creates files spread over directories of perDir entries.
func makeSealTree(t testing.TB, root string, files, perDir int) {
	t.Helper()
	for i := 0; i < files; i++ {
		dir := filepath.Join(root, "d"+strconv.Itoa(i/perDir))
		if i%perDir == 0 {
			if err := os.Mkdir(dir, 0o777); err != nil {
				t.Fatal(err)
			}
		}
		f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("cache-entry-with-a-long-name-%08d", i)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}

func sealPeakHeap(t *testing.T, root string) (uint64, int) {
	t.Helper()
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak uint64
	visited := 0
	sealVisitHook = func() {
		visited++
		if visited%2048 == 0 {
			// Measure live bytes the walk retains, not garbage awaiting
			// collection, whose amount depends on GC timing.
			runtime.GC()
			var now runtime.MemStats
			runtime.ReadMemStats(&now)
			if now.HeapAlloc > base.HeapAlloc && now.HeapAlloc-base.HeapAlloc > peak {
				peak = now.HeapAlloc - base.HeapAlloc
			}
		}
	}
	defer func() { sealVisitHook = nil }()
	if err := secureSealTree(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	return peak, visited
}

// F-04: sealing must not retain the tree. A tree ten times larger must not
// need materially more heap, and every entry must still be sealed.
func TestSecureSealTreeMemoryDoesNotGrowWithEntryCount(t *testing.T) {
	if raceDetectorEnabled {
		// The race detector's shadow memory grows with every access and
		// swamps the measurement; the plain test run enforces this bound.
		t.Skip("heap growth is not measurable under the race detector")
	}
	small, large := 4_000, 40_000
	if os.Getenv("STEPANEL_SEAL_STRESS") != "" {
		large = 1_000_000 // million-entry run: STEPANEL_SEAL_STRESS=1
	}
	smallRoot, largeRoot := t.TempDir(), t.TempDir()
	makeSealTree(t, smallRoot, small, 1000)
	makeSealTree(t, largeRoot, large, 20_000)

	smallPeak, _ := sealPeakHeap(t, smallRoot)
	largePeak, visited := sealPeakHeap(t, largeRoot)
	if visited < 2*large {
		t.Fatalf("visited %d entries over two passes, want at least %d", visited, 2*large)
	}
	// A retained path list for the large tree would need well over 2 MiB
	// (40,000 names of ~60 bytes plus string headers); the streaming walk
	// stays within 256 KiB of the small tree's live peak.
	const allowance = 256 << 10
	t.Logf("peak live heap: %d bytes for %d entries, %d bytes for %d", smallPeak, small, largePeak, large)
	if largePeak > smallPeak+allowance {
		t.Fatalf("peak heap grew with the tree: %d bytes for %d entries vs %d bytes for %d", largePeak, large, smallPeak, small)
	}
	var checked int
	if err := filepath.WalkDir(largeRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(0o640)
		if entry.IsDir() {
			want = 0o750
		}
		if info.Mode().Perm() != want {
			return fmt.Errorf("%s mode %o, want %o", path, info.Mode().Perm(), want)
		}
		checked++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if checked < large {
		t.Fatalf("checked %d entries", checked)
	}
}

// The first pass validates the whole tree, so an unsafe entry found late in
// the walk refuses the seal before any permission changes.
func TestSecureSealTreeChangesNothingWhenAnyEntryIsUnsafe(t *testing.T) {
	root := t.TempDir()
	makeSealTree(t, root, 600, 300)
	last := filepath.Join(root, "zz-late")
	if err := os.Mkdir(last, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(last, "link")); err != nil {
		t.Fatal(err)
	}
	if err := secureSealTree(context.Background(), root); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("seal with a late symlink = %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "d0", "cache-entry-with-a-long-name-00000000"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() == 0o640 {
		t.Fatal("entries were sealed before the unsafe entry was found")
	}
}

// A FIFO must be refused without blocking the broker on open.
func TestSecureSealTreeRefusesFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- secureSealTree(context.Background(), root) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "unsupported seal entry type") {
			t.Fatalf("seal with a FIFO = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("seal blocked on a FIFO")
	}
}

func TestSecureSealTreeBoundsDepthAndHonoursCancellation(t *testing.T) {
	root := t.TempDir()
	deep := root
	for i := 0; i <= maxSealDepth; i++ {
		deep = filepath.Join(deep, "d")
		if err := os.Mkdir(deep, 0o755); err != nil {
			t.Skipf("cannot build a %d-level tree here: %v", maxSealDepth, err)
		}
	}
	if err := secureSealTree(context.Background(), root); err == nil || !strings.Contains(err.Error(), "nested deeper") {
		t.Fatalf("over-deep tree = %v", err)
	}

	wide := t.TempDir()
	makeSealTree(t, wide, 2_000, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	seen := 0
	sealVisitHook = func() {
		seen++
		if seen == 100 {
			cancel()
		}
	}
	defer func() { sealVisitHook = nil }()
	if err := secureSealTree(ctx, wide); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled seal = %v", err)
	}
}

// NEW-03: entries are classified by a descriptor-relative fstatat, never by
// the directory entry's d_type, which a filesystem may report as unknown.
func TestSealClassifiesEntriesByDescriptorNotDirentType(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	for name, want := range map[string]uint32{"dir": syscall.S_IFDIR, "file": syscall.S_IFREG} {
		if got, err := sealClassify(fd, name, name); err != nil || got != want {
			t.Errorf("sealClassify(%s) = %o, %v", name, got, err)
		}
	}
	for name, want := range map[string]string{"link": "symlink", "pipe": "unsupported", "missing": "no such file"} {
		if _, err := sealClassify(fd, name, name); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("sealClassify(%s) = %v, want %q", name, err, want)
		}
	}

	// The walk resolves entries against the directory descriptor, not the
	// working directory: a same-named entry of another type in the cwd
	// must not change how the tree is sealed.
	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, "nested", "deeper"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "nested", "deeper", "page.php"), nil, 0o666); err != nil {
		t.Fatal(err)
	}
	decoy := t.TempDir()
	if err := os.WriteFile(filepath.Join(decoy, "nested"), nil, 0o666); err != nil {
		t.Fatal(err)
	}
	t.Chdir(decoy)
	if err := secureSealTree(context.Background(), tree); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]os.FileMode{"nested": 0o750, "nested/deeper": 0o750, "nested/deeper/page.php": 0o640} {
		info, err := os.Lstat(filepath.Join(tree, rel))
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("%s = %v, %v; want %o", rel, info.Mode(), err, want)
		}
	}
}
