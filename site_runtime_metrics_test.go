package stepanel

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
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

func TestCPUQuotaUtilizationUsesCounterDeltaAndRejectsInvalidSamples(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	previous := siteRuntimeMetricSample{
		resourceAvailable: true, cpuUsageNSec: 1_000_000_000,
		cpuQuotaPerSecUSec: 1_000_000, collectedAt: start,
	}
	current := previous
	current.collectedAt = start.Add(time.Minute)
	current.cpuUsageNSec += 54_000_000_000 // 90% of a 1 CPU quota for 60s.
	got, ok := cpuQuotaUtilization(previous, current)
	if !ok || got != 90 {
		t.Fatalf("cpuQuotaUtilization() = (%v, %v), want (90, true)", got, ok)
	}

	tests := []struct {
		name   string
		mutate func(*siteRuntimeMetricSample, *siteRuntimeMetricSample)
	}{
		{"unavailable probe", func(p, c *siteRuntimeMetricSample) { c.resourceAvailable = false }},
		{"unlimited quota", func(p, c *siteRuntimeMetricSample) { c.cpuQuotaPerSecUSec = 0 }},
		{"quota changed", func(p, c *siteRuntimeMetricSample) { c.cpuQuotaPerSecUSec /= 2 }},
		{"counter reset", func(p, c *siteRuntimeMetricSample) { c.cpuUsageNSec = p.cpuUsageNSec - 1 }},
		{"stale sample", func(p, c *siteRuntimeMetricSample) { c.collectedAt = p.collectedAt.Add(6 * siteRuntimeMetricsInterval) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, c := previous, current
			test.mutate(&p, &c)
			if got, ok := cpuQuotaUtilization(p, c); ok {
				t.Fatalf("invalid samples produced utilization %v", got)
			}
		})
	}
}
