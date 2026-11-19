package stepanel

import "testing"

func TestBackupDecryptionKeysConfig(t *testing.T) {
	const current = "backup-encryption-key-current-0123456789abcdef"
	const retired = "backup-encryption-key-retired-0123456789abcdef"
	t.Setenv("STEPANEL_BACKUP_ENCRYPTION_KEY", current)
	t.Setenv("STEPANEL_BACKUP_ENCRYPTION_PREVIOUS_KEYS", " "+retired+" , ,")
	cfg := LoadConfig()
	if got := cfg.backupDecryptionKeys(); len(got) != 2 || got[0] != current || got[1] != retired {
		t.Fatalf("decryption keys = %q", got)
	}
}
