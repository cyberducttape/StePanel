package sitefiles

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testSiteAccess string

func (s testSiteAccess) SiteID() string   { return string(s) }
func (s testSiteAccess) TenantID() string { return "test-tenant" }

func TestDownloadTimeoutIsFiniteAndSizeAware(t *testing.T) {
	if got := DownloadTimeout(0); got != 30*time.Second {
		t.Fatalf("empty download timeout = %s", got)
	}
	if got, want := DownloadTimeout(10<<20), 10*time.Second+time.Minute; got != want {
		t.Fatalf("10 MiB download timeout = %s, want %s", got, want)
	}
	if got := DownloadTimeout(MaxDownloadBytes); got <= time.Minute || got > 12*time.Hour {
		t.Fatalf("maximum download timeout = %s, want a bounded size-aware deadline", got)
	}
	if got := DownloadTimeout(1<<63 - 1); got != 12*time.Hour {
		t.Fatalf("oversized timeout input = %s, want capped deadline", got)
	}
}

func TestCleanAcceptsOnlyPlainRelativePaths(t *testing.T) {
	for input, want := range map[string]string{"": "", ".": "", "/": "", "a": "a", "/a/b.php": "a/b.php", ".well-known/x": ".well-known/x", "x y/z": "x y/z"} {
		if got, err := Clean(input); err != nil || got != want {
			t.Errorf("Clean(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"..", "a/..", "a/../b", "a/./b", "a//b", "a/", "a\nb", "a\x00b", strings.Repeat("a", 256), strings.Repeat("a/", 2049)} {
		if got, err := Clean(input); err == nil {
			t.Errorf("Clean(%q) = %q, want an error", input, got)
		}
	}
	if _, err := CleanTarget("/"); err == nil {
		t.Error("CleanTarget accepted the document root")
	}
}

func TestParseListing(t *testing.T) {
	output := "f\t12\t644\t1700000000.5\tindex.php\nd\t4096\t2750\t1700000001.0\twp-content\nl\t7\t777\t1700000002.0\tlink\n"
	listing, err := ParseListing("", []byte(output))
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Entries) != 3 || listing.Entries[0].Name != "wp-content" || listing.Entries[0].Type != "dir" ||
		listing.Entries[1].Name != "index.php" || listing.Entries[1].Size != 12 || listing.Entries[2].Type != "link" || listing.Truncated {
		t.Fatalf("listing = %+v", listing)
	}
	for _, bad := range []string{"f\t12\t644\tindex.php\n", "f\t-1\t644\t1\tx\n", "f\t1\t999\t1\tx\n", "f\t1\t644\t1\ta/b\n", "f\t1\t644\tnow\tx\n"} {
		if _, err := ParseListing("", []byte(bad)); err == nil {
			t.Errorf("ParseListing(%q) succeeded", bad)
		}
	}
	var many strings.Builder
	for i := 0; i <= MaxEntries; i++ {
		many.WriteString("f\t1\t644\t1\tfile" + strings.Repeat("x", i%7) + "\n")
	}
	if listing, err := ParseListing("", []byte(many.String())); err != nil || !listing.Truncated || len(listing.Entries) != MaxEntries {
		t.Fatalf("over-long listing: truncated=%v entries=%d err=%v", listing.Truncated, len(listing.Entries), err)
	}
	marker := []byte("f\t1\t644\t1\tvisible\n!\t0\t0\t0\t<truncated>\n")
	if listing, err := ParseListing("", marker); err != nil || !listing.Truncated || len(listing.Entries) != 1 {
		t.Fatalf("source-limited listing marker: truncated=%v entries=%d err=%v", listing.Truncated, len(listing.Entries), err)
	}
}

func newLocal(t *testing.T) (Local, string) {
	t.Helper()
	dir := t.TempDir()
	return Local{DocRoot: func(site string) (string, error) {
		if site != "alice" {
			return "", errors.New("unknown site")
		}
		return dir, nil
	}}, dir
}

func TestLocalStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	store, dir := newLocal(t)
	alice := testSiteAccess("alice")
	if err := store.Mkdir(ctx, alice, "assets"); err != nil {
		t.Fatal(err)
	}
	if err := store.Mkdir(ctx, alice, "assets"); err == nil {
		t.Fatal("an existing directory was created again")
	}
	if err := store.Write(ctx, alice, "assets/site.css", bytes.NewReader([]byte("body{}")), ""); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, "assets", "site.css")); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("new file = %v, %v", info, err)
	}
	// An existing file keeps its permissions when replaced.
	if err := os.Chmod(filepath.Join(dir, "assets", "site.css"), 0o604); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(ctx, alice, "assets/site.css", bytes.NewReader([]byte("main{}")), ""); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, "assets", "site.css")); err != nil || info.Mode().Perm() != 0o604 {
		t.Fatalf("replaced file = %v, %v", info, err)
	}
	data, err := store.Read(ctx, alice, "assets/site.css")
	if err != nil || string(data) != "main{}" {
		t.Fatalf("read = %q, %v", data, err)
	}
	listing, err := store.List(ctx, alice, "")
	if err != nil || len(listing.Entries) != 1 || listing.Entries[0].Name != "assets" || listing.Entries[0].Type != "dir" {
		t.Fatalf("root listing = %+v, %v", listing, err)
	}
	if err := store.Rename(ctx, alice, "assets", "assets/nested"); err == nil {
		t.Fatal("a directory was moved into itself")
	}
	if err := store.Rename(ctx, alice, "assets/site.css", "style.css"); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(ctx, alice, "assets/other.css", bytes.NewReader(nil), ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Rename(ctx, alice, "style.css", "assets/other.css"); err == nil {
		t.Fatal("a rename replaced an existing file")
	}
	if err := store.Delete(ctx, alice, "assets"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(ctx, alice, "assets/other.css"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read after delete = %v", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 || entries[0].Name() != "style.css" {
		t.Fatalf("document root = %v, %v (temporary files must not remain)", entries, err)
	}
}

func TestLocalRenameDoesNotOverwriteDestinationCreatedAtPublication(t *testing.T) {
	store, dir := newLocal(t)
	alice := testSiteAccess("alice")
	source := filepath.Join(dir, "source.txt")
	destination := filepath.Join(dir, "destination.txt")
	if err := os.WriteFile(source, []byte("source"), 0o640); err != nil {
		t.Fatal(err)
	}
	store.beforeRename = func() {
		if err := os.WriteFile(destination, []byte("concurrent writer"), 0o640); err != nil {
			t.Errorf("create racing destination: %v", err)
		}
	}
	err := store.Rename(context.Background(), alice, "source.txt", "destination.txt")
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("racing destination rename error = %v; want a destination conflict", err)
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != "concurrent writer" {
		t.Fatalf("racing destination = %q, %v", got, err)
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != "source" {
		t.Fatalf("source after rejected rename = %q, %v", got, err)
	}
}

func TestLocalStoreConditionalWriteRejectsStaleRevision(t *testing.T) {
	ctx := context.Background()
	store, dir := newLocal(t)
	alice := testSiteAccess("alice")
	current := []byte("newer saved version")
	if err := os.WriteFile(filepath.Join(dir, "page.php"), current, 0o640); err != nil {
		t.Fatal(err)
	}
	staleRevision := Revision([]byte("version from when editor opened"))
	err := store.Write(ctx, alice, "page.php", bytes.NewReader([]byte("stale overwrite")), staleRevision)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale conditional write error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "page.php"))
	if err != nil || !bytes.Equal(got, current) {
		t.Fatalf("stale write changed the current file to %q: %v", got, err)
	}
	if err := store.Write(ctx, alice, "page.php", bytes.NewReader([]byte("accepted update")), Revision(current)); err != nil {
		t.Fatalf("write against current revision: %v", err)
	}
}

