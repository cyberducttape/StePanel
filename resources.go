package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

type fpmStatusCacheEntry struct {
	status    rootbroker.FPMStatus
	expiresAt time.Time
}

const fpmStatusCacheTTL = 5 * time.Second

// Validation bounds: syntactically acceptable limits for resource values.
// These are upper limits for what the API accepts, not operational defaults.
const (
	maxValidCPUPercent = 6400       // Reject >6400% as obviously wrong input
	maxValidMemoryMB   = 1048576    // Reject >1TB as obviously wrong input
	maxValidTasksMax   = 100000     // Reject >100K tasks as obviously wrong input
	maxValidDiskMB     = 1048576    // Reject >1TB disk as obviously wrong input
	maxValidInodes     = 1000000000 // Reject >1B inodes as obviously wrong input
)

// Hosting policy defaults: what customers actually get on shared hosting.
// These are enforced per plan tier via HostingPlan in accounts.go.
// Examples:
//   - professional plan: 200% CPU, 1024 MB memory
//   - starter plan: 100% CPU, 512 MB memory (see accounts.go)
const (
	minCPUPercent = 25   // Minimum is 25% (0.25 cores)
	minMemoryMB   = 64   // Minimum is 64 MB
	minTasksMax   = 16   // Minimum is 16 concurrent tasks
	minPHPWorkers = 1    // Minimum is 1 PHP-FPM worker
	minDiskMB     = 64   // Minimum disk quota
	minInodes     = 1000 // Minimum inodes quota
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

func resourceMutationLockKeys(site, account string) []string {
	keys := []string{site}
	if strings.TrimSpace(account) != "" {
		keys = append(keys, "account:"+account)
	}
	return keys
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
	return a.ensurePlanResourcesContext(context.Background(), account)
}

// ensurePlanResourcesContext serializes both desired resource persistence and
// host enforcement with concurrent site mutations. Account creation already
// holds the account fence; this function acquires the site fences in the same
// order used by the other resource workflows.
func (a *App) ensurePlanResourcesContext(ctx context.Context, account HostingAccount) ([]string, error) {
	if a.Resources == nil {
		return nil, nil
	}
	if len(account.Sites) == 0 {
		// An account without assigned sites has no resource profiles to
		// persist or apply. Avoid turning an unrelated resource-store write
		// failure into an account-creation failure for this valid state.
		return nil, nil
	}
	plan, ok := hostingPlans[account.Plan]
	if !ok {
		return nil, errors.New("account plan is not available")
	}
	lockKeys := append([]string(nil), account.Sites...)
	operationCtx, release, lockErr := a.acquireSiteMutationLocksContext(ctx, lockKeys...)
	if lockErr != nil {
		return nil, fmt.Errorf("acquire account resource site locks: %w", lockErr)
	}
	defer release()
	if err := operationCtx.Err(); err != nil {
		return nil, err
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
	if err != nil && !errors.Is(err, errControlPlaneStateRefreshed) {
		for _, profile := range profiles {
			delete(a.Resources.values, profile.Site)
		}
	}
	a.Resources.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("persist plan resource profiles: %w", err)
	}
	pending := make([]string, 0, len(profiles))
	ctx, cancel := context.WithTimeout(operationCtx, 5*time.Minute)
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
		err = a.Resources.saveProfileLocked(profile)
		a.Resources.mu.Unlock()
		if err != nil {
			pending = append(pending, profile.Site)
		}
	}
	return pending, nil
}

