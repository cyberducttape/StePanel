package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
	"github.com/cyberducttape/StePanel/internal/siteidentity"
	"golang.org/x/crypto/ssh"
)

type repairSiteAccess struct {
	State        string `json:"state"`
	SFTPEnabled  bool   `json:"sftp_enabled"`
	ShellEnabled bool   `json:"shell_enabled"`
	Keys         []struct {
		PublicKey string `json:"public_key"`
	} `json:"keys"`
}

type repairResourceProfile struct {
	Account              string `json:"account"`
	Site                 string `json:"site"`
	CPUPercent           int    `json:"cpu_percent"`
	CPUWeight            int    `json:"cpu_weight"`
	MemoryHighMB         int    `json:"memory_high_mb"`
	MemoryMB             int    `json:"memory_mb"`
	IOWeight             int    `json:"io_weight"`
	TasksMax             int    `json:"tasks_max"`
	PHPWorkers           int    `json:"php_workers"`
	DiskMB               int    `json:"disk_mb"`
	Inodes               int    `json:"inodes"`
	FilesystemQuotaState string `json:"filesystem_quota_state"`
	State                string `json:"state"`
}

type identityRepairHost struct {
	webRoot       string
	siteCtl       string
	appCtl        string
	stateDir      string
	siteRegistry  string
	recoveryRoot  string
	db            *sql.DB
	revokeFTPS    bool
	access        map[string]repairSiteAccess
	profiles      map[string]repairResourceProfile
	ftpUsers      map[string]bool
	oldHomes      map[string]string
	commandRunner func(context.Context, string, []byte, ...string) ([]byte, error)
}

var managedServiceName = regexp.MustCompile(`^(stepanel(-worker|-root-broker|-app-.*|-task-.*|-runner-.*)?|caddy|apache2|httpd|lsws|lshttpd|litespeed|openlitespeed|php[^/]*fpm|php-fpm|ssh|sshd|vsftpd|cron|crond)\.service$`)
var managedActivationName = regexp.MustCompile(`^(stepanel.*\.(socket|timer)|ssh(d)?\.socket|vsftpd\.socket)$`)

func runIdentityRepair(args []string) int {
	if len(args) == 0 || args[0] != "plan" && args[0] != "apply" {
		fmt.Fprintln(os.Stderr, "usage: stepanel-root identity-repair plan|apply [options]")
		return 64
	}
	mode := args[0]
	fs := flag.NewFlagSet("identity-repair", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	webRoot := fs.String("webroot", "/var/www", "managed web root")
	database := fs.String("control-plane-db", "/var/lib/ste-panel/stepanel-control.db", "control-plane SQLite database")
	recoveryRoot := fs.String("recovery-root", "/var/lib/stepanel/recovery", "root-owned recovery directory")
	stateDir := fs.String("state-dir", "/var/lib/ste-panel", "directory containing site-access.json and resource-profiles.json")
	revokeFTPS := fs.Bool("revoke-ftps", false, "explicitly revoke legacy FTPS credentials that cannot be transferred")
	if err := fs.Parse(args[1:]); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional argument")
		return 64
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "site identity recovery must run as root from maintenance mode")
		return 1
	}
	for name, value := range map[string]string{"webroot": *webRoot, "control-plane-db": *database, "recovery-root": *recoveryRoot, "state-dir": *stateDir} {
		if !filepath.IsAbs(value) {
			fmt.Fprintf(os.Stderr, "%s must be an absolute path\n", name)
			return 64
		}
	}
	if mode == "apply" {
		syscall.Umask(0o077)
		if err := verifyPrivateRecoveryRoot(*recoveryRoot); err != nil {
			fmt.Fprintf(os.Stderr, "validate recovery root: %v\n", err)
			return 1
		}
	}
	db, err := openFencingDatabase(*database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open control-plane database: %v\n", err)
		return 1
	}
	defer db.Close()
	if mode == "plan" {
		plan, err := siteidentity.PlanCollisionRepair(db, *webRoot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plan site identity repair: %v\n", err)
			return 1
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(plan); err != nil {
			fmt.Fprintf(os.Stderr, "write repair plan: %v\n", err)
			return 1
		}
		return 0
	}

	journalPath := filepath.Join(*recoveryRoot, "site-identity-repair.json")
	host, err := newIdentityRepairHost(*webRoot, "/usr/local/sbin/stepanel-sitectl", "/usr/local/sbin/stepanel-appctl", *stateDir, *revokeFTPS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load site state for identity repair: %v\n", err)
		return 1
	}
	host.recoveryRoot, host.db = *recoveryRoot, db
	journal, err := siteidentity.ExecuteCollisionRepair(context.Background(), db, *webRoot, journalPath, host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "site identity repair is incomplete; keep StePanel services stopped and rerun apply to resume: %v\n", err)
		return 1
	}
	fmt.Printf("site identity repair %s completed for %d sites; review revoked FTPS accounts before restarting services\n", journal.ID, len(journal.Assignments))
	return 0
}

