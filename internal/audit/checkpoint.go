package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var checkpointKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var (
	checkpointReadDir = func(file *os.File, count int) ([]os.DirEntry, error) { return file.ReadDir(count) }
	checkpointReadKey = func(file *os.File) ([]byte, error) { return io.ReadAll(io.LimitReader(file, 4097)) }
)

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
	key, err := CheckpointSigningKey()
	if err != nil {
		return Checkpoint{}, err
	}
	return CreateCheckpointWithKey(path, operationID, keyID, key)
}

// CreateCheckpointWithKey creates a checkpoint using a key dedicated to
// checkpoint signing. The audit-chain key remains responsible only for the
// local event chain and its authenticated state.
func CreateCheckpointWithKey(path, operationID, keyID, signingKey string) (Checkpoint, error) {
	path = strings.TrimSpace(path)
	operationID = strings.TrimSpace(operationID)
	keyID = strings.TrimSpace(keyID)
	if path == "" || operationID == "" || len(operationID) > 256 || strings.ContainsAny(operationID, "\r\n\x00") {
		return Checkpoint{}, errors.New("audit checkpoint requires a log path and valid operation ID")
	}
	if !checkpointKeyIDPattern.MatchString(keyID) {
		return Checkpoint{}, errors.New("audit checkpoint key ID is invalid")
	}
	if len(signingKey) < 32 || strings.ContainsAny(signingKey, "\x00\r\n") {
		return Checkpoint{}, errors.New("audit checkpoint signing key must contain at least 32 bytes without NUL or newline")
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
		Version:     2,
		Sequence:    chainState.Sequence,
		FinalHash:   chainState.Hash,
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		KeyID:       keyID,
		OperationID: operationID,
	}
	checkpoint.Signature = signCheckpoint(checkpoint, checkpointKeyMaterial(checkpoint.Version, signingKey))
	return checkpoint, nil
}

// VerifyCheckpoint validates a checkpoint against a key-ID-indexed historical
// keyring. Values are the raw key strings used by STEPANEL_AUDIT_KEY or the
// protected audit key file. The current key alone is insufficient to verify a
// checkpoint after rotation, so callers must retain prior verification keys.
func VerifyCheckpoint(checkpoint Checkpoint, keyring map[string]string) error {
	if (checkpoint.Version != 1 && checkpoint.Version != 2) || checkpoint.Sequence == 0 || len(checkpoint.FinalHash) != sha256.Size*2 {
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
	expected := signCheckpoint(checkpoint, checkpointKeyMaterial(checkpoint.Version, key))
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

func checkpointKeyMaterial(version int, key string) []byte {
	if version == 1 {
		return []byte("stepanel-audit-v1\x00" + key)
	}
	return []byte("stepanel-audit-checkpoint-key-v1\x00" + key)
}

// VerifyCheckpointWithConfiguredKey verifies a checkpoint using the current
// audit signing key. Long-lived deployments that rotate keys should instead
// call VerifyCheckpoint with their protected historical keyring.
func VerifyCheckpointWithConfiguredKey(checkpoint Checkpoint) error {
	key, err := auditSigningKey()
	if err != nil {
		return err
	}
	const keyPrefix = "stepanel-audit-v1\x00"
	return VerifyCheckpoint(checkpoint, map[string]string{checkpoint.KeyID: strings.TrimPrefix(string(key), keyPrefix)})
}

// LoadCheckpointKeyring loads the active checkpoint key and any retained
// checkpoint-verification keys from dir. Historical key files are named
// <key-id>.key and contain the raw key bytes. The optional directory must be
// administrator-managed: it may not be a symlink or writable by group/other.
// An unset directory means only the current key is available; a configured
// but missing directory is an error so a mistaken path cannot hide key loss.
func LoadCheckpointKeyring(dir, activeKeyID, activeKey string, requireRootOwned bool) (map[string]string, error) {
	if !checkpointKeyIDPattern.MatchString(activeKeyID) {
		return nil, errors.New("audit checkpoint active key ID is invalid")
	}
	if len(activeKey) < 32 || strings.ContainsAny(activeKey, "\x00\r\n") {
		return nil, errors.New("audit checkpoint active key must contain at least 32 bytes without NUL or newline")
	}
	keyring := map[string]string{activeKeyID: activeKey}
	var err error
	if dir == "" {
		return keyring, nil
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("inspect audit checkpoint keyring: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("audit checkpoint keyring must be a real directory not writable by group or other")
	}
	if requireRootOwned && !isRootOwned(info) {
		return nil, errors.New("production audit checkpoint keyring must be root-owned")
	}
	dirFD, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, fmt.Errorf("open audit checkpoint keyring: %w", err)
	}
	keyDir := os.NewFile(uintptr(dirFD), dir)
	openedDirInfo, err := keyDir.Stat()
	if err != nil || !os.SameFile(info, openedDirInfo) {
		keyDir.Close()
		if err != nil {
			return nil, fmt.Errorf("stat opened audit checkpoint keyring: %w", err)
		}
		return nil, errors.New("audit checkpoint keyring changed while opening")
	}
	entries, readErr := checkpointReadDir(keyDir, 129)
	_ = keyDir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, fmt.Errorf("read audit checkpoint keyring: %w", readErr)
	}
	if len(entries) > 128 {
		return nil, errors.New("audit checkpoint keyring contains too many keys")
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".key") {
			return nil, fmt.Errorf("unexpected file in audit checkpoint keyring: %s", name)
		}
		keyID := strings.TrimSuffix(name, ".key")
		if !checkpointKeyIDPattern.MatchString(keyID) {
			return nil, fmt.Errorf("invalid audit checkpoint key filename: %s", name)
		}
		if _, exists := keyring[keyID]; exists {
			return nil, fmt.Errorf("audit checkpoint key ID %q conflicts with the active key", keyID)
		}
		keyPath := filepath.Join(dir, name)
		keyInfo, err := os.Lstat(keyPath)
		if err != nil {
			return nil, fmt.Errorf("inspect audit checkpoint key %q: %w", keyID, err)
		}
		if !keyInfo.Mode().IsRegular() || keyInfo.Mode().Perm()&0027 != 0 {
			return nil, fmt.Errorf("audit checkpoint key %q must be a regular file without group-write or other permissions", keyID)
		}
		if requireRootOwned && !isRootOwned(keyInfo) {
			return nil, fmt.Errorf("production audit checkpoint key %q must be root-owned", keyID)
		}
		fd, err := syscall.Open(keyPath, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return nil, fmt.Errorf("open audit checkpoint key %q: %w", keyID, err)
		}
		file := os.NewFile(uintptr(fd), keyPath)
		openedInfo, err := file.Stat()
		if err != nil || !os.SameFile(keyInfo, openedInfo) {
			file.Close()
			if err != nil {
				return nil, fmt.Errorf("stat opened audit checkpoint key %q: %w", keyID, err)
			}
			return nil, fmt.Errorf("audit checkpoint key %q changed while opening", keyID)
		}
		value, readErr := checkpointReadKey(file)
		_ = file.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read audit checkpoint key %q: %w", keyID, readErr)
		}
		if len(value) < 32 || len(value) > 4096 || strings.ContainsAny(string(value), "\x00\r\n") {
			return nil, fmt.Errorf("audit checkpoint key %q must contain 32-4096 bytes without NUL or newline", keyID)
		}
		keyring[keyID] = string(value)
	}
	return keyring, nil
}

