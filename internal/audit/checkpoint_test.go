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
		"version":           func(c *Checkpoint) { c.Version = 2 },
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
