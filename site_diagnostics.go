package stepanel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

type siteTLSDiagnostic struct {
	Domain          string    `json:"domain"`
	RouteState      string    `json:"route_state"`
	Status          string    `json:"status"`
	Renewal         string    `json:"renewal,omitempty"`
	RenewalJobID    string    `json:"renewal_job_id,omitempty"`
	RenewalAt       time.Time `json:"renewal_at,omitempty"`
	RenewalEvidence string    `json:"renewal_evidence,omitempty"`
	NotAfter        time.Time `json:"not_after,omitempty"`
	DaysLeft        int       `json:"days_left,omitempty"`
	Detail          string    `json:"detail,omitempty"`
}

type siteWordPressDBProbe struct {
	Status      string    `json:"status"`
	CollectedAt time.Time `json:"collected_at"`
	Detail      string    `json:"detail"`
	expiresAt   time.Time
}

const wordpressDBProbeTimeout = 10 * time.Second
const wordpressDBProbeCacheTTL = 20 * time.Second

// siteDiagnostics correlates host facts scoped to one authorized site. It is
// read-only and deliberately reports unknown where the host cannot establish
// an application's effective permissions or a live TLS handshake.
func (a *App) siteDiagnostics(w http.ResponseWriter, r *http.Request) {
	site := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sites/diagnostics/"), "/")
	if safeUser(site) == "" || strings.Contains(site, "/") {
		http.Error(w, "invalid or inaccessible site", http.StatusForbidden)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, site, "invalid or inaccessible site", http.StatusForbidden); !ok {
		return
	}
	if !a.requireCustomerScope(w, r, "site:read") {
		return
	}

	result := map[string]any{"site": site, "collected_at": time.Now().UTC()}
	if location, err := siteLocationFor(a.Config, site); err == nil {
		storage := []map[string]any{inspectFilesystemCapacity("site files", location.Home)}
		if a.Config.BackupRoot != "" {
			storage = append(storage, inspectFilesystemCapacity("backup storage", a.Config.BackupRoot))
		}
		if a.Config.DBDataDir != "" {
			storage = append(storage, inspectFilesystemCapacity("database storage", a.Config.DBDataDir))
		}
		result["host_storage"] = storage
	} else {
		result["host_storage"] = []map[string]any{{"label": "site files", "status": "unknown", "detail": "The managed site filesystem could not be resolved safely."}}
	}
	location, err := siteLocationFor(a.Config, site)
	if err != nil {
		result["filesystem"] = map[string]any{"status": "unknown", "detail": "Managed site path could not be resolved safely."}
	} else if info, statErr := os.Lstat(location.DocRoot); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		result["filesystem"] = map[string]any{"status": "failed", "detail": "The configured document root is missing or is not a real directory."}
	} else {
		filesystem := map[string]any{"status": "observed", "mode": fmt.Sprintf("%04o", info.Mode().Perm()), "detail": "Path mode and ownership are shown as evidence; this does not emulate the web-server service account's effective access."}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			filesystem["owner_uid"] = stat.Uid
			filesystem["owner_gid"] = stat.Gid
		}
		result["filesystem"] = filesystem
	}

	diagnostics := map[string]any{"available": false, "engine": a.Config.DBEngine, "databases": []string{}}
	if a.Config.DBEngine == "" {
		diagnostics["engine"] = "mysql"
	}
	if !a.Auth.HasRequiredCustomerScope(r, "database:read") {
		diagnostics["detail"] = "This identity does not have permission to inspect assigned database names."
	} else if items, inventoryErr := a.cachedManagedDatabaseInventory(); inventoryErr == nil {
		names := make([]string, 0, len(items))
		for _, item := range items {
			if item.Site == site {
				names = append(names, item.Name)
			}
		}
		diagnostics["available"] = true
		diagnostics["databases"] = names
		diagnostics["detail"] = "The database helper completed an inventory query; this confirms engine and metadata access, not an application login using the site's credentials."
	} else {
		diagnostics["detail"] = "The managed database inventory helper could not reach or inspect the configured database engine."
	}
	result["database"] = diagnostics
	engineProbe := a.cachedDatabaseDiagnostics(15 * time.Second)
	connection := map[string]any{"status": "unavailable", "engine": diagnostics["engine"], "collected_at": engineProbe.Collected, "detail": engineProbe.Detail}
	if engineProbe.Available {
		connection["status"] = "connected"
		connection["detail"] = "The restricted host diagnostic query completed successfully. This verifies host-to-engine connectivity, not the site's application credentials or query behavior."
	}
	result["database_connectivity"] = connection
	result["application_database_connectivity"] = a.siteWordPressDatabaseCheck(r.Context(), site)

	if a.Schedules != nil {
		a.Schedules.mu.Lock()
		schedule, ok := a.Schedules.items[site]
		a.Schedules.mu.Unlock()
		if ok && schedule.Enabled {
			result["backup_schedule"] = map[string]any{"enabled": true, "interval_minutes": schedule.IntervalMinutes, "last_run": schedule.LastRun, "last_success": schedule.LastSuccess, "has_error_detail": schedule.LastError != "", "consecutive_failures": schedule.ConsecutiveFails}
		} else {
			result["backup_schedule"] = map[string]any{"enabled": false}
		}
	}

	tlsChecks := make([]siteTLSDiagnostic, 0)
	if a.Routes != nil {
		domains := []string{}
		for _, route := range a.Routes.list() {
			if route.Site != site {
				continue
			}
			domains = append(domains, route.Domain)
			check := siteTLSDiagnostic{Domain: route.Domain, RouteState: route.State, Status: "unknown"}
			if a.Config.WebServer != "apache" {
				check.Detail = "Caddy manages certificates automatically; this snapshot does not perform a live TLS handshake."
			} else {
				check = a.inspectSiteCertificate(r.Context(), site, route.Domain, route.State)
			}
			tlsChecks = append(tlsChecks, check)
		}
		if len(domains) > 0 {
			if a.Jobs == nil {
				for i := range tlsChecks {
					tlsChecks[i].Renewal = "unknown"
					tlsChecks[i].RenewalEvidence = "Certificate issuance history is unavailable because the durable job store is not configured."
				}
			} else if jobs, err := a.Jobs.LatestForOwnersSummaries("certificate.issue", domains); err == nil {
				latest := make(map[string]Job, len(domains))
				for _, job := range jobs {
					latest[job.User] = job
				}
				for i := range tlsChecks {
					job, ok := latest[tlsChecks[i].Domain]
					if ok {
						tlsChecks[i].Renewal, tlsChecks[i].RenewalAt, tlsChecks[i].RenewalJobID = job.State, job.StartedAt, job.ID
						if job.State == "failed" || job.State == "dead-letter" {
							tlsChecks[i].RenewalEvidence = fmt.Sprintf("Latest certificate issuance job %s for this domain is in %s state; it was created at %s. Its failure output is available in authorized job history.", job.ID, job.State, job.StartedAt.UTC().Format(time.RFC3339))
						}
					}
				}
			} else {
				for i := range tlsChecks {
					tlsChecks[i].Renewal = "unknown"
					tlsChecks[i].RenewalEvidence = "Certificate issuance history could not be read from the durable job store."
				}
			}
		}
	}
	result["tls"] = tlsChecks
	writeJSON(w, http.StatusOK, result)
}

