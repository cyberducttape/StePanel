package stepanel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// recoveryProofTrustedUID owns the proof executable and every directory above
// it. Tests point it at their own UID; production requires root.
var recoveryProofTrustedUID uint32

// validateRecoveryProofExecutable refuses a proof program the StePanel
// service account could replace. The proof runs as that account (see
// docs/SECURITY.md, "Recovery proof hooks"), so if the panel could rewrite
// the program or a directory above it, a compromised panel would also own
// every future proof result. The resolved executable and each ancestor must
// be owned by the trusted owner and not group- or world-writable (a
// trusted-owned sticky directory such as /tmp is accepted for ancestors).
func validateRecoveryProofExecutable(command string) (string, error) {
	if !filepath.IsAbs(command) || filepath.Clean(command) != command {
		return "", errors.New("STEPANEL_RECOVERY_PROOF_COMMAND must be a clean absolute path")
	}
	resolved, err := filepath.EvalSymlinks(command)
	if err != nil {
		return "", fmt.Errorf("resolve recovery proof command: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat recovery proof command: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("recovery proof command is not an executable regular file")
	}
	if err := checkRecoveryProofOwner(resolved, info, false); err != nil {
		return "", err
	}
	for dir := filepath.Dir(resolved); ; dir = filepath.Dir(dir) {
		dirInfo, err := os.Stat(dir)
		if err != nil {
			return "", fmt.Errorf("stat recovery proof command directory: %w", err)
		}
		if err := checkRecoveryProofOwner(dir, dirInfo, true); err != nil {
			return "", err
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return resolved, nil
}

func checkRecoveryProofOwner(path string, info os.FileInfo, directory bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read the owner of %s", path)
	}
	if stat.Uid != 0 && stat.Uid != recoveryProofTrustedUID {
		return fmt.Errorf("recovery proof command path %s must be owned by root so the StePanel service account cannot replace it", path)
	}
	if info.Mode().Perm()&0o022 != 0 && !(directory && info.Mode()&os.ModeSticky != 0) {
		return fmt.Errorf("recovery proof command path %s must not be group- or world-writable", path)
	}
	return nil
}

// runRecoveryProofCommand executes the operator-configured proof program as
// the unprivileged StePanel service identity. The panel is deliberately not
// allowed to run this extension as root, and the child receives only the
// recovery inputs it needs rather than secrets from the service environment.
func runRecoveryProofCommand(ctx context.Context, command, site, stage string) error {
	command = strings.TrimSpace(command)
	if command == "" || site == "" || stage == "" {
		return errors.New("recovery proof command and inputs are required")
	}
	if os.Geteuid() == 0 {
		return errors.New("application recovery proof must run under the unprivileged StePanel service account")
	}
	executable, err := validateRecoveryProofExecutable(command)
	if err != nil {
		return err
	}
	proof := exec.CommandContext(ctx, executable, site, stage)
	proof.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"LANG=C.UTF-8",
		"STEPANEL_RECOVERY_PROOF_SITE=" + site,
		"STEPANEL_RECOVERY_PROOF_STAGE=" + stage,
	}
	// Bounded capture prevents a proof executable from consuming unbounded
	// memory. Its output is intentionally discarded: it may contain restored
	// credentials or other customer data, and job errors are durable.
	_, err = runBoundedCommand(ctx, proof)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("application recovery proof command did not finish: %w", contextErr)
		}
		return errors.New("application recovery proof command failed")
	}
	return nil
}
