package stepanel

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type retentionHost struct {
	t                    *testing.T
	root, env, installed string
}

func newRetentionHost(t *testing.T) *retentionHost {
	dir := t.TempDir()
	return &retentionHost{t: t, root: filepath.Join(dir, "upgrade-rollback"), env: filepath.Join(dir, "ste-panel.env"), installed: filepath.Join(dir, "installed-stepanel")}
}

func (h *retentionHost) write(path, content string) {
	h.t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// release writes a stand-in binary whose content identifies the release.
func (h *retentionHost) release(name string) string {
	h.t.Helper()
	path := filepath.Join(filepath.Dir(h.root), "release-"+name)
	h.write(path, "binary "+name+"\n")
	return path
}

// retain runs install_retain_replaced_configuration with date stubbed to
// stamp, so directory names (and so their age order) are fixed.
func (h *retentionHost) retain(newBinary, stamp string, keep int) (string, error) {
	h.t.Helper()
	lib, err := filepath.Abs("deploy/lib/install-upgrade-retention.sh")
	if err != nil {
		h.t.Fatal(err)
	}
	stubs := h.t.TempDir()
	h.write(filepath.Join(stubs, "date"), "#!/bin/sh\nprintf '%s\\n' \"$STAMP\"\n")
	if err := os.Chmod(filepath.Join(stubs, "date"), 0o700); err != nil {
		h.t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `. "$LIB"; install_retain_replaced_configuration "$ROOT" "$ENV_FILE" "$INSTALLED" "$NEW" 0.8.0 "$KEEP"`)
	cmd.Env = append(os.Environ(), "LIB="+lib, "ROOT="+h.root, "ENV_FILE="+h.env, "INSTALLED="+h.installed,
		"NEW="+newBinary, "STAMP="+stamp, "KEEP="+strconv.Itoa(keep), "PATH="+stubs+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

// install makes binary the installed release.
func (h *retentionHost) install(binary string) {
	h.t.Helper()
	data, err := os.ReadFile(binary)
	if err != nil {
		h.t.Fatal(err)
	}
	h.write(h.installed, string(data))
}

func (h *retentionHost) kept() []string {
	h.t.Helper()
	entries, err := os.ReadDir(h.root)
	if err != nil && !os.IsNotExist(err) {
		h.t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func mustSHA256(t *testing.T, path string) string {
	t.Helper()
	sum, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func TestInstallerKeepsPreviousReleaseConfigurationPrivately(t *testing.T) {
	h := newRetentionHost(t)
	old, candidate := h.release("0.7.0"), h.release("0.8.0")
	h.install(old)
	h.write(h.env, "STEPANEL_LISTEN=\"127.0.0.1:8080\"\n")

	kept, err := h.retain(candidate, "20261027T040000Z", 5)
	if err != nil {
		t.Fatalf("retain: %v\n%s", err, kept)
	}
	if filepath.Dir(kept) != h.root || !strings.HasPrefix(filepath.Base(kept), "20261027T040000Z-") {
		t.Fatalf("kept directory = %q", kept)
	}
	if data, err := os.ReadFile(filepath.Join(kept, "ste-panel.env")); err != nil || string(data) != "STEPANEL_LISTEN=\"127.0.0.1:8080\"\n" {
		t.Fatalf("kept configuration = %q, %v", data, err)
	}
	marker, err := os.ReadFile(filepath.Join(kept, "upgrade"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"replaced_by=0.8.0\n", "replaced_by_sha256=" + mustSHA256(t, candidate) + "\n", "previous_binary_sha256=" + mustSHA256(t, old) + "\n"} {
		if !strings.Contains(string(marker), want) {
			t.Errorf("upgrade marker %q is missing %q", marker, want)
		}
	}
	for path, want := range map[string]os.FileMode{h.root: 0o700, kept: 0o700, filepath.Join(kept, "ste-panel.env"): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", path, info.Mode().Perm(), want)
		}
	}
}

func TestInstallerKeepsNothingWithoutAReleaseChange(t *testing.T) {
	h := newRetentionHost(t)
	candidate := h.release("0.8.0")
	// A fresh install has no configuration to keep.
	if kept, err := h.retain(candidate, "20261027T040000Z", 5); err != nil || kept != "" {
		t.Fatalf("fresh install: kept %q, err %v", kept, err)
	}
	// A rerun of the installed release replaces no release.
	h.install(candidate)
	h.write(h.env, "STEPANEL_LISTEN=\"127.0.0.1:8080\"\n")
	if kept, err := h.retain(candidate, "20261027T040100Z", 5); err != nil || kept != "" {
		t.Fatalf("rerun: kept %q, err %v", kept, err)
	}
	if names := h.kept(); len(names) != 0 {
		t.Fatalf("kept %v without a release change", names)
	}
}

// TestInstallerInterruptedUpgradeKeepsFirstAttemptsCopy models a candidate
// install killed after it rewrote the environment file but before it replaced
// the binary. The rerun still sees the old binary, so only the copy rule for
// the same new release stops it from keeping the candidate's file as the
// "previous" configuration.
func TestInstallerInterruptedUpgradeKeepsFirstAttemptsCopy(t *testing.T) {
	h := newRetentionHost(t)
	old, candidate := h.release("0.7.0"), h.release("0.8.0")
	h.install(old)
	h.write(h.env, "STEPANEL_LISTEN=\"previous\"\n")
	first, err := h.retain(candidate, "20261027T040000Z", 5)
	if err != nil || first == "" {
		t.Fatalf("first attempt: kept %q, err %v", first, err)
	}
	h.write(h.env, "STEPANEL_LISTEN=\"candidate\"\nRCLONE_CONFIG=\"/etc/stepanel/rclone.conf\"\n")
	if kept, err := h.retain(candidate, "20261027T040500Z", 5); err != nil || kept != "" {
		t.Fatalf("rerun after interruption kept %q (err %v); it must keep the first attempt's copy", kept, err)
	}
	if data, _ := os.ReadFile(filepath.Join(first, "ste-panel.env")); string(data) != "STEPANEL_LISTEN=\"previous\"\n" {
		t.Fatalf("first attempt's copy = %q", data)
	}
}

func TestInstallerExpiresOldestRetainedConfigurations(t *testing.T) {
	h := newRetentionHost(t)
	stamps := []string{"20261020T000000Z", "20261021T000000Z", "20261022T000000Z", "20261023T000000Z"}
	h.install(h.release("base"))
	for i, stamp := range stamps {
		next := h.release(strconv.Itoa(i))
		h.write(h.env, "STEPANEL_LISTEN=\""+stamp+"\"\n")
		if out, err := h.retain(next, stamp, 2); err != nil {
			t.Fatalf("retain %s: %v\n%s", stamp, err, out)
		}
		h.install(next)
	}
	var stampsKept []string
	for _, name := range h.kept() {
		stampsKept = append(stampsKept, name[:len(stamps[0])])
	}
	if strings.Join(stampsKept, ",") != stamps[2]+","+stamps[3] {
		t.Fatalf("kept %v, want the two newest", stampsKept)
	}
}

func TestInstallerRefusesUnsafeRetentionRoot(t *testing.T) {
	h := newRetentionHost(t)
	if err := os.Mkdir(h.root, 0o755); err != nil {
		t.Fatal(err)
	}
	h.install(h.release("0.7.0"))
	h.write(h.env, "A=\"1\"\n")
	out, err := h.retain(h.release("0.8.0"), "20261027T040000Z", 5)
	if err == nil || !strings.Contains(out, "must be a private directory") {
		t.Fatalf("retain into a 0755 root: err %v, output %q", err, out)
	}
}