// prepareProvisionedSiteResources persists and applies the customer's plan
// ceilings while the site is prepared but before its document tree is
// activated. The caller already owns the site mutation lock.
func (a *App) prepareProvisionedSiteResources(ctx context.Context, account HostingAccount, site string) (bool, error) {
	if a.Resources == nil {
		return false, errors.New("customer site creation requires resource enforcement state")
	}
	plan, ok := hostingPlans[account.Plan]
	if !ok {
		return false, errors.New("customer plan is unavailable for resource enforcement")
	}
	a.Resources.mu.Lock()
	profile, existed := a.Resources.values[site]
	if existed {
		if profile.Account != "" && profile.Account != account.Username {
			a.Resources.mu.Unlock()
			return false, errors.New("existing site resource profile belongs to another account")
		}
		profile = clampResourceProfileToPlan(profile, plan)
		profile.Account = account.Username
	} else {
		profile = resourceProfileForPlan(account.Username, site, plan)
	}
	profile.State = "pending"
	if profile.hasFilesystemQuota() {
		profile.FilesystemQuotaState = "apply-pending"
	}
	err := persistMapKeyChange(a.Resources.values, site, &profile, a.Resources.persistLocked)
	a.Resources.mu.Unlock()
	if err != nil {
		return !existed, fmt.Errorf("persist desired plan resources: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return !existed, err
	}
	if err := a.applyResourceProfile(ctx, profile, false); err != nil {
		return !existed, fmt.Errorf("apply plan resource ceilings before site activation: %w", err)
	}
	profile.State = "applied"
	profile.AppliedAt = time.Now().UTC()
	if profile.hasFilesystemQuota() {
		profile.FilesystemQuotaState = "enforced"
	} else {
		profile.FilesystemQuotaState = "none"
	}
	a.Resources.mu.Lock()
	err = persistMapKeyChange(a.Resources.values, site, &profile, a.Resources.persistLocked)
	a.Resources.mu.Unlock()
	if err != nil {
		return !existed, fmt.Errorf("persist applied plan resource state: %w", err)
	}
	return !existed, nil
}

func (a *App) removeProvisionedSiteResourceProfile(account, site string) error {
	if a.Resources == nil {
		return nil
	}
	a.Resources.mu.Lock()
	defer a.Resources.mu.Unlock()
	profile, exists := a.Resources.values[site]
	if !exists {
		return nil
	}
	if profile.Account != account {
		return errors.New("refusing to remove a resource profile owned by another account")
	}
	return persistMapKeyChange(a.Resources.values, site, (*ResourceProfile)(nil), a.Resources.persistLocked)
}

func (s *ResourceStore) removeRecoveredSiteProfile(site string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.values[site]; !exists {
		return nil
	}
	return persistMapKeyChange(s.values, site, (*ResourceProfile)(nil), s.persistLocked)
}

// reconcileAccountResourcePlan updates resource desired state after an account
// plan or assignment change. Existing stricter site settings are preserved;
// values above the new plan ceiling are clamped. Host changes happen only after
// the complete desired set is persisted, and failed applications remain
// pending for the normal reconciliation path.
func (a *App) reconcileAccountResourcePlan(ctx context.Context, previous, account HostingAccount) ([]string, error) {
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
		if !errors.Is(err, errControlPlaneStateRefreshed) {
			for _, site := range append(append([]string{}, previous.Sites...), account.Sites...) {
				if previousExists[site] {
					a.Resources.values[site] = previousValues[site]
				} else {
					delete(a.Resources.values, site)
				}
			}
		}
		a.Resources.mu.Unlock()
		return nil, fmt.Errorf("persist account resource plan: %w", err)
	}
	a.Resources.mu.Unlock()

	if len(changed) == 0 {
		return nil, a.applyAccountResourceEnvelopeContext(ctx, account.Username, plan)
	}
	operationCtx, unlock, lockErr := a.acquireSiteMutationLocksContext(ctx, account.Sites...)
	if lockErr != nil {
		return nil, fmt.Errorf("acquire account resource locks: %w", lockErr)
	}
	defer unlock()
	operationCtx, cancel := context.WithTimeout(operationCtx, 5*time.Minute)
	defer cancel()
	pending := make([]string, 0, len(changed))
	if err := a.applyAccountResourceEnvelopeContext(operationCtx, account.Username, plan); err != nil {
		for _, profile := range changed {
			pending = append(pending, profile.Site)
		}
		return pending, nil
	}
	for _, profile := range changed {
		if err := a.applyResourceProfile(operationCtx, profile, false); err != nil {
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
		if err := a.Resources.saveProfileLocked(profile); err != nil {
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

func (a *App) applyAccountResourceEnvelopeContext(ctx context.Context, account string, plan HostingPlan) error {
	// Account resource envelope is a config mutation (cgroup setup) - use standard timeout
	if a.Config.Production {
		_, err := runTypedResourceOperation(ctx, a.Config, rootbroker.ResourceRequest{Action: "apply-account", Account: account, CPUPercent: plan.CPUPercent, CPUWeight: 100, MemoryHighMB: plan.MemoryMB * 90 / 100, MemoryMB: plan.MemoryMB, IOWeight: 100, TasksMax: plan.TasksMax})
		return err
	}
	return runHelperCommandWithTimeout(ctx, a.Config, helperConfigMutationTimeout, a.Config.AppCtl, "account-resource-apply", account, strconv.Itoa(plan.CPUPercent), "100", strconv.Itoa(plan.MemoryMB*90/100), strconv.Itoa(plan.MemoryMB), "100", strconv.Itoa(plan.TasksMax))
}

func OpenResourceStore(path string) (*ResourceStore, error) {
	s := &ResourceStore{path: path, values: map[string]ResourceProfile{}}
	d, e := readStateFile(path, 4<<20)
	if errors.Is(e, os.ErrNotExist) {
		return s, nil
	}
	if errors.Is(e, errStateFileTooLarge) {
		return nil, errors.New("resource state exceeds 4 MiB")
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

// saveProfileLocked applies one profile transition with rollback-on-failure.
// The persistence helper also preserves a newer SQLite snapshot after a CAS
// conflict instead of restoring this process's stale prior value. Caller must
// hold s.mu.
func (s *ResourceStore) saveProfileLocked(profile ResourceProfile) error {
	next := profile
	return persistMapKeyChange(s.values, profile.Site, &next, s.persistLocked)
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
		var err error
		if a.Config.Production {
			_, err = runTypedResourceOperation(ctx, a.Config, rootbroker.ResourceRequest{Action: "apply-account", Account: p.Account, CPUPercent: p.CPUPercent, CPUWeight: p.CPUWeight, MemoryHighMB: p.MemoryHighMB, MemoryMB: p.MemoryMB, IOWeight: p.IOWeight, TasksMax: p.TasksMax})
		} else {
			err = runHelperCommandWithTimeout(ctx, a.Config, helperConfigMutationTimeout, a.Config.AppCtl, "account-resource-apply", p.Account, strconv.Itoa(p.CPUPercent), strconv.Itoa(p.CPUWeight), strconv.Itoa(p.MemoryHighMB), strconv.Itoa(p.MemoryMB), strconv.Itoa(p.IOWeight), strconv.Itoa(p.TasksMax))
		}
		if err != nil {
			return err
		}
	}
	var resourceErr error
	if a.Config.Production {
		_, resourceErr = runTypedResourceOperation(ctx, a.Config, rootbroker.ResourceRequest{Action: "apply-site", Site: p.Site, Account: p.Account, CPUPercent: p.CPUPercent, CPUWeight: p.CPUWeight, MemoryHighMB: p.MemoryHighMB, MemoryMB: p.MemoryMB, IOWeight: p.IOWeight, TasksMax: p.TasksMax})
	} else {
		resourceErr = runHelperCommandWithTimeout(ctx, a.Config, helperConfigMutationTimeout, a.Config.AppCtl, "resource-apply", p.Site, strconv.Itoa(p.CPUPercent), strconv.Itoa(p.CPUWeight), strconv.Itoa(p.MemoryHighMB), strconv.Itoa(p.MemoryMB), strconv.Itoa(p.IOWeight), strconv.Itoa(p.TasksMax), p.Account)
	}
	if resourceErr != nil {
		return resourceErr
	}
	// A site placed in a hosting account (ADR-0007) shares the account's Unix
	// user with the account's other sites, so its disk limit is the account's
	// user quota, never a per-site one: a per-site setquota would overwrite
	// the other sites' limit, and clearing it would lift the whole account's.
	location, err := siteLocationFor(a.Config, p.Site)
	if err != nil {
		return err
	}
	placed := location.Placed
	if a.Config.Production {
		if err := runTypedSiteMutation(ctx, a.Config, rootbroker.SiteRequest{Action: "resources", Site: p.Site, PHPWorkers: p.PHPWorkers}); err != nil {
			return err
		}
		if placed {
			return a.applyAccountFilesystemQuota(ctx, location.Placement.Account, p)
		}
		if p.hasFilesystemQuota() {
			return runTypedSiteMutation(ctx, a.Config, rootbroker.SiteRequest{Action: "quota", Site: p.Site, DiskMB: p.DiskMB, Inodes: p.Inodes})
		}
		if clearFilesystemQuota {
			return runTypedSiteMutation(ctx, a.Config, rootbroker.SiteRequest{Action: "quota-clear", Site: p.Site})
		}
		return nil
	}
	if err := runHelperCommandWithTimeout(ctx, a.Config, helperConfigMutationTimeout, a.Config.SiteCtl, "resources", p.Site, strconv.Itoa(p.PHPWorkers)); err != nil {
		return err
	}
	if placed {
		return a.applyAccountFilesystemQuota(ctx, location.Placement.Account, p)
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

// accountFilesystemQuota is the disk and inode limit of a hosting account:
// its customer tenant's plan, or, for an administrator-created account with
// no tenant, the limits of the site profile being applied. ok is false when
// the account has no filesystem limit.
func (a *App) accountFilesystemQuota(account string, p ResourceProfile) (diskMB, inodes int, ok bool) {
	if a.Accounts != nil {
		if tenant, found := a.Accounts.Get(account); found {
			if plan, known := hostingPlans[tenant.Plan]; known && (plan.DiskMB > 0 || plan.Inodes > 0) {
				return plan.DiskMB, plan.Inodes, true
			}
		}
	}
	return p.DiskMB, p.Inodes, p.hasFilesystemQuota()
}

// applyAccountFilesystemQuota enforces a hosting account's limit on its Unix
// user. It never clears the limit: other sites of the account rely on it, and
// the account user and its quota go away together with the account.
func (a *App) applyAccountFilesystemQuota(ctx context.Context, account string, p ResourceProfile) error {
	diskMB, inodes, ok := a.accountFilesystemQuota(account, p)
	if !ok {
		return nil
	}
	if _, err := accountHelperContext(ctx, a.Config, rootbroker.AccountRequest{Action: "quota", Account: account, DiskMB: diskMB, Inodes: inodes}); err != nil {
		return fmt.Errorf("apply hosting account %s filesystem quota: %w", account, err)
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
		if !a.requireCustomerScope(w, r, "site:read") {
			return
		}
		a.Resources.mu.RLock()
		p, ok := a.Resources.values[site]
		a.Resources.mu.RUnlock()
		observed := map[string]string{"state": "unknown"}
		if ok {
			observed = a.resourceObserved(r.Context(), site)
		}
		fpmStatus := a.siteFPMStatusSnapshot(r.Context(), site)
		writeJSON(w, 200, map[string]any{"configured": ok, "profile": p, "observed": observed, "fpm_status": fpmStatus, "enforcement": "systemd slice for managed apps/workers/scheduled tasks; isolated PHP-FPM max_children"})
		return
	}
	if (r.Method != http.MethodPut && r.Method != http.MethodPost) || !a.Auth.CSRF(r) || !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator CSRF request required", 403)
		return
	}
	var input struct {
		ResourceProfile
		PreviewToken string `json:"preview_token,omitempty"`
	}
	if e := decodeJSON(w, r, 4096, &input); e != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	p := input.ResourceProfile
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
	accountName := p.Account
	if accountName == "" && a.Accounts != nil {
		var ownershipErr error
		accountName, _, ownershipErr = a.Accounts.OwnerOfSiteWithError(site)
		if ownershipErr != nil {
			http.Error(w, "owning account state is unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLocksContext(r.Context(), resourceMutationLockKeys(site, accountName)...)
	if lockErr != nil {
		http.Error(w, "resource mutation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "resource mutation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	if r.Method == http.MethodPost {
		before := struct {
			Configured bool            `json:"configured"`
			Profile    ResourceProfile `json:"profile"`
		}{hadPrevious, previous}
		effects := a.resourceProfilePreviewEffects(r.Context(), previous, p)
		preview, err := a.makeChangePreview("site.resources:"+site, before, p, effects,
			[]string{"Restore the previous resource profile and reapply it through this same preview workflow.", "If host activation fails, desired state remains pending reconciliation; inspect systemd slice and filesystem quota state before retrying."})
		if err != nil {
			http.Error(w, "could not create resource profile preview", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, preview)
		return
	}
	if !a.verifyChangePreview(input.PreviewToken, "site.resources:"+site, struct {
		Configured bool            `json:"configured"`
		Profile    ResourceProfile `json:"profile"`
	}{hadPrevious, previous}, p) {
		http.Error(w, "resource profile changed or its preview expired; generate a new preview", http.StatusConflict)
		return
	}
	a.Resources.mu.Lock()
	e := a.Resources.saveProfileLocked(p)
	a.Resources.mu.Unlock()
	if e != nil {
		http.Error(w, "could not persist desired resource profile", 503)
		return
	}
	e = a.applyResourceProfile(operationCtx, p, p.FilesystemQuotaState == "clear-pending")
	if e != nil {
		http.Error(w, "resource profile is pending reconciliation", 502)
		return
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "resource profile is pending reconciliation", http.StatusConflict)
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
	e = a.Resources.saveProfileLocked(p)
	a.Resources.mu.Unlock()
	if e != nil {
		http.Error(w, "resource profile applied but state update failed", 503)
		return
	}
	TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "site.resources.applied", site, "cgroup/FPM profile")
	writeJSON(w, 202, p)
}

func (a *App) resourceProfilePreviewEffects(ctx context.Context, before, proposed ResourceProfile) []string {
	return resourceProfileEffects(before, proposed, a.siteFPMStatusSnapshot(ctx, proposed.Site))
}

func resourceProfileEffects(before, proposed ResourceProfile, status rootbroker.FPMStatus) []string {
	effects := []string{
		fmt.Sprintf("PHP-FPM max_children: %d → %d.", before.PHPWorkers, proposed.PHPWorkers),
		fmt.Sprintf("Site systemd memory.max: %d MiB → %d MiB; CPU quota: %d%% → %d%%; task limit: %d → %d.", before.MemoryMB, proposed.MemoryMB, before.CPUPercent, proposed.CPUPercent, before.TasksMax, proposed.TasksMax),
		fmt.Sprintf("Filesystem quota allowance: %d MiB → %d MiB and %d → %d inodes; quota is account-scoped where sites share an account.", before.DiskMB, proposed.DiskMB, before.Inodes, proposed.Inodes),
	}
	memoryObserved := false
	for _, pool := range status.Pools {
		if pool.State != "available" || pool.WorkerMemory == nil {
			continue
		}
		memoryObserved = true
		perWorker := pool.WorkerMemory.P95RSSBytes
		oldP95 := perWorker * uint64(before.PHPWorkers)
		newP95 := perWorker * uint64(proposed.PHPWorkers)
		newObservedMax := pool.WorkerMemory.MaxRSSBytes * uint64(proposed.PHPWorkers)
		effects = append(effects, fmt.Sprintf("PHP %s measured worker RSS across %d sampled process(es): P95 %s and largest observed worker %s. The estimated pool RSS changes from %s at %d workers to %s at %d workers (change %+d MiB) at measured P95; the proposed profile also projects %s using the largest observed worker. These instantaneous, shared-page-inclusive projections are planning estimates, not reservations or a sustained-load guarantee.",
			pool.PHPVersion, pool.WorkerMemory.SampledWorkers, formatPHPBytes(perWorker), formatPHPBytes(pool.WorkerMemory.MaxRSSBytes),
			formatPHPBytes(oldP95), before.PHPWorkers, formatPHPBytes(newP95), proposed.PHPWorkers,
			(int64(newP95)-int64(oldP95))/(1024*1024), formatPHPBytes(newObservedMax)))
	}
	if !memoryObserved {
		effects = append(effects, "PHP-FPM worker memory could not be measured; no memory-based worker sizing estimate is available. Collect representative measurements before increasing max_children.")
	}
	return effects
}

func (a *App) siteFPMStatusSnapshot(parent context.Context, site string) rootbroker.FPMStatus {
	a.fpmStatusMu.Lock()
	if cached, ok := a.fpmStatusCache[site]; ok && time.Now().Before(cached.expiresAt) {
		a.fpmStatusMu.Unlock()
		return cached.status
	}
	a.fpmStatusMu.Unlock()

	status := rootbroker.FPMStatus{Available: false, CollectedAt: time.Now().UTC(), Pools: []rootbroker.FPMStatusPool{}, Warnings: []string{}, Detail: "Live PHP-FPM status is available only through the installed root broker."}
	if a.Config.Production || labDirectRootBrokerEnabled() {
		ctx, cancel := context.WithTimeout(parent, 1800*time.Millisecond)
		defer cancel()
		client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", a.Config.WebRoot)
		if err == nil {
			status, err = client.SiteFPMStatus(ctx, site)
		}
		if err != nil {
			// Customers can read this snapshot; broker errors name host paths
			// and configuration, so they go to the log only.
			log.Printf("PHP-FPM status for site %s: %v", site, err)
			status = rootbroker.FPMStatus{Available: false, CollectedAt: time.Now().UTC(), Pools: []rootbroker.FPMStatusPool{}, Warnings: []string{}, Detail: "Live PHP-FPM status could not be collected; an administrator can find the cause in the panel log."}
		}
	}
	a.fpmStatusMu.Lock()
	if a.fpmStatusCache == nil {
		a.fpmStatusCache = make(map[string]fpmStatusCacheEntry)
	}
	if len(a.fpmStatusCache) >= 256 {
		var oldestSite string
		var oldestExpiry time.Time
		for cachedSite, entry := range a.fpmStatusCache {
			if !time.Now().Before(entry.expiresAt) {
				delete(a.fpmStatusCache, cachedSite)
				continue
			}
			if oldestSite == "" || entry.expiresAt.Before(oldestExpiry) {
				oldestSite, oldestExpiry = cachedSite, entry.expiresAt
			}
		}
		if len(a.fpmStatusCache) >= 256 && oldestSite != "" {
			delete(a.fpmStatusCache, oldestSite)
		}
	}
	a.fpmStatusCache[site] = fpmStatusCacheEntry{status: status, expiresAt: time.Now().Add(fpmStatusCacheTTL)}
	a.fpmStatusMu.Unlock()
	return status
}

func (a *App) resourceObserved(ctx context.Context, site string) map[string]string {
	var output string
	var err error
	if a.Config.Production {
		output, err = runTypedResourceOperation(ctx, a.Config, rootbroker.ResourceRequest{Action: "status", Site: site})
	} else {
		var raw []byte
		raw, err, _ = runAllowlistedHelperOutput(ctx, a.Config, nil, a.Config.AppCtl, "resource-status", site)
		output = string(raw)
	}
	if err != nil {
		return map[string]string{"state": "unavailable"}
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
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
	TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "resources.reconciled", "resources", strings.Join(reconciled, ","))
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
	for _, snapshot := range pending {
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLocksContext(ctx, resourceMutationLockKeys(snapshot.Site, snapshot.Account)...)
		if lockErr != nil {
			failed[snapshot.Site] = lockErr.Error()
			continue
		}
		// Re-read under the site lock. The snapshot predates it, and applying
		// and saving it would revert a profile an administrator saved in the
		// meantime.
		a.Resources.mu.RLock()
		p, exists := a.Resources.values[snapshot.Site]
		a.Resources.mu.RUnlock()
		if !exists {
			releaseUnlock()
			continue
		}
		err := a.applyResourceProfile(operationCtx, p, p.FilesystemQuotaState == "clear-pending")
		if err != nil {
			failed[p.Site] = "apply failed"
			if p.Account != "" && a.Accounts != nil {
				if _, suspendErr := a.setAccountSuspended(operationCtx, p.Account, true); suspendErr != nil {
					failed[p.Site] = "apply failed and account suspension failed: " + suspendErr.Error()
				}
			}
			releaseUnlock()
			continue
		}
		if err := operationCtx.Err(); err != nil {
			failed[p.Site] = "apply cancelled; reconciliation remains pending"
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
		err = a.Resources.saveProfileLocked(p)
		if err != nil {
			failed[p.Site] = "state persistence failed"
		}
		a.Resources.mu.Unlock()
		if err != nil {
			failed[p.Site] = "state persistence failed"
			releaseUnlock()
			continue
		}
		if err := operationCtx.Err(); err != nil {
			p.State = "pending"
			if p.hasFilesystemQuota() {
				p.FilesystemQuotaState = "apply-pending"
			} else {
				p.FilesystemQuotaState = "none"
			}
			if saveErr := func() error {
				a.Resources.mu.Lock()
				defer a.Resources.mu.Unlock()
				return a.Resources.saveProfileLocked(p)
			}(); saveErr != nil {
				failed[p.Site] = saveErr.Error()
			} else {
				failed[p.Site] = err.Error()
			}
			releaseUnlock()
			continue
		}
		reconciled = append(reconciled, p.Site)
		releaseUnlock()
	}
	return reconciled, failed
}
