package stepanel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
