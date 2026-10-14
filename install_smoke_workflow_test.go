package stepanel

import (
	"os"
	"strings"
	"testing"
)

func TestInstallSmokeConcurrencyIsEventScoped(t *testing.T) {
	workflow, err := os.ReadFile(".github/workflows/install-smoke.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workflow), "group: stepanel-install-smoke-${{ github.ref }}-${{ github.event_name }}") {
		t.Fatal("install smoke concurrency group must isolate events so scheduled checks cannot cancel push evidence")
	}
}
