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
// Placeholder operations (Restore, UpdateConfiguration, Suspend, Resume)
// currently return ErrNotImplemented rather than a fake nil
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
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// Staged trees may only be direct children of sites/ or children of the
// importer staging directory. Restricting the final component prevents a
// caller from smuggling a path expression through ActivateStaged while still
// supporting the two staging layouts used by the application.
var validStagedComponent = regexp.MustCompile(`^(?:[A-Za-z0-9][A-Za-z0-9._-]{0,127}|\.stepanel-[A-Za-z0-9._-]{1,127})$`)
var validStagingPrefix = regexp.MustCompile(`^[A-Za-z0-9._-]{1,48}-?$`)

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
	CreateStaging(ctx context.Context, prefix string) (string, error)
	DiscardStaging(ctx context.Context, stagedRoot string) error
	ImportArchive(ctx context.Context, req *ImportRequest) (*Site, error)
	Clone(ctx context.Context, req *CloneRequest) (*Site, error)
	ActivateStaged(ctx context.Context, name, stagedRoot string) (*Site, error)
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

func (m *DefaultManager) resolveStagedRoot(name, stagedRoot string) (string, string, error) {
	sitesRoot, err := h.SafePath(m.webRoot, "sites")
	if err != nil {
		return "", "", fmt.Errorf("sites.Manager: resolve sites root: %w", err)
	}
	absStaged, err := filepath.Abs(stagedRoot)
	if err != nil {
		return "", "", fmt.Errorf("sites.Manager: resolve staged path: %w", err)
	}
	component := filepath.Base(absStaged)
	if !validStagedComponent.MatchString(component) {
		return "", "", errors.New("sites.Manager: invalid staged path component")
	}
	parent := filepath.Dir(absStaged)
	siteRoot, err := m.resolveSiteRoot(name)
	if err != nil {
		return "", "", err
	}
	if parent == siteRoot {
		if !strings.HasPrefix(component, ".stepanel-previous-") {
			return "", "", errors.New("sites.Manager: direct site path is not a rollback release")
		}
		if _, err := h.SafePath(siteRoot, component); err != nil {
			return "", "", err
		}
		return siteRoot, component, nil
	}
	if parent == sitesRoot {
		if !strings.HasPrefix(component, ".stepanel-") {
			return "", "", errors.New("sites.Manager: direct staged path must be manager-owned")
		}
		if _, err := h.SafePath(sitesRoot, component); err != nil {
			return "", "", err
		}
		return sitesRoot, component, nil
	}
	importRoot, err := h.SafePath(sitesRoot, ".import-staging")
	if err != nil {
		return "", "", fmt.Errorf("sites.Manager: resolve importer staging root: %w", err)
	}
	if parent == importRoot {
		if _, err := h.SafePath(importRoot, component); err != nil {
			return "", "", err
		}
		return importRoot, component, nil
	}
	managerRoot, err := h.SafePath(sitesRoot, ".stepanel-manager-staging")
	if err != nil {
		return "", "", fmt.Errorf("sites.Manager: resolve manager staging root: %w", err)
	}
	if parent == managerRoot {
		if _, err := h.SafePath(managerRoot, component); err != nil {
			return "", "", err
		}
		return managerRoot, component, nil
	}
	return "", "", errors.New("sites.Manager: staged path is not in a manager-owned staging root")
}

// CreateStaging allocates an isolated manager-owned staging directory. The
// returned path is valid input for ActivateStaged, so callers can prepare a
// tree without creating directories directly under the live site root.
func (m *DefaultManager) CreateStaging(ctx context.Context, prefix string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validStagingPrefix.MatchString(prefix) {
		return "", errors.New("sites.Manager: invalid staging prefix")
	}
	sitesRoot, err := h.SafePath(m.webRoot, "sites")
	if err != nil {
		return "", err
	}
	stageParent, err := h.SafePath(sitesRoot, ".stepanel-manager-staging")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(stageParent, 0700); err != nil {
		return "", fmt.Errorf("sites.Manager: create staging root: %w", err)
	}
	stage, err := os.MkdirTemp(stageParent, prefix)
	if err != nil {
		return "", fmt.Errorf("sites.Manager: create staging tree: %w", err)
	}
	return stage, nil
}

// DiscardStaging removes a manager-owned staging tree after a failed
// preparation. It accepts only paths allocated by CreateStaging and refuses
// symlink leaves before removal.
func (m *DefaultManager) DiscardStaging(ctx context.Context, stagedRoot string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sitesRoot, err := h.SafePath(m.webRoot, "sites")
	if err != nil {
		return err
	}
	managerRoot, err := h.SafePath(sitesRoot, ".stepanel-manager-staging")
	if err != nil {
		return err
	}
	absStaged, err := filepath.Abs(stagedRoot)
	if err != nil {
		return err
	}
	if filepath.Dir(absStaged) != managerRoot {
		return errors.New("sites.Manager: staged path is not manager-owned")
	}
	component := filepath.Base(absStaged)
	if !strings.HasPrefix(component, ".stepanel-") || !validStagedComponent.MatchString(component) {
		return errors.New("sites.Manager: invalid staged path component")
	}
	owned, err := h.SafePath(managerRoot, component)
	if err != nil {
		return err
	}
	info, err := os.Lstat(owned)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sites.Manager: inspect staging tree: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("sites.Manager: refuse to remove symlinked staging tree")
	}
	if err := os.RemoveAll(owned); err != nil {
		return fmt.Errorf("sites.Manager: discard staging tree: %w", err)
	}
	return nil
}

