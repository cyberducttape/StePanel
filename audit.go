package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var auditMu sync.Mutex  // Process-local mutex for goroutine safety
var auditDistMu map[string]string = make(map[string]string)  // Track distributed lock files
var auditDistMuLock sync.Mutex  // Protect auditDistMu map
var auditPersistenceErr error
var auditKeyPath = "/etc/stepanel-audit.key"

const maxAuditLogBytes int64 = 256 << 20

type AuditEvent struct {
	Time         string `json:"time"`
	Sequence     uint64 `json:"sequence"`
	Actor        string `json:"actor"`
	Action       string `json:"action"`
	Target       string `json:"target"`
	Detail       string `json:"detail"`
	PreviousHash string `json:"previous_hash"`
	Hash         string `json:"hash"`
}

type auditState struct {
	Version           int    `json:"version"`
	Sequence          uint64 `json:"sequence"`
	Hash              string `json:"hash"`
	FirstSequence     uint64 `json:"first_sequence"`
	FirstPreviousHash string `json:"first_previous_hash"`
	KeyCheck          string `json:"key_check"`
	Signature         string `json:"signature"`
}

func Audit(path, action, target, detail string) error {
	return AuditAs(path, "system", action, target, detail)
}

func AuditAs(path, actor, action, target, detail string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	auditMu.Lock()
	defer auditMu.Unlock()
	err := appendAuditEvent(path, actor, action, target, detail)
	// Keep failures sticky for the lifetime of the process. A later successful
	// append cannot restore an event that was lost before a privileged action.
	if err != nil {
		auditPersistenceErr = err
	}
	return err
}

func AuditPersistenceError() error {
	auditMu.Lock()
	defer auditMu.Unlock()
	return auditPersistenceErr
}

// acquireAuditLock creates a distributed lock for the audit file.
// This ensures that the read-reconcile-append-state transaction is atomic
// even across multiple processes (panel and worker).
func acquireAuditLock(path string) (lockFile string, unlock func() error, err error) {
	lockFile = path + ".lock"

	// Try to create lock file exclusively (atomic across processes)
	for attempts := 0; attempts < 300; attempts++ {  // 30 second timeout
		file, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			fmt.Fprintf(file, "%d", os.Getpid())
			file.Close()
			return lockFile, func() error { return os.Remove(lockFile) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, fmt.Errorf("acquire audit lock: %w", err)
		}
		// Lock exists, wait and retry
		time.Sleep(100 * time.Millisecond)
	}
	return "", nil, errors.New("timeout acquiring audit lock")
}

