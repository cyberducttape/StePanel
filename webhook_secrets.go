package stepanel

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/cyberducttape/StePanel/internal/secretbox"
)

// WebhookConfigStore manages per-site webhook configuration in the control-plane database
type WebhookConfigStore struct {
	mu sync.RWMutex
	db *sql.DB
	// box seals signing secrets at rest when an encryption key is
	// configured (PrepareSecrets); without one they stay plaintext, as on
	// development hosts.
	box *secretbox.Box
	// fingerprintKey keys secret_fingerprint, which lets the store find a
	// secret shared by several sites without opening every sealed row.
	fingerprintKey []byte
}

// ErrWebhookSecretReused reports a signing secret already configured for
// another site. The signature covers the delivery, not the target site, so
// each site's secret must be unique for a valid signature to authorize
// exactly one site.
var ErrWebhookSecretReused = errors.New("this webhook secret is already used by another site; use a unique secret per site")

// ErrWebhookSecretShared reports a site whose secret, configured before
// reuse was refused, is still shared with another enabled site. Its
// deliveries are refused until the secret is rotated.
var ErrWebhookSecretShared = errors.New("this site's webhook secret is shared with another site; configure a unique secret before deliveries are accepted")

// webhookSecretSealedPrefix starts the stored form of a sealed signing
// secret. The secret_sealed column, not this prefix, says whether a row is
// sealed: a plaintext secret may legitimately begin with it.
const webhookSecretSealedPrefix = "secretbox:"

// unkeyedWebhookFingerprintKey keys fingerprints on hosts without
// STEPANEL_ENVIRONMENT_KEY, where the secrets themselves are plaintext.
var unkeyedWebhookFingerprintKey = []byte("stepanel/webhook-secret-fingerprint/unkeyed")

// UnreadableWebhookSecretsError lists sites whose sealed signing secret does
// not open with the configured key. Startup stops on it rather than serving
// webhooks that can never verify; see docs/ENCRYPTION_KEYS.md for recovery.
type UnreadableWebhookSecretsError struct {
	Sites []string
}

func (e *UnreadableWebhookSecretsError) Error() string {
	return fmt.Sprintf("webhook signing secrets for %s cannot be decrypted with the configured STEPANEL_ENVIRONMENT_KEY; restore the key they were sealed with, or run `stepanel webhook-secrets disable-unreadable` and configure new secrets", strings.Join(e.Sites, ", "))
}

// UseEncryptionKey is PrepareSecrets; kept for callers that only set a key.
func (wcs *WebhookConfigStore) UseEncryptionKey(key string) error {
	return wcs.PrepareSecrets(key)
}

// PrepareSecrets configures sealing with key (empty on development hosts),
// seals every plaintext secret, refreshes every fingerprint and proves that
// every sealed secret opens. It changes nothing and returns
// *UnreadableWebhookSecretsError when any sealed secret does not open.
func (wcs *WebhookConfigStore) PrepareSecrets(key string) error {
	return wcs.prepareSecrets(key, false)
}

// DisableUnreadableSecrets clears the secret of, and disables, every webhook
// whose sealed secret does not open with key, then prepares the rest. It is
// the recovery path when the key the secrets were sealed with is lost; the
// affected sites need a new secret configured. It returns the sites changed.
func (wcs *WebhookConfigStore) DisableUnreadableSecrets(key string) ([]string, error) {
	var disabled []string
	err := wcs.prepareSecretsReporting(key, true, &disabled)
	return disabled, err
}

func (wcs *WebhookConfigStore) prepareSecrets(key string, disableUnreadable bool) error {
	return wcs.prepareSecretsReporting(key, disableUnreadable, nil)
}

