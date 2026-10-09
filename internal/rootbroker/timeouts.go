package rootbroker

import (
	"time"

	stepanelhelper "github.com/cyberducttape/StePanel/internal/helper"
)

const defaultRequestTimeout = stepanelhelper.ConfigMutationTimeout

// Broker deadlines by timeout class. The broker deadline is a safety net:
// the unprivileged caller still supplies its own, usually shorter, context.
// It must never be shorter than the longest timeout any caller gives the
// same action, because expiry SIGKILLs the helper before it can run its own
// rollback, leaving half-applied host state (for example a recursive chown
// cut off part-way through a large site).
const (
	timeoutConfig    = stepanelhelper.ConfigMutationTimeout    // 30s: config writes and reads
	timeoutLifecycle = stepanelhelper.ServiceLifecycleTimeout  // 60s: service start/stop/restart
	timeoutBuild     = stepanelhelper.PackageBuildTimeout      // 15m: dependency installs
	timeoutDatabase  = stepanelhelper.DatabaseOperationTimeout // 60m: database mutations
	timeoutContainer = stepanelhelper.ContainerOperationTimeout
	// timeoutBulk covers operations proportional to site or database size:
	// dumps, restores, recursive ownership changes, and tree removal.
	timeoutBulk = stepanelhelper.BackupRestoreTimeout // 120m
)

// typedTimeouts classifies every typed request action. An action missing
// here falls back to timeoutConfig; TestEveryBrokerActionHasATimeoutClass
// keeps the table complete.
var typedTimeouts = map[string]map[string]time.Duration{
	"site": {
		// prepare and seal apply ownership and ACLs recursively over the
		// whole site tree; delete removes it.
		"create": timeoutBulk, "prepare": timeoutBulk, "seal": timeoutBulk, "delete": timeoutBulk,
		"access": timeoutConfig, "ftp": timeoutConfig, "resources": timeoutConfig, "quota": timeoutConfig, "quota-clear": timeoutConfig, "runtime": timeoutConfig,
	},
	"app": {
		"apply": timeoutLifecycle, "delete": timeoutLifecycle, "start": timeoutLifecycle, "stop": timeoutLifecycle,
		"restart": timeoutLifecycle, "rollback": timeoutLifecycle,
		"composer-install": timeoutBuild, "node-tool": timeoutBuild, "python-apply": timeoutBuild,
		"python-start": timeoutLifecycle, "python-stop": timeoutLifecycle, "python-restart": timeoutLifecycle,
	},
	"worker": {
		"apply": timeoutLifecycle, "delete": timeoutLifecycle, "start": timeoutLifecycle, "stop": timeoutLifecycle,
		"restart": timeoutLifecycle,
	},
	"runner":      {"build": timeoutContainer},
	"task":        {"apply": timeoutLifecycle, "delete": timeoutLifecycle, "kill": timeoutLifecycle, "history": timeoutConfig},
	"environment": {"apply": timeoutLifecycle},
	"resource":    {"apply-account": timeoutConfig, "apply-site": timeoutConfig, "status": timeoutConfig},
	// wp-cli boots WordPress for every call; updates download packages and
	// search-replace rewrites every table.
	"wordpress": {
		"status": timeoutLifecycle, "maintenance-status": timeoutLifecycle, "maintenance-activate": timeoutLifecycle, "maintenance-deactivate": timeoutLifecycle,
		"option-get": timeoutLifecycle, "option-update": timeoutLifecycle, "config-create": timeoutLifecycle, "config-set": timeoutLifecycle, "config-set-password": timeoutLifecycle,
		"rewrite-flush": timeoutLifecycle, "cache-flush": timeoutLifecycle,
		"core-update": timeoutBuild, "plugin-update-all": timeoutBuild, "theme-update-all": timeoutBuild, "cron-run-due": timeoutBuild,
		"search-replace": timeoutDatabase,
	},
	"db": {
		"inventory": timeoutConfig, "reconcile": timeoutDatabase, "diagnostics": timeoutConfig,
		"sessions": timeoutConfig, "settings": timeoutConfig, "terminate": timeoutConfig,
		"dump": timeoutBulk, "restore": timeoutBulk, "restore-dump": timeoutBulk, "restore-wordpress": timeoutBulk,
		"provision": timeoutDatabase, "rotate": timeoutDatabase, "drop": timeoutDatabase, "drop-managed": timeoutDatabase, "cleanup-wordpress": timeoutDatabase,
	},
	"git": {
		"generate": timeoutConfig, "public": timeoutConfig, "delete": timeoutLifecycle,
		"clone": timeoutContainer, "verify-key": timeoutConfig,
	},
	"vhost":       {"apply": timeoutConfig, "apply-auth": timeoutConfig, "delete": timeoutConfig, "import-htaccess": timeoutConfig},
	"proxy":       {"apply": timeoutConfig, "reload": timeoutConfig, "delete": timeoutConfig},
	"certificate": {"issue": 15 * time.Minute},
}

// RequestTimeout returns the broker-side safety deadline for a request.
func RequestTimeout(req *Request) time.Duration {
	if req == nil {
		return defaultRequestTimeout
	}
	var table map[string]time.Duration
	var action string
	switch req.RequestType {
	case "site":
		if req.Site != nil {
			table, action = typedTimeouts["site"], req.Site.Action
		}
	case "app":
		if req.App != nil {
			table, action = typedTimeouts["app"], req.App.Action
		}
	case "worker":
		if req.Worker != nil {
			table, action = typedTimeouts["worker"], req.Worker.Action
		}
	case "runner":
		if req.Runner != nil {
			table, action = typedTimeouts["runner"], req.Runner.Action
		}
	case "task":
		if req.Task != nil {
			table, action = typedTimeouts["task"], req.Task.Action
		}
	case "environment":
		if req.Environment != nil {
			table, action = typedTimeouts["environment"], req.Environment.Action
		}
	case "resource":
		if req.Resource != nil {
			table, action = typedTimeouts["resource"], req.Resource.Action
		}
	case "wordpress":
		if req.WordPress != nil {
			table, action = typedTimeouts["wordpress"], req.WordPress.Action
		}
	case "db":
		if req.DB != nil {
			table, action = typedTimeouts["db"], req.DB.Action
		}
	case "git":
		if req.Git != nil {
			table, action = typedTimeouts["git"], req.Git.Action
		}
	case "vhost":
		if req.Vhost != nil {
			table, action = typedTimeouts["vhost"], req.Vhost.Action
		}
	case "proxy":
		if req.Proxy != nil {
			table, action = typedTimeouts["proxy"], req.Proxy.Action
		}
	case "certificate":
		if req.Certificate != nil {
			table, action = typedTimeouts["certificate"], req.Certificate.Action
		}
	}
	if timeout, ok := table[action]; ok {
		return timeout
	}
	return defaultRequestTimeout
}
