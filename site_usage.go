package stepanel

import (
	"errors"
	"net/http"
	"strings"
	"time"

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
	if !a.requireCustomerScope(w, r, "site:read") {
		return
	}
	location, err := siteLocationFor(a.Config, site)
	if err != nil {
		http.Error(w, "invalid site root", 422)
		return
	}
	// A placed site shares its account home; measure only its document root.
	root := location.Home
	if location.Placed {
		root = location.DocRoot
	}
	usage, err := usagecalc.Measure(root, a.Config.MaxEntries)
	if err != nil && !errors.Is(err, usagecalc.ErrLimit) {
		http.Error(w, "could not measure site usage", 500)
		return
	}
	response := map[string]any{
		"site":         usage.Site,
		"bytes":        usage.Bytes,
		"files":        usage.Files,
		"directories":  usage.Directories,
		"inodes":       usage.Inodes,
		"complete":     usage.Complete,
		"collected_at": time.Now().UTC(),
	}
	if a.Resources != nil {
		a.Resources.mu.RLock()
		profile, configured := a.Resources.values[site]
		a.Resources.mu.RUnlock()
		if configured {
			limitMB, limitInodes, limited := a.accountFilesystemQuota(profile.Account, profile)
			quota := map[string]any{"state": profile.FilesystemQuotaState, "enforced": profile.FilesystemQuotaState == "enforced", "measured_scope": "site_tree", "complete": usage.Complete}
			if limited && (limitMB > 0 || limitInodes > 0) {
				if limitMB > 0 {
					limitBytes := int64(limitMB) * 1024 * 1024
					quota["limit_bytes"] = limitBytes
					quota["tree_bytes"] = usage.Bytes
					quota["estimated_remaining_bytes"] = maxInt64(0, limitBytes-usage.Bytes)
				}
				quota["tree_files"] = usage.Files
				quota["tree_directories"] = usage.Directories
				quota["tree_inodes"] = usage.Inodes
				if limitInodes > 0 {
					quota["inode_limit"] = limitInodes
					quota["estimated_remaining_inodes"] = maxInt64(0, int64(limitInodes)-int64(usage.Inodes))
				}
				quota["scope_note"] = "The enforced byte and inode quotas belong to the hosting account. This site-tree measurement may omit other account content, so reported remaining values are upper bounds, not the kernel quota's exact headroom."
			} else {
				quota["scope_note"] = "No disk-space quota is configured; site-tree size is measured but quota headroom is unknown."
			}
			response["quota"] = quota
		}
	}
	writeJSON(w, 200, response)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
