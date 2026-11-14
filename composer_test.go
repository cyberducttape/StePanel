package stepanel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gitHead reads the checkout's commit without running git in the
// tenant-controlled tree, and refuses refs or links that leave it.
func TestGitHeadReadsTheCheckoutWithoutRunningGit(t *testing.T) {
	commit := strings.Repeat("ab", 20)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "refs", "heads"), 0750); err != nil {
		t.Fatal(err)
	}
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, ".git", name), []byte(data), 0640); err != nil {
			t.Fatal(err)
		}
	}
	write("HEAD", "ref: refs/heads/main\n")
	write("refs/heads/main", commit+"\n")
	if got := gitHead(root); got != commit {
		t.Fatalf("loose ref: gitHead = %q", got)
	}
	if err := os.Remove(filepath.Join(root, ".git", "refs", "heads", "main")); err != nil {
		t.Fatal(err)
	}
	write("packed-refs", "# pack-refs with: peeled\n"+commit+" refs/heads/main\n")
	if got := gitHead(root); got != commit {
		t.Fatalf("packed ref: gitHead = %q", got)
	}
	write("HEAD", commit+"\n")
	if got := gitHead(root); got != commit {
		t.Fatalf("detached HEAD: gitHead = %q", got)
	}
	for _, head := range []string{"ref: refs/heads/../../../../etc/passwd\n", "ref: /etc/passwd\n", "not a commit\n"} {
		write("HEAD", head)
		if got := gitHead(root); got != "" {
			t.Fatalf("HEAD %q: gitHead = %q, want none", head, got)
		}
	}
	// A HEAD that links outside the tree is not followed.
	outside := filepath.Join(t.TempDir(), "HEAD")
	if err := os.WriteFile(outside, []byte(commit), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ".git", "HEAD")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".git", "HEAD")); err != nil {
		t.Fatal(err)
	}
	if got := gitHead(root); got != "" {
		t.Fatalf("HEAD linked outside the tree: gitHead = %q, want none", got)
	}
}
