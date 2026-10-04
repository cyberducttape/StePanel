package stepanel

import (
	"net/http"
	"os"
	"os/exec"
)

type DatabaseAdmin struct {
	Engine         string `json:"engine"`
	Version        string `json:"version"`
	Host           string `json:"host"`
	Service        string `json:"service"`
	Status         string `json:"status"`
	Client         string `json:"client"`
	AdminProduct   string `json:"admin_product"`
	AdminURL       string `json:"admin_url"`
	AdminDetail    string `json:"admin_detail"`
	AdminInstalled bool   `json:"admin_installed"`
	AdminReady     bool   `json:"admin_ready"`
	ClientReady    bool   `json:"client_ready"`
	Local          bool   `json:"local"`
	LifecycleReady bool   `json:"lifecycle_ready"`
	LogicalBackup  bool   `json:"logical_backup"`
	PITRConfigured bool   `json:"pitr_configured"`
}

func (a *App) database(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"database": a.DatabaseAdmin()})
}

func (a *App) DatabaseAdmin() DatabaseAdmin {
	cfg := a.Config
	engine := cfg.DBEngine
	if engine == "" {
		engine = "mysql"
	}
	product, url, paths := "phpMyAdmin", "/phpmyadmin", []string{"/usr/share/phpmyadmin", "/usr/share/phpMyAdmin"}
	service, client := "mysql", "mysql"
	if engine == "postgresql" {
		product, url, paths = "phpPgAdmin", "/phppgadmin", []string{"/usr/share/phppgadmin", "/usr/share/phpPgAdmin"}
		service, client = "postgresql", "psql"
	} else if engine == "mariadb" {
		service = "mariadb"
	}
	if engine != "postgresql" {
		if _, err := exec.LookPath("mariadb"); err == nil {
			client = "mariadb"
		}
	}
	if cfg.DBAdminURL != "" {
		url = cfg.DBAdminURL
	}
	serviceStatus := ServiceStatus()
	status := serviceStatus[service]
	if status == "" && engine == "mysql" {
		status = serviceStatus["mariadb"]
	}
	if status == "" {
		status = "missing"
	}
	adminInstalled := false
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			adminInstalled = true
			break
		}
	}
	// Distribution packages provide the Apache alias/configuration. Other
	// webservers need an explicit operator-managed PHP route, so do not expose
	// a dashboard link that would silently lead to a directory or 404.
	adminReady := adminInstalled && cfg.WebServer == "apache" && apacheDBAdminConfigured()
	adminDetail := "Install with STEPANEL_INSTALL_DB_ADMIN=1."
	if adminInstalled && !adminReady {
		adminDetail = "Package detected; configure and protect its PHP route in the selected webserver."
	} else if adminReady {
		adminDetail = "Available through Apache with its own database authentication."
	}
	_, clientErr := exec.LookPath(client)
	local := cfg.DBHost == "" || cfg.DBHost == "localhost" || cfg.DBHost == "127.0.0.1"
	return DatabaseAdmin{Engine: engine, Version: cfg.DBVersion, Host: databaseHost(cfg), Service: service, Status: status, Client: client, AdminProduct: product, AdminURL: url, AdminDetail: adminDetail, AdminInstalled: adminInstalled, AdminReady: adminReady, ClientReady: clientErr == nil, Local: local, LifecycleReady: local && cfg.DBCtl != "", LogicalBackup: local && cfg.DBCtl != "", PITRConfigured: false}
}

func apacheDBAdminConfigured() bool {
	return apacheDBAdminConfig() != ""
}

func apacheDBAdminConfig() string {
	for _, config := range []string{"/etc/apache2/stepanel-panel/db-admin.conf", "/etc/httpd/stepanel-panel/db-admin.conf"} {
		if info, err := os.Stat(config); err == nil && info.Mode().IsRegular() {
			return config
		}
	}
	return ""
}

func databaseHost(cfg Config) string {
	if cfg.DBHost == "" || cfg.DBHost == "localhost" || cfg.DBHost == "127.0.0.1" {
		return "local socket"
	}
	return cfg.DBHost
}

func mysqlCompatible(cfg Config) bool {
	return cfg.DBEngine == "" || cfg.DBEngine == "mysql" || cfg.DBEngine == "mariadb"
}
