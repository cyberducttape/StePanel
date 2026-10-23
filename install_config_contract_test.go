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

func TestInstallerPreservesOperatorRuntimeSettingsAcrossUpgrade(t *testing.T) {
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	loadStart := strings.Index(text, "load_env_file() {")
	if loadStart < 0 {
		t.Fatal("load_env_file function not found")
	}
	loadEnd := strings.Index(text[loadStart:], "\n}\n")
	arrayStart := strings.Index(text, "PRESERVED_RUNTIME_SETTINGS=(")
	if arrayStart < 0 {
		t.Fatal("PRESERVED_RUNTIME_SETTINGS allowlist not found")
	}
	arrayEnd := strings.Index(text[arrayStart:], "\n)")
	writerStart := strings.Index(text, "write_env() {")
	if writerStart < 0 {
		t.Fatal("write_env function not found")
	}
	writerEnd := strings.Index(text[writerStart:], "\n}\n")
	preserveStart := strings.Index(text, "write_preserved_runtime_settings() {")
	if preserveStart < 0 {
		t.Fatal("write_preserved_runtime_settings function not found")
	}
	preserveEnd := strings.Index(text[preserveStart:], "\n}\n")
	if loadEnd < 0 || arrayEnd < 0 || writerEnd < 0 || preserveEnd < 0 {
		t.Fatal("installer runtime environment helpers not found")
	}
	loader := text[loadStart : loadStart+loadEnd+3]
	allowlist := text[arrayStart : arrayStart+arrayEnd+3]
	writer := text[writerStart : writerStart+writerEnd+3]
	preserve := text[preserveStart : preserveStart+preserveEnd+3]
	previousValues := [][2]string{
		{"STEPANEL_ACCOUNT_STATE", "/srv/stepanel/state/accounts.json"},
		{"STEPANEL_ALERT_WEBHOOK_URL", "https://alerts.example.net/hooks/stepanel"},
		{"STEPANEL_ALERT_WEBHOOK_SECRET", `secret with "quotes" and \\slashes`},
		{"STEPANEL_ALERT_EMAIL_TO", "oncall@example.net"},
		{"STEPANEL_ALERT_SMTP_PASSWORD", `smtp "password" with \\slashes`},
		{"STEPANEL_CLOUD_PROVIDER", "linode"},
		{"STEPANEL_DB_DATA_DIR", "/srv/mysql/data"},
		{"STEPANEL_GIT_WEBHOOK_SECRET", `legacy "hook" secret with \\slashes`},
		{"STEPANEL_OFFSITE_TRANSFER_TIMEOUT", "72h"},
		{"STEPANEL_RECOVERY_PROOF_COMMAND", `/usr/local/bin/prove --mode "strict"`},
		{"STEPANEL_SSH_SERVERS", "prod-a,prod-b"},
		{"STEPANEL_TRUSTED_PROXY_CIDRS", "198.51.100.0/24,2001:db8::/32"},
		{"STEPANEL_WORDPRESS_RECOVERY_PROOF", "always"},
	}
	for _, pair := range previousValues {
		if strings.Contains(text, "write_env "+pair[0]+" ") {
			t.Errorf("%s is written separately and would be duplicated by the preservation allowlist", pair[0])
		}
	}
	previousEnv, err := setupguide.EncodeEnvFile(previousValues)
	if err != nil {
		t.Fatal(err)
	}
	oldEnv := filepath.Join(t.TempDir(), "stepanel.env")
	if err := os.WriteFile(oldEnv, previousEnv, 0600); err != nil {
		t.Fatal(err)
	}
	envStart := strings.Index(text, `env_tmp=$(mktemp /etc/ste-panel.env.XXXXXX)`)
	if envStart < 0 {
		t.Fatal("installer environment-file generation block not found")
	}
	envEnd := strings.Index(text[envStart:], `chmod 0600 "$env_tmp"`)
	if envEnd < 0 || !strings.Contains(text[envStart:envStart+envEnd], "write_preserved_runtime_settings") {
		t.Fatal("installer does not include preserved runtime settings in the generated environment file")
	}
	var harness strings.Builder
	harness.WriteString("set -Eeuo pipefail\n")
	harness.WriteString(`stat() { if [[ $2 == %u ]]; then echo "0"; else echo "600"; fi; }` + "\n")
	harness.WriteString(loader)
	harness.WriteString(writer)
	harness.WriteString(allowlist)
	harness.WriteString(preserve)
	harness.WriteString(`load_env_file "$1"` + "\n")
	harness.WriteString("write_preserved_runtime_settings\n")
	cmd := exec.Command("bash", "-c", harness.String(), "harness", oldEnv)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "STEPANEL_FAIL_AT=should-not-persist", "STEPANEL_ALERT_WEBHOOK_URL=https://alerts.override.example/hook"}
	encoded, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("preserve runtime environment: %v\n%s", err, encoded)
	}
	seenKeys := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(encoded)), "\n") {
		key, _, ok := strings.Cut(line, "=")
		if !ok || seenKeys[key] {
			t.Fatalf("preserved environment contains a malformed or duplicate key %q:\n%s", key, encoded)
		}
		seenKeys[key] = true
	}
	path := filepath.Join(t.TempDir(), "preserved.env")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	expected := append([][2]string(nil), previousValues...)
	for i := range expected {
		if expected[i][0] == "STEPANEL_ALERT_WEBHOOK_URL" {
			expected[i][1] = "https://alerts.override.example/hook"
		}
	}
	names := make([]string, 0, len(expected))
	for _, pair := range expected {
		names = append(names, pair[0])
	}
	decoded, err := loadEnvFileHarness(t, path, "0", "600", "private", names)
	if err != nil {
		t.Fatalf("installer rejected preserved runtime settings: %v\n%s", err, decoded)
	}
	got := strings.Split(strings.TrimSuffix(decoded, "\n"), "\n")
	for i, pair := range expected {
		gotValue := "<missing>"
		if i < len(got) {
			gotValue = got[i]
		}
		if gotValue != pair[1] {
			t.Errorf("%s preserved as %q, want %q", pair[0], gotValue, pair[1])
		}
	}
	if strings.Contains(string(encoded), "STEPANEL_FAIL_AT") {
		t.Fatal("failure-injection settings must not persist across install")
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

func TestInstallerTrustsCaddyInternalCAAfterStartingWebServer(t *testing.T) {
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	serviceStart := strings.Index(text, `systemctl enable --now "$APACHE_SERVICE" "$DB_SERVICE"`)
	trust := strings.Index(text, `caddy trust --config /etc/caddy/Caddyfile`)
	fallback := strings.Index(text, `CADDY_LOCAL_CA=/var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt`)
	if serviceStart < 0 || trust < 0 || trust < serviceStart || fallback < trust {
		t.Fatal("installer must establish Caddy internal-CA trust after starting the web server")
	}
	caddyGuard := strings.LastIndex(text[:trust], `if [[ "$WEB_SERVER" == "caddy" ]]`)
	if caddyGuard < serviceStart {
		t.Fatal("Caddy trust installation must be restricted to Caddy hosts")
	}
	for _, required := range []string{"update-ca-certificates", "update-ca-trust extract", "openssl x509 -in \"$CADDY_LOCAL_CA\" -noout -checkend 0"} {
		if !strings.Contains(text[fallback:], required) {
			t.Errorf("Caddy trust fallback is missing %q", required)
		}
	}
}
