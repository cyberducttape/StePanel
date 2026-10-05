package stepanel

import (
	"net/http"
	"os"
	"strings"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

var nodeToolActions = map[string]bool{"install": true, "build": true}
var nodePackageManagers = map[string]bool{"npm": true, "yarn": true, "pnpm": true}

func (a *App) nodeTooling(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	var input struct {
		Site           string `json:"site"`
		Action         string `json:"action"`
		PackageManager string `json:"package_manager"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	input.Site = safeUser(input.Site)
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	input.PackageManager = strings.ToLower(strings.TrimSpace(input.PackageManager))
	if input.Site == "" || !nodeToolActions[input.Action] || !nodePackageManagers[input.PackageManager] {
		http.Error(w, "action or package manager is not supported", 422)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, input.Site, "site is not assigned to this account", 403); !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for Node operations", http.StatusForbidden)
		return
	}
	root, err := safePath(a.Config.WebRoot, "sites", input.Site, "public")
	if err != nil {
		http.Error(w, "invalid site root", 422)
		return
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		http.Error(w, "site document root does not exist", 422)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), input.Site)
	if lockErr != nil {
		http.Error(w, "Node tooling operation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "Node tooling operation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	var toolErr error
	if a.Config.Production {
		toolErr = runTypedAppOperation(operationCtx, a.Config, rootbroker.AppRequest{Action: "node-tool", Site: input.Site, ToolAction: input.Action, PackageManager: input.PackageManager, Root: root})
	} else {
		toolErr = runHelperCommandWithTimeout(operationCtx, a.Config, helperPackageBuildTimeout, a.Config.AppCtl, "node-tool", input.Site, input.Action, input.PackageManager, root)
	}
	if err := toolErr; err != nil {
		http.Error(w, "Node tooling action failed", 502)
		return
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "Node tooling operation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "node."+input.Action, input.Site, input.PackageManager)
	writeJSON(w, http.StatusAccepted, input)
}