// VerifyCheckpointWithKeyring verifies a checkpoint against the active key
// and protected historical keys in dir.
func VerifyCheckpointWithKeyring(checkpoint Checkpoint, dir, activeKeyID, activeKey string, requireRootOwned bool) error {
	keyring, err := LoadCheckpointKeyring(dir, activeKeyID, activeKey, requireRootOwned)
	if err != nil {
		return err
	}
	return VerifyCheckpoint(checkpoint, keyring)
}

func isRootOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}

// CheckpointSigningKey returns the dedicated checkpoint key when configured.
// The audit-chain key is retained as a compatibility fallback for existing
// installations; deployments should configure the independent key explicitly.
func CheckpointSigningKey() (string, error) {
	if key := os.Getenv("STEPANEL_AUDIT_CHECKPOINT_SIGNING_KEY"); key != "" {
		if len(key) < 32 || strings.ContainsAny(key, "\x00\r\n") {
			return "", errors.New("STEPANEL_AUDIT_CHECKPOINT_SIGNING_KEY must contain at least 32 bytes without NUL or newline")
		}
		chainKey, err := auditSigningKey()
		if err != nil {
			return "", err
		}
		if key == strings.TrimPrefix(string(chainKey), "stepanel-audit-v1\x00") || key == os.Getenv("STEPANEL_SESSION_SECRET") {
			return "", errors.New("audit checkpoint signing key must be independent of audit-chain and session keys")
		}
		return key, nil
	}
	key, err := auditSigningKey()
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(string(key), "stepanel-audit-v1\x00"), nil
}

func signCheckpoint(checkpoint Checkpoint, key []byte) string {
	// The payload contains only strings, integers, and booleans; encoding/json
	// cannot fail for these field types.
	payload, _ := json.Marshal(checkpointSigningPayload{
		Version:     checkpoint.Version,
		Sequence:    checkpoint.Sequence,
		FinalHash:   checkpoint.FinalHash,
		Timestamp:   checkpoint.Timestamp,
		KeyID:       checkpoint.KeyID,
		OperationID: checkpoint.OperationID,
	})
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("stepanel-audit-checkpoint-v1\x00"))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