func verifyPrivateRecoveryRoot(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("recovery root must be a private, real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("recovery root must be owned by root")
	}
	return nil
}

// BeforeCommit records a consistent pre-repair SQLite snapshot named for the
// journal. A snapshot left by an earlier attempt of the same journal is
// already pre-commit state and is kept.
func (h *identityRepairHost) BeforeCommit(_ context.Context, journal siteidentity.RepairJournal) error {
	if h.db == nil || h.recoveryRoot == "" {
		return errors.New("snapshot database and recovery root are not configured")
	}
	path := filepath.Join(h.recoveryRoot, "site-identities-before-"+journal.ID+".sqlite")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("existing snapshot %s is not a regular file", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := createIdentityDBSnapshot(h.db, path); err != nil {
		return fmt.Errorf("create pre-repair database snapshot: %w", err)
	}
	fmt.Fprintf(os.Stderr, "pre-repair SQLite snapshot: %s\n", path)
	return nil
}

// createIdentityDBSnapshot writes the snapshot under a temporary name and
// publishes it with a rename, so an interrupted VACUUM never leaves a
// truncated file at the final path.
func createIdentityDBSnapshot(db *sql.DB, path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("snapshot already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stage := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.Remove(stage); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale snapshot stage: %w", err)
	}
	defer os.Remove(stage)
	if _, err := db.Exec(`VACUUM INTO ?`, stage); err != nil {
		return fmt.Errorf("SQLite VACUUM INTO: %w", err)
	}
	if err := os.Chmod(stage, 0o600); err != nil {
		return fmt.Errorf("set snapshot mode: %w", err)
	}
	f, err := os.Open(stage)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync snapshot: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close snapshot: %w", err)
	}
	if err := os.Rename(stage, path); err != nil {
		return fmt.Errorf("publish snapshot: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func newIdentityRepairHost(webRoot, siteCtl, appCtl, stateDir string, revokeFTPS bool) (*identityRepairHost, error) {
	host := &identityRepairHost{
		webRoot: webRoot, siteCtl: siteCtl, appCtl: appCtl, stateDir: stateDir, siteRegistry: "/etc/stepanel/sites", revokeFTPS: revokeFTPS,
		access: map[string]repairSiteAccess{}, profiles: map[string]repairResourceProfile{}, ftpUsers: map[string]bool{}, oldHomes: map[string]string{},
		commandRunner: runRepairCommand,
	}
	if err := readRepairState(filepath.Join(stateDir, "site-access.json"), &host.access); err != nil {
		return nil, fmt.Errorf("read site access state: %w", err)
	}
	if err := readRepairState(filepath.Join(stateDir, "resource-profiles.json"), &host.profiles); err != nil {
		return nil, fmt.Errorf("read resource profile state: %w", err)
	}
	ftpData, err := readOptionalBounded(filepath.Join("/etc/vsftpd", "stepanel.users"), 1<<20)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read FTPS allowlist: %w", err)
	}
	for _, user := range strings.Fields(string(ftpData)) {
		host.ftpUsers[user] = true
	}
	return host, nil
}

