package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Validation bounds: syntactically acceptable limits for resource values.
// These are upper limits for what the API accepts, not operational defaults.
const (
	maxValidCPUPercent  = 6400      // Reject >6400% as obviously wrong input
	maxValidMemoryMB    = 1048576   // Reject >1TB as obviously wrong input
	maxValidTasksMax    = 100000    // Reject >100K tasks as obviously wrong input
	maxValidDiskMB      = 1048576   // Reject >1TB disk as obviously wrong input
	maxValidInodes      = 1000000000 // Reject >1B inodes as obviously wrong input
)

// Hosting policy defaults: what customers actually get on shared hosting.
// These are enforced per plan tier via HostingPlan in accounts.go.
// Examples:
//   - professional plan: 200% CPU, 1024 MB memory
//   - starter plan: 100% CPU, 512 MB memory (see accounts.go)
const (
	minCPUPercent  = 25   // Minimum is 25% (0.25 cores)
	minMemoryMB    = 64   // Minimum is 64 MB
	minTasksMax    = 16   // Minimum is 16 concurrent tasks
	minPHPWorkers  = 1    // Minimum is 1 PHP-FPM worker
	minDiskMB      = 64   // Minimum disk quota
	minInodes      = 1000 // Minimum inodes quota
)

// ResourceProfile is enforced for managed systemd applications/workers through
// an optional aggregate account slice and a per-site child slice. PHP workers
// are separately applied to the isolated FPM pool. Filesystem, network and
// database limits require their own providers.
type ResourceProfile struct {
	Account              string    `json:"account,omitempty"`
	Site                 string    `json:"site"`
	CPUPercent           int       `json:"cpu_percent"`
	CPUWeight            int       `json:"cpu_weight,omitempty"`
	MemoryHighMB         int       `json:"memory_high_mb,omitempty"`
	MemoryMB             int       `json:"memory_mb"`
	IOWeight             int       `json:"io_weight,omitempty"`
	TasksMax             int       `json:"tasks_max"`
	PHPWorkers           int       `json:"php_workers"`
	DiskMB               int       `json:"disk_mb,omitempty"`
	Inodes               int       `json:"inodes,omitempty"`
	FilesystemQuotaState string    `json:"filesystem_quota_state,omitempty"`
	AppliedAt            time.Time `json:"applied_at,omitempty"`
	State                string    `json:"state"`
}
type ResourceStore struct {
	mu     sync.RWMutex
	path   string
	values map[string]ResourceProfile
}

func resourceProfileForPlan(account, site string, plan HostingPlan) ResourceProfile {
	high := plan.MemoryMB * 90 / 100
	if high < 64 {
		high = 64
	}
	return ResourceProfile{Account: account, Site: site, CPUPercent: plan.CPUPercent, CPUWeight: 100, MemoryHighMB: high, MemoryMB: plan.MemoryMB, IOWeight: 100, TasksMax: plan.TasksMax, PHPWorkers: plan.PHPWorkers, DiskMB: plan.DiskMB, Inodes: plan.Inodes, State: "pending", FilesystemQuotaState: "apply-pending"}
}

func clampResourceProfileToPlan(profile ResourceProfile, plan HostingPlan) ResourceProfile {
	profile.CPUPercent = minInt(profile.CPUPercent, plan.CPUPercent)
	profile.MemoryHighMB = minInt(profile.MemoryHighMB, plan.MemoryMB*90/100)
	profile.MemoryMB = minInt(profile.MemoryMB, plan.MemoryMB)
	profile.TasksMax = minInt(profile.TasksMax, plan.TasksMax)
	profile.PHPWorkers = minInt(profile.PHPWorkers, plan.PHPWorkers)
	if plan.DiskMB > 0 {
		profile.DiskMB = minInt(profile.DiskMB, plan.DiskMB)
	}
	if plan.Inodes > 0 {
		profile.Inodes = minInt(profile.Inodes, plan.Inodes)
	}
	if profile.MemoryHighMB > profile.MemoryMB {
		profile.MemoryHighMB = profile.MemoryMB
	}
	return profile
}

