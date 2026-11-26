package stepanel

import (
	"testing"
	"time"
)

// The dashboard renders from ServiceStatus. While a refresh runs (systemctl
// and apachectl probes), a snapshot within the stale limit is served at once;
// only a snapshot past the limit makes the caller wait for fresh data.
func TestServiceStatusServesStaleSnapshotsWithoutWaiting(t *testing.T) {
	// Hold the refresh lock first: it waits out any background refresh an
	// earlier test started, which would otherwise overwrite the snapshot
	// planted here, and it simulates a slow refresh in progress.
	serviceStatusCache.refresh.Lock()
	serviceStatusCache.Lock()
	savedItems, savedAt := serviceStatusCache.items, serviceStatusCache.at
	serviceStatusCache.items = map[string]string{"caddy": "active"}
	serviceStatusCache.at = time.Now().Add(-30 * time.Second)
	serviceStatusCache.Unlock()
	t.Cleanup(func() {
		serviceStatusCache.Lock()
		serviceStatusCache.items, serviceStatusCache.at = savedItems, savedAt
		serviceStatusCache.Unlock()
	})

	got := make(chan map[string]string, 1)
	go func() { got <- ServiceStatus() }()
	select {
	case status := <-got:
		if status["caddy"] != "active" {
			t.Fatalf("stale snapshot = %v", status)
		}
	case <-time.After(time.Second):
		serviceStatusCache.refresh.Unlock()
		t.Fatal("a stale snapshot waited for the running refresh")
	}

	// Past the stale limit the caller must wait for the refresh.
	serviceStatusCache.Lock()
	serviceStatusCache.at = time.Now().Add(-serviceStatusStaleLimit - time.Minute)
	serviceStatusCache.Unlock()
	go func() { got <- ServiceStatus() }()
	select {
	case status := <-got:
		serviceStatusCache.refresh.Unlock()
		t.Fatalf("a snapshot past the stale limit was served without waiting: %v", status)
	case <-time.After(200 * time.Millisecond):
	}
	serviceStatusCache.refresh.Unlock()
	select {
	case <-got:
	case <-time.After(30 * time.Second):
		t.Fatal("the waiting caller never received the refreshed status")
	}
	serviceStatusCache.RLock()
	refreshedAt := serviceStatusCache.at
	serviceStatusCache.RUnlock()
	if time.Since(refreshedAt) > time.Minute {
		t.Fatal("the refresh did not store a new snapshot")
	}
}

func TestParseUnitStatesReadsBatchedSystemctlShow(t *testing.T) {
	output := "Id=caddy.service\nActiveState=active\n\nId=mariadb.service\nActiveState=failed\n\n" +
		// mysql.service is an alias of mariadb.service: its block reports the
		// target's Id, so mysql is absent and asked for on its own.
		"Id=mariadb.service\nActiveState=failed\n\nId=vsftpd.service\nActiveState=inactive\n\n" +
		"Id=odd.service\nActiveState=maintenance\n"
	states := parseUnitStates(output)
	want := map[string]string{"caddy": "active", "mariadb": "failed", "vsftpd": "inactive"}
	if len(states) != len(want) {
		t.Fatalf("states = %v, want %v", states, want)
	}
	for unit, state := range want {
		if states[unit] != state {
			t.Fatalf("states[%s] = %q, want %q (all: %v)", unit, states[unit], state, states)
		}
	}
	if _, ok := states["mysql"]; ok {
		t.Fatal("an alias was attributed to the name that was asked for")
	}
}
