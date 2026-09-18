package main

import (
	"path/filepath"
	"testing"
)

func TestRouteStorePersistsDesiredLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routes.json")
	store, err := OpenRouteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	route := routeState("site-demo-example_com.caddy", "demo", "example.com", "pending")
	if err := store.save(route); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRouteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	items := reopened.list()
	if len(items) != 1 || items[0].State != "pending" || items[0].Site != "demo" {
		t.Fatalf("reopened routes = %#v", items)
	}
	items[0].State = "applied"
	if err := reopened.save(items[0]); err != nil {
		t.Fatal(err)
	}
	if err := reopened.removeSite(AuthorizedSite{site: "demo"}); err != nil {
		t.Fatal(err)
	}
	if len(reopened.list()) != 0 {
		t.Fatal("site route remained after site removal")
	}
}
