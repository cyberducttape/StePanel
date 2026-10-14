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