func appendAuditEvent(path, actor, action, target, detail string) error {
	if actor == "" || action == "" {
		return errors.New("audit actor and action are required")
	}

	// Acquire distributed lock to serialize audit across processes
	_, unlock, err := acquireAuditLock(path)
	if err != nil {
		return fmt.Errorf("audit lock: %w", err)
	}
	defer unlock()

	actor = truncateAuditValue(actor, 128)
	action = truncateAuditValue(action, 128)
	target = truncateAuditValue(target, 512)
	detail = truncateAuditValue(detail, 4096)
	root := filepath.Dir(path)
	if err := os.MkdirAll(root, 0750); err != nil {
		return err
	}
	statePath := path + ".state"
	if info, statErr := os.Stat(path); statErr == nil && info.Size() >= maxAuditLogBytes {
		return errors.New("audit log exceeds the 256 MiB retention limit; rotate it after verification")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	state, err := loadAuditState(statePath)
	if err != nil {
		return err
	}
	if state.Version == 0 {
		keyCheck, err := auditKeyCheck()
		if err != nil {
			return err
		}
		if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 {
			legacy := path + ".legacy-" + time.Now().UTC().Format("20060102-150405")
			if err := os.Rename(path, legacy); err != nil {
				return fmt.Errorf("preserve legacy audit log: %w", err)
			}
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		state = auditState{Version: 1, KeyCheck: keyCheck}
	}
	state, err = reconcileAuditTail(path, state)
	if err != nil {
		return err
	}
	event := AuditEvent{Time: time.Now().UTC().Format(time.RFC3339Nano), Sequence: state.Sequence + 1, Actor: actor, Action: action, Target: target, Detail: detail, PreviousHash: state.Hash}
	event.Hash, err = hashAuditEvent(event)
	if err != nil {
		return err
	}
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(append(line, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return writeAuditState(statePath, auditState{Version: 1, Sequence: event.Sequence, Hash: event.Hash, FirstSequence: state.FirstSequence, FirstPreviousHash: state.FirstPreviousHash, KeyCheck: state.KeyCheck})
}

func truncateAuditValue(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func auditSigningKey() ([]byte, error) {
	key := os.Getenv("STEPANEL_AUDIT_KEY")
	if key == "" {
		if data, err := os.ReadFile(auditKeyPath); err == nil {
			key = strings.TrimSpace(string(data))
		}
	}
	// The session secret is retained only as a development fallback. Prefer the
	// dedicated key file before considering it so production installs that keep
	// the audit key out of the environment remain correctly verifiable.
	if key == "" {
		key = os.Getenv("STEPANEL_SESSION_SECRET")
	}
	if len(key) < 32 {
		return nil, errors.New("STEPANEL_AUDIT_KEY or STEPANEL_SESSION_SECRET must contain at least 32 characters")
	}
	return []byte("stepanel-audit-v1\x00" + key), nil
}

func hashAuditEvent(event AuditEvent) (string, error) {
	event.Hash = ""
	data, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	key, err := auditSigningKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func auditKeyCheck() (string, error) {
	key, err := auditSigningKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("stepanel-audit-state-key-check-v1"))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func signAuditState(state auditState) (string, error) {
	state.Signature = ""
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	key, err := auditSigningKey()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("stepanel-audit-state-v1\x00"))
	_, _ = mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func loadAuditState(path string) (auditState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return auditState{}, nil
	}
	if err != nil {
		return auditState{}, err
	}
	keyCheck, err := auditKeyCheck()
	if err != nil {
		return auditState{}, err
	}
	var state auditState
	if err := json.Unmarshal(data, &state); err != nil || state.Version != 1 || state.Sequence == 0 && state.Hash != "" || state.Sequence > 0 && len(state.Hash) != sha256.Size*2 || state.FirstSequence > state.Sequence+1 || !hmac.Equal([]byte(state.KeyCheck), []byte(keyCheck)) {
		return auditState{}, errors.New("invalid audit chain state")
	}
	expected, err := signAuditState(state)
	if err != nil || !hmac.Equal([]byte(state.Signature), []byte(expected)) {
		return auditState{}, errors.New("audit chain state has an invalid signature")
	}
	return state, nil
}

func reconcileAuditTail(path string, state auditState) (auditState, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		state.FirstSequence = state.Sequence + 1
		state.FirstPreviousHash = state.Hash
		return state, nil
	}
	if err != nil {
		return state, err
	}
	defer file.Close()
	var first, last AuditEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		if err := json.Unmarshal(scanner.Bytes(), &last); err != nil {
			return state, errors.New("audit log contains an invalid event")
		}
		if err := validateAuditEvent(last); err != nil {
			return state, err
		}
		if first.Sequence == 0 {
			first = last
		}
	}
	if err := scanner.Err(); err != nil {
		return state, err
	}
	if last.Sequence == 0 {
		state.FirstSequence = state.Sequence + 1
		state.FirstPreviousHash = state.Hash
		return state, nil
	}
	if state.FirstSequence == 0 {
		state.FirstSequence = first.Sequence
		state.FirstPreviousHash = first.PreviousHash
	}
	if first.Sequence != state.FirstSequence || first.PreviousHash != state.FirstPreviousHash {
		return state, errors.New("audit log prefix does not match chain state")
	}
	if last.Sequence == state.Sequence {
		if last.Sequence > 0 && last.Hash != state.Hash {
			return state, errors.New("audit log tail does not match chain state")
		}
		if last.Sequence > 0 {
			expected, err := hashAuditEvent(last)
			if err != nil || !hmac.Equal([]byte(expected), []byte(last.Hash)) {
				return state, errors.New("audit log tail has an invalid signature")
			}
		}
		return state, nil
	}
	if last.Sequence == state.Sequence+1 && last.PreviousHash == state.Hash {
		expected, err := hashAuditEvent(last)
		if err != nil || !hmac.Equal([]byte(expected), []byte(last.Hash)) {
			return state, errors.New("uncommitted audit tail has an invalid signature")
		}
		return auditState{Version: 1, Sequence: last.Sequence, Hash: last.Hash, FirstSequence: state.FirstSequence, FirstPreviousHash: state.FirstPreviousHash, KeyCheck: state.KeyCheck}, nil
	}
	return state, errors.New("audit log and chain state are inconsistent")
}

func validateAuditEvent(event AuditEvent) error {
	if event.Sequence == 0 || strings.TrimSpace(event.Actor) == "" || strings.TrimSpace(event.Action) == "" {
		return errors.New("audit log contains an event with invalid identity or sequence")
	}
	if _, err := time.Parse(time.RFC3339Nano, event.Time); err != nil {
		return errors.New("audit log contains an invalid event timestamp")
	}
	return nil
}

func writeAuditState(path string, state auditState) error {
	var err error
	state.Signature, err = signAuditState(state)
	if err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	root := filepath.Dir(path)
	temp, err := os.CreateTemp(root, ".audit-state-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	// Set restrictive permissions immediately to protect audit state
	if err = temp.Chmod(0600); err != nil {
		temp.Close()
		return fmt.Errorf("secure audit state file permissions: %w", err)
	}
	// Write audit data with proper permissions guaranteed
	if _, err = temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return fmt.Errorf("write audit state: %w", err)
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync audit state: %w", err)
	}
	if err = temp.Close(); err != nil {
		return fmt.Errorf("close audit state: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	err = directory.Sync()
	if closeErr := directory.Close(); err == nil {
		err = closeErr
	}
	return err
}

func VerifyAuditLog(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	var first, previous *AuditEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var event AuditEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return err
		}
		if err := validateAuditEvent(event); err != nil {
			return err
		}
		expected, err := hashAuditEvent(event)
		if err != nil || !hmac.Equal([]byte(expected), []byte(event.Hash)) {
			return fmt.Errorf("audit event %d has an invalid signature", event.Sequence)
		}
		if previous != nil && (event.Sequence != previous.Sequence+1 || event.PreviousHash != previous.Hash) {
			return fmt.Errorf("audit chain breaks at event %d", event.Sequence)
		}
		eventCopy := event
		if first == nil {
			first = &eventCopy
		}
		previous = &eventCopy
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if previous == nil {
		return errors.New("audit log contains no signed events")
	}
	state, err := loadAuditState(path + ".state")
	if err != nil {
		return err
	}
	if state.Sequence != previous.Sequence || state.Hash != previous.Hash {
		return errors.New("audit chain state does not match the log tail")
	}
	if first.Sequence != state.FirstSequence || first.PreviousHash != state.FirstPreviousHash {
		return errors.New("audit chain state does not match the log prefix")
	}
	return nil
}