func (h *identityRepairHost) Preflight(ctx context.Context, assignments []siteidentity.RepairAssignment) error {
	output, err := h.commandRunner(ctx, "systemctl", nil, "list-units", "--type=service", "--state=active", "--no-legend", "--plain")
	if err != nil {
		return fmt.Errorf("list active system services: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && managedServiceName.MatchString(fields[0]) {
			return fmt.Errorf("managed service %s is active; stop all panel, broker, web, PHP-FPM, SSH and FTPS services before repair", fields[0])
		}
	}
	for _, unitType := range []string{"socket", "timer"} {
		output, err := h.commandRunner(ctx, "systemctl", nil, "list-units", "--type="+unitType, "--state=active", "--no-legend", "--plain")
		if err != nil {
			return fmt.Errorf("list active system %s units: %w", unitType, err)
		}
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && managedActivationName.MatchString(fields[0]) {
				return fmt.Errorf("managed activation unit %s is active; disable it for maintenance", fields[0])
			}
		}
	}
	affectedRoots := make(map[string]bool, len(assignments))
	for _, assignment := range assignments {
		affectedRoots[filepath.Clean(filepath.Join(h.webRoot, "sites", assignment.Site))] = true
	}
	checkedOldUsers := map[string]bool{}
	for _, assignment := range assignments {
		siteRoot := filepath.Clean(filepath.Join(h.webRoot, "sites", assignment.Site))
		// A site placed in a hosting account (ADR-0007) runs as the account's
		// user, never a per-site sp- account; the site helper would refuse
		// it only after the mappings had committed.
		if h.siteRegistry != "" {
			if _, err := os.Lstat(filepath.Join(h.siteRegistry, assignment.Site, "location")); err == nil {
				return fmt.Errorf("site %q is placed in a hosting account and has no per-site Unix account to repair", assignment.Site)
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect placement of site %q: %w", assignment.Site, err)
			}
		}
		if err := h.verifyNoRepairHardlinks(ctx, siteRoot); err != nil {
			return fmt.Errorf("site %q is not safe to reassign: %w", assignment.Site, err)
		}
		if err := verifyNoNestedRepairMounts(siteRoot); err != nil {
			return fmt.Errorf("site %q contains an unsafe mount: %w", assignment.Site, err)
		}
		for _, oldUser := range oldRepairUsers(assignment) {
			if h.ftpUsers[oldUser] && !h.revokeFTPS {
				return fmt.Errorf("legacy identity %q has FTPS credentials that cannot be transferred; rerun with --revoke-ftps to explicitly revoke them", oldUser)
			}
			if checkedOldUsers[oldUser] {
				continue
			}
			home, exists, err := h.accountHome(ctx, oldUser)
			if err != nil {
				return fmt.Errorf("inspect legacy account %q: %w", oldUser, err)
			}
			if !exists {
				// An account removed earlier holds no credentials, processes
				// or quota to revoke; the seal re-owns its orphaned files.
				h.oldHomes[oldUser] = ""
				checkedOldUsers[oldUser] = true
				continue
			}
			if !affectedRoots[filepath.Clean(home)] {
				return fmt.Errorf("legacy account %q has its home outside the affected site set", oldUser)
			}
			h.oldHomes[oldUser] = filepath.Clean(home)
			if err := h.requireNoProcesses(ctx, oldUser); err != nil {
				return err
			}
			checkedOldUsers[oldUser] = true
		}
		if home, exists, err := h.accountHome(ctx, assignment.Username); err != nil {
			return fmt.Errorf("inspect target account %q: %w", assignment.Username, err)
		} else if exists && filepath.Clean(home) != siteRoot {
			return fmt.Errorf("target account %q already belongs to another site", assignment.Username)
		} else if exists {
			if err := h.requireNoProcesses(ctx, assignment.Username); err != nil {
				return err
			}
		}
		if access := h.access[assignment.Site]; access.State == "pending" {
			return fmt.Errorf("site %q has pending SSH/SFTP state; reconcile it before identity reassignment", assignment.Site)
		} else if access.SFTPEnabled || access.ShellEnabled {
			for _, key := range access.Keys {
				if len(key.PublicKey) == 0 || len(key.PublicKey) > 8192 || strings.ContainsAny(key.PublicKey, "\r\n") {
					return fmt.Errorf("site %q has an invalid stored SSH public key", assignment.Site)
				}
				parsed, _, options, _, err := ssh.ParseAuthorizedKey([]byte(key.PublicKey))
				if err != nil || len(options) != 0 || parsed.Type() == "ssh-dss" {
					return fmt.Errorf("site %q has an invalid or unsupported stored SSH public key", assignment.Site)
				}
			}
		}
		profile, ok := h.profiles[assignment.Site]
		if !ok {
			return fmt.Errorf("site %q has no durable resource profile; establish its CPU, memory, PHP-worker, and filesystem quota policy before repair", assignment.Site)
		}
		profile = normalizeRepairResourceProfile(profile)
		if profile.State != "applied" || strings.HasSuffix(profile.FilesystemQuotaState, "pending") {
			return fmt.Errorf("site %q has pending resource or quota state; reconcile it before identity reassignment", assignment.Site)
		}
		if err := validateRepairResourceProfile(profile); err != nil {
			return fmt.Errorf("site %q resource profile: %w", assignment.Site, err)
		}
		if profile.DiskMB > 0 && profile.FilesystemQuotaState != "enforced" || profile.DiskMB == 0 && profile.FilesystemQuotaState != "none" {
			return fmt.Errorf("site %q quota profile state does not match its configured limits", assignment.Site)
		}
	}
	return nil
}

