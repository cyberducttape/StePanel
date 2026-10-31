package stepanel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/audit"
)

const auditCheckpointJobKind = "audit.checkpoint"

type auditCheckpointJobPayload struct {
	Checkpoint *audit.Checkpoint `json:"checkpoint,omitempty"`
	Target     string            `json:"target"`
}

// scheduleAuditCheckpoint submits at most one checkpoint publication per UTC
// day. Admission is durable and idempotent; network I/O is performed later by
// a worker, outside scheduler locks and SQLite transactions.
func (a *App) scheduleAuditCheckpoint(now time.Time) error {
	if a == nil || a.Jobs == nil || a.Jobs.db == nil || a.Config.AuditCheckpointTarget == "" {
		return nil
	}
	if err := validateOffsiteTarget(a.Config.AuditCheckpointTarget); err != nil {
		return err
	}
	payload, err := json.Marshal(auditCheckpointJobPayload{Target: a.Config.AuditCheckpointTarget})
	if err != nil {
		return err
	}
	day := now.UTC().Format("2006-01-02")
	_, _, err = a.Jobs.EnqueueIdempotent(auditCheckpointJobKind, "audit-checkpoints", "daily:"+day, payload, 8)
	return err
}

func (a *App) handleAuditCheckpointJob(ctx context.Context, item Job) ([]byte, error) {
	if a.Jobs == nil || item.ID == "" || item.LeaseToken == "" {
		return nil, errors.New("audit checkpoint requires a fenced durable job")
	}
	var payload auditCheckpointJobPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return nil, fmt.Errorf("decode audit checkpoint job: %w", err)
	}
	if err := validateOffsiteTarget(payload.Target); err != nil {
		return nil, err
	}
	signingKey, err := audit.CheckpointSigningKey()
	if err != nil {
		return nil, err
	}
	if payload.Checkpoint == nil {
		keyID := a.Config.AuditCheckpointKeyID
		if keyID == "" {
			keyID = "primary"
		}
		checkpoint, err := audit.CreateCheckpointWithKey(a.Config.AuditLog, item.ID, keyID, signingKey)
		if err != nil {
			return nil, fmt.Errorf("create signed audit checkpoint: %w", err)
		}
		payload.Checkpoint = &checkpoint
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		// Persist the exact signed bytes before the first remote side effect.
		// A retry after an ambiguous upload therefore uses the same object name
		// and content instead of checkpointing a newer audit-chain head.
		if err := a.Jobs.UpdateClaimPayload(item.ID, item.LeaseToken, encoded); err != nil {
			return nil, fmt.Errorf("persist signed audit checkpoint before publication: %w", err)
		}
		item.Payload = encoded
	}
	checkpoint := *payload.Checkpoint
	if checkpoint.OperationID != item.ID {
		return nil, errors.New("audit checkpoint operation ID does not match its durable job")
	}
	keyID := a.Config.AuditCheckpointKeyID
	if keyID == "" {
		keyID = "primary"
	}
	verify := func(value audit.Checkpoint) error {
		return audit.VerifyCheckpointWithKeyring(value, a.Config.AuditCheckpointKeyringDir, keyID, signingKey, a.Config.Production)
	}
	if err := verify(checkpoint); err != nil {
		return nil, fmt.Errorf("verify signed audit checkpoint before publication: %w", err)
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, err
	}
	remote := auditCheckpointObject(payload.Target, checkpoint)
	if remote == "" {
		return nil, errors.New("invalid audit checkpoint object path")
	}
	if err := publishAuditCheckpointBytes(ctx, remote, encoded); err != nil {
		return nil, err
	}
	remoteBytes, err := readAuditCheckpointRemote(ctx, remote)
	if err != nil {
		return nil, fmt.Errorf("read back published audit checkpoint: %w", err)
	}
	var verified audit.Checkpoint
	if err := json.Unmarshal(remoteBytes, &verified); err != nil {
		return nil, fmt.Errorf("decode remote audit checkpoint: %w", err)
	}
	if err := verify(verified); err != nil {
		return nil, fmt.Errorf("verify remote audit checkpoint: %w", err)
	}
	if verified != checkpoint {
		return nil, errors.New("remote audit checkpoint does not match the durable publication intent")
	}
	detail := fmt.Sprintf("sequence=%d key_id=%s remote=%s", checkpoint.Sequence, checkpoint.KeyID, remote)
	if err := SecurityAuditRequired(a.Config.AuditLog, "scheduler", "audit.checkpoint.published", item.ID, detail); err != nil {
		return nil, fmt.Errorf("record verified audit checkpoint publication: %w", err)
	}
	return json.Marshal(struct {
		Sequence uint64 `json:"sequence"`
		KeyID    string `json:"key_id"`
		Remote   string `json:"remote"`
		Verified bool   `json:"verified"`
	}{Sequence: checkpoint.Sequence, KeyID: checkpoint.KeyID, Remote: remote, Verified: true})
}

func auditCheckpointObject(target string, checkpoint audit.Checkpoint) string {
	if validateOffsiteTarget(target) != nil || checkpoint.Sequence == 0 || checkpoint.OperationID == "" {
		return ""
	}
	if !validJobOperationKey(strings.ReplaceAll(checkpoint.OperationID, "-", ":")) {
		return ""
	}
	return path.Join(strings.TrimRight(target, "/"), "audit-checkpoints", fmt.Sprintf("%020d-%s.json", checkpoint.Sequence, checkpoint.OperationID))
}

// readAuditCheckpointRemote is kept small and bounded because checkpoints are
// compact signed metadata, not backup payloads.
func readAuditCheckpointRemote(ctx context.Context, remote string) ([]byte, error) {
	const maxCheckpointBytes = 64 << 10
	output, err := runOffsiteRcloneLimit(ctx, maxCheckpointBytes+1, "cat", remote)
	if err != nil {
		return nil, err
	}
	if len(output) > maxCheckpointBytes {
		return nil, errors.New("remote audit checkpoint exceeds size limit")
	}
	return output, nil
}

func publishAuditCheckpointBytes(ctx context.Context, remote string, content []byte) error {
	const maxCheckpointBytes = 64 << 10
	if len(content) == 0 || len(content) > maxCheckpointBytes {
		return errors.New("audit checkpoint content is empty or exceeds size limit")
	}
	file, err := os.CreateTemp("", "stepanel-audit-checkpoint-*.json")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	_, uploadErr := runOffsiteRclone(ctx, "copyto", "--immutable", name, remote)
	remoteBytes, readErr := readAuditCheckpointRemote(ctx, remote)
	if readErr != nil {
		if uploadErr != nil {
			return fmt.Errorf("upload audit checkpoint: %v; read-back verification: %w", uploadErr, readErr)
		}
		return fmt.Errorf("read back remote audit checkpoint: %w", readErr)
	}
	if !bytes.Equal(remoteBytes, content) {
		return errors.New("remote audit checkpoint differs from the signed local checkpoint")
	}
	// Exact read-back verification also resolves an ambiguous upload result
	// where the provider accepted the object but the client observed an error.
	return nil
}
