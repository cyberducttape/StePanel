package audit

import (
	"context"
	"log"
	"net/http"
)

var defaultAudit Logger

func SetDefault(logger Logger) {
	defaultAudit = logger
}

func Log(action, target, detail string) error {
	if defaultAudit == nil {
		return nil
	}
	return defaultAudit.Log(context.Background(), action, target, detail)
}

func LogAs(actor, action, target, detail string) error {
	if defaultAudit == nil {
		return nil
	}
	return defaultAudit.LogAs(context.Background(), actor, action, target, detail)
}

func PersistenceError() error {
	if defaultAudit == nil {
		return nil
	}
	return defaultAudit.PersistenceError()
}

func Verify(path string) error {
	if defaultAudit == nil {
		return nil
	}
	return defaultAudit.Verify(path)
}

func MustLog(w http.ResponseWriter, actor, action, target, detail string) error {
	if err := LogAs(actor, action, target, detail); err != nil {
		log.Printf("[CRITICAL] audit persistence unavailable during %s for %s/%s: %v", action, actor, target, err)
		http.Error(w, "audit system unavailable; the operation was applied but could not be recorded, contact an administrator", http.StatusServiceUnavailable)
		return err
	}
	return nil
}

func ShouldLog(actor, action, target, detail string) error {
	if err := LogAs(actor, action, target, detail); err != nil {
		log.Printf("[ERROR] audit persistence failed during %s/%s: %v (operator should investigate)", action, target, err)
		return err
	}
	return nil
}

// TestSetKeyPath allows tests to mock the audit key file path
func TestSetKeyPath(path string) {
	auditKeyPath = path
}

// TestResetPersistenceError allows tests to clear persistence error state
func TestResetPersistenceError() {
	mu.Lock()
	defer mu.Unlock()
	persistenceErr = nil
}
