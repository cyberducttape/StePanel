package stepanel

import (
	"context"
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
	RenewalAt       time.Time `json:"renewal_at,omitempty"`
	RenewalEvidence string    `json:"renewal_evidence,omitempty"`
	NotAfter        time.Time `json:"not_after,omitempty"`
	DaysLeft        int       `json:"days_left,omitempty"`
	Detail          string    `json:"detail,omitempty"`
}

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
		if a.Jobs != nil && len(domains) > 0 {
			if jobs, err := a.Jobs.ListForOwnersSummaries(10, domains); err == nil {
				latest := make(map[string]Job, len(domains))
				for _, job := range jobs {
					if job.Kind != "certificate.issue" {
						continue
					}
					if prior, ok := latest[job.User]; !ok || job.StartedAt.After(prior.StartedAt) {
						latest[job.User] = job
					}
				}
				for i := range tlsChecks {
					job, ok := latest[tlsChecks[i].Domain]
					if ok {
						tlsChecks[i].Renewal, tlsChecks[i].RenewalAt = job.State, job.StartedAt
						if job.State == "failed" || job.State == "dead-letter" {
							tlsChecks[i].RenewalEvidence = "The latest durable certificate issuance job for this domain ended " + job.State + ". Job error details are available in authorized job history."
						}
					}
				}
			}
		}
	}
	result["tls"] = tlsChecks
	writeJSON(w, http.StatusOK, result)
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