func (h *identityRepairHost) requireNoProcesses(ctx context.Context, username string) error {
	output, err := h.commandRunner(ctx, "pgrep", nil, "-u", username)
	if err == nil {
		if len(bytes.TrimSpace(output)) > 0 {
			return fmt.Errorf("processes are still running as site account %q", username)
		}
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return nil
	}
	return fmt.Errorf("check for processes owned by site account %q: %w", username, err)
}

func (h *identityRepairHost) accountHome(ctx context.Context, username string) (string, bool, error) {
	output, err := h.commandRunner(ctx, "getent", nil, "passwd", username)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
			return "", false, nil
		}
		return "", false, err
	}
	fields := strings.Split(strings.TrimSpace(string(output)), ":")
	if len(fields) < 6 || fields[0] != username {
		return "", false, nil
	}
	return fields[5], true, nil
}

func (h *identityRepairHost) verifyNoRepairHardlinks(ctx context.Context, siteRoot string) error {
	info, err := os.Lstat(siteRoot)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("site root must be a real directory")
	}
	output, err := h.commandRunner(ctx, "find", nil, "-P", siteRoot, "-xdev", "-type", "f", "-links", "+1", "-print", "-quit")
	if err != nil {
		return fmt.Errorf("inspect hard links: %w", err)
	}
	if len(bytes.TrimSpace(output)) != 0 {
		return errors.New("hard-linked files exist; manual review is required before ownership changes")
	}
	return nil
}

func verifyNoNestedRepairMounts(siteRoot string) error {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("open mount table: %w", err)
	}
	defer file.Close()
	siteRoot = filepath.Clean(siteRoot)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			return errors.New("mount table contains a malformed entry")
		}
		mountpoint := decodeMountField(fields[4])
		rel, err := filepath.Rel(siteRoot, mountpoint)
		if err != nil {
			return fmt.Errorf("compare mountpoint %q: %w", mountpoint, err)
		}
		if rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return fmt.Errorf("nested mountpoint %q", mountpoint)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read mount table: %w", err)
	}
	return nil
}

func decodeMountField(field string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(field)
}

