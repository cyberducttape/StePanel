package main

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	usagecalc "github.com/cyberducttape/StePanel/internal/usage"
)

type SiteUsage = usagecalc.SiteUsage

// siteUsage is deliberately bounded. It reports actual regular-file usage but
// never follows symlinks and refuses to turn a dashboard request into an
// unbounded recursive walk of a tenant-owned tree.
func (a *App) siteUsage(w http.ResponseWriter, r *http.Request) {
	site := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sites/usage/"), "/")
	if safeUser(site) == "" || strings.Contains(site, "/") {
		http.Error(w, "invalid or inaccessible site", 403)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, site, "invalid or inaccessible site", 403); !ok {
		return
	}
	root := filepath.Join(a.Config.WebRoot, "sites", site)
	if err := ensureInside(a.Config.WebRoot, root); err != nil {
		http.Error(w, "invalid site root", 422)
		return
	}
	usage, err := usagecalc.Measure(root, a.Config.MaxEntries)
	if err != nil && !errors.Is(err, usagecalc.ErrLimit) {
		http.Error(w, "could not measure site usage", 500)
		return
	}
	writeJSON(w, 200, usage)
}