func TestLocalStoreRefusesLinksAndLimits(t *testing.T) {
	ctx := context.Background()
	store, dir := newLocal(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	alice := testSiteAccess("alice")
	if _, err := store.Read(ctx, alice, "escape/secret"); err == nil {
		t.Fatal("a read followed a link out of the document root")
	}
	if err := store.Write(ctx, alice, "escape/new", bytes.NewReader([]byte("x")), ""); err == nil {
		t.Fatal("a write followed a link out of the document root")
	}
	if _, err := store.List(ctx, alice, "escape"); err == nil {
		t.Fatal("a listing followed a link out of the document root")
	}
	// Deleting the link removes only the link.
	if err := store.Delete(ctx, alice, "escape"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "secret")); err != nil {
		t.Fatalf("deleting a link removed its target: %v", err)
	}
	if err := store.Write(ctx, alice, "big", bytes.NewReader(make([]byte, MaxWriteBytes+1)), ""); !errors.Is(err, ErrTooLarge) {
		t.Fatal("an oversized write was accepted")
	}
	if err := store.Write(ctx, alice, "missing/file", bytes.NewReader([]byte("x")), ""); err == nil {
		t.Fatal("a write into a missing directory was accepted")
	}
	if _, err := store.List(ctx, testSiteAccess("bob"), ""); err == nil {
		t.Fatal("an unknown site was listed")
	}
	if _, err := store.List(ctx, nil, ""); err == nil {
		t.Fatal("a missing authorization capability was accepted")
	}
	if _, err := store.List(ctx, testSiteAccess(""), ""); err == nil {
		t.Fatal("an empty authorization capability was accepted")
	}
}

func TestLocalStoreWriteLargeUsesAtomicPathAndKeepsSiteRootConfinement(t *testing.T) {
	ctx := context.Background()
	store, dir := newLocal(t)
	content := bytes.Repeat([]byte("large"), (MaxWriteBytes+4)/5)
	if err := store.WriteLarge(ctx, testSiteAccess("alice"), "uploads/artifact.bin", bytes.NewReader(content), ""); err == nil {
		t.Fatal("write into a missing parent directory succeeded")
	}
	if err := os.Mkdir(filepath.Join(dir, "uploads"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteLarge(ctx, testSiteAccess("alice"), "uploads/artifact.bin", bytes.NewReader(content), ""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "uploads/artifact.bin"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("large write length=%d, err=%v", len(got), err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteLarge(ctx, testSiteAccess("alice"), "escape/artifact.bin", bytes.NewReader(content), ""); err == nil {
		t.Fatal("large write followed a symlink outside the site root")
	}
}

func TestReadBoundedRejectsFileGrowthAfterStat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.log")
	if err := os.WriteFile(path, []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() != 4 {
		t.Fatalf("initial stat size = %v, %v", info, err)
	}
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := readBounded(file, 4)
	var refused *RefusedError
	if !errors.As(err, &refused) || data != nil {
		t.Fatalf("read after growth = %q, %v; expected refusal", data, err)
	}
}
