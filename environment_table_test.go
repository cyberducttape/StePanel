package stepanel

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

const (
	envTestKeyA = "environment-key-A-0123456789abcdefghijklmnop"
	envTestKeyB = "environment-key-B-0123456789abcdefghijklmnop"
)

func openEnvironmentForTest(t *testing.T, dbPath, key string, previous ...string) *EnvironmentStore {
	t.Helper()
	db, err := openControlPlaneDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := OpenEnvironmentStore(filepath.Join(filepath.Dir(dbPath), "absent-environment.json"), key, previous...)
	if err != nil {
		t.Fatal(err)
	}
	store.setLegacyCiphertextAllowed(false)
	if err := bindEnvironment(store, db); err != nil {
		t.Fatalf("bind environment with key %q: %v", key[:20], err)
	}
	return store
}

// Key rotation re-seals secrets whose plaintext is unchanged. The table must
// still rewrite them, or removing the previous key would lose them.
func TestEnvironmentKeyRotationRewritesUnchangedSecrets(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control-plane.db")
	first := openEnvironmentForTest(t, dbPath, envTestKeyA)
	if err := first.replaceSiteValues("shop", map[string]environmentValue{"DB_PASSWORD": {Value: "hunter2", Secret: true}, "APP_ENV": {Value: "production"}}, true); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := first.rows.db.QueryRow(`SELECT value FROM environment_variables WHERE site = 'shop' AND name = 'DB_PASSWORD'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "hunter2") || !strings.HasPrefix(stored, environmentSecretPrefix) {
		t.Fatalf("the secret is not sealed in the table: %q", stored)
	}

	rotated := openEnvironmentForTest(t, dbPath, envTestKeyB, envTestKeyA)
	if got := rotated.values["shop"]["DB_PASSWORD"].Value; got != "hunter2" {
		t.Fatalf("secret under the previous key = %q", got)
	}
	opened, err := rotated.completeKeyRotation()
	if err != nil || opened == 0 {
		t.Fatalf("key rotation = %d secrets, %v", opened, err)
	}

	// With the previous key removed, the re-sealed secret still opens.
	after := openEnvironmentForTest(t, dbPath, envTestKeyB)
	if got := after.values["shop"]; got["DB_PASSWORD"].Value != "hunter2" || got["APP_ENV"].Value != "production" {
		t.Fatalf("environment after removing the previous key = %#v", got)
	}
}

// Migration 44 copies sealed secrets as they are: they open afterwards with
// the same key, and the blob is gone.
func TestEnvironmentMigratesFromTheStateBlobSealed(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "control-plane.db")
	db, err := openControlPlaneDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source, err := OpenEnvironmentStore(filepath.Join(dir, "absent.json"), envTestKeyA)
	if err != nil {
		t.Fatal(err)
	}
	source.values = map[string]map[string]environmentValue{"shop": {"DB_PASSWORD": {Value: "hunter2", Secret: true}, "APP_ENV": {Value: "staging"}}}
	payload, err := source.encodePersisted()
	if err != nil {
		t.Fatal(err)
	}
	var check map[string]map[string]environmentValue
	if err := json.Unmarshal(payload, &check); err != nil || strings.Contains(check["shop"]["DB_PASSWORD"].Value, "hunter2") {
		t.Fatalf("the blob fixture holds a plaintext secret: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES ('environment', ?, unixepoch(), 1)`, payload); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateEnvironmentBlob(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := openEnvironmentForTest(t, dbPath, envTestKeyA)
	if got := store.values["shop"]; got["DB_PASSWORD"].Value != "hunter2" || !got["DB_PASSWORD"].Secret || got["APP_ENV"].Value != "staging" {
		t.Fatalf("migrated environment = %#v", got)
	}
	var blobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM state_blobs WHERE name = 'environment'`).Scan(&blobs); err != nil || blobs != 0 {
		t.Fatalf("environment blob after migration: %d, %v", blobs, err)
	}
	// Deleting the site removes its variables.
	store.mu.Lock()
	delete(store.values, "shop")
	err = store.persistLocked()
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var vars int
	if err := db.QueryRow(`SELECT COUNT(*) FROM environment_variables`).Scan(&vars); err != nil || vars != 0 {
		t.Fatalf("environment variables after deleting the site: %d, %v", vars, err)
	}
}