// siteWordPressDatabaseCheck verifies the configured application's database
// connection with the existing restricted WP-CLI action. It runs as the site
// identity through the root broker and is cached briefly to keep dashboard
// refreshes from repeatedly bootstrapping WordPress.
func (a *App) siteWordPressDatabaseCheck(parent context.Context, site string) siteWordPressDBProbe {
	now := time.Now().UTC()
	a.wordpressDBProbeMu.Lock()
	if cached, ok := a.wordpressDBProbeCache[site]; ok && now.Before(cached.expiresAt) {
		a.wordpressDBProbeMu.Unlock()
		return cached
	}
	a.wordpressDBProbeMu.Unlock()

	probe := siteWordPressDBProbe{Status: "unknown", CollectedAt: now, Detail: "The application's database connection could not be verified."}
	root, err := existingManagedDocRoot(a.Config, site)
	if err != nil {
		probe.Detail = "WordPress configuration could not be safely inspected; no application database claim is made."
	} else if _, err := existingRegularEntry(root, "wp-config.php"); errors.Is(err, os.ErrNotExist) {
		probe.Status = "not_applicable"
		probe.Detail = "No regular wp-config.php was found; the application-specific database check does not apply."
	} else if err != nil {
		probe.Detail = "wp-config.php could not be verified as a regular file; no application database probe was run."
	} else if a.Config.WPCLI == "" {
		probe.Detail = "WordPress is present, but WP-CLI is not configured for a credential-safe database check."
	} else if !commandAvailable(a.Config.WPCLI) {
		probe.Detail = "WordPress is present, but the configured WP-CLI executable is unavailable."
	} else {
		ctx, cancel := context.WithTimeout(parent, wordpressDBProbeTimeout)
		_, err := runWordPress(ctx, a.Config, rootbroker.WordPressRequest{Action: "db-check", Site: site})
		cancel()
		if err == nil {
			probe.Status = "connected"
			probe.Detail = "WP-CLI db check succeeded using this site's configured WordPress database credentials; this proves connectivity and a successful table check at the sample time."
		} else if parent.Err() == nil {
			probe.Status = "failed"
			probe.Detail = classifyWordPressDBCheckFailure(err)
		}
	}
	if parent.Err() != nil {
		probe.Detail = "The application database probe was cancelled with the diagnostics request."
		return probe
	}
	probe.expiresAt = now.Add(wordpressDBProbeCacheTTL)
	a.wordpressDBProbeMu.Lock()
	if a.wordpressDBProbeCache == nil {
		a.wordpressDBProbeCache = make(map[string]siteWordPressDBProbe)
	}
	for cachedSite, cached := range a.wordpressDBProbeCache {
		if !now.Before(cached.expiresAt) {
			delete(a.wordpressDBProbeCache, cachedSite)
		}
	}
	if len(a.wordpressDBProbeCache) >= 256 {
		oldestSite := ""
		var oldest time.Time
		for cachedSite, cached := range a.wordpressDBProbeCache {
			if oldestSite == "" || cached.CollectedAt.Before(oldest) {
				oldestSite, oldest = cachedSite, cached.CollectedAt
			}
		}
		delete(a.wordpressDBProbeCache, oldestSite)
	}
	a.wordpressDBProbeCache[site] = probe
	a.wordpressDBProbeMu.Unlock()
	return probe
}

