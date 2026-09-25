// Package sites is the internal trust boundary for site lifecycle operations.
//
// The Manager interface is intentionally the single authoritative surface
// for creating, importing, cloning, restoring, updating, deleting,
// suspending and resuming a managed site. Everything under this interface
// re-validates its inputs — a fresh site name pattern, a re-checked path
// containment against the manager's configured web root — even when the
// caller (an HTTP handler, a durable job) has already validated. The
// manager cannot trust that every future caller will remember to.
//
// The manager owns its web root at construction. Callers do NOT supply a
// per-request WebRoot; that would let a bug or malicious request point
// operations at an arbitrary directory tree. Every path is derived from
// the manager's stored root via helper.SafePath, so a request that
// smuggled "../../" or an absolute component through name validation
// would still be caught at the path-containment check.
//
// Placeholder operations (Clone, Restore, UpdateConfiguration, Suspend,
// Resume) currently return ErrNotImplemented rather than a fake nil
// success. Prior code returned nil, which would have let a caller — or a
// future consolidator wiring the manager to real handlers — treat an
// unimplemented no-op as "done". ErrNotImplemented makes that fail loudly
// and is the mechanism that keeps the manager honest until the operations
// are actually wired.
package sites

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	h "github.com/cyberducttape/StePanel/internal/helper"
)

// ErrNotImplemented is returned by manager methods whose real
// implementation is still pending. A caller that receives this MUST NOT
// treat it as success — the operation did not happen.
var ErrNotImplemented = errors.New("sites.Manager: operation not yet implemented")

// validSiteName matches the same shape enforced at the HTTP layer
// (validSiteName in root package importer.go). Duplicated here on purpose:
// the manager is a trust boundary and cannot rely on the caller having
// already validated.
var validSiteName = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// ResourceEnvelope describes resource limits for a site.
type ResourceEnvelope struct {
	CPULimitMillis   int64
	MemoryLimitBytes int64
	StorageLimitGB   int64
}

// Site is the state returned by manager operations that create or return
// a managed site.
type Site struct {
	Name       string
	Status     string
	CreatedAt  time.Time
	WebRoot    string
	PHPVersion string
	Owner      string
}

// Manager is the single authoritative interface for site lifecycle
// operations. No code should bypass this to create, modify, or delete
// sites.
type Manager interface {
	Create(ctx context.Context, req *CreateRequest) (*Site, error)
	ImportArchive(ctx context.Context, req *ImportRequest) (*Site, error)
	Clone(ctx context.Context, req *CloneRequest) (*Site, error)
	Restore(ctx context.Context, req *RestoreRequest) (*Site, error)
	UpdateConfiguration(ctx context.Context, name string, req *UpdateRequest) error
	Delete(ctx context.Context, name string) error
	Suspend(ctx context.Context, name string, reason string) error
	Resume(ctx context.Context, name string) error
}

// Request types deliberately omit WebRoot — the manager owns the
// configured root at construction and callers cannot redirect operations
// at a different tree.

type CreateRequest struct {
	Name         string
	PHPVersion   string
	AccountOwner string
}

type ImportRequest struct {
	Name       string
	ArchiveURL string
	ConfigPath string
}

type CloneRequest struct {
	SourceName   string
	DestName     string
	AccountOwner string
}

type RestoreRequest struct {
	Name      string
	BackupID  string
	Overwrite bool
}

type UpdateRequest struct {
	PHPVersion string
	Resources  *ResourceEnvelope
	Domains    []string
}

// DefaultManager is the production Manager. Construct it once at startup
// and reuse across handlers.
type DefaultManager struct {
	webRoot string
}

// NewDefaultManager validates and stores the configured web root. It
// rejects an empty or non-absolute root because every downstream
// containment check is anchored on this value; a relative root would let
// a caller's CWD change what "inside root" means.
func NewDefaultManager(webRoot string) (*DefaultManager, error) {
	webRoot = filepath.Clean(webRoot)
	if webRoot == "" || webRoot == "." || !filepath.IsAbs(webRoot) {
		return nil, fmt.Errorf("sites.Manager: webRoot must be a non-empty absolute path, got %q", webRoot)
	}
	return &DefaultManager{webRoot: webRoot}, nil
}

// resolveSiteRoot is the single path-derivation primitive every method
// uses. It validates the site name, joins under the configured web root,
// and checks containment + symlink-parent safety via helper.SafePath. The
// returned path is safe to pass to os.RemoveAll or os.MkdirAll.
func (m *DefaultManager) resolveSiteRoot(name string) (string, error) {
	if !validSiteName.MatchString(name) {
		return "", fmt.Errorf("sites.Manager: invalid site name %q", name)
	}
	return h.SafePath(m.webRoot, "sites", name)
}

// resolvePublicRoot returns the site's public/ path with the same safety
// guarantees as resolveSiteRoot.
func (m *DefaultManager) resolvePublicRoot(name string) (string, error) {
	if !validSiteName.MatchString(name) {
		return "", fmt.Errorf("sites.Manager: invalid site name %q", name)
	}
	return h.SafePath(m.webRoot, "sites", name, "public")
}

