package stepanel

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cyberducttape/StePanel/internal/domainname"
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
				check = inspectSiteCertificate(route.Domain, route.State)
			}
			tlsChecks = append(tlsChecks, check)
		}
	}
	result["tls"] = tlsChecks
	writeJSON(w, http.StatusOK, result)
}

func inspectSiteCertificate(domain, routeState string) siteTLSDiagnostic {
	check := siteTLSDiagnostic{Domain: domain, RouteState: routeState, Status: "unknown"}
	if !domainname.Valid(domain) {
		check.Detail = "The stored route domain is invalid; certificate inspection was skipped."
		return check
	}
	root, err := os.OpenRoot("/etc/letsencrypt")
	if err != nil {
		check.Detail = "The local certificate store is unavailable."
		return check
	}
	defer root.Close()
	file, err := root.Open(filepath.Join("live", domain, "fullchain.pem"))
	if err != nil {
		if os.IsNotExist(err) {
			check.Status = "missing"
			check.Detail = "No local Certbot certificate was found for this route."
		} else {
			check.Detail = "The local certificate could not be read safely."
		}
		return check
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		check.Status = "failed"
		check.Detail = "The certificate file is not a bounded regular file."
		return check
	}
	data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		check.Status = "failed"
		check.Detail = "The certificate file could not be read within its size limit."
		return check
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		check.Status = "failed"
		check.Detail = "The certificate file does not contain a parseable PEM certificate."
		return check
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		check.Status = "failed"
		check.Detail = "The certificate is malformed."
		return check
	}
	check.NotAfter = cert.NotAfter.UTC()
	check.DaysLeft = int(time.Until(cert.NotAfter).Hours() / 24)
	if err := cert.VerifyHostname(domain); err != nil {
		check.Status = "hostname_mismatch"
		check.Detail = "The local certificate does not cover this route's hostname."
	} else if time.Now().After(cert.NotAfter) {
		check.Status = "expired"
		check.Detail = "The local certificate has expired."
	} else if check.DaysLeft <= 14 {
		check.Status = "expiring"
		check.Detail = "The local certificate expires within 14 days."
	} else {
		check.Status = "valid"
		check.Detail = "The local certificate covers this hostname and is not near expiry; the live listener was not probed."
	}
	return check
}
