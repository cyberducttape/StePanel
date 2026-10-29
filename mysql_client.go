package stepanel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// newMySQLCommand creates a client command without placing the database
// password in argv or the process environment. MySQL reads its password from
// a private, short-lived option file. The caller must invoke cleanup after
// the command has completed.
func newMySQLCommand(ctx context.Context, cfg Config, args ...string) (*exec.Cmd, func() error, error) {
	cleanup := func() error { return nil }
	commandArgs := make([]string, 0, len(args)+len(mysqlArgs(cfg))+1)
	if cfg.DBPassword != "" {
		if strings.IndexByte(cfg.DBPassword, 0) >= 0 {
			return nil, cleanup, fmt.Errorf("database password contains an unsupported NUL byte")
		}
		dir, err := os.MkdirTemp("", "stepanel-mysql-client-")
		if err != nil {
			return nil, cleanup, fmt.Errorf("create private MySQL credential directory: %w", err)
		}
		cleanup = func() error { return os.RemoveAll(dir) }
		optionPath := filepath.Join(dir, "client.cnf")
		file, err := os.OpenFile(optionPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			cleanupErr := cleanup()
			return nil, func() error { return nil }, fmt.Errorf("create private MySQL option file: %w", errors.Join(err, cleanupErr))
		}
		content := "[client]\npassword=" + mysqlOptionValue(cfg.DBPassword) + "\n"
		written, writeErr := io.WriteString(file, content)
		if writeErr == nil && written != len(content) {
			writeErr = io.ErrShortWrite
		}
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			cleanupErr := cleanup()
			return nil, func() error { return nil }, fmt.Errorf("write private MySQL option file: %w", errors.Join(writeErr, closeErr, cleanupErr))
		}
		commandArgs = append(commandArgs, "--defaults-extra-file="+optionPath)
	}
	commandArgs = append(commandArgs, mysqlArgs(cfg)...)
	commandArgs = append(commandArgs, args...)
	cmd := exec.CommandContext(ctx, mysqlClient(), commandArgs...)
	if cfg.DBPassword != "" {
		cmd.Env = withoutMySQLPassword(os.Environ())
	}
	return cmd, cleanup, nil
}

func mysqlOptionValue(value string) string {
	escaped := strings.NewReplacer(
		"\\", "\\\\",
		"\"", "\\\"",
		"\n", "\\n",
		"\r", "\\r",
		"\t", "\\t",
	).Replace(value)
	return "\"" + escaped + "\""
}

func withoutMySQLPassword(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, "MYSQL_PWD=") {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func runMySQLQuery(ctx context.Context, cfg Config, query string, args ...string) ([]byte, error) {
	cmd, cleanup, err := newMySQLCommand(ctx, cfg, args...)
	if err != nil {
		return nil, err
	}
	cmd.Stdin = strings.NewReader(query + "\n")
	output, runErr := runMySQLCommand(ctx, cfg, cmd)
	if query != "" {
		output = redactBytes(output, []byte(query))
		runErr = redactError(runErr, query)
	}
	if cleanupErr := cleanup(); cleanupErr != nil {
		return output, errors.Join(runErr, fmt.Errorf("remove private MySQL credential directory: %w", cleanupErr))
	}
	return output, runErr
}

func runMySQLCommand(ctx context.Context, cfg Config, cmd *exec.Cmd) ([]byte, error) {
	output, err := runBoundedCommand(ctx, cmd)
	if cfg.DBPassword == "" {
		return output, err
	}
	secret := []byte(cfg.DBPassword)
	return redactBytes(output, secret), redactError(err, cfg.DBPassword)
}

func redactBytes(output, secret []byte) []byte {
	if len(secret) == 0 {
		return output
	}
	return bytes.ReplaceAll(output, secret, bytes.Repeat([]byte("*"), len(secret)))
}

func redactError(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	redaction := strings.Repeat("*", len(secret))
	return redactedCommandError{
		cause:   safeCommandCause(err),
		message: strings.ReplaceAll(err.Error(), secret, redaction),
	}
}

type redactedCommandError struct {
	cause   error
	message string
}

func (e redactedCommandError) Error() string { return e.message }
func (e redactedCommandError) Unwrap() error { return e.cause }

func safeCommandCause(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr
	}
	return err
}
