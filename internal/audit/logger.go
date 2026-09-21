package audit

import (
	"bufio"
	"context"
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

const maxAuditLogBytes int64 = 256 << 20
const AuditKeyPath = "/etc/stepanel-audit.key"

var mu sync.Mutex
var persistenceErr error
var distributedLocks = make(map[string]string)
var distributedLocksMu sync.Mutex

// auditKeyPath is used by tests to mock the key file location
var auditKeyPath = AuditKeyPath

type defaultLogger struct {
	path string
}

func New(path string) Logger {
	return &defaultLogger{path: path}
}

func (l *defaultLogger) Log(ctx context.Context, action, target, detail string) error {
	return l.LogAs(ctx, "system", action, target, detail)
}

func (l *defaultLogger) LogAs(ctx context.Context, actor, action, target, detail string) error {
	if strings.TrimSpace(l.path) == "" {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	err := l.appendEvent(actor, action, target, detail)
	if err != nil {
		persistenceErr = err
	}
	return err
}

func (l *defaultLogger) PersistenceError() error {
	mu.Lock()
	defer mu.Unlock()
	return persistenceErr
}

func (l *defaultLogger) appendEvent(actor, action, target, detail string) error {
	if actor == "" || action == "" {
		return errors.New("audit actor and action are required")
	}

	_, unlock, err := l.acquireLock()
	if err != nil {
		return fmt.Errorf("audit lock: %w", err)
	}
	defer unlock()

	actor = truncateValue(actor, 128)
	action = truncateValue(action, 128)
	target = truncateValue(target, 512)
	detail = truncateValue(detail, 4096)

	root := filepath.Dir(l.path)
	if err := os.MkdirAll(root, 0750); err != nil {
		return err
	}

	statePath := l.path + ".state"
	if info, statErr := os.Stat(l.path); statErr == nil && info.Size() >= maxAuditLogBytes {
		return errors.New("audit log exceeds the 256 MiB retention limit; rotate it after verification")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	s, err := l.loadState(statePath)
	if err != nil {
		return err
	}

	if s.Version == 0 {
		keyCheck, err := auditKeyCheck()
		if err != nil {
			return err
		}
		if info, statErr := os.Stat(l.path); statErr == nil && info.Size() > 0 {
			legacy := l.path + ".legacy-" + time.Now().UTC().Format("20060102-150405")
			if err := os.Rename(l.path, legacy); err != nil {
				return fmt.Errorf("preserve legacy audit log: %w", err)
			}
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		s = state{Version: 1, KeyCheck: keyCheck}
	}

	info, statErr := os.Stat(l.path)
	if statErr == nil && s.LastValidatedSize == info.Size() && s.LastValidatedSize > 0 {
	} else {
		s, err = l.reconcileTail(s)
		if err != nil {
			return err
		}
		if info, statErr := os.Stat(l.path); statErr == nil {
			s.LastValidatedSize = info.Size()
		}
	}

	event := Event{
		Time:         time.Now().UTC().Format(time.RFC3339Nano),
		Sequence:     s.Sequence + 1,
		Actor:        actor,
		Action:       action,
		Target:       target,
		Detail:       detail,
		PreviousHash: s.Hash,
	}

	var hashErr error
	event.Hash, hashErr = hashEvent(event)
	if hashErr != nil {
		return hashErr
	}

	line, err := json.Marshal(event)
	if err != nil {
		return err
	}

	file, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
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

	finalSize := int64(0)
	if info, statErr := os.Stat(l.path); statErr == nil {
		finalSize = info.Size()
	}

	return l.writeState(statePath, state{
		Version:           1,
		Sequence:          event.Sequence,
		Hash:              event.Hash,
		FirstSequence:     s.FirstSequence,
		FirstPreviousHash: s.FirstPreviousHash,
		KeyCheck:          s.KeyCheck,
		LastValidatedSize: finalSize,
	})
}

func (l *defaultLogger) acquireLock() (string, func() error, error) {
	lockFile := l.path + ".lock"

	for attempts := 0; attempts < 300; attempts++ {
		file, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			fmt.Fprintf(file, "%d", os.Getpid())
			file.Close()
			return lockFile, func() error { return os.Remove(lockFile) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, fmt.Errorf("acquire audit lock: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", nil, errors.New("timeout acquiring audit lock")
}

func (l *defaultLogger) reconcileTail(s state) (state, error) {
	file, err := os.Open(l.path)
	if errors.Is(err, os.ErrNotExist) {
		s.FirstSequence = s.Sequence + 1
		s.FirstPreviousHash = s.Hash
		return s, nil
	}
	if err != nil {
		return s, err
	}
	defer file.Close()

	var first, last Event
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)

	for scanner.Scan() {
		if err := json.Unmarshal(scanner.Bytes(), &last); err != nil {
			return s, errors.New("audit log contains an invalid event")
		}
		if err := validateEvent(last); err != nil {
			return s, err
		}
		if first.Sequence == 0 {
			first = last
		}
	}

	if err := scanner.Err(); err != nil {
		return s, err
	}

	if last.Sequence == 0 {
		s.FirstSequence = s.Sequence + 1
		s.FirstPreviousHash = s.Hash
		return s, nil
	}

	if s.FirstSequence == 0 {
		s.FirstSequence = first.Sequence
		s.FirstPreviousHash = first.PreviousHash
	}

	if first.Sequence != s.FirstSequence || first.PreviousHash != s.FirstPreviousHash {
		return s, errors.New("audit log prefix does not match chain state")
	}

	if last.Sequence == s.Sequence {
		if last.Sequence > 0 && last.Hash != s.Hash {
			return s, errors.New("audit log tail does not match chain state")
		}
		if last.Sequence > 0 {
			expected, err := hashEvent(last)
			if err != nil || !hmac.Equal([]byte(expected), []byte(last.Hash)) {
				return s, errors.New("audit log tail has an invalid signature")
			}
		}
		return s, nil
	}

	if last.Sequence == s.Sequence+1 && last.PreviousHash == s.Hash {
		expected, err := hashEvent(last)
		if err != nil || !hmac.Equal([]byte(expected), []byte(last.Hash)) {
			return s, errors.New("uncommitted audit tail has an invalid signature")
		}
		return state{
			Version:           1,
			Sequence:          last.Sequence,
			Hash:              last.Hash,
			FirstSequence:     s.FirstSequence,
			FirstPreviousHash: s.FirstPreviousHash,
			KeyCheck:          s.KeyCheck,
		}, nil
	}

	return s, errors.New("audit log and chain state are inconsistent")
}

func (l *defaultLogger) loadState(path string) (state, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state{}, nil
	}
	if err != nil {
		return state{}, err
	}

	keyCheck, err := auditKeyCheck()
	if err != nil {
		return state{}, err
	}

	var s state
	if err := json.Unmarshal(data, &s); err != nil || s.Version != 1 ||
		s.Sequence == 0 && s.Hash != "" ||
		s.Sequence > 0 && len(s.Hash) != sha256.Size*2 ||
		s.FirstSequence > s.Sequence+1 ||
		!hmac.Equal([]byte(s.KeyCheck), []byte(keyCheck)) {
		return s, errors.New("invalid audit chain state")
	}

	expected, err := signState(s)
	if err != nil || !hmac.Equal([]byte(s.Signature), []byte(expected)) {
		return s, errors.New("audit chain state has an invalid signature")
	}

	return s, nil
}

func (l *defaultLogger) writeState(path string, s state) error {
	var err error
	s.Signature, err = signState(s)
	if err != nil {
		return err
	}

	data, err := json.Marshal(s)
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

	if err = temp.Chmod(0600); err != nil {
		temp.Close()
		return fmt.Errorf("secure audit state file permissions: %w", err)
	}

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

func (l *defaultLogger) Verify(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	var first, previous *Event
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)

	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return err
		}
		if err := validateEvent(event); err != nil {
			return err
		}

		expected, err := hashEvent(event)
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

	s, err := l.loadState(path + ".state")
	if err != nil {
		return err
	}

	if s.Sequence != previous.Sequence || s.Hash != previous.Hash {
		return errors.New("audit chain state does not match the log tail")
	}

	if first.Sequence != s.FirstSequence || first.PreviousHash != s.FirstPreviousHash {
		return errors.New("audit chain state does not match the log prefix")
	}

	return nil
}

func hashEvent(event Event) (string, error) {
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

func signState(s state) (string, error) {
	s.Signature = ""
	data, err := json.Marshal(s)
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

func auditSigningKey() ([]byte, error) {
	key := os.Getenv("STEPANEL_AUDIT_KEY")
	if key == "" {
		if data, err := os.ReadFile(auditKeyPath); err == nil {
			key = strings.TrimSpace(string(data))
		}
	}
	if key == "" {
		key = os.Getenv("STEPANEL_SESSION_SECRET")
	}
	if len(key) < 32 {
		return nil, errors.New("STEPANEL_AUDIT_KEY or STEPANEL_SESSION_SECRET must contain at least 32 characters")
	}
	return []byte("stepanel-audit-v1\x00" + key), nil
}

func truncateValue(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func validateEvent(event Event) error {
	if event.Sequence == 0 || strings.TrimSpace(event.Actor) == "" || strings.TrimSpace(event.Action) == "" {
		return errors.New("audit log contains an event with invalid identity or sequence")
	}
	if _, err := time.Parse(time.RFC3339Nano, event.Time); err != nil {
		return errors.New("audit log contains an invalid event timestamp")
	}
	return nil
}