// ActivateStaged publishes a fully prepared public tree under the manager's
// configured web root. The caller owns any higher-level recovery journal; the
// manager owns path validation and the final atomic rename.
func (m *DefaultManager) ActivateStaged(ctx context.Context, name, stagedRoot string) (*Site, error) {
	destination, err := m.resolvePublicRoot(name)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stagedParent, stagedName, err := m.resolveStagedRoot(name, stagedRoot)
	if err != nil {
		return nil, err
	}
	// Resolve the requested component through a directory entry obtained from
	// the manager-owned parent. The final filesystem call therefore uses a
	// name supplied by the trusted directory listing, not the original request
	// path expression.
	var stagedEntry string
	var stagedInfo os.FileInfo
	entries, err := os.ReadDir(stagedParent)
	if err != nil {
		return nil, fmt.Errorf("sites.Manager: inspect staging parent: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() == stagedName {
			stagedEntry = filepath.Join(stagedParent, entry.Name())
			stagedInfo, err = entry.Info()
			if err != nil {
				return nil, fmt.Errorf("sites.Manager: inspect staged site: %w", err)
			}
			break
		}
	}
	if stagedEntry == "" {
		return nil, errors.New("sites.Manager: staged site does not exist")
	}
	if stagedInfo.Mode()&os.ModeSymlink != 0 || !stagedInfo.IsDir() {
		return nil, errors.New("sites.Manager: staged site must be a directory")
	}
	if _, err := os.Lstat(destination); err == nil {
		return nil, fmt.Errorf("sites.Manager: destination site %q already exists", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("sites.Manager: inspect activation destination: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return nil, fmt.Errorf("sites.Manager: prepare activation parent: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(stagedEntry, destination); err != nil {
		return nil, fmt.Errorf("sites.Manager: activate staged site: %w", err)
	}
	return &Site{Name: name, Status: "ready", CreatedAt: time.Now().UTC(), WebRoot: destination}, nil
}

// Create provisions a new site's directory structure through a private stage
// and one final rename. A caller cannot observe a partially-created public
// tree, and cancellation before publication removes the stage.
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sitesRoot, err := h.SafePath(m.webRoot, "sites")
	if err != nil {
		return nil, err
	}
	stageParent, err := h.SafePath(sitesRoot, ".stepanel-manager-staging")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stageParent, 0700); err != nil {
		return nil, fmt.Errorf("sites.Manager: create site staging root: %w", err)
	}
	stage, err := os.MkdirTemp(stageParent, ".create-")
	if err != nil {
		return nil, fmt.Errorf("sites.Manager: create site stage: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := os.Mkdir(filepath.Join(stage, "public"), 0750); err != nil {
		return nil, fmt.Errorf("sites.Manager: create staged public directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, filepath.Dir(publicRoot)); err != nil {
		return nil, fmt.Errorf("sites.Manager: publish site directory: %w", err)
	}
	committed = true
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

// Clone copies a site's public tree into an isolated staging directory and
// atomically publishes the destination site directory. Symlinks and special
// files are rejected rather than copied, and cancellation removes the stage
// without exposing a partial destination.
func (m *DefaultManager) Clone(ctx context.Context, req *CloneRequest) (*Site, error) {
	if req == nil {
		return nil, errors.New("sites.Manager: nil CloneRequest")
	}
	if req.SourceName == req.DestName {
		return nil, errors.New("sites.Manager: clone source and destination must differ")
	}
	source, err := m.resolvePublicRoot(req.SourceName)
	if err != nil {
		return nil, err
	}
	destination, err := m.resolveSiteRoot(req.DestName)
	if err != nil {
		return nil, err
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return nil, fmt.Errorf("sites.Manager: inspect clone source: %w", err)
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.IsDir() {
		return nil, errors.New("sites.Manager: clone source must be a directory")
	}
	if _, err := os.Lstat(destination); err == nil {
		return nil, fmt.Errorf("sites.Manager: site %q already exists", req.DestName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("sites.Manager: inspect clone destination: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sitesRoot, err := h.SafePath(m.webRoot, "sites")
	if err != nil {
		return nil, err
	}
	stageParent := filepath.Join(sitesRoot, ".stepanel-manager-staging")
	if err := os.MkdirAll(stageParent, 0700); err != nil {
		return nil, fmt.Errorf("sites.Manager: create clone staging root: %w", err)
	}
	stage, err := os.MkdirTemp(stageParent, req.DestName+"-")
	if err != nil {
		return nil, fmt.Errorf("sites.Manager: create clone stage: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stage)
		}
	}()
	stagePublic := filepath.Join(stage, "public")
	if err := copySiteTree(ctx, source, stagePublic); err != nil {
		return nil, fmt.Errorf("sites.Manager: stage clone: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, destination); err != nil {
		return nil, fmt.Errorf("sites.Manager: activate clone: %w", err)
	}
	committed = true
	return &Site{Name: req.DestName, Status: "ready", CreatedAt: time.Now().UTC(), WebRoot: filepath.Join(destination, "public"), Owner: req.AccountOwner}, nil
}

func copySiteTree(ctx context.Context, source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("clone source contains a non-directory root")
	}
	if err := os.MkdirAll(destination, info.Mode().Perm()|0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		src := filepath.Join(source, entry.Name())
		dst := filepath.Join(destination, entry.Name())
		entryInfo, err := os.Lstat(src)
		if err != nil {
			return err
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse to clone symlink %q", entry.Name())
		}
		if entryInfo.IsDir() {
			if err := copySiteTree(ctx, src, dst); err != nil {
				return err
			}
			continue
		}
		if !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("refuse to clone special file %q", entry.Name())
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, entryInfo.Mode().Perm())
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, &contextReader{ctx: ctx, reader: in})
		closeOutErr := out.Close()
		closeInErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
		if closeInErr != nil {
			return closeInErr
		}
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
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
