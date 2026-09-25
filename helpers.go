package main

import (
	"context"
	h "github.com/cyberducttape/StePanel/internal/helper"
	"io"
	"os"
	"os/exec"
	"time"
)

// Type aliases for backward compatibility
type boundedBuffer = h.BoundedBuffer

const maxCommandOutput = h.MaxCommandOutput

// Operation-specific timeout classes (re-exported from internal/helper)
const (
	helperConfigMutationTimeout     = h.ConfigMutationTimeout
	helperServiceLifecycleTimeout   = h.ServiceLifecycleTimeout
	helperPackageBuildTimeout       = h.PackageBuildTimeout
	helperDatabaseOperationTimeout  = h.DatabaseOperationTimeout
	helperContainerOperationTimeout = h.ContainerOperationTimeout
	helperBackupRestoreTimeout      = h.BackupRestoreTimeout
	// helperCommandTimeout is deprecated - use operation-specific timeouts instead
	helperCommandTimeout = h.ConfigMutationTimeout // default for backward compatibility
)

func runBoundedCommand(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	return h.RunBoundedCommand(ctx, cmd)
}

func runBoundedCommandLimit(ctx context.Context, cmd *exec.Cmd, limit int) ([]byte, error) {
	return h.RunBoundedCommandLimit(ctx, cmd, limit)
}

func runBoundedCommandInput(ctx context.Context, cmd *exec.Cmd, input io.Reader) ([]byte, error) {
	return h.RunBoundedCommandInput(ctx, cmd, input)
}

func helperCommand(cfg Config, path string, args ...string) *exec.Cmd {
	return h.HelperCommand(cfg.Sudo, path, args...)
}

func helperCommandContext(ctx context.Context, cfg Config, path string, args ...string) *exec.Cmd {
	return h.HelperCommandContext(ctx, cfg.Sudo, path, args...)
}

func safePath(root string, parts ...string) (string, error) {
	return h.SafePath(root, parts...)
}

func runHelperCommand(ctx context.Context, cfg Config, path string, args ...string) error {
	return h.RunHelperCommand(ctx, cfg.Sudo, path, args...)
}

func runHelperCommandWithTimeout(ctx context.Context, cfg Config, timeout time.Duration, path string, args ...string) error {
	return h.RunHelperCommandWithTimeout(ctx, cfg.Sudo, path, timeout, args...)
}

func siteHelper(cfg Config, action, site string) error {
	return h.SiteHelper(cfg.Sudo, cfg.SiteCtl, action, site)
}

func openRegularNoFollow(path string, expected os.FileInfo) (*os.File, os.FileInfo, error) {
	return h.OpenRegularNoFollow(path, expected)
}

func openWriteNoFollow(path string, mode os.FileMode) (*os.File, error) {
	return h.OpenWriteNoFollow(path, mode)
}

func sameFileInfo(a, b os.FileInfo) bool {
	return h.SameFileInfo(a, b)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	return h.WriteAtomic(path, data, mode)
}

func ensureInside(root, target string) error {
	return h.EnsureInside(root, target)
}

func rejectSymlinkParents(path, root string) error {
	return h.RejectSymlinkParents(path, root)
}

func acquireProcessLock(path string) (*os.File, error) {
	return h.AcquireProcessLock(path)
}
