package stepanel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartupBarrierOpensOnlyAfterSuccessfulRecovery(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	barrier := startupBarrier{db: db}
	if open, reason := barrier.status(); open || !strings.Contains(reason, "no panel has finished") {
		t.Fatalf("fresh barrier open=%v reason=%q", open, reason)
	}
	first, err := barrier.begin("panel-1")
	if err != nil {
		t.Fatal(err)
	}
	if open, reason := barrier.status(); open || !strings.Contains(reason, "recovering") {
		t.Fatalf("recovering barrier open=%v reason=%q", open, reason)
	}
	if err := barrier.finish(first, nil); err != nil {
		t.Fatal(err)
	}
	if open, _ := barrier.status(); !open {
		t.Fatal("barrier stayed closed after successful recovery")
	}
	// A restart closes it again, and a failed recovery keeps it closed.
	second, err := barrier.begin("panel-2")
	if err != nil || second != first+1 {
		t.Fatalf("second epoch = %d, %v", second, err)
	}
	if err := barrier.finish(second, errors.New("recover site blog: journal requires operator action")); err != nil {
		t.Fatal(err)
	}
	if open, reason := barrier.status(); open || !strings.Contains(reason, "journal requires operator action") {
		t.Fatalf("failed barrier open=%v reason=%q", open, reason)
	}
	// A panel whose epoch was superseded cannot open the barrier.
	third, err := barrier.begin("panel-3")
	if err != nil {
		t.Fatal(err)
	}
	if err := barrier.finish(second, nil); err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("stale finish error = %v", err)
	}
	if open, _ := barrier.status(); open {
		t.Fatal("a superseded epoch opened the barrier")
	}
	if err := barrier.finish(third, errors.New(strings.Repeat("x", 3*startupBarrierDetailLimit))); err != nil {
		t.Fatal(err)
	}
	if _, reason := barrier.status(); len(reason) > startupBarrierDetailLimit+100 {
		t.Fatalf("stored failure detail is unbounded: %d bytes", len(reason))
	}
}

// Jobs queued before a restart (restores, activations, imports, site
// terminations) must not be claimed until startup recovery has succeeded.
func TestWorkerClaimsNothingWhileStartupBarrierIsClosed(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 4)
	barrier := startupBarrier{db: db}
	epoch, err := barrier.begin("panel-test")
	if err != nil {
		t.Fatal(err)
	}
	jobs.SetClaimGate(barrier.status)
	kinds := []string{"backup.restore", "git.activate", "import.cpmove", "site.terminate"}
	for _, kind := range kinds {
		if _, err := jobs.Enqueue(kind, "admin", kind, []byte("{}"), 1); err != nil {
			t.Fatal(err)
		}
	}
	var ran atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- jobs.RunWorkerPool(ctx, "barrier-worker", kinds, time.Millisecond, 2, func(context.Context, Job) ([]byte, error) {
			ran.Add(1)
			return nil, nil
		})
	}()
	time.Sleep(100 * time.Millisecond)
	if got := ran.Load(); got != 0 {
		t.Fatalf("%d jobs ran while startup recovery was in progress", got)
	}
	if err := barrier.finish(epoch, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for ran.Load() < int32(len(kinds)) {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d jobs ran after the barrier opened", ran.Load(), len(kinds))
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker exit = %v", err)
	}
}

func TestStartupMutationGateRefusesWritesUntilRecoverySucceeds(t *testing.T) {
	app := &App{}
	var reached atomic.Int32
	handler := app.startupMutationGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	request := func(method, path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
		return response
	}

	app.startup.begin()
	for _, path := range []string{"/api/sites", "/api/backups/restore", "/api/jobs/abc/cancel", "/api/sites/git-webhook/blog"} {
		response := request(http.MethodPost, path)
		if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") == "" || !strings.Contains(response.Body.String(), `"retryable":true`) {
			t.Fatalf("POST %s during recovery = %d %q %s", path, response.Code, response.Header().Get("Retry-After"), response.Body.String())
		}
	}
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if response := request(method, "/api/sites/blog"); response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s during recovery = %d", method, response.Code)
		}
	}
	if reached.Load() != 0 {
		t.Fatal("a mutating request reached its handler during recovery")
	}
	for _, check := range []struct{ method, path string }{{http.MethodGet, "/readyz"}, {http.MethodGet, "/livez"}, {http.MethodGet, "/api/sites"}, {http.MethodHead, "/api/health"}, {http.MethodPost, "/login"}, {http.MethodPost, "/logout"}} {
		if response := request(check.method, check.path); response.Code != http.StatusNoContent {
			t.Fatalf("%s %s during recovery = %d", check.method, check.path, response.Code)
		}
	}

	app.startup.finish(errors.New("recover site blog: journal requires operator action"))
	response := request(http.MethodPost, "/api/sites")
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "" || !strings.Contains(response.Body.String(), `"retryable":false`) || strings.Contains(response.Body.String(), "journal") {
		t.Fatalf("POST after failed recovery = %d %q %s", response.Code, response.Header().Get("Retry-After"), response.Body.String())
	}

	app.startup.begin()
	app.startup.setHostState(errors.New("workers reconciliation failed"))
	app.startup.finish(nil)
	if response := request(http.MethodPost, "/api/sites"); response.Code != http.StatusNoContent {
		t.Fatalf("POST after successful recovery with host drift = %d", response.Code)
	}
}
