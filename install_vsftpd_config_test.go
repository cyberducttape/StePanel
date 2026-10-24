package stepanel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runVSFTPDConfigValidator(t *testing.T, fakeBody string) (string, error, string) {
	t.Helper()
	dir := t.TempDir()
	helper := filepath.Join("deploy", "lib", "vsftpd-config-validation.sh")
	config := filepath.Join(dir, "vsftpd.conf")
	if err := os.WriteFile(config, []byte("listen=YES\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "vsftpd")
	fake := "#!/bin/sh\n" + fakeBody
	if err := os.WriteFile(binary, []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "args")
	cmd := exec.Command("bash", "-c", `set -euo pipefail
. "$1"
stepanel_validate_vsftpd_config "$2" "$3"
`, "bash", helper, binary, config)
	cmd.Env = append(os.Environ(), "VSFTPD_TEST_ARGS="+argsFile)
	output, err := cmd.CombinedOutput()
	return string(output), err, argsFile
}

func TestVSFTPDConfigValidationUsesBoundedLoopbackListener(t *testing.T) {
	output, err, argsFile := runVSFTPDConfigValidator(t, `printf '%s\n' "$@" > "$VSFTPD_TEST_ARGS"
exec sleep 30
`)
	if err != nil {
		t.Fatalf("valid foreground listener should time out as expected: %v: %s", err, output)
	}

	// The config must be loaded before these overrides, keeping the probe
	// bounded, loopback-only, and independent of the production FTP port.
	argsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(argsBytes)), "\n")
	if len(args) != 6 || args[0] == "" || args[1] != "-olisten=YES" || args[2] != "-olisten_ipv6=NO" || args[3] != "-obackground=NO" || args[4] != "-olisten_address=127.0.0.1" || !strings.HasPrefix(args[5], "-olisten_port=") {
		t.Fatalf("unexpected vsftpd probe arguments: %#v", args)
	}
}

func TestVSFTPDConfigValidationRejectsEarlyExit(t *testing.T) {
	for name, body := range map[string]string{
		"config error": `echo 'invalid directive' >&2
exit 2
`,
		"unexpected clean exit": `exit 0
`,
	} {
		t.Run(name, func(t *testing.T) {
			output, err, _ := runVSFTPDConfigValidator(t, body)
			if err == nil {
				t.Fatalf("validator accepted an early process exit; output=%q", output)
			}
			if name == "config error" && !strings.Contains(output, "invalid directive") {
				t.Fatalf("expected vsftpd diagnostic, got %q", output)
			}
		})
	}
}
