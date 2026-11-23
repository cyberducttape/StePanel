package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/doctor"
)

func TestMigrationAnalysisStatusPayloadUsesDurableJobStates(t *testing.T) {
	output, err := json.Marshal(migrationAnalysisResponse{Analysis: doctor.MigrationAnalysis{Mode: "real", ReadyForMigration: true}})
	if err != nil {
		t.Fatal(err)
	}
	completed := migrationAnalysisStatusPayload("job-completed", Job{Kind: "migration.analysis", State: "completed", Output: output})
	analysis, ok := completed["analysis"].(doctor.MigrationAnalysis)
	if !ok || !analysis.ReadyForMigration {
		t.Fatalf("completed payload = %#v, want decoded analysis", completed)
	}

	failed := migrationAnalysisStatusPayload("job-failed", Job{Kind: "migration.analysis", State: "failed", Error: "source unavailable"})
	if got, ok := failed["error"].(string); !ok || !strings.Contains(got, "source unavailable") {
		t.Fatalf("failed payload = %#v, want durable job error", failed)
	}
}

func TestControlPlaneDoctorUsesCachedIntegrityEvidence(t *testing.T) {
	jobs, err := OpenDurableJobs(filepath.Join(t.TempDir(), "control.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jobs.Close() })
	app := &App{Jobs: jobs}

	controlPlane := app.checkControlPlaneReadiness()
	if controlPlane.Status != "pass" {
		t.Fatalf("bounded control-plane check = %+v", controlPlane)
	}
	if checkedAt, integrityErr, checked := jobs.IntegrityStatus(); checked || !checkedAt.IsZero() || integrityErr != nil {
		t.Fatalf("doctor unexpectedly ran full integrity check: checkedAt=%v err=%v checked=%v", checkedAt, integrityErr, checked)
	}
	if integrity := app.checkControlPlaneIntegrityReadiness(); integrity.Status != "warning" || !strings.Contains(integrity.Message, "has not completed") || integrity.Remediation == "" {
		t.Fatalf("missing integrity evidence = %+v", integrity)
	}

	if err := jobs.VerifyIntegrity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if integrity := app.checkControlPlaneIntegrityReadiness(); integrity.Status != "pass" || !strings.Contains(integrity.Message, "succeeded at") {
		t.Fatalf("successful integrity evidence = %+v", integrity)
	}

	jobs.integrityMu.Lock()
	jobs.integrityAt = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	jobs.integrityErr = errors.New("injected quick_check failure")
	jobs.integrityMu.Unlock()
	integrity := app.checkControlPlaneIntegrityReadiness()
	if integrity.Status != "fail" || !strings.Contains(integrity.Message, "injected quick_check failure") || integrity.Remediation == "" {
		t.Fatalf("failed integrity evidence = %+v", integrity)
	}
}

func TestDeploymentModeReadinessDistinguishesContainerPackaging(t *testing.T) {
	previous := containerMarkers
	t.Cleanup(func() { containerMarkers = previous })
	root := t.TempDir()
	marker := filepath.Join(root, ".dockerenv")
	containerMarkers = []string{marker}
	t.Setenv("KUBERNETES_SERVICE_HOST", "")

	if check := checkDeploymentModeReadiness(); check.Status != "pass" || !strings.Contains(check.Message, "native host") {
		t.Fatalf("native deployment check = %+v", check)
	}
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	check := checkDeploymentModeReadiness()
	if check.Status != "warning" || !strings.HasPrefix(check.Message, "container deployment") || !strings.Contains(check.Message, "root broker") || !strings.Contains(check.Remediation, "install.sh") {
		t.Fatalf("container deployment check = %+v", check)
	}
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	if check := checkDeploymentModeReadiness(); !strings.HasPrefix(check.Message, "Kubernetes deployment") {
		t.Fatalf("Kubernetes deployment check = %+v", check)
	}
}
