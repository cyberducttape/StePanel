package stepanel

import (
	"testing"
	"time"
)

// The dashboard renders from ServiceStatus. While a refresh runs (systemctl
// and apachectl probes), a snapshot within the stale limit is served at once;
// only a snapshot past the limit makes the caller wait for fresh data.
func TestServiceStatusServesStaleSnapshotsWithoutWaiting(t *testing.T) {
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

	// Simulate a slow refresh already in progress.
	serviceStatusCache.refresh.Lock()
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
