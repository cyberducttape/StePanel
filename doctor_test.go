package stepanel

import (
	"encoding/json"
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
