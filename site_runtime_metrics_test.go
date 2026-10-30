package stepanel

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSiteRuntimeInventoryCombinesManagedSourcesAndRejectsInvalidNames(t *testing.T) {
	webRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "legacy"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", ".stepanel-stage-123"), 0700); err != nil {
		t.Fatal(err)
	}
	resources := &ResourceStore{values: map[string]ResourceProfile{
		"legacy":   {Site: "legacy", PHPWorkers: 4},
		"bad/name": {Site: "bad/name", PHPWorkers: 4},
	}}
	routes := &RouteStore{values: map[string]RouteDesired{
		"managed": {Name: "managed", Site: "managed"},
	}}
	app := &App{Config: Config{WebRoot: webRoot}, Resources: resources, Routes: routes}

	names, profiles, complete := app.siteRuntimeInventory()
	if !complete {
		t.Fatal("complete filesystem/resource/route inventory reported incomplete")
	}
	if !reflect.DeepEqual(names, []string{"legacy", "managed"}) {
		t.Fatalf("site inventory = %#v", names)
	}
	if profiles["legacy"].PHPWorkers != 4 {
		t.Fatalf("resource profile was not retained: %#v", profiles)
	}
}
