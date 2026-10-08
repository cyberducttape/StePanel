package rootbroker

import (
	"encoding/json"

	"github.com/cyberducttape/StePanel/internal/operations"
)

// Request is the top-level RPC request type sent from the unprivileged app.
// All fields are strongly-typed to prevent parsing ambiguity.
type Request struct {
	// RequestType identifies which operation to perform, such as "health", "site", "app", "db", "vhost", "proxy", or "certificate".
	RequestType string `json:"type"`

	// Fencing carries the durable lease generations protecting this request.
	// The broker verifies supplied tokens against the shared control-plane DB
	// immediately before dispatching the host mutation.
	Fencing []operations.FencingToken `json:"fencing,omitempty"`

	// Site operations: create, delete, seal, prepare, access, resources, quota, quota-clear, runtime
	Site *SiteRequest `json:"site,omitempty"`

	// App operations: apply, start, stop, restart, rollback
	App *AppRequest `json:"app,omitempty"`

	// Worker operations: apply, delete, start, stop, restart
	Worker *WorkerRequest `json:"worker,omitempty"`

	// Sandboxed build operation.
	Runner *RunnerRequest `json:"runner,omitempty"`

	// Database operations: inventory, provision, dump, restore, restore-dump,
	// restore-wordpress, drop, drop-managed, cleanup-wordpress, rotate
	DB *DBRequest `json:"db,omitempty"`

	// Vhost operations: apply, delete
	Vhost *VhostRequest `json:"vhost,omitempty"`

	// Proxy operations: apply, reload
	Proxy *ProxyRequest `json:"proxy,omitempty"`

	// Git operations: clone, verify-key
	Git *GitRequest `json:"git,omitempty"`

	// Certificate requests expose only the bounded issuance operation; they
	// cannot select an executable, arbitrary arguments, or certificate paths.
	Certificate *CertificateRequest `json:"certificate,omitempty"`

	// Task requests expose validated scheduler controls without helper argv.
	Task *TaskRequest `json:"task,omitempty"`

	// Environment requests update one site's systemd environment file.
	Environment *EnvironmentRequest `json:"environment,omitempty"`
	Resource    *ResourceRequest    `json:"resource,omitempty"`

	// WordPress requests run one named wp-cli operation as the site's
	// isolated user; they cannot select the executable, path, or argv.
	WordPress *WordPressRequest `json:"wordpress,omitempty"`
}

type EnvironmentRequest struct {
	Action  string `json:"action"`
	Site    string `json:"site"`
	Content string `json:"content"`
}

type ResourceRequest struct {
	Action       string `json:"action"`
	Site         string `json:"site,omitempty"`
	Account      string `json:"account,omitempty"`
	CPUPercent   int    `json:"cpu_percent"`
	CPUWeight    int    `json:"cpu_weight"`
	MemoryHighMB int    `json:"memory_high_mb"`
	MemoryMB     int    `json:"memory_mb"`
	IOWeight     int    `json:"io_weight"`
	TasksMax     int    `json:"tasks_max"`
}

type ResourceResponse struct {
	Output string `json:"output,omitempty"`
}

type CertificateRequest struct {
	Action string `json:"action"`
	Domain string `json:"domain"`
	Email  string `json:"email"`
}

type CertificateResponse struct {
	Issued bool   `json:"issued"`
	Domain string `json:"domain"`
	Output string `json:"output,omitempty"`
}

type TaskRequest struct {
	Action             string `json:"action"`
	Site               string `json:"site"`
	Name               string `json:"name"`
	Runtime            string `json:"runtime,omitempty"`
	Command            string `json:"command,omitempty"`
	OnCalendar         string `json:"on_calendar,omitempty"`
	TimeoutSec         int    `json:"timeout_sec,omitempty"`
	Enabled            bool   `json:"enabled,omitempty"`
	MinIntervalSeconds int    `json:"min_interval_seconds,omitempty"`
	MissedRunPolicy    string `json:"missed_run_policy,omitempty"`
	CPUPercent         int    `json:"cpu_percent,omitempty"`
	MemoryMB           int    `json:"memory_mb,omitempty"`
	TasksMax           int    `json:"tasks_max,omitempty"`
	NotifyWebhook      string `json:"notify_webhook,omitempty"`
}

