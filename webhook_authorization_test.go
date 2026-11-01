package stepanel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	webhookSecretA = "webhook-secret-a-0123456789abcdef0123"
	webhookSecretB = "webhook-secret-b-0123456789abcdef0123"
	webhookRepo    = "https://github.com/acme/shop.git"
	webhookBody    = `{"repository":"https://github.com/acme/shop.git","ref":"main"}`
)

func webhookFixture(t *testing.T) (*App, *siteOperationObservation) {
	t.Helper()
	app, seen := newSiteOperationFixture(t, http.StatusOK, "{}")
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "webhooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	app.Webhooks = NewWebhookConfigStore(db)
	if err := app.Webhooks.InitializeSchema(); err != nil {
		t.Fatal(err)
	}
	app.webhookReplayCache = NewWebhookReplayCache(5 * time.Minute)
	return app, seen
}

// queueSignedDelivery delivers a signed webhook and returns the queued job,
// rewritten to run the test operation instead of a real Git deployment.
// releaseJob ends a handled job's claim so the site's next job can be
// claimed; jobs for one site run one at a time.
func releaseJob(t *testing.T, app *App, item Job) {
	t.Helper()
	if err := app.Jobs.FailClaim(item.ID, item.LeaseToken, "test finished"); err != nil {
		t.Fatal(err)
	}
}

func queueSignedDelivery(t *testing.T, app *App, site, secret, delivery string) Job {
	t.Helper()
	response := httptest.NewRecorder()
	app.gitWebhook(response, signedWebhook(t, site, secret, webhookBody, delivery))
	if response.Code != http.StatusAccepted {
		t.Fatalf("delivery %s status = %d %s", delivery, response.Code, response.Body.String())
	}
	item := claimSiteOperation(t, app)
	var payload siteOperationPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.WebhookGeneration == 0 {
		t.Fatal("queued delivery records no secret generation")
	}
	payload.Operation, payload.Path = "test.echo", "/api/test/echo"
	item.Payload, _ = json.Marshal(payload)
	return item
}

// A delivery accepted under secret A must not deploy once A is rotated to B;
// an allowlist update that keeps the secret does not revoke it.
func TestQueuedWebhookDeliveryIsRevokedBySecretRotation(t *testing.T) {
	app, seen := webhookFixture(t)
	if err := app.Webhooks.SetWebhookConfig("shop", webhookSecretA, []string{webhookRepo}, []string{"main"}); err != nil {
		t.Fatal(err)
	}

	// Same secret, new allowlist: the queued delivery still runs.
	item := queueSignedDelivery(t, app, "shop", webhookSecretA, "delivery-1")
	if change, err := app.Webhooks.ConfigureWebhook("shop", webhookSecretA, []string{webhookRepo}, []string{"main", "release/*"}); err != nil || change != WebhookPolicyUpdated {
		t.Fatalf("policy update = %q, %v", change, err)
	}
	if _, err := app.handleSiteOperationJob(context.Background(), item); err != nil || seen.path == "" {
		t.Fatalf("delivery after a same-secret policy update = %v, ran=%v", err, seen.path != "")
	}
	releaseJob(t, app, item)

	// Rotation to B revokes a delivery accepted under A.
	seen.path = ""
	item = queueSignedDelivery(t, app, "shop", webhookSecretA, "delivery-2")
	if change, err := app.Webhooks.ConfigureWebhook("shop", webhookSecretB, []string{webhookRepo}, []string{"main"}); err != nil || change != WebhookSecretRotated {
		t.Fatalf("rotation = %q, %v", change, err)
	}
	_, err := app.handleSiteOperationJob(context.Background(), item)
	if err == nil || !strings.Contains(err.Error(), "signing secret changed") || !isNonRetryableJobError(err) {
		t.Fatalf("delivery under a rotated secret = %v", err)
	}
	if seen.path != "" {
		t.Fatal("a delivery accepted under the rotated secret deployed")
	}
	releaseJob(t, app, item)
	// A is no longer accepted at all; B is.
	response := httptest.NewRecorder()
	app.gitWebhook(response, signedWebhook(t, "shop", webhookSecretA, webhookBody, "delivery-3"))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("delivery signed with the rotated secret = %d", response.Code)
	}
	item = queueSignedDelivery(t, app, "shop", webhookSecretB, "delivery-4")
	if _, err := app.handleSiteOperationJob(context.Background(), item); err != nil || seen.path == "" {
		t.Fatalf("delivery under the current secret = %v", err)
	}
}

