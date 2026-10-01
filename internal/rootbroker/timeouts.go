package rootbroker

import (
	"time"

	stepanelhelper "github.com/cyberducttape/StePanel/internal/helper"
)

const defaultRequestTimeout = stepanelhelper.ConfigMutationTimeout

// RequestTimeout returns the broker-side safety deadline for a typed request.
// The unprivileged caller still supplies its own context; this deadline must
// never be shorter than the caller's timeout class, because expiry SIGKILLs the
// helper before it can run its own rollback.
func RequestTimeout(req *Request) time.Duration {
	if req == nil {
		return defaultRequestTimeout
	}
	switch req.RequestType {
	case "certificate":
		if req.Certificate != nil && req.Certificate.Action == "issue" {
			return 15 * time.Minute
		}
	case "app", "task":
		return stepanelhelper.ServiceLifecycleTimeout
	case "site":
		if req.Site != nil && req.Site.Action == "delete" {
			return stepanelhelper.ServiceLifecycleTimeout
		}
	case "git":
		if req.Git != nil && req.Git.Action == "delete" {
			return stepanelhelper.ServiceLifecycleTimeout
		}
	case "helper":
		if req.Helper != nil {
			return helperRequestTimeout(req.Helper)
		}
	}
	return defaultRequestTimeout
}

func helperRequestTimeout(req *HelperRequest) time.Duration {
	switch req.Name {
	case "appctl":
		switch req.Action {
		case "composer-install", "python-apply", "node-tool":
			return stepanelhelper.PackageBuildTimeout
		case "python-start", "python-stop", "python-restart",
			"worker-apply", "worker-delete", "worker-start", "worker-stop", "worker-restart":
			return stepanelhelper.ServiceLifecycleTimeout
		}
	case "sitectl":
		if req.Action == "delete" {
			return stepanelhelper.ServiceLifecycleTimeout
		}
	}
	return defaultRequestTimeout
}
