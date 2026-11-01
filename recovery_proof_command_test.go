package stepanel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func init() {
	// Test proof programs live in temporary directories owned by the test
	// user; production trusts only root.
	recoveryProofTrustedUID = uint32(os.Getuid())
}

// recoveryProofDir is a temporary directory a proof program may live in:
// t.TempDir honours the umask, which can leave it group-writable.
func recoveryProofDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRecoveryProofExecutableMustNotBeReplaceableByThePanel(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("ownership refusal needs a non-root test user")
	}
	dir := recoveryProofDir(t)
	proof := filepath.Join(dir, "proof")
	if err := os.WriteFile(proof, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := validateRecoveryProofExecutable(proof); err != nil {
		t.Fatalf("trusted proof refused: %v", err)
	}

	previous := recoveryProofTrustedUID
	recoveryProofTrustedUID = 0
	t.Cleanup(func() { recoveryProofTrustedUID = previous })
	err := runRecoveryProofCommand(context.Background(), proof, "shop", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "must be owned by root") {
		t.Fatalf("proof owned by the service account = %v, want ownership refusal", err)
	}
	recoveryProofTrustedUID = previous

	if err := os.Chmod(proof, 0o775); err != nil {
		t.Fatal(err)
	}
	if _, err := validateRecoveryProofExecutable(proof); err == nil || !strings.Contains(err.Error(), "group- or world-writable") {
		t.Fatalf("group-writable proof = %v", err)
	}
	if err := os.Chmod(proof, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := validateRecoveryProofExecutable(proof); err == nil || !strings.Contains(err.Error(), "group- or world-writable") {
		t.Fatalf("proof in a world-writable directory = %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"proof", dir + "/../" + filepath.Base(dir) + "/proof"} {
		if _, err := validateRecoveryProofExecutable(bad); err == nil {
			t.Fatalf("non-canonical proof path %q accepted", bad)
		}
	}
	if err := os.Chmod(proof, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validateRecoveryProofExecutable(proof); err == nil {
		t.Fatal("non-executable proof accepted")
	}
}
