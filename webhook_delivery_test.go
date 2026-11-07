package stepanel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const webhookTestSecret = "per-site-webhook-secret-0123456789"

// newWebhookDeliveryFixture uses the durable replay cache on the job store's
// database, as production does.
func newWebhookDeliveryFixture(t *testing.T) *App {
	t.Helper()
	app, _ := newSiteOperationFixture(t, http.StatusOK, "{}")
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "webhooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	app.Webhooks = NewWebhookConfigStore(db)
	if err := app.Webhooks.InitializeSchema(); err != nil {
		t.Fatal(err)
	}
	if err := app.Webhooks.SetWebhookConfig("shop", webhookTestSecret, []string{"https://github.com/acme/shop.git"}, []string{"main"}); err != nil {
		t.Fatal(err)
	}
	app.webhookReplayCache = NewDurableWebhookReplayCache(app.Jobs.db, 5*time.Minute)
	return app
}

func signedWebhookAt(t *testing.T, site, body, deliveryID string, at time.Time) *http.Request {
	t.Helper()
	timestamp := at.UTC().Format(time.RFC3339)
	digest := hmac.New(sha256.New, []byte(webhookTestSecret))
	_, _ = digest.Write(webhookSignatureMessage(timestamp, deliveryID, []byte(body)))
	r := httptest.NewRequest(http.MethodPost, "/api/sites/git-webhook/"+site, strings.NewReader(body))
	r.Header.Set("X-StePanel-Delivery-ID", deliveryID)
	r.Header.Set("X-StePanel-Delivery-Timestamp", timestamp)
	r.Header.Set("X-StePanel-Signature", "sha256="+hex.EncodeToString(digest.Sum(nil)))
	return r
}

func countSiteOperationJobs(t *testing.T, app *App) int {
	t.Helper()
	var n int
	if err := app.Jobs.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind=?`, siteOperationKind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const webhookTestBody = `{"repository":"https://github.com/acme/shop.git","ref":"main"}`

// F-01: a delivery whose job could not be queued must stay retriable, and a
// successful delivery must be queued exactly once.
func TestWebhookDeliveryRetriesAfterQueueFailure(t *testing.T) {
	app := newWebhookDeliveryFixture(t)
	signedAt := time.Now()
	if _, err := app.Jobs.db.Exec(`CREATE TRIGGER fail_job_insert BEFORE INSERT ON jobs BEGIN SELECT RAISE(ABORT, 'injected queue outage'); END`); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	app.gitWebhook(response, signedWebhookAt(t, "shop", webhookTestBody, "delivery-retry", signedAt))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("delivery during a queue outage: status %d %s, want 503", response.Code, response.Body.String())
	}
	if _, err := app.Jobs.db.Exec(`DROP TRIGGER fail_job_insert`); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	app.gitWebhook(response, signedWebhookAt(t, "shop", webhookTestBody, "delivery-retry", signedAt))
	if response.Code != http.StatusAccepted {
		t.Fatalf("retry of the same signed delivery: status %d %s, want 202", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	app.gitWebhook(response, signedWebhookAt(t, "shop", webhookTestBody, "delivery-retry", signedAt))
	if response.Code != http.StatusConflict {
		t.Fatalf("replay after success: status %d %s, want 409", response.Code, response.Body.String())
	}
	if n := countSiteOperationJobs(t, app); n != 1 {
		t.Fatalf("queued deployments = %d, want exactly 1", n)
	}
}

// A crash after the job commits but before the replay record is written must
// not let the delivery queue a second deployment.
func TestWebhookDeliveryIsQueuedOnceWithoutReplayRecord(t *testing.T) {
	app := newWebhookDeliveryFixture(t)
	signedAt := time.Now()
	response := httptest.NewRecorder()
	app.gitWebhook(response, signedWebhookAt(t, "shop", webhookTestBody, "delivery-crash", signedAt))
	if response.Code != http.StatusAccepted {
		t.Fatalf("first delivery: status %d %s", response.Code, response.Body.String())
	}
	if _, err := app.Jobs.db.Exec(`DELETE FROM webhook_deliveries`); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	app.gitWebhook(response, signedWebhookAt(t, "shop", webhookTestBody, "delivery-crash", signedAt))
	if response.Code != http.StatusConflict {
		t.Fatalf("retry after a lost replay record: status %d %s, want 409", response.Code, response.Body.String())
	}
	if n := countSiteOperationJobs(t, app); n != 1 {
		t.Fatalf("queued deployments = %d, want exactly 1", n)
	}
}

// A body refused for naming another site must not consume the delivery ID.
func TestRefusedWebhookDoesNotConsumeDeliveryID(t *testing.T) {
	app := newWebhookDeliveryFixture(t)
	signedAt := time.Now()
	response := httptest.NewRecorder()
	app.gitWebhook(response, signedWebhookAt(t, "shop", `{"site":"other","repository":"https://github.com/acme/shop.git","ref":"main"}`, "delivery-refused", signedAt))
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-site body: status %d %s, want 403", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	app.gitWebhook(response, signedWebhookAt(t, "shop", webhookTestBody, "delivery-refused", signedAt))
	if response.Code != http.StatusAccepted {
		t.Fatalf("valid delivery reusing a refused ID: status %d %s, want 202", response.Code, response.Body.String())
	}
}

// F-02: a future-dated delivery stays valid for maxAge after its signed time,
// so its replay record and job key must last at least that long.
func TestFutureDatedWebhookReplayRecordOutlivesSignature(t *testing.T) {
	app := newWebhookDeliveryFixture(t)
	signedAt := time.Now().Add(4 * time.Minute)
	response := httptest.NewRecorder()
	app.gitWebhook(response, signedWebhookAt(t, "shop", webhookTestBody, "delivery-future", signedAt))
	if response.Code != http.StatusAccepted {
		t.Fatalf("future-dated delivery: status %d %s", response.Code, response.Body.String())
	}
	signatureExpiry := signedAt.Truncate(time.Second).Add(5 * time.Minute)
	var recordExpiry, keyExpiry int64
	if err := app.Jobs.db.QueryRow(`SELECT expires_at FROM webhook_deliveries WHERE site='shop' AND delivery_id='delivery-future'`).Scan(&recordExpiry); err != nil {
		t.Fatal(err)
	}
	if err := app.Jobs.db.QueryRow(`SELECT idempotency_expires_at FROM jobs WHERE kind=? AND owner='shop'`, siteOperationKind).Scan(&keyExpiry); err != nil {
		t.Fatal(err)
	}
	for name, expiry := range map[string]int64{"replay record": recordExpiry, "job idempotency key": keyExpiry} {
		if !time.Unix(0, expiry).After(signatureExpiry) {
			t.Fatalf("%s expires at %s, before the signature stops being valid at %s", name, time.Unix(0, expiry).UTC(), signatureExpiry.UTC())
		}
	}
}
