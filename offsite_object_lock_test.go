package stepanel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeS3Bucket models the S3 behavior the Object Lock proof relies on:
// deleting an object in a versioned bucket hides its version, and Object Lock
// refuses permanent deletion of a retained version.
type fakeS3Bucket struct {
	versioned, locked bool
	// cleanupLies makes cleanup-hidden report success without deleting.
	cleanupLies bool
	failWrite   bool
	current     map[string][]byte
	hidden      map[string][]byte // versioned name -> contents
	calls       []string
}

func newFakeS3Bucket(versioned, locked bool) *fakeS3Bucket {
	return &fakeS3Bucket{versioned: versioned, locked: locked, current: map[string][]byte{}, hidden: map[string][]byte{}}
}

func (b *fakeS3Bucket) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	b.calls = append(b.calls, strings.Join(args, " "))
	switch {
	case len(args) == 3 && args[0] == "copyto" && strings.Contains(args[2], ":"):
		if b.failWrite {
			return []byte("AccessDenied"), errors.New("exit status 1")
		}
		data, err := os.ReadFile(args[1])
		if err != nil {
			return nil, err
		}
		b.current[args[2]] = data
		return nil, nil
	case len(args) == 2 && args[0] == "deletefile":
		data, ok := b.current[args[1]]
		if !ok {
			return nil, errors.New("exit status 4")
		}
		delete(b.current, args[1])
		if b.versioned {
			b.hidden[args[1]+"-v2026-11-25-120000-000"] = data
		}
		return nil, nil
	case len(args) == 4 && args[0] == "lsf" && args[1] == "--s3-versions":
		var names []string
		for name := range b.hidden {
			if strings.HasPrefix(name, args[3]+"/") {
				names = append(names, strings.TrimPrefix(name, args[3]+"/"))
			}
		}
		return []byte(strings.Join(names, "\n")), nil
	case len(args) == 3 && args[0] == "backend" && args[1] == "cleanup-hidden":
		if b.cleanupLies {
			return nil, nil
		}
		if b.locked {
			return []byte("AccessDenied: object is WORM protected"), errors.New("exit status 1")
		}
		for name := range b.hidden {
			if strings.HasPrefix(name, args[2]+"/") {
				delete(b.hidden, name)
			}
		}
		return nil, nil
	case len(args) == 4 && args[0] == "copyto" && args[1] == "--s3-versions":
		data, ok := b.hidden[args[2]]
		if !ok {
			return nil, errors.New("exit status 3")
		}
		return nil, os.WriteFile(args[3], data, 0o600)
	}
	return nil, fmt.Errorf("unexpected rclone call %q", args)
}

