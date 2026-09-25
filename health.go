package main

import (
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

type ReadinessCheck struct {
	Ready  bool   `json:"ready"`
	Detail string `json:"detail,omitempty"`
}

func (a *App) livez(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) readyz(w http.ResponseWriter, r *http.Request) {
	checks := readinessChecks(a.Config, a.Jobs)
	startupInProgress, startupError := a.startup.status()
	if startupInProgress {
		checks["startup_state"] = ReadinessCheck{Ready: false, Detail: "recovery and reconciliation in progress"}
	} else if startupError != nil {
		checks["startup_state"] = ReadinessCheck{Ready: false, Detail: startupError.Error()}
	} else {
		checks["startup_state"] = ReadinessCheck{Ready: true}
	}
	if a.RecoveryError != nil {
		checks["recovery_state"] = ReadinessCheck{Ready: false, Detail: a.RecoveryError.Error()}
	} else {
		checks["recovery_state"] = ReadinessCheck{Ready: true}
	}
	if err := a.Auth.SessionPersistenceError(); err != nil {
		checks["session_state"] = ReadinessCheck{Ready: false, Detail: err.Error()}
	} else {
		checks["session_state"] = ReadinessCheck{Ready: true}
	}
	ready := true
	for _, check := range checks {
		if !check.Ready {
			ready = false
			break
		}
	}
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	if a.Auth.Enabled && !a.Auth.validSession(r) {
		for name, check := range checks {
			check.Detail = ""
			checks[name] = check
		}
	}
	writeJSON(w, status, map[string]any{"ready": ready, "checks": checks, "time": time.Now().UTC()})
}

func (a *App) operationalHealth(w http.ResponseWriter, r *http.Request) {
	checks := operationalChecks(a.Config, a.Jobs)
	startupInProgress, startupError := a.startup.status()
	switch {
	case startupInProgress:
		checks["startup_reconciliation"] = ReadinessCheck{Ready: false, Detail: "recovery and reconciliation in progress"}
	case startupError != nil:
		checks["startup_reconciliation"] = ReadinessCheck{Ready: false, Detail: startupError.Error()}
	default:
		checks["startup_reconciliation"] = ReadinessCheck{Ready: true}
	}
	operational := true
	for _, check := range checks {
		if !check.Ready {
			operational = false
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "operational": operational, "checks": checks, "time": time.Now().UTC()})
}

func readinessChecks(cfg Config, jobs *Jobs) map[string]ReadinessCheck {
	checks := map[string]ReadinessCheck{}
	if err := AuditPersistenceError(); err != nil {
		checks["audit_state"] = ReadinessCheck{Ready: false, Detail: err.Error()}
	} else {
		checks["audit_state"] = ReadinessCheck{Ready: true}
	}
	if jobs == nil {
		checks["job_state"] = ReadinessCheck{Ready: false, Detail: "job store is not initialized"}
	} else if err := jobs.PersistenceError(); err != nil {
		checks["job_state"] = ReadinessCheck{Ready: false, Detail: err.Error()}
	} else if jobs.db != nil {
		if err := jobs.IntegrityCheck(); err != nil {
			checks["control_plane_integrity"] = ReadinessCheck{Ready: false, Detail: err.Error()}
		} else {
			checks["control_plane_integrity"] = ReadinessCheck{Ready: true}
		}
		checks["job_state"] = ReadinessCheck{Ready: true}
	} else {
		checks["job_state"] = ReadinessCheck{Ready: true}
	}
	return checks
}

func operationalChecks(cfg Config, jobs *Jobs) map[string]ReadinessCheck {
	checks := map[string]ReadinessCheck{}
	if jobs == nil || jobs.db == nil {
		checks["dead_letter_jobs"] = ReadinessCheck{Ready: false, Detail: "job store is not initialized"}
	} else if stats, err := jobs.QueueStats(); err != nil {
		checks["dead_letter_jobs"] = ReadinessCheck{Ready: false, Detail: fmt.Sprintf("durable queue unavailable: %v", err)}
	} else if stats.DeadLetter > 0 {
		checks["dead_letter_jobs"] = ReadinessCheck{Ready: false, Detail: fmt.Sprintf("%d durable jobs require operator review", stats.DeadLetter)}
	} else {
		checks["dead_letter_jobs"] = ReadinessCheck{Ready: true}
	}
	roots := map[string]string{
		"backup_capacity":   cfg.BackupRoot,
		"import_capacity":   cfg.ImportRoot,
		"job_capacity":      filepath.Dir(cfg.JobState),
		"recovery_capacity": filepath.Dir(cfg.RecoveryRoot),
	}
	if cfg.RequireOffsiteBackup {
		if cfg.OffsiteTarget == "" {
			checks["offsite_backup"] = ReadinessCheck{Ready: false, Detail: "offsite backup is required but no target is configured"}
		} else if err := validateOffsiteTarget(cfg.OffsiteTarget); err != nil {
			checks["offsite_backup"] = ReadinessCheck{Ready: false, Detail: err.Error()}
		} else if _, err := exec.LookPath("rclone"); err != nil {
			checks["offsite_backup"] = ReadinessCheck{Ready: false, Detail: "rclone is required for the configured offsite backup target"}
		} else {
			checks["offsite_backup"] = ReadinessCheck{Ready: true, Detail: "rclone and an offsite target are configured"}
		}
	}
	names := make([]string, 0, len(roots))
	for name := range roots {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := roots[name]
		free, err := availableBytes(path)
		switch {
		case err != nil:
			checks[name] = ReadinessCheck{Ready: false, Detail: err.Error()}
		case free < cfg.MinFreeBytes:
			checks[name] = ReadinessCheck{Ready: false, Detail: fmt.Sprintf("%d bytes free; minimum is %d", free, cfg.MinFreeBytes)}
		default:
			checks[name] = ReadinessCheck{Ready: true, Detail: fmt.Sprintf("%d bytes free", free)}
		}
	}
	return checks
}

func restoreCapacity(cfg Config) error {
	paths := []string{cfg.ImportRoot, filepath.Join(cfg.WebRoot, "sites")}
	checked := map[string]bool{}
	for _, path := range paths {
		if checked[path] {
			continue
		}
		checked[path] = true
		free, err := availableBytes(path)
		if err != nil {
			return fmt.Errorf("inspect restore capacity at %s: %w", path, err)
		}
		if free < cfg.MinFreeBytes {
			return fmt.Errorf("insufficient free space at %s: %d bytes available, %d required", path, free, cfg.MinFreeBytes)
		}
	}
	return nil
}
