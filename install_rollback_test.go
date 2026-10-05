package stepanel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runControlPlaneTxn sources deploy/lib/control-plane-txn.sh and runs script
// with DB and TXN set, as install.sh does.
func runControlPlaneTxn(t *testing.T, db, txn, script string) (string, error) {
	t.Helper()
	lib, err := filepath.Abs("deploy/lib/control-plane-txn.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "set -Eeuo pipefail\n. \"$LIB\"\n"+script)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LIB=" + lib, "DB=" + db, "TXN=" + txn}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func readOrMissing(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "<missing>"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func failedUpgradeCopies(t *testing.T, db string) []string {
	t.Helper()
	matches, err := filepath.Glob(db + ".failed-upgrade-*")
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

// The candidate migrates the database, then fails its health check: rollback
// must restore the exact database file set the previous release left, and
// keep the candidate's files aside for diagnosis.
func TestInstallRollbackRestoresMigratedControlPlaneDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "stepanel-control.db")
	if err := os.WriteFile(db, []byte("schema v10"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db+"-wal", []byte("v10 wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runControlPlaneTxn(t, db, t.TempDir(), `
control_plane_txn_begin "$DB" "$TXN"
printf 'schema v11' > "$DB"
printf 'v11 wal' > "$DB-wal"
printf 'v11 shm' > "$DB-shm"
control_plane_txn_rollback
`)
	if err != nil {
		t.Fatalf("rollback failed: %v\n%s", err, output)
	}
	if got := readOrMissing(t, db); got != "schema v10" {
		t.Fatalf("database = %q, want the pre-upgrade copy", got)
	}
	if got := readOrMissing(t, db+"-wal"); got != "v10 wal" {
		t.Fatalf("WAL = %q, want the pre-upgrade copy", got)
	}
	if got := readOrMissing(t, db+"-shm"); got != "<missing>" {
		t.Fatalf("candidate SHM file survived rollback: %q", got)
	}
	if info, err := os.Stat(db); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored database mode = %v, %v", info, err)
	}
	copies := failedUpgradeCopies(t, db)
	if len(copies) != 3 {
		t.Fatalf("candidate files kept aside = %v, want database, WAL and SHM", copies)
	}
	if !strings.Contains(output, "Restored the control-plane database") {
		t.Fatalf("rollback did not report the restore:\n%s", output)
	}
}

// A first install that fails leaves no database behind, as before it ran.
func TestInstallRollbackRemovesDatabaseCreatedByFailedFirstInstall(t *testing.T) {
	db := filepath.Join(t.TempDir(), "stepanel-control.db")
	if output, err := runControlPlaneTxn(t, db, t.TempDir(), `
control_plane_txn_begin "$DB" "$TXN"
printf 'fresh' > "$DB"
control_plane_txn_rollback
`); err != nil {
		t.Fatalf("rollback failed: %v\n%s", err, output)
	}
	if got := readOrMissing(t, db); got != "<missing>" {
		t.Fatalf("database created by the failed install remained: %q", got)
	}
}

// A failure before the candidate ever started leaves the database alone and
// makes no secret-bearing copy of it.
func TestInstallRollbackLeavesUntouchedDatabaseAlone(t *testing.T) {
	db := filepath.Join(t.TempDir(), "stepanel-control.db")
	if err := os.WriteFile(db, []byte("schema v10"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := runControlPlaneTxn(t, db, t.TempDir(), `
control_plane_txn_begin "$DB" "$TXN"
control_plane_txn_rollback
`); err != nil {
		t.Fatalf("rollback failed: %v\n%s", err, output)
	}
	if got := readOrMissing(t, db); got != "schema v10" {
		t.Fatalf("database = %q", got)
	}
	if copies := failedUpgradeCopies(t, db); len(copies) != 0 {
		t.Fatalf("unchanged database was copied aside: %v", copies)
	}
}

func TestInstallCommitKeepsCandidateDatabase(t *testing.T) {
	db := filepath.Join(t.TempDir(), "stepanel-control.db")
	if err := os.WriteFile(db, []byte("schema v10"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := runControlPlaneTxn(t, db, t.TempDir(), `
control_plane_txn_begin "$DB" "$TXN"
printf 'schema v11' > "$DB"
control_plane_txn_commit
control_plane_txn_rollback
`); err != nil {
		t.Fatalf("commit failed: %v\n%s", err, output)
	}
	if got := readOrMissing(t, db); got != "schema v11" {
		t.Fatalf("committed database = %q, want the candidate's", got)
	}
}

func TestInstallTransactionRefusesSymlinkedDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "stepanel-control.db")
	if err := os.Symlink(filepath.Join(dir, "elsewhere.db"), db); err != nil {
		t.Fatal(err)
	}
	if output, err := runControlPlaneTxn(t, db, t.TempDir(), `control_plane_txn_begin "$DB" "$TXN"`); err == nil {
		t.Fatalf("snapshot of a symlinked database accepted:\n%s", output)
	}
}

// install.sh takes the snapshot after stopping every StePanel process and
// rolls it back after stopping the candidate.
func TestInstallScriptWiresControlPlaneTransaction(t *testing.T) {
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	stop := strings.Index(text, "if (( STEPANEL_BROKER_WAS_ACTIVE )); then systemctl stop stepanel-root-broker.service; fi")
	begin := strings.Index(text, `control_plane_txn_begin "$DATA_DIR/stepanel-control.db" "$INSTALL_TXN"`)
	start := strings.Index(text, "systemctl enable --now stepanel.service stepanel-worker.service")
	if stop < 0 || begin < 0 || start < 0 || !(stop < begin && begin < start) {
		t.Fatal("install.sh must snapshot the control-plane database after stopping services and before starting the candidate")
	}
	rollback := text[strings.Index(text, "rollback_install() {"):]
	rollback = rollback[:strings.Index(rollback, "\n}\n")]
	stopCandidate := strings.Index(rollback, "systemctl stop stepanel.service stepanel-worker.service")
	restore := strings.Index(rollback, "control_plane_txn_rollback")
	restart := strings.Index(rollback, "systemctl restart stepanel.service")
	if stopCandidate < 0 || restore < 0 || restart < 0 || !(stopCandidate < restore && restore < restart) {
		t.Fatal("rollback_install must stop the candidate, restore the database, then restart the previous release")
	}
	if !strings.Contains(text, "control_plane_txn_commit") {
		t.Fatal("install.sh never commits the control-plane transaction")
	}
}