func (h *identityRepairHost) Reconcile(ctx context.Context, assignment siteidentity.RepairAssignment) error {
	for _, previous := range oldRepairUsers(assignment) {
		if previous != assignment.Username {
			oldHome, ok := h.oldHomes[previous]
			if !ok {
				return fmt.Errorf("preflight did not establish the home of legacy account %q", previous)
			}
			// The helper manages an account only through the site that is its
			// home, so a shared account is revoked once, via that site.
			if oldHome == "" || oldHome != filepath.Clean(filepath.Join(h.webRoot, "sites", assignment.Site)) {
				continue
			}
			if h.ftpUsers[previous] {
				if _, err := h.commandRunner(ctx, h.siteCtl, nil, "ftp", assignment.Site, previous, "0"); err != nil {
					return fmt.Errorf("revoke old FTPS credential: %w", err)
				}
			}
			if _, err := h.commandRunner(ctx, h.siteCtl, nil, "access", assignment.Site, previous, "0", "0"); err != nil {
				return fmt.Errorf("revoke old SSH access: %w", err)
			}
		}
	}
	output, err := h.commandRunner(ctx, h.siteCtl, nil, "seal", assignment.Site, assignment.Username)
	if err != nil {
		return fmt.Errorf("apply filesystem ownership and PHP-FPM identity: %w", err)
	}
	// Like the broker, refuse to continue if the helper sealed a different
	// account than the one this repair assigned.
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if reported := strings.TrimSpace(lines[len(lines)-1]); reported != assignment.Username {
		return fmt.Errorf("site helper sealed account %q, expected %q", reported, assignment.Username)
	}
	if err := rootbroker.SecureSealSiteTree(ctx, filepath.Join(h.webRoot, "sites", assignment.Site, "public")); err != nil {
		return fmt.Errorf("secure public document tree: %w", err)
	}
	access := h.access[assignment.Site]
	var keyBytes []byte
	if access.SFTPEnabled || access.ShellEnabled {
		for _, key := range access.Keys {
			keyBytes = append(keyBytes, []byte(key.PublicKey+"\n")...)
		}
	}
	if _, err := h.commandRunner(ctx, h.siteCtl, keyBytes, "access", assignment.Site, assignment.Username, boolDigit(access.SFTPEnabled), boolDigit(access.ShellEnabled)); err != nil {
		return fmt.Errorf("restore SSH/SFTP policy: %w", err)
	}
	profile, ok := h.profiles[assignment.Site]
	if !ok {
		return nil
	}
	profile = normalizeRepairResourceProfile(profile)
	if profile.FilesystemQuotaState == "apply-pending" || profile.FilesystemQuotaState == "clear-pending" {
		return errors.New("filesystem quota state is pending and requires operator reconciliation")
	}
	if profile.Account != "" {
		if _, err := h.commandRunner(ctx, h.appCtl, nil, "account-resource-apply", profile.Account, number(profile.CPUPercent), number(profile.CPUWeight), number(profile.MemoryHighMB), number(profile.MemoryMB), number(profile.IOWeight), number(profile.TasksMax)); err != nil {
			return fmt.Errorf("restore hosting-account resource profile: %w", err)
		}
	}
	if _, err := h.commandRunner(ctx, h.appCtl, nil, "resource-apply", assignment.Site, assignment.Username, number(profile.CPUPercent), number(profile.CPUWeight), number(profile.MemoryHighMB), number(profile.MemoryMB), number(profile.IOWeight), number(profile.TasksMax), profile.Account); err != nil {
		return fmt.Errorf("restore site resource profile: %w", err)
	}
	if _, err := h.commandRunner(ctx, h.siteCtl, nil, "resources", assignment.Site, assignment.Username, number(profile.PHPWorkers)); err != nil {
		return fmt.Errorf("restore PHP worker limit: %w", err)
	}
	// A new account starts without a quota, so a profile without filesystem
	// limits needs no quota call. Clearing one would require user quotas on
	// hosts that never enabled them, as the panel's own apply path avoids.
	if profile.DiskMB > 0 || profile.Inodes > 0 {
		if _, err := h.commandRunner(ctx, h.siteCtl, nil, "quota", assignment.Site, assignment.Username, number(profile.DiskMB), number(profile.Inodes)); err != nil {
			return fmt.Errorf("restore site disk quota: %w", err)
		}
	}
	return nil
}

func (h *identityRepairHost) Verify(ctx context.Context, assignment siteidentity.RepairAssignment) error {
	output, err := h.commandRunner(ctx, "getent", nil, "passwd", assignment.Username)
	if err != nil {
		return fmt.Errorf("resolve new Unix account: %w", err)
	}
	fields := strings.Split(strings.TrimSpace(string(output)), ":")
	wantHome := filepath.Join(h.webRoot, "sites", assignment.Site)
	if len(fields) < 6 || fields[0] != assignment.Username || filepath.Clean(fields[5]) != filepath.Clean(wantHome) {
		return fmt.Errorf("new Unix account home does not match site root %q", wantHome)
	}
	// The helpers keep root-owned state such as .php/resources.env inside
	// the site root; every other path must belong to the new account.
	output, err = h.commandRunner(ctx, "find", nil, "-P", wantHome, "-xdev", "!", "-type", "l", "!", "-user", assignment.Username, "!", "-uid", "0", "-print", "-quit")
	if err != nil {
		return fmt.Errorf("verify site filesystem ownership: %w", err)
	}
	if len(bytes.TrimSpace(output)) > 0 {
		return fmt.Errorf("site tree still contains paths not owned by %q or root: %s", assignment.Username, strings.TrimSpace(string(output)))
	}
	return nil
}

