package sites

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

// newAccountManager returns a manager with two placed sites in alice's home
// (main "alice" and addon "shop") and treats every other site as legacy.
func newAccountManager(t *testing.T) (*DefaultManager, string, string) {
	t.Helper()
	webRoot, homeRoot := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(webRoot, "sites"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(homeRoot, "alice"), 0711); err != nil {
		t.Fatal(err)
	}
	placements := map[string]sitelayout.Placement{
		"alice": {Site: "alice", Account: "alice", UnixUser: "alice", Kind: sitelayout.KindMain, Directory: sitelayout.MainDirectory},
		"shop":  {Site: "shop", Account: "alice", UnixUser: "alice", Kind: sitelayout.KindAddon, Directory: "shop.example.org"},
	}
	m, err := NewAccountManager(webRoot, homeRoot, func(site string) (sitelayout.Placement, error) {
		if p, ok := placements[site]; ok {
			return p, nil
		}
		return sitelayout.Placement{}, sitelayout.ErrNotPlaced
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, webRoot, homeRoot
}

func TestPlacedSitesUseTheirAccountHome(t *testing.T) {
	m, webRoot, homeRoot := newAccountManager(t)
	ctx := context.Background()
	for _, tc := range []struct{ site, docRoot string }{
		{"alice", filepath.Join(homeRoot, "alice", "public_html")},
		{"shop", filepath.Join(homeRoot, "alice", "shop.example.org")},
		{"legacy", filepath.Join(webRoot, "sites", "legacy", "public")},
	} {
		site, err := m.Create(ctx, &CreateRequest{Name: tc.site})
		if err != nil {
			t.Fatalf("create %s: %v", tc.site, err)
		}
		if site.WebRoot != tc.docRoot {
			t.Fatalf("%s web root = %s, want %s", tc.site, site.WebRoot, tc.docRoot)
		}
		if info, err := os.Stat(tc.docRoot); err != nil || !info.IsDir() {
			t.Fatalf("%s document root missing: %v", tc.site, err)
		}
	}
	if _, err := m.Create(ctx, &CreateRequest{Name: "alice"}); err == nil {
		t.Fatal("created the main site twice")
	}
	if _, err := os.Stat(filepath.Join(webRoot, "sites", "alice")); !os.IsNotExist(err) {
		t.Fatalf("a placed site was also created in the legacy tree: %v", err)
	}
}

func TestPlacedSiteRequiresAnExistingHome(t *testing.T) {
	m, _, homeRoot := newAccountManager(t)
	if err := os.Remove(filepath.Join(homeRoot, "alice")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(context.Background(), &CreateRequest{Name: "alice"}); err == nil || !strings.Contains(err.Error(), "account home") {
		t.Fatalf("create without a home = %v", err)
	}
	if _, err := os.Stat(filepath.Join(homeRoot, "alice")); !os.IsNotExist(err) {
		t.Fatal("the manager created an account home; only the privileged helper may")
	}
}

func TestPlacedActivationUsesTheHomeStagingRoot(t *testing.T) {
	m, _, homeRoot := newAccountManager(t)
	ctx := context.Background()
	stage, lease, err := m.CreateSiteStagingLease(ctx, "shop", ".stepanel-import-")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(stage) != filepath.Join(homeRoot, ".stepanel-manager-staging") {
		t.Fatalf("placed stage = %s, want it under the home staging root", stage)
	}
	if err := os.WriteFile(filepath.Join(stage, "index.html"), []byte("shop"), 0644); err != nil {
		t.Fatal(err)
	}
	// A stage from the legacy root would need a cross-filesystem copy.
	legacyStage, legacyLease, err := m.CreateStagingLease(ctx, ".stepanel-import-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ActivateStaged(ctx, "shop", legacyStage); err == nil {
		t.Fatal("a placed site accepted a stage from the legacy staging root")
	}
	if err := m.DiscardStaging(ctx, legacyStage); err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseStagingLease(legacyStage, legacyLease); err != nil {
		t.Fatal(err)
	}
	site, err := m.ActivateStaged(ctx, "shop", stage)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseStagingLease(stage, lease); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(site.WebRoot, "index.html")); err != nil || string(data) != "shop" {
		t.Fatalf("activated document root = %q, %v", data, err)
	}
}

func TestPlacedReleasesKeepPerSitePreviousTrees(t *testing.T) {
	m, _, homeRoot := newAccountManager(t)
	ctx := context.Background()
	home := filepath.Join(homeRoot, "alice")
	for _, site := range []string{"alice", "shop"} {
		if _, err := m.Create(ctx, &CreateRequest{Name: site}); err != nil {
			t.Fatal(err)
		}
	}
	release, lease, err := m.CreateReleaseStagingLease(ctx, "shop", ".stepanel-release-")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if filepath.Dir(release) != filepath.Join(homeRoot, ".stepanel-manager-staging") {
		t.Fatalf("release stage = %s", release)
	}
	if err := os.WriteFile(filepath.Join(release, "v2"), []byte("2"), 0644); err != nil {
		t.Fatal(err)
	}
	previous, err := m.ActivateStagedReplacing(ctx, "shop", release)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(previous) != home || !strings.HasPrefix(filepath.Base(previous), ".stepanel-previous-shop.example.org-") {
		t.Fatalf("previous release = %s, want a shop-specific sibling in the home", previous)
	}
	// Another site's preserved release is not a valid rollback source.
	if err := m.RollbackStagedActivation(ctx, "alice", previous); err == nil {
		t.Fatal("rolled the main site back to an addon site's release")
	}
	if err := m.RollbackStagedActivation(ctx, "shop", previous); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "shop.example.org", "v2")); !os.IsNotExist(err) {
		t.Fatalf("rollback kept the replacement release: %v", err)
	}
}

