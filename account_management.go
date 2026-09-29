package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// PlanUsageMetrics shows a customer's usage against their plan limits
type PlanUsageMetrics struct {
	Username          string    `json:"username"`
	Plan              string    `json:"plan"`
	Suspended         bool      `json:"suspended"`
	CreatedAt         time.Time `json:"created_at"`
	SitesUsed         int       `json:"sites_used"`
	SiteLimit         int       `json:"site_limit"`
	SitesPercent      int       `json:"sites_percent"` // 0-100
	DatabasesUsed     int       `json:"databases_used"`
	DatabaseLimit     int       `json:"database_limit"`
	DatabasesPercent  int       `json:"databases_percent"`
	WarningThreshold  int       `json:"warning_threshold_percent"`  // 80
	CriticalThreshold int       `json:"critical_threshold_percent"` // 95
}

// CustomerAccountView is the narrow account representation exposed to a
// customer. It contains only tenant-owned inventory and plan limits.
type CustomerAccountView struct {
	Username string `json:"username"`
	TenantID string `json:"tenant_id"`
	Role     string `json:"role"`
	PlanUsageMetrics
	Sites                 []string `json:"sites"`
	MFAEnabled            bool     `json:"mfa_enabled"`
	PasswordResetRequired bool     `json:"password_reset_required"`
	MFAEnrollmentRequired bool     `json:"mfa_enrollment_required"`
	CPUPercent            int      `json:"cpu_percent"`
	MemoryMB              int      `json:"memory_mb"`
	DiskMB                int      `json:"disk_mb"`
	Inodes                int      `json:"inodes"`
	TasksMax              int      `json:"tasks_max"`
	RedisMemoryMB         int      `json:"redis_memory_mb"`
}

// SuspensionRequest holds parameters for account suspension
type SuspensionRequest struct {
	Username  string `json:"username"`
	Reason    string `json:"reason"`
	Permanent bool   `json:"permanent"` // false = temporary, auto-lift on usage drop; true = manual only
}

// SuspensionAuditEvent logs suspension activities
type SuspensionAuditEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"` // suspend, unsuspend, warn
	Username  string    `json:"username"`
	Reason    string    `json:"reason"`
	Triggered string    `json:"triggered_by"`        // admin or automatic
	Threshold string    `json:"threshold,omitempty"` // sites_limit, database_limit
}

func (a *App) setAccountSuspended(ctx context.Context, username string, suspended bool) (HostingAccount, error) {
	if err := ctx.Err(); err != nil {
		return HostingAccount{}, err
	}
	action := "unsuspend"
	if suspended {
		action = "suspend"
	}
	if err := failureInjection(action, "before-persist"); err != nil {
		return HostingAccount{}, err
	}
	account, err := a.Accounts.SetSuspended(username, suspended)
	if err != nil {
		return HostingAccount{}, err
	}
	processKillInjection(action, "persisted")
	return account, nil
}

func (a *App) autoSuspendAccount(ctx context.Context, username string) error {
	operationCtx, release, err := a.acquireSiteMutationLockContext(ctx, "account:"+username)
	if err != nil {
		return err
	}
	defer release()
	_, err = a.setAccountSuspended(operationCtx, username, true)
	return err
}

// accountPlanStatus returns the current usage and limits for a customer account
func (a *App) accountPlanStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Only administrators can check account plans
	if !a.Auth.IsAdministrator(r) {
		http.Error(w, "unauthorized", http.StatusForbidden)
		return
	}

	username := strings.TrimSpace(r.URL.Query().Get("account"))
	if username == "" {
		http.Error(w, "missing account parameter", http.StatusBadRequest)
		return
	}

	username = safeUser(username)
	if username == "" {
		http.Error(w, "invalid account name", http.StatusUnprocessableEntity)
		return
	}

	account, ok := a.Accounts.Get(username)
	if !ok {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}
	if tenant, tenantOK := a.Accounts.Get(accountTenantID(account)); tenantOK {
		account = tenant
	}

	metrics, err := a.planUsageMetrics(account)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (a *App) planUsageMetrics(account HostingAccount) (PlanUsageMetrics, error) {
	plan, exists := hostingPlans[account.Plan]
	if !exists {
		return PlanUsageMetrics{}, errors.New("plan not found")
	}
	databasesUsed := 0
	for _, site := range account.Sites {
		databases, err := managedDatabasesForSite(a.Config, site)
		if err == nil {
			databasesUsed += len(databases)
		}
	}
	percent := func(used, limit int) int {
		if limit <= 0 {
			return 0
		}
		return (used * 100) / limit
	}
	return PlanUsageMetrics{
		Username: account.Username, Plan: account.Plan, Suspended: account.Suspended,
		CreatedAt: account.CreatedAt, SitesUsed: len(account.Sites), SiteLimit: plan.SiteLimit,
		SitesPercent: percent(len(account.Sites), plan.SiteLimit), DatabasesUsed: databasesUsed,
		DatabaseLimit: plan.DatabaseLimit, DatabasesPercent: percent(databasesUsed, plan.DatabaseLimit),
		WarningThreshold: 80, CriticalThreshold: 95,
	}, nil
}

