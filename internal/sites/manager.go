// Package sites is the internal trust boundary for site lifecycle operations.
//
// Manager is the filesystem primitive surface for managed-site paths,
// staging, publication, cloning, and deletion. Lifecycle orchestration
// (including imports, restores, configuration changes, and privileged
// suspension) belongs to durable jobs and the root broker, not this package.
// Methods here revalidate their inputs and derive paths only from the
// manager's configured root.
//
// The manager owns its web root at construction. Callers do NOT supply a
// per-request WebRoot; that would let a bug or malicious request point
// operations at an arbitrary directory tree. Every path is derived from
// the manager's stored root via helper.SafePath, so a request that
// smuggled "../../" or an absolute component through name validation
// would still be caught at the path-containment check.
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
	"syscall"
	"time"

	h "github.com/cyberducttape/StePanel/internal/helper"
)

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

// Manager is the authoritative interface for unprivileged filesystem
// primitives that create/publish, stage, clone, and delete canonical site
// trees. Durable jobs own lifecycle ordering and the root broker owns
// privileged account, service, and configuration changes.
type Manager interface {
	Create(ctx context.Context, req *CreateRequest) (*Site, error)
	CreateStaging(ctx context.Context, prefix string) (string, error)
	DiscardStaging(ctx context.Context, stagedRoot string) error
	CreateReleaseStaging(ctx context.Context, name, prefix string) (string, error)
	DiscardReleaseStaging(ctx context.Context, name, stagedRoot string) error
	CleanupOrphanedStaging(ctx context.Context, olderThan time.Duration) (int, error)
	ActivateStagedReplacing(ctx context.Context, name, stagedRoot string) (string, error)
	RollbackStagedActivation(ctx context.Context, name, previous string) error
	Clone(ctx context.Context, req *CloneRequest) (*Site, error)
	ActivateStaged(ctx context.Context, name, stagedRoot string) (*Site, error)
	Delete(ctx context.Context, name string) error
}

// Compile-time contract: the authoritative manager surface contains only
// operations implemented by this package. Durable import, restore, and
// configuration workflows belong to their job orchestrators; suspension is
// intentionally not exposed until a reversible privileged workflow exists.
var _ Manager = (*DefaultManager)(nil)

// Request types deliberately omit WebRoot — the manager owns the
// configured root at construction and callers cannot redirect operations
// at a different tree.

type CreateRequest struct {
	Name         string
	PHPVersion   string
	AccountOwner string
}

