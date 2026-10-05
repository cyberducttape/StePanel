package rootbroker

import (
	"strings"
	"testing"
)

func TestTypedRequestFixedCommandArgumentsAreValid(t *testing.T) {
	v := NewValidator("/var/www")
	public := "/var/www/sites/demo/public"
	release := "/var/www/sites/demo/.stepanel-release-20260101-abc"
	image := "docker.io/library/node:20.12@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, action string
		args         []string
	}{
		{"appctl", "resource-apply", []string{"demo", "100", "100", "900", "1024", "100", "256"}},
		{"appctl", "resource-apply", []string{"demo", "100", "100", "900", "1024", "100", "256", ""}},
		{"appctl", "resource-apply", []string{"demo", "100", "100", "900", "1024", "100", "256", "alice"}},
		{"appctl", "account-resource-apply", []string{"alice", "100", "100", "900", "1024", "100", "256"}},
		{"runnerctl", "build", []string{"demo", image, release, "/var/lib/ste-panel/apps/pipeline-123.sh", "100", "1024", "256", "none", "1073741824"}},
		{"runnerctl", "build", []string{"demo", image, public, "/var/lib/ste-panel/apps/runner-9.sh", "100", "1024", "256", "egress", "1073741824"}},
	} {
		if err := v.validateFixedCommandArgs(tc.name, tc.action, tc.args); err != nil {
			t.Errorf("%s/%s %q rejected: %v", tc.name, tc.action, tc.args, err)
		}
	}
}

func TestTypedRequestFixedCommandArgumentsRejectInvalidInputs(t *testing.T) {
	v := NewValidator("/var/www")
	for _, tc := range []struct {
		why, name, action string
		args              []string
	}{
		{"unsupported action", "appctl", "task-apply", []string{"demo"}},
		{"unsupported helper", "rootctl", "exec", []string{"id"}},
		{"extra argument", "appctl", "resource-status", []string{"demo", "--force"}},
		{"missing argument", "appctl", "resource-status", nil},
		{"invalid account resource", "appctl", "account-resource-apply", []string{"alice", "100", "100", "900", "1024", "100", "-1"}},
		{"invalid site resource", "appctl", "resource-apply", []string{"demo", "100", "100", "900", "1024", "100", "256", "bad account"}},
		{"unpinned image", "runnerctl", "build", []string{"demo", "node:latest", "/var/www/sites/demo/public", "/var/lib/ste-panel/apps/runner-1.sh", "100", "1024", "256", "none", "1"}},
		{"arbitrary script", "runnerctl", "build", []string{"demo", "n@sha256:" + strings.Repeat("a", 64), "/var/www/sites/demo/public", "/etc/shadow", "100", "1024", "256", "none", "1"}},
		{"script outside application directory", "runnerctl", "build", []string{"demo", "n@sha256:" + strings.Repeat("a", 64), "/var/www/sites/demo/public", "/etc/runner-1.sh", "100", "1024", "256", "none", "1"}},
	} {
		if err := v.validateFixedCommandArgs(tc.name, tc.action, tc.args); err == nil {
			t.Errorf("%s: %s/%s %q accepted", tc.why, tc.name, tc.action, tc.args)
		}
	}
}
