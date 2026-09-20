package doctor

import "time"

// ServerInventory describes the software and hardware configuration of a remote server
type ServerInventory struct {
	Hostname         string            `json:"hostname"`
	ScanTime         time.Time         `json:"scan_time"`
	OS               OperatingSystem   `json:"os"`
	PHP              PHPRuntime        `json:"php"`
	Database         DatabaseSystem    `json:"database"`
	WebServer        WebServerInfo     `json:"web_server"`
	SystemResources  SystemResources   `json:"system_resources"`
	Applications     []Application     `json:"applications"`
	Sites            []SiteInfo        `json:"sites"`
	ExternalServices []ExternalService `json:"external_services"`
	CronJobs         int               `json:"cron_jobs"`
	MailAccounts     int               `json:"mail_accounts"`
	ScanErrors       []string          `json:"scan_errors,omitempty"`
}

// OperatingSystem describes OS details
type OperatingSystem struct {
	Name          string `json:"name"`           // "CentOS", "Ubuntu", "AlmaLinux"
	Version       string `json:"version"`        // "7.9", "20.04", "9.0"
	KernelVersion string `json:"kernel_version"` // "3.10.0-1160", "5.15.0-48"
	DistributorID string `json:"distributor_id"` // "centos", "ubuntu", "almalinux"
	Architecture  string `json:"architecture"`   // "x86_64", "aarch64"
}

// PHPRuntime describes PHP configuration
type PHPRuntime struct {
	Version      string            `json:"version"`     // "7.4", "8.1", "8.2"
	Extensions   []string          `json:"extensions"`  // "mysqli", "curl", "redis", "igbinary"
	FPMVersion   string            `json:"fpm_version"` // "7.4.x", "8.1.x"
	MaxUploadMB  int               `json:"max_upload_mb"`
	MemoryLimit  int               `json:"memory_limit_mb"`
	MaxExecution int               `json:"max_execution_seconds"`
	TimeZone     string            `json:"timezone"`
	OpCacheInfo  map[string]string `json:"opcache_info,omitempty"`
}

// DatabaseSystem describes database configuration
type DatabaseSystem struct {
	Type      string `json:"type"`     // "MySQL", "MariaDB", "PostgreSQL"
	Version   string `json:"version"`  // "5.7.x", "8.0.x", "10.4.x"
	Encoding  string `json:"encoding"` // "utf8mb4", "utf8"
	SQLMode   string `json:"sql_mode"` // MySQL SQL modes
	Databases int    `json:"databases"`
	Reachable bool   `json:"reachable"`
}

// WebServerInfo describes web server configuration
type WebServerInfo struct {
	Type           string   `json:"type"`              // "Apache", "Nginx", "OpenLiteSpeed"
	Version        string   `json:"version"`           // "2.4.x"
	Modules        []string `json:"modules,omitempty"` // "mod_rewrite", "mod_ssl"
	MaxConnections int      `json:"max_connections"`
	DocumentRoot   string   `json:"document_root"`
	VirtualHosts   int      `json:"virtual_hosts"`
}

// SystemResources describes hardware and system capacity
type SystemResources struct {
	TotalDiskGB     int64 `json:"total_disk_gb"`
	AvailableDiskGB int64 `json:"available_disk_gb"`
	TotalMemoryGB   int   `json:"total_memory_gb"`
	CPUCores        int   `json:"cpu_cores"`
	Swap            int   `json:"swap_mb"`
}

// Application describes detected application on a site
type Application struct {
	Name     string `json:"name"`     // "WordPress", "Drupal", "Laravel"
	Version  string `json:"version"`  // "5.8.1", "9.0.0"
	Location string `json:"location"` // "public_html"
}

// SiteInfo describes a website on the source server
type SiteInfo struct {
	Domain             string   `json:"domain"`
	DocumentRoot       string   `json:"document_root"`
	DiskUsageMB        int64    `json:"disk_usage_mb"`
	FileCount          int      `json:"file_count"`
	DatabaseNames      []string `json:"databases"`
	HasHTAccess        bool     `json:"has_htaccess"`
	HasSSL             bool     `json:"has_ssl"`
	Application        string   `json:"application,omitempty"`
	ApplicationVersion string   `json:"application_version,omitempty"`
}

// ExternalService describes external dependencies
type ExternalService struct {
	Type        string `json:"type"`        // "Redis", "AWS", "SMTP", "API"
	Description string `json:"description"` // "AWS RDS database", "SendGrid SMTP"
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	Reachable   bool   `json:"reachable"`
	Error       string `json:"error,omitempty"`
}

// MigrationAnalysis compares two inventories and identifies issues
type MigrationAnalysis struct {
	SourceInventory      ServerInventory `json:"source_inventory"`
	DestinationInventory ServerInventory `json:"destination_inventory"`
	AnalysisTime         time.Time       `json:"analysis_time"`
	Blockers             []Issue         `json:"blockers"`      // Migration cannot proceed
	Warnings             []Issue         `json:"warnings"`      // Migration possible but risky
	InfoMessages         []string        `json:"info_messages"` // Informational
	ReadyForMigration    bool            `json:"ready_for_migration"`
	EstimatedDataGB      int64           `json:"estimated_data_gb"`
	RecommendedActions   []string        `json:"recommended_actions"`
}

// Issue represents a blocker or warning
type Issue struct {
	Severity    string   `json:"severity"`    // "blocker", "warning"
	Category    string   `json:"category"`    // "php_extension", "sql_mode", "disk_space"
	Title       string   `json:"title"`       // Human-readable title
	Description string   `json:"description"` // Detailed explanation
	Impact      string   `json:"impact"`      // What happens if ignored
	Evidence    []string `json:"evidence"`    // URLs, files, configurations proving the issue
	Solution    string   `json:"solution"`    // How to fix it
}

// CompatibilityMatrix defines what versions are compatible
type CompatibilityMatrix struct {
	PHPVersions      []string
	DatabaseVersions map[string][]string // "MySQL" -> ["5.7", "8.0"]
	Extensions       map[string][]string // "igbinary" -> ["7.4", "8.0", "8.1"]
}
