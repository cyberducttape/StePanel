package stepanel

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	rotationOldKey = "previous-master-key-0123456789abcdefABCDEF"
	rotationNewKey = "current-master-key-0123456789abcdefABCDEF"
)

// After a rotation the account store opens seeds sealed under the previous
// key, re-seals them at startup, and from then on needs only the new key.
func TestAccountKeyRotationReSealsTOTPSeeds(t *testing.T) {
	db := openTestControlPlane(t)
	store, err := OpenAccountStoreDB(db, "", rotationOldKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("customer", "a sufficiently long customer password", testTOTPSecret, "starter", []string{"site-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAccountStoreDB(db, "", rotationNewKey); err == nil {
		t.Fatal("seeds sealed under the previous key opened without it")
	}
	rotated, err := OpenAccountStoreDB(db, "", rotationNewKey, rotationOldKey)
	if err != nil {
		t.Fatalf("open with the previous key listed: %v", err)
	}
	if account, ok := rotated.Get("customer"); !ok || account.TOTPSecret != testTOTPSecret {
		t.Fatalf("rotated account = %#v, %v", account, ok)
	}
	reopened, err := OpenAccountStoreDB(db, "", rotationNewKey)
	if err != nil {
		t.Fatalf("seeds were not re-sealed under the current key: %v", err)
	}
	if account, ok := reopened.Get("customer"); !ok || account.TOTPSecret != testTOTPSecret {
		t.Fatalf("account after retiring the previous key = %#v, %v", account, ok)
	}
	if _, err := OpenAccountStoreDB(db, "", rotationOldKey); err == nil {
		t.Fatal("re-sealed seeds still open with the retired key")
	}
	if _, err := OpenAccountStoreDB(db, "", "", rotationOldKey); err == nil {
		t.Fatal("previous keys were accepted without a current key")
	}
}

func TestEnvironmentKeyRotationReSealsSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment.json")
	store, err := OpenEnvironmentStore(path, rotationOldKey)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.values["site-a"] = map[string]environmentValue{"API_KEY": {Value: "sk-live-123", Secret: true}, "MODE": {Value: "prod"}}
	err = store.persistLocked()
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenEnvironmentStore(path, rotationNewKey); err == nil {
		t.Fatal("secrets sealed under the previous key opened without it")
	}
	rotated, err := OpenEnvironmentStore(path, rotationNewKey, rotationOldKey)
	if err != nil {
		t.Fatalf("open with the previous key listed: %v", err)
	}
	resealed, err := rotated.completeKeyRotation()
	if err != nil || resealed != 1 {
		t.Fatalf("completeKeyRotation = %d, %v; want 1 secret re-sealed", resealed, err)
	}
	reopened, err := OpenEnvironmentStore(path, rotationNewKey)
	if err != nil {
		t.Fatalf("secrets were not re-sealed under the current key: %v", err)
	}
	if value := reopened.values["site-a"]["API_KEY"]; value.Value != "sk-live-123" || !value.Secret {
		t.Fatalf("secret after retiring the previous key = %#v", value)
	}
	if again, err := reopened.completeKeyRotation(); err != nil || again != 0 {
		t.Fatalf("a store with nothing to re-seal rewrote itself: %d, %v", again, err)
	}
	if _, err := OpenEnvironmentStore(path, "", rotationOldKey); err == nil {
		t.Fatal("previous keys were accepted without a current key")
	}
}

// Every job row is re-sealed, queued and finished alike. The re-seal scans
// the whole jobs table rather than the jobs startup caches in memory.
func TestJobPayloadKeyRotationReSealsEveryRow(t *testing.T) {
	db := openTestControlPlane(t)
	jobs, err := openDurableJobsDBWithKey(db, "", rotationOldKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := jobs.Enqueue("site.backup", "site-a", "", []byte(`{"secret":"archive-db-password"}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	// A finished job.
	finished, err := jobs.Enqueue("site.backup", "site-b", "", []byte(`{"secret":"older-password"}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET state = 'completed', finished_at = 1 WHERE id = ?`, finished.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := openDurableJobsDBWithKey(db, "", rotationNewKey, 1); err == nil {
		t.Fatal("payloads sealed under the previous key opened without it")
	}
	rotated, err := openDurableJobsDBWithKeys(db, "", rotationNewKey, []string{rotationOldKey}, 1)
	if err != nil {
		t.Fatalf("open with the previous key listed: %v", err)
	}
	if job, ok := rotated.Get(queued.ID); !ok || string(job.Payload) != `{"secret":"archive-db-password"}` {
		t.Fatalf("rotated queued job = %q, %v", job.Payload, ok)
	}
	retired, err := openDurableJobsDBWithKey(db, "", rotationNewKey, 1)
	if err != nil {
		t.Fatalf("payloads were not re-sealed under the current key: %v", err)
	}
	for id, want := range map[string]string{queued.ID: `{"secret":"archive-db-password"}`, finished.ID: `{"secret":"older-password"}`} {
		var raw []byte
		if err := db.QueryRow(`SELECT payload FROM jobs WHERE id = ?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "password") {
			t.Fatalf("job %s payload is stored in plaintext after re-sealing", id)
		}
		var persisted persistedJob
		if err := json.Unmarshal(raw, &persisted); err != nil {
			t.Fatal(err)
		}
		opened, err := retired.openPayload(&persisted.Job, decodePersistedJobPayload(persisted.Payload))
		if err != nil || string(opened) != want {
			t.Fatalf("job %s payload under the current key only = %q, %v", id, opened, err)
		}
	}
	if _, err := openDurableJobsDBWithKeys(db, "", "", []string{rotationOldKey}, 1); err == nil {
		t.Fatal("previous keys were accepted without a current key")
	}
}

func TestWebhookSecretKeyRotationReSealsSecrets(t *testing.T) {
	db := openTestControlPlane(t)
	store := NewWebhookConfigStore(db)
	if err := store.InitializeSchema(); err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareSecrets(rotationOldKey); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWebhookConfig("shop", webhookTestSecret, []string{"https://github.com/acme/shop.git"}, []string{"main"}); err != nil {
		t.Fatal(err)
	}
	var unreadable *UnreadableWebhookSecretsError
	if err := NewWebhookConfigStore(db).PrepareSecrets(rotationNewKey); !errors.As(err, &unreadable) {
		t.Fatalf("secrets sealed under the previous key opened without it: %v", err)
	}
	if err := NewWebhookConfigStore(db).PrepareSecrets(rotationNewKey, rotationOldKey); err != nil {
		t.Fatalf("prepare with the previous key listed: %v", err)
	}
	retired := NewWebhookConfigStore(db)
	if err := retired.PrepareSecrets(rotationNewKey); err != nil {
		t.Fatalf("secrets were not re-sealed under the current key: %v", err)
	}
	config, err := retired.GetWebhookConfig("shop")
	if err != nil || config == nil || config.WebhookSecret != webhookTestSecret {
		t.Fatalf("webhook after retiring the previous key = %+v, %v", config, err)
	}
	if err := NewWebhookConfigStore(db).PrepareSecrets("", rotationOldKey); err == nil {
		t.Fatal("previous keys were accepted without a current key")
	}
}

func TestPreviousKeyConfigurationIsValidated(t *testing.T) {
	current := rotationNewKey
	cases := []struct {
		name     string
		previous []string
		current  string
		want     string
	}{
		{"short", []string{"too-short"}, current, "shorter than 32"},
		{"repeats current", []string{current}, current, "repeats the current"},
		{"duplicate", []string{rotationOldKey, rotationOldKey}, current, "repeats an earlier entry"},
		{"no current key", []string{rotationOldKey}, "", "requires STEPANEL_ACCOUNT_KEY"},
	}
	for _, test := range cases {
		problems := previousKeyProblems("STEPANEL_ACCOUNT_PREVIOUS_KEYS", test.previous, "STEPANEL_ACCOUNT_KEY", test.current)
		joined := errors.Join(problems...)
		if joined == nil || !strings.Contains(joined.Error(), test.want) {
			t.Errorf("%s: problems = %v, want %q", test.name, joined, test.want)
		}
	}
	if problems := previousKeyProblems("STEPANEL_ACCOUNT_PREVIOUS_KEYS", []string{rotationOldKey}, "STEPANEL_ACCOUNT_KEY", current); len(problems) != 0 {
		t.Fatalf("a valid previous key was refused: %v", problems)
	}
}

// Idempotency records are kept indefinitely, so a retry first sent before a
// rotation must replay its stored token after the previous key is retired.
func TestAPITokenIdempotencyKeyRotationReSealsResults(t *testing.T) {
	db := openTestControlPlane(t)
	before := &apiTokenStore{db: db}
	if err := before.configureIdempotencyKey(rotationOldKey); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour).Unix()
	create := func(store *apiTokenStore) (apiTokenInfo, string, error) {
		return store.createScopedIdempotent("customer", "automation", &expires, []string{"site:read"}, customerAPIScopes, "auth.api_token.create", "retry-token-1")
	}
	first, secret, err := create(before)
	if err != nil {
		t.Fatal(err)
	}
	rotated := &apiTokenStore{db: db}
	if err := rotated.configureIdempotencyKey(rotationNewKey, rotationOldKey); err != nil {
		t.Fatal(err)
	}
	if resealed, err := rotated.resealRotatedIdempotencyResults(); err != nil || resealed != 1 {
		t.Fatalf("resealRotatedIdempotencyResults = %d, %v; want 1", resealed, err)
	}
	retired := &apiTokenStore{db: db}
	if err := retired.configureIdempotencyKey(rotationNewKey); err != nil {
		t.Fatal(err)
	}
	replayed, replayedSecret, err := create(retired)
	if err != nil {
		t.Fatalf("replay after retiring the previous key: %v", err)
	}
	if replayed.ID != first.ID || replayedSecret != secret {
		t.Fatalf("replay = %s/%q, want the original %s token", replayed.ID, replayedSecret, first.ID)
	}
	if resealed, err := retired.resealRotatedIdempotencyResults(); err != nil || resealed != 0 {
		t.Fatalf("a store without previous keys re-sealed %d results: %v", resealed, err)
	}
}
