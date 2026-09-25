package main

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

func TestActivatePipelineReleaseHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&App{}).activatePipelineRelease(ctx, "demo", "/tmp/site", "/tmp/site/public", "/tmp/release")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("activatePipelineRelease error = %v, want context.Canceled", err)
	}
}

// TestPipelineBuildArgsMatchesRunnerCtlContract asserts that pipelineBuildArgs
// produces exactly the positional argument list the stepanel-runnerctl helper
// requires. The helper enforces `$# -eq 10 && $1 == "build"` at
// deploy/integrations/stepanel-runnerctl:4; any drift here breaks every
// release-pipeline build with the helper's usage error, which no other test
// catches because the helper isn't invoked from unit tests.
func TestPipelineBuildArgsMatchesRunnerCtlContract(t *testing.T) {
	app := &App{
		Config: Config{RunnerNetworkMode: "none", RunnerMaxImageBytes: 5 << 30},
		Resources: &ResourceStore{values: map[string]ResourceProfile{
			"demo": {Site: "demo", CPUPercent: 200, CPUWeight: 100, MemoryHighMB: 900, MemoryMB: 1024, IOWeight: 100, TasksMax: 256, PHPWorkers: 16},
		}},
	}
	args := app.pipelineBuildArgs("demo", "img@sha256:cafe", "/var/www/sites/demo/public", "/tmp/build.sh")
	if len(args) != 10 {
		t.Fatalf("pipelineBuildArgs returned %d args, helper requires exactly 10: %v", len(args), args)
	}
	want := []string{"build", "demo", "img@sha256:cafe", "/var/www/sites/demo/public", "/tmp/build.sh", strconv.Itoa(200), strconv.Itoa(1024), strconv.Itoa(256), "none", strconv.FormatInt(5<<30, 10)}
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
		app := &App{Config: Config{RunnerNetworkMode: mode, RunnerMaxImageBytes: 5 << 30}, Resources: &ResourceStore{values: map[string]ResourceProfile{}}}
		args := app.pipelineBuildArgs("s", "img@sha256:0", "/r", "/t/s.sh")
		if len(args) != 10 {
			t.Fatalf("expected 10 args, got %d", len(args))
		}
		if args[8] != mode {
			t.Errorf("NETWORK_MODE = %q, want %q", args[8], mode)
		}
	}
}

func TestPipelineBuildArgsUsesSafeImageLimitWhenConfigIsZero(t *testing.T) {
	app := &App{Config: Config{RunnerNetworkMode: "none"}}
	args := app.pipelineBuildArgs("site", "image@sha256:0", "/root", "/script")
	if args[9] != strconv.FormatInt(defaultRunnerMaxImageBytes, 10) {
		t.Fatalf("image limit = %q, want %d", args[9], defaultRunnerMaxImageBytes)
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
