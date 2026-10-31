package audit

import (
	"context"
	"encoding/json"
	"os"
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
	checkpoint.FinalHash = strings.Repeat("0", 64)
	if err := VerifyCheckpoint(checkpoint, keyring); err == nil {
		t.Fatal("tampered checkpoint verified")
	}
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
