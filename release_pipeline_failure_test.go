package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActivatePipelineReleaseFailureInjectionPreservesLiveRelease(t *testing.T) {
	t.Setenv("STEPANEL_FAIL_AT", "deploy:activate")
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	siteRoot := filepath.Join(webRoot, "sites", "example")
	publicRoot := filepath.Join(siteRoot, "public")
	release := filepath.Join(siteRoot, ".stepanel-release-next")
	if err := os.MkdirAll(publicRoot, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(release, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicRoot, "index.html"), []byte("live"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, "index.html"), []byte("next"), 0640); err != nil {
		t.Fatal(err)
	}

	_, err := (&App{Config: Config{WebRoot: webRoot, RecoveryRoot: filepath.Join(root, "recovery")}}).activatePipelineRelease(context.Background(), "example", release)
	if err == nil || !strings.Contains(err.Error(), "failure injection") {
		t.Fatalf("activation error = %v, want injected failure", err)
	}
	data, err := os.ReadFile(filepath.Join(publicRoot, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "live" {
		t.Fatalf("live release changed to %q", data)
	}
	if _, err := os.Stat(release); err != nil {
		t.Fatalf("staged release was unexpectedly removed: %v", err)
	}
}

func TestActivatePipelineReleaseRejectsReleaseOutsideSiteRoot(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	outside := filepath.Join(root, "outside-release")
	if err := os.MkdirAll(outside, 0750); err != nil {
		t.Fatal(err)
	}
	_, err := (&App{Config: Config{WebRoot: webRoot, RecoveryRoot: filepath.Join(root, "recovery")}}).activatePipelineRelease(context.Background(), "example", outside)
	if err == nil || !strings.Contains(err.Error(), "outside site root") {
		t.Fatalf("outside release error = %v, want containment rejection", err)
	}
}
