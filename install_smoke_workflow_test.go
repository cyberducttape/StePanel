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
		{path: ".github/workflows/install-smoke.yml", group: "stepanel-install-smoke"},
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
