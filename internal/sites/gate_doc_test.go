package sites

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestGateOneDocumentMatchesManager keeps the release gate aligned with the
// filesystem primitive API. Durable workflow and privileged operations must
// not be represented as manager methods until this package owns them.
func TestGateOneDocumentMatchesManager(t *testing.T) {
	managerType := reflect.TypeOf((*Manager)(nil)).Elem()
	methods := make([]string, managerType.NumMethod())
	for i := 0; i < managerType.NumMethod(); i++ {
		methods[i] = managerType.Method(i).Name
	}
	sort.Strings(methods)
	want := []string{
		"ActivateStaged", "ActivateStagedReplacing", "CleanupOrphanedStaging",
		"Clone", "Create", "CreateReleaseStagingLease", "CreateStaging", "Delete",
		"DiscardReleaseStaging", "DiscardStaging", "RollbackStagedActivation",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(methods, want) {
		t.Fatalf("Manager methods = %v, want implemented filesystem primitives %v", methods, want)
	}
	if !reflect.TypeOf((*DefaultManager)(nil)).Implements(managerType) {
		t.Fatal("DefaultManager no longer implements the authoritative Manager interface")
	}

	data, err := os.ReadFile("../../docs/V1_PRODUCTION_GATES.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	start := strings.Index(doc, "## Gate 1:")
	end := strings.Index(doc, "## Gate 2:")
	if start < 0 || end < start {
		t.Fatal("Gate 1 section not found")
	}
	gate := doc[start:end]
	gateNormalized := strings.Join(strings.Fields(strings.ToLower(gate)), " ")
	for _, obsolete := range []string{
		"ErrNotImplemented", "SiteManager.ImportArchive(", "SiteManager.Restore(",
		"SiteManager.UpdateConfiguration(", "SiteManager.Suspend(", "SiteManager.Resume(",
	} {
		if strings.Contains(gate, obsolete) {
			t.Errorf("Gate 1 documents obsolete placeholder API %q", obsolete)
		}
	}
	for _, current := range []string{
		"site filesystem primitive interface",
		"imports, restores, and configuration updates are orchestrated by durable jobs and the root broker",
		"suspend and resume are not exposed by this interface",
	} {
		if !strings.Contains(gateNormalized, current) {
			t.Errorf("Gate 1 is missing current manager contract %q", current)
		}
	}
}
