package stepanel

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigDriftMetricsAndHealthReportTheDriftCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status")
	previous := configDriftStatusPath
	configDriftStatusPath = path
	t.Cleanup(func() { configDriftStatusPath = previous })

	var out bytes.Buffer
	writeConfigDriftMetrics(&out, path)
	if !strings.Contains(out.String(), "stepanel_config_drift_check_up 0\n") || strings.Contains(out.String(), "stepanel_config_drift_files") {
		t.Fatalf("metrics without a result:\n%s", out.String())
	}
	checks := operationalChecks(Config{Production: true}, nil)
	if check := checks["config_drift"]; check.Ready || !strings.Contains(check.Detail, "has not run") {
		t.Fatalf("config_drift without a result = %+v", check)
	}

	now := time.Now().Unix()
	status := fmt.Sprintf("format=stepanel-config-drift-v1\nchecked_unix=%d\nmanifest=present\nmanifest_unix=%d\nfiles=30\ndrifted=1\ndrift\t/etc/sudoers.d/stepanel\tcontent\n", now, now)
	if err := os.WriteFile(path, []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	writeConfigDriftMetrics(&out, path)
	for _, want := range []string{"stepanel_config_drift_check_up 1\n", "stepanel_config_drift_files 1\n", fmt.Sprintf("stepanel_config_drift_last_check_timestamp_seconds %d\n", now)} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("metrics missing %q:\n%s", want, out.String())
		}
	}
	checks = operationalChecks(Config{Production: true}, nil)
	if check := checks["config_drift"]; check.Ready || !strings.Contains(check.Detail, "/etc/sudoers.d/stepanel (content)") {
		t.Fatalf("config_drift with drift = %+v", check)
	}
	// Development hosts have no drift timer, so the check is production-only.
	if _, ok := operationalChecks(Config{}, nil)["config_drift"]; ok {
		t.Fatal("config_drift was reported outside production")
	}
}
