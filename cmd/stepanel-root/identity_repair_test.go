package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cyberducttape/StePanel/internal/siteidentity"
)

func validRepairProfile() repairResourceProfile {
	return repairResourceProfile{
		Site: "example", CPUPercent: 100, CPUWeight: 100, MemoryHighMB: 460,
		MemoryMB: 512, IOWeight: 100, TasksMax: 128, PHPWorkers: 4,
		DiskMB: 2048, Inodes: 50000, FilesystemQuotaState: "enforced", State: "applied",
	}
}

func TestIdentityRepairPreflightRejectsActiveManagedService(t *testing.T) {
	host := &identityRepairHost{
		webRoot: t.TempDir(), access: map[string]repairSiteAccess{},
		profiles: map[string]repairResourceProfile{"example": validRepairProfile()},
		ftpUsers: map[string]bool{},
		commandRunner: func(_ context.Context, command string, _ []byte, _ ...string) ([]byte, error) {
			if command == "systemctl" {
				return []byte("caddy.service loaded active running Caddy\n"), nil
			}
			return nil, nil
		},
	}
	if err := host.Preflight(context.Background(), []siteidentity.RepairAssignment{{Site: "example", LegacyUser: "sp-example-12345678", Username: "sp-0123456789abcdef01234567"}}); err == nil || !strings.Contains(err.Error(), "caddy.service is active") {
		t.Fatalf("active web server preflight error = %v", err)
	}
}

func TestIdentityRepairPreflightRequiresExplicitFTPSRevocation(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sites"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sites", "example"), 0o750); err != nil {
		t.Fatal(err)
	}
	host := &identityRepairHost{
		webRoot: root, access: map[string]repairSiteAccess{},
		profiles: map[string]repairResourceProfile{"example": validRepairProfile()},
		ftpUsers: map[string]bool{"sp-example-12345678": true},
		commandRunner: func(_ context.Context, command string, _ []byte, _ ...string) ([]byte, error) {
			if command == "systemctl" {
				return nil, nil
			}
			if command == "pgrep" {
				return nil, &os.PathError{Op: "pgrep", Path: "user", Err: errors.New("not found")}
			}
			return nil, nil
		},
	}
	assignment := []siteidentity.RepairAssignment{{Site: "example", LegacyUser: "sp-example-12345678", Username: "sp-0123456789abcdef01234567"}}
	if err := host.Preflight(context.Background(), assignment); err == nil || !strings.Contains(err.Error(), "--revoke-ftps") {
		t.Fatalf("FTPS preflight error = %v", err)
	}
}

func TestIdentityRepairReconcilesHostPolicyAndVerifiesNewAccount(t *testing.T) {
	root := t.TempDir()
	siteRoot := filepath.Join(root, "sites", "example")
	public := filepath.Join(siteRoot, "public")
	if err := os.MkdirAll(public, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "index.php"), []byte("<?php"), 0o640); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	var newAccessInput string
	host := &identityRepairHost{
		webRoot: root, siteCtl: "/usr/local/sbin/stepanel-sitectl", appCtl: "/usr/local/sbin/stepanel-appctl",
		access: map[string]repairSiteAccess{"example": {SFTPEnabled: true, Keys: []struct {
			PublicKey string `json:"public_key"`
		}{{PublicKey: "ssh-ed25519 AAAATEST"}}}},
		profiles: map[string]repairResourceProfile{"example": validRepairProfile()},
		ftpUsers: map[string]bool{"sp-example-12345678": true}, revokeFTPS: true,
		oldHomes: map[string]string{"sp-example-12345678": filepath.Clean(siteRoot)},
		commandRunner: func(_ context.Context, command string, stdin []byte, args ...string) ([]byte, error) {
			call := append([]string{filepath.Base(command)}, args...)
			calls = append(calls, call)
			if filepath.Base(command) == "stepanel-sitectl" && len(args) > 0 && args[0] == "access" && args[2] == "sp-0123456789abcdef01234567" {
				newAccessInput = string(stdin)
			}
			return nil, nil
		},
	}
	assignment := siteidentity.RepairAssignment{Site: "example", PreviousUser: "sp-example-12345678", LegacyUser: "sp-example-12345678", Username: "sp-0123456789abcdef01234567"}
	if err := host.Reconcile(context.Background(), assignment); err != nil {
		t.Fatalf("reconcile host: %v", err)
	}
	if !strings.Contains(newAccessInput, "ssh-ed25519 AAAATEST") {
		t.Fatalf("site SSH key was not restored to the new identity: %q", newAccessInput)
	}
	for _, action := range []string{"ftp", "access", "seal", "quota"} {
		found := false
		for _, call := range calls {
			if len(call) > 1 && call[1] == action {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("reconciliation did not invoke %s: %v", action, calls)
		}
	}
	newHome := "/var/www/sites/example"
	host.commandRunner = func(_ context.Context, command string, _ []byte, args ...string) ([]byte, error) {
		if command == "getent" {
			return []byte(args[1] + ":x:1001:1001::" + newHome + ":/usr/sbin/nologin\n"), nil
		}
		return nil, nil
	}
	host.webRoot = "/var/www"
	if err := host.Verify(context.Background(), assignment); err != nil {
		t.Fatalf("verify new identity: %v", err)
	}
}

func TestCreateIdentityDBSnapshotUsesSQLiteOnlineBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE state (value TEXT); INSERT INTO state VALUES ('preserved')`); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "recovery", "before.sqlite")
	if err := os.Mkdir(filepath.Dir(snapshot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := createIdentityDBSnapshot(db, snapshot); err != nil {
		t.Fatalf("create consistent snapshot: %v", err)
	}
	info, err := os.Stat(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %o, want 0600", info.Mode().Perm())
	}
	backup, err := sql.Open("sqlite", "file:"+snapshot+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var value string
	if err := backup.QueryRow(`SELECT value FROM state`).Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("snapshot content = %q, %v", value, err)
	}
}
