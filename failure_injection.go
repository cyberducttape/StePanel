package stepanel

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// failureInjection implements the deterministic fault hook used by recovery
// tests and disposable-host drills. The hook is inert unless STEPANEL_FAIL_AT
// is set. Its value is operation:point, for example restore:commit or
// deploy:init. A wildcard point (operation:*) fails at every instrumented
// point for that operation.
func failureInjection(operation, point string) error {
	spec := strings.TrimSpace(os.Getenv("STEPANEL_FAIL_AT"))
	if spec == "" {
		return nil
	}
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("invalid STEPANEL_FAIL_AT %q: expected operation:point", spec)
	}
	if !strings.EqualFold(strings.TrimSpace(parts[0]), operation) {
		return nil
	}
	wantedPoint := strings.TrimSpace(parts[1])
	if wantedPoint != "*" && !strings.EqualFold(wantedPoint, point) {
		return nil
	}
	return fmt.Errorf("failure injection at %s:%s", operation, point)
}

// processKillInjection is intentionally separate from failureInjection: a
// SIGKILL cannot be returned to the caller for cleanup, so callers use it only
// at boundaries where startup recovery owns the abandoned state. It is inert
// unless explicitly enabled by a crash-recovery test.
func processKillInjection(operation, point string) {
	processPauseInjection(operation, point)
	if injectionPointMatches("STEPANEL_KILL_AT", operation, point) {
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
}

func injectionPointMatches(variable, operation, point string) bool {
	parts := strings.SplitN(strings.TrimSpace(os.Getenv(variable)), ":", 2)
	return len(parts) == 2 && strings.EqualFold(strings.TrimSpace(parts[0]), operation) && strings.EqualFold(strings.TrimSpace(parts[1]), point)
}

// defaultPauseMarkerDir is inside the services' ReadWritePaths: their
// ProtectSystem=strict sandbox makes /run read-only. Markers are never read
// by StePanel; the drill removes them.
const defaultPauseMarkerDir = "/var/lib/ste-panel/pause-markers"

// pauseForever blocks the paused operation; tests replace it.
var pauseForever = func() { select {} }

// processPauseInjection holds an operation at a kill boundary so a lab
// harness can kill the whole guest there, which a process kill cannot model.
// STEPANEL_PAUSE_AT uses the STEPANEL_KILL_AT syntax and is honored only with
// STEPANEL_UNSAFE_LAB=1. On a match it writes and syncs the marker
// <STEPANEL_PAUSE_MARKER_DIR>/<operation>-<point> and then blocks; the
// operation's lease keeps renewing, so the job stays running until the guest
// dies. See deploy/lab/abrupt-loss-operation-drill.sh.
func processPauseInjection(operation, point string) {
	if os.Getenv("STEPANEL_UNSAFE_LAB") != "1" || !injectionPointMatches("STEPANEL_PAUSE_AT", operation, point) {
		return
	}
	dir := strings.TrimSpace(os.Getenv("STEPANEL_PAUSE_MARKER_DIR"))
	if dir == "" {
		dir = defaultPauseMarkerDir
	}
	if err := writePauseMarker(dir, strings.ToLower(operation)+"-"+strings.ToLower(point)); err != nil {
		log.Printf("pause injection at %s:%s could not write its marker: %v", operation, point, err)
	}
	log.Printf("pause injection holding %s:%s until the process is killed", operation, point)
	pauseForever()
}

func writePauseMarker(dir, name string) error {
	if !filepath.IsLocal(name) || filepath.Base(name) != name {
		return fmt.Errorf("invalid pause marker name %q", name)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, name), []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0600)
}

func transactionFailureOperation(kind string) string {
	kind = strings.ToLower(kind)
	switch {
	case strings.Contains(kind, "backup"):
		return "backup"
	case strings.Contains(kind, "restore"), strings.Contains(kind, "import"), strings.Contains(kind, "cpmove"), strings.Contains(kind, "wordpress"):
		return "restore"
	case strings.Contains(kind, "deploy"), strings.Contains(kind, "git"), strings.Contains(kind, "staging"):
		return "deploy"
	default:
		return kind
	}
}
