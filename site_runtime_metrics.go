package stepanel

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

const (
	siteRuntimeMetricsInterval = time.Minute
	siteRuntimeMetricsTimeout  = 4 * time.Second
	siteRuntimeMetricsWorkers  = 16
	siteRuntimeMetricsMaxSites = 5000
)

type siteRuntimeMetricsStore struct {
	mu            sync.RWMutex
	samples       map[string]siteRuntimeMetricSample
	inventoryUp   bool
	inventorySize int
	updatedAt     time.Time
}

type siteRuntimeMetricSample struct {
	site               string
	collectedAt        time.Time
	fpmConfigured      bool
	fpmAvailable       bool
	pools              []rootbroker.FPMStatusPool
	resourceConfigured bool
	resourceAvailable  bool
	memoryCurrent      uint64
	memoryMax          uint64
	tasksCurrent       uint64
	tasksMax           uint64
	cpuUsageNSec       uint64
	cpuQuotaPerSecUSec uint64
	cpuUtilizationPct  float64
	cpuUtilizationUp   bool
}

func (s *ResourceStore) profilesSnapshot() []ResourceProfile {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	profiles := make([]ResourceProfile, 0, len(s.values))
	for _, profile := range s.values {
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Site < profiles[j].Site })
	return profiles
}

func (a *App) runSiteRuntimeMetricsLoop(ctx context.Context) {
	ticker := time.NewTicker(siteRuntimeMetricsInterval)
	defer ticker.Stop()
	a.refreshSiteRuntimeMetrics(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.refreshSiteRuntimeMetrics(ctx)
		}
	}
}

// refreshSiteRuntimeMetrics performs privileged probes off the scrape path.
// Its fixed worker pool and inventory cap keep monitoring overhead bounded;
// timestamps and inventory health make incomplete samples visible to alerts.
func (a *App) refreshSiteRuntimeMetrics(parent context.Context) {
	names, profiles, inventoryUp := a.siteRuntimeInventory()
	if len(names) > siteRuntimeMetricsMaxSites {
		names = names[:siteRuntimeMetricsMaxSites]
		inventoryUp = false
	}
	samples := make(map[string]siteRuntimeMetricSample, len(names))
	for _, site := range names {
		_, hasProfile := profiles[site]
		samples[site] = siteRuntimeMetricSample{site: site, fpmConfigured: hasProfile, resourceConfigured: hasProfile}
	}
	work := make(chan string)
	var workers sync.WaitGroup
	var samplesMu sync.Mutex
	for i := 0; i < siteRuntimeMetricsWorkers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for site := range work {
				if parent.Err() != nil {
					return
				}
				fpmCtx, cancelFPM := context.WithTimeout(parent, siteRuntimeMetricsTimeout)
				fpm, fpmErr := a.collectSiteFPMMetrics(fpmCtx, site)
				cancelFPM()
				resourceCtx, cancelResource := context.WithTimeout(parent, siteRuntimeMetricsTimeout)
				observed := a.resourceObserved(resourceCtx, site)
				cancelResource()
				samplesMu.Lock()
				sample := samples[site]
				sample.collectedAt = time.Now().UTC()
				if fpmErr == nil {
					sample.fpmConfigured = sample.fpmConfigured || len(fpm.Pools) > 0
					sample.fpmAvailable = fpm.Available
					sample.pools = append([]rootbroker.FPMStatusPool(nil), fpm.Pools...)
				}
				applyResourceObservation(&sample, observed)
				samples[site] = sample
				samplesMu.Unlock()
			}
		}()
	}
sendSites:
	for _, site := range names {
		select {
		case <-parent.Done():
			break sendSites
		case work <- site:
		}
	}
	close(work)
	workers.Wait()
	if parent.Err() != nil {
		return
	}
	if a.siteRuntimeMetrics == nil {
		return
	}
	a.siteRuntimeMetrics.mu.Lock()
	// CPUUsageNSec is cumulative. Derive a bounded interval utilization from
	// two successful samples; never infer a rate from a single counter value.
	for site, sample := range samples {
		previous, ok := a.siteRuntimeMetrics.samples[site]
		if ok {
			sample.cpuUtilizationPct, sample.cpuUtilizationUp = cpuQuotaUtilization(previous, sample)
		}
		samples[site] = sample
	}
	a.siteRuntimeMetrics.samples = samples
	a.siteRuntimeMetrics.inventoryUp = inventoryUp
	a.siteRuntimeMetrics.inventorySize = len(names)
	a.siteRuntimeMetrics.updatedAt = time.Now().UTC()
	a.siteRuntimeMetrics.mu.Unlock()
}