// ensurePlanResources persists desired resource profiles for newly assigned
// sites without overwriting an administrator's existing site-specific policy.
// Host application is deliberately separate so a helper failure leaves a
// durable pending profile for reconciliation.
func (a *App) ensurePlanResources(account HostingAccount) ([]string, error) {
	if a.Resources == nil {
		return nil, nil
	}
	plan, ok := hostingPlans[account.Plan]
	if !ok {
		return nil, errors.New("account plan is not available")
	}
	profiles := make([]ResourceProfile, 0, len(account.Sites))
	a.Resources.mu.Lock()
	for _, site := range account.Sites {
		if _, exists := a.Resources.values[site]; exists {
			continue
		}
		profile := resourceProfileForPlan(account.Username, site, plan)
		a.Resources.values[site] = profile
		profiles = append(profiles, profile)
	}
	err := a.Resources.persistLocked()
	if err != nil {
		for _, profile := range profiles {
			delete(a.Resources.values, profile.Site)
		}
	}
	a.Resources.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("persist plan resource profiles: %w", err)
	}
	pending := make([]string, 0, len(profiles))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, profile := range profiles {
		if err := a.applyResourceProfile(ctx, profile, false); err != nil {
			pending = append(pending, profile.Site)
			continue
		}
		profile.State = "applied"
		if profile.hasFilesystemQuota() {
			profile.FilesystemQuotaState = "enforced"
		} else {
			profile.FilesystemQuotaState = "none"
		}
		a.Resources.mu.Lock()
		a.Resources.values[profile.Site] = profile
		err = a.Resources.persistLocked()
		a.Resources.mu.Unlock()
		if err != nil {
			pending = append(pending, profile.Site)
		}
	}
	return pending, nil
}

