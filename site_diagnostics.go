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
	Domain     string    `json:"domain"`
	RouteState string    `json:"route_state"`
	Status     string    `json:"status"`
	NotAfter   time.Time `json:"not_after,omitempty"`
	DaysLeft   int       `json:"days_left,omitempty"`
	Detail     string    `json:"detail,omitempty"`
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

	tlsChecks := make([]siteTLSDiagnostic, 0)
	if a.Routes != nil {
		for _, route := range a.Routes.list() {
			if route.Site != site {
				continue
			}
			check := siteTLSDiagnostic{Domain: route.Domain, RouteState: route.State, Status: "unknown"}
			if a.Config.WebServer != "apache" {
				check.Detail = "Caddy manages certificates automatically; this snapshot does not perform a live TLS handshake."
			} else {
				check = a.inspectSiteCertificate(r.Context(), site, route.Domain, route.State)
			}
			tlsChecks = append(tlsChecks, check)
		}
	}
	result["tls"] = tlsChecks
	writeJSON(w, http.StatusOK, result)
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
