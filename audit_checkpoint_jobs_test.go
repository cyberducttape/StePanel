package stepanel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/audit"
)

func TestAuditCheckpointJobPublishesAndVerifiesDurableCheckpoint(t *testing.T) {
	root := t.TempDir()
	key := "test-audit-checkpoint-key-0123456789abcdef"
	t.Setenv("STEPANEL_AUDIT_KEY", key)
	logPath := filepath.Join(root, "audit.jsonl")
	if err := SecurityAuditRequired(logPath, "test", "test.started", "control-plane", "checkpoint test"); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	remoteRoot := filepath.Join(root, "remote")
	rclone := `#!/bin/sh
set -eu
command=$1
shift
case "$command" in
  copyto)
    [ "$1" = "--immutable" ] && shift
    source=$1
    destination=${2#file:}
    mkdir -p "$(dirname "$destination")"
    if [ -e "$destination" ]; then exit 3; fi
    cp "$source" "$destination"
		# Simulate a provider accepting the object before the client observes a
		# transport error. Read-back reconciliation should resolve the outcome.
		exit 8
    ;;
  cat)
    source=${1#file:}
    cat "$source"
    ;;
  *) exit 4 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "rclone"), []byte(rclone), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	db, err := openControlPlaneDB(filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	jobs := newJobsWithDB(db, 1)
	target := "file:" + remoteRoot
	payload, _ := json.Marshal(auditCheckpointJobPayload{Target: target})
	job, err := jobs.Enqueue(auditCheckpointJobKind, "audit-checkpoints", "test:checkpoint", payload, 3)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := jobs.ClaimNext("test-worker", auditCheckpointJobKind)
	if err != nil || !ok {
		t.Fatalf("claim checkpoint job: ok=%t err=%v", ok, err)
	}
	app := &App{Config: Config{AuditLog: logPath, AuditCheckpointKeyID: "test-key"}, Jobs: jobs}
	result, err := app.handleAuditCheckpointJob(context.Background(), claimed)
	if err != nil {
		t.Fatalf("publish checkpoint: %v", err)
	}
	var publication struct {
		Sequence uint64 `json:"sequence"`
		KeyID    string `json:"key_id"`
		Remote   string `json:"remote"`
		Verified bool   `json:"verified"`
	}
	if err := json.Unmarshal(result, &publication); err != nil {
		t.Fatal(err)
	}
	if !publication.Verified || publication.Sequence == 0 || publication.KeyID != "test-key" || !strings.HasPrefix(publication.Remote, target+"/") {
		t.Fatalf("unexpected publication result: %+v", publication)
	}
	remoteBytes, err := os.ReadFile(strings.TrimPrefix(publication.Remote, "file:"))
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint audit.Checkpoint
	if err := json.Unmarshal(remoteBytes, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.OperationID != job.ID || checkpoint.Sequence != publication.Sequence {
		t.Fatalf("remote checkpoint does not bind to durable job: %+v", checkpoint)
	}
	if err := audit.VerifyCheckpoint(checkpoint, map[string]string{"test-key": key}); err != nil {
		t.Fatalf("remote checkpoint signature invalid: %v", err)
	}
	stored, ok := jobs.Get(job.ID)
	if !ok {
		t.Fatal("durable job disappeared")
	}
	var storedPayload auditCheckpointJobPayload
	if err := json.Unmarshal(stored.Payload, &storedPayload); err != nil || storedPayload.Checkpoint == nil {
		t.Fatalf("signed checkpoint was not durably saved before completion: payload=%s err=%v", stored.Payload, err)
	}
}

func TestAuditCheckpointSchedulingIsDailyAndOptIn(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)
	app := &App{Jobs: jobs}
	now := time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC)
	if err := app.scheduleAuditCheckpoint(now); err != nil {
		t.Fatal(err)
	}
	if len(jobs.List(10)) != 0 {
		t.Fatal("checkpoint scheduled without a configured destination")
	}
	app.Config.AuditCheckpointTarget = "s3:separate-audit-evidence/checkpoints"
	if err := app.scheduleAuditCheckpoint(now); err != nil {
		t.Fatal(err)
	}
	if err := app.scheduleAuditCheckpoint(now); err != nil {
		t.Fatal(err)
	}
	items := jobs.List(10)
	if len(items) != 1 || items[0].Kind != auditCheckpointJobKind {
		t.Fatalf("same-day scheduling was not idempotent: %+v", items)
	}
	if err := app.scheduleAuditCheckpoint(now.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(jobs.List(10)) != 2 {
		t.Fatal("next UTC day did not schedule a new checkpoint")
	}
}
