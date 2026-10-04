package stepanel

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
	if err := store.markResult(request, "update", nil); err != nil {
		t.Fatal(err)
	}
	items := store.list("123")
	if len(items) != 1 || items[0].State != "applied" || items[0].Type != "A" {
		t.Fatalf("applied DNS state = %#v", items)
	}
	if err := store.markResult(request, "delete", nil); err != nil {
		t.Fatal(err)
	}
	if len(store.list("123")) != 0 {
		t.Fatal("deleted DNS desired record remained")
	}
}

func newDNSTestApp(t *testing.T) (*App, *DNSDesiredStore) {
	t.Helper()
	store, err := OpenDNSDesiredStore(filepath.Join(t.TempDir(), "dns.json"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &App{DNSDesired: store, Jobs: newJobsWithDB(db, 1)}, store
}

// TestDNSPendingIsDerivedFromTheDurableJob verifies the enqueue is the only
// write: the pending view comes from the queued job, and a stored outcome
// is replaced by pending while a newer change for the record is in flight.
func TestDNSPendingIsDerivedFromTheDurableJob(t *testing.T) {
	app, store := newDNSTestApp(t)
	request := cloudDNSRequest{DomainID: "123", RecordID: "1", Type: "A", Name: "www", Target: "192.0.2.10", TTL: 300}
	if err := store.markResult(request, "update", nil); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.Target = "192.0.2.20"
	if _, err := app.enqueueCloudJob(durableCloudRequest{Operation: "dns", Provider: "linode", Action: "update", ID: changed.DomainID, DNS: changed, Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	view, err := app.dnsDesiredView("123")
	if err != nil {
		t.Fatal(err)
	}
	if len(view) != 1 || view[0].State != "pending" || view[0].Target != "192.0.2.20" || view[0].Actor != "admin" {
		t.Fatalf("desired view = %#v; want the queued change as pending", view)
	}
	if stored := store.list("123"); len(stored) != 1 || stored[0].State != "applied" {
		t.Fatalf("enqueue wrote desired state directly: %#v", stored)
	}
}

func TestReconcileFailsLegacyPendingDNSWithoutDurableJob(t *testing.T) {
	app, store := newDNSTestApp(t)
	orphan := cloudDNSRequest{DomainID: "123", RecordID: "1", Type: "A", Name: "www", Target: "192.0.2.10", TTL: 300}
	live := cloudDNSRequest{DomainID: "123", RecordID: "2", Type: "A", Name: "api", Target: "192.0.2.11", TTL: 300}
	store.mu.Lock()
	for _, request := range []cloudDNSRequest{orphan, live} {
		store.values[dnsDesiredKey(request)] = newPendingDNSRecord(request, "update", "admin")
	}
	if err := store.persistLocked(); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.mu.Unlock()
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
