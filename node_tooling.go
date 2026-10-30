package stepanel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

var nodeToolActions = map[string]bool{"install": true, "build": true}
var nodePackageManagers = map[string]bool{"npm": true, "yarn": true, "pnpm": true}

type nodeToolingCommand struct {
	Site           string `json:"site"`
	Action         string `json:"action"`
	PackageManager string `json:"package_manager"`
}

func normalizeNodeToolingCommand(input nodeToolingCommand) (nodeToolingCommand, error) {
	input.Site = safeUser(input.Site)
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	input.PackageManager = strings.ToLower(strings.TrimSpace(input.PackageManager))
	if input.Site == "" || !nodeToolActions[input.Action] || !nodePackageManagers[input.PackageManager] {
		return nodeToolingCommand{}, errors.New("action or package manager is not supported")
	}
	return input, nil
}

func decodeNodeToolingCommand(body []byte) (nodeToolingCommand, error) {
	if len(body) > 4096 {
		return nodeToolingCommand{}, errors.New("Node tooling request is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var input nodeToolingCommand
	if err := decoder.Decode(&input); err != nil {
		return nodeToolingCommand{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nodeToolingCommand{}, errors.New("request body must contain one JSON value")
	}
	return normalizeNodeToolingCommand(input)
}

func validateNodeToolingPayload(body []byte) error {
	_, err := decodeNodeToolingCommand(body)
	return err
}

func (a *App) nodeTooling(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	var input nodeToolingCommand
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	input, err := normalizeNodeToolingCommand(input)
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	access, ok := a.requireSiteAccess(w, r, input.Site, "site is not assigned to this account", 403)
	if !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for Node operations", http.StatusForbidden)
		return
	}
	root, err := siteDocRoot(a.Config, input.Site)
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
	if _, err := a.applyNodeTooling(operationCtx, access, a.Auth.AuditActor(r), input, root); err != nil {
		http.Error(w, "Node tooling action failed", 502)
		return
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "Node tooling operation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusAccepted, input)
}

func executeNodeToolingCommand(a *App, ctx context.Context, command siteOperationCommand, principal siteOperationPrincipal) ([]byte, error) {
	input, err := decodeNodeToolingCommand(command.Body)
	if err != nil || input.Site != command.Site {
		return nil, errors.New("invalid Node tooling command")
	}
	access, err := a.authorizeDurableSiteJob(input.Site, principal.Actor, false)
	if err != nil {
		return nil, fmt.Errorf("Node tooling authorization: %w", err)
	}
	if !a.durableSiteOperationScopeAllowed(principal, "site:deploy") {
		return nil, errors.New("Node tooling authorization: required site:deploy permission is no longer active")
	}
	operationCtx, release, err := a.acquireSiteMutationLockContext(ctx, access.Site())
	if err != nil {
		return nil, fmt.Errorf("Node tooling operation is busy: %w", err)
	}
	defer release()
	if err := operationCtx.Err(); err != nil {
		return nil, fmt.Errorf("Node tooling operation cancelled: %w", err)
	}
	root, err := siteDocRoot(a.Config, access.Site())
	if err != nil {
		return nil, errors.New("invalid site root")
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, errors.New("site document root does not exist")
	}
	return a.applyNodeTooling(operationCtx, access, principal.Actor, input, root)
}

// applyNodeTooling is the shared typed operation service used by both the
// direct HTTP adapter and durable worker. Authorization and mutation locking
// are established by those entry points before invoking it.
func (a *App) applyNodeTooling(ctx context.Context, access SiteCapability, actor string, input nodeToolingCommand, root string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var err error
	if a.Config.Production {
		err = runTypedAppOperation(ctx, a.Config, rootbroker.AppRequest{Action: "node-tool", Site: access.Site(), ToolAction: input.Action, PackageManager: input.PackageManager, Root: root})
	} else {
		err = runHelperCommandWithTimeout(ctx, a.Config, helperPackageBuildTimeout, a.Config.AppCtl, "node-tool", access.Site(), input.Action, input.PackageManager, root)
	}
	if err != nil {
		return nil, errors.New("Node tooling action failed")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("Node tooling operation cancelled: %w", err)
	}
	TelemetryAudit(a.Config.AuditLog, actor, "node."+input.Action, access.Site(), input.PackageManager)
	return json.Marshal(input)
}

func (a *App) durableSiteOperationScopeAllowed(principal siteOperationPrincipal, required string) bool {
	if principal.Actor == "" {
		return false
	}
	if principal.Actor == a.Auth.Username {
		return true
	}
	if a.Accounts == nil {
		return false
	}
	account, ok := a.Accounts.Get(principal.Actor)
	if !ok || a.Accounts.TenantSuspended(account.Username) || !roleAllowsCustomerScope(accountRole(account), required) {
		return false
	}
	if !principal.Token || len(principal.Scopes) == 0 {
		return true
	}
	for _, scope := range principal.Scopes {
		if scope == required || (required == "site:deploy" && scope == "deploy:write") || (required == "deploy:write" && scope == "site:deploy") {
			return true
		}
	}
	return false
}
