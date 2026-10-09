package stepanel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	setupguide "github.com/cyberducttape/StePanel/internal/setup"
)

// loadEnvFileHarness runs install.sh's load_env_file against path with stat
// stubbed to report the given owner and mode, then prints the named
// variables one per line.
func loadEnvFileHarness(t *testing.T, path, owner, mode, visibility string, names []string) (string, error) {
	t.Helper()
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	start := strings.Index(text, "load_env_file() {")
	end := strings.Index(text[start:], "\n}\n")
	if start < 0 || end < 0 {
		t.Fatal("load_env_file not found in install.sh")
	}
	function := text[start : start+end+3]
	var harness strings.Builder
	harness.WriteString("set -Eeuo pipefail\n")
	harness.WriteString(`stat() { if [[ $2 == %u ]]; then echo "` + owner + `"; else echo "` + mode + `"; fi; }` + "\n")
	harness.WriteString(function)
	harness.WriteString(`load_env_file "$1" ` + visibility + "\n")
	for _, name := range names {
		harness.WriteString(`printf '%s\n' "${` + name + `}"` + "\n")
	}
	cmd := exec.Command("bash", "-c", harness.String(), "harness", path)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func TestInstallerReadsSetupSettingsExactly(t *testing.T) {
	values := [][2]string{
		{"STEPANEL_PANEL_HOSTNAME", "panel.example.org"},
		{"STEPANEL_ADMIN_PASSWORD_HASH", `$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA`},
		{"STEPANEL_OFFSITE_TARGET", `b2:bucket/path with "quotes" and \back\slashes`},
		{"STEPANEL_FAIL2BAN_IGNORE_IP", "198.51.100.0/24 2001:db8::1"},
		{"RCLONE_CONFIG", "/root/.config/rclone/rclone.conf"},
	}
	data, err := setupguide.EncodeEnvFile(values)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "answers.env")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(values))
	for i, pair := range values {
		names[i] = pair[0]
	}
	output, err := loadEnvFileHarness(t, path, "0", "600", "private", names)
	if err != nil {
		t.Fatalf("installer rejected setup output: %v\n%s", err, output)
	}
	got := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	for i, pair := range values {
		if i >= len(got) || got[i] != pair[1] {
			t.Errorf("%s read back as %q, want %q", pair[0], got[i], pair[1])
		}
	}
}

func TestInstallerRejectsUnsafeSettingsFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "answers.env")
	if err := os.WriteFile(path, []byte("STEPANEL_A=\"1\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := loadEnvFileHarness(t, path, "0", "640", "private", []string{"STEPANEL_A"}); err == nil || !strings.Contains(output, "chmod 600") {
		t.Fatalf("group-readable secrets accepted: %v %s", err, output)
	}
	if output, err := loadEnvFileHarness(t, path, "1000", "600", "private", []string{"STEPANEL_A"}); err == nil || !strings.Contains(output, "root-owned") {
		t.Fatalf("non-root file accepted: %v %s", err, output)
	}
	if err := os.WriteFile(path, []byte("PATH=\"/tmp\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := loadEnvFileHarness(t, path, "0", "600", "private", nil); err == nil || !strings.Contains(output, "unsupported entry") {
		t.Fatalf("non-StePanel variable accepted: %v %s", err, output)
	}
}

// A fresh install must still require consent before stopping another web
// server, while an upgrade of an existing installation that keeps the same
// web server must not be refused (the N-1 upgrade smoke).
func TestInstallerTakeoverConsentAndUpgrade(t *testing.T) {
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	for _, required := range []string{
		`preflight_blockers+=("rerun with --take-over-host to stop and disable: ${PREEXISTING_WEB_SERVICES[*]}")`,
		`grep -Fxq "STEPANEL_WEBSERVER=\"$WEB_SERVER\"" "$ENV_FILE"; then`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("install.sh is missing %q", required)
		}
	}
	upgrade := strings.Index(text, `grep -Fxq "STEPANEL_WEBSERVER=\"$WEB_SERVER\"" "$ENV_FILE"; then`)
	guard := strings.Index(text, `preflight_blockers+=("rerun with --take-over-host`)
	if upgrade < 0 || guard < 0 || upgrade > guard {
		t.Fatal("the upgrade consent check must run before the takeover guard")
	}
}
