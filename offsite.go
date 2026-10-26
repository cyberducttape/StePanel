package stepanel

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/recovery"
	"github.com/cyberducttape/StePanel/internal/state"
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
	if _, err := offsiteBackupDirectorySize(backupPath); err != nil {
		return fmt.Errorf("measure offsite backup directory: %w", err)
	}
	deadline, err := offsiteTransferTimeout()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, deadline)
	defer cancel()
	// A backup's final prefix only ever holds that backup's own objects. rclone
	// copy merges and one-way checks ignore extras, so refuse a destination
	// that already holds anything else instead of publishing a mixed backup.
	// Objects from an interrupted publication of this same backup are allowed:
	// --immutable fails the copy if any of them differ from the local files.
	expected, err := offsiteBackupObjects(backupPath)
	if err != nil {
		return fmt.Errorf("list offsite backup objects: %w", err)
	}
	existing, err := offsiteDestinationObjects(ctx, destination)
	if err != nil {
		return err
	}
	if err := requireOffsiteObjects(destination, existing, expected, false); err != nil {
		return err
	}
	if existing[offsiteCompleteMarker] {
		if err := requireOffsiteObjects(destination, existing, expected, true); err != nil {
			return err
		}
		if output, err := runOffsiteRclone(ctx, "check", backupPath, destination, "--one-way"); err != nil {
			return fmt.Errorf("previously published offsite backup verification failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("create offsite upload identity: %w", err)
	}
	// Upload to a unique staging prefix first so transfer failures never expose
	// a partial backup at its final name. Remote object stores do not generally
	// provide atomic directory renames, so publish the verified objects into the
	// final prefix and write the completion marker only after that copy succeeds.
	// The staging name records its creation time so sweepOffsiteUploadStaging
	// can remove prefixes this cleanup never reaches (process killed, remote
	// unavailable) once no upload could still be using them.
	temporary := fmt.Sprintf("%s%s%d-%x", destination, offsiteUploadStagingInfix, time.Now().Unix(), nonce[:])
	completed := false
	defer func() {
		if completed {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if output, err := runOffsiteRclone(cleanupCtx, "purge", temporary); err != nil {
			log.Printf("remove failed offsite upload staging prefix %s (the periodic sweep will retry): %v: %s", temporary, err, strings.TrimSpace(string(output)))
		}
	}()
	copyArgs := append([]string{"copy", backupPath, temporary}, offsiteRcloneTransferArgs(maxOffsiteObjectBytes)...)
	if output, err := runOffsiteRclone(ctx, copyArgs...); err != nil {
		return fmt.Errorf("offsite upload failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if output, err := runOffsiteRclone(ctx, "check", backupPath, temporary, "--one-way"); err != nil {
		return fmt.Errorf("offsite upload verification failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	copyFinalArgs := append([]string{"copy", temporary, destination}, offsiteRcloneTransferArgs(maxOffsiteObjectBytes)...)
	if output, err := runOffsiteRclone(ctx, copyFinalArgs...); err != nil {
		return fmt.Errorf("publish offsite backup objects failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if output, err := runOffsiteRclone(ctx, "check", backupPath, destination, "--one-way"); err != nil {
		return fmt.Errorf("published offsite backup verification failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	published, err := offsiteDestinationObjects(ctx, destination)
	if err != nil {
		return err
	}
	if err := requireOffsiteObjects(destination, published, expected, true); err != nil {
		return err
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
	markerRemote, err := offsiteRemoteObject(destination, offsiteCompleteMarker)
	if err != nil {
		return err
	}
	if output, err := runOffsiteRclone(ctx, "copyto", markerPath, markerRemote, "--immutable"); err != nil {
		return fmt.Errorf("publish offsite completion marker failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	completed = true
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
	if output, cleanupErr := runOffsiteRclone(cleanupCtx, "purge", temporary); cleanupErr != nil {
		log.Printf("remove verified offsite upload staging prefix %s (the periodic sweep will retry): %v: %s", temporary, cleanupErr, strings.TrimSpace(string(output)))
	}
	cleanupCancel()
	return nil
}

const offsiteCompleteMarker = ".stepanel-complete"

// offsiteUploadStagingInfix joins a backup's final prefix to the unique
// staging prefix its upload writes first:
// <site>/<backup>.stepanel-upload-<unix seconds>-<32 hex nonce>.
const offsiteUploadStagingInfix = ".stepanel-upload-"

// offsiteStagingSweepMargin is added to the transfer deadline before an
// abandoned staging prefix is removed, covering clock skew and the final
// cleanup step that runs after the transfer context ends.
const offsiteStagingSweepMargin = time.Hour

var offsiteStagingSuffixPattern = regexp.MustCompile(`^(?:([0-9]{1,19})-)?([0-9a-f]{32})$`)

// offsiteStagingSweepResult reports one sweep of the offsite repository.
// Legacy prefixes predate timestamped staging names; their age cannot be
// established, so they are reported for manual removal instead of deleted.
type offsiteStagingSweepResult struct {
	Removed []string
	Legacy  []string
}

// sweepOffsiteUploadStaging removes upload staging prefixes older than the
// transfer deadline plus a margin. No upload can still be writing one: every
// upload runs under that deadline from the time recorded in its name.
// Removal failures are collected and the remaining prefixes are still swept.
func sweepOffsiteUploadStaging(parent context.Context, cfg Config, now time.Time) (offsiteStagingSweepResult, error) {
	var result offsiteStagingSweepResult
	if cfg.OffsiteTarget == "" {
		return result, nil
	}
	if err := validateOffsiteTarget(cfg.OffsiteTarget); err != nil {
		return result, err
	}
	deadline, err := offsiteTransferTimeout()
	if err != nil {
		return result, err
	}
	cutoff := now.Add(-(deadline + offsiteStagingSweepMargin))
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	target := strings.TrimRight(cfg.OffsiteTarget, "/")
	output, err := runOffsiteRcloneLimit(ctx, maxOffsiteListingBytes, "lsf", target, "--recursive", "--dirs-only", "--max-depth", "2", "--quiet")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == rcloneExitDirectoryNotFound {
			return result, nil
		}
		return result, fmt.Errorf("list offsite upload staging failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if len(output) >= maxOffsiteListingBytes {
		return result, fmt.Errorf("offsite repository listing exceeds %d bytes; prune it so abandoned uploads can be swept", maxOffsiteListingBytes)
	}
	var failures []error
	for _, line := range strings.Split(string(output), "\n") {
		parts := strings.Split(strings.Trim(strings.TrimSpace(line), "/"), "/")
		if len(parts) != 2 || safeUser(parts[0]) == "" {
			continue
		}
		backupName, suffix, found := strings.Cut(parts[1], offsiteUploadStagingInfix)
		match := offsiteStagingSuffixPattern.FindStringSubmatch(suffix)
		if !found || match == nil || !validBackupName(backupName) {
			continue
		}
		prefix := target + "/" + parts[0] + "/" + parts[1]
		if match[1] == "" {
			result.Legacy = append(result.Legacy, prefix)
			continue
		}
		created, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || time.Unix(created, 0).After(cutoff) {
			continue
		}
		if output, err := runOffsiteRclone(ctx, "purge", prefix); err != nil {
			failures = append(failures, fmt.Errorf("remove abandoned offsite upload staging %s: %w: %s", prefix, err, strings.TrimSpace(string(output))))
			continue
		}
		result.Removed = append(result.Removed, prefix)
	}
	return result, errors.Join(failures...)
}

// offsiteStagingSweepInterval spaces repository listings; abandoned staging
// only becomes eligible after a multi-day transfer deadline.
const offsiteStagingSweepInterval = 6 * time.Hour

func (a *App) runOffsiteStagingSweepLoop(ctx context.Context) {
	if a.Config.OffsiteTarget == "" {
		return
	}
	timer := time.NewTimer(10 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		result, err := sweepOffsiteUploadStaging(ctx, a.Config, time.Now())
		for _, prefix := range result.Removed {
			log.Printf("removed abandoned offsite upload staging prefix %s", prefix)
		}
		if len(result.Legacy) > 0 {
			log.Printf("offsite repository has %d upload staging prefixes from before timestamped staging names (first: %s); remove them with rclone purge once no upload is running", len(result.Legacy), result.Legacy[0])
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			a.observeStateError(state.NewCleanupError("offsite_staging_sweep", err, "offsite upload staging sweep"))
		}
		timer.Reset(offsiteStagingSweepInterval)
	}
}

// rcloneExitDirectoryNotFound is rclone's documented exit status for a
// listing of a directory that does not exist.
const rcloneExitDirectoryNotFound = 3

// offsiteBackupObjects returns the slash-separated relative names of the
// regular files in a published local backup directory.
func offsiteBackupObjects(root string) (map[string]bool, error) {
	objects := make(map[string]bool)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("backup contains non-regular file %s", filepath.Base(path))
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		objects[filepath.ToSlash(rel)] = true
		return nil
	})
	return objects, err
}

// offsiteDestinationObjects lists every file under a backup's final remote
// prefix. A prefix that does not exist yet is empty.
func offsiteDestinationObjects(ctx context.Context, destination string) (map[string]bool, error) {
	output, err := runOffsiteRcloneLimit(ctx, maxOffsiteListingBytes, "lsf", destination, "--recursive", "--files-only", "--quiet")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == rcloneExitDirectoryNotFound {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("list offsite destination failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if len(output) >= maxOffsiteListingBytes {
		return nil, fmt.Errorf("offsite destination listing exceeds %d bytes", maxOffsiteListingBytes)
	}
	objects := make(map[string]bool)
	for _, line := range strings.Split(string(output), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			objects[name] = true
		}
	}
	return objects, nil
}

// requireOffsiteObjects rejects remote objects that are not part of the
// backup (other than the completion marker) and, when complete is set, any
// backup object the remote is missing.
func requireOffsiteObjects(destination string, remote, expected map[string]bool, complete bool) error {
	var unexpected, missing []string
	for name := range remote {
		if !expected[name] && name != offsiteCompleteMarker {
			unexpected = append(unexpected, name)
		}
	}
	if complete {
		for name := range expected {
			if !remote[name] {
				missing = append(missing, name)
			}
		}
	}
	sort.Strings(unexpected)
	sort.Strings(missing)
	if len(unexpected) > 0 {
		return fmt.Errorf("offsite destination %s holds %d objects that are not part of this backup (first: %s); refusing to publish into it", destination, len(unexpected), unexpected[0])
	}
	if len(missing) > 0 {
		return fmt.Errorf("offsite destination %s is missing %d backup objects (first: %s)", destination, len(missing), missing[0])
	}
	return nil
}

func runOffsiteRclone(ctx context.Context, args ...string) ([]byte, error) {
	// Pass the executable and each operand as separate argv entries. Never
	// construct a shell command from provider paths or persisted metadata.
	cmd := exec.CommandContext(ctx, "rclone", args...)
	cmd.Env = cloudCommandEnv()
	return runBoundedCommand(ctx, cmd)
}

func runOffsiteRcloneLimit(ctx context.Context, limit int, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "rclone", args...)
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

func downloadOffsiteBackupContext(parent context.Context, cfg Config, site, backupName string) (string, func(), error) {
	return downloadOffsiteBackupWithLedgerContext(parent, cfg, site, backupName, &capacityLedger{})
}

// downloadOffsiteBackupWithLedgerContext admits the declared archive size
// against the shared capacity ledger before transferring archive bytes. The
// hold stays attached to the returned staging cleanup until the caller has
// finished using the downloaded archive.
func downloadOffsiteBackupWithLedgerContext(parent context.Context, cfg Config, site, backupName string, ledger *capacityLedger) (string, func(), error) {
	if safeUser(site) == "" || !validBackupName(backupName) {
		return "", func() {}, errors.New("invalid offsite backup identity")
	}
	if err := validateOffsiteTarget(cfg.OffsiteTarget); err != nil {
		return "", func() {}, err
	}
	if cfg.OffsiteTarget == "" {
		return "", func() {}, errors.New("offsite backup target is not configured")
	}
	deadline, err := offsiteTransferTimeout()
	if err != nil {
		return "", func() {}, err
	}
	transferCtx, cancelTransfer := context.WithTimeout(parent, deadline)
	defer cancelTransfer()
	if err := os.MkdirAll(cfg.ImportRoot, 0700); err != nil {
		return "", func() {}, fmt.Errorf("create offsite restore root: %w", err)
	}
	root, stageLock, err := createImportStage(cfg.ImportRoot, "offsite-restore-")
	if err != nil {
		return "", func() {}, err
	}
	var reservation *capacityReservation
	cleanup := func() {
		removeImportStage(root, stageLock)
		reservation.release()
	}
	remoteRoot, err := buildOffsiteRemoteRoot(cfg.OffsiteTarget, site, backupName)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := downloadOffsiteObjectLimit(transferCtx, remoteRoot, root, "manifest.json", maxOffsiteManifestBytes); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := downloadOffsiteObjectLimit(transferCtx, remoteRoot, root, offsiteCompleteMarker, maxOffsiteMarkerBytes); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("offsite backup is not complete: %w", err)
	}
	markerData, err := os.ReadFile(filepath.Join(root, offsiteCompleteMarker))
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
	if manifest.Bytes <= 0 || manifest.Bytes > maxOffsiteObjectBytes {
		cleanup()
		return "", func() {}, fmt.Errorf("downloaded offsite manifest declares an archive size outside the 1..%d byte limit", maxOffsiteObjectBytes)
	}
	if ledger == nil {
		ledger = &capacityLedger{}
	}
	reservation, err = ledger.reserve(cfg, "offsite-backup-download", root, []capacityDemand{{Path: root, Bytes: uint64(manifest.Bytes)}})
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("admit offsite archive download: %w", err)
	}
	if err := downloadOffsiteObjectLimit(transferCtx, remoteRoot, root, manifest.Archive, manifest.Bytes); err != nil {
		cleanup()
		return "", func() {}, err
	}
	archivePath := filepath.Join(root, manifest.Archive)
	archiveInfo, err := os.Lstat(archivePath)
	if err != nil || !archiveInfo.Mode().IsRegular() || archiveInfo.Size() != manifest.Bytes {
		cleanup()
		return "", func() {}, errors.New("downloaded offsite archive size does not match its manifest")
	}
	if err := reservation.consume(archiveInfo.Size()); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("record offsite archive capacity consumption: %w", err)
	}
	if err := downloadOffsiteObjectLimit(transferCtx, remoteRoot, root, manifest.Archive+".sha256", maxOffsiteChecksumBytes); err != nil {
		cleanup()
		return "", func() {}, err
	}
	// A signed backup must retain its signature. Unsigned backups do not have
	// this object, so absence is allowed and strict backup verification enforces the
	// configured signing policy.
	ctx, cancel := context.WithTimeout(transferCtx, deadline)
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

func downloadOffsiteObjectLimit(parent context.Context, remoteRoot, localRoot, object string, maxBytes int64) error {
	if !validOffsiteObjectName(object) || !filepath.IsLocal(object) {
		return errors.New("invalid offsite object name")
	}
	if maxBytes <= 0 {
		return errors.New("offsite object size limit must be positive")
	}
	deadline, err := offsiteTransferTimeout()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, deadline)
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
	info, err := os.Lstat(local)
	if err != nil {
		return fmt.Errorf("stat downloaded offsite backup object %s: %w", object, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("downloaded offsite backup object %s is not a regular file", object)
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

// maxOffsiteListingBytes bounds the manifest and completion-marker listing
// used to choose a recovery-proof backup (roughly 50k backups).
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
	offsiteDefaultTransferDeadline = 7 * 24 * time.Hour
	offsiteMinimumTransferDeadline = time.Minute
	offsiteMaximumTransferDeadline = 30 * 24 * time.Hour
	offsiteStallTimeout            = 30 * time.Minute
	offsiteConnectTimeout          = 5 * time.Minute
)

// offsiteTransferTimeout is an operator-selected total cap, not an assumed
// minimum throughput. Rclone's --timeout separately cancels stalled I/O.
func offsiteTransferTimeout() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("STEPANEL_OFFSITE_TRANSFER_TIMEOUT"))
	if raw == "" {
		return offsiteDefaultTransferDeadline, nil
	}
	deadline, err := time.ParseDuration(raw)
	if err != nil || deadline < offsiteMinimumTransferDeadline || deadline > offsiteMaximumTransferDeadline {
		return 0, fmt.Errorf("STEPANEL_OFFSITE_TRANSFER_TIMEOUT must be between 1m and 720h")
	}
	return deadline, nil
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
	output, err := runOffsiteRcloneLimit(ctx, maxOffsiteListingBytes, "lsf", strings.TrimRight(cfg.OffsiteTarget, "/"), "--recursive", "--files-only", "--include", "/*/*/manifest.json", "--include", "/*/*/"+offsiteCompleteMarker)
	if err != nil {
		return nil, fmt.Errorf("list offsite backups failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if len(output) >= maxOffsiteListingBytes {
		return nil, fmt.Errorf("offsite backup listing exceeds %d bytes; prune the repository before running a recovery proof", maxOffsiteListingBytes)
	}
	// A backup is complete only when both its manifest and completion marker
	// are published; an interrupted publication has the manifest alone.
	manifests := make(map[string]bool)
	complete := make(map[string]bool)
	var order []offsiteBackupReference
	for _, line := range strings.Split(string(output), "\n") {
		parts := strings.Split(strings.Trim(strings.TrimSpace(line), "/"), "/")
		// Upload staging prefixes carry a manifest copy but are never complete
		// backups, so a recovery proof must not select them.
		if len(parts) != 3 || safeUser(parts[0]) == "" || !validBackupName(parts[1]) || strings.Contains(parts[1], offsiteUploadStagingInfix) {
			continue
		}
		key := parts[0] + "\x00" + parts[1]
		switch parts[2] {
		case "manifest.json":
			if !manifests[key] {
				manifests[key] = true
				order = append(order, offsiteBackupReference{Site: parts[0], Backup: parts[1]})
			}
		case offsiteCompleteMarker:
			complete[key] = true
		}
	}
	var backups []offsiteBackupReference
	for _, ref := range order {
		if complete[ref.Site+"\x00"+ref.Backup] {
			backups = append(backups, ref)
		}
	}
	if len(backups) == 0 {
		return nil, errors.New("offsite repository contains no complete backups")
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
	root, cleanup, err := downloadOffsiteBackupWithLedgerContext(context.Background(), cfg, ref.Site, ref.Backup, &capacityLedger{})
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