func (h *identityRepairHost) Finalize(ctx context.Context, assignments []siteidentity.RepairAssignment) error {
	seen := map[string]bool{}
	var retired []string
	for _, assignment := range assignments {
		for _, previous := range oldRepairUsers(assignment) {
			if previous == assignment.Username || seen[previous] {
				continue
			}
			seen[previous] = true
			// find rejects an unknown user name, and an account that no
			// longer exists cannot own anything by name.
			if _, exists, err := h.accountHome(ctx, previous); err != nil {
				return fmt.Errorf("inspect retired identity %q: %w", previous, err)
			} else if exists {
				retired = append(retired, previous)
			}
		}
	}
	if len(retired) == 0 {
		return nil
	}
	// One scan of the sites tree covers every retired account.
	args := []string{"-P", filepath.Join(h.webRoot, "sites"), "-xdev", "("}
	for index, user := range retired {
		if index > 0 {
			args = append(args, "-o")
		}
		args = append(args, "-user", user)
	}
	args = append(args, ")", "-print", "-quit")
	output, err := h.commandRunner(ctx, "find", nil, args...)
	if err != nil {
		return fmt.Errorf("verify retired identities no longer own site data: %w", err)
	}
	if path := strings.TrimSpace(string(output)); path != "" {
		return fmt.Errorf("a retired identity still owns %s under the sites tree; inspect before restarting", path)
	}
	return nil
}

func oldRepairUsers(assignment siteidentity.RepairAssignment) []string {
	users := make([]string, 0, 2)
	if assignment.LegacyUser != "" {
		users = append(users, assignment.LegacyUser)
	}
	if assignment.PreviousUser != "" && assignment.PreviousUser != assignment.LegacyUser {
		users = append(users, assignment.PreviousUser)
	}
	return users
}

func readRepairState[T any](path string, dst *T) error {
	data, err := readOptionalBounded(path, 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

func readOptionalBounded(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s exceeds %d-byte limit", filepath.Base(path), max)
	}
	return data, nil
}

func runRepairCommand(ctx context.Context, command string, stdin []byte, args ...string) ([]byte, error) {
	commandTimeout := 30 * time.Second
	base := filepath.Base(command)
	if base == "find" || base == "stepanel-sitectl" && len(args) > 0 && args[0] == "seal" {
		commandTimeout = 2 * time.Hour
	} else if base == "stepanel-sitectl" || base == "stepanel-appctl" {
		commandTimeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, args...)
	if base == "stepanel-sitectl" {
		cmd.Env = append(os.Environ(), "STEPANEL_IDENTITY_REPAIR_OFFLINE=1")
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out boundedRepairOutput
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%s failed: %w: %s", filepath.Base(command), err, strings.TrimSpace(out.String()))
	}
	return out.Bytes(), nil
}

type boundedRepairOutput struct{ bytes.Buffer }

func (b *boundedRepairOutput) Write(p []byte) (int, error) {
	const limit = 64 << 10
	remaining := limit - b.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func boolDigit(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func number(value int) string { return fmt.Sprintf("%d", value) }

func validateRepairResourceProfile(profile repairResourceProfile) error {
	profile = normalizeRepairResourceProfile(profile)
	if profile.CPUPercent < 25 || profile.CPUPercent > 6400 || profile.CPUWeight < 1 || profile.CPUWeight > 10000 || profile.MemoryMB < 64 || profile.MemoryMB > 1048576 || profile.MemoryHighMB < 64 || profile.MemoryHighMB > profile.MemoryMB || profile.IOWeight < 1 || profile.IOWeight > 10000 || profile.TasksMax < 16 || profile.TasksMax > 100000 || profile.PHPWorkers < 1 || profile.PHPWorkers > 512 || profile.DiskMB < 0 || profile.Inodes < 0 {
		return errors.New("limits are missing or outside the supported range")
	}
	if (profile.DiskMB == 0) != (profile.Inodes == 0) || profile.DiskMB > 0 && (profile.DiskMB < 64 || profile.DiskMB > 1048576 || profile.Inodes < 1000 || profile.Inodes > 1000000000) {
		return errors.New("disk and inode quota limits are incomplete or below the supported minimum")
	}
	return nil
}

func normalizeRepairResourceProfile(profile repairResourceProfile) repairResourceProfile {
	if profile.CPUWeight == 0 {
		profile.CPUWeight = 100
	}
	if profile.IOWeight == 0 {
		profile.IOWeight = 100
	}
	if profile.MemoryHighMB == 0 {
		profile.MemoryHighMB = profile.MemoryMB * 90 / 100
		if profile.MemoryHighMB < 64 {
			profile.MemoryHighMB = 64
		}
	}
	if profile.FilesystemQuotaState == "" {
		if profile.DiskMB > 0 || profile.Inodes > 0 {
			profile.FilesystemQuotaState = "enforced"
		} else {
			profile.FilesystemQuotaState = "none"
		}
	}
	return profile
}
