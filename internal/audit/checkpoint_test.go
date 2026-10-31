package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateAndVerifyOperationBoundCheckpoint(t *testing.T) {
	logger, _ := newTestLogger(t)
	if err := logger.LogAs(context.Background(), "admin", "site.restore", "site-a", "restored"); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := CreateCheckpoint(logger.path, "checkpoint-job-42", "audit-2026-primary")
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Sequence != 1 || checkpoint.FinalHash == "" || checkpoint.OperationID != "checkpoint-job-42" {
		t.Fatalf("checkpoint does not bind the verified head and operation: %#v", checkpoint)
	}
	if err := VerifyCheckpoint(checkpoint, map[string]string{"audit-2026-primary": strings.Repeat("k", 32)}); err != nil {
		t.Fatalf("verify checkpoint: %v", err)
	}
	if err := VerifyCheckpointWithConfiguredKey(checkpoint); err != nil {
		t.Fatalf("verify checkpoint with configured key: %v", err)
	}

	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Checkpoint
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCheckpoint(decoded, map[string]string{"audit-2026-primary": strings.Repeat("k", 32)}); err != nil {
		t.Fatalf("verify serialized checkpoint: %v", err)
	}
}

func TestVerifyCheckpointWithConfiguredKeyRequiresKey(t *testing.T) {
	previousKeyPath := auditKeyPath
	TestSetKeyPath(filepath.Join(t.TempDir(), "missing-audit.key"))
	t.Setenv("STEPANEL_AUDIT_KEY", "")
	t.Cleanup(func() { TestSetKeyPath(previousKeyPath) })
	if err := VerifyCheckpointWithConfiguredKey(Checkpoint{}); err == nil {
		t.Fatal("verification succeeded without a configured audit key")
	}
}

func TestCheckpointRejectsTamperingAndUnknownHistoricalKey(t *testing.T) {
	logger, _ := newTestLogger(t)
	if err := logger.Log(context.Background(), "site.create", "site-a", "created"); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := CreateCheckpoint(logger.path, "operation-1", "old-key")
	if err != nil {
		t.Fatal(err)
	}
	keyring := map[string]string{"old-key": strings.Repeat("k", 32)}
	if err := VerifyCheckpoint(checkpoint, keyring); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCheckpoint(checkpoint, map[string]string{"new-key": strings.Repeat("n", 32)}); err == nil {
		t.Fatal("checkpoint verified without its historical key ID")
	}
	checkpoint.Signature = strings.Repeat("0", 1<<20)
	if err := VerifyCheckpoint(checkpoint, keyring); err == nil {
		t.Fatal("oversized checkpoint signature verified")
	}
	checkpoint.Signature = ""
	if err := VerifyCheckpoint(checkpoint, keyring); err == nil {
		t.Fatal("empty checkpoint signature verified")
	}
	checkpoint, err = CreateCheckpoint(logger.path, "operation-1", "old-key")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.FinalHash = strings.Repeat("0", 64)
	if err := VerifyCheckpoint(checkpoint, keyring); err == nil {
		t.Fatal("tampered checkpoint verified")
	}
}

func TestCheckpointKeyringSupportsIndependentKeyRotation(t *testing.T) {
	logger, _ := newTestLogger(t)
	auditKey := strings.Repeat("a", 64)
	oldCheckpointKey := strings.Repeat("o", 64)
	newCheckpointKey := strings.Repeat("n", 64)
	t.Setenv("STEPANEL_AUDIT_KEY", auditKey)
	if err := logger.Log(context.Background(), "site.restore", "site-a", "restored"); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := CreateCheckpointWithKey(logger.path, "checkpoint-job-old", "checkpoint-2026-10", oldCheckpointKey)
	if err != nil {
		t.Fatal(err)
	}
	keyringDir := filepath.Join(t.TempDir(), "checkpoint-keys")
	if err := os.Mkdir(keyringDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyringDir, "checkpoint-2026-10.key"), []byte(oldCheckpointKey), 0640); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCheckpointWithKeyring(checkpoint, keyringDir, "checkpoint-2026-11", newCheckpointKey, false); err != nil {
		t.Fatalf("historical checkpoint did not verify after signing-key rotation: %v", err)
	}

	current, err := CreateCheckpointWithKey(logger.path, "checkpoint-job-new", "checkpoint-2026-11", newCheckpointKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyCheckpointWithKeyring(current, keyringDir, "checkpoint-2026-11", newCheckpointKey, false); err != nil {
		t.Fatalf("active checkpoint key did not verify: %v", err)
	}
	if err := os.WriteFile(filepath.Join(keyringDir, "checkpoint-2026-10.key"), []byte(strings.Repeat("x", 64)), 0640); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCheckpointWithKeyring(checkpoint, keyringDir, "checkpoint-2026-11", newCheckpointKey, false); err == nil {
		t.Fatal("altered historical key verified old checkpoint")
	}
}

