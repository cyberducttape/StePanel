package stepanel

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sessionstate "github.com/cyberducttape/StePanel/internal/session"
)

// queuedSessionApp has a real session registry and a customer account.
func queuedSessionApp(t *testing.T) (*App, *siteOperationObservation) {
	t.Helper()
	app, seen := newSiteOperationFixture(t, 200, "{}")
	app.Auth.sessions = &sessionRegistry{inner: sessionstate.New("")}
	accounts, err := OpenAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Create("customer", "a sufficiently long customer password", testTOTPSecret, "starter", []string{"shop"}); err != nil {
		t.Fatal(err)
	}
	app.Accounts = accounts
	expiry := time.Now().Add(time.Hour).Unix()
	for _, id := range []string{"laptop", "phone"} {
		if err := app.Auth.sessions.add(id, "customer", expiry); err != nil {
			t.Fatal(err)
		}
	}
	return app, seen
}

func bindingFor(t *testing.T, app *App, sessionID string) queuedSession {
	t.Helper()
	generation, err := app.Auth.sessions.generation("customer")
	if err != nil {
		t.Fatal(err)
	}
	return queuedSession{SessionGeneration: generation, SessionHash: sessionstate.HashSessionID(sessionID)}
}

// The browser-session queued-work contract (revalidateQueuedSession).
func TestQueuedBrowserSessionWorkFollowsAccountRevocation(t *testing.T) {
	t.Run("logout keeps queued work", func(t *testing.T) {
		app, _ := queuedSessionApp(t)
		binding := bindingFor(t, app, "laptop")
		if err := app.Auth.sessions.revoke("laptop"); err != nil {
			t.Fatal(err)
		}
		if err := app.revalidateQueuedSession("customer", binding); err != nil {
			t.Fatalf("work queued before a logout = %v", err)
		}
	})
	t.Run("account-wide revocation cancels queued work", func(t *testing.T) {
		app, _ := queuedSessionApp(t)
		binding := bindingFor(t, app, "laptop")
		if err := app.Auth.sessions.revokeUser("customer"); err != nil {
			t.Fatal(err)
		}
		err := app.revalidateQueuedSession("customer", binding)
		if err == nil || !isNonRetryableJobError(err) || !strings.Contains(err.Error(), "revoked") {
			t.Fatalf("work queued before an account-wide revocation = %v", err)
		}
		// Work queued after the revocation (a new login) runs.
		if err := app.Auth.sessions.add("new-login", "customer", time.Now().Add(time.Hour).Unix()); err != nil {
			t.Fatal(err)
		}
		if err := app.revalidateQueuedSession("customer", bindingFor(t, app, "new-login")); err != nil {
			t.Fatalf("work queued after re-login = %v", err)
		}
	})
	t.Run("logging out other devices keeps only the surviving session's work", func(t *testing.T) {
		app, _ := queuedSessionApp(t)
		laptop, phone := bindingFor(t, app, "laptop"), bindingFor(t, app, "phone")
		if err := app.Auth.sessions.revokeUserExcept("customer", "laptop"); err != nil {
			t.Fatal(err)
		}
		if err := app.revalidateQueuedSession("customer", laptop); err != nil {
			t.Fatalf("surviving session's work = %v", err)
		}
		if err := app.revalidateQueuedSession("customer", phone); err == nil {
			t.Fatal("work queued by a logged-out device ran")
		}
	})
	t.Run("suspended account", func(t *testing.T) {
		app, _ := queuedSessionApp(t)
		binding := bindingFor(t, app, "laptop")
		if _, err := app.Accounts.SetSuspended("customer", true); err != nil {
			t.Fatal(err)
		}
		if err := app.revalidateQueuedSession("customer", binding); err == nil || !strings.Contains(err.Error(), "suspended") {
			t.Fatalf("suspended account's queued work = %v", err)
		}
	})
	t.Run("deleted account", func(t *testing.T) {
		app, _ := queuedSessionApp(t)
		if err := app.revalidateQueuedSession("nobody", queuedSession{}); err == nil || !strings.Contains(err.Error(), "no longer exists") {
			t.Fatalf("deleted account's queued work = %v", err)
		}
	})
}

// End to end through the durable site-operation job: work queued from a
// browser session does not run after the account's sessions are revoked.
func TestQueuedSiteOperationStopsAfterAccountSessionRevocation(t *testing.T) {
	app, seen := queuedSessionApp(t)
	payload, _ := json.Marshal(siteOperationPayload{Operation: "test.echo", Path: "/api/test/echo", Body: []byte(`{}`), Site: "shop", Actor: "customer", queuedSession: bindingFor(t, app, "laptop")})
	if _, err := app.Jobs.Enqueue(siteOperationKind, "shop", "", payload, 1); err != nil {
		t.Fatal(err)
	}
	if err := app.Auth.sessions.revokeUser("customer"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.handleSiteOperationJob(context.Background(), claimSiteOperation(t, app)); err == nil || seen.path != "" {
		t.Fatalf("queued operation after revocation = %v, ran=%v", err, seen.path != "")
	}
}

// Generations live in the shared database, so a worker process sees a
// revocation made by the panel process.
func TestSessionGenerationIsSharedThroughTheControlPlaneDatabase(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	panel, err := sessionstate.OpenDB(db)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := sessionstate.OpenDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := panel.Add("laptop", "customer", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if alive, err := worker.ActiveSessionHash("customer", sessionstate.HashSessionID("laptop")); err != nil || !alive {
		t.Fatalf("worker sees the panel's session = %v, %v", alive, err)
	}
	if err := panel.RevokeUser("customer"); err != nil {
		t.Fatal(err)
	}
	if generation, err := worker.Generation("customer"); err != nil || generation != 1 {
		t.Fatalf("worker sees generation %d, %v", generation, err)
	}
	if alive, _ := worker.ActiveSessionHash("customer", sessionstate.HashSessionID("laptop")); alive {
		t.Fatal("revoked session still active for the worker")
	}
}
