package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
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
	for _, object := range []string{"manifest.json", "backup.tar.gz", "backup.tar.gz.sha256"} {
		remote := remoteRoot + "/" + object
		local := filepath.Join(root, object)
		ctx, cancel := context.WithTimeout(parent, 2*time.Hour)
		cmd := exec.CommandContext(ctx, "rclone", "copyto", remote, local, "--immutable")
		cmd.Env = cloudCommandEnv()
		output, copyErr := runBoundedCommand(ctx, cmd)
		cancel()
		if copyErr != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("download offsite backup object %s: %w: %s", object, copyErr, strings.TrimSpace(string(output)))
		}
	}
	// A signed backup must retain its signature. Unsigned backups do not have
	// this object, so absence is allowed and strict backup verification enforces the
	// configured signing policy.
	ctx, cancel := context.WithTimeout(parent, 2*time.Hour)
	cmd := exec.CommandContext(ctx, "rclone", "copyto", remoteRoot+"/manifest.sig", filepath.Join(root, "manifest.sig"), "--immutable")
	cmd.Env = cloudCommandEnv()
	_, copyErr := runBoundedCommand(ctx, cmd)
	cancel()
	if copyErr != nil {
		_ = os.Remove(filepath.Join(root, "manifest.sig"))
	}
	return root, cleanup, nil
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
