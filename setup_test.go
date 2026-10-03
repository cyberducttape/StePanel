package main

import (
	"bytes"
	"encoding/base32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type scriptedSetupHost struct{ setupHost }

func (scriptedSetupHost) HashPassword(password string) (string, error) { return "hashed", nil }
func (scriptedSetupHost) VerifyTOTP(_, code string, _ time.Time) bool  { return code == "123456" }
func (scriptedSetupHost) Hostname() string                             { return "panel.example.org" }
func (scriptedSetupHost) Rclone() bool                                 { return true }
func (scriptedSetupHost) RcloneRemotes() ([]string, error)             { return []string{"b2:"}, nil }
func (scriptedSetupHost) RcloneConfigFile() (string, error) {
	return "/root/.config/rclone/rclone.conf", nil
}
func (scriptedSetupHost) ProbeOffsite(string) error { return nil }

// scriptedInput returns a file with one answer per line; non-terminal input
// is read line by line, secrets included.
func scriptedInput(t *testing.T, lines ...string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

var defaultSetupAnswers = []string{
	"", "", "", "", // hostname, web server, database, username
	"correct horse battery staple", "correct horse battery staple", // password twice
	"123456", "", "", "", // TOTP code, offsite target, skip extras, save
}

func TestSetupWritesPrivateInstallerSettings(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "answers.env")
	var out bytes.Buffer
	err := runSetup([]string{"--output", output}, scriptedInput(t, defaultSetupAnswers...), &out, scriptedSetupHost{}, filepath.Join(dir, "missing.env"))
	if err != nil {
		t.Fatalf("setup: %v\n%s", err, out.String())
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("settings mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`STEPANEL_PANEL_HOSTNAME="panel.example.org"`, `STEPANEL_WEBSERVER="caddy"`, `STEPANEL_ADMIN_PASSWORD_HASH="hashed"`, `STEPANEL_OFFSITE_TARGET="b2:stepanel-backups"`, `STEPANEL_BACKUP_ENCRYPTION_KEY="`, `RCLONE_CONFIG="/root/.config/rclone/rclone.conf"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("settings missing %s", want)
		}
	}
	if !strings.Contains(out.String(), "--dry-run") || strings.Contains(out.String(), "correct horse") {
		t.Fatalf("transcript:\n%s", out.String())
	}
}

func TestSetupRefusesInstalledHost(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "ste-panel.env")
	if err := os.WriteFile(existing, []byte("STEPANEL_X=\"1\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := runSetup([]string{"--output", filepath.Join(dir, "answers.env")}, scriptedInput(t, defaultSetupAnswers...), &bytes.Buffer{}, scriptedSetupHost{}, existing)
	if err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Fatalf("err = %v", err)
	}
}

func TestSetupDoesNotReplaceSettingsWithoutForce(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "answers.env")
	if err := os.WriteFile(output, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	err := runSetup([]string{"--output", output}, scriptedInput(t, defaultSetupAnswers...), &bytes.Buffer{}, scriptedSetupHost{}, filepath.Join(dir, "missing.env"))
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v", err)
	}
	if data, _ := os.ReadFile(output); string(data) != "keep" {
		t.Fatal("existing settings were replaced")
	}
	if err := runSetup([]string{"--output", output, "--force"}, scriptedInput(t, defaultSetupAnswers...), &bytes.Buffer{}, scriptedSetupHost{}, filepath.Join(dir, "missing.env")); err != nil {
		t.Fatal(err)
	}
}

func TestSetupHostVerifiesTOTPWithOneStepOfDrift(t *testing.T) {
	raw := bytes.Repeat([]byte{7}, 20)
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	now := time.Unix(1_800_000_000, 0)
	counter := uint64(now.Unix() / 30)
	host := setupHost{}
	for _, drift := range []int64{-1, 0, 1} {
		if !host.VerifyTOTP(secret, totpCode(raw, uint64(int64(counter)+drift)), now) {
			t.Errorf("code at drift %d rejected", drift)
		}
	}
	if host.VerifyTOTP(secret, totpCode(raw, counter+2), now) || host.VerifyTOTP(secret, totpCode(raw, counter-2), now) {
		t.Error("code two steps away accepted")
	}
	if host.VerifyTOTP("not base32!", "000000", now) {
		t.Error("invalid secret accepted")
	}
}
