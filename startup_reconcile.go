package stepanel

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// hostReconciler applies one domain of desired host state (routes, SSH
// access, workers, ...) and reports the resources it could not apply.
type hostReconciler struct {
	name string
	run  func(context.Context) ([]string, map[string]string)
}

func (a *App) hostReconcilers() []hostReconciler {
	return []hostReconciler{
		{"routes", a.reconcileRoutes},
		{"SSH access", a.reconcileSiteAccess},
		{"workers", a.reconcileWorkers},
		{"PHP profile", a.reconcilePHPProfiles},
		{"Python application", a.reconcilePythonApps},
		{"scheduled task", a.reconcileTasks},
		{"resource profile", a.reconcileResourceProfiles},
		{"environment", a.reconcileEnvironments},
	}
}

// hostReconcileFailureLimit bounds how many failed resources one readiness
// detail names; the count still covers all of them.
const hostReconcileFailureLimit = 5

// runHostReconcilers runs each domain under its own deadline, so a slow
// helper in an early domain cannot starve the later ones. It returns the
// domains that left any resource unapplied, with one error per domain.
func runHostReconcilers(domains []hostReconciler, timeout time.Duration) ([]hostReconciler, []error) {
	var failedDomains []hostReconciler
	var errs []error
	for _, domain := range domains {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		reconciled, failed := domain.run(ctx)
		cancel()
		if len(failed) == 0 {
			continue
		}
		log.Printf("%s reconciliation incomplete: reconciled=%d failed=%d", domain.name, len(reconciled), len(failed))
		failedDomains = append(failedDomains, domain)
		errs = append(errs, hostReconcileError(domain.name, len(reconciled), failed))
	}
	return failedDomains, errs
}

func hostReconcileError(name string, reconciled int, failed map[string]string) error {
	keys := make([]string, 0, len(failed))
	for key := range failed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	details := make([]string, 0, hostReconcileFailureLimit)
	for _, key := range keys {
		if len(details) == hostReconcileFailureLimit {
			details = append(details, fmt.Sprintf("and %d more", len(keys)-hostReconcileFailureLimit))
			break
		}
		details = append(details, key+": "+failed[key])
	}
	return fmt.Errorf("%s reconciliation failed for %d of %d resource(s): %s", name, len(failed), len(failed)+reconciled, strings.Join(details, "; "))
}

// Host reconciliation retries back off from the first delay to the maximum.
var (
	hostReconcileRetryFirst = 30 * time.Second
	hostReconcileRetryMax   = 10 * time.Minute
)

// retryHostReconcilers re-runs the domains that failed at startup until
// each applies cleanly, publishing the remaining failures to readiness after
// every pass. Readiness recovers without a restart once the host converges.
func (a *App) retryHostReconcilers(ctx context.Context, domains []hostReconciler, timeout time.Duration) {
	delay := hostReconcileRetryFirst
	for len(domains) > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		var errs []error
		domains, errs = runHostReconcilers(domains, timeout)
		a.startup.setHostState(errors.Join(errs...))
		if len(domains) == 0 {
			log.Printf("host reconciliation recovered; startup readiness no longer degraded by host state")
		}
		delay = min(2*delay, hostReconcileRetryMax)
	}
}
