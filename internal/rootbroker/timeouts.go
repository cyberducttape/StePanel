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
		"access": timeoutConfig, "resources": timeoutConfig, "quota": timeoutConfig, "quota-clear": timeoutConfig, "runtime": timeoutConfig,
	},
	"app": {
		"apply": timeoutLifecycle, "delete": timeoutLifecycle, "start": timeoutLifecycle, "stop": timeoutLifecycle,
		"restart": timeoutLifecycle, "rollback": timeoutLifecycle,
	},
	"task": {"apply": timeoutLifecycle, "delete": timeoutLifecycle, "kill": timeoutLifecycle, "history": timeoutConfig},
	"db": {
		"inventory": timeoutConfig,
		"dump":      timeoutBulk, "restore": timeoutBulk, "restore-dump": timeoutBulk, "restore-wordpress": timeoutBulk,
		"provision": timeoutDatabase, "rotate": timeoutDatabase, "drop": timeoutDatabase, "drop-managed": timeoutDatabase, "cleanup-wordpress": timeoutDatabase,
	},
	"git": {
		"generate": timeoutConfig, "public": timeoutConfig, "delete": timeoutLifecycle,
		"clone": timeoutContainer, "verify-key": timeoutConfig,
	},
	"vhost":       {"apply": timeoutConfig, "apply-auth": timeoutConfig, "delete": timeoutConfig},
	"proxy":       {"apply": timeoutConfig, "reload": timeoutConfig},
	"certificate": {"issue": 15 * time.Minute},
}

// helperTimeouts classifies every allow-listed generic helper action.
var helperTimeouts = map[string]map[string]time.Duration{
	"appctl": {
		"composer-install": timeoutBuild, "python-apply": timeoutBuild, "node-tool": timeoutBuild,
		"python-start": timeoutLifecycle, "python-stop": timeoutLifecycle, "python-restart": timeoutLifecycle,
		"worker-apply": timeoutLifecycle, "worker-delete": timeoutLifecycle, "worker-start": timeoutLifecycle,
		"worker-stop": timeoutLifecycle, "worker-restart": timeoutLifecycle,
		"env-apply": timeoutLifecycle, "resource-apply": timeoutConfig, "account-resource-apply": timeoutConfig,
		"resource-status": timeoutConfig,
	},
	"proxyctl": {"apply": timeoutConfig, "delete": timeoutConfig, "reload": timeoutConfig},
	"sitectl": {
		"prepare": timeoutBulk, "prepare-root": timeoutBulk, "seal": timeoutBulk, "delete": timeoutBulk,
		"access": timeoutConfig, "resources": timeoutConfig, "quota": timeoutConfig, "quota-clear": timeoutConfig,
		"runtime": timeoutConfig,
	},
	"vhostctl":  {"apply": timeoutConfig, "apply-auth": timeoutConfig, "delete": timeoutConfig, "import-htaccess": timeoutConfig},
	"runnerctl": {"build": timeoutContainer},
	"gitctl":    {"clone": timeoutContainer},
	"dbctl": {
		"reconcile": timeoutDatabase, "inventory": timeoutConfig, "diagnostics": timeoutConfig, "sessions": timeoutConfig,
		"settings": timeoutConfig, "list": timeoutConfig, "terminate": timeoutConfig,
		"provision": timeoutDatabase, "rotate": timeoutDatabase, "drop-managed": timeoutDatabase,
		"cleanup-wordpress": timeoutDatabase, "drop": timeoutDatabase,
		"dump": timeoutBulk, "restore": timeoutBulk, "restore-dump": timeoutBulk, "restore-wordpress": timeoutBulk,
	},
}

// RequestTimeout returns the broker-side safety deadline for a request.
func RequestTimeout(req *Request) time.Duration {
	if req == nil {
		return defaultRequestTimeout
	}
	var table map[string]time.Duration
	var action string
	switch req.RequestType {
	case "helper":
		if req.Helper == nil {
			return defaultRequestTimeout
		}
		table, action = helperTimeouts[req.Helper.Name], req.Helper.Action
	case "site":
		if req.Site != nil {
			table, action = typedTimeouts["site"], req.Site.Action
		}
	case "app":
		if req.App != nil {
			table, action = typedTimeouts["app"], req.App.Action
		}
	case "task":
		if req.Task != nil {
			table, action = typedTimeouts["task"], req.Task.Action
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
