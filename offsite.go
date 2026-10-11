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
	info, err := os.Stat(result.Path)
	if err != nil {
		return fmt.Errorf("stat offsite backup: %w", err)
	}
	if !info.IsDir() {
		return errors.New("offsite backup must be a published directory")
	}
	backupRoot, err := filepath.Abs(cfg.BackupRoot)
	if err != nil {
		return fmt.Errorf("resolve backup root: %w", err)
	}
	backupPath, err := filepath.Abs(result.Path)
	if err != nil || filepath.Dir(backupPath) != backupRoot || !validBackupName(filepath.Base(backupPath)) {
		return errors.New("offsite backup path is outside the configured backup root")
	}
	destination, err := buildOffsiteRemoteRoot(cfg.OffsiteTarget, result.Site, filepath.Base(backupPath))
	if err != nil {
		return err
	}
	size, err := offsiteBackupDirectorySize(backupPath)
	if err != nil {
		return fmt.Errorf("measure offsite backup directory: %w", err)
	}
	ctx, cancel := context.WithTimeout(parent, offsiteTransferTimeout(size))
	defer cancel()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("create offsite upload identity: %w", err)
	}
	// Keep the temporary prefix beside, not beneath, the final prefix so
	// provider-side moveto can promote it atomically as a directory.
	temporary := destination + ".stepanel-upload-" + fmt.Sprintf("%x", nonce[:])
	completed := false
	defer func() {
		if completed {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		_, _ = runOffsiteRclone(cleanupCtx, "purge", temporary)
	}()
	copyArgs := append([]string{"copy", backupPath, temporary}, offsiteRcloneTransferArgs(maxOffsiteObjectBytes)...)
	if output, err := runOffsiteRclone(ctx, copyArgs...); err != nil {
		return fmt.Errorf("offsite upload failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if output, err := runOffsiteRclone(ctx, "check", backupPath, temporary, "--one-way"); err != nil {
		return fmt.Errorf("offsite upload verification failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if output, err := runOffsiteRclone(ctx, "moveto", temporary, destination); err != nil {
		return fmt.Errorf("publish offsite backup failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	marker, err := os.CreateTemp("", "stepanel-offsite-complete-")
	if err != nil {
		return fmt.Errorf("create offsite completion marker: %w", err)
	}
	markerPath := marker.Name()
	defer os.Remove(markerPath)
	if _, err := marker.WriteString("STEPANEL_OFFSITE_COMPLETE_V1\n"); err != nil {
		_ = marker.Close()
		return fmt.Errorf("write offsite completion marker: %w", err)
	}
	if err := marker.Close(); err != nil {
		return fmt.Errorf("close offsite completion marker: %w", err)
	}
	markerRemote, err := offsiteRemoteObject(destination, ".stepanel-complete")
	if err != nil {
		return err
	}
	if output, err := runOffsiteRclone(ctx, "copyto", markerPath, markerRemote, "--immutable"); err != nil {
		return fmt.Errorf("publish offsite completion marker failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	completed = true
	return nil
}

func runOffsiteRclone(ctx context.Context, args ...string) ([]byte, error) {
	// Keep the executable fixed and append operands after construction so
	// provider-controlled paths remain distinct argument values.
	cmd := exec.CommandContext(ctx, "rclone")
	cmd.Args = append(cmd.Args, args...)
	cmd.Env = cloudCommandEnv()
	return runBoundedCommand(ctx, cmd)
}

func runOffsiteRcloneLimit(ctx context.Context, limit int, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "rclone")
	cmd.Args = append(cmd.Args, args...)
	cmd.Env = cloudCommandEnv()
	return runBoundedCommandLimit(ctx, cmd, limit)
}

func offsiteBackupDirectorySize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup contains symlink %s", filepath.Base(path))
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || total > maxOffsiteObjectBytes-info.Size() {
			return errors.New("offsite backup directory exceeds object size limit")
		}
		total += info.Size()
		return nil
	})
	return total, err
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
	remoteRoot, err := buildOffsiteRemoteRoot(cfg.OffsiteTarget, site, backupName)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := downloadOffsiteObjectLimit(parent, remoteRoot, root, "manifest.json", maxOffsiteManifestBytes); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := downloadOffsiteObjectLimit(parent, remoteRoot, root, ".stepanel-complete", maxOffsiteMarkerBytes); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("offsite backup is not complete: %w", err)
	}
	markerData, err := os.ReadFile(filepath.Join(root, ".stepanel-complete"))
	if err != nil || string(markerData) != "STEPANEL_OFFSITE_COMPLETE_V1\n" {
		cleanup()
		return "", func() {}, errors.New("offsite backup completion marker is invalid")
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
	if err := downloadOffsiteObjectLimit(parent, remoteRoot, root, manifest.Archive, maxOffsiteObjectBytes); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := downloadOffsiteObjectLimit(parent, remoteRoot, root, manifest.Archive+".sha256", maxOffsiteChecksumBytes); err != nil {
		cleanup()
		return "", func() {}, err
	}
	// A signed backup must retain its signature. Unsigned backups do not have
	// this object, so absence is allowed and strict backup verification enforces the
	// configured signing policy.
	ctx, cancel := context.WithTimeout(parent, offsiteTransferTimeout(maxOffsiteSignatureBytes))
	defer cancel()
	signaturePath := filepath.Join(root, "manifest.sig")
	remoteSignature, err := offsiteRemoteObject(remoteRoot, "manifest.sig")
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	copyArgs := append([]string{"copyto", remoteSignature, signaturePath}, offsiteRcloneTransferArgs(maxOffsiteSignatureBytes)...)
	_, copyErr := runOffsiteRclone(ctx, copyArgs...)
	if copyErr == nil {
		if info, statErr := os.Stat(signaturePath); statErr != nil || info.Size() > maxOffsiteSignatureBytes {
			copyErr = errors.New("downloaded offsite signature exceeds the object limit")
		}
	}
	if copyErr != nil {
		_ = os.Remove(signaturePath)
	}
	return root, cleanup, nil
}

func downloadOffsiteObject(parent context.Context, remoteRoot, localRoot, object string) error {
	return downloadOffsiteObjectLimit(parent, remoteRoot, localRoot, object, maxOffsiteObjectBytes)
}

func downloadOffsiteObjectLimit(parent context.Context, remoteRoot, localRoot, object string, maxBytes int64) error {
	if !validOffsiteObjectName(object) {
		return errors.New("invalid offsite object name")
	}
	if maxBytes <= 0 {
		return errors.New("offsite object size limit must be positive")
	}
	ctx, cancel := context.WithTimeout(parent, offsiteTransferTimeout(maxBytes))
	defer cancel()
	remote, err := offsiteRemoteObject(remoteRoot, object)
	if err != nil {
		return err
	}
	local, err := safePath(localRoot, object)
	if err != nil {
		return fmt.Errorf("invalid offsite local object path: %w", err)
	}
	copyArgs := append([]string{"copyto", remote, local}, offsiteRcloneTransferArgs(maxBytes)...)
	output, err := runOffsiteRclone(ctx, copyArgs...)
	if err != nil {
		return fmt.Errorf("download offsite backup object %s: %w: %s", object, err, strings.TrimSpace(string(output)))
	}
	info, err := os.Stat(local)
	if err != nil {
		return fmt.Errorf("stat downloaded offsite backup object %s: %w", object, err)
	}
	if info.Size() > maxBytes {
		return fmt.Errorf("downloaded offsite backup object %s exceeds the %d-byte limit", object, maxBytes)
	}
	return nil
}

func buildOffsiteRemoteRoot(target, site, backupName string) (string, error) {
	if err := validateOffsiteTarget(target); err != nil || safeUser(site) == "" || !validBackupName(backupName) {
		return "", errors.New("invalid offsite remote identity")
	}
	return strings.TrimRight(target, "/") + "/" + site + "/" + backupName, nil
}

func validOffsiteObjectName(object string) bool {
	if object == "" || strings.HasPrefix(object, "-") || strings.ContainsAny(object, "/\\\x00\r\n") {
		return false
	}
	for _, r := range object {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}

func offsiteRemoteObject(root, object string) (string, error) {
	if strings.ContainsAny(root, "\x00\r\n") || !validOffsiteObjectName(object) {
		return "", errors.New("invalid offsite remote object")
	}
	return root + "/" + object, nil
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
const maxOffsiteMarkerBytes = 1024
const maxOffsiteChecksumBytes = 4096
const maxOffsiteSignatureBytes = 4096

// This admission limit applies before archive verification and extraction. It
// is intentionally explicit so a remote object cannot consume an unbounded
// amount of restore-disk space.
const maxOffsiteObjectBytes = 20 << 30

const (
	offsiteMinimumRate    = 1 << 20 // 1 MiB/s prevents a fixed wall-clock cutoff on slow links.
	offsiteMinimumTimeout = 2 * time.Hour
	offsiteMaximumTimeout = 7 * 24 * time.Hour
	offsiteStallTimeout   = 30 * time.Minute
	offsiteConnectTimeout = 5 * time.Minute
)

func offsiteTransferTimeout(size int64) time.Duration {
	if size < 0 {
		size = 0
	}
	seconds := size / offsiteMinimumRate
	if size%offsiteMinimumRate != 0 {
		seconds++
	}
	maxSeconds := int64(offsiteMaximumTimeout / time.Second)
	if seconds >= maxSeconds {
		return offsiteMaximumTimeout
	}
	transfer := time.Duration(seconds) * time.Second
	if transfer < offsiteMinimumTimeout {
		return offsiteMinimumTimeout
	}
	if transfer > offsiteMaximumTimeout {
		return offsiteMaximumTimeout
	}
	return transfer
}

func offsiteRcloneTransferArgs(maxSize int64) []string {
	args := []string{"--immutable", "--timeout", offsiteStallTimeout.String(), "--contimeout", offsiteConnectTimeout.String(), "--retries", "3", "--low-level-retries", "10", "--retries-sleep", "30s", "--stats", "1m", "--stats-one-line"}
	if maxSize > 0 {
		args = append(args, "--max-size", strconv.FormatInt(maxSize, 10))
	}
	return args
}

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
	output, err := runOffsiteRcloneLimit(ctx, maxOffsiteListingBytes, "lsf", strings.TrimRight(cfg.OffsiteTarget, "/"), "--recursive", "--files-only", "--include", "/*/*/manifest.json")
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
