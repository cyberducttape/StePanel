package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type BackupSchedule struct {
	Site             string     `json:"site"`
	IntervalMinutes  int        `json:"interval_minutes"`
	KeepLast         int        `json:"keep_last"`
	IncludeDatabases bool       `json:"include_databases"`
	Enabled          bool       `json:"enabled"`
	NextRun          time.Time  `json:"next_run"`
	LastRun          *time.Time `json:"last_run,omitempty"`
	LastSuccess      *time.Time `json:"last_success,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	LastDurationMS   int64      `json:"last_duration_ms,omitempty"`
	ConsecutiveFails int        `json:"consecutive_failures"`
}

type backupSchedules struct {
	mu    sync.Mutex
	path  string
	items map[string]BackupSchedule
}

func openBackupSchedules(path string) (*backupSchedules, error) {
	s := &backupSchedules{path: path, items: map[string]BackupSchedule{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, errors.New("backup schedule state exceeds 1 MiB")
	}
	if err := json.Unmarshal(b, &s.items); err != nil {
		return nil, fmt.Errorf("decode backup schedules: %w", err)
	}
	for site, item := range s.items {
		if item.KeepLast == 0 {
			item.KeepLast = 7
			s.items[site] = item
		}
		if safeUser(site) == "" || item.Site != site || item.IntervalMinutes < 5 || item.IntervalMinutes > 10080 || item.KeepLast < 1 || item.KeepLast > 365 || item.NextRun.IsZero() {
			return nil, fmt.Errorf("invalid backup schedule for site %q", site)
		}
	}
	return s, nil
}
func (s *backupSchedules) persistLocked() error {
	b, err := json.Marshal(s.items)
	if err != nil {
		return err
	}
	if bound, err := persistBoundControlPlaneState(s, b); bound {
		return err
	}
	return writeAtomic(s.path, append(b, '\n'), 0600)
}
func (s *backupSchedules) list() []BackupSchedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]BackupSchedule, 0, len(s.items))
	for _, v := range s.items {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Site < out[j].Site })
	return out
}

func (a *App) backupSchedules(w http.ResponseWriter, r *http.Request) {
	if a.Schedules == nil {
		http.Error(w, "backup scheduling is unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"schedules": a.Schedules.list()})
	case http.MethodPut:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		var in BackupSchedule
		if err := decodeJSON(w, r, 4096, &in); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		in.Site = safeUser(in.Site)
		if in.KeepLast == 0 {
			in.KeepLast = 7
		}
		if in.Site == "" || in.IntervalMinutes < 5 || in.IntervalMinutes > 10080 || in.KeepLast < 1 || in.KeepLast > 365 {
			http.Error(w, "site is invalid, interval must be 5-10080 minutes, and keep_last must be 1-365", 422)
			return
		}
		if in.IncludeDatabases && a.Config.DBCtl == "" {
			http.Error(w, "managed database backup requires the local database helper", 422)
			return
		}
		if info, err := os.Stat(filepath.Join(a.Config.WebRoot, "sites", in.Site, "public")); err != nil || !info.IsDir() {
			http.Error(w, "site document root does not exist", 422)
			return
		}
		in.Enabled = true
		in.NextRun = time.Now().UTC().Add(time.Duration(in.IntervalMinutes) * time.Minute)
		a.Schedules.mu.Lock()
		previous, existed := a.Schedules.items[in.Site]
		a.Schedules.items[in.Site] = in
		err := a.Schedules.persistLocked()
		if err != nil {
			if existed {
				a.Schedules.items[in.Site] = previous
			} else {
				delete(a.Schedules.items, in.Site)
			}
		}
		a.Schedules.mu.Unlock()
		if err != nil {
			http.Error(w, "could not persist backup schedule", 500)
			return
		}
		_ = ShouldAudit(a.Config.AuditLog, a.Auth.Username, "backup.schedule.updated", in.Site, fmt.Sprintf("every %d minutes; keep last %d", in.IntervalMinutes, in.KeepLast))
		writeJSON(w, http.StatusOK, in)
	case http.MethodDelete:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		site := safeUser(r.URL.Query().Get("site"))
		if site == "" {
			http.Error(w, "invalid site", 422)
			return
		}
		a.Schedules.mu.Lock()
		previous, existed := a.Schedules.items[site]
		delete(a.Schedules.items, site)
		err := a.Schedules.persistLocked()
		if err != nil && existed {
			a.Schedules.items[site] = previous
		}
		a.Schedules.mu.Unlock()
		if err != nil {
			http.Error(w, "could not persist backup schedule", 500)
			return
		}
		_ = ShouldAudit(a.Config.AuditLog, a.Auth.Username, "backup.schedule.deleted", site, "schedule removed")
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (a *App) runDueBackups() {
	if a.Schedules == nil {
		return
	}
	now := time.Now().UTC()
	a.Schedules.mu.Lock()
	defer a.Schedules.mu.Unlock()
	for site, s := range a.Schedules.items {
		if !s.Enabled || s.NextRun.After(now) {
			continue
		}
		operationKey := fmt.Sprintf("schedule:%s:%d", site, s.NextRun.UnixNano())
		s.LastRun = &now
		s.NextRun = now.Add(time.Duration(s.IntervalMinutes) * time.Minute)
		payload, marshalErr := json.Marshal(durableBackupRequest{Site: site, IncludeDatabases: s.IncludeDatabases, Scheduled: true, KeepLast: s.KeepLast, Actor: "scheduler", StartedAt: now})
		if marshalErr != nil {
			continue
		}
		_, _, enqueueErr := a.Jobs.EnqueueIdempotent("site.backup", site, operationKey, payload, 3)
		if enqueueErr != nil {
			continue
		}
		a.Schedules.items[site] = s
		if err := a.Schedules.persistLocked(); err != nil {
			_ = ShouldAudit(a.Config.AuditLog, "scheduler", "backup.schedule.persistence_failed", site, err.Error())
		}
	}
}

func (s *backupSchedules) recordResult(site string, started time.Time, resultErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, exists := s.items[site]
	if !exists {
		return
	}
	item.LastDurationMS = time.Since(started).Milliseconds()
	if resultErr != nil {
		item.LastError = resultErr.Error()
		item.ConsecutiveFails++
	} else {
		now := time.Now().UTC()
		item.LastSuccess = &now
		item.LastError = ""
		item.ConsecutiveFails = 0
	}
	s.items[site] = item
	if err := s.persistLocked(); err != nil {
		log.Printf("persist backup result for %s: %v", site, err)
	}
}