// reconcileAccountResourcePlan updates resource desired state after an account
// plan or assignment change. Existing stricter site settings are preserved;
// values above the new plan ceiling are clamped. Host changes happen only after
// the complete desired set is persisted, and failed applications remain
// pending for the normal reconciliation path.
func (a *App) reconcileAccountResourcePlan(previous, account HostingAccount) ([]string, error) {
	if a.Resources == nil {
		return nil, nil
	}
	plan, ok := hostingPlans[account.Plan]
	if !ok {
		return nil, errors.New("account plan is not available")
	}
	assigned := make(map[string]bool, len(account.Sites))
	for _, site := range account.Sites {
		assigned[site] = true
	}
	changed := make([]ResourceProfile, 0, len(previous.Sites)+len(account.Sites))
	// Keep an in-memory rollback image until the desired state is durable. A
	// failed write must not leave the running process diverged from the state
	// that will be loaded after restart.
	previousValues := make(map[string]ResourceProfile, len(previous.Sites)+len(account.Sites))
	previousExists := make(map[string]bool, len(previous.Sites)+len(account.Sites))
	a.Resources.mu.Lock()
	for _, site := range previous.Sites {
		if profile, exists := a.Resources.values[site]; exists {
			previousValues[site] = profile
			previousExists[site] = true
		}
		if assigned[site] {
			continue
		}
		if profile, exists := a.Resources.values[site]; exists && profile.Account == previous.Username {
			profile.Account = ""
			profile.State = "pending"
			a.Resources.values[site] = profile
			changed = append(changed, profile)
		}
	}
	for _, site := range account.Sites {
		if _, captured := previousExists[site]; !captured {
			if profile, exists := a.Resources.values[site]; exists {
				previousValues[site] = profile
				previousExists[site] = true
			}
		}
		profile, exists := a.Resources.values[site]
		if !exists {
			profile = resourceProfileForPlan(account.Username, site, plan)
		} else {
			profile.Account = account.Username
			profile = clampResourceProfileToPlan(profile, plan)
			profile.State = "pending"
		}
		a.Resources.values[site] = profile
		changed = append(changed, profile)
	}
	if err := a.Resources.persistLocked(); err != nil {
		for _, site := range append(append([]string{}, previous.Sites...), account.Sites...) {
			if previousExists[site] {
				a.Resources.values[site] = previousValues[site]
			} else {
				delete(a.Resources.values, site)
			}
		}
		a.Resources.mu.Unlock()
		return nil, fmt.Errorf("persist account resource plan: %w", err)
	}
	a.Resources.mu.Unlock()

	if len(changed) == 0 {
		return nil, a.applyAccountResourceEnvelope(account.Username, plan)
	}
	unlock := a.siteOperations.AcquireMany(account.Sites...)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pending := make([]string, 0, len(changed))
	if err := a.applyAccountResourceEnvelope(account.Username, plan); err != nil {
		for _, profile := range changed {
			pending = append(pending, profile.Site)
		}
		return pending, nil
	}
	for _, profile := range changed {
		if err := a.applyResourceProfile(ctx, profile, false); err != nil {
			pending = append(pending, profile.Site)
			continue
		}
		profile.State = "applied"
		if profile.hasFilesystemQuota() {
			profile.FilesystemQuotaState = "enforced"
		} else {
			profile.FilesystemQuotaState = "none"
		}
		profile.AppliedAt = time.Now().UTC()
		a.Resources.mu.Lock()
		a.Resources.values[profile.Site] = profile
		if err := a.Resources.persistLocked(); err != nil {
			a.Resources.values[profile.Site] = func() ResourceProfile {
				profile.State = "pending"
				return profile
			}()
			pending = append(pending, profile.Site)
		}
		a.Resources.mu.Unlock()
	}
	return pending, nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func (a *App) applyAccountResourceEnvelope(account string, plan HostingPlan) error {
	// Account resource envelope is a config mutation (cgroup setup) - use standard timeout
	return runHelperCommandWithTimeout(context.Background(), a.Config, helperConfigMutationTimeout, a.Config.AppCtl, "account-resource-apply", account, strconv.Itoa(plan.CPUPercent), "100", strconv.Itoa(plan.MemoryMB*90/100), strconv.Itoa(plan.MemoryMB), "100", strconv.Itoa(plan.TasksMax))
}

func OpenResourceStore(path string) (*ResourceStore, error) {
	s := &ResourceStore{path: path, values: map[string]ResourceProfile{}}
	d, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return s, nil
	}
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(d, &s.values); e != nil {
		return nil, e
	}
	for site, profile := range s.values {
		profile = normalizeResourceProfile(profile)
		if profile.Site == "" {
			profile.Site = site
		}
		if profile.Site != site || !validResourceProfile(profile) {
			return nil, fmt.Errorf("invalid resource profile for site %q", site)
		}
		if profile.FilesystemQuotaState == "" {
			if profile.hasFilesystemQuota() {
				profile.FilesystemQuotaState = "enforced"
			} else {
				profile.FilesystemQuotaState = "none"
			}
		}
		if profile.FilesystemQuotaState != "none" && profile.FilesystemQuotaState != "enforced" && profile.FilesystemQuotaState != "apply-pending" && profile.FilesystemQuotaState != "clear-pending" {
			return nil, fmt.Errorf("invalid filesystem quota state for site %q", site)
		}
		s.values[site] = profile
	}
	return s, nil
}
func (s *ResourceStore) persistLocked() error {
	d, e := json.MarshalIndent(s.values, "", "  ")
	if e != nil {
		return e
	}
	if bound, err := persistBoundControlPlaneState(s, d); bound {
		return err
	}
	return writeAtomic(s.path, append(d, '\n'), 0600)
}
func normalizeResourceProfile(p ResourceProfile) ResourceProfile {
	if p.CPUWeight == 0 {
		p.CPUWeight = 100
	}
	if p.MemoryHighMB == 0 {
		p.MemoryHighMB = p.MemoryMB * 90 / 100
		if p.MemoryHighMB < 64 {
			p.MemoryHighMB = 64
		}
	}
	if p.IOWeight == 0 {
		p.IOWeight = 100
	}
	return p
}
func validResourceProfile(p ResourceProfile) bool {
	// Validate site name and optional account
	if safeUser(p.Site) == "" || (p.Account != "" && safeUser(p.Account) == "") {
		return false
	}

	// Validate CPU: 25% to 6400% (validation bound, not operational default)
	if p.CPUPercent < minCPUPercent || p.CPUPercent > maxValidCPUPercent {
		return false
	}

	// Validate CPU weight (for share ratio): 1 to 10000
	if p.CPUWeight < 1 || p.CPUWeight > 10000 {
		return false
	}

	// Validate memory: 64 MB to 1 TB (validation bound, not operational default)
	// MemoryHighMB must be between minMemoryMB and MemoryMB
	if p.MemoryMB < minMemoryMB || p.MemoryMB > maxValidMemoryMB {
		return false
	}
	if p.MemoryHighMB < minMemoryMB || p.MemoryHighMB > p.MemoryMB {
		return false
	}

	// Validate I/O weight: 1 to 10000
	if p.IOWeight < 1 || p.IOWeight > 10000 {
		return false
	}

	// Validate tasks max: 16 to 100,000 (validation bound, not operational default)
	if p.TasksMax < minTasksMax || p.TasksMax > maxValidTasksMax {
		return false
	}

	// Validate PHP workers: 1 to 512
	if p.PHPWorkers < minPHPWorkers || p.PHPWorkers > 512 {
		return false
	}

	// Validate disk quota (optional, 0 means no quota)
	if p.DiskMB == 0 && p.Inodes == 0 {
		return true // No disk quota configured
	}
	if p.DiskMB < minDiskMB || p.DiskMB > maxValidDiskMB {
		return false
	}
	if p.Inodes < minInodes || p.Inodes > maxValidInodes {
		return false
	}

	return true
}

