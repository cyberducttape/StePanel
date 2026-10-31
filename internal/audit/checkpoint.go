package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var checkpointKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Checkpoint authenticates one verified audit-chain head and binds it to the
// durable operation that requested publication. A verifier can validate the
// signature using a protected keyring without access to the source host.
type Checkpoint struct {
	Version     int    `json:"version"`
	Sequence    uint64 `json:"sequence"`
	FinalHash   string `json:"final_hash"`
	Timestamp   string `json:"timestamp"`
	KeyID       string `json:"key_id"`
	OperationID string `json:"operation_id"`
	Signature   string `json:"signature"`
}

type checkpointSigningPayload struct {
	Version     int    `json:"version"`
	Sequence    uint64 `json:"sequence"`
	FinalHash   string `json:"final_hash"`
	Timestamp   string `json:"timestamp"`
	KeyID       string `json:"key_id"`
	OperationID string `json:"operation_id"`
}

// CreateCheckpoint locks the local writer, verifies the full event chain and
// its authenticated state, and signs the exact current tail. The operation ID
// is normally the durable job ID; retries should reuse it and persist the
// resulting JSON before attempting remote publication.
func CreateCheckpoint(path, operationID, keyID string) (Checkpoint, error) {
	path = strings.TrimSpace(path)
	operationID = strings.TrimSpace(operationID)
	keyID = strings.TrimSpace(keyID)
	if path == "" || operationID == "" || len(operationID) > 256 || strings.ContainsAny(operationID, "\r\n\x00") {
		return Checkpoint{}, errors.New("audit checkpoint requires a log path and valid operation ID")
	}
	if !checkpointKeyIDPattern.MatchString(keyID) {
		return Checkpoint{}, errors.New("audit checkpoint key ID is invalid")
	}

	mu.Lock()
	defer mu.Unlock()
	logger := &defaultLogger{path: path}
	_, unlock, err := logger.acquireLock()
	if err != nil {
		return Checkpoint{}, fmt.Errorf("lock audit chain for checkpoint: %w", err)
	}
	defer unlock()
	if err := logger.Verify(path); err != nil {
		return Checkpoint{}, fmt.Errorf("verify audit chain before checkpoint: %w", err)
	}
	chainState, err := logger.loadState(path + ".state")
	if err != nil {
		return Checkpoint{}, fmt.Errorf("read verified audit state for checkpoint: %w", err)
	}
	if chainState.Sequence == 0 || chainState.Hash == "" {
		return Checkpoint{}, errors.New("cannot checkpoint an empty audit chain")
	}

	checkpoint := Checkpoint{
		Version:     1,
		Sequence:    chainState.Sequence,
		FinalHash:   chainState.Hash,
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		KeyID:       keyID,
		OperationID: operationID,
	}
	key, err := auditSigningKey()
	if err != nil {
		return Checkpoint{}, err
	}
	checkpoint.Signature, err = signCheckpoint(checkpoint, key)
	if err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

// VerifyCheckpoint validates a checkpoint against a key-ID-indexed historical
// keyring. Values are the raw key strings used by STEPANEL_AUDIT_KEY or the
// protected audit key file. The current key alone is insufficient to verify a
// checkpoint after rotation, so callers must retain prior verification keys.
func VerifyCheckpoint(checkpoint Checkpoint, keyring map[string]string) error {
	if checkpoint.Version != 1 || checkpoint.Sequence == 0 || len(checkpoint.FinalHash) != sha256.Size*2 {
		return errors.New("audit checkpoint fields are invalid")
	}
	if _, err := hex.DecodeString(checkpoint.FinalHash); err != nil {
		return errors.New("audit checkpoint final hash is invalid")
	}
	if !checkpointKeyIDPattern.MatchString(checkpoint.KeyID) || strings.TrimSpace(checkpoint.OperationID) == "" || len(checkpoint.OperationID) > 256 || strings.ContainsAny(checkpoint.OperationID, "\r\n\x00") {
		return errors.New("audit checkpoint key ID or operation ID is invalid")
	}
	if len(checkpoint.Signature) != sha256.Size*2 {
		return errors.New("audit checkpoint signature has an invalid length")
	}
	if _, err := time.Parse(time.RFC3339Nano, checkpoint.Timestamp); err != nil {
		return errors.New("audit checkpoint timestamp is invalid")
	}
	key, ok := keyring[checkpoint.KeyID]
	if !ok || len(key) < 32 {
		return errors.New("audit checkpoint verification key is unavailable")
	}
	expected, err := signCheckpoint(checkpoint, []byte("stepanel-audit-v1\x00"+key))
	if err != nil {
		return err
	}
	actualBytes, err := hex.DecodeString(checkpoint.Signature)
	if err != nil {
		return errors.New("audit checkpoint signature is invalid")
	}
	expectedBytes, _ := hex.DecodeString(expected)
	if !hmac.Equal(actualBytes, expectedBytes) {
		return errors.New("audit checkpoint signature verification failed")
	}
	return nil
}

func signCheckpoint(checkpoint Checkpoint, key []byte) (string, error) {
	payload, err := json.Marshal(checkpointSigningPayload{
		Version:     checkpoint.Version,
		Sequence:    checkpoint.Sequence,
		FinalHash:   checkpoint.FinalHash,
		Timestamp:   checkpoint.Timestamp,
		KeyID:       checkpoint.KeyID,
		OperationID: checkpoint.OperationID,
	})
	if err != nil {
		return "", fmt.Errorf("encode audit checkpoint: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("stepanel-audit-checkpoint-v1\x00"))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil)), nil
}
