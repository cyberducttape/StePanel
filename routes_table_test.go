package stepanel

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func routeTestValue(name, site, domain, state string) RouteDesired {
	return RouteDesired{Name: name, Site: site, Domain: domain, State: state, UpdatedAt: time.Date(2026, 11, 26, 11, 0, 0, 7, time.UTC)}
}

func TestDesiredRoutesMigrateFromTheStateBlob(t *testing.T) {
	db := openTestControlPlane(t)
	legacy := map[string]RouteDesired{
		"shop-example-org": routeTestValue("shop-example-org", "shop", "shop.example.org", "applied"),
		"blog-example-org": {Site: "blog", Domain: "blog.example.org", State: "pending", LastError: "proxy reload failed"},
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES ('routes', ?, unixepoch(), 1)`, payload); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateRoutesBlob(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &RouteStore{values: map[string]RouteDesired{}}
	if err := bindRoutes(store, db); err != nil {
		t.Fatal(err)
	}
	if got := store.values["shop-example-org"]; !routeEqual(got, legacy["shop-example-org"]) {
		t.Fatalf("migrated route = %#v", got)
	}
	if got := store.values["blog-example-org"]; got.Name != "blog-example-org" || got.LastError != "proxy reload failed" {
		t.Fatalf("migrated pending route = %#v", got)
	}
	var blobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM state_blobs WHERE name = 'routes'`).Scan(&blobs); err != nil || blobs != 0 {
		t.Fatalf("routes blob after migration: %d, %v", blobs, err)
	}
}

func TestDesiredRouteWritesConflictOnlyOnTheSameRoute(t *testing.T) {
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
	panel := &RouteStore{values: map[string]RouteDesired{}}
	worker := &RouteStore{values: map[string]RouteDesired{}}
	if err := bindRoutes(panel, panelDB); err != nil {
		t.Fatal(err)
	}
	if err := bindRoutes(worker, workerDB); err != nil {
		t.Fatal(err)
	}
	if err := panel.save(routeTestValue("shop-example-org", "shop", "shop.example.org", "pending")); err != nil {
		t.Fatal(err)
	}
	if err := worker.save(routeTestValue("blog-example-org", "blog", "blog.example.org", "pending")); err != nil {
		t.Fatalf("a write to another route conflicted: %v", err)
	}
	err = worker.save(routeTestValue("shop-example-org", "shop", "shop.example.org", "delete-pending"))
	if !errors.Is(err, errControlPlaneStateRefreshed) {
		t.Fatalf("a write over another process's route = %v", err)
	}
	if got := worker.values["shop-example-org"]; got.State != "pending" {
		t.Fatalf("the refreshed route = %#v", got)
	}
	if err := worker.save(routeTestValue("shop-example-org", "shop", "shop.example.org", "applied")); err != nil {
		t.Fatalf("retry after refresh: %v", err)
	}
}
