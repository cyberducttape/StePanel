package stepanel

import (
	"database/sql"
	"errors"
	"fmt"
)

// Stores whose secrets are sealed with internal/secretbox. Each records in
// encryption_formats when all of its values have been re-sealed in the
// context-bound format.
const (
	encryptionStoreEnvironment = "environment"
	encryptionStoreAccountTOTP = "account-totp"
	encryptionStoreJobPayloads = "job-payloads"

	contextBoundEncryptionVersion = 2
)

// legacyCiphertextAllowed reports whether store may still read secrets sealed
// without context binding. It is true only until the store has completed its
// migration; without a control-plane database (development file stores)
// there is nowhere to record completion, so legacy values stay readable.
func legacyCiphertextAllowed(db *sql.DB, store string) (bool, error) {
	if db == nil {
		return true, nil
	}
	var version int
	err := db.QueryRow(`SELECT version FROM encryption_formats WHERE store = ?`, store).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s encryption format: %w", store, err)
	}
	return version < contextBoundEncryptionVersion, nil
}

// markContextBoundEncryption records that every secret in store is sealed in
// the context-bound format, after which legacy ciphertexts are refused.
func markContextBoundEncryption(db *sql.DB, store string) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(`INSERT INTO encryption_formats (store, version, updated_at) VALUES (?, ?, unixepoch())
		ON CONFLICT(store) DO UPDATE SET version = excluded.version, updated_at = excluded.updated_at`,
		store, contextBoundEncryptionVersion)
	if err != nil {
		return fmt.Errorf("record %s encryption format: %w", store, err)
	}
	return nil
}