func (wcs *WebhookConfigStore) prepareSecretsReporting(key string, disableUnreadable bool, disabled *[]string) error {
	var box *secretbox.Box
	fingerprintKey := unkeyedWebhookFingerprintKey
	if strings.TrimSpace(key) != "" {
		var err error
		if box, err = secretbox.New([]byte(key), "webhook-secret"); err != nil {
			return fmt.Errorf("initialize webhook secret encryption: %w", err)
		}
		if fingerprintKey, err = hkdf.Key(sha256.New, []byte(key), nil, "stepanel/webhook-secret-fingerprint/v1", 32); err != nil {
			return fmt.Errorf("derive webhook secret fingerprint key: %w", err)
		}
	}
	wcs.mu.Lock()
	defer wcs.mu.Unlock()
	tx, err := beginImmediate(context.Background(), wcs.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id, site, webhook_secret, secret_sealed, secret_fingerprint FROM webhook_configs WHERE webhook_secret <> '' ORDER BY site`)
	if err != nil {
		return fmt.Errorf("read webhook secrets: %w", err)
	}
	type storedSecret struct {
		id                        int64
		site, secret, fingerprint string
		sealed                    bool
	}
	var stored []storedSecret
	for rows.Next() {
		var item storedSecret
		if err := rows.Scan(&item.id, &item.site, &item.secret, &item.sealed, &item.fingerprint); err != nil {
			rows.Close()
			return err
		}
		stored = append(stored, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var unreadable []string
	for _, item := range stored {
		plaintext, readable := item.secret, true
		switch {
		case item.sealed:
			plaintext, readable = openWebhookSecret(box, item.site, item.secret)
		case strings.HasPrefix(item.secret, webhookSecretSealedPrefix):
			// Rows sealed before secret_sealed existed carry only the
			// prefix. A value counts as sealed only if it authenticates
			// under this key and site; anything else is a plaintext secret
			// that happens to start with the prefix. A value in the sealed
			// format that does not open (no key, or a different key) is
			// refused rather than taken for plaintext.
			if opened, ok := openWebhookSecret(box, item.site, item.secret); ok {
				plaintext = opened
			} else if looksLikeSealedWebhookSecret(item.secret) {
				readable = false
			}
		}
		if !readable {
			if !disableUnreadable {
				unreadable = append(unreadable, item.site)
				continue
			}
			if _, err := tx.Exec(`UPDATE webhook_configs SET webhook_secret = '', secret_sealed = 0, secret_fingerprint = '', enabled = 0, secret_generation = secret_generation + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, item.id); err != nil {
				return fmt.Errorf("disable webhook for %s: %w", item.site, err)
			}
			if disabled != nil {
				*disabled = append(*disabled, item.site)
			}
			continue
		}
		value, sealed, err := sealWebhookSecret(box, item.site, plaintext)
		if err != nil {
			return err
		}
		if sealed == item.sealed && sealed {
			value = item.secret // already sealed; keep the ciphertext
		}
		fingerprint := webhookSecretFingerprint(fingerprintKey, plaintext)
		if value == item.secret && sealed == item.sealed && fingerprint == item.fingerprint {
			continue
		}
		if _, err := tx.Exec(`UPDATE webhook_configs SET webhook_secret = ?, secret_sealed = ?, secret_fingerprint = ? WHERE id = ?`, value, sealed, fingerprint, item.id); err != nil {
			return fmt.Errorf("seal webhook secret for %s: %w", item.site, err)
		}
	}
	if len(unreadable) > 0 {
		return &UnreadableWebhookSecretsError{Sites: unreadable}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	wcs.box, wcs.fingerprintKey = box, fingerprintKey
	return nil
}

// looksLikeSealedWebhookSecret reports a value in the stored sealed format.
func looksLikeSealedWebhookSecret(value string) bool {
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, webhookSecretSealedPrefix))
	return err == nil && secretbox.IsSealed(raw)
}

func openWebhookSecret(box *secretbox.Box, site, stored string) (string, bool) {
	if box == nil || !strings.HasPrefix(stored, webhookSecretSealedPrefix) {
		return "", false
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(stored, webhookSecretSealedPrefix))
	if err != nil {
		return "", false
	}
	plaintext, err := box.Open(raw, site)
	if err != nil {
		return "", false
	}
	return string(plaintext), true
}

func sealWebhookSecret(box *secretbox.Box, site, secret string) (string, bool, error) {
	if secret == "" || box == nil {
		return secret, false, nil
	}
	sealed, err := box.Seal([]byte(secret), site)
	if err != nil {
		return "", false, fmt.Errorf("seal webhook secret: %w", err)
	}
	return webhookSecretSealedPrefix + base64.RawStdEncoding.EncodeToString(sealed), true, nil
}

func webhookSecretFingerprint(key []byte, secret string) string {
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(secret))
	return hex.EncodeToString(mac.Sum(nil))
}

func (wcs *WebhookConfigStore) fingerprintLocked(secret string) string {
	key := wcs.fingerprintKey
	if key == nil {
		key = unkeyedWebhookFingerprintKey
	}
	return webhookSecretFingerprint(key, secret)
}

func (wcs *WebhookConfigStore) openSecretLocked(site, stored string, sealed bool) (string, error) {
	if !sealed {
		return stored, nil
	}
	if wcs.box == nil {
		return "", errors.New("webhook secret is encrypted but no encryption key is configured")
	}
	plaintext, ok := openWebhookSecret(wcs.box, site, stored)
	if !ok {
		return "", fmt.Errorf("webhook secret for %s does not open with the configured key", site)
	}
	return plaintext, nil
}

