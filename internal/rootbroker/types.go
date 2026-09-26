package rootbroker

import "encoding/json"

// Request is the top-level RPC request type sent from the unprivileged app.
// All fields are strongly-typed to prevent parsing ambiguity.
type Request struct {
	// RequestType identifies which operation to perform: "site", "app", "db", "vhost", "proxy"
	RequestType string `json:"type"`

	// Site operations: create, delete, seal, prepare, access, resources, quota, quota-clear, runtime
	Site *SiteRequest `json:"site,omitempty"`

	// App operations: apply, start, stop, restart, rollback
	App *AppRequest `json:"app,omitempty"`

	// Database operations: provision, restore-dump, drop
	DB *DBRequest `json:"db,omitempty"`

	// Vhost operations: apply, delete
	Vhost *VhostRequest `json:"vhost,omitempty"`

	// Proxy operations: apply, reload
	Proxy *ProxyRequest `json:"proxy,omitempty"`

	// Git operations: clone, verify-key
	Git *GitRequest `json:"git,omitempty"`
}

// Response is the top-level RPC response sent back to the app.
type Response struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Details json.RawMessage `json:"details,omitempty"` // Operation-specific response
}

// --- Site Operations ---

type SiteRequest struct {
	Action        string `json:"action"` // create, delete, seal, prepare, access, resources, quota, quota-clear, runtime
	Site          string `json:"site"`   // Validated: [a-z0-9_-]{1,32}
	SSHKeys       string `json:"ssh_keys,omitempty"`
	SFTPEnabled   *bool  `json:"sftp,omitempty"`
	ShellEnabled  *bool  `json:"shell,omitempty"`
	PHPWorkers    int    `json:"php_workers,omitempty"`
	DiskMB        int    `json:"disk_mb,omitempty"`
	Inodes        int    `json:"inodes,omitempty"`
	PHPVersion    string `json:"php_version,omitempty"` // e.g. "8.2"
	MemoryMB      int    `json:"memory_mb,omitempty"`
	ExecTimeout   int    `json:"exec_timeout,omitempty"`
	UploadSize    int    `json:"upload_size,omitempty"`
	PostSize      int    `json:"post_size,omitempty"`
	InputTimeout  int    `json:"input_timeout,omitempty"`
	OpcacheSize   int    `json:"opcache_size,omitempty"`
	DisplayErrors bool   `json:"display_errors,omitempty"`
	ErrorLogging  bool   `json:"error_logging,omitempty"`
}

type SiteResponse struct {
	Username string `json:"username,omitempty"` // site_user for new sites
	Created  bool   `json:"created,omitempty"`
	Sealed   bool   `json:"sealed,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
}

// --- App Operations ---

type AppRequest struct {
	Action  string `json:"action"`  // apply, start, stop, restart, rollback
	Site    string `json:"site"`    // Validated site name
	Version string `json:"version"` // Node version (e.g. "18.0.0", validated against pattern)
	Port    int    `json:"port"`    // 1024-65535
	Root    string `json:"root"`    // Site's public root (validated)
}

type AppResponse struct {
	Applied    bool `json:"applied,omitempty"`
	Started    bool `json:"started,omitempty"`
	Stopped    bool `json:"stopped,omitempty"`
	Restarted  bool `json:"restarted,omitempty"`
	RolledBack bool `json:"rolled_back,omitempty"`
	Port       int  `json:"port,omitempty"`
}

// --- Database Operations ---

type DBRequest struct {
	Action   string `json:"action"`              // provision, restore-dump, drop
	Database string `json:"database"`            // Database name (validated)
	Username string `json:"username"`            // DB username (validated)
	Password string `json:"password"`            // DB password (not logged)
	Site     string `json:"site"`                // Associated site
	Encoding string `json:"encoding"`            // utf8mb4, UTF8, etc.
	DumpData []byte `json:"dump_data,omitempty"` // For restore-dump action
}

type DBResponse struct {
	Provisioned bool   `json:"provisioned,omitempty"`
	Restored    bool   `json:"restored,omitempty"`
	Dropped     bool   `json:"dropped,omitempty"`
	Database    string `json:"database,omitempty"`
	Username    string `json:"username,omitempty"`
}

// --- Vhost Operations ---

type VhostRequest struct {
	Action        string `json:"action"` // apply, delete, apply-auth
	Site          string `json:"site"`
	Domain        string `json:"domain"` // Validated domain name
	SSLCertPath   string `json:"ssl_cert_path,omitempty"`
	SSLKeyPath    string `json:"ssl_key_path,omitempty"`
	WebServer     string `json:"webserver"` // caddy, apache, nginx, ols
	BasicAuthUser string `json:"basic_auth_user,omitempty"`
	BasicAuthHash string `json:"basic_auth_hash,omitempty"` // bcrypt hash
	UpstreamPort  int    `json:"upstream_port,omitempty"`
}

type VhostResponse struct {
	Applied bool   `json:"applied,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
	Domain  string `json:"domain,omitempty"`
}

// --- Proxy Operations ---

type ProxyRequest struct {
	Action    string            `json:"action"`              // apply, reload
	WebServer string            `json:"webserver"`           // caddy, apache, nginx, ols
	Upstreams map[string]string `json:"upstreams,omitempty"` // domain -> upstream address
	CertPath  string            `json:"cert_path,omitempty"`
	KeyPath   string            `json:"key_path,omitempty"`
}

type ProxyResponse struct {
	Applied  bool `json:"applied,omitempty"`
	Reloaded bool `json:"reloaded,omitempty"`
}

// --- Git Operations ---

type GitRequest struct {
	Action       string   `json:"action"`                // clone, verify-key
	Repository   string   `json:"repository"`            // Git URL (validated)
	Ref          string   `json:"ref"`                   // Branch/tag
	Destination  string   `json:"destination"`           // Clone destination (validated)
	PrivateKey   string   `json:"private_key,omitempty"` // SSH key content
	KnownHosts   string   `json:"known_hosts,omitempty"` // SSH known_hosts
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
}

type GitResponse struct {
	Cloned   bool   `json:"cloned,omitempty"`
	Verified bool   `json:"verified,omitempty"`
	Commit   string `json:"commit,omitempty"`
}

// --- Error Types ---

// ErrorResponse is returned when a request fails
type ErrorResponse struct {
	Code    string `json:"code"`              // e.g. "INVALID_SITE", "PERMISSION_DENIED", "SYSTEM_ERROR"
	Message string `json:"message"`           // Human-readable error
	Details string `json:"details,omitempty"` // Additional context
}