func TestDeletingAPlacedSiteKeepsTheHomeAndOtherSites(t *testing.T) {
	m, _, homeRoot := newAccountManager(t)
	ctx := context.Background()
	home := filepath.Join(homeRoot, "alice")
	for _, site := range []string{"alice", "shop"} {
		if _, err := m.Create(ctx, &CreateRequest{Name: site}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{".stepanel-previous-shop.example.org-1", ".stepanel-previous-public_html-1"} {
		if err := os.Mkdir(filepath.Join(home, name), 0750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("# user file"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "shop"); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"shop.example.org", ".stepanel-previous-shop.example.org-1"} {
		if _, err := os.Lstat(filepath.Join(home, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived deletion: %v", gone, err)
		}
	}
	for _, kept := range []string{"public_html", ".stepanel-previous-public_html-1", ".bashrc"} {
		if _, err := os.Lstat(filepath.Join(home, kept)); err != nil {
			t.Errorf("%s was removed with another site: %v", kept, err)
		}
	}
	if err := m.Delete(ctx, "shop"); err != nil {
		t.Fatalf("repeated delete = %v", err)
	}
}

func TestDeleteRefusesASymlinkedDocumentRoot(t *testing.T) {
	m, _, homeRoot := newAccountManager(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(homeRoot, "alice", "public_html")); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(context.Background(), "alice"); err == nil {
		t.Fatal("deleted through a symlinked document root")
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatalf("symlink target was modified: %v", err)
	}
}

func TestPlacementLookupFailuresFailClosed(t *testing.T) {
	webRoot, homeRoot := t.TempDir(), t.TempDir()
	m, err := NewAccountManager(webRoot, homeRoot, func(string) (sitelayout.Placement, error) {
		return sitelayout.Placement{}, errors.New("control plane unavailable")
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(context.Background(), &CreateRequest{Name: "alice"}); err == nil {
		t.Fatal("an unreadable placement fell back to the legacy layout")
	}
	evil, err := NewAccountManager(webRoot, homeRoot, func(site string) (sitelayout.Placement, error) {
		return sitelayout.Placement{Site: site, Account: "x", UnixUser: "root", Kind: sitelayout.KindAddon, Directory: "../../etc"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evil.Create(context.Background(), &CreateRequest{Name: "alice"}); err == nil {
		t.Fatal("a tampered placement was honoured")
	}
	if _, err := NewAccountManager(webRoot, "relative/home", func(string) (sitelayout.Placement, error) { return sitelayout.Placement{}, nil }); err == nil {
		t.Fatal("accepted a relative home root")
	}
}

func TestCleanupCoversTheHomeStagingRoot(t *testing.T) {
	m, _, homeRoot := newAccountManager(t)
	ctx := context.Background()
	stage, lease, err := m.CreateSiteStagingLease(ctx, "shop", ".stepanel-import-")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil { // the owner died without releasing
		t.Fatal(err)
	}
	removed, err := m.CleanupOrphanedStaging(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed %d stages, want 1", removed)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("abandoned home stage survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(homeRoot, ".stepanel-manager-staging", fmt.Sprintf("%s%s", stageOwnerLockPrefix, filepath.Base(stage)))); !os.IsNotExist(err) {
		t.Fatalf("lease file survived: %v", err)
	}
}
