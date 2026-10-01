package main

import (
	"path/filepath"
	"testing"
)

func TestDNSDesiredStateSurvivesTransitions(t *testing.T) {
	store, err := OpenDNSDesiredStore(filepath.Join(t.TempDir(), "dns.json"))
	if err != nil {
		t.Fatal(err)
	}
	request := cloudDNSRequest{DomainID: "123", RecordID: "456", Type: "a", Name: "www", Target: "192.0.2.10", TTL: 300}
	if err := store.markPending(request, "update", "admin"); err != nil {
		t.Fatal(err)
	}
	items := store.list("123")
	if len(items) != 1 || items[0].State != "pending" || items[0].Type != "A" {
		t.Fatalf("pending DNS state = %#v", items)
	}
	if err := store.markResult(request, "update", nil); err != nil {
		t.Fatal(err)
	}
	items = store.list("123")
	if len(items) != 1 || items[0].State != "applied" {
		t.Fatalf("applied DNS state = %#v", items)
	}
	if err := store.markPending(request, "delete", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.markResult(request, "delete", nil); err != nil {
		t.Fatal(err)
	}
	if len(store.list("123")) != 0 {
		t.Fatal("deleted DNS desired record remained")
	}
}

func TestReconcileFailsPendingDNSWithoutDurableJob(t *testing.T) {
	store, err := OpenDNSDesiredStore(filepath.Join(t.TempDir(), "dns.json"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := &App{DNSDesired: store, Jobs: newJobsWithDB(db, 1)}
	orphan := cloudDNSRequest{DomainID: "123", RecordID: "1", Type: "A", Name: "www", Target: "192.0.2.10", TTL: 300}
	live := cloudDNSRequest{DomainID: "123", RecordID: "2", Type: "A", Name: "api", Target: "192.0.2.11", TTL: 300}
	for _, request := range []cloudDNSRequest{orphan, live} {
		if err := store.markPending(request, "update", "admin"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.enqueueCloudJob(durableCloudRequest{Operation: "dns", Provider: "linode", Action: "update", ID: live.DomainID, DNS: live, Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	orphaned, err := app.reconcileOrphanedDNSDesired()
	if err != nil || orphaned != 1 {
		t.Fatalf("reconcile orphaned = %d, err = %v; want 1", orphaned, err)
	}
	states := map[string]string{}
	for _, record := range store.list("123") {
		states[record.RecordID] = record.State
	}
	if states["1"] != "failed" || states["2"] != "pending" {
		t.Fatalf("desired states after reconcile = %v; want orphan failed and live job pending", states)
	}
}
