package audit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestLogger(t *testing.T) (*defaultLogger, string) {
	t.Helper()
	root := t.TempDir()
	keyPath := filepath.Join(root, "audit.key")
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("k", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	previousKeyPath := auditKeyPath
	TestSetKeyPath(keyPath)
	t.Setenv("STEPANEL_AUDIT_KEY", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "")
	t.Cleanup(func() { TestSetKeyPath(previousKeyPath) })
	TestResetPersistenceError()
	return New(filepath.Join(root, "audit.jsonl")).(*defaultLogger), root
}

func TestLoggerWritesVerifiesAndFiltersSignedEvents(t *testing.T) {
	logger, _ := newTestLogger(t)
	ctx := context.Background()
	if err := logger.LogAs(ctx, "admin", "site.create", "site-a", "created"); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogAs(ctx, "admin", "site.delete", "site-b", "deleted"); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogAs(ctx, "operator", "site.create", "site-c", "created"); err != nil {
		t.Fatal(err)
	}
	if err := logger.Verify(logger.path); err != nil {
		t.Fatalf("verify signed log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/audit?target=site-c", nil)
	resp := httptest.NewRecorder()
	logger.Events(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("filtered events status = %d body=%s", resp.Code, resp.Body)
	}
	var payload struct {
		Events    []Event `json:"events"`
		Integrity string  `json:"integrity"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Events) != 1 || payload.Events[0].Target != "site-c" || payload.Integrity != "verified" {
		t.Fatalf("filtered payload = %#v", payload)
	}

	req = httptest.NewRequest(http.MethodGet, "/audit?limit=0", nil)
	resp = httptest.NewRecorder()
	logger.Events(resp, req)
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid limit status = %d", resp.Code)
	}

	for _, raw := range []string{"501", "not-a-number"} {
		req = httptest.NewRequest(http.MethodGet, "/audit?limit="+raw, nil)
		resp = httptest.NewRecorder()
		logger.Events(resp, req)
		if resp.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid limit %q status = %d", raw, resp.Code)
		}
	}
	req = httptest.NewRequest(http.MethodGet, "/audit?action=site.create&limit=1", nil)
	resp = httptest.NewRecorder()
	logger.Events(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("action filter status = %d", resp.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/audit?limit=1", nil)
	resp = httptest.NewRecorder()
	logger.Events(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("rolling limit status = %d", resp.Code)
	}
}

func TestReadScopedEventsFiltersTenantTargetsAndActor(t *testing.T) {
	logger, _ := newTestLogger(t)
	ctx := context.Background()
	for _, event := range []struct{ actor, action, target string }{
		{"admin", "site.deploy", "alice-site"},
		{"admin", "site.deploy", "bob-site"},
		{"alice", "auth.login.succeeded", "login"},
		{"bob", "auth.login.succeeded", "login"},
	} {
		if err := logger.LogAs(ctx, event.actor, event.action, event.target, "detail"); err != nil {
			t.Fatal(err)
		}
	}
	events, err := ReadScopedEvents(logger.path, []string{"alice", "alice-site"}, "alice", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Target != "alice-site" || events[1].Actor != "alice" {
		t.Fatalf("scoped events = %#v", events)
	}
}

func TestLoggerRejectsTamperedLogAndMissingIdentity(t *testing.T) {
	logger, _ := newTestLogger(t)
	if err := logger.LogAs(context.Background(), "admin", "site.create", "site-a", "created"); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogAs(context.Background(), "", "site.update", "site-a", "bad"); err == nil {
		t.Fatal("missing actor was accepted")
	}
	data, err := os.ReadFile(logger.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logger.path, append(data, []byte(`{"broken":true}`+"\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := logger.Verify(logger.path); err == nil {
		t.Fatal("tampered audit log verified")
	}
	req := httptest.NewRequest(http.MethodGet, "/audit", nil)
	resp := httptest.NewRecorder()
	logger.Events(resp, req)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("tampered events status = %d", resp.Code)
	}
}

func TestAuditClassificationAndHandlerLevels(t *testing.T) {
	if got := MUST_AUDIT.String(); got != "MUST_AUDIT" {
		t.Fatalf("MUST_AUDIT string = %q", got)
	}
	if got := BEST_EFFORT.String(); got != "BEST_EFFORT" {
		t.Fatalf("BEST_EFFORT string = %q", got)
	}
	if got := AuditLevel(99).String(); got != "UNKNOWN" {
		t.Fatalf("unknown level string = %q", got)
	}
	if GetClassification("site.delete") == nil || GetClassification("unknown") != nil {
		t.Fatal("audit classifications are incorrect")
	}
	var messages []string
	handler := NewDefaultAuditHandler(func(_ AuditLevel, message string) { messages = append(messages, message) })
	if err := handler.Audit(SHOULD_AUDIT, "site.create", "admin", "site-a", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := handler.AuditOrFail(MUST_AUDIT, "site.delete", "admin", "site-a", "ok"); err == nil {
		t.Fatal("MUST_AUDIT unexpectedly succeeded")
	}
	if err := handler.AuditOrFail(SHOULD_AUDIT, "site.create", "admin", "site-a", "ok"); err != nil {
		t.Fatal("SHOULD_AUDIT unexpectedly failed: ", err)
	}
	if err := handler.AuditWithResult(MUST_AUDIT, "site.delete", "admin", "site-a", "failed", os.ErrPermission); err == nil {
		t.Fatal("failed MUST_AUDIT unexpectedly succeeded")
	}
	if err := handler.AuditWithResult(SHOULD_AUDIT, "site.create", "admin", "site-a", "failed", os.ErrPermission); err != nil {
		t.Fatal("failed SHOULD_AUDIT unexpectedly failed: ", err)
	}
	if err := handler.AuditWithResult(MUST_AUDIT, "site.delete", "admin", "site-a", "ok", nil); err != nil {
		t.Fatal("successful MUST_AUDIT unexpectedly failed: ", err)
	}
	if len(messages) != 6 {
		t.Fatalf("handler messages = %d", len(messages))
	}
}

func signedTestEvent(t *testing.T, sequence uint64, previous string) Event {
	t.Helper()
	event := Event{
		Time:         "2026-01-01T00:00:00Z",
		Sequence:     sequence,
		Actor:        "admin",
		Action:       "site.update",
		Target:       "site-a",
		Detail:       "test",
		PreviousHash: previous,
	}
	var err error
	event.Hash, err = hashEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func writeTestEvents(t *testing.T, path string, events ...Event) {
	t.Helper()
	data := make([]byte, 0)
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileTailRecoveryAndCorruptionBranches(t *testing.T) {
	logger, _ := newTestLogger(t)
	first := signedTestEvent(t, 1, "")
	second := signedTestEvent(t, 2, first.Hash)
	base := state{Version: 1, Sequence: 1, Hash: first.Hash, FirstSequence: 1}
	originalPath := logger.path

	t.Run("missing log", func(t *testing.T) {
		os.Remove(logger.path)
		got, err := logger.reconcileTail(base)
		if err != nil || got.FirstSequence != 2 || got.FirstPreviousHash != first.Hash {
			t.Fatalf("missing log recovery = %#v, %v", got, err)
		}
	})
	t.Run("empty log", func(t *testing.T) {
		if err := os.WriteFile(logger.path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := logger.reconcileTail(base)
		if err != nil || got.FirstSequence != 2 {
			t.Fatalf("empty log recovery = %#v, %v", got, err)
		}
	})
	t.Run("directory scanner error", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "audit-dir")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		logger.path = directory
		if _, err := logger.reconcileTail(base); err == nil {
			t.Fatal("directory scanner error was ignored")
		}
		logger.path = originalPath
	})
	t.Run("open error", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "file-parent")
		if err := os.WriteFile(parent, nil, 0600); err != nil {
			t.Fatal(err)
		}
		logger.path = filepath.Join(parent, "audit.jsonl")
		if _, err := logger.reconcileTail(base); err == nil {
			t.Fatal("audit open error was ignored")
		}
		logger.path = originalPath
	})
	t.Run("committed tail", func(t *testing.T) {
		writeTestEvents(t, logger.path, first)
		got, err := logger.reconcileTail(base)
		if err != nil || got.Sequence != base.Sequence {
			t.Fatalf("committed tail = %#v, %v", got, err)
		}
	})
	t.Run("uncommitted tail", func(t *testing.T) {
		writeTestEvents(t, logger.path, first, second)
		got, err := logger.reconcileTail(base)
		if err != nil || got.Sequence != 2 || got.Hash != second.Hash {
			t.Fatalf("uncommitted tail = %#v, %v", got, err)
		}
	})

	invalidCases := []struct {
		name  string
		data  []byte
		state state
	}{
		{name: "malformed event", data: []byte("not-json\n"), state: base},
		{name: "invalid event", data: []byte(`{"sequence":0}` + "\n"), state: base},
		{name: "prefix mismatch", state: state{Version: 1, Sequence: 1, Hash: first.Hash, FirstSequence: 9}},
		{name: "tail mismatch", state: state{Version: 1, Sequence: 1, Hash: strings.Repeat("0", 64), FirstSequence: 1}},
		{name: "inconsistent chain", state: state{Version: 1, Sequence: 9, Hash: first.Hash, FirstSequence: 1}},
	}
	for _, test := range invalidCases {
		t.Run(test.name, func(t *testing.T) {
			if test.data == nil {
				writeTestEvents(t, logger.path, first)
			} else if err := os.WriteFile(logger.path, test.data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := logger.reconcileTail(test.state); err == nil {
				t.Fatal("corrupt chain was accepted")
			}
		})
	}

	badSignature := second
	badSignature.Hash = strings.Repeat("f", 64)
	writeTestEvents(t, logger.path, first, badSignature)
	if _, err := logger.reconcileTail(base); err == nil {
		t.Fatal("invalid uncommitted signature was accepted")
	}
	badCommitted := first
	badCommitted.Hash = strings.Repeat("f", 64)
	writeTestEvents(t, logger.path, badCommitted)
	if _, err := logger.reconcileTail(base); err == nil {
		t.Fatal("invalid committed signature was accepted")
	}
}

func TestAuditChainStateValidationBranches(t *testing.T) {
	logger, root := newTestLogger(t)
	missing := filepath.Join(root, "missing.state")
	if got, err := logger.loadState(missing); err != nil || got.Version != 0 {
		t.Fatalf("missing state = %#v, %v", got, err)
	}
	statePath := logger.path + ".state"
	keyCheck, err := auditKeyCheck()
	if err != nil {
		t.Fatal(err)
	}
	valid := state{Version: 1, Sequence: 1, Hash: strings.Repeat("a", 64), FirstSequence: 1, KeyCheck: keyCheck}
	if err := logger.writeState(statePath, valid); err != nil {
		t.Fatal(err)
	}
	if got, err := logger.loadState(statePath); err != nil || got.Sequence != 1 {
		t.Fatalf("valid state = %#v, %v", got, err)
	}

	variants := []struct {
		name string
		edit func(*state)
	}{
		{name: "malformed", edit: nil},
		{name: "version", edit: func(s *state) { s.Version = 2 }},
		{name: "hash with zero sequence", edit: func(s *state) { s.Sequence = 0 }},
		{name: "short hash", edit: func(s *state) { s.Hash = "short" }},
		{name: "first sequence", edit: func(s *state) { s.FirstSequence = 3 }},
		{name: "key check", edit: func(s *state) { s.KeyCheck = "wrong" }},
		{name: "signature", edit: func(s *state) { s.Signature = "wrong" }},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			if variant.edit == nil {
				if err := os.WriteFile(statePath, []byte("not-json"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				candidate := valid
				variant.edit(&candidate)
				data, err := json.Marshal(candidate)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(statePath, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := logger.loadState(statePath); err == nil {
				t.Fatal("invalid state was accepted")
			}
		})
	}

	if err := logger.writeState(filepath.Join(root, "missing", "state"), valid); err == nil {
		t.Fatal("writeState succeeded in a missing directory")
	}
	if err := os.Mkdir(filepath.Join(root, "state-directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := logger.loadState(filepath.Join(root, "state-directory")); err == nil {
		t.Fatal("loadState accepted a directory")
	}
	previousKeyPath := auditKeyPath
	if err := logger.writeState(statePath, valid); err != nil {
		t.Fatal(err)
	}
	TestSetKeyPath(filepath.Join(root, "missing-key"))
	t.Setenv("STEPANEL_AUDIT_KEY", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "")
	if _, err := logger.loadState(statePath); err == nil {
		t.Fatal("loadState succeeded without a signing key")
	}
	TestSetKeyPath(previousKeyPath)
}

func TestAuditUtilityValidationAndFallbacks(t *testing.T) {
	if err := New("   ").(*defaultLogger).Log(context.Background(), "ignored", "ignored", "ignored"); err != nil {
		t.Fatal("whitespace audit path was not treated as unconfigured: ", err)
	}
	if got := truncateValue("abcdef", 3); got != "abc" {
		t.Fatalf("truncateValue = %q", got)
	}
	if got := truncateValue("héllo", 3); got != "hél" {
		t.Fatalf("unicode truncateValue = %q", got)
	}
	if err := validateEvent(Event{}); err == nil {
		t.Fatal("empty event was accepted")
	}
	if err := validateEvent(Event{Sequence: 1, Actor: "admin", Action: "read", Time: "bad"}); err == nil {
		t.Fatal("invalid timestamp was accepted")
	}
	if err := validateEvent(Event{Sequence: 1, Actor: "admin", Action: "read", Time: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	logger, _ := newTestLogger(t)
	if err := logger.Log(context.Background(), "site.read", "site-a", "ok"); err != nil {
		t.Fatal(err)
	}
	if logger.PersistenceError() != nil {
		t.Fatal("successful log recorded a persistence error")
	}
	t.Setenv("STEPANEL_AUDIT_KEY", "")
	t.Setenv("STEPANEL_SESSION_SECRET", strings.Repeat("s", 32))
	if _, err := auditSigningKey(); err != nil {
		t.Fatal("session secret fallback failed: ", err)
	}
	previousKeyPath := auditKeyPath
	TestSetKeyPath(filepath.Join(t.TempDir(), "missing-key"))
	t.Cleanup(func() { TestSetKeyPath(previousKeyPath) })
	t.Setenv("STEPANEL_SESSION_SECRET", "short")
	if _, err := auditSigningKey(); err == nil {
		t.Fatal("short audit key was accepted")
	}
	if _, err := hashEvent(Event{Sequence: 1}); err == nil {
		t.Fatal("hashEvent succeeded without a signing key")
	}
	if _, err := signState(state{Version: 1}); err == nil {
		t.Fatal("signState succeeded without a signing key")
	}
}

func TestVerifyAndRetentionFailures(t *testing.T) {
	logger, _ := newTestLogger(t)
	if err := logger.Verify(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing audit log verified")
	}
	if err := logger.Verify(t.TempDir()); err == nil {
		t.Fatal("directory audit log verified")
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := logger.Verify(empty); err == nil {
		t.Fatal("empty audit log verified")
	}
	if err := logger.Log(context.Background(), "site.read", "site-a", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := logger.Verify(logger.path); err != nil {
		t.Fatal(err)
	}
	statePath := logger.path + ".state"
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := logger.Verify(logger.path); err == nil {
		t.Fatal("audit log without chain state verified")
	}
	verifiedLogger, _ := newTestLogger(t)
	if err := verifiedLogger.Log(context.Background(), "site.read", "site-a", "verified"); err != nil {
		t.Fatal(err)
	}
	verifiedStatePath := verifiedLogger.path + ".state"
	verifiedState, err := verifiedLogger.loadState(verifiedStatePath)
	if err != nil {
		t.Fatal(err)
	}
	verifiedState.Sequence = 99
	verifiedState.Signature, err = signState(verifiedState)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifiedLogger.writeState(verifiedStatePath, verifiedState); err != nil {
		t.Fatal(err)
	}
	if err := verifiedLogger.Verify(verifiedLogger.path); err == nil {
		t.Fatal("chain state tail mismatch was accepted")
	}
	verifiedState, err = verifiedLogger.loadState(verifiedStatePath)
	if err != nil {
		t.Fatal(err)
	}
	verifiedState.Sequence = 1
	verifiedState.FirstSequence = 2
	verifiedState.Signature, err = signState(verifiedState)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifiedLogger.writeState(verifiedStatePath, verifiedState); err != nil {
		t.Fatal(err)
	}
	if err := verifiedLogger.Verify(verifiedLogger.path); err == nil {
		t.Fatal("chain state prefix mismatch was accepted")
	}
	for name, events := range map[string][]Event{
		"chain break": {
			signedTestEvent(t, 1, ""),
			signedTestEvent(t, 3, "wrong-previous"),
		},
		"invalid event": {{Sequence: 0, Actor: "admin", Action: "read", Time: "2026-01-01T00:00:00Z"}},
	} {
		path := filepath.Join(t.TempDir(), name+".log")
		writeTestEvents(t, path, events...)
		if err := logger.Verify(path); err == nil {
			t.Fatalf("%s unexpectedly verified", name)
		}
	}
	badSignature := signedTestEvent(t, 1, "")
	badSignature.Hash = strings.Repeat("0", 64)
	badPath := filepath.Join(t.TempDir(), "bad-signature.log")
	writeTestEvents(t, badPath, badSignature)
	if err := logger.Verify(badPath); err == nil {
		t.Fatal("invalid event signature verified")
	}

	retained := filepath.Join(t.TempDir(), "retained.log")
	retainedLogger := New(retained).(*defaultLogger)
	if err := os.WriteFile(retained, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(retained, maxAuditLogBytes); err != nil {
		t.Fatal(err)
	}
	if err := retainedLogger.Log(context.Background(), "site.read", "site-a", "too large"); err == nil {
		t.Fatal("oversized audit log accepted")
	}
}

func TestAuditAppendFailureBoundaries(t *testing.T) {
	logger, root := newTestLogger(t)
	parent := filepath.Join(root, "lock-parent")
	if err := os.WriteFile(parent, nil, 0600); err != nil {
		t.Fatal(err)
	}
	originalPath := logger.path
	logger.path = filepath.Join(parent, "audit.jsonl")
	if _, _, err := logger.acquireLock(); err == nil {
		t.Fatal("audit lock parent failure was ignored")
	}
	logger.path = originalPath
	if err := os.WriteFile(logger.path+".state", []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := logger.Log(context.Background(), "site.read", "site-a", "state failure"); err == nil {
		t.Fatal("invalid audit state was ignored")
	}
	if err := os.Remove(logger.path + ".state"); err != nil {
		t.Fatal(err)
	}
	if err := logger.LogAs(context.Background(), "", "site.read", "site-a", "bad actor"); err == nil {
		t.Fatal("missing actor was accepted")
	}
	if err := logger.LogAs(context.Background(), "admin", "", "site-a", "bad action"); err == nil {
		t.Fatal("missing action was accepted")
	}
}

func TestAuditAppendRejectsUnavailableSigningKey(t *testing.T) {
	logger, _ := newTestLogger(t)
	previousKeyPath := auditKeyPath
	TestSetKeyPath(filepath.Join(t.TempDir(), "missing-key"))
	t.Setenv("STEPANEL_AUDIT_KEY", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "")
	t.Cleanup(func() { TestSetKeyPath(previousKeyPath) })
	if err := logger.Log(context.Background(), "site.read", "site-a", "missing key"); err == nil {
		t.Fatal("audit append succeeded without a signing key")
	}
}

func TestAuditAppendFilesystemFailureBranches(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*defaultLogger) error
		inject func()
	}{
		{name: "mkdir", inject: func() { auditMkdirAll = func(string, os.FileMode) error { return errors.New("mkdir") } }},
		{name: "stat", inject: func() { auditStat = func(string) (os.FileInfo, error) { return nil, errors.New("stat") } }},
		{name: "open file", inject: func() {
			auditOpenFile = func(string, int, os.FileMode) (*os.File, error) { return nil, errors.New("open") }
		}},
		{name: "write", inject: func() { auditWrite = func(*os.File, []byte) (int, error) { return 0, errors.New("write") } }},
		{name: "short write", inject: func() { auditWrite = func(_ *os.File, data []byte) (int, error) { return len(data) - 1, nil } }},
		{name: "sync", inject: func() { auditSync = func(*os.File) error { return errors.New("sync") } }},
		{name: "close", inject: func() { auditClose = func(*os.File) error { return errors.New("close") } }},
		{name: "directory open", inject: func() { auditOpen = func(string) (*os.File, error) { return nil, errors.New("directory open") } }},
		{name: "legacy rename", setup: func(logger *defaultLogger) error { return os.WriteFile(logger.path, []byte("legacy\n"), 0600) }, inject: func() { auditRename = func(string, string) error { return errors.New("rename") } }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logger, _ := newTestLogger(t)
			preserveAuditHooks(t)
			if test.setup != nil {
				if err := test.setup(logger); err != nil {
					t.Fatal(err)
				}
			}
			test.inject()
			if err := logger.Log(context.Background(), "site.read", "site-a", "failure"); err == nil {
				t.Fatal("filesystem failure was ignored")
			}
		})
	}
	t.Run("lock error through append", func(t *testing.T) {
		logger, root := newTestLogger(t)
		parent := filepath.Join(root, "lock-parent")
		if err := os.WriteFile(parent, nil, 0600); err != nil {
			t.Fatal(err)
		}
		logger.path = filepath.Join(parent, "audit.jsonl")
		if err := logger.Log(context.Background(), "site.read", "site-a", "lock"); err == nil {
			t.Fatal("append ignored lock acquisition failure")
		}
	})
}

func TestAuditEventsEmptyAndReadFailures(t *testing.T) {
	missing := New(filepath.Join(t.TempDir(), "missing.jsonl")).(*defaultLogger)
	request := httptest.NewRequest(http.MethodGet, "/audit", nil)
	response := httptest.NewRecorder()
	missing.Events(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"integrity":"empty"`) {
		t.Fatalf("missing audit events = %d %s", response.Code, response.Body.String())
	}

	emptyPath := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(emptyPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	empty := New(emptyPath).(*defaultLogger)
	response = httptest.NewRecorder()
	empty.Events(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty audit events status = %d", response.Code)
	}

	directory := New(t.TempDir()).(*defaultLogger)
	response = httptest.NewRecorder()
	directory.Events(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("directory audit events status = %d", response.Code)
	}
}

func TestReadVerifiedEventsRejectsEveryIntegrityFailure(t *testing.T) {
	logger, root := newTestLogger(t)
	if _, err := logger.readVerifiedEvents("", "", 10); err == nil {
		t.Fatal("missing audit log was accepted")
	}
	malformedPath := filepath.Join(root, "malformed-read.jsonl")
	if err := os.WriteFile(malformedPath, []byte("not-json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	logger.path = malformedPath
	if _, err := logger.readVerifiedEvents("", "", 10); err == nil {
		t.Fatal("malformed audit log was accepted")
	}
	emptyPath := filepath.Join(root, "empty-read.jsonl")
	if err := os.WriteFile(emptyPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	logger.path = emptyPath
	if _, err := logger.readVerifiedEvents("", "", 10); err == nil {
		t.Fatal("empty audit log was accepted")
	}

	first := signedTestEvent(t, 1, "")
	second := signedTestEvent(t, 2, first.Hash)
	writeTestEvents(t, emptyPath, first, second)
	// A valid log without its signed state must fail closed.
	if _, err := logger.readVerifiedEvents("", "", 10); err == nil {
		t.Fatal("audit log without state was accepted")
	}

	badSignature := second
	badSignature.Hash = strings.Repeat("0", 64)
	writeTestEvents(t, emptyPath, first, badSignature)
	if _, err := logger.readVerifiedEvents("", "", 10); err == nil {
		t.Fatal("invalid event signature was accepted")
	}
	broken := signedTestEvent(t, 4, "wrong")
	writeTestEvents(t, emptyPath, first, broken)
	if _, err := logger.readVerifiedEvents("", "", 10); err == nil {
		t.Fatal("broken event chain was accepted")
	}
	writeTestEvents(t, emptyPath, first, second)
	keyCheck, err := auditKeyCheck()
	if err != nil {
		t.Fatal(err)
	}
	chainState := state{Version: 1, Sequence: 2, Hash: second.Hash, FirstSequence: 1, KeyCheck: keyCheck}
	if err := logger.writeState(emptyPath+".state", chainState); err != nil {
		t.Fatal(err)
	}
	chainState.Sequence = 9
	chainState.Signature, err = signState(chainState)
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.writeState(emptyPath+".state", chainState); err != nil {
		t.Fatal(err)
	}
	if _, err := logger.readVerifiedEvents("", "", 10); err == nil {
		t.Fatal("chain state mismatch was accepted")
	}
}

func preserveAuditHooks(t *testing.T) {
	t.Helper()
	mkdirAll, stat, createTemp, rename, open, openFile := auditMkdirAll, auditStat, auditCreateTemp, auditRename, auditOpen, auditOpenFile
	chmod, write, syncFile, closeFile := auditChmod, auditWrite, auditSync, auditClose
	sleep, tries := auditLockSleep, auditLockTries
	t.Cleanup(func() {
		auditMkdirAll, auditStat, auditCreateTemp, auditRename, auditOpen, auditOpenFile = mkdirAll, stat, createTemp, rename, open, openFile
		auditChmod, auditWrite, auditSync, auditClose = chmod, write, syncFile, closeFile
		auditLockSleep, auditLockTries = sleep, tries
	})
}

func TestAuditStateWriteAndLockFailureBranches(t *testing.T) {
	logger, root := newTestLogger(t)
	keyCheck, err := auditKeyCheck()
	if err != nil {
		t.Fatal(err)
	}
	stateValue := state{Version: 1, Sequence: 1, Hash: strings.Repeat("a", 64), FirstSequence: 1, KeyCheck: keyCheck}
	tests := []struct {
		name   string
		inject func()
	}{
		{name: "create temp", inject: func() { auditCreateTemp = func(string, string) (*os.File, error) { return nil, errors.New("create") } }},
		{name: "chmod", inject: func() { auditChmod = func(*os.File, os.FileMode) error { return errors.New("chmod") } }},
		{name: "write", inject: func() { auditWrite = func(*os.File, []byte) (int, error) { return 0, errors.New("write") } }},
		{name: "short write", inject: func() { auditWrite = func(_ *os.File, data []byte) (int, error) { return len(data) - 1, nil } }},
		{name: "file sync", inject: func() { auditSync = func(*os.File) error { return errors.New("sync") } }},
		{name: "file close", inject: func() { auditClose = func(*os.File) error { return errors.New("close") } }},
		{name: "rename", inject: func() { auditRename = func(string, string) error { return errors.New("rename") } }},
		{name: "directory open", inject: func() { auditOpen = func(string) (*os.File, error) { return nil, errors.New("open") } }},
		{name: "directory sync", inject: func() {
			calls := 0
			auditSync = func(*os.File) error {
				calls++
				if calls == 2 {
					return errors.New("directory sync")
				}
				return nil
			}
		}},
		{name: "directory close", inject: func() {
			calls := 0
			auditClose = func(*os.File) error {
				calls++
				if calls == 2 {
					return errors.New("directory close")
				}
				return nil
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preserveAuditHooks(t)
			test.inject()
			if err := logger.writeState(filepath.Join(root, test.name+"-state"), stateValue); err == nil {
				t.Fatal("writeState unexpectedly succeeded")
			}
		})
	}

	lockPath := logger.path + ".lock"
	if err := os.WriteFile(lockPath, []byte("held"), 0600); err != nil {
		t.Fatal(err)
	}
	preserveAuditHooks(t)
	auditLockTries = 1
	auditLockSleep = func(time.Duration) {}
	if _, _, err := logger.acquireLock(); err == nil {
		t.Fatal("audit lock timeout was not reported")
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
}

func TestAuditReclaimsLockOwnedByDeadProcess(t *testing.T) {
	logger, _ := newTestLogger(t)
	lockPath := logger.path + ".lock"
	if err := os.WriteFile(lockPath, []byte("999999 1"), 0600); err != nil {
		t.Fatal(err)
	}

	lock, unlock, err := logger.acquireLock()
	if err != nil {
		t.Fatalf("stale audit lock was not reclaimed: %v", err)
	}
	if lock != lockPath {
		t.Fatalf("lock path = %q, want %q", lock, lockPath)
	}
	if err := unlock(); err != nil {
		t.Fatalf("release reclaimed audit lock: %v", err)
	}
}

func TestAuditLegacyLogIsPreservedBeforeStartingChain(t *testing.T) {
	logger, root := newTestLogger(t)
	if err := os.WriteFile(logger.path, []byte("legacy audit data\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := logger.Log(context.Background(), "site.read", "site-a", "new chain"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	foundLegacy := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "audit.jsonl.legacy-") {
			foundLegacy = true
			break
		}
	}
	if !foundLegacy {
		t.Fatal("legacy audit log was not preserved")
	}
}
