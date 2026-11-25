package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	rotationOldAuditKey = strings.Repeat("o", 40)
	rotationNewAuditKey = strings.Repeat("n", 40)
)

func newRotationLogger(t *testing.T) *defaultLogger {
	t.Helper()
	TestSetKeyPath(filepath.Join(t.TempDir(), "absent.key"))
	t.Setenv("STEPANEL_AUDIT_CHECKPOINT_SIGNING_KEY", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "")
	t.Setenv("STEPANEL_AUDIT_PREVIOUS_KEYS", "")
	t.Setenv("STEPANEL_AUDIT_KEY", rotationOldAuditKey)
	TestResetPersistenceError()
	return New(filepath.Join(t.TempDir(), "audit.jsonl")).(*defaultLogger)
}

// After a rotation the chain keeps verifying across the boundary, the first
// append re-signs the state with the new key, and history stays verifiable
// only while the previous key is listed.
func TestAuditChainKeyRotation(t *testing.T) {
	logger := newRotationLogger(t)
	for _, action := range []string{"before.one", "before.two"} {
		if err := logger.LogAs(context.Background(), "admin", action, "site-a", "pre-rotation"); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("STEPANEL_AUDIT_KEY", rotationNewAuditKey)
	if err := logger.Verify(logger.path); err == nil {
		t.Fatal("a chain signed with a retired key verified without the key listed")
	}
	t.Setenv("STEPANEL_AUDIT_PREVIOUS_KEYS", rotationOldAuditKey)
	if err := logger.Verify(logger.path); err != nil {
		t.Fatalf("verify with the previous key listed: %v", err)
	}
	if rotating, err := ChainSignedWithPreviousKey(logger.path); err != nil || !rotating {
		t.Fatalf("ChainSignedWithPreviousKey before the first new append = %v, %v; want true", rotating, err)
	}
	if err := logger.LogAs(context.Background(), "system", "after.rotation", "audit-log", "first event under the new key"); err != nil {
		t.Fatalf("append after rotation: %v", err)
	}
	if rotating, err := ChainSignedWithPreviousKey(logger.path); err != nil || rotating {
		t.Fatalf("ChainSignedWithPreviousKey after the first new append = %v, %v; want false", rotating, err)
	}
	if err := logger.Verify(logger.path); err != nil {
		t.Fatalf("verify across the rotation boundary: %v", err)
	}
	events, err := logger.readVerifiedEvents("", "", 10)
	if err != nil || len(events) != 3 {
		t.Fatalf("readVerifiedEvents across the rotation = %d events, %v", len(events), err)
	}

	// The history itself is never re-signed: dropping the previous key while
	// the pre-rotation events are retained fails verification closed.
	t.Setenv("STEPANEL_AUDIT_PREVIOUS_KEYS", "")
	if err := logger.Verify(logger.path); err == nil {
		t.Fatal("pre-rotation events verified after the previous key was removed")
	}
}

// A retired key cannot extend the chain after events signed with the new key.
func TestAuditChainRefusesRetiredKeyAfterRotation(t *testing.T) {
	logger := newRotationLogger(t)
	if err := logger.LogAs(context.Background(), "admin", "before", "site-a", ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEPANEL_AUDIT_KEY", rotationNewAuditKey)
	t.Setenv("STEPANEL_AUDIT_PREVIOUS_KEYS", rotationOldAuditKey)
	if err := logger.LogAs(context.Background(), "admin", "after", "site-a", ""); err != nil {
		t.Fatal(err)
	}

	// Forge the next event with the retired key and append it.
	last := lastEvent(t, logger.path)
	forged := Event{Time: last.Time, Sequence: last.Sequence + 1, Actor: "attacker", Action: "forged", Target: "site-a", PreviousHash: last.Hash}
	hash, err := hashEventWithKey(forged, []byte(auditKeyPrefix+rotationOldAuditKey))
	if err != nil {
		t.Fatal(err)
	}
	forged.Hash = hash
	line, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(logger.path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := logger.Verify(logger.path); err == nil || !strings.Contains(err.Error(), "invalid signature") {
		t.Fatalf("a retired-key event after the rotation verified: %v", err)
	}
}

func TestAuditPreviousKeysAreValidated(t *testing.T) {
	newRotationLogger(t)
	for _, value := range []string{"short", rotationOldAuditKey, rotationNewAuditKey + "," + rotationNewAuditKey} {
		t.Setenv("STEPANEL_AUDIT_KEY", rotationOldAuditKey)
		t.Setenv("STEPANEL_AUDIT_PREVIOUS_KEYS", value)
		if _, err := chainKeys(); err == nil {
			t.Errorf("STEPANEL_AUDIT_PREVIOUS_KEYS=%q was accepted", value)
		}
	}
}

func lastEvent(t *testing.T, path string) Event {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var last Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if err := json.Unmarshal(scanner.Bytes(), &last); err != nil {
			t.Fatal(err)
		}
	}
	return last
}

// An invalid previous-key list fails every verification path closed rather
// than silently verifying with the current key alone.
func TestAuditInvalidPreviousKeysFailClosed(t *testing.T) {
	logger := newRotationLogger(t)
	if err := logger.LogAs(context.Background(), "admin", "before", "site-a", ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEPANEL_AUDIT_PREVIOUS_KEYS", "too-short")
	if err := logger.Verify(logger.path); err == nil {
		t.Error("Verify accepted an invalid previous-key list")
	}
	if _, err := logger.readVerifiedEvents("", "", 10); err == nil {
		t.Error("readVerifiedEvents accepted an invalid previous-key list")
	}
	if _, err := ChainSignedWithPreviousKey(logger.path); err == nil {
		t.Error("ChainSignedWithPreviousKey accepted an invalid previous-key list")
	}
	if _, err := logger.reconcileTail(state{Version: 1}); err == nil {
		t.Error("reconcileTail accepted an invalid previous-key list")
	}
	if err := logger.LogAs(context.Background(), "admin", "after", "site-a", ""); err == nil {
		t.Error("append accepted an invalid previous-key list")
	}
}

func TestChainSignedWithPreviousKeyWithoutState(t *testing.T) {
	logger := newRotationLogger(t)
	if rotating, err := ChainSignedWithPreviousKey(logger.path); err != nil || rotating {
		t.Fatalf("a log without chain state = %v, %v; want false, nil", rotating, err)
	}
	if err := os.WriteFile(logger.path+".state", []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ChainSignedWithPreviousKey(logger.path); err == nil {
		t.Fatal("a corrupt chain state was accepted")
	}
}
