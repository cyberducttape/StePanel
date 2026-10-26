package stepanel

import (
	"os"
	"strings"
	"testing"
)

func TestScheduledWorkflowConcurrencyIsEventScoped(t *testing.T) {
	for _, workflow := range []struct {
		path  string
		group string
	}{
		{path: ".github/workflows/codeql.yml", group: "stepanel-codeql"},
	} {
		content, err := os.ReadFile(workflow.path)
		if err != nil {
			t.Fatal(err)
		}
		expected := "group: " + workflow.group + "-${{ github.ref }}-${{ github.event_name }}"
		if !strings.Contains(string(content), expected) {
			t.Errorf("%s concurrency group must include github.event_name so scheduled checks cannot cancel push evidence", workflow.path)
		}
	}
}

func TestInstallRecoveryShardsHaveIndependentConcurrencyGroups(t *testing.T) {
	workflow, err := os.ReadFile(".github/workflows/install-smoke.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(workflow)
	for _, contract := range []string{
		"group: stepanel-install-smoke-${{ github.ref }}-${{ github.event_name }}-${{ inputs.recovery_matrix_shard || 'default' }}",
		"recovery_matrix_shard:",
		"RECOVERY_MATRIX_SHARD: ${{ inputs.recovery_matrix_shard || '' }}",
		"FULL_RECOVERY_MATRIX: ${{ inputs.full_recovery_matrix || false }}",
		"run: bash scripts/validate-recovery-matrix-inputs.sh",
	} {
		if !strings.Contains(text, contract) {
			t.Errorf("install smoke workflow is missing recovery-shard contract %q", contract)
		}
	}
	validator, err := os.ReadFile("scripts/validate-recovery-matrix-inputs.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{
		"[[ $repeats == 1 ]]",
		"[[ $full == true ]]",
		"recovery_matrix_repeats must be 1; run repeated certification as one-iteration shards",
		"full recovery shards require a unique recovery_matrix_shard",
	} {
		if !strings.Contains(string(validator), contract) {
			t.Errorf("recovery-matrix validator is missing contract %q", contract)
		}
	}
}

func TestInstallSmokeTimeoutLeavesHostAndDiagnosticHeadroom(t *testing.T) {
	workflow, err := os.ReadFile(".github/workflows/install-smoke.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(workflow)
	for _, budget := range []string{
		"timeout-minutes: ${{ inputs.full_recovery_matrix && 360 || 150 }}",
		"inputs.full_recovery_matrix && '350m' || '140m'",
		"80-minute deadline while apt was still provisioning vsftpd",
		"140 minutes by default and preserve ten minutes for diagnostics.",
		"One full recovery iteration needs substantially more wall-clock",
	} {
		if !strings.Contains(text, budget) {
			t.Errorf("install smoke timeout contract is missing %q", budget)
		}
	}
}

func TestControlPlaneDBOutageSmokeRequiresExplicitFailClosedEvidence(t *testing.T) {
	script, err := os.ReadFile("deploy/lab/control-plane-db-outage-smoke.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	for _, evidence := range []string{
		"journalctl -u stepanel.service --no-pager -n 200",
		"(open|ping) control-plane database:",
		"control_plane_integrity",
		"unexpected readiness response during database outage",
	} {
		if !strings.Contains(text, evidence) {
			t.Errorf("control-plane database outage drill lacks explicit evidence check %q", evidence)
		}
	}
	for _, branch := range []string{
		`case "$ready_status" in`,
		"  503)",
		"  000|unreachable)",
		"  *)",
	} {
		if !strings.Contains(text, branch) {
			t.Errorf("database outage drill lacks explicit status handling %q", branch)
		}
	}
}
