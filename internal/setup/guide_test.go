package setup

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// script feeds lines and secrets in order, failing the test if the guide
// asks for more input than scripted.
type script struct {
	lines   []string
	secrets []string
}

func (s *script) ReadLine() (string, error) {
	if len(s.lines) == 0 {
		return "", io.EOF
	}
	line := s.lines[0]
	s.lines = s.lines[1:]
	return line, nil
}

func (s *script) ReadSecret() (string, error) {
	if len(s.secrets) == 0 {
		return "", io.EOF
	}
	secret := s.secrets[0]
	s.secrets = s.secrets[1:]
	return secret, nil
}

type fakeHost struct {
	validCode   string
	codeChecks  int
	rclone      bool
	remotes     []string
	probeErrors map[string]error
	probed      []string
	hashed      []string
	draws       byte
}

func (h *fakeHost) HashPassword(password string) (string, error) {
	h.hashed = append(h.hashed, password)
	return "hash-of-" + password, nil
}
func (h *fakeHost) VerifyTOTP(secret, code string, now time.Time) bool {
	h.codeChecks++
	return code == h.validCode && len(secret) == 32
}
func (h *fakeHost) Now() time.Time                    { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }
func (h *fakeHost) Hostname() string                  { return "panel.example.org" }
func (h *fakeHost) Rclone() bool                      { return h.rclone }
func (h *fakeHost) RcloneRemotes() ([]string, error)  { return h.remotes, nil }
func (h *fakeHost) RcloneConfigFile() (string, error) { return "/root/.config/rclone/rclone.conf", nil }
func (h *fakeHost) ProbeOffsite(target string) error {
	h.probed = append(h.probed, target)
	return h.probeErrors[target]
}
func (h *fakeHost) SSHClientIP() string { return "203.0.113.10" }
func (h *fakeHost) Random(n int) ([]byte, error) {
	h.draws++
	return bytes.Repeat([]byte{h.draws}, n), nil
}

func newHost() *fakeHost {
	return &fakeHost{validCode: "123456", rclone: true, remotes: []string{"b2:"}, probeErrors: map[string]error{}}
}

const password = "correct horse battery staple"

func run(t *testing.T, host *fakeHost, lines []string, secrets []string) (Answers, string, error) {
	t.Helper()
	var out bytes.Buffer
	answers, err := NewGuide(&script{lines: lines, secrets: secrets}, &out, host).Run()
	return answers, out.String(), err
}

func TestGuideAcceptsDefaults(t *testing.T) {
	host := newHost()
	answers, out, err := run(t, host,
		[]string{"", "", "", "", "123456", "", "", ""},
		[]string{password, password})
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if answers.PanelHostname != "panel.example.org" || answers.WebServer != "caddy" || answers.DBEngine != "mariadb" || answers.AdminUsername != "admin" {
		t.Fatalf("answers = %+v", answers)
	}
	if answers.AdminPasswordHash != "hash-of-"+password || len(answers.TOTPSecret) != 32 {
		t.Fatalf("credentials = %q %q", answers.AdminPasswordHash, answers.TOTPSecret)
	}
	if answers.OffsiteTarget != "b2:stepanel-backups" || host.probed[0] != "b2:stepanel-backups" || answers.RcloneConfig == "" {
		t.Fatalf("offsite = %q probed %v", answers.OffsiteTarget, host.probed)
	}
	if len(answers.Keys) != len(generatedKeys) {
		t.Fatalf("keys = %d", len(answers.Keys))
	}
	for name, key := range answers.Keys {
		if len(key) != 64 {
			t.Fatalf("%s has %d characters", name, len(key))
		}
	}
	// No secret may appear in the transcript.
	for _, secret := range []string{password, answers.AdminPasswordHash, answers.Keys["STEPANEL_BACKUP_ENCRYPTION_KEY"]} {
		if strings.Contains(out, secret) {
			t.Fatalf("transcript leaks %q", secret)
		}
	}
}

