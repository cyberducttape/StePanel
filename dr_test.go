package stepanel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestControlPlaneDRDoesNotRequireLegacyJobsWhenDatabaseIsConfigured(t *testing.T) {
	root := t.TempDir()
	checks := controlPlaneDRChecks(Config{
		ControlPlaneDB: filepath.Join(root, "control-plane.db"),
		JobState:       filepath.Join(root, "legacy-jobs.json"),
		AuditLog:       filepath.Join(root, "audit.jsonl"),
	})
	for _, check := range checks {
		if check.Name == "legacy job state" && check.Status != "optional-missing" {
			t.Fatalf("legacy job check = %#v", check)
		}
		if check.Name == "job state" {
			t.Fatal("durable control-plane DR manifest still requires legacy job state")
		}
	}
}

func TestControlPlaneDRRejectsBroadPermissionsOnRequiredArtifact(t *testing.T) {
	root := t.TempDir()
	audit := filepath.Join(root, "audit.jsonl")
	if err := os.WriteFile(audit, []byte("audit"), 0644); err != nil {
		t.Fatal(err)
	}
	checks := controlPlaneDRChecks(Config{
		ControlPlaneDB: filepath.Join(root, "control-plane.db"),
		AuditLog:       audit,
	})
	for _, check := range checks {
		if check.Name == "audit log" && check.Status != "unsafe" {
			t.Fatalf("required audit permission check = %#v", check)
		}
	}
}