type TaskResponse struct {
	Killed  bool   `json:"killed,omitempty"`
	Applied bool   `json:"applied,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
	Output  string `json:"output,omitempty"`
}

// WordPressRequest names one wp-cli operation. Only the fields that the
// action documents in wordpressArgs are read.
type WordPressRequest struct {
	Action string `json:"action"`
	Site   string `json:"site"`
	// Name is the option or wp-config constant for option-* and config-set.
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
	// Search and Replace are the site URLs for search-replace.
	Search  string `json:"search,omitempty"`
	Replace string `json:"replace,omitempty"`
	// Database settings for config-create.
	DBName   string `json:"db_name,omitempty"`
	DBUser   string `json:"db_user,omitempty"`
	DBHost   string `json:"db_host,omitempty"`
	DBPrefix string `json:"db_prefix,omitempty"`
	// Secret is written to wp-cli's stdin for --prompt; it never reaches argv.
	Secret string `json:"secret,omitempty"`
}

type WordPressResponse struct {
	Output string `json:"output,omitempty"`
	// Active reports maintenance-status.
	Active bool `json:"active,omitempty"`
}

// Response is the top-level RPC response sent back to the app.
type Response struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Details json.RawMessage `json:"details,omitempty"` // Operation-specific response
}

// --- Site Operations ---

type SiteRequest struct {
	Action            string `json:"action"` // create, delete, seal, prepare, access, resources, quota, quota-clear, runtime
	Site              string `json:"site"`   // Validated: [a-z0-9_-]{1,32}
	SSHKeys           string `json:"ssh_keys,omitempty"`
	SFTPEnabled       *bool  `json:"sftp,omitempty"`
	ShellEnabled      *bool  `json:"shell,omitempty"`
	PHPWorkers        int    `json:"php_workers,omitempty"`
	DiskMB            int    `json:"disk_mb,omitempty"`
	Inodes            int    `json:"inodes,omitempty"`
	PHPVersion        string `json:"php_version,omitempty"`  // e.g. "8.2"
	MemoryLimit       string `json:"memory_limit,omitempty"` // e.g. "256M"
	UploadMaxFilesize string `json:"upload_max_filesize,omitempty"`
	PostMaxSize       string `json:"post_max_size,omitempty"`
	MaxInputVars      int    `json:"max_input_vars,omitempty"`
	OPcache           bool   `json:"opcache,omitempty"`
	ErrorReporting    string `json:"error_reporting,omitempty"`
	MemoryMB          int    `json:"memory_mb,omitempty"`
	ExecTimeout       int    `json:"exec_timeout,omitempty"`
	UploadSize        int    `json:"upload_size,omitempty"`
	PostSize          int    `json:"post_size,omitempty"`
	InputTimeout      int    `json:"input_timeout,omitempty"`
	OpcacheSize       int    `json:"opcache_size,omitempty"`
	DisplayErrors     bool   `json:"display_errors,omitempty"`
	ErrorLogging      bool   `json:"error_logging,omitempty"`
}

type SiteResponse struct {
	Username string `json:"username,omitempty"` // site_user for new sites
	Created  bool   `json:"created,omitempty"`
	Sealed   bool   `json:"sealed,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
}

// --- App Operations ---

type AppRequest struct {
	Action             string `json:"action"` // lifecycle, package, Python, or Node tooling action
	Site               string `json:"site"`
	Version            string `json:"version"`
	Port               int    `json:"port"`
	Root               string `json:"root"`
	ToolAction         string `json:"tool_action,omitempty"`
	PackageManager     string `json:"package_manager,omitempty"`
	Development        bool   `json:"development,omitempty"`
	OptimizeAutoloader bool   `json:"optimize_autoloader,omitempty"`
	EntryPoint         string `json:"entrypoint,omitempty"`
	Workers            int    `json:"workers,omitempty"`
}

type AppResponse struct {
	Applied    bool   `json:"applied,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
	Started    bool   `json:"started,omitempty"`
	Stopped    bool   `json:"stopped,omitempty"`
	Restarted  bool   `json:"restarted,omitempty"`
	RolledBack bool   `json:"rolled_back,omitempty"`
	Port       int    `json:"port,omitempty"`
	Output     string `json:"output,omitempty"`
}

type WorkerRequest struct {
	Action    string `json:"action"`
	Site      string `json:"site"`
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"`
	Root      string `json:"root,omitempty"`
	Processes int    `json:"processes,omitempty"`
	MemoryMB  int    `json:"memory_mb,omitempty"`
	Retries   int    `json:"retries,omitempty"`
}

type WorkerResponse struct {
	Applied   bool   `json:"applied,omitempty"`
	Deleted   bool   `json:"deleted,omitempty"`
	Started   bool   `json:"started,omitempty"`
	Stopped   bool   `json:"stopped,omitempty"`
	Restarted bool   `json:"restarted,omitempty"`
	Output    string `json:"output,omitempty"`
}

