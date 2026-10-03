package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/recovery"
)

// rehearsalPolicy derives how old recovery evidence may be before the status
// degrades: twice the automatic rehearsal interval, and twice the site's
// backup schedule interval. Either check is off when its cadence is unset.
func (a *App) rehearsalPolicy(site string) recovery.Policy {
	var policy recovery.Policy
	if a.Config.RehearsalIntervalHours > 0 {
		policy.RehearsalMaxAge = 2 * time.Duration(a.Config.RehearsalIntervalHours) * time.Hour
	}
	if a.Schedules != nil {
		a.Schedules.mu.Lock()
		schedule, ok := a.Schedules.items[site]
		a.Schedules.mu.Unlock()
		if ok && schedule.Enabled && schedule.IntervalMinutes > 0 {
			policy.BackupMaxAge = 2 * time.Duration(schedule.IntervalMinutes) * time.Minute
		}
	}
	return policy
}

// maybeEnqueueScheduledRehearsal queues a restore rehearsal of a backup that
// a scheduled backup just produced, unless the site was rehearsed within the
// configured interval. Rehearsals therefore follow the backup schedule
// without scanning backup directories.
func (a *App) maybeEnqueueScheduledRehearsal(ctx context.Context, site, backup string) {
	if a.Recovery == nil || a.Config.RehearsalIntervalHours <= 0 {
		return
	}
	interval := time.Duration(a.Config.RehearsalIntervalHours) * time.Hour
	latest, err := a.Recovery.Latest(ctx, site)
	if err != nil {
		log.Printf("check last rehearsal for %s: %v", site, err)
		return
	}
	if latest != nil && time.Since(latest.StartedAt) < interval {
		return
	}
	payload, err := json.Marshal(durableBackupRehearsalRequest{Site: site, Backup: backup, Actor: "scheduler", Scheduled: true})
	if err != nil {
		log.Printf("encode scheduled rehearsal for %s: %v", site, err)
		return
	}
	if _, _, err := a.Jobs.EnqueueIdempotent("backup.rehearsal", site, "rehearsal:"+site+":"+backup, payload, 1); err != nil {
		recordAudit(a.Config.AuditLog, "scheduler", "backup.rehearsal.enqueue_failed", site, err.Error())
	}
}

// siteRecoveryResponse is the recovery status plus recent history.
type siteRecoveryResponse struct {
	recovery.Status
	History                     []recovery.Rehearsal `json:"history"`
	RehearsalIntervalHours      int                  `json:"rehearsal_interval_hours"`
	AutomaticRehearsalsEnabled  bool                 `json:"automatic_rehearsals_enabled"`
	ScheduledBackupsForThisSite bool                 `json:"scheduled_backups"`
}

// siteRecovery serves GET /api/sites/recovery/{site}.
func (a *App) siteRecovery(w http.ResponseWriter, r *http.Request) {
	if !a.requireCustomerScope(w, r, "backup:read") {
		return
	}
	site := strings.TrimPrefix(r.URL.Path, "/api/sites/recovery/")
	if site == "" || strings.Contains(site, "/") || safeUser(site) == "" {
		http.Error(w, "invalid site", http.StatusUnprocessableEntity)
		return
	}
	access, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", http.StatusForbidden)
	if !ok {
		return
	}
	if a.Recovery == nil {
		http.Error(w, "recovery history is unavailable", http.StatusServiceUnavailable)
		return
	}
	response, err := a.buildSiteRecovery(r.Context(), access)
	if err != nil {
		log.Printf("build recovery status for %s: %v", site, err)
		http.Error(w, "recovery status is unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) buildSiteRecovery(ctx context.Context, access SiteCapability) (siteRecoveryResponse, error) {
	site := access.Site()
	in := recovery.Inputs{Site: site, Now: time.Now().UTC(), Policy: a.rehearsalPolicy(site)}
	backups, err := listBackupsPage(a.Config.BackupRoot, access, 1, a.Config.BackupSigningKey, a.Config.BackupEncryptionKey)
	if err != nil {
		return siteRecoveryResponse{}, fmt.Errorf("list backups: %w", err)
	}
	if len(backups) > 0 {
		newest := backups[0]
		name := filepath.Base(newest.Path)
		in.LatestBackup = &recovery.BackupFacts{Name: name, CreatedAt: newest.CreatedAt, Signed: newest.ManifestSigned, Encrypted: newest.Encrypted, Consistency: newest.Consistency}
		if a.Config.OffsiteTarget != "" {
			in.Offsite.Configured = true
			if a.BackupIndex != nil {
				in.Offsite.Tracked, in.Offsite.UploadedAt, err = a.BackupIndex.OffsiteBackupState(a.Config.OffsiteTarget, site, name)
				if err != nil {
					return siteRecoveryResponse{}, fmt.Errorf("read offsite state: %w", err)
				}
			}
		}
	} else if a.Config.OffsiteTarget != "" {
		in.Offsite.Configured = true
	}
	if in.LatestRehearsal, err = a.Recovery.Latest(ctx, site); err != nil {
		return siteRecoveryResponse{}, err
	}
	if in.LatestPassed, err = a.Recovery.LatestPassed(ctx, site); err != nil {
		return siteRecoveryResponse{}, err
	}
	history, err := a.Recovery.History(ctx, site, 10)
	if err != nil {
		return siteRecoveryResponse{}, err
	}
	scheduled := false
	if a.Schedules != nil {
		a.Schedules.mu.Lock()
		schedule, ok := a.Schedules.items[site]
		a.Schedules.mu.Unlock()
		scheduled = ok && schedule.Enabled
	}
	return siteRecoveryResponse{
		Status:                      recovery.Assess(in),
		History:                     history,
		RehearsalIntervalHours:      a.Config.RehearsalIntervalHours,
		AutomaticRehearsalsEnabled:  a.Config.RehearsalIntervalHours > 0,
		ScheduledBackupsForThisSite: scheduled,
	}, nil
}