// accountMe exposes only the authenticated customer's own tenant boundary.
// Administrator and bearer-token requests are rejected so this cannot become
// an account-enumeration primitive.
func (a *App) accountMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	username := a.Auth.UsernameForRequest(r)
	if username == "" || a.Auth.IsAdministrator(r) || a.Auth.IsAPITokenRequest(r) || a.Accounts == nil {
		http.Error(w, "customer account required", http.StatusForbidden)
		return
	}
	account, ok := a.Accounts.Get(username)
	if !ok {
		http.Error(w, "customer account required", http.StatusForbidden)
		return
	}
	tenant, tenantOK := a.Accounts.Get(accountTenantID(account))
	if !tenantOK {
		http.Error(w, "customer tenant is unavailable", http.StatusServiceUnavailable)
		return
	}
	metrics, err := a.planUsageMetrics(tenant)
	if err != nil {
		http.Error(w, "account plan is unavailable", http.StatusServiceUnavailable)
		return
	}
	plan := hostingPlans[tenant.Plan]
	writeJSON(w, http.StatusOK, CustomerAccountView{
		Username: username, TenantID: accountTenantID(account), Role: accountRole(account),
		PlanUsageMetrics:      metrics,
		Sites:                 append([]string(nil), tenant.Sites...),
		MFAEnabled:            account.TOTPSecret != "",
		PasswordResetRequired: account.PasswordResetRequired,
		MFAEnrollmentRequired: account.MFAEnrollmentRequired,
		CPUPercent:            plan.CPUPercent, MemoryMB: plan.MemoryMB, DiskMB: plan.DiskMB,
		Inodes: plan.Inodes, TasksMax: plan.TasksMax, RedisMemoryMB: plan.RedisMemoryMB,
	})
}

type tenantMemberRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	TOTPSecret string `json:"totp_secret"`
	Role       string `json:"role"`
}

func (a *App) authenticatedTenantOwner(r *http.Request) (HostingAccount, bool) {
	username := a.Auth.UsernameForRequest(r)
	if username == "" || a.Auth.IsAdministrator(r) || a.Auth.IsAPITokenRequest(r) || a.Accounts == nil {
		return HostingAccount{}, false
	}
	account, ok := a.Accounts.Get(username)
	return account, ok && accountRole(account) == "owner" && accountTenantID(account) == username && !account.Suspended
}