type CloneRequest struct {
	SourceName   string
	DestName     string
	AccountOwner string
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
// path check is not a substitute for descriptor-relative deletion; Delete
// opens the sites directory and removes the validated name through os.Root.
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
		if !strings.HasPrefix(component, ".stepanel-previous-") && !strings.HasPrefix(component, ".stepanel-release-") {
			return "", "", errors.New("sites.Manager: direct site path is not a manager-owned release")
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
	if err := h.EnsureInside(sitesRoot, absStaged); err != nil {
		return "", "", errors.New("sites.Manager: staged path is outside site root")
	}
	return "", "", errors.New("sites.Manager: staged path is not in a manager-owned staging root")
}

// CreateReleaseStaging allocates a temporary release below the manager-owned
// staging root. Site accounts cannot rename this parent or replace the clone
// destination while an untrusted repository is being fetched.
func (m *DefaultManager) CreateReleaseStaging(ctx context.Context, name, prefix string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !strings.HasPrefix(prefix, ".stepanel-release-") || !validStagingPrefix.MatchString(prefix) {
		return "", errors.New("sites.Manager: invalid release staging prefix")
	}
	if !validSiteName.MatchString(name) {
		return "", fmt.Errorf("sites.Manager: invalid site name %q", name)
	}
	siteRoot, err := m.resolveSiteRoot(name)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(siteRoot)
	if err != nil {
		return "", fmt.Errorf("sites.Manager: inspect release site root: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("sites.Manager: release site root is not a directory")
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
		return "", fmt.Errorf("sites.Manager: create release staging root: %w", err)
	}
	// Keep the entry searchable but not tenant-writable. The panel owns this
	// directory; Git staging directories themselves receive site ownership and
	// a panel ACL from the root broker before cloning.
	if err := os.Chmod(stageParent, 0711); err != nil {
		return "", fmt.Errorf("sites.Manager: secure release staging root: %w", err)
	}
	stage, err := os.MkdirTemp(stageParent, ".stepanel-release-"+name+"-")
	if err != nil {
		return "", fmt.Errorf("sites.Manager: create release staging tree: %w", err)
	}
	return stage, nil
}

// DiscardReleaseStaging removes only a release staging tree allocated under
// the named site's root. Retained previous releases are never accepted.
func (m *DefaultManager) DiscardReleaseStaging(ctx context.Context, name, stagedRoot string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parent, component, err := m.resolveStagedRoot(name, stagedRoot)
	if err != nil {
		return err
	}
	if parent == "" || !strings.HasPrefix(component, ".stepanel-release-") {
		return errors.New("sites.Manager: path is not a release staging tree")
	}
	owned, err := h.SafePath(parent, component)
	if err != nil {
		return err
	}
	info, err := os.Lstat(owned)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sites.Manager: inspect release staging tree: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("sites.Manager: refuse to remove symlinked release staging tree")
	}
	if err := os.RemoveAll(owned); err != nil {
		return fmt.Errorf("sites.Manager: discard release staging tree: %w", err)
	}
	return nil
}

// CleanupOrphanedStaging removes staging trees that a crashed or killed
// operation left behind: per-site release checkouts (.stepanel-release-*)
// and lifecycle staging trees under .stepanel-manager-staging. Only trees
// untouched for longer than olderThan are removed, so a caller passing more
// than the longest operation timeout never deletes a tree a live operation,
// including one in another process, is still using. Previous releases kept
// for rollback are not staging and are left alone.
func (m *DefaultManager) CleanupOrphanedStaging(ctx context.Context, olderThan time.Duration) (int, error) {
	sitesRoot, err := h.SafePath(m.webRoot, "sites")
	if err != nil {
		return 0, fmt.Errorf("sites.Manager: resolve sites root: %w", err)
	}
	entries, err := os.ReadDir(sitesRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("sites.Manager: read sites root: %w", err)
	}
	cutoff := time.Now().Add(-olderThan)
	abandoned := func(entry os.DirEntry) bool {
		info, err := entry.Info()
		return err == nil && entry.IsDir() && !info.ModTime().After(cutoff)
	}
	removed := 0
	for _, siteEntry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if !siteEntry.IsDir() || !validSiteName.MatchString(siteEntry.Name()) {
			continue
		}
		siteRoot, err := m.resolveSiteRoot(siteEntry.Name())
		if err != nil {
			return removed, err
		}
		releases, err := os.ReadDir(siteRoot)
		if err != nil {
			return removed, fmt.Errorf("sites.Manager: read site %q: %w", siteEntry.Name(), err)
		}
		for _, release := range releases {
			if err := ctx.Err(); err != nil {
				return removed, err
			}
			if !strings.HasPrefix(release.Name(), ".stepanel-release-") || !abandoned(release) {
				continue
			}
			if err := m.DiscardReleaseStaging(ctx, siteEntry.Name(), filepath.Join(siteRoot, release.Name())); err != nil {
				return removed, err
			}
			removed++
		}
	}
	stageParent, err := h.SafePath(sitesRoot, ".stepanel-manager-staging")
	if err != nil {
		return removed, fmt.Errorf("sites.Manager: resolve staging root: %w", err)
	}
	staged, err := os.ReadDir(stageParent)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return removed, fmt.Errorf("sites.Manager: read staging root: %w", err)
	}
	for _, entry := range staged {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if !abandoned(entry) {
			continue
		}
		if err := m.DiscardStaging(ctx, filepath.Join(stageParent, entry.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
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

func (m *DefaultManager) inspectStaged(name, stagedRoot string) (string, string, string, error) {
	destination, err := m.resolvePublicRoot(name)
	if err != nil {
		return "", "", "", err
	}
	stagedParent, stagedName, err := m.resolveStagedRoot(name, stagedRoot)
	if err != nil {
		return "", "", "", err
	}
	// Resolve the requested component through a directory entry obtained from
	// the manager-owned parent. The final filesystem call therefore uses a
	// name supplied by the trusted directory listing, not the original request
	// path expression.
	var stagedEntry string
	var stagedInfo os.FileInfo
	entries, err := os.ReadDir(stagedParent)
	if err != nil {
		return "", "", "", fmt.Errorf("sites.Manager: inspect staging parent: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() == stagedName {
			stagedEntry = filepath.Join(stagedParent, entry.Name())
			stagedInfo, err = entry.Info()
			if err != nil {
				return "", "", "", fmt.Errorf("sites.Manager: inspect staged site: %w", err)
			}
			break
		}
	}
	if stagedEntry == "" {
		return "", "", "", errors.New("sites.Manager: staged site does not exist")
	}
	if stagedInfo.Mode()&os.ModeSymlink != 0 || !stagedInfo.IsDir() {
		return "", "", "", errors.New("sites.Manager: staged site must be a directory")
	}
	return filepath.Dir(destination), destination, stagedEntry, nil
}

// ActivateStaged publishes a fully prepared public tree under the manager's
// configured web root. The caller owns any higher-level recovery journal; the
// manager owns path validation and the final atomic rename.
func (m *DefaultManager) ActivateStaged(ctx context.Context, name, stagedRoot string) (*Site, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, destination, stagedEntry, err := m.inspectStaged(name, stagedRoot)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
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
	if err := syncTree(ctx, stagedEntry); err != nil {
		return nil, fmt.Errorf("sites.Manager: persist staged site: %w", err)
	}
	if err := renameDurable(stagedEntry, destination); err != nil {
		return nil, fmt.Errorf("sites.Manager: activate staged site: %w", err)
	}
	return &Site{Name: name, Status: "ready", CreatedAt: time.Now().UTC(), WebRoot: destination}, nil
}

// ActivateStagedReplacing atomically publishes a staged release while
// retaining the current public tree at a manager-generated rollback path.
func (m *DefaultManager) ActivateStagedReplacing(ctx context.Context, name, stagedRoot string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	siteRoot, destination, stagedEntry, err := m.inspectStaged(name, stagedRoot)
	if err != nil {
		return "", err
	}
	// Make the staged release durable before the current release is moved
	// aside, so a crash can never leave an unwritten tree published.
	if err := syncTree(ctx, stagedEntry); err != nil {
		return "", fmt.Errorf("sites.Manager: persist staged release: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	previous := ""
	if info, statErr := os.Lstat(destination); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("sites.Manager: activation destination must be a directory")
		}
		previousName := ".stepanel-previous-" + fmt.Sprintf("%d", time.Now().UTC().UnixNano())
		previous, err = h.SafePath(siteRoot, previousName)
		if err != nil {
			return "", err
		}
		if err := os.Rename(destination, previous); err != nil {
			return "", fmt.Errorf("sites.Manager: preserve current release: %w", err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("sites.Manager: inspect activation destination: %w", statErr)
	}
	if err := ctx.Err(); err != nil {
		return "", restorePreviousRelease(previous, destination, err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return "", restorePreviousRelease(previous, destination, fmt.Errorf("sites.Manager: prepare activation parent: %w", err))
	}
	if err := renameDurable(stagedEntry, destination); err != nil {
		return "", restorePreviousRelease(previous, destination, fmt.Errorf("sites.Manager: activate staged release: %w", err))
	}
	return previous, nil
}

// RollbackStagedActivation removes the active replacement and restores the
// manager-generated previous release. An empty previous path removes the
// newly-created site tree.
func (m *DefaultManager) RollbackStagedActivation(ctx context.Context, name, previous string) error {
	destination, err := m.resolvePublicRoot(name)
	if err != nil {
		return err
	}
	siteRoot, err := m.resolveSiteRoot(name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if info, statErr := os.Lstat(destination); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("sites.Manager: refuse to remove non-directory activation")
		}
		if err := os.RemoveAll(destination); err != nil {
			return fmt.Errorf("sites.Manager: remove failed activation: %w", err)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("sites.Manager: inspect failed activation: %w", statErr)
	}
	if previous == "" {
		return nil
	}
	absPrevious, err := filepath.Abs(previous)
	if err != nil || filepath.Dir(absPrevious) != siteRoot || !strings.HasPrefix(filepath.Base(absPrevious), ".stepanel-previous-") {
		return errors.New("sites.Manager: invalid previous release path")
	}
	previous, err = h.SafePath(siteRoot, filepath.Base(absPrevious))
	if err != nil {
		return err
	}
	if err := os.Rename(previous, destination); err != nil {
		return fmt.Errorf("sites.Manager: restore previous release: %w", err)
	}
	if err := syncDir(siteRoot); err != nil {
		return fmt.Errorf("sites.Manager: persist restored release: %w", err)
	}
	return nil
}

// syncDir fsyncs a directory so renames inside it survive power loss. Tests
// replace it to inject I/O failures.
var syncDir = func(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	if closeErr := directory.Close(); err == nil {
		err = closeErr
	}
	return err
}

// syncTree fsyncs every regular file and directory under root (without
// following symlinks) so staged data is durable before it is published.
// Tests replace it to inject I/O failures.
const syscallNoFollow = syscall.O_NOFOLLOW

var syncTree = func(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return nil
		}
		file, err := os.OpenFile(path, os.O_RDONLY|syscallNoFollow, 0)
		if err != nil {
			return err
		}
		err = file.Sync()
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return fmt.Errorf("sync %s: %w", path, err)
		}
		return nil
	})
}

// renameDurable renames from to to and fsyncs the destination's parent so
// the new entry is durable, not merely atomic. When the fsync fails the
// rename is reversed; if the reversal fails too, the returned error says
// that to remains published so callers never mistake it for a clean failure.
func renameDurable(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	parent := filepath.Dir(to)
	syncErr := syncDir(parent)
	if syncErr == nil {
		return nil
	}
	syncErr = fmt.Errorf("sync %s: %w", parent, syncErr)
	if err := os.Rename(to, from); err != nil {
		return errors.Join(syncErr, fmt.Errorf("%s remains published; reverse rename failed: %w", to, err))
	}
	return syncErr
}

// restorePreviousRelease moves a preserved release back to destination after
// a failed replacement and joins any restore failure onto cause, so a site
// left without its public tree is reported rather than silently masked.
func restorePreviousRelease(previous, destination string, cause error) error {
	if previous == "" {
		return cause
	}
	if err := os.Rename(previous, destination); err != nil {
		return errors.Join(cause, fmt.Errorf("sites.Manager: restore previous release from %s: %w", previous, err))
	}
	if err := syncDir(filepath.Dir(destination)); err != nil {
		return errors.Join(cause, fmt.Errorf("sites.Manager: persist restored previous release: %w", err))
	}
	return cause
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
	if err := renameDurable(stage, filepath.Dir(publicRoot)); err != nil {
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
	if err := renameDurable(stage, destination); err != nil {
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
		in, _, err := h.OpenRegularNoFollow(src, entryInfo)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, entryInfo.Mode().Perm())
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, &contextReader{ctx: ctx, reader: in})
		if copyErr == nil {
			copyErr = out.Sync()
		}
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
	// Staged data must be durable before the publishing rename so a crash
	// cannot expose a site directory whose entries were never written.
	return syncDir(destination)
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

// openSitesRoot opens the sites directory once and validates its immediate
// entry. Site accounts can traverse this directory but must not be able to
// replace entries in it; fail closed if group/other write permission drifts.
func openSitesRoot(webRoot string) (*os.Root, error) {
	web, err := os.OpenRoot(webRoot)
	if err != nil {
		return nil, fmt.Errorf("sites.Manager: open web root: %w", err)
	}
	info, err := web.Lstat("sites")
	if err != nil {
		_ = web.Close()
		return nil, fmt.Errorf("sites.Manager: inspect sites directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		_ = web.Close()
		return nil, errors.New("sites.Manager: sites path must be a real directory")
	}
	if info.Mode().Perm()&0o022 != 0 {
		_ = web.Close()
		return nil, errors.New("sites.Manager: sites directory must not be group/world writable")
	}
	sites, err := web.OpenRoot("sites")
	closeErr := web.Close()
	if err != nil {
		return nil, fmt.Errorf("sites.Manager: open sites directory: %w", err)
	}
	if closeErr != nil {
		_ = sites.Close()
		return nil, fmt.Errorf("sites.Manager: close web root: %w", closeErr)
	}
	return sites, nil
}

func removeSiteAtRoot(ctx context.Context, sites *os.Root, name string) error {
	if !validSiteName.MatchString(name) {
		return fmt.Errorf("sites.Manager: invalid site name %q", name)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := sites.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("sites.Manager: inspect site: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("sites.Manager: refuse to delete site %q: path is a symlink", name)
	}
	if !info.IsDir() {
		return fmt.Errorf("sites.Manager: refuse to delete site %q: path is not a directory", name)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := sites.RemoveAll(name); err != nil {
		return fmt.Errorf("sites.Manager: remove site: %w", err)
	}
	return nil
}

// Delete removes one canonical site relative to an open directory descriptor.
// It validates the name independently, refuses symlink/non-directory leaves,
// and uses os.Root.RemoveAll so replacing a pathname cannot redirect recursive
// deletion outside the originally opened sites tree. The sites parent must
// remain non-writable to customer accounts.
func (m *DefaultManager) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validSiteName.MatchString(name) {
		return fmt.Errorf("sites.Manager: invalid site name %q", name)
	}
	sites, err := openSitesRoot(m.webRoot)
	if err != nil {
		return err
	}
	removeErr := removeSiteAtRoot(ctx, sites, name)
	closeErr := sites.Close()
	if removeErr != nil {
		return removeErr
	}
	if closeErr != nil {
		return fmt.Errorf("sites.Manager: close sites directory: %w", closeErr)
	}
	return nil
}
