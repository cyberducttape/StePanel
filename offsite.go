package stepanel

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/recovery"
)

var offsiteTargetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{1,240}$`)

func validateOffsiteTarget(target string) error {
	if target == "" {
		return nil
	}
	if strings.HasPrefix(target, "-") || !offsiteTargetPattern.MatchString(target) || !strings.Contains(target, ":") {
		return errors.New("STEPANEL_OFFSITE_TARGET must be an rclone destination such as s3:bucket/stepanel")
	}
	return nil
}

func uploadOffsite(cfg Config, result BackupResult) error {
	return uploadOffsiteContext(context.Background(), cfg, result)
}

func uploadOffsiteContext(parent context.Context, cfg Config, result BackupResult) error {
	if cfg.OffsiteTarget == "" {
		return nil
	}
	if err := validateOffsiteTarget(cfg.OffsiteTarget); err != nil {
		return err
	}
	destination := strings.TrimRight(cfg.OffsiteTarget, "/") + "/" + result.Site + "/" + filepath.Base(result.Path)
	ctx, cancel := context.WithTimeout(parent, 2*time.Hour)
	defer cancel()
	cmd := exec.CommandContext(ctx, "rclone", "copyto", result.Path, destination, "--immutable")
	cmd.Env = cloudCommandEnv()
	if output, err := runBoundedCommand(ctx, cmd); err != nil {
		return fmt.Errorf("offsite upload failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (a *App) uploadOffsiteBackup(ctx context.Context, result BackupResult) error {
	if a.Config.OffsiteTarget == "" {
		return nil
	}
	if a.BackupIndex != nil {
		if err := a.BackupIndex.TrackOffsiteBackup(a.Config.OffsiteTarget, result.Site, filepath.Base(result.Path), result.CreatedAt); err != nil {
			return fmt.Errorf("record offsite backup before upload: %w", err)
		}
	}
	if err := uploadOffsiteContext(ctx, a.Config, result); err != nil {
		return err
	}
	if a.BackupIndex != nil {
		if err := a.BackupIndex.MarkOffsiteUploaded(a.Config.OffsiteTarget, result.Site, filepath.Base(result.Path), time.Now().UTC()); err != nil {
			return fmt.Errorf("offsite upload succeeded but recording its status failed: %w", err)
		}
	}
	return nil
}

// downloadOffsiteBackup retrieves only the fixed backup objects for one site
// and backup ID. It never accepts a caller-supplied remote path or performs a
// recursive copy, which keeps the restore boundary tied to the configured
// provider layout.
func downloadOffsiteBackup(cfg Config, site, backupName string) (string, func(), error) {
	return downloadOffsiteBackupContext(context.Background(), cfg, site, backupName)
}

func downloadOffsiteBackupContext(parent context.Context, cfg Config, site, backupName string) (string, func(), error) {
	if safeUser(site) == "" || !validBackupName(backupName) {
		return "", func() {}, errors.New("invalid offsite backup identity")
	}
	if err := validateOffsiteTarget(cfg.OffsiteTarget); err != nil {
		return "", func() {}, err
	}
	if cfg.OffsiteTarget == "" {
		return "", func() {}, errors.New("offsite backup target is not configured")
	}
	if err := os.MkdirAll(cfg.ImportRoot, 0700); err != nil {
		return "", func() {}, fmt.Errorf("create offsite restore root: %w", err)
	}
	root, err := os.MkdirTemp(cfg.ImportRoot, "offsite-restore-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	remoteRoot := strings.TrimRight(cfg.OffsiteTarget, "/") + "/" + site + "/" + backupName
	if err := downloadOffsiteObject(parent, remoteRoot, root, "manifest.json"); err != nil {
		cleanup()
		return "", func() {}, err
	}
	manifestPath := filepath.Join(root, "manifest.json")
	manifestInfo, err := os.Stat(manifestPath)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("stat downloaded offsite manifest: %w", err)
	}
	if manifestInfo.Size() > maxOffsiteManifestBytes {
		cleanup()
		return "", func() {}, errors.New("downloaded offsite manifest exceeds the 8 MiB limit")
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("read downloaded offsite manifest: %w", err)
	}
	var manifest BackupManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil || (manifest.Archive != "backup.tar.gz" && manifest.Archive != "backup.tar.gz.enc") {
		cleanup()
		return "", func() {}, errors.New("downloaded offsite manifest declares an invalid archive")
	}
	for _, object := range []string{manifest.Archive, manifest.Archive + ".sha256"} {
		if err := downloadOffsiteObject(parent, remoteRoot, root, object); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	// A signed backup must retain its signature. Unsigned backups do not have
	// this object, so absence is allowed and strict backup verification enforces the
	// configured signing policy.
	ctx, cancel := context.WithTimeout(parent, 2*time.Hour)
	signaturePath := filepath.Join(root, "manifest.sig")
	cmd := exec.CommandContext(ctx, "rclone", "copyto", remoteRoot+"/manifest.sig", signaturePath, "--immutable", "--max-size", strconv.FormatInt(maxOffsiteObjectBytes, 10))
	cmd.Env = cloudCommandEnv()
	_, copyErr := runBoundedCommand(ctx, cmd)
	cancel()
	if copyErr == nil {
		if info, statErr := os.Stat(signaturePath); statErr != nil || info.Size() > maxOffsiteObjectBytes {
			copyErr = errors.New("downloaded offsite signature exceeds the object limit")
		}
	}
	if copyErr != nil {
		_ = os.Remove(signaturePath)
	}
	return root, cleanup, nil
}

func downloadOffsiteObject(parent context.Context, remoteRoot, localRoot, object string) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Hour)
	defer cancel()
	remote := remoteRoot + "/" + object
	local := filepath.Join(localRoot, object)
	cmd := exec.CommandContext(ctx, "rclone", "copyto", remote, local, "--immutable", "--max-size", strconv.FormatInt(maxOffsiteObjectBytes, 10))
	cmd.Env = cloudCommandEnv()
	output, err := runBoundedCommand(ctx, cmd)
	if err != nil {
		return fmt.Errorf("download offsite backup object %s: %w: %s", object, err, strings.TrimSpace(string(output)))
	}
	info, err := os.Stat(local)
	if err != nil {
		return fmt.Errorf("stat downloaded offsite backup object %s: %w", object, err)
	}
	if info.Size() > maxOffsiteObjectBytes {
		return fmt.Errorf("downloaded offsite backup object %s exceeds the %d-byte limit", object, maxOffsiteObjectBytes)
	}
	return nil
}

func validBackupName(name string) bool {
	if len(name) < 1 || len(name) > 160 || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}

// maxOffsiteListingBytes bounds the manifest listing used to choose a
// recovery-proof backup (roughly 100k manifests).
const maxOffsiteListingBytes = 8 << 20

const maxOffsiteManifestBytes = 8 << 20

// This admission limit applies before archive verification and extraction. It
// is intentionally explicit so a remote object cannot consume an unbounded
// amount of restore-disk space.
const maxOffsiteObjectBytes = 20 << 30

type offsiteBackupReference struct {
	Site   string
	Backup string
}

// listOffsiteBackups lists only manifest objects. The caller can use the
// resulting references to select one complete immutable backup without
// consulting the primary host's local backup index.
func listOffsiteBackupsContext(parent context.Context, cfg Config) ([]offsiteBackupReference, error) {
	if err := validateOffsiteTarget(cfg.OffsiteTarget); err != nil {
		return nil, err
	}
	if cfg.OffsiteTarget == "" {
		return nil, errors.New("offsite backup target is not configured")
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	// Only manifests are listed so a large repository stays well inside the
	// output bound. A listing that reaches the bound is refused rather than
	// silently sampling only the first backups in lexical order.
	cmd := exec.CommandContext(ctx, "rclone", "lsf", strings.TrimRight(cfg.OffsiteTarget, "/"), "--recursive", "--files-only", "--include", "/*/*/manifest.json")
	cmd.Env = cloudCommandEnv()
	output, err := runBoundedCommandLimit(ctx, cmd, maxOffsiteListingBytes)
	if err != nil {
		return nil, fmt.Errorf("list offsite backups failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if len(output) >= maxOffsiteListingBytes {
		return nil, fmt.Errorf("offsite backup listing exceeds %d bytes; prune the repository before running a recovery proof", maxOffsiteListingBytes)
	}
	seen := make(map[string]bool)
	var backups []offsiteBackupReference
	for _, line := range strings.Split(string(output), "\n") {
		parts := strings.Split(strings.Trim(strings.TrimSpace(line), "/"), "/")
		if len(parts) != 3 || parts[2] != "manifest.json" || safeUser(parts[0]) == "" || !validBackupName(parts[1]) {
			continue
		}
		key := parts[0] + "\x00" + parts[1]
		if !seen[key] {
			seen[key] = true
			backups = append(backups, offsiteBackupReference{Site: parts[0], Backup: parts[1]})
		}
	}
	if len(backups) == 0 {
		return nil, errors.New("offsite repository contains no valid backup manifests")
	}
	return backups, nil
}

// RunOffsiteRecoveryProof selects a random immutable backup from the remote
// repository and runs the full archive/application rehearsal. It is intended
// to run from a recovery host configured with the offsite credentials and
// decryption/signing keys, but without access to the primary host's local
// backup tree.
func RunOffsiteRecoveryProof(cfg Config) (backupRehearsalResult, error) {
	if strings.TrimSpace(cfg.RecoveryProofCommand) == "" {
		return backupRehearsalResult{}, errors.New("STEPANEL_RECOVERY_PROOF_COMMAND is required for offsite recovery proof")
	}
	refs, err := listOffsiteBackupsContext(context.Background(), cfg)
	if err != nil {
		return backupRehearsalResult{}, err
	}
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(refs))))
	if err != nil {
		return backupRehearsalResult{}, fmt.Errorf("choose offsite backup: %w", err)
	}
	ref := refs[index.Int64()]
	root, cleanup, err := downloadOffsiteBackupContext(context.Background(), cfg, ref.Site, ref.Backup)
	if err != nil {
		return backupRehearsalResult{}, err
	}
	defer cleanup()
	proofCfg := cfg
	proofCfg.BackupRoot = filepath.Dir(root)
	app := &App{Config: proofCfg}
	request := durableBackupRehearsalRequest{Site: ref.Site, Backup: filepath.Base(root), Actor: "offsite-recovery-proof"}
	run := &rehearsalRun{record: recovery.Rehearsal{Site: ref.Site, Backup: ref.Backup, Trigger: "offsite"}}
	result, err := app.rehearseBackupArchive(context.Background(), request, run)
	if err != nil {
		return backupRehearsalResult{}, fmt.Errorf("offsite recovery proof for %s/%s: %w", ref.Site, ref.Backup, err)
	}
	result.Backup = ref.Backup
	return result, nil
}
