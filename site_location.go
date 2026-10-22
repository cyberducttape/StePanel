package stepanel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

// siteLocation mirrors the root-side site location library
// (deploy/integrations/stepanel-site-location.sh, ADR-0007) for the panel.
// Both sides must agree: helpers compare the paths the panel sends with
// their own derivation.
type siteLocation struct {
	// Placed reports a site inside a hosting-account home.
	Placed bool
	// Placement is set for a placed site.
	Placement sitelayout.Placement
	// Home holds the document root: {webRoot}/sites/{site} for a legacy site,
	// {homeRoot}/{user} for a placed site.
	Home string
	// Private holds per-site state beside the document root (PHP state,
	// virtualenv, build artifacts, the staging noindex marker): the legacy
	// site root, or {homeRoot}/{user}/.stepanel/sites/{site}.
	Private string
	// DocRoot is the directory the web server serves.
	DocRoot string
	// PreviousPrefix names releases preserved beside DocRoot in Home. A placed
	// site's prefix includes its directory so sites sharing a home never
	// list, roll back to, or prune each other's releases.
	PreviousPrefix string
}

// siteNamePattern is the managed site name grammar (safeUser, without its
// whitespace trimming).
var siteNamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// siteLocationFor resolves a site through the registered control-plane
// placements. Without a registered database (unit tests, tooling) every site
// is legacy. A placement that cannot be read fails closed.
func siteLocationFor(cfg Config, site string) (siteLocation, error) {
	// Every site path is derived here, often from a request; the anchored
	// pattern keeps separators, "..", spaces and control characters out.
	if !siteNamePattern.MatchString(site) {
		return siteLocation{}, fmt.Errorf("invalid site %q", site)
	}
	if db := sitePlacementDB.Load(); db != nil && strings.TrimSpace(cfg.HomeRoot) != "" {
		placement, err := sitelayout.Get(db, site)
		switch {
		case err == nil:
			home, err := safePath(cfg.HomeRoot, placement.UnixUser)
			if err != nil {
				return siteLocation{}, err
			}
			docRoot, err := safePath(home, placement.Directory)
			if err != nil {
				return siteLocation{}, err
			}
			private, err := safePath(home, ".stepanel", "sites", site)
			if err != nil {
				return siteLocation{}, err
			}
			return siteLocation{Placed: true, Placement: placement, Home: home, Private: private, DocRoot: docRoot, PreviousPrefix: ".stepanel-previous-" + placement.Directory + "-"}, nil
		case !errors.Is(err, sitelayout.ErrNotPlaced):
			return siteLocation{}, fmt.Errorf("resolve site %s placement: %w", site, err)
		}
	}
	home, err := safePath(cfg.WebRoot, "sites", site)
	if err != nil {
		return siteLocation{}, err
	}
	return siteLocation{Home: home, Private: home, DocRoot: filepath.Join(home, "public"), PreviousPrefix: ".stepanel-previous-"}, nil
}

// existingManagedDocRoot returns the site's document root after checking it
// is a real directory.
func existingManagedDocRoot(cfg Config, site string) (string, error) {
	location, err := siteLocationFor(cfg, site)
	if err != nil {
		return "", err
	}
	if !location.Placed {
		return existingManagedSitePublicRoot(cfg.WebRoot, site)
	}
	return existingRealDirectory(location.DocRoot, "managed site document root")
}

// existingManagedSiteState returns the site's per-site state directory (the
// legacy site root, or a placed site's private directory) after checking it
// is a real directory.
func existingManagedSiteState(cfg Config, site string) (string, error) {
	location, err := siteLocationFor(cfg, site)
	if err != nil {
		return "", err
	}
	if !location.Placed {
		return existingManagedSiteRoot(cfg.WebRoot, site)
	}
	return existingRealDirectory(location.Private, "managed site state directory")
}

func existingRealDirectory(path, what string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", what)
	}
	return path, nil
}

// siteDocRoot is the site's document root.
func siteDocRoot(cfg Config, site string) (string, error) {
	location, err := siteLocationFor(cfg, site)
	if err != nil {
		return "", err
	}
	return location.DocRoot, nil
}

// sitePrivatePath joins name under the site's private state directory.
func sitePrivatePath(cfg Config, site, name string) (string, error) {
	location, err := siteLocationFor(cfg, site)
	if err != nil {
		return "", err
	}
	return safePath(location.Private, name)
}
