package stepanel

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Public and private repositories are both cloned by stepanel-gitctl (as
// the unprivileged stepanel-git identity), never by Git running as the
// panel's service account.
func TestPublicAndPrivateClonesGoThroughTheCloneHelper(t *testing.T) {
	root := t.TempDir()
	calls := filepath.Join(root, "calls")
	helper := filepath.Join(root, "fake-gitctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+calls+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{GitCtl: helper, GitAllowedHosts: "github.com"}
	for _, repository := range []string{"https://github.com/acme/shop.git", "git@github.com:acme/shop.git"} {
		if _, err := cloneGitRepository(context.Background(), cfg, "shop", repository, "main", "/stage/.stepanel-release-shop-1", 4096, 1000000001); err != nil {
			t.Fatalf("clone %s: %v", repository, err)
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	want := "clone shop https://github.com/acme/shop.git main /stage/.stepanel-release-shop-1 github.com 4096 1000000001\n" +
		"clone shop git@github.com:acme/shop.git main /stage/.stepanel-release-shop-1 github.com 4096 1000000001\n"
	if string(data) != want {
		t.Fatalf("clone helper calls = %q, want %q", data, want)
	}
}

// No Go code runs `git clone` itself; the clone helper owns cloning.
func TestNoGoCodeRunsGitCloneDirectly(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	direct := regexp.MustCompile(`"clone",\s*"--depth"`)
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if direct.Match(data) {
			t.Errorf("%s runs git clone directly instead of through stepanel-gitctl", source)
		}
	}
}