// Create provisions a new site's directory structure. Consolidation note:
// site provisioning currently happens inside the archive-import
// orchestrator (see importer.go handleArchiveImportJob), which uses the
// SiteTransaction primitive to journal-and-atomically-activate. When the
// full Manager is wired up, Create should route through that same
// SiteTransaction primitive rather than a bare os.MkdirAll, to keep the
// mature transaction pattern as the single provisioning path.
func (m *DefaultManager) Create(ctx context.Context, req *CreateRequest) (*Site, error) {
	if req == nil {
		return nil, errors.New("sites.Manager: nil CreateRequest")
	}
	publicRoot, err := m.resolvePublicRoot(req.Name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(publicRoot); err == nil {
		return nil, fmt.Errorf("sites.Manager: site %q already exists", req.Name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("sites.Manager: inspect target: %w", err)
	}
	if err := os.MkdirAll(publicRoot, 0750); err != nil {
		return nil, fmt.Errorf("sites.Manager: create site directory: %w", err)
	}
	return &Site{
		Name:       req.Name,
		Status:     "ready",
		CreatedAt:  time.Now().UTC(),
		WebRoot:    publicRoot,
		PHPVersion: req.PHPVersion,
		Owner:      req.AccountOwner,
	}, nil
}

// ImportArchive is not implemented here on purpose. Archive import runs
// as a durable job (see importer.go handleArchiveImportJob) so it can
// stage into an isolated directory, wrap the atomic activation in a
// SiteTransaction, and survive a mid-import crash via
// RecoverSiteTransactions. Doing it synchronously from a Manager call
// would lose those properties, so this method returns ErrNotImplemented
// and callers use the durable-job path.
func (m *DefaultManager) ImportArchive(ctx context.Context, req *ImportRequest) (*Site, error) {
	return nil, fmt.Errorf("sites.Manager.ImportArchive: %w — use the archive.import durable job instead", ErrNotImplemented)
}

// Clone is not yet implemented. When wired up it must copy the source
// tree into a staged directory and atomically rename into place under a
// SiteTransaction, so a partial copy cannot leave a half-cloned site
// live at the destination.
func (m *DefaultManager) Clone(ctx context.Context, req *CloneRequest) (*Site, error) {
	return nil, ErrNotImplemented
}

// Restore is not yet implemented. Backup restore currently runs through
// the root-package backup_restore.go job handler, which uses
// SiteTransaction. Consolidation would move that call path to route
// through this Manager method; until that happens, callers must use the
// backup.restore durable job.
func (m *DefaultManager) Restore(ctx context.Context, req *RestoreRequest) (*Site, error) {
	return nil, fmt.Errorf("sites.Manager.Restore: %w — use the backup.restore durable job instead", ErrNotImplemented)
}

// UpdateConfiguration is not yet implemented. The prior placeholder
// returned nil, which would silently claim configuration success.
func (m *DefaultManager) UpdateConfiguration(ctx context.Context, name string, req *UpdateRequest) error {
	if _, err := m.resolveSiteRoot(name); err != nil {
		return err
	}
	return ErrNotImplemented
}

// Delete removes a site's directory tree. This is the highest-risk
// operation the manager exposes, so it re-validates independently even
// though every HTTP caller already validates. The steps:
//
//  1. Name pattern is re-checked (defense in depth: a malformed name
//     that reached this point would be caught here even if the HTTP
//     validator regressed).
//  2. The path is derived from the manager's stored webRoot — the
//     caller cannot influence which tree gets rm -rf'd.
//  3. helper.SafePath enforces "inside webRoot" and rejects any
//     symlinked parent, so `sites/../etc` and `sites/attacker-symlink`
//     both fail.
//  4. The final path is Lstat'd to verify it is a directory (not a
//     symlink at the leaf either), so a race between validation and
//     removal cannot substitute a symlink for the site dir.
func (m *DefaultManager) Delete(ctx context.Context, name string) error {
	siteDir, err := m.resolveSiteRoot(name)
	if err != nil {
		return err
	}
	info, err := os.Lstat(siteDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // Idempotent: deleting an absent site is not an error.
		}
		return fmt.Errorf("sites.Manager: inspect site: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("sites.Manager: refuse to delete site %q: path is a symlink", name)
	}
	if !info.IsDir() {
		return fmt.Errorf("sites.Manager: refuse to delete site %q: path is not a directory", name)
	}
	if err := os.RemoveAll(siteDir); err != nil {
		return fmt.Errorf("sites.Manager: remove site: %w", err)
	}
	return nil
}

// Suspend and Resume are not implemented yet.
func (m *DefaultManager) Suspend(ctx context.Context, name string, reason string) error {
	if _, err := m.resolveSiteRoot(name); err != nil {
		return err
	}
	return ErrNotImplemented
}

func (m *DefaultManager) Resume(ctx context.Context, name string) error {
	if _, err := m.resolveSiteRoot(name); err != nil {
		return err
	}
	return ErrNotImplemented
}