func TestGuideRepromptsInvalidAnswers(t *testing.T) {
	host := newHost()
	host.probeErrors["b2:bad"] = errors.New("access denied")
	answers, out, err := run(t, host,
		[]string{
			"localhost", "not a host", "Panel.Example.COM", // hostname: two rejections
			"9", "apache", "n", // web server: invalid choice, then apache without certificates
			"3",                // PostgreSQL by number
			"bad user!", "ops", // username
			"12345", "000000", "123456", // TOTP: malformed, wrong, right
			"s3:x", "b2:bad", "y", "b2:sites", // offsite: unknown remote, failing probe, retry, good
			"y", "y", "not-an-ip", "198.51.100.0/24 2001:db8::1", "y", "n", "y", // extras
			"y", // save
		},
		[]string{"short", "short", password, "different password!", password, password})
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if answers.PanelHostname != "panel.example.com" || answers.WebServer != "apache" || answers.InstallTLS || answers.DBEngine != "postgresql" || answers.AdminUsername != "ops" {
		t.Fatalf("answers = %+v", answers)
	}
	if answers.OffsiteTarget != "b2:sites" || !answers.Fail2ban || answers.Fail2banIgnoreIP != "198.51.100.0/24 2001:db8::1" || !answers.InstallNode || answers.InstallSecurity || !answers.InstallDBAdmin {
		t.Fatalf("answers = %+v", answers)
	}
	for _, want := range []string{"not one of the configured remotes", "access denied", "do not match", "does not match", "phpPgAdmin", "is not an IP address"} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript missing %q", want)
		}
	}
	if len(host.hashed) != 1 {
		t.Fatalf("hashed %d passwords, want only the confirmed one", len(host.hashed))
	}
}

func TestGuideExplainsClockAfterRepeatedCodeFailures(t *testing.T) {
	host := newHost()
	_, out, err := run(t, host,
		[]string{"", "", "", "", "111111", "222222", "333333", "123456", "", "", ""},
		[]string{password, password})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "check this server's clock") || host.codeChecks != 4 {
		t.Fatalf("checks = %d\n%s", host.codeChecks, out)
	}
}

func TestGuideStopsWithInstructionsWithoutRclone(t *testing.T) {
	host := newHost()
	host.rclone = false
	_, out, err := run(t, host, []string{"", "", "", "", "123456"}, []string{password, password})
	if !errors.Is(err, ErrCancelled) || !strings.Contains(out, "sudo rclone config") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	host = newHost()
	host.remotes = nil
	_, out, err = run(t, host, []string{"", "", "", "", "123456"}, []string{password, password})
	if !errors.Is(err, ErrCancelled) || !strings.Contains(out, "no storage remotes") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestGuideCanBeDeclined(t *testing.T) {
	_, _, err := run(t, newHost(), []string{"", "", "", "", "123456", "", "", "n"}, []string{password, password})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v", err)
	}
	_, _, err = run(t, newHost(), []string{""}, nil)
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("early end of input: err = %v", err)
	}
}

func TestEncodeEnvFileMatchesInstallerFormat(t *testing.T) {
	data, err := EncodeEnvFile([][2]string{{"STEPANEL_A", `plain`}, {"STEPANEL_B", `has "quotes" and \slash`}, {"RCLONE_CONFIG", "/root/rclone.conf"}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"STEPANEL_A=\"plain\"\n", `STEPANEL_B="has \"quotes\" and \\slash"` + "\n", "RCLONE_CONFIG=\"/root/rclone.conf\"\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("encoded file missing %q:\n%s", want, text)
		}
	}
	if _, err := EncodeEnvFile([][2]string{{"PATH", "/bin"}}); err == nil {
		t.Fatal("unsupported variable accepted")
	}
	if _, err := EncodeEnvFile([][2]string{{"STEPANEL_X", "a\nb"}}); err == nil {
		t.Fatal("line break accepted")
	}
}

func TestAnswersEnvIncludesEverythingTheInstallerRequires(t *testing.T) {
	answers, _, err := run(t, newHost(), []string{"", "", "", "", "123456", "", "", ""}, []string{password, password})
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, pair := range answers.Env() {
		env[pair[0]] = pair[1]
	}
	for _, required := range []string{
		"STEPANEL_PANEL_HOSTNAME", "STEPANEL_ADMIN_PASSWORD_HASH", "STEPANEL_ADMIN_TOTP_SECRET",
		"STEPANEL_ACCOUNT_KEY", "STEPANEL_ENVIRONMENT_KEY", "STEPANEL_BACKUP_SIGNING_KEY",
		"STEPANEL_BACKUP_ENCRYPTION_KEY", "STEPANEL_AUDIT_KEY", "STEPANEL_SESSION_SECRET",
		"STEPANEL_REQUIRE_OFFSITE_BACKUP", "STEPANEL_OFFSITE_TARGET", "STEPANEL_WEBSERVER", "STEPANEL_DB_ENGINE",
	} {
		if env[required] == "" {
			t.Errorf("installer setting %s is missing", required)
		}
	}
	seen := map[string]string{}
	for _, name := range generatedKeys {
		if other, dup := seen[env[name]]; dup {
			t.Errorf("%s repeats %s", name, other)
		}
		seen[env[name]] = name
	}
}
