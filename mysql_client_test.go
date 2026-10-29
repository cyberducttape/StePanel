package stepanel

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewMySQLCommandKeepsCredentialsOutOfArgvAndEnvironment(t *testing.T) {
	t.Setenv("MYSQL_PWD", "inherited-secret")
	cfg := Config{
		DBHost: "db.example.test",
		DBUser: "site-user",
		// Match the supported managed-database password alphabet; quotes,
		// control characters, and surrounding whitespace are rejected at API
		// admission and differ across MySQL-compatible option parsers.
		DBPassword: "site-db-credential-2026!@#",
	}
	query := "CREATE USER 'site-user' IDENTIFIED BY 'sql-secret'"
	cmd, cleanup, err := newMySQLCommand(context.Background(), cfg, "--batch")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	joinedArgs := strings.Join(cmd.Args, "\x00")
	if strings.Contains(joinedArgs, cfg.DBPassword) || strings.Contains(joinedArgs, query) {
		t.Fatalf("secret material found in command arguments: %q", cmd.Args)
	}
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "MYSQL_PWD=") {
			t.Fatalf("MYSQL_PWD leaked to child environment: %q", entry)
		}
	}
	if len(cmd.Args) < 2 || !strings.HasPrefix(cmd.Args[1], "--defaults-extra-file=") {
		t.Fatalf("private option file is not the first client option: %q", cmd.Args)
	}
	optionPath := strings.TrimPrefix(cmd.Args[1], "--defaults-extra-file=")
	info, err := os.Stat(optionPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("option file mode = %04o, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(optionPath))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0700 {
		t.Fatalf("credential directory mode = %04o, want 0700", dirInfo.Mode().Perm())
	}
	content, err := os.ReadFile(optionPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "[client]\npassword=" + mysqlOptionValue(cfg.DBPassword) + "\n"
	if string(content) != want {
		t.Fatalf("option file content = %q, want %q", content, want)
	}
	if strings.Contains(string(content), "inherited-secret") || strings.Contains(string(content), query) {
		t.Fatalf("unrelated secret or SQL leaked into option file: %q", content)
	}
	if parser, err := exec.LookPath("my_print_defaults"); err == nil {
		parsed, parseErr := exec.Command(parser, "--defaults-extra-file="+optionPath, "client").Output()
		parsedOption := strings.TrimSpace(string(parsed))
		// MariaDB's my_print_defaults redacts password options as --***,
		// while other supported MySQL clients print the parsed value. The
		// exact option-file bytes are checked above; here verify that the
		// parser accepted a password option without requiring it to disclose it.
		if parseErr != nil || (parsedOption != "--password="+cfg.DBPassword && parsedOption != "--***" && parsedOption != "--password=***") {
			t.Fatalf("MySQL option-file parser rejected the password option (output %q, error %v)", parsed, parseErr)
		}
	}
}

func TestMySQLQueryUsesStdinAndCleansCredentialFile(t *testing.T) {
	t.Setenv("MYSQL_PWD", "inherited-secret")
	bin := t.TempDir()
	argsPath := filepath.Join(t.TempDir(), "args")
	stdinPath := filepath.Join(t.TempDir(), "stdin")
	client := filepath.Join(bin, "mysql")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_ARGS\"\nwhile IFS= read -r line; do printf '%s\\n' \"$line\" >> \"$CAPTURE_STDIN\"; printf '%s\\n' \"$line\"; done\nif [ -n \"$MYSQL_PWD\" ]; then printf present; else printf unset; fi > \"$CAPTURE_MYSQL_PWD\"\n"
	if err := os.WriteFile(client, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("CAPTURE_ARGS", argsPath)
	t.Setenv("CAPTURE_STDIN", stdinPath)
	pwdPath := filepath.Join(t.TempDir(), "mysql-pwd")
	t.Setenv("CAPTURE_MYSQL_PWD", pwdPath)

	query := "CREATE USER 'site' IDENTIFIED BY 'sensitive SQL value'"
	output, err := runMySQLQuery(context.Background(), Config{DBPassword: "database-secret"}, query, "--batch")
	if err != nil {
		t.Fatalf("runMySQLQuery() error: %v (output %q)", err, output)
	}
	if strings.Contains(string(output), query) {
		t.Fatal("SQL text leaked through captured client output")
	}
	gotArgs, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(gotArgs), query) || strings.Contains(string(gotArgs), "database-secret") {
		t.Fatalf("SQL or database password was passed in argv: %q", gotArgs)
	}
	gotStdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotStdin) != query+"\n" {
		t.Fatalf("stdin = %q, want query", gotStdin)
	}
	gotPasswordEnv, err := os.ReadFile(pwdPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotPasswordEnv) != "unset" {
		t.Fatalf("child MYSQL_PWD = %q, want unset", gotPasswordEnv)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(gotArgs)), "\n") {
		if strings.HasPrefix(line, "--defaults-extra-file=") {
			optionPath := strings.TrimPrefix(line, "--defaults-extra-file=")
			if _, err := os.Stat(optionPath); !os.IsNotExist(err) {
				t.Fatalf("credential file remains after command: stat error %v", err)
			}
		}
	}
}

func TestMySQLCommandRedactsKnownPasswordFromOutputAndError(t *testing.T) {
	const secret = "db-secret-unique"
	cmd := exec.Command("sh", "-c", "printf 'failed with "+secret+"' >&2; exit 7")
	output, err := runMySQLCommand(context.Background(), Config{DBPassword: secret}, cmd)
	if err == nil {
		t.Fatal("expected command failure")
	}
	if strings.Contains(string(output), secret) || strings.Contains(err.Error(), secret) {
		t.Fatal("database password leaked through command output or error")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("redacted error lost the process exit error: %T", err)
	}
	if cause := errors.Unwrap(err); cause == nil || strings.Contains(cause.Error(), secret) {
		t.Fatal("unwrapping the redacted error exposed the password")
	}
}