func (p ResourceProfile) hasFilesystemQuota() bool { return p.DiskMB > 0 || p.Inodes > 0 }

func (a *App) applyResourceProfile(ctx context.Context, p ResourceProfile, clearFilesystemQuota bool) error {
	if p.Account != "" {
		if err := runHelperCommandWithTimeout(ctx, a.Config, helperConfigMutationTimeout, a.Config.AppCtl, "account-resource-apply", p.Account, strconv.Itoa(p.CPUPercent), strconv.Itoa(p.CPUWeight), strconv.Itoa(p.MemoryHighMB), strconv.Itoa(p.MemoryMB), strconv.Itoa(p.IOWeight), strconv.Itoa(p.TasksMax)); err != nil {
			return err
		}
	}
	if err := runHelperCommandWithTimeout(ctx, a.Config, helperConfigMutationTimeout, a.Config.AppCtl, "resource-apply", p.Site, strconv.Itoa(p.CPUPercent), strconv.Itoa(p.CPUWeight), strconv.Itoa(p.MemoryHighMB), strconv.Itoa(p.MemoryMB), strconv.Itoa(p.IOWeight), strconv.Itoa(p.TasksMax), p.Account); err != nil {
		return err
	}
	if err := runHelperCommandWithTimeout(ctx, a.Config, helperConfigMutationTimeout, a.Config.SiteCtl, "resources", p.Site, strconv.Itoa(p.PHPWorkers)); err != nil {
		return err
	}
	if p.hasFilesystemQuota() {
		if err := runHelperCommandWithTimeout(ctx, a.Config, helperConfigMutationTimeout, a.Config.SiteCtl, "quota", p.Site, strconv.Itoa(p.DiskMB), strconv.Itoa(p.Inodes)); err != nil {
			return err
		}
	} else if clearFilesystemQuota {
		if err := runHelperCommandWithTimeout(ctx, a.Config, helperConfigMutationTimeout, a.Config.SiteCtl, "quota-clear", p.Site); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) siteResources(w http.ResponseWriter, r *http.Request) {
	site := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sites/resources/"), "/")
	if safeUser(site) == "" || strings.Contains(site, "/") {
		http.Error(w, "invalid or inaccessible site", 403)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, site, "invalid or inaccessible site", 403); !ok {
		return
	}
	if a.Resources == nil {
		http.Error(w, "resource state unavailable", 503)
		return
	}
	if r.Method == http.MethodGet {
		a.Resources.mu.RLock()
		p, ok := a.Resources.values[site]
		a.Resources.mu.RUnlock()
		observed := map[string]string{"state": "unknown"}
		if ok {
			observed = a.resourceObserved(r.Context(), site)
		}
		writeJSON(w, 200, map[string]any{"configured": ok, "profile": p, "observed": observed, "enforcement": "systemd slice for managed apps/workers/scheduled tasks; isolated PHP-FPM max_children"})
		return
	}
	if r.Method != http.MethodPut || !a.Auth.CSRF(r) || !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator CSRF request required", 403)
		return
	}
	var p ResourceProfile
	if e := decodeJSON(w, r, 4096, &p); e != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	p.Site = site
	a.Resources.mu.RLock()
	previous, hadPrevious := a.Resources.values[site]
	a.Resources.mu.RUnlock()
	p.Account = ""
	if hadPrevious {
		p.Account = previous.Account
	}
	p = normalizeResourceProfile(p)
	if p.Account != "" {
		if a.Accounts == nil {
			http.Error(w, "owning account state is unavailable", http.StatusServiceUnavailable)
			return
		}
		account, exists := a.Accounts.Get(p.Account)
		if !exists {
			http.Error(w, "owning account is unavailable", http.StatusConflict)
			return
		}
		plan, exists := hostingPlans[account.Plan]
		if !exists {
			http.Error(w, "owning account plan is unavailable", http.StatusConflict)
			return
		}
		p = clampResourceProfileToPlan(p, plan)
	}
	p.State = "pending"
	p.FilesystemQuotaState = "none"
	if p.hasFilesystemQuota() {
		p.FilesystemQuotaState = "apply-pending"
	} else if hadPrevious && previous.FilesystemQuotaState != "none" && previous.FilesystemQuotaState != "" {
		p.FilesystemQuotaState = "clear-pending"
	}
	if !validResourceProfile(p) {
		http.Error(w, "invalid resource profile", 422)
		return
	}
	releaseUnlock := a.siteOperations.Acquire(site)
	defer releaseUnlock()
	a.Resources.mu.Lock()
	a.Resources.values[site] = p
	e := a.Resources.persistLocked()
	if e != nil {
		if hadPrevious {
			a.Resources.values[site] = previous
		} else {
			delete(a.Resources.values, site)
		}
	}
	a.Resources.mu.Unlock()
	if e != nil {
		http.Error(w, "could not persist desired resource profile", 503)
		return
	}
	e = a.applyResourceProfile(r.Context(), p, p.FilesystemQuotaState == "clear-pending")
	if e != nil {
		http.Error(w, "resource profile is pending reconciliation", 502)
		return
	}
	p.State = "applied"
	if p.hasFilesystemQuota() {
		p.FilesystemQuotaState = "enforced"
	} else {
		p.FilesystemQuotaState = "none"
	}
	p.AppliedAt = time.Now().UTC()
	a.Resources.mu.Lock()
	a.Resources.values[site] = p
	e = a.Resources.persistLocked()
	if e != nil {
		// The pending desired profile was already durable before host
		// mutation. Preserve that state in memory so reconciliation can retry
		// the observation update after a transient persistence failure.
		p.State = "pending"
		p.FilesystemQuotaState = func() string {
			if p.hasFilesystemQuota() {
				return "apply-pending"
			}
			return "none"
		}()
		a.Resources.values[site] = p
	}
	a.Resources.mu.Unlock()
	if e != nil {
		http.Error(w, "resource profile applied but state update failed", 503)
		return
	}
	_ = ShouldAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.resources.applied", site, "cgroup/FPM profile")
	writeJSON(w, 202, p)
}

