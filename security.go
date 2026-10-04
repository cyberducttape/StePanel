package stepanel

import (
	"fmt"
	"os"
	"strings"
)

type SecurityCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
}

type WAFCapability struct {
	WebServer string `json:"webserver"`
	Provider  string `json:"provider"`
	Status    string `json:"status"`
	Detail    string `json:"detail"`
}

func (a *App) WAFCapability() WAFCapability {
	webserver := a.Config.WebServer
	if webserver == "" {
		webserver = "caddy"
	}
	return wafCapability(webserver, ServiceStatus())
}

func wafCapability(webserver string, services map[string]string) WAFCapability {
	switch webserver {
	case "apache":
		if services["modsecurity"] == "enabled" {
			return WAFCapability{WebServer: webserver, Provider: "ModSecurity + OWASP CRS", Status: "available", Detail: "Apache reports the ModSecurity module as enabled; review DetectionOnly audit events before blocking."}
		}
		return WAFCapability{WebServer: webserver, Provider: "ModSecurity + OWASP CRS", Status: "available-not-enabled", Detail: "The Apache integration is supported but ModSecurity is not currently detected as enabled."}
	case "openlitespeed":
		return WAFCapability{WebServer: webserver, Provider: "external", Status: "unavailable", Detail: "Native StePanel WAF integration is not available for OpenLiteSpeed; place a supported WAF/security proxy in front."}
	default:
		return WAFCapability{WebServer: "caddy", Provider: "external", Status: "unavailable", Detail: "Native StePanel ModSecurity integration is Apache-only; use a vetted Caddy WAF plugin or external security proxy."}
	}
}

func (a *App) SecurityChecks() []SecurityCheck {
	checks := make([]SecurityCheck, 0, 6)
	if a.Auth.Enabled {
		checks = append(checks, SecurityCheck{Name: "Administrator authentication", Status: "pass", Severity: "low", Detail: "Password authentication and signed sessions are enabled."})
	} else {
		severity := "warning"
		status := "warning"
		detail := "Authentication is disabled for local development."
		if a.Config.Production {
			severity, status = "high", "fail"
			detail = "Production mode requires administrator authentication."
		}
		checks = append(checks, SecurityCheck{Name: "Administrator authentication", Status: status, Severity: severity, Detail: detail})
	}
	if a.Auth.TOTPEnabled {
		checks = append(checks, SecurityCheck{Name: "Administrator MFA", Status: "pass", Severity: "low", Detail: "TOTP is required for administrator login."})
	} else {
		checks = append(checks, SecurityCheck{Name: "Administrator MFA", Status: "warning", Severity: "medium", Detail: "Configure STEPANEL_ADMIN_TOTP_SECRET to require an authenticator code."})
	}
	checks = append(checks, privateDirectoryCheck("Import staging permissions", a.Config.ImportRoot))
	checks = append(checks, directoryCheck("Web root availability", a.Config.WebRoot))

	services := ServiceStatus()
	waf := a.WAFCapability()
	if waf.Status == "available" {
		checks = append(checks, SecurityCheck{Name: "Web Application Firewall", Status: "pass", Severity: "low", Detail: waf.Detail})
	} else if waf.Status == "available-not-enabled" {
		checks = append(checks, SecurityCheck{Name: "Web Application Firewall", Status: "warning", Severity: "medium", Detail: waf.Detail})
	} else {
		checks = append(checks, SecurityCheck{Name: "Web Application Firewall", Status: "warning", Severity: "medium", Detail: waf.Detail})
	}
	if services["fail2ban"] == "installed" || services["fail2ban"] == "active" {
		checks = append(checks, SecurityCheck{Name: "Fail2Ban", Status: "pass", Severity: "low", Detail: "The Fail2Ban executable is available."})
	} else {
		checks = append(checks, SecurityCheck{Name: "Fail2Ban", Status: "warning", Severity: "medium", Detail: "Fail2Ban is not available on PATH."})
	}
	if database := a.DatabaseAdmin(); database.AdminInstalled {
		checks = append(checks, databaseAdminSecurityCheck(database))
	}
	return checks
}

func databaseAdminSecurityCheck(database DatabaseAdmin) SecurityCheck {
	name := database.AdminProduct + " access policy"
	config := apacheDBAdminConfig()
	if !database.AdminReady || config == "" {
		return SecurityCheck{Name: name, Status: "warning", Severity: "high", Detail: "The database administrator is installed without a verified StePanel Apache IP policy."}
	}
	data, err := os.ReadFile(config)
	if err != nil {
		return SecurityCheck{Name: name, Status: "fail", Severity: "high", Detail: "The database administrator policy cannot be read."}
	}
	hasIPAllowlist, openToAll := apacheAccessPolicy(string(data))
	if openToAll {
		return SecurityCheck{Name: name, Status: "fail", Severity: "critical", Detail: "The database administrator is open to all clients; replace Require all granted with an IP allowlist."}
	}
	if !hasIPAllowlist {
		return SecurityCheck{Name: name, Status: "fail", Severity: "high", Detail: "The database administrator policy has no explicit Require ip allowlist."}
	}
	return SecurityCheck{Name: name, Status: "pass", Severity: "low", Detail: "The database administrator has an explicit Apache IP allowlist."}
}

func apacheAccessPolicy(content string) (hasIPAllowlist, openToAll bool) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.ToLower(strings.TrimSpace(strings.SplitN(line, "#", 2)[0]))
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "require" && fields[1] == "all" && fields[2] == "granted" {
			openToAll = true
		}
		if len(fields) >= 3 && fields[0] == "require" && fields[1] == "ip" {
			hasIPAllowlist = true
		}
	}
	return hasIPAllowlist, openToAll
}

func privateDirectoryCheck(name, path string) SecurityCheck {
	info, err := os.Stat(path)
	if err != nil {
		return SecurityCheck{Name: name, Status: "warning", Severity: "medium", Detail: fmt.Sprintf("%s is unavailable: %v", path, err)}
	}
	if !info.IsDir() {
		return SecurityCheck{Name: name, Status: "fail", Severity: "high", Detail: path + " is not a directory."}
	}
	if info.Mode().Perm()&0077 != 0 {
		return SecurityCheck{Name: name, Status: "warning", Severity: "medium", Detail: path + " should be readable only by its owner."}
	}
	return SecurityCheck{Name: name, Status: "pass", Severity: "low", Detail: path + " is owner-only."}
}

func directoryCheck(name, path string) SecurityCheck {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return SecurityCheck{Name: name, Status: "warning", Severity: "medium", Detail: "The configured web root is not currently available."}
	}
	return SecurityCheck{Name: name, Status: "pass", Severity: "low", Detail: path + " is available."}
}