func cpuQuotaUtilization(previous, current siteRuntimeMetricSample) (float64, bool) {
	if !previous.resourceAvailable || !current.resourceAvailable ||
		previous.cpuQuotaPerSecUSec == 0 || current.cpuQuotaPerSecUSec == 0 ||
		previous.cpuQuotaPerSecUSec != current.cpuQuotaPerSecUSec ||
		current.cpuUsageNSec < previous.cpuUsageNSec ||
		!current.collectedAt.After(previous.collectedAt) {
		return 0, false
	}
	elapsed := current.collectedAt.Sub(previous.collectedAt)
	if elapsed < 10*time.Second || elapsed > 5*siteRuntimeMetricsInterval {
		return 0, false
	}
	// CPUQuotaPerSecUSec is allowed CPU time in microseconds per wall second.
	allowedCPU := float64(current.cpuQuotaPerSecUSec) * elapsed.Seconds() * 1e3
	if allowedCPU <= 0 {
		return 0, false
	}
	return float64(current.cpuUsageNSec-previous.cpuUsageNSec) / allowedCPU * 100, true
}

// systemdTimespanUnits maps the unit suffixes systemctl show prints for
// timespan properties to microseconds.
var systemdTimespanUnits = map[string]uint64{
	"us": 1, "usec": 1, "ms": 1_000, "msec": 1_000,
	"s": 1_000_000, "sec": 1_000_000, "min": 60_000_000,
	"h": 3_600_000_000, "hr": 3_600_000_000,
}

// parseSystemdTimespanUSec parses a systemctl show timespan into
// microseconds. systemctl prints *USec properties in human form, not as
// integers: CPUQuotaPerSecUSec is "1.500000s" on systemd 259 and may be
// "2s", "500ms", or "1s 500ms" elsewhere. A bare integer is accepted as
// microseconds. "infinity" and anything unparseable report false so callers
// treat the quota as unknown.
func parseSystemdTimespanUSec(value string) (uint64, bool) {
	fields := strings.Fields(value)
	if len(fields) == 0 || len(fields) > 8 {
		return 0, false
	}
	if len(fields) == 1 {
		if plain, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
			return plain, true
		}
	}
	var total uint64
	for _, field := range fields {
		split := strings.IndexFunc(field, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
		if split <= 0 {
			return 0, false
		}
		unit, known := systemdTimespanUnits[field[split:]]
		whole, fraction, hasFraction := strings.Cut(field[:split], ".")
		if !known || whole == "" || (hasFraction && (fraction == "" || len(fraction) > 9 || strings.Contains(fraction, "."))) {
			return 0, false
		}
		amount, err := strconv.ParseUint(whole, 10, 32)
		if err != nil {
			return 0, false
		}
		total += amount * unit
		if hasFraction {
			digits, err := strconv.ParseUint(fraction, 10, 32)
			if err != nil {
				return 0, false
			}
			scale := uint64(1)
			for range fraction {
				scale *= 10
			}
			total += digits * unit / scale
		}
	}
	return total, true
}

// parseResourceStatus reads the KEY=VALUE lines of systemctl show.
func parseResourceStatus(output string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	if values["ActiveState"] == "" {
		values["state"] = "unknown"
	} else {
		values["state"] = values["ActiveState"]
	}
	return values
}

// applyResourceObservation records a site slice's systemctl show values.
// Unlimited ("infinity") and unset ("[not set]") values become 0, which the
// metric writers treat as no limit rather than a measured value.
func applyResourceObservation(sample *siteRuntimeMetricSample, observed map[string]string) {
	sample.resourceAvailable = observed["state"] != "unavailable" && observed["state"] != "unknown"
	sample.memoryCurrent, _ = strconv.ParseUint(observed["MemoryCurrent"], 10, 64)
	sample.memoryMax, _ = strconv.ParseUint(observed["MemoryMax"], 10, 64)
	sample.tasksCurrent, _ = strconv.ParseUint(observed["TasksCurrent"], 10, 64)
	sample.tasksMax, _ = strconv.ParseUint(observed["TasksMax"], 10, 64)
	sample.cpuUsageNSec, _ = strconv.ParseUint(observed["CPUUsageNSec"], 10, 64)
	sample.cpuQuotaPerSecUSec, _ = parseSystemdTimespanUSec(observed["CPUQuotaPerSecUSec"])
}

