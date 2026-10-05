package stepanel

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// legacySeal reproduces the pre-v2 format: AES-GCM under SHA-256(secret),
// nonce || ciphertext, no associated data.
func legacySeal(t *testing.T, secret string, plaintext []byte) []byte {
	t.Helper()
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	return aead.Seal(append([]byte(nil), nonce...), nonce, plaintext, nil)
}

func openTestControlPlane(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func encryptionMigrated(t *testing.T, db *sql.DB, store string) bool {
	t.Helper()
	allowed, err := legacyCiphertextAllowed(db, store)
	if err != nil {
		t.Fatal(err)
	}
	return !allowed
}

func TestEnvironmentSecretsAreBoundToSiteAndName(t *testing.T) {
	store, err := OpenEnvironmentStore(filepath.Join(t.TempDir(), "environment.json"), "environment-key")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := store.encrypt("site-a", "DB_PASSWORD", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, environmentSecretPrefix) {
		t.Fatalf("sealed value %q is not in the context-bound format", sealed)
	}
	if plain, err := store.decrypt("site-a", "DB_PASSWORD", sealed); err != nil || plain != "hunter2" {
		t.Fatalf("decrypt = %q, %v", plain, err)
	}
	if _, err := store.decrypt("site-b", "DB_PASSWORD", sealed); err == nil {
		t.Fatal("secret moved to another site was decrypted")
	}
	if _, err := store.decrypt("site-a", "PUBLIC_LABEL", sealed); err == nil {
		t.Fatal("secret moved to another variable was decrypted")
	}
}

func TestEnvironmentLegacySecretsMigrateThenAreRefused(t *testing.T) {
	db := openTestControlPlane(t)
	path := filepath.Join(t.TempDir(), "environment.json")
	legacy := base64.RawStdEncoding.EncodeToString(legacySeal(t, "environment-key", []byte("old-secret")))
	state, err := json.Marshal(map[string]map[string]environmentValue{"site-a": {"API_KEY": {Value: legacy, Secret: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(path, state, 0600); err != nil {
		t.Fatal(err)
	}

	// Startup sequence from Main.
	store, err := OpenEnvironmentStore(path, "environment-key")
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := legacyCiphertextAllowed(db, encryptionStoreEnvironment)
	if err != nil || !allowed {
		t.Fatalf("fresh database: legacy allowed = %v, %v", allowed, err)
	}
	store.setLegacyCiphertextAllowed(allowed)
	if found, err := bindControlPlaneState(store, db, "environment", store); err != nil {
		t.Fatal(err)
	} else if !found {
		store.mu.Lock()
		err = store.persistLocked()
		store.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.completeEncryptionMigration(db); err != nil {
		t.Fatal(err)
	}
	if !encryptionMigrated(t, db, encryptionStoreEnvironment) {
		t.Fatal("environment migration was not recorded")
	}
	payload, _, err := readControlPlaneBlobWithRevision(db, "environment")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), legacy) || !strings.Contains(string(payload), environmentSecretPrefix) {
		t.Fatalf("durable environment state was not re-sealed: %s", payload)
	}
	store.mu.RLock()
	value := store.values["site-a"]["API_KEY"].Value
	store.mu.RUnlock()
	if value != "old-secret" {
		t.Fatalf("migrated secret = %q", value)
	}
	// After migration the legacy format is refused, so an old ciphertext
	// copied back from a backup cannot be planted.
	if _, err := store.decrypt("site-a", "API_KEY", legacy); err == nil {
		t.Fatal("legacy ciphertext accepted after migration")
	}
}

func TestAccountTOTPIsBoundToUsernameAndLegacyMigrates(t *testing.T) {
	db := openTestControlPlane(t)
	const key = "account-key"
	store, err := OpenAccountStoreDB(db, "", key)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob"} {
		if _, err := store.Create(name, "a sufficiently long customer password", testTOTPSecret, "starter", nil); err != nil {
			t.Fatal(err)
		}
	}
	rawPayload := func(username string) map[string]any {
		var payload []byte
		if err := db.QueryRow(`SELECT payload FROM accounts WHERE username = ?`, username).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(payload, &fields); err != nil {
			t.Fatal(err)
		}
		return fields
	}
	writePayload := func(username string, fields map[string]any) {
		data, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE accounts SET payload = ? WHERE username = ?`, data, username); err != nil {
			t.Fatal(err)
		}
	}
	alice := rawPayload("alice")
	if seed, _ := alice["totp_secret"].(string); !strings.HasPrefix(seed, accountTOTPPrefix) {
		t.Fatalf("stored TOTP %q is not context-bound", seed)
	}

	// Moving Alice's sealed seed onto Bob's account must not authenticate as
	// Bob with Alice's authenticator.
	bob := rawPayload("bob")
	original := bob["totp_secret"]
	bob["totp_secret"] = alice["totp_secret"]
	writePayload("bob", bob)
	if _, ok := store.Get("bob"); ok {
		t.Fatal("account loaded with a TOTP seed moved from another account")
	}
	bob["totp_secret"] = original
	writePayload("bob", bob)

	// A database from before this release holds legacy seeds and no
	// migration record: it loads, is re-sealed, and the format is recorded.
	legacySeed := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(legacySeal(t, key, []byte(testTOTPSecret)))
	alice["totp_secret"] = legacySeed
	writePayload("alice", alice)
	if _, err := db.Exec(`DELETE FROM encryption_formats`); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAccountStoreDB(db, "", key)
	if err != nil {
		t.Fatalf("legacy account state did not load: %v", err)
	}
	if account, ok := reopened.Get("alice"); !ok || account.TOTPSecret != testTOTPSecret {
		t.Fatal("legacy TOTP seed was not migrated")
	}
	if seed, _ := rawPayload("alice")["totp_secret"].(string); !strings.HasPrefix(seed, accountTOTPPrefix) {
		t.Fatalf("legacy TOTP seed was not re-sealed: %q", seed)
	}
	if !encryptionMigrated(t, db, encryptionStoreAccountTOTP) {
		t.Fatal("account TOTP migration was not recorded")
	}

	// After migration a legacy seed is refused at runtime and at startup.
	alice = rawPayload("alice")
	alice["totp_secret"] = legacySeed
	writePayload("alice", alice)
	if _, ok := reopened.Get("alice"); ok {
		t.Fatal("legacy TOTP seed accepted after migration")
	}
	if _, err := OpenAccountStoreDB(db, "", key); err == nil {
		t.Fatal("startup accepted a legacy TOTP seed after migration")
	}
}

func TestJobPayloadsAreBoundToTheirJob(t *testing.T) {
	db := openTestControlPlane(t)
	const key = "account-key"
	jobs, err := openDurableJobsDBWithKey(db, "", key, 1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := jobs.Enqueue("backup.restore", "site-a", "", []byte(`{"site":"site-a","mode":"files"}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := jobs.Enqueue("backup.restore", "site-b", "", []byte(`{"site":"site-b","mode":"database"}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !encryptionMigrated(t, db, encryptionStoreJobPayloads) {
		t.Fatal("a fresh job store was not recorded as context-bound")
	}
	rowPayload := func(id string) json.RawMessage {
		var raw []byte
		if err := db.QueryRow(`SELECT payload FROM jobs WHERE id = ?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		return fields["payload"]
	}
	setRowPayload := func(id string, payload json.RawMessage) {
		var raw []byte
		if err := db.QueryRow(`SELECT payload FROM jobs WHERE id = ?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		fields["payload"] = payload
		data, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE jobs SET payload = ? WHERE id = ?`, data, id); err != nil {
			t.Fatal(err)
		}
	}
	reopen := func() error {
		_, err := openDurableJobsDBWithKey(db, "", key, 1)
		return err
	}

	// Moving site A's restore request into site B's job must not run it.
	firstPayload, secondPayload := rowPayload(first.ID), rowPayload(second.ID)
	setRowPayload(second.ID, firstPayload)
	if err := reopen(); err == nil {
		t.Fatal("job store loaded a payload moved from another job")
	}
	setRowPayload(second.ID, secondPayload)
	if err := reopen(); err != nil {
		t.Fatalf("restored job store did not load: %v", err)
	}

	// A legacy (SPJ1) payload from before this release is migrated once.
	legacyBytes := append(append([]byte(nil), encryptedJobPayloadPrefix...), legacySeal(t, key, []byte(`{"site":"site-a","mode":"files"}`))...)
	legacy, err := json.Marshal(legacyBytes)
	if err != nil {
		t.Fatal(err)
	}
	setRowPayload(first.ID, legacy)
	if _, err := db.Exec(`DELETE FROM encryption_formats`); err != nil {
		t.Fatal(err)
	}
	migrated, err := openDurableJobsDBWithKey(db, "", key, 1)
	if err != nil {
		t.Fatalf("legacy job payload did not load: %v", err)
	}
	if item, ok := migrated.Get(first.ID); !ok || string(item.Payload) != `{"site":"site-a","mode":"files"}` {
		t.Fatal("legacy job payload was not migrated")
	}
	var resealed []byte
	if err := json.Unmarshal(rowPayload(first.ID), &resealed); err != nil || !strings.HasPrefix(string(resealed), "SPB2") {
		t.Fatalf("legacy job payload was not re-sealed: %q, %v", resealed, err)
	}
	if !encryptionMigrated(t, db, encryptionStoreJobPayloads) {
		t.Fatal("job payload migration was not recorded")
	}

	// After migration, legacy and unsealed payloads are refused.
	setRowPayload(first.ID, legacy)
	if err := reopen(); err == nil {
		t.Fatal("legacy job payload accepted after migration")
	}
	setRowPayload(first.ID, json.RawMessage(`{"site":"site-b","mode":"files"}`))
	if err := reopen(); err == nil {
		t.Fatal("unsealed job payload accepted after migration")
	}
}
