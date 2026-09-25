package main

import (
	"strconv"
	"testing"
)

// TestPipelineBuildArgsMatchesRunnerCtlContract asserts that pipelineBuildArgs
// produces exactly the positional argument list the stepanel-runnerctl helper
// requires. The helper enforces `$# -eq 9 && $1 == "build"` at
// deploy/integrations/stepanel-runnerctl:4; any drift here breaks every
// release-pipeline build with the helper's usage error, which no other test
// catches because the helper isn't invoked from unit tests.
func TestPipelineBuildArgsMatchesRunnerCtlContract(t *testing.T) {
	app := &App{
		Config: Config{RunnerNetworkMode: "none"},
		Resources: &ResourceStore{values: map[string]ResourceProfile{
			"demo": {Site: "demo", CPUPercent: 200, CPUWeight: 100, MemoryHighMB: 900, MemoryMB: 1024, IOWeight: 100, TasksMax: 256, PHPWorkers: 16},
		}},
	}
	args := app.pipelineBuildArgs("demo", "img@sha256:cafe", "/var/www/sites/demo/public", "/tmp/build.sh")
	if len(args) != 9 {
		t.Fatalf("pipelineBuildArgs returned %d args, helper requires exactly 9: %v", len(args), args)
	}
	want := []string{"build", "demo", "img@sha256:cafe", "/var/www/sites/demo/public", "/tmp/build.sh", strconv.Itoa(200), strconv.Itoa(1024), strconv.Itoa(256), "none"}
	for i, w := range want {
		if args[i] != w {
			t.Errorf("arg[%d] = %q, want %q", i, args[i], w)
		}
	}
}

// TestPipelineBuildArgsPassesNetworkMode is a targeted regression test for the
// missing-NETWORK_MODE bug that broke every deployment pipeline reaching
// runPipelineBuild. The helper accepts only "none" or "egress" as its ninth
// argument (see stepanel-runnerctl:6), and callers must supply one of them.
func TestPipelineBuildArgsPassesNetworkMode(t *testing.T) {
	for _, mode := range []string{"none", "egress"} {
		app := &App{Config: Config{RunnerNetworkMode: mode}, Resources: &ResourceStore{values: map[string]ResourceProfile{}}}
		args := app.pipelineBuildArgs("s", "img@sha256:0", "/r", "/t/s.sh")
		if len(args) != 9 {
			t.Fatalf("expected 9 args, got %d", len(args))
		}
		if args[8] != mode {
			t.Errorf("NETWORK_MODE = %q, want %q", args[8], mode)
		}
	}
}

func TestPipelineResourceLimitsUseSiteProfile(t *testing.T) {
	app := &App{Resources: &ResourceStore{values: map[string]ResourceProfile{
		"demo": {Site: "demo", CPUPercent: 200, CPUWeight: 100, MemoryHighMB: 900, MemoryMB: 1024, IOWeight: 100, TasksMax: 256, PHPWorkers: 16},
	}}}
	cpu, memory, tasks := app.pipelineResourceLimits("demo")
	if cpu != 200 || memory != 1024 || tasks != 256 {
		t.Fatalf("limits = %d%%, %d MB, %d tasks", cpu, memory, tasks)
	}
}

func TestPipelineResourceLimitsBoundLegacySite(t *testing.T) {
	app := &App{Resources: &ResourceStore{values: map[string]ResourceProfile{}}}
	cpu, memory, tasks := app.pipelineResourceLimits("legacy")
	if cpu != 100 || memory != 512 || tasks != 128 {
		t.Fatalf("legacy limits = %d%%, %d MB, %d tasks", cpu, memory, tasks)
	}
}
