package sites

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	h "github.com/cyberducttape/StePanel/internal/helper"
	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

// PlacementLookup returns where a site lives in the hosting-account layout
// (ADR-0007), or an error wrapping sitelayout.ErrNotPlaced for a site that
// keeps the legacy per-site layout.
type PlacementLookup func(site string) (sitelayout.Placement, error)

// location is everything the manager needs to publish, replace and remove
// one site's document root.
type location struct {
	// container holds the document root and its preserved previous releases:
	// {webRoot}/sites/{site} for a legacy site, {homeRoot}/{user} for a
	// placed site.
	container string
	// docName is the document root's name inside container.
	docName string
	// docRoot is filepath.Join(container, docName).
	docRoot string
	// stageRoot is the manager staging directory on the same filesystem as
	// docRoot, so activation is a single atomic rename.
	stageRoot string
	// previousPrefix names preserved releases inside container. Placed sites
	// include their directory so sites sharing a home never collide.
	previousPrefix string
	placed         bool
}

// NewAccountManager returns a manager that also serves sites placed inside
// hosting-account homes under homeRoot. Sites lookup reports as unplaced keep
// the legacy layout under webRoot.
func NewAccountManager(webRoot, homeRoot string, lookup PlacementLookup) (*DefaultManager, error) {
	m, err := NewDefaultManager(webRoot)
	if err != nil {
		return nil, err
	}
	homeRoot = filepath.Clean(homeRoot)
	if homeRoot == "" || homeRoot == "." || !filepath.IsAbs(homeRoot) {
		return nil, fmt.Errorf("sites.Manager: homeRoot must be a non-empty absolute path, got %q", homeRoot)
	}
	if lookup == nil {
		return nil, errors.New("sites.Manager: account layout requires a placement lookup")
	}
	m.homeRoot, m.placements = homeRoot, lookup
	return m, nil
}

func (m *DefaultManager) legacyStageRoot() (string, error) {
	return h.SafePath(m.webRoot, "sites", ".stepanel-manager-staging")
}

// homeStageRoot is the manager staging directory for placed sites, or "" when
// the account layout is not configured.
func (m *DefaultManager) homeStageRoot() (string, error) {
	if m.homeRoot == "" {
		return "", nil
	}
	return h.SafePath(m.homeRoot, ".stepanel-manager-staging")
}

// stageRoots lists every manager staging directory this manager owns.
func (m *DefaultManager) stageRoots() ([]string, error) {
	legacy, err := m.legacyStageRoot()
	if err != nil {
		return nil, err
	}
	roots := []string{legacy}
	if home, err := m.homeStageRoot(); err != nil {
		return nil, err
	} else if home != "" {
		roots = append(roots, home)
	}
	return roots, nil
}

// isStageRoot reports whether dir is one of the manager staging directories.
func (m *DefaultManager) isStageRoot(dir string) (bool, error) {
	roots, err := m.stageRoots()
	if err != nil {
		return false, err
	}
	for _, root := range roots {
		if dir == root {
			return true, nil
		}
	}
	return false, nil
}

// location resolves where site name lives. Every manager method derives its
// paths from here.
func (m *DefaultManager) location(name string) (location, error) {
	if !validSiteName.MatchString(name) {
		return location{}, fmt.Errorf("sites.Manager: invalid site name %q", name)
	}
	if m.placements != nil {
		placement, err := m.placements(name)
		switch {
		case err == nil:
			return m.placedLocation(name, placement)
		case !errors.Is(err, sitelayout.ErrNotPlaced):
			return location{}, fmt.Errorf("sites.Manager: look up site placement: %w", err)
		}
	}
	container, err := h.SafePath(m.webRoot, "sites", name)
	if err != nil {
		return location{}, err
	}
	stageRoot, err := m.legacyStageRoot()
	if err != nil {
		return location{}, err
	}
	return location{
		container:      container,
		docName:        "public",
		docRoot:        filepath.Join(container, "public"),
		stageRoot:      stageRoot,
		previousPrefix: ".stepanel-previous-",
	}, nil
}

func (m *DefaultManager) placedLocation(name string, placement sitelayout.Placement) (location, error) {
	if placement.Site != name {
		return location{}, fmt.Errorf("sites.Manager: placement lookup for %q returned site %q", name, placement.Site)
	}
	if err := placement.Validate(); err != nil {
		return location{}, fmt.Errorf("sites.Manager: invalid placement for %q: %w", name, err)
	}
	container, err := h.SafePath(m.homeRoot, placement.UnixUser)
	if err != nil {
		return location{}, err
	}
	docRoot, err := h.SafePath(container, placement.Directory)
	if err != nil {
		return location{}, err
	}
	stageRoot, err := m.homeStageRoot()
	if err != nil {
		return location{}, err
	}
	return location{
		container:      container,
		docName:        placement.Directory,
		docRoot:        docRoot,
		stageRoot:      stageRoot,
		previousPrefix: ".stepanel-previous-" + placement.Directory + "-",
		placed:         true,
	}, nil
}

// requireAccountHome checks that a placed site's home exists as a real
// directory. Homes are created by the privileged account helper, never by
// the manager.
func requireAccountHome(loc location) error {
	info, err := os.Lstat(loc.container)
	if err != nil {
		return fmt.Errorf("sites.Manager: account home %s is unavailable: %w", loc.container, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("sites.Manager: account home %s must be a real directory", loc.container)
	}
	return nil
}
