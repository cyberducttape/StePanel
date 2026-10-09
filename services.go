package stepanel

import (
	"bytes"
	"context"
	"fmt"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ServiceSummary struct {
	Name   string `json:"name"`
	Region string `json:"region"`
	Status string `json:"status"`
	Load   string `json:"load"`
	Uptime string `json:"uptime"`
}

var serviceStatusCache struct {
	sync.RWMutex
	at    time.Time
	items map[string]string
	// refresh lets one caller rebuild an expired cache while concurrent
	// callers wait for its result, instead of every request spawning its
	// own set of systemctl and apachectl processes.
	refresh sync.Mutex
}

func cachedServiceStatus() (map[string]string, bool) {
	serviceStatusCache.RLock()
	defer serviceStatusCache.RUnlock()
	if serviceStatusCache.items != nil && time.Since(serviceStatusCache.at) < 5*time.Second {
		return cloneServiceStatus(serviceStatusCache.items), true
	}
	return nil, false
}

func ServiceStatus() map[string]string {
	if result, ok := cachedServiceStatus(); ok {
		return result
	}
	serviceStatusCache.refresh.Lock()
	defer serviceStatusCache.refresh.Unlock()
	if result, ok := cachedServiceStatus(); ok {
		return result
	}
	result := map[string]string{}
	for _, service := range []string{"apache2", "httpd", "lsws", "caddy", "mysql", "mariadb", "redis-server", "valkey-server", "fail2ban", "fpm-lens", "exim4", "exim", "dovecot", "spamassassin", "spamd", "vsftpd"} {
		if _, err := exec.LookPath(service); err == nil {
			result[service] = serviceUnitState(service)
		}
	}
	if state := systemdPatternState("postgresql*.service"); state != "" {
		result["postgresql"] = state
	} else if _, err := exec.LookPath("postgres"); err == nil {
		result["postgresql"] = serviceUnitState("postgresql")
	}
	if state := systemdPatternState("php*-fpm.service"); state != "" {
		result["php-fpm"] = state
	} else if _, err := exec.LookPath("php-fpm"); err == nil {
		result["php-fpm"] = serviceUnitState("php-fpm")
	}
	for _, apache := range []string{"apachectl", "httpd"} {
		if _, err := exec.LookPath(apache); err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		output, err := runBoundedCommand(ctx, exec.CommandContext(ctx, apache, "-M"))
		cancel()
		if err == nil && (bytes.Contains(output, []byte("security2_module")) || bytes.Contains(output, []byte("security3_module"))) {
			result["modsecurity"] = "enabled"
			break
		}
	}
	serviceStatusCache.Lock()
	serviceStatusCache.items = cloneServiceStatus(result)
	serviceStatusCache.at = time.Now()
	serviceStatusCache.Unlock()
	return result
}

func systemdPatternState(pattern string) string {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := runBoundedCommand(ctx, exec.CommandContext(ctx, "systemctl", "list-unit-files", pattern, "--no-legend", "--plain"))
	if err != nil || strings.TrimSpace(string(output)) == "" {
		return ""
	}
	state := "installed"
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		unitState := serviceUnitState(strings.TrimSuffix(fields[0], ".service"))
		if unitState == "active" {
			return "active"
		}
		if unitState == "failed" {
			state = "failed"
		}
	}
	return state
}

func cloneServiceStatus(source map[string]string) map[string]string {
	copy := make(map[string]string, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

func serviceUnitState(service string) string {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "installed"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := runBoundedCommand(ctx, exec.CommandContext(ctx, "systemctl", "is-active", service))
	state := strings.TrimSpace(string(output))
	if err == nil && state == "active" {
		return "active"
	}
	if state == "failed" {
		return "failed"
	}
	switch state {
	case "inactive", "activating", "deactivating", "reloading":
		return state
	}
	return "unknown"
}

func ServiceSummaries(cfg Config) []ServiceSummary {
	status := ServiceStatus()
	load := "n/a"
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			load = fields[0]
		}
	}
	uptime := "n/a"
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			if seconds, err := strconv.ParseFloat(fields[0], 64); err == nil {
				uptime = formatUptime(seconds)
			}
		}
	}
	services := configuredServiceNames(cfg, status)
	result := make([]ServiceSummary, 0, len(services))
	for _, name := range services {
		state := resolvedServiceStatus(name, status)
		if state == "" {
			state = "missing"
		}
		result = append(result, ServiceSummary{Name: name, Region: "this server", Status: state, Load: load, Uptime: uptime})
	}
	return result
}

