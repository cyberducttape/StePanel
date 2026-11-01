package stepanel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

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
	proof := exec.CommandContext(ctx, command, site, stage)
	proof.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"LANG=C.UTF-8",
		"STEPANEL_RECOVERY_PROOF_SITE=" + site,
		"STEPANEL_RECOVERY_PROOF_STAGE=" + stage,
	}
	// Bounded capture prevents a proof executable from consuming unbounded
	// memory. Its output is intentionally discarded: it may contain restored
	// credentials or other customer data, and job errors are durable.
	_, err := runBoundedCommand(ctx, proof)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("application recovery proof command did not finish: %w", contextErr)
		}
		return errors.New("application recovery proof command failed")
	}
	return nil
}