func TestVersionOneCheckpointRemainsVerifiable(t *testing.T) {
	key := strings.Repeat("v", 64)
	checkpoint := Checkpoint{
		Version:     1,
		Sequence:    1,
		FinalHash:   strings.Repeat("a", 64),
		Timestamp:   "2026-10-31T00:00:00Z",
		KeyID:       "legacy",
		OperationID: "legacy-job",
	}
	checkpoint.Signature = signCheckpoint(checkpoint, checkpointKeyMaterial(1, key))
	if err := VerifyCheckpoint(checkpoint, map[string]string{"legacy": key}); err != nil {
		t.Fatalf("legacy checkpoint signature failed verification: %v", err)
	}
}

func TestCheckpointSigningKeyUsesIndependentKeyAndRejectsReuse(t *testing.T) {
	_, _ = newTestLogger(t)
	t.Setenv("STEPANEL_AUDIT_CHECKPOINT_SIGNING_KEY", strings.Repeat("c", 64))
	key, err := CheckpointSigningKey()
	if err != nil || key != strings.Repeat("c", 64) {
		t.Fatalf("dedicated checkpoint key not selected: key=%q err=%v", key, err)
	}
	t.Setenv("STEPANEL_AUDIT_CHECKPOINT_SIGNING_KEY", "short")
	if _, err := CheckpointSigningKey(); err == nil {
		t.Fatal("short dedicated checkpoint key accepted")
	}
	t.Setenv("STEPANEL_AUDIT_CHECKPOINT_SIGNING_KEY", "bad\nkey"+strings.Repeat("x", 32))
	if _, err := CheckpointSigningKey(); err == nil {
		t.Fatal("newline in dedicated checkpoint key accepted")
	}
	t.Setenv("STEPANEL_AUDIT_CHECKPOINT_SIGNING_KEY", strings.Repeat("k", 32))
	if _, err := CheckpointSigningKey(); err == nil {
		t.Fatal("checkpoint key reused the audit-chain key")
	}
	t.Setenv("STEPANEL_AUDIT_CHECKPOINT_SIGNING_KEY", strings.Repeat("s", 32))
	t.Setenv("STEPANEL_SESSION_SECRET", strings.Repeat("s", 32))
	if _, err := CheckpointSigningKey(); err == nil {
		t.Fatal("checkpoint key reused the session key")
	}
	t.Setenv("STEPANEL_AUDIT_CHECKPOINT_SIGNING_KEY", "")
	key, err = CheckpointSigningKey()
	if err != nil || key != strings.Repeat("k", 32) {
		t.Fatalf("legacy audit-key compatibility fallback failed: key=%q err=%v", key, err)
	}
}

func TestCreateCheckpointRejectsMissingConfiguredSigningKey(t *testing.T) {
	previousKeyPath := auditKeyPath
	TestSetKeyPath(filepath.Join(t.TempDir(), "missing-audit.key"))
	t.Setenv("STEPANEL_AUDIT_KEY", "")
	t.Setenv("STEPANEL_AUDIT_CHECKPOINT_SIGNING_KEY", "")
	t.Cleanup(func() { TestSetKeyPath(previousKeyPath) })
	if _, err := CreateCheckpoint("/tmp/audit.jsonl", "job", "active"); err == nil {
		t.Fatal("checkpoint creation succeeded without an audit/checkpoint key")
	}
}

func TestCreateCheckpointWithKeyRejectsInvalidInputsAndLockFailure(t *testing.T) {
	for _, tc := range []struct {
		name, path, operation, keyID, key string
	}{
		{name: "empty path", path: "", operation: "job", keyID: "active", key: strings.Repeat("k", 32)},
		{name: "bad operation", path: "/tmp/audit", operation: "bad\njob", keyID: "active", key: strings.Repeat("k", 32)},
		{name: "bad key id", path: "/tmp/audit", operation: "job", keyID: "../active", key: strings.Repeat("k", 32)},
		{name: "short key", path: "/tmp/audit", operation: "job", keyID: "active", key: "short"},
		{name: "newline key", path: "/tmp/audit", operation: "job", keyID: "active", key: strings.Repeat("k", 32) + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CreateCheckpointWithKey(tc.path, tc.operation, tc.keyID, tc.key); err == nil {
				t.Fatal("invalid checkpoint input accepted")
			}
		})
	}
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateCheckpointWithKey(filepath.Join(blocked, "audit.jsonl"), "job", "active", strings.Repeat("k", 32)); err == nil {
		t.Fatal("checkpoint succeeded when audit lock could not be created")
	}
}