func configuredServiceNames(cfg Config, status map[string]string) []string {
	web := map[string]string{"apache": "apache2", "openlitespeed": "openlitespeed", "caddy": "caddy"}[cfg.WebServer]
	if web == "" {
		web = "apache2"
	}
	database := cfg.DBEngine
	if database == "" {
		database = "mysql"
	}
	services := []string{web, database, "php-fpm"}
	for _, optional := range []string{"fail2ban", "fpm-lens", "modsecurity", "exim", "dovecot", "spamassassin", "vsftpd"} {
		if resolvedServiceStatus(optional, status) != "" {
			services = append(services, optional)
		}
	}
	return services
}

func resolvedServiceStatus(name string, status map[string]string) string {
	state := status[name]
	switch name {
	case "apache2":
		if state == "" {
			state = status["httpd"]
		}
	case "openlitespeed":
		if state == "" {
			state = status["lsws"]
		}
	case "mysql":
		if state == "" {
			state = status["mariadb"]
		}
	case "exim":
		if state == "" {
			state = status["exim4"]
		}
	case "spamassassin":
		if state == "" {
			state = status["spamd"]
		}
	}
	return state
}

func (a *App) ftpStatus(w http.ResponseWriter, r *http.Request) {
	status := ServiceStatus()["vsftpd"]
	if status == "" {
		status = "missing"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service":           "vsftpd",
		"status":            status,
		"anonymous":         false,
		"local_user_chroot": true,
		"passive_ports":     []int{a.Config.FTPPassiveMin, a.Config.FTPPassiveMax},
		"ftps_required":     true,
		"setup_warning":     "Keep vsftpd disabled until FTPS certificates, firewall rules, and per-site users are configured.",
	})
}

func (a *App) ftpEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		a.ftpStatus(w, r)
		return
	}
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid FTPS mutation request", http.StatusForbidden)
		return
	}
	var input struct {
		Site     string `json:"site"`
		Enabled  *bool  `json:"enabled"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Site = safeUser(strings.TrimSpace(input.Site))
	if input.Site == "" || input.Enabled == nil {
		http.Error(w, "site and enabled are required", http.StatusUnprocessableEntity)
		return
	}
	if !*input.Enabled {
		input.Password = ""
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), input.Site)
	if lockErr != nil {
		http.Error(w, "FTPS mutation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	// Enabling FTPS installs a password credential, so it is intent-first.
	// Disabling is a pure revocation and is recorded after the change.
	var intent *SecurityAudit
	if *input.Enabled {
		var err error
		intent, err = BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "site.ftps.enabled", input.Site, "ftps=true")
		if err != nil {
			refuseWithoutSecurityAudit(w)
			return
		}
	}
	ctx, cancel := context.WithTimeout(operationCtx, helperConfigMutationTimeout)
	defer cancel()
	request := rootbroker.SiteRequest{Action: "ftp", Site: input.Site, FTPEnabled: input.Enabled, FTPPassword: input.Password}
	var err error
	if a.Config.Production || labDirectRootBrokerEnabled() {
		err = runTypedSiteMutation(ctx, a.Config, request)
	} else {
		_, err, _ = runAllowlistedHelperOutput(ctx, a.Config, []byte(input.Password), a.Config.SiteCtl, "ftp", input.Site, boolString(*input.Enabled))
	}
	if err != nil {
		if intent != nil {
			intent.Failed(err.Error())
		}
		http.Error(w, "FTPS mutation failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if intent != nil {
		intent.Completed("ftps=true")
	} else {
		RevocationAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "site.ftps.disabled", input.Site, "ftps=false")
	}
	writeJSON(w, http.StatusOK, map[string]any{"site": input.Site, "enabled": *input.Enabled})
}

func formatUptime(seconds float64) string {
	days := int(seconds) / (24 * 60 * 60)
	hours := int(seconds) / (60 * 60) % 24
	minutes := int(seconds) / 60 % 60
	if days > 0 {
		return fmt.Sprintf("%dd %02dh", days, hours)
	}
	return fmt.Sprintf("%02dh %02dm", hours, minutes)
}