func TestOffsiteObjectLockProofPassesOnlyWhenVersionsCannotBeDeleted(t *testing.T) {
	cases := []struct {
		name   string
		bucket *fakeS3Bucket
		want   string
	}{
		{name: "locked", bucket: newFakeS3Bucket(true, true)},
		{name: "versioned without lock", bucket: newFakeS3Bucket(true, false), want: "permanently deleted an object version"},
		{name: "unversioned", bucket: newFakeS3Bucket(false, false), want: "not versioned"},
		{name: "write refused", bucket: func() *fakeS3Bucket { b := newFakeS3Bucket(true, true); b.failWrite = true; return b }(), want: "write failed"},
		// A deletion that reports success fails the proof even if the version
		// survived: the proof never vouches for behavior it did not observe.
		{name: "cleanup reports success", bucket: func() *fakeS3Bucket { b := newFakeS3Bucket(true, true); b.cleanupLies = true; return b }(), want: "permanently deleted an object version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := proveOffsiteObjectLockWithRunner(t.Context(), "s3:bucket/stepanel/", "/usr/bin/rclone", tc.bucket.run)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("locked bucket failed the proof: %v (calls %q)", err, tc.bucket.calls)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("proof error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	// Every probe object lives under the target's health prefix.
	bucket := newFakeS3Bucket(true, true)
	if err := proveOffsiteObjectLockWithRunner(t.Context(), "s3:bucket/stepanel", "/usr/bin/rclone", bucket.run); err != nil {
		t.Fatal(err)
	}
	for _, call := range bucket.calls {
		if !strings.Contains(call, "s3:bucket/stepanel/.stepanel-health/lock-") {
			t.Fatalf("proof touched an object outside the health prefix: %q", call)
		}
	}
}

func TestOffsiteObjectLockProofRefusesAmbiguousVersionListings(t *testing.T) {
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "lsf" {
			return []byte("probe-v2026-11-25-120000-000\nprobe-v2026-11-25-120001-000\n"), nil
		}
		return nil, nil
	}
	if err := proveOffsiteObjectLockWithRunner(t.Context(), "s3:bucket", "/usr/bin/rclone", run); err == nil || !strings.Contains(err.Error(), "2 versions") {
		t.Fatalf("expected an ambiguous listing to fail the proof, got %v", err)
	}
}

func resetOffsiteObjectLockProofs() {
	offsiteObjectLockProofs.Lock()
	offsiteObjectLockProofs.results = make(map[string]offsiteProbeResult)
	offsiteObjectLockProofs.Unlock()
}

func TestOffsiteReadinessRequiresTheObjectLockProofWhenConfigured(t *testing.T) {
	previousProbe, previousProof := probeOffsiteRemote, probeOffsiteObjectLock
	t.Cleanup(func() {
		probeOffsiteRemote, probeOffsiteObjectLock = previousProbe, previousProof
		resetOffsiteProbeCache()
		resetOffsiteObjectLockProofs()
	})
	probeOffsiteRemote = func(string) error { return nil }
	proofs := 0
	probeOffsiteObjectLock = func(string) error { proofs++; return errors.New("the bucket is not versioned") }
	app := &App{Config: Config{OffsiteTarget: "s3:bucket/stepanel", OffsiteObjectLock: true}}

	resetOffsiteProbeCache()
	resetOffsiteObjectLockProofs()
	ready, reason := app.checkOffsiteTargetReadiness()
	if ready || !strings.Contains(reason, "Object Lock was not") || !strings.Contains(reason, "not versioned") {
		t.Fatalf("unlocked bucket readiness = %v %q", ready, reason)
	}

	resetOffsiteObjectLockProofs()
	probeOffsiteObjectLock = func(string) error { proofs++; return nil }
	ready, reason = app.checkOffsiteTargetReadiness()
	if !ready || !strings.Contains(reason, "Object Lock verified") {
		t.Fatalf("locked bucket readiness = %v %q", ready, reason)
	}
	// A passing proof stands for its interval instead of writing another
	// retained object on every readiness check.
	before := proofs
	if ready, _ := app.checkOffsiteTargetReadiness(); !ready || proofs != before {
		t.Fatalf("passing proof was repeated: ready=%v proofs %d -> %d", ready, before, proofs)
	}

	// Without the setting the proof is never run.
	resetOffsiteObjectLockProofs()
	before = proofs
	plain := &App{Config: Config{OffsiteTarget: "s3:bucket/stepanel"}}
	if ready, reason := plain.checkOffsiteTargetReadiness(); !ready || strings.Contains(reason, "Object Lock") || proofs != before {
		t.Fatalf("readiness without Object Lock = %v %q (proofs %d -> %d)", ready, reason, before, proofs)
	}
}

func TestOffsiteObjectLockUploadWritesStraightToTheFinalPrefix(t *testing.T) {
	root := t.TempDir()
	cfg, remoteRoot := offsiteTestRemote(t, root)
	cfg.OffsiteObjectLock = true
	// Log every rclone call and refuse purge: nothing written to a locked
	// bucket can be removed, so the upload must never stage and purge.
	if err := os.Rename(filepath.Join(root, "rclone"), filepath.Join(root, "rclone.real")); err != nil {
		t.Fatal(err)
	}
	callLog := filepath.Join(root, "calls")
	wrapper := `#!/bin/sh
printf '%s\n' "$*" >> "` + callLog + `"
[ "$1" = purge ] && { echo "purge refused on a locked bucket" >&2; exit 1; }
exec "` + filepath.Join(root, "rclone.real") + `" "$@"
`
	if err := os.WriteFile(filepath.Join(root, "rclone"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	backup, err := CreateSiteBackup(cfg, AuthorizedSite{site: "account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := uploadOffsiteContext(t.Context(), cfg, backup); err != nil {
			t.Fatalf("upload %d to a locked bucket: %v", attempt+1, err)
		}
	}
	final := filepath.Join(remoteRoot, "account", filepath.Base(backup.Path))
	if _, err := os.Stat(filepath.Join(final, offsiteCompleteMarker)); err != nil {
		t.Fatalf("completion marker missing: %v", err)
	}
	if staged, _ := filepath.Glob(final + offsiteUploadStagingInfix + "*"); len(staged) != 0 {
		t.Fatalf("locked upload staged objects: %v", staged)
	}
	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), offsiteUploadStagingInfix) || strings.Contains(string(calls), "purge") {
		t.Fatalf("locked upload staged or purged:\n%s", calls)
	}
	restored, cleanup, err := downloadOffsiteBackupContext(t.Context(), cfg, "account", filepath.Base(backup.Path))
	if err != nil {
		t.Fatalf("download from locked bucket: %v", err)
	}
	defer cleanup()
	if _, err := VerifySiteBackupStrict(restored, ""); err != nil {
		t.Fatalf("verify backup downloaded from locked bucket: %v", err)
	}
}

func TestOffsiteObjectLockSettingIsValidated(t *testing.T) {
	t.Setenv("STEPANEL_OFFSITE_OBJECT_LOCK", "yes")
	if err := ValidateConfig(LoadConfig()); err == nil || !strings.Contains(err.Error(), "STEPANEL_OFFSITE_OBJECT_LOCK must be 0 or 1") {
		t.Fatalf("expected an invalid value to be refused, got %v", err)
	}
	t.Setenv("STEPANEL_OFFSITE_OBJECT_LOCK", "1")
	t.Setenv("STEPANEL_OFFSITE_TARGET", "")
	if err := ValidateConfig(LoadConfig()); err == nil || !strings.Contains(err.Error(), "STEPANEL_OFFSITE_OBJECT_LOCK=1 requires STEPANEL_OFFSITE_TARGET") {
		t.Fatalf("expected Object Lock without a target to be refused, got %v", err)
	}
	t.Setenv("STEPANEL_OFFSITE_TARGET", "s3:bucket/stepanel")
	if cfg := LoadConfig(); !cfg.OffsiteObjectLock {
		t.Fatal("STEPANEL_OFFSITE_OBJECT_LOCK=1 was not loaded")
	}
}