func (a *App) siteRuntimeInventory() ([]string, map[string]ResourceProfile, bool) {
	profiles := make(map[string]ResourceProfile)
	for _, profile := range a.Resources.profilesSnapshot() {
		if safeUser(profile.Site) == profile.Site && profile.Site != "" {
			profiles[profile.Site] = profile
		}
	}
	names := make(map[string]struct{}, len(profiles))
	for site := range profiles {
		names[site] = struct{}{}
	}
	inventoryUp := true
	if a.Accounts != nil {
		accounts, err := a.Accounts.ListWithError()
		if err != nil {
			inventoryUp = false
		} else {
			for _, account := range accounts {
				for _, site := range account.Sites {
					if safeUser(site) == site && site != "" {
						names[site] = struct{}{}
					}
				}
			}
		}
	}
	if a.Routes != nil {
		for _, route := range a.Routes.list() {
			if safeUser(route.Site) == route.Site && route.Site != "" {
				names[route.Site] = struct{}{}
			}
		}
	}
	if a.Config.WebRoot != "" {
		entries, err := os.ReadDir(filepath.Join(a.Config.WebRoot, "sites"))
		if err != nil {
			inventoryUp = false
		} else {
			for _, entry := range entries {
				site := entry.Name()
				if entry.IsDir() && safeUser(site) == site && site != "" {
					names[site] = struct{}{}
				}
			}
		}
	}
	ordered := make([]string, 0, len(names))
	for site := range names {
		ordered = append(ordered, site)
	}
	sort.Strings(ordered)
	return ordered, profiles, inventoryUp
}

func (a *App) collectSiteFPMMetrics(ctx context.Context, site string) (rootbroker.FPMStatus, error) {
	if !a.Config.Production && !labDirectRootBrokerEnabled() {
		return rootbroker.FPMStatus{}, fmt.Errorf("PHP-FPM metrics require the installed root broker")
	}
	client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", a.Config.WebRoot)
	if err != nil {
		return rootbroker.FPMStatus{}, err
	}
	return client.SiteFPMCounters(ctx, site)
}