type RunnerRequest struct {
	Action        string `json:"action"`
	Site          string `json:"site"`
	Image         string `json:"image"`
	Root          string `json:"root"`
	Script        string `json:"script"`
	CPUPercent    int    `json:"cpu_percent"`
	MemoryMB      int    `json:"memory_mb"`
	TasksMax      int    `json:"tasks_max"`
	NetworkMode   string `json:"network_mode"`
	MaxImageBytes int64  `json:"max_image_bytes"`
}

// --- Database Operations ---

type DBRequest struct {
	Action    string `json:"action"`   // inventory, provision, dump, restore-dump, drop
	Database  string `json:"database"` // Database name (validated)
	SessionID string `json:"session_id,omitempty"`
	Username  string `json:"username"`            // DB username (validated)
	Password  string `json:"password"`            // DB password (not logged)
	Site      string `json:"site"`                // Associated site
	Encoding  string `json:"encoding"`            // utf8mb4, UTF8, etc.
	DumpData  []byte `json:"dump_data,omitempty"` // For restore-dump action
	DumpPath  string `json:"dump_path,omitempty"` // Root broker reads and streams this validated staging file
}

type DBResponse struct {
	Provisioned bool   `json:"provisioned,omitempty"`
	Restored    bool   `json:"restored,omitempty"`
	Dropped     bool   `json:"dropped,omitempty"`
	Database    string `json:"database,omitempty"`
	Username    string `json:"username,omitempty"`
	Output      string `json:"output,omitempty"`
	DumpData    []byte `json:"dump_data,omitempty"`
}

// --- Vhost Operations ---

type VhostRequest struct {
	Action        string `json:"action"` // apply, delete, apply-auth
	Site          string `json:"site"`
	Domain        string `json:"domain"`         // Validated domain name
	Name          string `json:"name,omitempty"` // Managed config name for delete
	SSLCertPath   string `json:"ssl_cert_path,omitempty"`
	SSLKeyPath    string `json:"ssl_key_path,omitempty"`
	WebServer     string `json:"webserver"` // caddy, apache, nginx, ols
	BasicAuthUser string `json:"basic_auth_user,omitempty"`
	BasicAuthHash string `json:"basic_auth_hash,omitempty"` // bcrypt hash
	UpstreamPort  int    `json:"upstream_port,omitempty"`
	Directives    string `json:"directives,omitempty"`
}

type VhostResponse struct {
	Applied bool   `json:"applied,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
	Domain  string `json:"domain,omitempty"`
}

// --- Proxy Operations ---

type ProxyRequest struct {
	Action    string            `json:"action"` // apply, reload, delete
	Site      string            `json:"site,omitempty"`
	Domain    string            `json:"domain,omitempty"`
	Backend   string            `json:"backend,omitempty"`
	Name      string            `json:"name,omitempty"`
	WebServer string            `json:"webserver"`           // caddy, apache, nginx, ols
	Upstreams map[string]string `json:"upstreams,omitempty"` // domain -> upstream address
	CertPath  string            `json:"cert_path,omitempty"`
	KeyPath   string            `json:"key_path,omitempty"`
}

type ProxyResponse struct {
	Applied  bool `json:"applied,omitempty"`
	Reloaded bool `json:"reloaded,omitempty"`
	Deleted  bool `json:"deleted,omitempty"`
}

// --- Git Operations ---

type GitRequest struct {
	Action       string   `json:"action"`                // clone, delete, verify-key
	Site         string   `json:"site,omitempty"`        // managed site for delete
	Repository   string   `json:"repository"`            // Git URL (validated)
	Ref          string   `json:"ref"`                   // Branch/tag
	Destination  string   `json:"destination"`           // Clone destination (validated)
	PrivateKey   string   `json:"private_key,omitempty"` // SSH key content
	KnownHosts   string   `json:"known_hosts,omitempty"` // SSH known_hosts
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
}

type GitResponse struct {
	Cloned    bool   `json:"cloned,omitempty"`
	Verified  bool   `json:"verified,omitempty"`
	Deleted   bool   `json:"deleted,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
	Commit    string `json:"commit,omitempty"`
}

// --- Error Types ---

// ErrorResponse is returned when a request fails
type ErrorResponse struct {
	Code    string `json:"code"`              // e.g. "INVALID_SITE", "PERMISSION_DENIED", "SYSTEM_ERROR"
	Message string `json:"message"`           // Human-readable error
	Details string `json:"details,omitempty"` // Additional context
}
