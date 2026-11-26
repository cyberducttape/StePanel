package stepanel

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func siteAccessTestValue(site string, keys ...string) SiteAccess {
	access := SiteAccess{Site: site, SFTPEnabled: true, ShellEnabled: false, State: "applied", Keys: []SSHKey{}}
	for _, label := range keys {
		access.Keys = append(access.Keys, SSHKey{Label: label, PublicKey: "ssh-ed25519 AAAA" + label, Fingerprint: "SHA256:" + label})
	}
	return access
}

func countSiteAccessKeys(t *testing.T, store *SiteAccessStore, site string) int {
	t.Helper()
	var count int
	if err := store.rows.db.QueryRow(`SELECT COUNT(*) FROM site_access_keys WHERE site = ?`, site).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// Migration 34 moves the "site-access" blob into the tables, keeping keys
// in order, and deletes the blob.
func TestSiteAccessMigratesFromTheStateBlob(t *testing.T) {
	db := openTestControlPlane(t)
	legacy := map[string]SiteAccess{"shop": siteAccessTestValue("shop", "laptop", "ci"), "blog": {SFTPEnabled: true}}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES ('site-access', ?, unixepoch(), 1)`, payload); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSiteAccessBlob(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &SiteAccessStore{values: map[string]SiteAccess{}}
	if err := bindSiteAccess(store, db); err != nil {
		t.Fatal(err)
	}
	if got := store.values["shop"]; !siteAccessEqual(got, legacy["shop"]) || got.Keys[0].Label != "laptop" || got.Keys[1].Label != "ci" {
		t.Fatalf("migrated shop access = %#v", got)
	}
	if got := store.values["blog"]; got.Site != "blog" || got.State != "applied" || !got.SFTPEnabled || got.Keys == nil {
		t.Fatalf("migrated blog access (empty site, state and keys) = %#v", got)
	}
	var blobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM state_blobs WHERE name = 'site-access'`).Scan(&blobs); err != nil || blobs != 0 {
		t.Fatalf("site-access blob after migration: %d, %v", blobs, err)
	}
}

func TestSiteAccessKeysAreReplacedAndCascadeOnDelete(t *testing.T) {
	db := openTestControlPlane(t)
	store := &SiteAccessStore{values: map[string]SiteAccess{}}
	if err := bindSiteAccess(store, db); err != nil {
		t.Fatal(err)
	}
	store.values["shop"] = siteAccessTestValue("shop", "laptop", "ci")
	if err := store.persistLocked(); err != nil {
		t.Fatal(err)
	}
	// An in-place change to a key element must still be detected: the
	// persisted view holds its own copy of the slice.
	access := store.values["shop"]
	access.Keys[1].Label = "deploy"
	access.Keys = access.Keys[1:]
	store.values["shop"] = access
	if err := store.persistLocked(); err != nil {
		t.Fatal(err)
	}
	reopened := &SiteAccessStore{values: map[string]SiteAccess{}}
	if err := bindSiteAccess(reopened, db); err != nil {
		t.Fatal(err)
	}
	if got := reopened.values["shop"]; len(got.Keys) != 1 || got.Keys[0].Label != "deploy" {
		t.Fatalf("keys after replacement = %#v", got.Keys)
	}
	delete(store.values, "shop")
	if err := store.persistLocked(); err != nil {
		t.Fatal(err)
	}
	if n := countSiteAccessKeys(t, store, "shop"); n != 0 {
		t.Fatalf("%d SSH key rows outlived their site", n)
	}
}

func TestSiteAccessConflictsRefreshFromThePeer(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control-plane.db")
	panelDB, err := openControlPlaneDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer panelDB.Close()
	workerDB, err := openControlPlaneDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer workerDB.Close()
	panel := &SiteAccessStore{values: map[string]SiteAccess{}}
	worker := &SiteAccessStore{values: map[string]SiteAccess{}}
	if err := bindSiteAccess(panel, panelDB); err != nil {
		t.Fatal(err)
	}
	if err := bindSiteAccess(worker, workerDB); err != nil {
		t.Fatal(err)
	}
	panel.values["shop"] = siteAccessTestValue("shop", "laptop")
	if err := panel.persistLocked(); err != nil {
		t.Fatal(err)
	}
	worker.values["blog"] = siteAccessTestValue("blog")
	if err := worker.persistLocked(); err != nil {
		t.Fatalf("a write to another site conflicted: %v", err)
	}
	worker.values["shop"] = siteAccessTestValue("shop", "attacker")
	err = worker.persistLocked()
	if !errors.Is(err, errControlPlaneStateRefreshed) {
		t.Fatalf("a write over a row another process created = %v", err)
	}
	if got := worker.values["shop"]; len(got.Keys) != 1 || got.Keys[0].Label != "laptop" {
		t.Fatalf("the refreshed view = %#v, want the panel's keys", got)
	}
}
