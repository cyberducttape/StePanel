package stepanel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeMaintenanceWP is a wp-cli stand-in that keeps maintenance mode in a
// state file. Deactivation fails while $WP_DEACTIVATE_FAILS is set.
type fakeMaintenanceWP struct {
	cfg    Config
	state  string
	marker string
}

func newFakeMaintenanceWP(t *testing.T) fakeMaintenanceWP {
	t.Helper()
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "wp-config.php"), "<?php")
	state := filepath.Join(root, "maintenance-on")
	wp := filepath.Join(root, "wp")
	script := "#!/bin/sh\nfor arg; do last=$arg; done\ncase \"$last\" in\n" +
		"is-active) [ -f \"" + state + "\" ] ;;\n" +
		"activate) : > \"" + state + "\" ;;\n" +
		"deactivate) [ -z \"$WP_DEACTIVATE_FAILS\" ] && rm -f \"" + state + "\" ;;\n" +
		"esac\n"
	if err := os.WriteFile(wp, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{WebRoot: webRoot, BackupRoot: filepath.Join(root, "backups"), WPCLI: wp, ControlPlaneDB: filepath.Join(root, "state", "control.db"), AuditLog: filepath.Join(root, "audit.jsonl")}
	return fakeMaintenanceWP{cfg: cfg, state: state, marker: filepath.Join(root, "state", "wordpress-maintenance", "account")}
}

func (f fakeMaintenanceWP) maintenanceOn() bool {
	_, err := os.Stat(f.state)
	return err == nil
}

func (f fakeMaintenanceWP) markerExists() bool {
	_, err := os.Stat(f.marker)
	return err == nil
}

func TestWordPressBackupEndsMaintenanceAndClearsRecord(t *testing.T) {
	f := newFakeMaintenanceWP(t)
	if _, err := CreateSiteBackupContext(context.Background(), f.cfg, AuthorizedSite{site: "account"}, false); err != nil {
		t.Fatal(err)
	}
	if f.maintenanceOn() || f.markerExists() {
		t.Fatalf("after backup: maintenance on = %v, record present = %v", f.maintenanceOn(), f.markerExists())
	}
}

// A cancelled backup must still turn maintenance mode off: deactivation has
// its own context.
func TestWordPressMaintenanceEndsDespiteCancelledBackup(t *testing.T) {
	f := newFakeMaintenanceWP(t)
	if err := recordWordPressMaintenance(f.cfg, "account"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.state, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := endWordPressMaintenance(ctx, f.cfg, "account"); err != nil {
		t.Fatal(err)
	}
	if f.maintenanceOn() || f.markerExists() {
		t.Fatalf("maintenance on = %v, record present = %v", f.maintenanceOn(), f.markerExists())
	}
}

// A failed deactivation surfaces in the backup error and keeps the record so
// recovery can retry.
func TestWordPressBackupReportsMaintenanceLeftOn(t *testing.T) {
	f := newFakeMaintenanceWP(t)
	t.Setenv("WP_DEACTIVATE_FAILS", "1")
	_, err := CreateSiteBackupContext(context.Background(), f.cfg, AuthorizedSite{site: "account"}, false)
	if err == nil || !strings.Contains(err.Error(), "still in maintenance mode") {
		t.Fatalf("backup error = %v, want maintenance-mode failure", err)
	}
	if !f.maintenanceOn() || !f.markerExists() {
		t.Fatalf("maintenance on = %v, record present = %v; want both kept for recovery", f.maintenanceOn(), f.markerExists())
	}
}

// After a process kill the deferred deactivation never runs; recovery ends
// the maintenance window from the durable record.
func TestRecoverWordPressMaintenanceAfterKill(t *testing.T) {
	f := newFakeMaintenanceWP(t)
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	if err := recordWordPressMaintenance(f.cfg, "account"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.state, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WP_DEACTIVATE_FAILS", "1")
	if failures := recoverWordPressMaintenance(f.cfg, nil, time.Second); len(failures) != 1 {
		t.Fatalf("failed recovery = %v, want one reported failure", failures)
	}
	if !f.markerExists() {
		t.Fatal("record was cleared although maintenance mode is still on")
	}
	t.Setenv("WP_DEACTIVATE_FAILS", "")
	if failures := recoverWordPressMaintenance(f.cfg, nil, time.Second); len(failures) != 0 {
		t.Fatalf("recovery failures = %v", failures)
	}
	if f.maintenanceOn() || f.markerExists() {
		t.Fatalf("after recovery: maintenance on = %v, record present = %v", f.maintenanceOn(), f.markerExists())
	}
	if got := strings.Join(auditActions(t, f.cfg.AuditLog), ","); got != "wordpress.maintenance.recovered" {
		t.Fatalf("audit = %s", got)
	}
}

// A backup that still holds the site lease owns its maintenance window;
// recovery leaves it alone and does not report a failure.
func TestRecoverWordPressMaintenanceSkipsLeasedSite(t *testing.T) {
	f := newFakeMaintenanceWP(t)
	if err := recordWordPressMaintenance(f.cfg, "account"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.state, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	busy := func(context.Context, string) (context.Context, func(), error) {
		return nil, nil, errors.New("lease held")
	}
	if failures := recoverWordPressMaintenance(f.cfg, busy, time.Millisecond); len(failures) != 0 {
		t.Fatalf("failures = %v, want none for a leased site", failures)
	}
	if !f.maintenanceOn() || !f.markerExists() {
		t.Fatal("recovery changed a maintenance window still owned by a running backup")
	}
}
