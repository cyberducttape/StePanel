package stepanel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func readinessTestApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	for _, path := range []string{filepath.Join(root, "imports"), filepath.Join(root, "backups"), filepath.Join(root, "sites")} {
		if err := os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	return &App{
		Config: Config{ImportRoot: filepath.Join(root, "imports"), BackupRoot: filepath.Join(root, "backups"), JobState: filepath.Join(root, "jobs.json"), RecoveryRoot: filepath.Join(root, "sites", ".stepanel-recovery"), MinFreeBytes: 1},
		Jobs:   NewJobs(),
	}
}

func readyzResponse(app *App) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	app.readyz(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return response
}

func TestStartupHostReconcilersCoverEveryDomain(t *testing.T) {
	var names []string
	for _, domain := range (&App{}).hostReconcilers() {
		if domain.run == nil {
			t.Fatalf("%s has no reconciler", domain.name)
		}
		names = append(names, domain.name)
	}
	want := "routes,SSH access,workers,PHP profile,Python application,scheduled task,resource profile,environment"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("host reconcilers = %s, want %s", got, want)
	}
}

// Each domain's failure alone must make /readyz fail with a reason naming
// the domain and the resource, and a later clean pass must restore it.
func TestHostReconcileFailureDegradesReadinessUntilRetrySucceeds(t *testing.T) {
	names := []string{}
	for _, domain := range (&App{}).hostReconcilers() {
		names = append(names, domain.name)
	}
	previousFirst, previousMax := hostReconcileRetryFirst, hostReconcileRetryMax
	hostReconcileRetryFirst, hostReconcileRetryMax = time.Millisecond, 2*time.Millisecond
	t.Cleanup(func() { hostReconcileRetryFirst, hostReconcileRetryMax = previousFirst, previousMax })
	for _, failing := range names {
		t.Run(failing, func(t *testing.T) {
			app := readinessTestApp(t)
			var healthy atomic.Bool
			var domains []hostReconciler
			for _, name := range names {
				domains = append(domains, hostReconciler{name: name, run: func(context.Context) ([]string, map[string]string) {
					if name == failing && !healthy.Load() {
						return []string{"site-a"}, map[string]string{"site-b": "helper refused"}
					}
					return []string{"site-a", "site-b"}, nil
				}})
			}
			app.startup.begin()
			failed, errs := runHostReconcilers(domains, time.Second)
			app.startup.setHostState(errors.Join(errs...))
			app.startup.finish(nil)
			if len(failed) != 1 || failed[0].name != failing {
				t.Fatalf("failed domains = %v", failed)
			}
			response := readyzResponse(app)
			body := response.Body.String()
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(body, "startup_state") || !strings.Contains(body, failing+" reconciliation failed for 1 of 2 resource(s): site-b: helper refused") {
				t.Fatalf("status = %d, body = %s", response.Code, body)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { app.retryHostReconcilers(ctx, failed, time.Second); close(done) }()
			time.Sleep(20 * time.Millisecond)
			if response := readyzResponse(app); response.Code != http.StatusServiceUnavailable {
				t.Fatalf("readiness recovered while %s still fails: %s", failing, response.Body.String())
			}
			healthy.Store(true)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("host reconciliation retries did not stop after the domain recovered")
			}
			if response := readyzResponse(app); response.Code != http.StatusOK {
				t.Fatalf("readiness did not recover after the retry: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestHostReconcileRetryKeepsOtherStartupFailures(t *testing.T) {
	app := readinessTestApp(t)
	app.startup.begin()
	app.startup.setHostState(errors.New("routes reconciliation failed"))
	app.startup.finish(errors.New("flush audit outbox during startup: disk full"))
	app.startup.setHostState(nil)
	response := readyzResponse(app)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "flush audit outbox") || strings.Contains(response.Body.String(), "routes reconciliation") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestHostReconcileErrorBoundsResourceList(t *testing.T) {
	failed := map[string]string{}
	for i := range 8 {
		failed[fmt.Sprintf("site-%d", i)] = "unavailable"
	}
	message := hostReconcileError("workers", 2, failed).Error()
	if !strings.Contains(message, "failed for 8 of 10 resource(s)") || !strings.Contains(message, "site-4: unavailable") || strings.Contains(message, "site-5") || !strings.Contains(message, "and 3 more") {
		t.Fatalf("error = %s", message)
	}
}

func TestHostReconcileRetryStopsWithContext(t *testing.T) {
	app := readinessTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	failing := []hostReconciler{{name: "routes", run: func(context.Context) ([]string, map[string]string) {
		t.Error("a cancelled retry loop ran a reconciler")
		return nil, nil
	}}}
	app.retryHostReconcilers(ctx, failing, time.Second)
}