func (a *App) accountMembers(w http.ResponseWriter, r *http.Request) {
	owner, ok := a.authenticatedTenantOwner(r)
	if !ok {
		http.Error(w, "tenant owner access required", http.StatusForbidden)
		return
	}
	memberName := strings.TrimPrefix(r.URL.Path, "/api/account/members/")
	memberName = safeUser(strings.Trim(memberName, "/"))
	if r.URL.Path != "/api/account/members" && memberName == "" {
		http.Error(w, "member username is required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		members, err := a.Accounts.ListMembers(owner.Username)
		if err != nil {
			http.Error(w, "unable to read tenant members", http.StatusInternalServerError)
			return
		}
		result := make([]HostingAccountSummary, 0, len(members))
		for _, member := range members {
			result = append(result, publicHostingAccount(member))
		}
		writeJSON(w, http.StatusOK, map[string]any{"members": result})
	case http.MethodPost:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		var req tenantMemberRequest
		if err := decodeJSON(w, r, 4096, &req); err != nil {
			return
		}
		req.Username = safeUser(req.Username)
		req.Role = strings.ToLower(strings.TrimSpace(req.Role))
		if req.Username == "" || req.Role == "" || len(req.Password) < 20 {
			http.Error(w, "username, role, and a password of at least 20 characters are required", http.StatusUnprocessableEntity)
			return
		}
		if _, err := decodeTOTPSecret(req.TOTPSecret); err != nil {
			http.Error(w, "invalid MFA secret", http.StatusUnprocessableEntity)
			return
		}
		member, err := a.Accounts.CreateMember(owner.Username, req.Username, req.Password, req.TOTPSecret, req.Role)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if err := AuditAs(a.Config.AuditLog, owner.Username, "tenant.member.created", member.Username, member.Role); err != nil {
			http.Error(w, "member created but audit persistence failed", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusCreated, publicHostingAccount(member))
	case http.MethodPatch, http.MethodDelete:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		member, exists := a.Accounts.Get(memberName)
		if !exists || accountRole(member) == "owner" || accountTenantID(member) != owner.Username {
			http.Error(w, "tenant member not found", http.StatusNotFound)
			return
		}
		if r.Method == http.MethodDelete {
			if a.APITokens != nil {
				if err := a.APITokens.revokeAll(memberName); err != nil {
					http.Error(w, "member API token revocation could not be persisted", http.StatusServiceUnavailable)
					return
				}
			}
			if err := a.Accounts.RemoveLogin(memberName); err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			if a.Auth.sessions != nil {
				_ = a.Auth.sessions.revokeUser(memberName)
			}
			if err := AuditAs(a.Config.AuditLog, owner.Username, "tenant.member.removed", memberName, "member identity removed"); err != nil {
				http.Error(w, "member removed but audit persistence failed", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var input struct {
			Suspended *bool  `json:"suspended"`
			Role      string `json:"role"`
		}
		if err := decodeJSON(w, r, 2048, &input); err != nil {
			return
		}
		if input.Suspended == nil && strings.TrimSpace(input.Role) == "" {
			http.Error(w, "suspended or role is required", http.StatusBadRequest)
			return
		}
		if input.Suspended != nil {
			if _, err := a.Accounts.SetSuspended(memberName, *input.Suspended); err != nil {
				http.Error(w, err.Error(), http.StatusUnprocessableEntity)
				return
			}
		}
		if strings.TrimSpace(input.Role) != "" {
			if _, err := a.Accounts.SetMemberRole(memberName, input.Role); err != nil {
				http.Error(w, err.Error(), http.StatusUnprocessableEntity)
				return
			}
		}
		updated, _ := a.Accounts.Get(memberName)
		if err := AuditAs(a.Config.AuditLog, owner.Username, "tenant.member.updated", memberName, fmt.Sprintf("role=%s suspended=%t", accountRole(updated), updated.Suspended)); err != nil {
			http.Error(w, "member updated but audit persistence failed", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, publicHostingAccount(updated))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// accountSuspend implements account suspension with audit logging
func (a *App) accountSuspend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !a.Auth.IsAdministrator(r) {
		http.Error(w, "unauthorized", http.StatusForbidden)
		return
	}

	if !a.Auth.CSRF(r) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	var req SuspensionRequest
	if err := decodeJSON(w, r, 2048, &req); err != nil {
		return
	}

	req.Username = safeUser(req.Username)
	if req.Username == "" {
		http.Error(w, "invalid account name", http.StatusUnprocessableEntity)
		return
	}

	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		http.Error(w, "suspension reason required", http.StatusBadRequest)
		return
	}
	operationCtx, releaseAccountLock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "account:"+req.Username)
	if lockErr != nil {
		http.Error(w, "account mutation is busy", http.StatusConflict)
		return
	}
	defer releaseAccountLock()

	account, ok := a.Accounts.Get(req.Username)
	if !ok {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}

	if account.Suspended {
		http.Error(w, "account already suspended", http.StatusConflict)
		return
	}

	// Perform suspension
	_, err := a.setAccountSuspended(operationCtx, req.Username, true)
	if err != nil {
		http.Error(w, "could not suspend account: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "account suspension cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}

	// Audit the suspension
	auditAction := "account.suspended"
	if !req.Permanent {
		auditAction = "account.suspension.temporary"
	}

	auditErr := AuditAs(a.Config.AuditLog, "admin", auditAction, req.Username, req.Reason)
	if auditErr != nil {
		log.Printf("suspension audit logging failed for %s: %v", req.Username, auditErr)
		http.Error(w, "account suspended but audit persistence failed", http.StatusServiceUnavailable)
		return
	}

	// Return confirmation
	result := map[string]any{
		"username":  req.Username,
		"suspended": true,
		"reason":    req.Reason,
		"timestamp": time.Now().UTC(),
	}
	writeJSON(w, http.StatusOK, result)
}

// accountUnsuspend lifts account suspension
func (a *App) accountUnsuspend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !a.Auth.IsAdministrator(r) {
		http.Error(w, "unauthorized", http.StatusForbidden)
		return
	}

	if !a.Auth.CSRF(r) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	var req struct {
		Username string `json:"username"`
		Reason   string `json:"reason,omitempty"`
	}

	if err := decodeJSON(w, r, 2048, &req); err != nil {
		return
	}

	req.Username = safeUser(req.Username)
	if req.Username == "" {
		http.Error(w, "invalid account name", http.StatusUnprocessableEntity)
		return
	}
	operationCtx, releaseAccountLock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "account:"+req.Username)
	if lockErr != nil {
		http.Error(w, "account mutation is busy", http.StatusConflict)
		return
	}
	defer releaseAccountLock()

	account, ok := a.Accounts.Get(req.Username)
	if !ok {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}

	if !account.Suspended {
		http.Error(w, "account is not suspended", http.StatusConflict)
		return
	}

	// Perform unsuspension
	_, err := a.setAccountSuspended(operationCtx, req.Username, false)
	if err != nil {
		http.Error(w, "could not unsuspend account: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "account unsuspension cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}

	// Audit the unsuspension
	auditErr := AuditAs(a.Config.AuditLog, "admin", "account.unsuspended", req.Username, req.Reason)
	if auditErr != nil {
		log.Printf("unsuspension audit logging failed for %s: %v", req.Username, auditErr)
		http.Error(w, "account unsuspended but audit persistence failed", http.StatusServiceUnavailable)
		return
	}

	result := map[string]any{
		"username":  req.Username,
		"suspended": false,
		"timestamp": time.Now().UTC(),
	}
	writeJSON(w, http.StatusOK, result)
}

// checkPlanLimits automatically checks all accounts for limit violations
// and logs warnings for operators to take action
func (a *App) checkPlanLimits() error {
	if a.Accounts == nil {
		return errors.New("accounts store not initialized")
	}

	accounts, err := a.Accounts.ListWithError()
	if err != nil {
		return fmt.Errorf("read account inventory: %w", err)
	}
	warningThreshold := 80
	criticalThreshold := 95

	for _, account := range accounts {
		plan, exists := hostingPlans[account.Plan]
		if !exists {
			continue
		}

		sitesUsed := len(account.Sites)
		sitesPercent := 0
		if plan.SiteLimit > 0 {
			sitesPercent = (sitesUsed * 100) / plan.SiteLimit
		}

		// Check site limit violations
		if sitesPercent >= criticalThreshold && !account.Suspended {
			// Auto-suspend if critical
			if plan.SiteLimit > 0 && sitesUsed >= plan.SiteLimit {
				if err := a.autoSuspendAccount(context.Background(), account.Username); err != nil {
					return fmt.Errorf("auto-suspend account %s for site limit: %w", account.Username, err)
				}
				recordAudit(a.Config.AuditLog, "system", "account.suspended.auto", account.Username,
					fmt.Sprintf("site limit exceeded: %d/%d", sitesUsed, plan.SiteLimit))
			}
		} else if sitesPercent >= warningThreshold {
			// Log warning for operator
			recordAudit(a.Config.AuditLog, "system", "account.usage.warning", account.Username,
				fmt.Sprintf("site usage at %d%% of limit (%d/%d)", sitesPercent, sitesUsed, plan.SiteLimit))
		}

		// Check database limit violations
		databasesUsed := 0
		for _, site := range account.Sites {
			databases, err := managedDatabasesForSite(a.Config, site)
			if err == nil {
				databasesUsed += len(databases)
			}
		}

		databasesPercent := 0
		if plan.DatabaseLimit > 0 {
			databasesPercent = (databasesUsed * 100) / plan.DatabaseLimit
		}

		if databasesPercent >= criticalThreshold && !account.Suspended {
			if plan.DatabaseLimit > 0 && databasesUsed >= plan.DatabaseLimit {
				if err := a.autoSuspendAccount(context.Background(), account.Username); err != nil {
					return fmt.Errorf("auto-suspend account %s for database limit: %w", account.Username, err)
				}
				recordAudit(a.Config.AuditLog, "system", "account.suspended.auto", account.Username,
					fmt.Sprintf("database limit exceeded: %d/%d", databasesUsed, plan.DatabaseLimit))
			}
		} else if databasesPercent >= warningThreshold {
			recordAudit(a.Config.AuditLog, "system", "account.usage.warning", account.Username,
				fmt.Sprintf("database usage at %d%% of limit (%d/%d)", databasesPercent, databasesUsed, plan.DatabaseLimit))
		}
	}

	return nil
}
