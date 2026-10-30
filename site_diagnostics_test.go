package stepanel

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSiteWordPressDatabaseCheckUsesSiteWPCLIAndCachesEvidence(t *testing.T) {
	root := t.TempDir()
	public := filepath.Join(root, "sites", "shop", "public")
	if err := os.MkdirAll(public, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "wp-config.php"), []byte("<?php"), 0640); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "calls")
	wp := filepath.Join(root, "wp")
	if err := os.WriteFile(wp, []byte("#!/bin/sh\nprintf x >> \"$STEPANEL_TEST_WP_MARKER\"\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEPANEL_TEST_WP_MARKER", marker)
	a := &App{Config: Config{WebRoot: root, WPCLI: wp}}
	first := a.siteWordPressDatabaseCheck(context.Background(), "shop")
	if first.Status != "connected" || first.CollectedAt.IsZero() || first.Detail == "" {
		t.Fatalf("database probe = %+v, want timestamped connection evidence", first)
	}
	second := a.siteWordPressDatabaseCheck(context.Background(), "shop")
	if second.Status != "connected" || !second.CollectedAt.Equal(first.CollectedAt) {
		t.Fatalf("cached probe = %+v, want original evidence timestamp", second)
	}
	calls, err := os.ReadFile(marker)
	if err != nil || string(calls) != "x" {
		t.Fatalf("WP-CLI invocation marker = %q, err %v; expected one cached execution", calls, err)
	}
}

func TestSiteWordPressDatabaseCheckDistinguishesMissingWordPressAndFailure(t *testing.T) {
	root := t.TempDir()
	public := filepath.Join(root, "sites", "plain", "public")
	if err := os.MkdirAll(public, 0755); err != nil {
		t.Fatal(err)
	}
	wpPublic := filepath.Join(root, "sites", "wp-site", "public")
	if err := os.MkdirAll(wpPublic, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wpPublic, "wp-config.php"), []byte("<?php"), 0640); err != nil {
		t.Fatal(err)
	}
	a := &App{Config: Config{WebRoot: root}}
	if got := a.siteWordPressDatabaseCheck(context.Background(), "plain"); got.Status != "not_applicable" || got.Detail == "" {
		t.Fatalf("non-WordPress probe = %+v, want explicit not_applicable", got)
	}
	wp := filepath.Join(root, "wp-failing")
	if err := os.WriteFile(wp, []byte("#!/bin/sh\necho 'access denied' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a.Config.WPCLI = wp
	got := a.siteWordPressDatabaseCheck(context.Background(), "wp-site")
	if got.Status != "failed" || got.Detail == "" || strings.Contains(strings.ToLower(got.Detail), "access denied") {
		t.Fatalf("failed probe = %+v; expected a useful redacted diagnosis", got)
	}
}

func TestInspectFilesystemCapacityIncludesEvidence(t *testing.T) {
	root := t.TempDir()
	got := inspectFilesystemCapacity("test volume", root)
	if got["status"] != "healthy" {
		t.Fatalf("status = %v, want healthy; diagnostic: %#v", got["status"], got)
	}
	for _, key := range []string{"available_bytes", "total_bytes", "available_inodes", "total_inodes", "collected_at", "detail"} {
		if _, ok := got[key]; !ok {
			t.Errorf("capacity evidence missing %q: %#v", key, got)
		}
	}
	if at, ok := got["collected_at"].(time.Time); !ok || at.IsZero() {
		t.Errorf("collected_at = %#v, want timestamp", got["collected_at"])
	}
}

func TestInspectFilesystemCapacityUnknownPathIsExplicit(t *testing.T) {
	got := inspectFilesystemCapacity("backup volume", filepath.Join(t.TempDir(), "missing"))
	if got["status"] != "unknown" {
		t.Fatalf("status = %v, want unknown", got["status"])
	}
	if got["detail"] == "" || got["collected_at"] == nil {
		t.Fatalf("unavailable probe must carry explanation and time: %#v", got)
	}
}

func TestOpenManagedVhostRejectsPathAndSymlink(t *testing.T) {
	root := t.TempDir()
	name := "site-demo-example_test.conf"
	want := "example.test {\n}\n"
	if err := os.WriteFile(filepath.Join(root, name), []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	file, _, err := openManagedVhost(root, name)
	if err != nil {
		t.Fatalf("open managed vhost: %v", err)
	}
	got, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || string(got) != want {
		t.Fatalf("read managed vhost: got %q, err %v", got, err)
	}
	if _, _, err := openManagedVhost(root, "../outside.conf"); err == nil {
		t.Fatal("path traversal name was accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside.conf")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "site-demo-outside.conf")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openManagedVhost(root, "site-demo-outside.conf"); err == nil {
		t.Fatal("symlinked vhost was accepted")
	}
}
