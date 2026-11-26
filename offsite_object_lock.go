package stepanel

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// offsiteObjectLockProofInterval is how long a passing Object Lock proof
// stands. Each proof leaves one small object version that the bucket retains
// for its default retention, so the proof runs rarely; a failing proof is
// retried at the ordinary probe interval.
const offsiteObjectLockProofInterval = 6 * time.Hour

var errOffsiteObjectLockProofPending = errors.New("offsite Object Lock proof is in progress")

// probeOffsiteObjectLock proves that the offsite target keeps object versions
// that a client tries to delete permanently.
var probeOffsiteObjectLock = func(target string) error {
	rclone, err := exec.LookPath("rclone")
	if err != nil {
		return errOffsiteToolMissing
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return proveOffsiteObjectLockWithRunner(ctx, target, rclone, func(runCtx context.Context, executable string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(runCtx, executable, args...)
		cmd.Env = cloudCommandEnv()
		return runBoundedCommand(runCtx, cmd)
	})
}

// proveOffsiteObjectLockWithRunner writes a probe object, deletes it (which
// on a versioned bucket only hides it), and then tries to delete the hidden
// version permanently with the same credentials the panel uses. The target
// passes only if that deletion is refused and the version still reads back
// intact, so a compromised panel cannot destroy backups it has written. Any
// unexpected result fails the proof: it never vouches for a bucket it could
// not test.
func proveOffsiteObjectLockWithRunner(ctx context.Context, target, rclone string, run offsiteProbeRunner) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	dir := fmt.Sprintf("%s/.stepanel-health/lock-%x", strings.TrimRight(target, "/"), nonce[:])
	object := dir + "/probe"
	want := []byte(fmt.Sprintf("stepanel-offsite-object-lock-v1:%x", nonce[:]))
	work, err := os.MkdirTemp("", "stepanel-offsite-lock-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	local := work + "/probe"
	if err := os.WriteFile(local, want, 0o600); err != nil {
		return err
	}
	if output, err := run(ctx, rclone, "copyto", local, object); err != nil {
		return fmt.Errorf("Object Lock proof write failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if output, err := run(ctx, rclone, "deletefile", object); err != nil {
		return fmt.Errorf("Object Lock proof delete failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	hidden, err := offsiteProbeEntries(ctx, rclone, run, dir)
	if err != nil {
		return err
	}
	if len(hidden) != 1 || !strings.HasPrefix(hidden[0], "probe-v") {
		if len(hidden) == 0 {
			return errors.New("deleting an object removed it permanently: the bucket is not versioned; enable Object Lock with a default retention")
		}
		return fmt.Errorf("Object Lock proof expected one hidden version after deleting the probe, found %q", hidden)
	}
	if output, err := run(ctx, rclone, "backend", "cleanup-hidden", dir); err == nil {
		return fmt.Errorf("the panel's credentials permanently deleted an object version: enable Object Lock with a default retention in compliance mode (%s)", strings.TrimSpace(string(output)))
	}
	// cleanup-hidden also removes delete markers, which Object Lock does not
	// protect, so the retained version may now be current again under the
	// plain name. Either way exactly one copy must remain, intact.
	survivors, err := offsiteProbeEntries(ctx, rclone, run, dir)
	if err != nil {
		return err
	}
	if len(survivors) != 1 {
		if len(survivors) == 0 {
			return errors.New("an object version disappeared after a refused deletion: the bucket does not retain object versions")
		}
		return fmt.Errorf("Object Lock proof found %q after one write", survivors)
	}
	readback := work + "/readback"
	if output, err := run(ctx, rclone, "copyto", "--s3-versions", dir+"/"+survivors[0], readback); err != nil {
		return fmt.Errorf("Object Lock proof could not read the retained version: %w: %s", err, strings.TrimSpace(string(output)))
	}
	got, err := os.ReadFile(readback)
	if err != nil || !bytes.Equal(got, want) {
		return errors.New("the retained object version did not match the written object")
	}
	return nil
}

// offsiteProbeEntries lists the probe object in dir with its versions: the
// current object as "probe" and each hidden version as "probe-v<time>".
func offsiteProbeEntries(ctx context.Context, rclone string, run offsiteProbeRunner, dir string) ([]string, error) {
	output, err := run(ctx, rclone, "lsf", "--s3-versions", "--files-only", dir)
	if err != nil {
		return nil, fmt.Errorf("Object Lock proof could not list object versions: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var entries []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line = strings.TrimSpace(line); line == "probe" || strings.HasPrefix(line, "probe-v") {
			entries = append(entries, line)
		}
	}
	return entries, nil
}

var offsiteObjectLockProofs = struct {
	sync.Mutex
	results map[string]offsiteProbeResult
}{results: make(map[string]offsiteProbeResult)}

// offsiteObjectLockProof reports the latest Object Lock proof for target and
// starts a new one in the background when the last passing proof is older
// than offsiteObjectLockProofInterval, or a failing one older than the
// ordinary probe interval. With wait, a target with no result yet is proven
// inline instead of reporting pending.
func offsiteObjectLockProof(target string, wait bool) error {
	offsiteObjectLockProofs.Lock()
	previous, ok := offsiteObjectLockProofs.results[target]
	fresh := ok && !previous.checked.IsZero() &&
		((previous.err == nil && time.Since(previous.checked) < offsiteObjectLockProofInterval) ||
			(previous.err != nil && time.Since(previous.checked) < 30*time.Second))
	if fresh || (ok && previous.running) {
		offsiteObjectLockProofs.Unlock()
		if previous.checked.IsZero() {
			return errOffsiteObjectLockProofPending
		}
		return previous.err
	}
	offsiteObjectLockProofs.results[target] = offsiteProbeResult{checked: previous.checked, err: previous.err, running: true}
	offsiteObjectLockProofs.Unlock()
	prove := func() error {
		err := probeOffsiteObjectLock(target)
		offsiteObjectLockProofs.Lock()
		offsiteObjectLockProofs.results[target] = offsiteProbeResult{checked: time.Now(), err: err}
		offsiteObjectLockProofs.Unlock()
		return err
	}
	if wait && previous.checked.IsZero() {
		return prove()
	}
	go func() { _ = prove() }() // the result is recorded in offsiteObjectLockProofs
	if previous.checked.IsZero() {
		return errOffsiteObjectLockProofPending
	}
	// A stale result keeps standing while its refresh runs.
	return previous.err
}
