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

func TestParseSystemdTimespanUSecReadsSystemctlShowFormat(t *testing.T) {
	for value, want := range map[string]uint64{
		"2s": 2_000_000, "500ms": 500_000, "1s 500ms": 1_500_000,
		"1.500000s": 1_500_000, "0.250000s": 250_000, "1.5ms": 1_500,
		"10ms": 10_000, "1min 30s": 90_000_000, "1000000": 1_000_000,
	} {
		got, ok := parseSystemdTimespanUSec(value)
		if !ok || got != want {
			t.Errorf("parseSystemdTimespanUSec(%q) = (%d, %v), want (%d, true)", value, got, ok, want)
		}
	}
	for _, value := range []string{"", "infinity", "[not set]", "2x", "ms", "-1s", "1.s", ".5s", "1..5s", "1.2.3s", "1.0000000001s"} {
		if got, ok := parseSystemdTimespanUSec(value); ok {
			t.Errorf("parseSystemdTimespanUSec(%q) = %d, want rejection", value, got)
		}
	}
}

func readSystemctlFixture(t *testing.T, name string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "systemctl", name))
	if err != nil {
		t.Fatal(err)
	}
	return parseResourceStatus(string(data))
}

func TestResourceObservationReadsRealSystemctlLimitedSlice(t *testing.T) {
	var sample siteRuntimeMetricSample
	applyResourceObservation(&sample, readSystemctlFixture(t, "systemd-259-limited.txt"))
	want := siteRuntimeMetricSample{
		resourceAvailable: true, memoryCurrent: 2981888, memoryMax: 268435456,
		tasksCurrent: 1, tasksMax: 64, cpuUsageNSec: 13857000, cpuQuotaPerSecUSec: 1_500_000,
	}
	if !reflect.DeepEqual(sample, want) {
		t.Fatalf("observation = %+v, want %+v", sample, want)
	}

	// A real quota must yield a utilization once a second sample arrives.
	start := time.Unix(1_800_000_000, 0)
	previous, current := sample, sample
	previous.collectedAt, current.collectedAt = start, start.Add(time.Minute)
	current.cpuUsageNSec += 45_000_000_000 // 50% of a 1.5 CPU quota for 60s.
	got, ok := cpuQuotaUtilization(previous, current)
	if !ok || got != 50 {
		t.Fatalf("cpuQuotaUtilization() = (%v, %v), want (50, true)", got, ok)
	}
}

func TestResourceObservationTreatsRealUnlimitedSliceAsNoLimit(t *testing.T) {
	var sample siteRuntimeMetricSample
	applyResourceObservation(&sample, readSystemctlFixture(t, "systemd-259-unlimited.txt"))
	if !sample.resourceAvailable || sample.memoryMax != 0 || sample.tasksMax != 0 || sample.cpuQuotaPerSecUSec != 0 {
		t.Fatalf("unlimited slice produced limits: %+v", sample)
	}
	if sample.memoryCurrent != 12355264512 || sample.tasksCurrent != 2237 {
		t.Fatalf("unlimited slice lost current usage: %+v", sample)
	}
}
