package stepanel

import (
	"encoding/json"
	"os"
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

func TestDNSDesiredLegacyStateReloadAcceptsCurrentDNSContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dns-desired.json")
	records := map[string]DNSDesiredRecord{}
	inputs := []cloudDNSRequest{
		{DomainID: "123", Type: "A", Name: "@", Target: "192.0.2.10", TTL: 2419200},
		{DomainID: "123", Type: "AAAA", Name: "v6", Target: "2001:db8::10", TTL: 1209600},
		{DomainID: "123", Type: "CNAME", Name: "www", Target: "example.com", TTL: 300},
		{DomainID: "123", Type: "MX", Name: "@", Target: "mail.example.com", TTL: 604800, Priority: 10},
		{DomainID: "123", Type: "NS", Name: "@", Target: "ns1.example.com", TTL: 1209600},
		{DomainID: "123", Type: "SRV", Name: "", Target: "service.example.com", TTL: 2419200, Priority: 10, Service: "https", Protocol: "tcp", Port: 443, Weight: 5},
		{DomainID: "123", Type: "TXT", Name: "@", Target: "v=spf1 include:example.com", TTL: 300},
	}
	for index, input := range inputs {
		key := "123/legacy-" + string(rune('a'+index))
		records[key] = DNSDesiredRecord{
			Key: key, DomainID: input.DomainID, Type: input.Type, Name: input.Name,
			Target: input.Target, TTL: input.TTL, Priority: input.Priority,
			Service: input.Service, Protocol: input.Protocol, Port: input.Port,
			Weight: input.Weight, Action: "update", State: "applied",
		}
	}
	data, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenDNSDesiredStore(path)
	if err != nil {
		t.Fatalf("reload valid legacy DNS schema: %v", err)
	}
	if got := store.list("123"); len(got) != len(inputs) {
		t.Fatalf("reloaded %d DNS records, want %d", len(got), len(inputs))
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
	if _, err := app.enqueueCloudJob(durableCloudRequest{Operation: "dns", Provider: "linode", Action: "update", ID: changed.DomainID, DNS: changed, Actor: "admin"}, ""); err != nil {
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

func TestCloudResourcesWithSameNumericIDHaveIndependentQueueOwners(t *testing.T) {
	app, _ := newDNSTestApp(t)
	requests := []durableCloudRequest{
		{Operation: "instance", Provider: "linode", Action: "reboot", ID: "123", Actor: "admin"},
		{Operation: "dns", Provider: "linode", Action: "update", ID: "123", DNS: cloudDNSRequest{DomainID: "123", RecordID: "1", Type: "A", Name: "@", Target: "192.0.2.1", TTL: 300}, Actor: "admin"},
		{Operation: "loadbalancer", Provider: "linode", Action: "remove", ID: "123", LB: cloudLBRequest{NodeBalancerID: "123", ConfigID: "1", NodeID: "2", Action: "remove"}, Actor: "admin"},
	}
	app.Jobs.workerLimit = len(requests)
	wantOwners := map[string]bool{
		"linode:instance:123":     false,
		"linode:domain:123":       false,
		"linode:nodebalancer:123": false,
	}
	for _, request := range requests {
		job, err := app.enqueueCloudJob(request, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := wantOwners[job.User]; !ok {
			t.Fatalf("cloud job owner = %q, want a typed owner", job.User)
		}
		wantOwners[job.User] = true
	}
	for owner, seen := range wantOwners {
		if !seen {
			t.Errorf("no job received independent owner %q", owner)
		}
	}
	claimedOwners := map[string]bool{}
	for range requests {
		job, claimed, err := app.Jobs.ClaimNext("typed-owner-test", "cloud.action")
		if err != nil || !claimed {
			t.Fatalf("independent cloud job claim = %v, %v; want a claim", claimed, err)
		}
		claimedOwners[job.User] = true
	}
	for owner := range wantOwners {
		if !claimedOwners[owner] {
			t.Errorf("queue head-of-line blocking prevented claim for %q", owner)
		}
	}
}

func TestCloudActionsAreNewAttemptsUnlessCallerRetriesWithKey(t *testing.T) {
	app, _ := newDNSTestApp(t)
	request := durableCloudRequest{Operation: "instance", Provider: "linode", Action: "stop", ID: "123", Actor: "admin"}
	seen := map[string]bool{}
	for _, action := range []string{"start", "stop", "start", "reboot", "reboot"} {
		request.Action = action
		job, err := app.enqueueCloudJob(request, "")
		if err != nil {
			t.Fatal(err)
		}
		if seen[job.ID] {
			t.Fatalf("separate %s action reused durable job %q", action, job.ID)
		}
		seen[job.ID] = true
	}
	request.Action = "stop"
	retry, err := app.enqueueCloudJob(request, "operator-request-123")
	if err != nil {
		t.Fatal(err)
	}
	exactRetry, err := app.enqueueCloudJob(request, "operator-request-123")
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID != exactRetry.ID {
		t.Fatalf("exact retry jobs differ: %q and %q", retry.ID, exactRetry.ID)
	}
	if retry.MaxAttempts != 1 {
		t.Fatalf("cloud mutation max attempts = %d, want 1 to prevent ambiguous automatic replay", retry.MaxAttempts)
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
	if _, err := app.enqueueCloudJob(durableCloudRequest{Operation: "dns", Provider: "linode", Action: "update", ID: live.DomainID, DNS: live, Actor: "admin"}, ""); err != nil {
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
