package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

type PHPProfile struct {
	Site              string `json:"site"`
	Version           string `json:"version"`
	MemoryLimit       string `json:"memory_limit"`
	MaxExecutionTime  int    `json:"max_execution_time"`
	UploadMaxFilesize string `json:"upload_max_filesize"`
	PostMaxSize       string `json:"post_max_size"`
	MaxInputVars      int    `json:"max_input_vars"`
	OPcache           bool   `json:"opcache"`
	DisplayErrors     bool   `json:"display_errors"`
	ErrorReporting    string `json:"error_reporting"`
	PreviewToken      string `json:"preview_token,omitempty"`
	State             string `json:"state,omitempty"`
	LastError         string `json:"last_error,omitempty"`
}
type PHPProfileStore struct {
	mu     sync.RWMutex
	path   string
	values map[string]PHPProfile
}

var phpSizePattern = regexp.MustCompile(`^[1-9][0-9]{0,4}M$`)
var phpVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
var phpErrorReportingPattern = regexp.MustCompile(`^[A-Z0-9_~& |]{1,80}$`)

func OpenPHPProfileStore(path string) (*PHPProfileStore, error) {
	s := &PHPProfileStore{path: path, values: map[string]PHPProfile{}}
	d, e := readStateFile(path, 4<<20)
	if errors.Is(e, os.ErrNotExist) {
		return s, nil
	}
	if errors.Is(e, errStateFileTooLarge) {
		return nil, errors.New("PHP profile state exceeds 4 MiB")
	}
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(d, &s.values); e != nil {
		return nil, e
	}
	for site, profile := range s.values {
		if profile.State == "" {
			profile.State = "applied"
		}
		s.values[site] = profile
	}
	return s, nil
}
func (s *PHPProfileStore) save(access SiteCapability, p PHPProfile) error {
	return s.saveLocked(access.Site(), p)
}