// SitesSharingSecrets reports groups of enabled sites configured with the
// same signing secret before reuse was refused. Deliveries for those sites
// are refused until each has a unique secret.
func (wcs *WebhookConfigStore) SitesSharingSecrets() ([][]string, error) {
	wcs.mu.RLock()
	defer wcs.mu.RUnlock()
	rows, err := wcs.db.Query(`SELECT secret_fingerprint, site FROM webhook_configs
		WHERE enabled = 1 AND secret_fingerprint IN (
			SELECT secret_fingerprint FROM webhook_configs
			WHERE enabled = 1 AND secret_fingerprint <> ''
			GROUP BY secret_fingerprint HAVING COUNT(*) > 1)
		ORDER BY secret_fingerprint, site`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups [][]string
	last := ""
	for rows.Next() {
		var fingerprint, site string
		if err := rows.Scan(&fingerprint, &site); err != nil {
			return nil, err
		}
		if fingerprint != last || len(groups) == 0 {
			groups = append(groups, nil)
			last = fingerprint
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], site)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups, nil
}

// secretSharedLocked reports whether another enabled site holds the secret
// with this fingerprint.
func (wcs *WebhookConfigStore) secretSharedLocked(site, fingerprint string) (bool, error) {
	if fingerprint == "" {
		return false, nil
	}
	var others int
	if err := wcs.db.QueryRow(`SELECT COUNT(*) FROM webhook_configs WHERE site <> ? AND enabled = 1 AND secret_fingerprint = ?`, site, fingerprint).Scan(&others); err != nil {
		return false, fmt.Errorf("check webhook secret uniqueness: %w", err)
	}
	return others > 0, nil
}

// refuseSecretReuseLocked returns ErrWebhookSecretReused when another site,
// enabled or not, already holds secret.
func (wcs *WebhookConfigStore) refuseSecretReuseLocked(tx *immediateTx, site, fingerprint string) error {
	var others int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM webhook_configs WHERE site <> ? AND secret_fingerprint = ?`, site, fingerprint).Scan(&others); err != nil {
		return fmt.Errorf("check webhook secret uniqueness: %w", err)
	}
	if others > 0 {
		return ErrWebhookSecretReused
	}
	return nil
}

// runDisableUnreadableWebhookSecrets is `stepanel webhook-secrets
// disable-unreadable`: the recovery path when the key webhook secrets were
// sealed with is lost. It disables each webhook whose secret cannot be
// opened so the panel can start; each needs a new secret configured.
func runDisableUnreadableWebhookSecrets(cfg Config) error {
	db, err := openControlPlaneDB(filepath.Clean(cfg.ControlPlaneDB))
	if err != nil {
		return err
	}
	defer db.Close()
	store := NewWebhookConfigStore(db)
	if err := store.InitializeSchema(); err != nil {
		return fmt.Errorf("initialize webhook configuration schema: %w", err)
	}
	// Removing credentials is Class A: the intent is recorded first.
	intent, err := BeginSecurityAudit(cfg.AuditLog, "cli", "webhook.secrets.disable-unreadable", "webhooks", "disable webhooks whose signing secret does not open with the configured key")
	if err != nil {
		return fmt.Errorf("refusing to change webhook secrets without a durable audit record: %w", err)
	}
	disabled, err := store.DisableUnreadableSecrets(cfg.EnvironmentKey)
	if err != nil {
		intent.Failed(err.Error())
		return err
	}
	if err := intent.Completed("disabled: " + strings.Join(disabled, ",")); err != nil {
		return fmt.Errorf("webhooks disabled but the audit outcome was not recorded: %w", err)
	}
	if len(disabled) == 0 {
		fmt.Fprintln(os.Stdout, "every webhook signing secret opens with the configured key; nothing changed")
		return nil
	}
	fmt.Fprintf(os.Stdout, "disabled webhooks whose secret could not be decrypted: %s\nconfigure a new secret for each, and update its sender\n", strings.Join(disabled, ", "))
	return nil
}

// SitesWithUnrevalidatedRepositories lists sites with allowlist entries
// saved lowercased by an earlier release on a host whose repository paths
// may be case-sensitive. Those entries now match only exactly; saving the
// webhook again with the repository's exact URL revalidates them.
func (wcs *WebhookConfigStore) SitesWithUnrevalidatedRepositories() ([]string, error) {
	wcs.mu.RLock()
	defer wcs.mu.RUnlock()
	rows, err := wcs.db.Query(`SELECT DISTINCT c.site, r.repository_url FROM webhook_allowed_repos r JOIN webhook_configs c ON c.id = r.webhook_id WHERE r.case_sensitive = 0 AND c.enabled = 1 ORDER BY c.site`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sites []string
	for rows.Next() {
		var site, repository string
		if err := rows.Scan(&site, &repository); err != nil {
			return nil, err
		}
		if caseInsensitiveRepositoryHosts[gitRepositoryURLHost(repository)] {
			continue
		}
		if len(sites) == 0 || sites[len(sites)-1] != site {
			sites = append(sites, site)
		}
	}
	return sites, rows.Err()
}