func classifyWordPressDBCheckFailure(err error) string {
	if err == nil {
		return "WP-CLI db check failed; raw helper output is withheld from this diagnostic."
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "access denied"), strings.Contains(message, "authentication failed"), strings.Contains(message, "password authentication failed"):
		return "WP-CLI db check failed with a database authentication error. Verify the site's managed database username and password through the credential rotation workflow; raw output is withheld."
	case strings.Contains(message, "connection refused"), strings.Contains(message, "could not connect"), strings.Contains(message, "can't connect"), strings.Contains(message, "connection timed out"):
		return "WP-CLI db check could not connect to the configured database host. Verify database service availability and the host/socket setting; raw output is withheld."
	case strings.Contains(message, "unknown database"), strings.Contains(message, "does not exist"):
		return "WP-CLI db check reports that the configured database is missing. Verify database ownership and restore state; raw output is withheld."
	default:
		return "WP-CLI db check returned a failure while using this site's configured credentials. Review the authorized job and site logs for details; raw helper output is withheld here."
	}
}

type filesystemCapacity struct {
	Label           string    `json:"label"`
	Status          string    `json:"status"`
	AvailableBytes  uint64    `json:"available_bytes,omitempty"`
	TotalBytes      uint64    `json:"total_bytes,omitempty"`
	AvailableInodes uint64    `json:"available_inodes,omitempty"`
	TotalInodes     uint64    `json:"total_inodes,omitempty"`
	CollectedAt     time.Time `json:"collected_at"`
	Detail          string    `json:"detail"`
}

func inspectFilesystemCapacity(label, path string) map[string]any {
	check := filesystemCapacity{Label: label, Status: "unknown", CollectedAt: time.Now().UTC(), Detail: "Filesystem capacity could not be measured."}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return map[string]any{"label": check.Label, "status": check.Status, "collected_at": check.CollectedAt, "detail": check.Detail}
	}
	check.AvailableBytes = stat.Bavail * uint64(stat.Bsize)
	check.TotalBytes = stat.Blocks * uint64(stat.Bsize)
	check.AvailableInodes, check.TotalInodes = stat.Ffree, stat.Files
	check.Status = "healthy"
	byteLow := check.TotalBytes > 0 && check.AvailableBytes*100/check.TotalBytes < 10
	inodeLow := check.TotalInodes > 0 && check.AvailableInodes*100/check.TotalInodes < 10
	if byteLow || inodeLow {
		check.Status = "pressure"
		check.Detail = "Filesystem headroom is below 10% for bytes or inodes."
	} else {
		check.Detail = "Filesystem available bytes and inodes were measured from the host."
	}
	return map[string]any{"label": check.Label, "status": check.Status, "available_bytes": check.AvailableBytes, "total_bytes": check.TotalBytes, "available_inodes": check.AvailableInodes, "total_inodes": check.TotalInodes, "collected_at": check.CollectedAt, "detail": check.Detail}
}

// inspectSiteCertificate reads route certificate metadata. The Certbot store
// is root-only, so production hosts ask the broker; the direct read serves
// development hosts. Broker errors name host paths and go to the log only.
func (a *App) inspectSiteCertificate(ctx context.Context, site, domain, routeState string) siteTLSDiagnostic {
	var status rootbroker.CertificateStatus
	if a.Config.Production || labDirectRootBrokerEnabled() {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", a.Config.WebRoot)
		if err == nil {
			status, err = client.SiteCertificateStatus(ctx, site, domain)
		}
		if err != nil {
			log.Printf("certificate status for site %s route %s: %v", site, domain, err)
			return siteTLSDiagnostic{Domain: domain, RouteState: routeState, Status: "unknown", Detail: "The local certificate could not be inspected; an administrator can find the cause in the panel log."}
		}
	} else {
		status = rootbroker.InspectCertificate(rootbroker.CertbotStoreRoot, domain, time.Now())
	}
	return siteTLSDiagnostic{Domain: domain, RouteState: routeState, Status: status.Status, NotAfter: status.NotAfter, DaysLeft: status.DaysLeft, Detail: status.Detail}
}