func (a *App) resourceObserved(ctx context.Context, site string) map[string]string {
	output, err := runBoundedCommand(ctx, helperCommandContext(ctx, a.Config, a.Config.AppCtl, "resource-status", site))
	if err != nil {
		return map[string]string{"state": "unavailable"}
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}
	if values["ActiveState"] == "" {
		values["state"] = "unknown"
	} else {
		values["state"] = values["ActiveState"]
	}
	return values
}

func (a *App) reconcileResources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) || !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator CSRF request required", 403)
		return
	}
	if a.Resources == nil {
		http.Error(w, "resource state unavailable", 503)
		return
	}
	reconciled, failed := a.reconcileResourceProfiles(r.Context())
	_ = ShouldAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "resources.reconciled", "resources", strings.Join(reconciled, ","))
	writeJSON(w, 200, map[string]any{"reconciled": reconciled, "failed": failed})
}

// reconcileResourceProfiles reapplies every profile whose desired state is
// pending or whose site slice is no longer active. It is used both by the
// administrator endpoint and during startup so a reboot cannot silently
// remove cgroup/PHP enforcement.
func (a *App) reconcileResourceProfiles(ctx context.Context) (reconciled []string, failed map[string]string) {
	failed = map[string]string{}
	if a.Resources == nil {
		return nil, failed
	}
	a.Resources.mu.RLock()
	profiles := make([]ResourceProfile, 0, len(a.Resources.values))
	for _, p := range a.Resources.values {
		profiles = append(profiles, p)
	}
	a.Resources.mu.RUnlock()

	pending := make([]ResourceProfile, 0, len(profiles))
	for _, p := range profiles {
		if p.State != "applied" || p.FilesystemQuotaState == "apply-pending" || p.FilesystemQuotaState == "clear-pending" || a.resourceObserved(ctx, p.Site)["state"] != "active" {
			pending = append(pending, p)
		}
	}
	for _, p := range pending {
		releaseUnlock := a.siteOperations.Acquire(p.Site)
		err := a.applyResourceProfile(ctx, p, p.FilesystemQuotaState == "clear-pending")
		if err != nil {
			failed[p.Site] = "apply failed"
			if p.Account != "" && a.Accounts != nil {
				if _, suspendErr := a.Accounts.SetSuspended(p.Account, true); suspendErr != nil {
					failed[p.Site] = "apply failed and account suspension failed: " + suspendErr.Error()
				}
			}
			releaseUnlock()
			continue
		}
		p.State = "applied"
		if p.hasFilesystemQuota() {
			p.FilesystemQuotaState = "enforced"
		} else {
			p.FilesystemQuotaState = "none"
		}
		p.AppliedAt = time.Now().UTC()
		a.Resources.mu.Lock()
		a.Resources.values[p.Site] = p
		err = a.Resources.persistLocked()
		if err != nil {
			p.State = "pending"
			if p.hasFilesystemQuota() {
				p.FilesystemQuotaState = "apply-pending"
			} else {
				p.FilesystemQuotaState = "none"
			}
			a.Resources.values[p.Site] = p
		}
		a.Resources.mu.Unlock()
		if err != nil {
			failed[p.Site] = "state persistence failed"
			releaseUnlock()
			continue
		}
		reconciled = append(reconciled, p.Site)
		releaseUnlock()
	}
	return reconciled, failed
}