// A delivery queued before generations were recorded carries none and is
// refused rather than trusted.
func TestQueuedWebhookDeliveryWithoutGenerationIsRefused(t *testing.T) {
	app, seen := webhookFixture(t)
	if err := app.Webhooks.SetWebhookConfig("shop", webhookSecretA, []string{webhookRepo}, []string{"main"}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(siteOperationPayload{Operation: "test.echo", Path: "/api/test/echo", Body: []byte(webhookBody), Site: "shop", Actor: "webhook", Webhook: true})
	if _, err := app.Jobs.Enqueue(siteOperationKind, "shop", "", payload, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := app.handleSiteOperationJob(context.Background(), claimSiteOperation(t, app)); err == nil || seen.path != "" {
		t.Fatalf("legacy delivery = %v, ran=%v", err, seen.path != "")
	}
}

// Disabling and then configuring a webhook again re-enables it with a new
// generation, also after the store is reopened (as after a restart).
func TestDisabledWebhookCanBeReenabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "webhooks.db")
	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewWebhookConfigStore(db)
	if err := store.InitializeSchema(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWebhookConfig("shop", webhookSecretA, []string{webhookRepo}, []string{"main"}); err != nil {
		t.Fatal(err)
	}
	first, err := store.GetWebhookConfig("shop")
	if err != nil || first == nil {
		t.Fatalf("configured webhook = %v, %v", first, err)
	}
	if err := store.DisableWebhookConfig("shop"); err != nil {
		t.Fatal(err)
	}
	if config, err := store.GetWebhookConfig("shop"); err != nil || config != nil {
		t.Fatalf("disabled webhook = %+v, %v", config, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store = NewWebhookConfigStore(db)
	if err := store.InitializeSchema(); err != nil {
		t.Fatal(err)
	}
	if change, err := store.ConfigureWebhook("shop", webhookSecretA, []string{webhookRepo}, []string{"main"}); err != nil || change != WebhookConfigReenabled {
		t.Fatalf("re-enable = %q, %v", change, err)
	}
	config, err := store.GetWebhookConfig("shop")
	if err != nil || config == nil {
		t.Fatalf("re-enabled webhook = %v, %v", config, err)
	}
	// Re-enabling, even with the same secret, revokes deliveries accepted
	// before the webhook was disabled.
	if config.SecretGeneration <= first.SecretGeneration {
		t.Fatalf("generation %d after re-enable, was %d", config.SecretGeneration, first.SecretGeneration)
	}
}

// create → configure webhook → terminate → recreate the same name → the old
// signed webhook (and a delivery queued before termination) must not deploy.
func TestTerminatedSiteNameDoesNotKeepWebhookAuthorization(t *testing.T) {
	f := newSiteCreationFixture(t, "")
	store := NewWebhookConfigStore(f.app.Jobs.db)
	if err := store.InitializeSchema(); err != nil {
		t.Fatal(err)
	}
	f.app.Webhooks = store
	if _, err := f.run(t, "shop"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWebhookConfig("shop", webhookSecretA, []string{webhookRepo}, []string{"main"}); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetWebhookConfig("shop")
	if err != nil || before == nil {
		t.Fatal(err)
	}
	if err := f.app.removeSiteState(context.Background(), AuthorizedSite{site: "shop"}); err != nil {
		t.Fatal(err)
	}
	if config, err := store.GetWebhookConfig("shop"); err != nil || config != nil {
		t.Fatalf("terminated site's webhook = %+v, %v", config, err)
	}
	// Revocation is idempotent (termination may be replayed).
	if err := f.app.removeSiteState(context.Background(), AuthorizedSite{site: "shop"}); err != nil {
		t.Fatal(err)
	}

	// The new owner of the name configures a webhook of their own: the old
	// secret is refused, and a delivery queued under the old site cannot
	// match the new generation.
	if err := store.SetWebhookConfig("shop", webhookSecretB, []string{webhookRepo}, []string{"main"}); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetWebhookConfig("shop")
	if err != nil || after == nil || after.SecretGeneration <= before.SecretGeneration {
		t.Fatalf("recreated webhook = %+v, %v (old generation %d)", after, err, before.SecretGeneration)
	}

	app, seen := webhookFixture(t)
	app.Webhooks = store
	response := httptest.NewRecorder()
	app.gitWebhook(response, signedWebhook(t, "shop", webhookSecretA, webhookBody, "old-secret"))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("old site's signed webhook on the reused name = %d", response.Code)
	}
	payload, _ := json.Marshal(siteOperationPayload{Operation: "test.echo", Path: "/api/test/echo", Body: []byte(webhookBody), Site: "shop", Actor: "webhook", Webhook: true, WebhookGeneration: before.SecretGeneration})
	if _, err := app.Jobs.Enqueue(siteOperationKind, "shop", "", payload, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := app.handleSiteOperationJob(context.Background(), claimSiteOperation(t, app)); err == nil || seen.path != "" {
		t.Fatalf("delivery queued before termination = %v, ran=%v", err, seen.path != "")
	}
}

// A new site never inherits webhook authorization left under its name.
func TestSiteCreationClearsInheritedWebhookAuthorization(t *testing.T) {
	f := newSiteCreationFixture(t, "")
	store := NewWebhookConfigStore(f.app.Jobs.db)
	if err := store.InitializeSchema(); err != nil {
		t.Fatal(err)
	}
	f.app.Webhooks = store
	// Left behind, for example, by a site removed before termination
	// revoked webhooks.
	if err := store.SetWebhookConfig("shop", webhookSecretA, []string{webhookRepo}, []string{"main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(t, "shop"); err != nil {
		t.Fatal(err)
	}
	if config, err := store.GetWebhookConfig("shop"); err != nil || config != nil {
		t.Fatalf("new site inherited webhook %+v, %v", config, err)
	}
}
