# StePanel secret lifecycle

Use `stepanel dr-check` to generate a secret-safe inventory of control-plane
recovery obligations. It reports presence and permissions without printing
secret values.

StePanel stores several credentials outside the application binary. Treat the
files and keys below as control-plane secrets: keep them root/service-account
readable, include them in encrypted host backups, and never publish them with
site archives or support bundles.

## Environment encryption

Set `STEPANEL_ENVIRONMENT_KEY` to a stable, high-entropy secret. StePanel
derives a 32-byte AES-256 key from that value with SHA-256;
`STEPANEL_ENVIRONMENT_STATE` contains encrypted per-site values. Back up both
the state file and the key together. A missing or changed key makes existing
secret values unrecoverable; it does not silently fall back to plaintext.

To rotate the key, export the current environment metadata and values through a
reviewed operator process, stop managed application updates, write the new key
to protected configuration, and rewrite each environment record. Verify a
decrypt/read test and service restart before retiring the old key. Do not
delete the old backup until recovery has been tested.

## Audit, session, and account state

`STEPANEL_ACCOUNT_KEY` encrypts customer TOTP secrets in account state. Back it
up with `STEPANEL_ACCOUNT_STATE`; losing the key means affected MFA seeds must
be regenerated. `POST /api/accounts/{username}/mfa` regenerates one seed,
revokes that customer's sessions, and returns the replacement once for secure
delivery. `POST /api/accounts/{username}/recovery-codes` generates ten
one-time recovery codes, persists only bcrypt hashes, revokes existing sessions,
and returns the codes once for secure delivery. Generating a new set invalidates
the previous set.
For a complete incident response, `POST /api/accounts/{username}/recover`
rotates the temporary password, TOTP seed, and recovery codes in one persisted
operation, revokes sessions, and requires the customer to complete password and
MFA enrollment. Temporary credentials and replacement seeds are returned once;
deliver them over separate secure channels.

`STEPANEL_AUDIT_KEY` protects the HMAC-linked audit chain. Do not rotate it in
place: existing events would no longer verify. Preserve the old key with the
audit log, and start a separately documented chain only through an intentional
key-rotation procedure.

## Backup manifest signing

Set `STEPANEL_BACKUP_SIGNING_KEY` to a stable high-entropy secret kept outside
`STEPANEL_BACKUP_ROOT`. New backups contain a `manifest.sig` HMAC-SHA256
signature. `verify-backup` and `POST /api/backups/verify` require the key to
verify signed artifacts. Store an escrow copy separately from off-site backup
objects; changing or losing the key makes existing signatures unverifiable.
This authenticates the manifest but does not make storage immutable. Use
provider Object Lock or equivalent immutable retention for compromise recovery.

Production mode requires both `STEPANEL_ENVIRONMENT_KEY` and
`STEPANEL_BACKUP_SIGNING_KEY` and `STEPANEL_BACKUP_ENCRYPTION_KEY`, each at least
32 characters without newlines. The signing key authenticates manifest metadata;
the encryption key protects backup archive contents. Store them separately from
the backup root and escrow both for disaster recovery.
The installer generates and preserves them on upgrades; container and
Kubernetes deployments must supply them through their secret manager.

`STEPANEL_SESSION_SECRET`, session state, and account state must be backed up
together when session recovery is required. Rotating the session secret is a
deliberate logout of existing sessions. If account or session state may have
been exposed, rotate administrator/customer credentials and revoke sessions.

## Git and off-site credentials

Per-site Git deploy keys are generated and retained by the privileged Git
helper; the panel API returns only their public half. Retire and regenerate a
key if its private file may have been copied. Review
`/etc/stepanel/git-known-hosts` when changing a provider host key; do not bypass
strict host verification.

Protect rclone/off-site configuration and provider credentials separately from
the panel state. Test an off-site restore without placing those credentials in
site environment variables or application logs.

## Recovery checklist

1. Keep an encrypted, access-controlled backup of the panel state, encryption
   keys, audit key, and documented file locations.
2. Test restoring environment state and verifying audit history on a disposable
   host after every key or packaging change.
3. If a key is lost, preserve the ciphertext for forensic purposes, record the
   loss in the audit/incident process, and recreate affected secrets rather
   than attempting plaintext recovery.
4. After recovery, rotate any credential that crossed the failed host boundary
   and confirm managed services use the newly rendered environment files.
