package stepanel

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Production requires a dedicated audit key. Give tests a deterministic,
	// independent key by default; tests for missing-key behavior clear it with
	// t.Setenv so they continue to exercise fail-closed handling explicitly.
	if os.Getenv("STEPANEL_AUDIT_KEY") == "" {
		_ = os.Setenv("STEPANEL_AUDIT_KEY", "stepanel-test-only-audit-key-32-bytes-minimum")
	}
	// Tests deploy apps on fixed ports; do not let a listener on the test
	// host refuse them. TestAppPortConflict covers the real probe.
	appPortListening = func(int) bool { return false }
	os.Exit(m.Run())
}
