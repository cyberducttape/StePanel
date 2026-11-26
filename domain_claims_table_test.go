package stepanel

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func domainClaimTestValue(domain, site, tenant string) DomainClaim {
	return DomainClaim{Domain: domain, Site: site, Token: "tok-" + domain, State: "verified", VerifiedAt: time.Date(2026, 11, 26, 10, 0, 0, 5, time.UTC), Tenant: tenant, ExpiresAt: time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)}
}

// Migration 35 moves the "domain-claims" blob into rows with every field,
// including both timestamps, and deletes the blob.
func TestDomainClaimsMigrateFromTheStateBlob(t *testing.T) {
	db := openTestControlPlane(t)
	legacy := map[string]DomainClaim{
		"shop.example.org": domainClaimTestValue("shop.example.org", "shop", "alice"),
		"blog.example.org": {Site: "blog", State: "pending", Token: "t", LastError: "TXT record not found"},
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES ('domain-claims', ?, unixepoch(), 1)`, payload); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateDomainClaimsBlob(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &DomainClaimStore{values: map[string]DomainClaim{}}
	if err := bindDomainClaims(store, db); err != nil {
		t.Fatal(err)
	}
	if got := store.values["shop.example.org"]; !domainClaimEqual(got, legacy["shop.example.org"]) {
		t.Fatalf("migrated claim = %#v", got)
	}
	if got := store.values["blog.example.org"]; got.Domain != "blog.example.org" || got.LastError != "TXT record not found" || !got.VerifiedAt.IsZero() {
		t.Fatalf("migrated pending claim = %#v", got)
	}
	var blobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM state_blobs WHERE name = 'domain-claims'`).Scan(&blobs); err != nil || blobs != 0 {
		t.Fatalf("domain-claims blob after migration: %d, %v", blobs, err)
	}
}

// Two processes claiming the same domain cannot both succeed: the second is
// refused with the first claim in its refreshed view.
func TestDomainClaimsAreUniqueAcrossProcesses(t *testing.T) {
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
	panel := &DomainClaimStore{values: map[string]DomainClaim{}}
	worker := &DomainClaimStore{values: map[string]DomainClaim{}}
	if err := bindDomainClaims(panel, panelDB); err != nil {
		t.Fatal(err)
	}
	if err := bindDomainClaims(worker, workerDB); err != nil {
		t.Fatal(err)
	}
	if err := panel.save(domainClaimTestValue("shop.example.org", "shop", "alice")); err != nil {
		t.Fatal(err)
	}
	if err := worker.save(domainClaimTestValue("other.example.org", "other", "bob")); err != nil {
		t.Fatalf("a claim on another domain conflicted: %v", err)
	}
	err = worker.save(domainClaimTestValue("shop.example.org", "evil", "mallory"))
	if !errors.Is(err, errControlPlaneStateRefreshed) {
		t.Fatalf("a second claim on the same domain = %v, want a refreshed-state conflict", err)
	}
	if got := worker.values["shop.example.org"]; got.Tenant != "alice" || got.Site != "shop" {
		t.Fatalf("the refreshed claim = %#v, want alice's", got)
	}
	var owner string
	if err := panelDB.QueryRow(`SELECT tenant FROM domain_claims WHERE domain = 'shop.example.org'`).Scan(&owner); err != nil || owner != "alice" {
		t.Fatalf("durable claim owner = %q, %v", owner, err)
	}
	// A state change from a stale view is refused the same way.
	stale := domainClaimTestValue("other.example.org", "other", "bob")
	stale.State = "failed"
	if err := worker.save(stale); err != nil {
		t.Fatalf("the worker's own claim update: %v", err)
	}
	staleView := domainClaimTestValue("other.example.org", "other", "bob")
	staleView.State = "pending"
	panelErr := panel.save(staleView)
	if !errors.Is(panelErr, errControlPlaneStateRefreshed) {
		t.Fatalf("an update from a view without the row = %v", panelErr)
	}
}