func TestCheckpointKeyringRejectsUnsafeStorage(t *testing.T) {
	active := strings.Repeat("a", 64)
	if _, err := LoadCheckpointKeyring("", "active", active, false); err != nil {
		t.Fatalf("current-key-only verification should work: %v", err)
	}
	if _, err := LoadCheckpointKeyring("", "../active", active, false); err == nil {
		t.Fatal("invalid active key ID accepted")
	}
	if _, err := LoadCheckpointKeyring("", "active", "short", false); err == nil {
		t.Fatal("short active key accepted")
	}
	if err := VerifyCheckpointWithKeyring(Checkpoint{}, "", "../active", active, false); err == nil {
		t.Fatal("verification ignored invalid active key ID")
	}
	if _, err := LoadCheckpointKeyring(filepath.Join(t.TempDir(), "missing"), "active", active, false); err == nil {
		t.Fatal("configured but missing keyring directory silently fell back to active key")
	}

	root := t.TempDir()
	writable := filepath.Join(root, "writable")
	if err := os.Mkdir(writable, 0770); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpointKeyring(writable, "active", active, false); err == nil {
		t.Fatal("group-writable keyring accepted")
	}

	valid := filepath.Join(root, "valid")
	if err := os.Mkdir(valid, 0750); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if _, err := LoadCheckpointKeyring(valid, "active", active, true); err == nil {
			t.Fatal("non-root-owned production keyring accepted")
		}
	}
	if err := os.WriteFile(filepath.Join(valid, "old.key"), []byte(strings.Repeat("o", 64)), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(valid, "unexpected.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpointKeyring(valid, "active", active, false); err == nil {
		t.Fatal("unexpected keyring entry accepted")
	}
	os.Remove(filepath.Join(valid, "unexpected.txt"))
	if err := os.Remove(filepath.Join(valid, "old.key")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside.key"), filepath.Join(valid, "old.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpointKeyring(valid, "active", active, false); err == nil {
		t.Fatal("symlink key accepted")
	}
	for _, tc := range []struct {
		name, filename, content string
		mode                    os.FileMode
	}{
		{name: "invalid filename ID", filename: "bad id.key", content: strings.Repeat("o", 64), mode: 0600},
		{name: "active ID collision", filename: "active.key", content: strings.Repeat("o", 64), mode: 0600},
		{name: "active ID collision", filename: "active.key", content: strings.Repeat("o", 64), mode: 0600},
		{name: "short historical key", filename: "old.key", content: "short", mode: 0600},
		{name: "oversized historical key", filename: "old.key", content: strings.Repeat("o", 4097), mode: 0600},
		{name: "newline historical key", filename: "old.key", content: strings.Repeat("o", 64) + "\n", mode: 0600},
		{name: "writable historical key", filename: "old.key", content: strings.Repeat("o", 64), mode: 0660},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "keys")
			if err := os.Mkdir(dir, 0750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, tc.filename), []byte(tc.content), tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(dir, tc.filename), tc.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadCheckpointKeyring(dir, "active", active, false); err == nil {
				t.Fatal("invalid historical keyring accepted")
			}
		})
	}
	dirLink := filepath.Join(root, "keyring-link")
	if err := os.Symlink(valid, dirLink); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpointKeyring(dirLink, "active", active, false); err == nil {
		t.Fatal("symlink keyring directory accepted")
	}
	noRead := filepath.Join(root, "no-read")
	if err := os.Mkdir(noRead, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(noRead, 0000); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpointKeyring(noRead, "active", active, false); err == nil {
		t.Fatal("unreadable keyring directory accepted")
	}
	_ = os.Chmod(noRead, 0700)
	if os.Geteuid() != 0 {
		noKeyRead := filepath.Join(root, "no-key-read")
		if err := os.Mkdir(noKeyRead, 0700); err != nil {
			t.Fatal(err)
		}
		keyPath := filepath.Join(noKeyRead, "old.key")
		if err := os.WriteFile(keyPath, []byte(strings.Repeat("o", 64)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(keyPath, 0000); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCheckpointKeyring(noKeyRead, "active", active, false); err == nil {
			t.Fatal("unreadable historical key accepted")
		}
	}
	tooMany := filepath.Join(root, "too-many")
	if err := os.Mkdir(tooMany, 0750); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 129; i++ {
		name := filepath.Join(tooMany, fmt.Sprintf("key-%03d.key", i))
		if err := os.WriteFile(name, []byte(strings.Repeat("k", 32)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadCheckpointKeyring(tooMany, "active", active, false); err == nil {
		t.Fatal("oversized keyring accepted")
	}
}

func TestCheckpointKeyringReturnsDirectoryAndKeyReadFailures(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.key"), []byte(strings.Repeat("o", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	active := strings.Repeat("a", 64)
	readDir := checkpointReadDir
	checkpointReadDir = func(*os.File, int) ([]os.DirEntry, error) {
		return nil, errors.New("injected directory read failure")
	}
	if _, err := LoadCheckpointKeyring(dir, "active", active, false); err == nil {
		t.Fatal("directory read failure was ignored")
	}
	checkpointReadDir = readDir
	t.Cleanup(func() { checkpointReadDir = readDir })

	readKey := checkpointReadKey
	checkpointReadKey = func(*os.File) ([]byte, error) { return nil, errors.New("injected key read failure") }
	if _, err := LoadCheckpointKeyring(dir, "active", active, false); err == nil {
		t.Fatal("key read failure was ignored")
	}
	checkpointReadKey = readKey
}

func TestCreateCheckpointRefusesMissingOrTamperedAuditChain(t *testing.T) {
	logger, _ := newTestLogger(t)
	if _, err := CreateCheckpoint(logger.path, "job-1", "key-1"); err == nil {
		t.Fatal("empty audit chain produced a checkpoint")
	}
	if err := logger.Log(context.Background(), "site.create", "site-a", "created"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logger.path, []byte("tampered\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateCheckpoint(logger.path, "job-1", "key-1"); err == nil {
		t.Fatal("tampered audit chain produced a checkpoint")
	}
}

func TestCheckpointRejectsInvalidFields(t *testing.T) {
	valid := Checkpoint{
		Version:     1,
		Sequence:    1,
		FinalHash:   strings.Repeat("a", 64),
		Timestamp:   "2026-10-31T00:00:00Z",
		KeyID:       "key-1",
		OperationID: "job-1",
		Signature:   strings.Repeat("0", 64),
	}
	for name, mutate := range map[string]func(*Checkpoint){
		"version":           func(c *Checkpoint) { c.Version = 3 },
		"sequence":          func(c *Checkpoint) { c.Sequence = 0 },
		"hash length":       func(c *Checkpoint) { c.FinalHash = "abcd" },
		"hash encoding":     func(c *Checkpoint) { c.FinalHash = strings.Repeat("z", 64) },
		"key ID":            func(c *Checkpoint) { c.KeyID = "../key" },
		"empty operation":   func(c *Checkpoint) { c.OperationID = " \t" },
		"newline operation": func(c *Checkpoint) { c.OperationID = "job-1\\nnext" },
		"timestamp":         func(c *Checkpoint) { c.Timestamp = "not-time" },
		"signature length":  func(c *Checkpoint) { c.Signature = "short" },
		"signature encoding": func(c *Checkpoint) {
			c.Signature = strings.Repeat("z", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			checkpoint := valid
			mutate(&checkpoint)
			if err := VerifyCheckpoint(checkpoint, map[string]string{"key-1": strings.Repeat("k", 32)}); err == nil {
				t.Fatal("invalid checkpoint was accepted")
			}
		})
	}
}

func TestCreateCheckpointRejectsInvalidIdentity(t *testing.T) {
	logger, _ := newTestLogger(t)
	if err := logger.Log(context.Background(), "site.create", "site-a", "created"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, operationID, keyID string
	}{
		{path: logger.path, operationID: "job-1", keyID: "invalid key"},
		{path: logger.path, operationID: "  ", keyID: "key-1"},
		{path: "", operationID: "job-1", keyID: "key-1"},
	} {
		if _, err := CreateCheckpoint(tc.path, tc.operationID, tc.keyID); err == nil {
			t.Fatalf("CreateCheckpoint(%q, %q, %q) accepted invalid identity", tc.path, tc.operationID, tc.keyID)
		}
	}
}