// saveLocked is an internal method for background reconciliation loops that
// operate on already-persisted desired state. This is intentionally excluded
// from the capability model since reconciliation has no request/job context.
func (s *PHPProfileStore) saveLocked(site string, p PHPProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.values[site]
	s.values[site] = p
	d, e := json.MarshalIndent(s.values, "", "  ")
	if e != nil {
		return e
	}
	if bound, err := persistBoundControlPlaneState(s, d); bound {
		if err != nil {
			if existed {
				s.values[site] = previous
			} else {
				delete(s.values, site)
			}
		}
		return err
	}
	if err := writeAtomic(s.path, append(d, '\n'), 0600); err != nil {
		if existed {
			s.values[site] = previous
		} else {
			delete(s.values, site)
		}
		return err
	}
	return nil
}
func (s *PHPProfileStore) get(access SiteCapability) (PHPProfile, bool) {
	site := access.Site()
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.values[site]
	return p, ok
}
func installedPHPVersions() []string {
	entries, _ := filepath.Glob("/etc/php/*/fpm/pool.d")
	out := []string{}
	for _, entry := range entries {
		v := filepath.Base(filepath.Dir(filepath.Dir(entry)))
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func (a *App) phpRuntime(w http.ResponseWriter, r *http.Request) {
	site := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sites/php/"), "/")
	if site == "" || strings.Contains(site, "/") || safeUser(site) == "" {
		http.Error(w, "invalid site", 422)
		return
	}
	access, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", 403)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		if !a.requireCustomerScope(w, r, "site:read") {
			return
		}
		p, ok := a.PHP.get(access)
		writeJSON(w, 200, map[string]any{"site": site, "profile": p, "configured": ok, "versions": installedPHPVersions(), "extensions": []string{"curl", "gd", "intl", "mbstring", "mysqli", "opcache", "zip"}})
		return
	}
	if (r.Method != http.MethodPut && r.Method != http.MethodPost) || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	if !a.requireCustomerScope(w, r, "site:deploy") {
		return
	}
	var p PHPProfile
	if e := decodeJSON(w, r, 4096, &p); e != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	p.Site = site
	p.Version = strings.TrimPrefix(p.Version, "v")
	if !phpVersionPattern.MatchString(p.Version) || !phpSizePattern.MatchString(p.MemoryLimit) || !phpSizePattern.MatchString(p.UploadMaxFilesize) || !phpSizePattern.MatchString(p.PostMaxSize) || p.MaxExecutionTime < 1 || p.MaxExecutionTime > 3600 || p.MaxInputVars < 1 || p.MaxInputVars > 1000000 || !phpErrorReportingPattern.MatchString(p.ErrorReporting) {
		http.Error(w, "invalid PHP profile", 422)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), site)
	if lockErr != nil {
		http.Error(w, "PHP profile mutation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "PHP profile mutation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	current, configured := a.PHP.get(access)
	before := struct {
		Configured bool       `json:"configured"`
		Profile    PHPProfile `json:"profile"`
	}{configured, current}
	proposed := p
	proposed.PreviewToken = ""
	if r.Method == http.MethodPost {
		impact := a.phpProfileResourceImpact(site, current, proposed)
		preview, err := a.makeChangePreview("php.runtime:"+site, before, proposed,
			append([]string{"Update the site's PHP-FPM pool configuration.", "Restart or reload the site's PHP-FPM service; requests may briefly fail during activation."}, impact...),
			[]string{"Restore the previous PHP runtime profile shown in this preview and reapply it.", "If activation fails, StePanel retains the desired profile as pending reconciliation; inspect the PHP-FPM pool and service before retrying."})
		if err != nil {
			http.Error(w, "could not create PHP change preview", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, preview)
		return
	}
	if err := changePreviewRequired(p.PreviewToken); err != nil || !a.verifyChangePreview(p.PreviewToken, "php.runtime:"+site, before, proposed) {
		http.Error(w, "PHP profile changed or its preview expired; generate a new preview", http.StatusConflict)
		return
	}
	p.PreviewToken = ""
	p.State, p.LastError = "pending", ""
	if e := a.PHP.save(access, p); e != nil {
		http.Error(w, "could not persist desired PHP profile", 503)
		return
	}
	if e := a.applyPHPProfile(operationCtx, p); e != nil {
		p.State, p.LastError = "pending", e.Error()
		if saveErr := a.SavePHPProfileState(access, p); saveErr != nil {
			http.Error(w, "PHP runtime profile failed and pending state could not be persisted", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "PHP runtime profile is pending reconciliation", 502)
		return
	}
	if err := operationCtx.Err(); err != nil {
		p.State, p.LastError = "pending", err.Error()
		if saveErr := a.SavePHPProfileState(access, p); saveErr != nil {
			http.Error(w, "PHP runtime profile was cancelled and pending state could not be persisted", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "PHP runtime profile is pending reconciliation", http.StatusConflict)
		return
	}
	p.State, p.LastError = "applied", ""
	if e := a.PHP.save(access, p); e != nil {
		http.Error(w, "PHP profile applied but state update is pending", 503)
		return
	}
	TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "site.php.updated", site, p.Version)
	writeJSON(w, 202, p)
}

// phpProfileResourceImpact turns the per-request PHP memory limit into an
// explicit upper-bound estimate for the site's configured worker count. It
// supplements (and never substitutes for) measured PHP-FPM worker RSS.
func (a *App) phpProfileResourceImpact(site string, before, proposed PHPProfile) []string {
	workers := 1
	if a.Resources != nil {
		a.Resources.mu.RLock()
		if profile, ok := a.Resources.values[site]; ok && profile.PHPWorkers > 0 {
			workers = profile.PHPWorkers
		}
		a.Resources.mu.RUnlock()
	}
	oldMB, oldOK := parsePHPSizeMB(before.MemoryLimit)
	newMB, newOK := parsePHPSizeMB(proposed.MemoryLimit)
	if !newOK {
		return []string{"PHP memory impact is unavailable because the proposed memory limit could not be parsed."}
	}
	effect := fmt.Sprintf("PHP request memory ceiling: %d workers × %d MiB = %d MiB theoretical per-site PHP allocation; this is a configuration ceiling, not reserved or measured RSS.", workers, newMB, workers*newMB)
	if oldOK {
		effect += fmt.Sprintf(" Previous ceiling was %d MiB total; proposed change is %+d MiB at the same worker count.", workers*oldMB, workers*(newMB-oldMB))
	}
	status := a.siteFPMStatusSnapshot(context.Background(), site)
	for _, pool := range status.Pools {
		if pool.State != "available" || pool.WorkerMemory == nil {
			continue
		}
		effect += fmt.Sprintf(" PHP %s instantaneous worker RSS sample: %d worker(s), P95 %s, largest observed %s; configured max_children %d projects to about %s at P95 and %s using the largest observed worker.",
			pool.PHPVersion, pool.WorkerMemory.SampledWorkers, formatPHPBytes(pool.WorkerMemory.P95RSSBytes),
			formatPHPBytes(pool.WorkerMemory.MaxRSSBytes), pool.MaxChildren,
			formatPHPBytes(pool.WorkerMemory.ProjectedRSSAtMaxChildrenBytes),
			formatPHPBytes(pool.WorkerMemory.ProjectedMaxRSSAtMaxChildrenBytes))
	}
	effect += " RSS includes shared mappings and one snapshot is not a sustained-load forecast; compare repeated peak-period samples with host headroom before increasing worker limits."
	return []string{effect}
}

func parsePHPSizeMB(value string) (int, bool) {
	if !phpSizePattern.MatchString(value) {
		return 0, false
	}
	parsed, err := strconv.Atoi(strings.TrimSuffix(value, "M"))
	return parsed, err == nil
}

func formatPHPBytes(value uint64) string {
	return fmt.Sprintf("%.1f MiB", float64(value)/(1024*1024))
}

func (a *App) applyPHPProfile(ctx context.Context, p PHPProfile) error {
	if a.Config.Production {
		return runTypedSiteMutation(ctx, a.Config, rootbroker.SiteRequest{
			Action:            "runtime",
			Site:              p.Site,
			PHPVersion:        p.Version,
			MemoryLimit:       p.MemoryLimit,
			ExecTimeout:       p.MaxExecutionTime,
			UploadMaxFilesize: p.UploadMaxFilesize,
			PostMaxSize:       p.PostMaxSize,
			MaxInputVars:      p.MaxInputVars,
			OPcache:           p.OPcache,
			DisplayErrors:     p.DisplayErrors,
			ErrorReporting:    p.ErrorReporting,
		})
	}
	return runHelperCommandWithTimeout(ctx, a.Config, helperConfigMutationTimeout, a.Config.SiteCtl, "runtime", p.Site, p.Version, p.MemoryLimit, itoa(p.MaxExecutionTime), p.UploadMaxFilesize, p.PostMaxSize, itoa(p.MaxInputVars), boolString(p.OPcache), boolString(p.DisplayErrors), p.ErrorReporting)
}

func (a *App) reconcilePHPProfiles(ctx context.Context) (reconciled []string, failed map[string]string) {
	failed = map[string]string{}
	if a.PHP == nil {
		return nil, failed
	}
	a.PHP.mu.RLock()
	pending := make([]PHPProfile, 0)
	for _, profile := range a.PHP.values {
		if profile.State == "pending" {
			pending = append(pending, profile)
		}
	}
	a.PHP.mu.RUnlock()
	for _, snapshot := range pending {
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(ctx, snapshot.Site)
		if lockErr != nil {
			failed[snapshot.Site] = lockErr.Error()
			continue
		}
		// Re-read under the site lock so a profile saved after the snapshot
		// is not reverted.
		a.PHP.mu.RLock()
		profile, exists := a.PHP.values[snapshot.Site]
		a.PHP.mu.RUnlock()
		if !exists || profile.State != "pending" {
			releaseUnlock()
			continue
		}
		if err := a.applyPHPProfile(operationCtx, profile); err != nil {
			profile.LastError = err.Error()
			a.SavePHPProfileStateLocked(profile.Site, profile)
			failed[profile.Site] = err.Error()
			releaseUnlock()
			continue
		}
		if err := operationCtx.Err(); err != nil {
			profile.LastError = err.Error()
			a.SavePHPProfileStateLocked(profile.Site, profile)
			failed[profile.Site] = err.Error()
			releaseUnlock()
			continue
		}
		profile.State, profile.LastError = "applied", ""
		if err := a.PHP.saveLocked(profile.Site, profile); err != nil {
			failed[profile.Site] = err.Error()
			releaseUnlock()
			continue
		}
		if err := operationCtx.Err(); err != nil {
			profile.State, profile.LastError = "pending", err.Error()
			if saveErr := a.SavePHPProfileStateLocked(profile.Site, profile); saveErr != nil {
				failed[profile.Site] = saveErr.Error()
			} else {
				failed[profile.Site] = err.Error()
			}
			releaseUnlock()
			continue
		}
		reconciled = append(reconciled, profile.Site)
		releaseUnlock()
	}
	return reconciled, failed
}
func itoa(v int) string { return fmt.Sprintf("%d", v) }
