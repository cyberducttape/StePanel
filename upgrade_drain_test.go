package stepanel

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func upgradeDrainFixture(t *testing.T) (*sql.DB, *Jobs, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stepanel-control.db")
	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, newJobsWithDB(db, 2), path
}

func TestUpgradeDrainPausesClaimsAndMutations(t *testing.T) {
	db, jobs, _ := upgradeDrainFixture(t)
	gate := &upgradeDrainGate{db: db}
	jobs.SetClaimGate(func() (bool, string) {
		if draining, reason := gate.status(); draining {
			return false, reason
		}
		return true, ""
	})
	if _, err := jobs.Enqueue("site.backup", "shop", "", []byte(`{}`), 1); err != nil {
		t.Fatal(err)
	}
	if err := beginUpgradeDrain(db, time.Minute, false, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	gate.checked = time.Time{}
	if open, reason := jobs.claimsOpen(); open || !strings.Contains(reason, "upgrade") {
		t.Fatalf("claims during a drain = %v (%q)", open, reason)
	}

	app := &App{upgradeDrain: gate}
	app.startup.begin()
	app.startup.finish(nil)
	handler := app.startupMutationGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/sites", nil))
	if post.Code != http.StatusServiceUnavailable || post.Header().Get("Retry-After") == "" {
		t.Fatalf("mutation during a drain = %d", post.Code)
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/sites", nil))
	if get.Code != http.StatusNoContent {
		t.Fatalf("read during a drain = %d", get.Code)
	}

	if err := endUpgradeDrain(db); err != nil {
		t.Fatal(err)
	}
	gate.checked = time.Time{}
	if open, _ := jobs.claimsOpen(); !open {
		t.Fatal("claims stayed closed after the drain ended")
	}
	post = httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/sites", nil))
	if post.Code != http.StatusNoContent {
		t.Fatalf("mutation after the drain = %d", post.Code)
	}
}

func markRunning(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE jobs SET state = 'running', lease_owner = 'w', lease_token = 't', lease_expires_at = ? WHERE id = ?`, time.Now().Add(time.Hour).UnixNano(), id); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeDrainWaitsForRunningJobs(t *testing.T) {
	db, jobs, _ := upgradeDrainFixture(t)
	item, err := jobs.Enqueue("site.backup", "shop", "", []byte(`{}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	markRunning(t, db, item.ID)
	go func() {
		time.Sleep(150 * time.Millisecond)
		_, _ = db.Exec(`UPDATE jobs SET state = 'completed', lease_owner = NULL, lease_token = NULL, lease_expires_at = NULL, finished_at = ? WHERE id = ?`, time.Now().UnixNano(), item.ID)
	}()
	started := time.Now()
	if err := beginUpgradeDrain(db, 10*time.Second, false, 20*time.Millisecond); err != nil {
		t.Fatalf("drain with a job that finishes = %v", err)
	}
	if time.Since(started) < 100*time.Millisecond {
		t.Fatal("the drain did not wait for the running job")
	}
}

func TestUpgradeDrainTimeoutRefusesUnlessForced(t *testing.T) {
	db, jobs, _ := upgradeDrainFixture(t)
	item, err := jobs.Enqueue("site.backup", "shop", "", []byte(`{}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	markRunning(t, db, item.ID)
	err = beginUpgradeDrain(db, 100*time.Millisecond, false, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("drain timeout = %v", err)
	}
	if draining, _, err := upgradeDrainStatus(db, time.Now()); err != nil || draining {
		t.Fatalf("a refused drain left the flag set (%v, %v)", draining, err)
	}
	if err := beginUpgradeDrain(db, 100*time.Millisecond, true, 20*time.Millisecond); err != nil {
		t.Fatalf("forced drain = %v", err)
	}
	if draining, _, _ := upgradeDrainStatus(db, time.Now()); !draining {
		t.Fatal("a forced drain did not keep the flag")
	}
	// The flag expires on its own.
	if draining, _, _ := upgradeDrainStatus(db, time.Now().Add(upgradeDrainGrace+time.Hour)); draining {
		t.Fatal("an expired drain is still in effect")
	}
}

func TestUpgradeDrainEndAndStatusOnADatabaseWithoutTheTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE jobs (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := endUpgradeDrain(db); err != nil {
		t.Fatalf("end on an older database = %v", err)
	}
	if draining, _, err := upgradeDrainStatus(db, time.Now()); err != nil || draining {
		t.Fatalf("status on an older database = %v, %v", draining, err)
	}
}

func TestUpgradePreflight(t *testing.T) {
	_, _, path := upgradeDrainFixture(t)
	if err := runUpgradePreflight(path); err != nil {
		t.Fatalf("preflight on a healthy database = %v", err)
	}
	if err := runUpgradePreflight(filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("preflight accepted a missing database")
	}
}