func (s *siteRuntimeMetricsStore) write(w io.Writer) {
	if s == nil {
		writeEmptySiteRuntimeMetrics(w)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, _ = fmt.Fprintln(w, "# HELP stepanel_site_runtime_inventory_up Whether the site inventory used for runtime metrics was completely read")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_site_runtime_inventory_up gauge")
	_, _ = fmt.Fprintf(w, "stepanel_site_runtime_inventory_up %d\n", boolMetric(s.inventoryUp))
	_, _ = fmt.Fprintln(w, "# HELP stepanel_site_runtime_inventory_sites Number of sites included in the latest runtime sample")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_site_runtime_inventory_sites gauge")
	_, _ = fmt.Fprintf(w, "stepanel_site_runtime_inventory_sites %d\n", s.inventorySize)
	_, _ = fmt.Fprintln(w, "# HELP stepanel_site_runtime_inventory_sample_timestamp_seconds Time of the latest site runtime inventory refresh")
	_, _ = fmt.Fprintln(w, "# TYPE stepanel_site_runtime_inventory_sample_timestamp_seconds gauge")
	if !s.updatedAt.IsZero() {
		_, _ = fmt.Fprintf(w, "stepanel_site_runtime_inventory_sample_timestamp_seconds %d\n", s.updatedAt.Unix())
	}
	writeSiteRuntimeSamples(w, s.samples)
}

func writeEmptySiteRuntimeMetrics(w io.Writer) {
	(&siteRuntimeMetricsStore{}).write(w)
}

func writeSiteRuntimeSamples(w io.Writer, samples map[string]siteRuntimeMetricSample) {
	for _, metric := range []struct{ name, help string }{
		{"stepanel_site_fpm_configured", "Whether a site resource profile expects a PHP-FPM pool"},
		{"stepanel_site_fpm_probe_up", "Whether a live PHP-FPM status sample was obtained for a site"},
		{"stepanel_site_fpm_sample_timestamp_seconds", "Unix timestamp of the site's live PHP-FPM sample"},
		{"stepanel_site_fpm_saturated", "Whether this PHP-FPM pool has active workers waiting in its listen queue"},
		{"stepanel_site_fpm_active_workers", "Active PHP-FPM workers in the latest pool sample"},
		{"stepanel_site_fpm_max_children", "Configured PHP-FPM max_children for the latest pool sample"},
		{"stepanel_site_fpm_listen_queue", "Requests waiting in the PHP-FPM listen queue"},
		{"stepanel_site_fpm_slow_requests", "Slow PHP-FPM requests counted since the pool started"},
		{"stepanel_site_resource_probe_up", "Whether the site's systemd resource status was measured"},
		{"stepanel_site_resource_configured", "Whether StePanel has an enforced resource profile for this site"},
		{"stepanel_site_resource_memory_current_bytes", "Current site cgroup memory usage in bytes"},
		{"stepanel_site_resource_memory_max_bytes", "Configured site cgroup memory limit in bytes"},
		{"stepanel_site_resource_tasks_current", "Current number of tasks in the site cgroup"},
		{"stepanel_site_resource_tasks_max", "Configured maximum number of tasks in the site cgroup"},
		{"stepanel_site_resource_cpu_quota_utilization_percent", "Measured site CPU time as a percentage of its configured systemd CPU quota over the latest sampling interval"},
		{"stepanel_site_resource_sample_timestamp_seconds", "Unix timestamp of the site's cgroup resource sample"},
	} {
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n", metric.name, metric.help, metric.name)
	}
	for site, sample := range samples {
		if safeUser(site) != site || site == "" {
			continue
		}
		_, _ = fmt.Fprintf(w, "stepanel_site_fpm_configured{site=%q} %d\n", site, boolMetric(sample.fpmConfigured))
		_, _ = fmt.Fprintf(w, "stepanel_site_fpm_probe_up{site=%q} %d\n", site, boolMetric(sample.fpmAvailable))
		_, _ = fmt.Fprintf(w, "stepanel_site_fpm_sample_timestamp_seconds{site=%q} %d\n", site, sample.collectedAt.Unix())
		for _, pool := range sample.pools {
			if pool.State != "available" || !sample.fpmAvailable {
				continue
			}
			labels := fmt.Sprintf("site=%q,php_version=%q", site, pool.PHPVersion)
			_, _ = fmt.Fprintf(w, "stepanel_site_fpm_saturated{%s} %d\n", labels, boolMetric(pool.Saturated))
			_, _ = fmt.Fprintf(w, "stepanel_site_fpm_active_workers{%s} %d\n", labels, pool.ActiveProcesses)
			_, _ = fmt.Fprintf(w, "stepanel_site_fpm_max_children{%s} %d\n", labels, pool.MaxChildren)
			_, _ = fmt.Fprintf(w, "stepanel_site_fpm_listen_queue{%s} %d\n", labels, pool.ListenQueue)
			_, _ = fmt.Fprintf(w, "stepanel_site_fpm_slow_requests{%s} %d\n", labels, pool.SlowRequests)
		}
		_, _ = fmt.Fprintf(w, "stepanel_site_resource_probe_up{site=%q} %d\n", site, boolMetric(sample.resourceAvailable))
		_, _ = fmt.Fprintf(w, "stepanel_site_resource_configured{site=%q} %d\n", site, boolMetric(sample.resourceConfigured))
		_, _ = fmt.Fprintf(w, "stepanel_site_resource_sample_timestamp_seconds{site=%q} %d\n", site, sample.collectedAt.Unix())
		if sample.resourceAvailable && sample.memoryMax > 0 {
			_, _ = fmt.Fprintf(w, "stepanel_site_resource_memory_current_bytes{site=%q} %d\n", site, sample.memoryCurrent)
			_, _ = fmt.Fprintf(w, "stepanel_site_resource_memory_max_bytes{site=%q} %d\n", site, sample.memoryMax)
		}
		if sample.resourceAvailable && sample.tasksMax > 0 {
			_, _ = fmt.Fprintf(w, "stepanel_site_resource_tasks_current{site=%q} %d\n", site, sample.tasksCurrent)
			_, _ = fmt.Fprintf(w, "stepanel_site_resource_tasks_max{site=%q} %d\n", site, sample.tasksMax)
		}
		if sample.resourceAvailable && sample.cpuUtilizationUp {
			_, _ = fmt.Fprintf(w, "stepanel_site_resource_cpu_quota_utilization_percent{site=%q} %.2f\n", site, sample.cpuUtilizationPct)
		}
	}
}

func boolMetric(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (a *App) writeSiteRuntimeMetrics(w io.Writer) {
	if a.siteRuntimeMetrics == nil {
		writeEmptySiteRuntimeMetrics(w)
		return
	}
	a.siteRuntimeMetrics.write(w)
}
