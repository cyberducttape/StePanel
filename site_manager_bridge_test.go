package stepanel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cyberducttape/StePanel/internal/sitelayout"
	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

func TestActivateStagedSiteHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&App{Config: Config{WebRoot: t.TempDir()}}).activateStagedSite(ctx, "demo", t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("activateStagedSite error = %v, want context.Canceled", err)
	}
}

func TestSiteManagerResolvesPlacedSitesToTheAccountHome(t *testing.T) {
	root := t.TempDir()
	db, err := openControlPlaneDB(filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registerSitePlacementDB(db)
	t.Cleanup(func() { registerSitePlacementDB(nil) })
	if _, err := sitelayout.AllocateAccount(db, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := sitelayout.Place(db, sitelayout.Placement{Site: "alice", Account: "alice", UnixUser: "alice", Kind: sitelayout.KindMain, Directory: sitelayout.MainDirectory}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{WebRoot: filepath.Join(root, "www"), HomeRoot: filepath.Join(root, "home")}
	for _, dir := range []string{filepath.Join(cfg.WebRoot, "sites"), filepath.Join(cfg.HomeRoot, "alice")} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := newSiteManagerForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	placed, err := manager.Create(context.Background(), &siteauthority.CreateRequest{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cfg.HomeRoot, "alice", "public_html"); placed.WebRoot != want {
		t.Fatalf("placed site web root = %s, want %s", placed.WebRoot, want)
	}
	legacy, err := manager.Create(context.Background(), &siteauthority.CreateRequest{Name: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cfg.WebRoot, "sites", "legacy", "public"); legacy.WebRoot != want {
		t.Fatalf("legacy site web root = %s, want %s", legacy.WebRoot, want)
	}
}
