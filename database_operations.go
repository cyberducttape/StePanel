package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type DatabaseResource struct {
	Name     string `json:"name"`
	Site     string `json:"site"`
	User     string `json:"user,omitempty"`
	Bytes    uint64 `json:"bytes"`
	Encoding string `json:"encoding"`
}

type DatabaseDiagnostics struct {
	Available bool              `json:"available"`
	Engine    string            `json:"engine"`
	Collected time.Time         `json:"collected_at"`
	Values    map[string]uint64 `json:"values"`
	Warnings  []string          `json:"warnings"`
	Detail    string            `json:"detail,omitempty"`
}

type DatabaseSafetyBackup struct {
	Database string    `json:"database"`
	Created  time.Time `json:"created_at"`
	Path     string    `json:"path"`
	SHA256   string    `json:"sha256"`
}

type DatabaseSession struct {
	ID       uint64 `json:"id"`
	User     string `json:"user"`
	Database string `json:"database,omitempty"`
	State    string `json:"state"`
	Seconds  uint64 `json:"seconds"`
	Wait     string `json:"wait,omitempty"`
}

func runDatabaseHelper(cfg Config, timeout time.Duration, input string, args ...string) ([]byte, error) {
	if cfg.DBCtl == "" {
		return nil, errors.New("local database lifecycle helper is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := helperCommandContext(ctx, cfg, cfg.DBCtl, args...)
	if input == "" {
		return runBoundedCommand(ctx, cmd)
	}
	return runBoundedCommandInput(ctx, cmd, strings.NewReader(input+"\n"))
}

func managedDatabaseInventory(cfg Config) ([]DatabaseResource, error) {
	output, err := runDatabaseHelper(cfg, 15*time.Second, "", "inventory")
	if err != nil {
		return nil, err
	}
	items := []DatabaseResource{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 || !validManagedDatabaseIdentifier(fields[0], databaseNameLimit(cfg)) || safeUser(fields[1]) == "" || fields[2] != "" && !validManagedDatabaseIdentifier(fields[2], 32) {
			return nil, errors.New("database helper returned invalid inventory")
		}
		bytes, err := strconv.ParseUint(fields[3], 10, 64)
		if err != nil {
			return nil, errors.New("database helper returned invalid database size")
		}
		items = append(items, DatabaseResource{Name: fields[0], Site: fields[1], User: fields[2], Bytes: bytes, Encoding: fields[4]})
	}
	return items, nil
}

func databaseNameLimit(cfg Config) int {
	if cfg.DBEngine == "postgresql" {
		return 63
	}
	return 64
}

func collectDatabaseDiagnostics(cfg Config) DatabaseDiagnostics {
	engine := cfg.DBEngine
	if engine == "" {
		engine = "mysql"
	}
	d := DatabaseDiagnostics{Engine: engine, Collected: time.Now().UTC(), Values: map[string]uint64{}, Warnings: []string{}}
	output, err := runDatabaseHelper(cfg, 10*time.Second, "", "diagnostics")
	if err != nil {
		d.Detail = "Database diagnostics require a healthy local engine and the restricted lifecycle helper."
		return d
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 {
			continue
		}
		value, parseErr := strconv.ParseUint(strings.TrimSpace(fields[1]), 10, 64)
		if parseErr == nil {
			d.Values[fields[0]] = value
		}
	}
	d.Available = len(d.Values) > 0
	if d.Values["blocked_sessions"] > 0 {
		d.Warnings = append(d.Warnings, "blocked database sessions require investigation")
	}
	if d.Values["long_transactions"] > 0 {
		d.Warnings = append(d.Warnings, "transactions older than five minutes were detected")
	}
	return d
}

func (a *App) cachedDatabaseDiagnostics(maxAge time.Duration) DatabaseDiagnostics {
	a.databaseDiagnosticsMu.Lock()
	defer a.databaseDiagnosticsMu.Unlock()
	if !a.databaseDiagnosticsCache.Collected.IsZero() && time.Since(a.databaseDiagnosticsCache.Collected) < maxAge {
		return a.databaseDiagnosticsCache
	}
	a.databaseDiagnosticsCache = collectDatabaseDiagnostics(a.Config)
	return a.databaseDiagnosticsCache
}

func (a *App) databaseCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := managedDatabaseInventory(a.Config)
		if err != nil {
			http.Error(w, "managed database inventory is unavailable", http.StatusServiceUnavailable)
			return
		}
		if a.Accounts != nil && !a.Auth.IsAdministrator(r) {
			filtered := items[:0]
			for _, item := range items {
				if a.canAccessSite(r, item.Site) {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
		writeJSON(w, http.StatusOK, map[string]any{"databases": items, "engine": a.Config.DBEngine})
	case http.MethodPost:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		var in struct {
			Name, User, Site, Password, Encoding string
		}
		if err := decodeJSON(w, r, 4096, &in); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		in.Name, in.User, in.Site = strings.ToLower(strings.TrimSpace(in.Name)), strings.ToLower(strings.TrimSpace(in.User)), strings.ToLower(strings.TrimSpace(in.Site))
		if !validManagedDatabaseIdentifier(in.Name, databaseNameLimit(a.Config)) || !validManagedDatabaseIdentifier(in.User, 32) || in.User[0] < 'a' || in.User[0] > 'z' || safeUser(in.Site) == "" || !validDatabasePassword(in.Password) {
			http.Error(w, "invalid database, user, site, or password; passwords must be 20-128 supported characters", http.StatusUnprocessableEntity)
			return
		}
		if info, err := os.Stat(filepath.Join(a.Config.WebRoot, "sites", in.Site, "public")); err != nil || !info.IsDir() {
			http.Error(w, "owning site document root does not exist", http.StatusUnprocessableEntity)
			return
		}
		lockKeys := []string{in.Site, "database:" + in.Name}
		if !a.Auth.IsAdministrator(r) {
			lockKeys = append(lockKeys, "account:"+a.Auth.UsernameForRequest(r))
		}
		releaseUnlock, lockErr := a.acquireSiteMutationLocks(r.Context(), lockKeys...)
		if lockErr != nil {
			http.Error(w, "database mutation is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
		if a.Accounts != nil && !a.Auth.IsAdministrator(r) {
			if _, ok := a.requireSiteAccess(w, r, in.Site, "site is not assigned to this account", http.StatusForbidden); !ok {
				return
			}
			if !a.Auth.HasRequiredCustomerScope(r, "database:write") {
				http.Error(w, "API token lacks the database:write scope", http.StatusForbidden)
				return
			}
			account, ok := a.Accounts.Get(a.Auth.UsernameForRequest(r))
			plan := hostingPlans[account.Plan]
			if !ok || plan.DatabaseLimit < 1 {
				http.Error(w, "database entitlement is unavailable", http.StatusForbidden)
				return
			}
			current, inventoryErr := managedDatabaseInventory(a.Config)
			if inventoryErr != nil {
				http.Error(w, "managed database inventory is unavailable", http.StatusServiceUnavailable)
				return
			}
			count := 0
			for _, item := range current {
				if a.canAccessSite(r, item.Site) {
					count++
				}
			}
			if count >= plan.DatabaseLimit {
				http.Error(w, "database plan limit reached", http.StatusConflict)
				return
			}
		}
		if in.Encoding == "" {
			if a.Config.DBEngine == "postgresql" {
				in.Encoding = "UTF8"
			} else {
				in.Encoding = "utf8mb4"
			}
		}
		if a.Config.DBEngine == "postgresql" && in.Encoding != "UTF8" || a.Config.DBEngine != "postgresql" && in.Encoding != "utf8mb4" {
			http.Error(w, "encoding must be UTF8 for PostgreSQL or utf8mb4 for MySQL/MariaDB", http.StatusUnprocessableEntity)
			return
		}
		if _, err := runDatabaseHelper(a.Config, time.Minute, in.Password, "provision", in.Name, in.User, in.Site, in.Encoding); err != nil {
			log.Printf("database provision rejected for %s: %v", in.Name, err)
			http.Error(w, "database or user already exists, or provisioning failed", http.StatusConflict)
			return
		}
		recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "database.provisioned", in.Name, fmt.Sprintf("site=%s user=%s encoding=%s", in.Site, in.User, in.Encoding))
		writeJSON(w, http.StatusCreated, DatabaseResource{Name: in.Name, Site: in.Site, User: in.User, Encoding: in.Encoding})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func validDatabasePassword(password string) bool {
	if len(password) < 20 || len(password) > 128 {
		return false
	}
	for _, char := range password {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#%^*_=+.,:-", char) {
			return false
		}
	}
	return true
}

func createDatabaseSafetyBackup(cfg Config, database string) (DatabaseSafetyBackup, error) {
	result := DatabaseSafetyBackup{Database: database, Created: time.Now().UTC()}
	root := filepath.Join(cfg.BackupRoot, ".database-deletions")
	if err := os.MkdirAll(root, 0700); err != nil {
		return result, err
	}
	temp, err := os.MkdirTemp(root, ".pending-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(temp)
	dump := filepath.Join(temp, database+".sql")
	if err := dumpManagedDatabase(cfg, database, dump); err != nil {
		return result, err
	}
	result.SHA256, err = fileSHA256(dump)
	if err != nil {
		return result, err
	}
	if err := writeSyncedFile(filepath.Join(temp, database+".sql.sha256"), []byte(result.SHA256+"  "+database+".sql\n"), 0600); err != nil {
		return result, err
	}
	final := filepath.Join(root, result.Created.Format("20060102-150405.000000000")+"-"+database)
	if err := os.Rename(temp, final); err != nil {
		return result, err
	}
	if err := syncDirectory(root); err != nil {
		return result, err
	}
	result.Path = final
	return result, nil
}

func (a *App) databaseResource(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/databases/")
	credentialRotation := strings.HasSuffix(path, "/credentials")
	name := strings.TrimSuffix(path, "/credentials")
	if !validManagedDatabaseIdentifier(name, databaseNameLimit(a.Config)) || strings.Contains(name, "/") {
		http.Error(w, "invalid database", http.StatusUnprocessableEntity)
		return
	}
	items, inventoryErr := managedDatabaseInventory(a.Config)
	customerRequest := a.Accounts != nil && !a.Auth.IsAdministrator(r)
	if inventoryErr != nil && (r.Method == http.MethodGet || r.Method == http.MethodHead || customerRequest || a.Accounts != nil) {
		http.Error(w, "managed database inventory is unavailable", http.StatusServiceUnavailable)
		return
	}
	var owned DatabaseResource
	found := false
	for _, item := range items {
		if item.Name == name {
			owned, found = item, true
			break
		}
	}
	if !found && inventoryErr == nil {
		http.NotFound(w, r)
		return
	}
	if customerRequest {
		if _, ok := a.requireSiteAccess(w, r, owned.Site, "database is not assigned to this account", http.StatusForbidden); !ok {
			return
		}
	}
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && !credentialRotation {
		for _, item := range items {
			if item.Name == name {
				writeJSON(w, http.StatusOK, map[string]any{"database": item, "engine": a.Config.DBEngine})
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	if !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "database:write") {
		http.Error(w, "API token lacks the database:write scope", http.StatusForbidden)
		return
	}
	var in struct{ User, Password, Confirm string }
	if err := decodeJSON(w, r, 4096, &in); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	in.User = strings.ToLower(strings.TrimSpace(in.User))
	if !validManagedDatabaseIdentifier(in.User, 32) || in.User[0] < 'a' || in.User[0] > 'z' {
		http.Error(w, "invalid database user", http.StatusUnprocessableEntity)
		return
	}
	switch {
	case r.Method == http.MethodPatch && credentialRotation:
		if !validDatabasePassword(in.Password) {
			http.Error(w, "password must contain 20-128 supported characters", http.StatusUnprocessableEntity)
			return
		}
		releaseUnlock, lockErr := a.acquireSiteMutationLock(r.Context(), "database:"+name)
		if lockErr != nil {
			http.Error(w, "database mutation is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
		if _, err := runDatabaseHelper(a.Config, 30*time.Second, in.Password, "rotate", name, in.User); err != nil {
			http.Error(w, "credential rotation failed", http.StatusConflict)
			return
		}
		recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "database.credentials_rotated", name, "user="+in.User)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && !credentialRotation:
		if in.Confirm != "DROP "+name {
			http.Error(w, "confirmation must exactly match DROP "+name, http.StatusUnprocessableEntity)
			return
		}
		releaseUnlock, lockErr := a.acquireSiteMutationLock(r.Context(), "database:"+name)
		if lockErr != nil {
			http.Error(w, "database mutation is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
		safetyBackup, err := createDatabaseSafetyBackup(a.Config, name)
		if err != nil {
			log.Printf("database safety backup failed for %s: %v", name, err)
			http.Error(w, "database deletion refused because its safety backup failed", http.StatusServiceUnavailable)
			return
		}
		if _, err := runDatabaseHelper(a.Config, time.Minute, "", "drop-managed", name, in.User); err != nil {
			http.Error(w, "database deletion failed", http.StatusConflict)
			return
		}
		if err := MustAudit(w, a.Config.AuditLog, a.Auth.UsernameForRequest(r), "database.deleted", name, "user="+in.User+" safety_backup="+safetyBackup.Path+" sha256="+safetyBackup.SHA256); err != nil {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": name, "safety_backup": safetyBackup})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) databaseDiagnostics(w http.ResponseWriter, _ *http.Request) {
	diagnostics := a.cachedDatabaseDiagnostics(5 * time.Second)
	status := http.StatusOK
	if !diagnostics.Available {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, diagnostics)
}

func (a *App) databaseSessions(w http.ResponseWriter, _ *http.Request) {
	output, err := runDatabaseHelper(a.Config, 10*time.Second, "", "sessions")
	if err != nil {
		http.Error(w, "database sessions are unavailable", http.StatusServiceUnavailable)
		return
	}
	sessions := []DatabaseSession{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 6 {
			continue
		}
		id, idErr := strconv.ParseUint(fields[0], 10, 64)
		seconds, secondsErr := strconv.ParseUint(fields[4], 10, 64)
		if idErr == nil && secondsErr == nil {
			sessions = append(sessions, DatabaseSession{ID: id, User: fields[1], Database: fields[2], State: fields[3], Seconds: seconds, Wait: fields[5]})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions, "query_text_included": false})
}

func (a *App) databaseSettings(w http.ResponseWriter, _ *http.Request) {
	output, err := runDatabaseHelper(a.Config, 10*time.Second, "", "settings")
	if err != nil {
		http.Error(w, "database settings are unavailable", http.StatusServiceUnavailable)
		return
	}
	settings := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) == 2 {
			settings[fields[0]] = fields[1]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings, "read_only": true, "detail": "Effective values are shown for review; StePanel does not auto-tune production databases."})
}

func (a *App) databaseSessionTerminate(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/database/sessions/")
	if r.Method != http.MethodDelete || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if _, err := strconv.ParseUint(id, 10, 64); err != nil || id == "0" {
		http.Error(w, "invalid session ID", http.StatusUnprocessableEntity)
		return
	}
	var in struct{ Confirm string }
	if err := decodeJSON(w, r, 1024, &in); err != nil || in.Confirm != "TERMINATE "+id {
		http.Error(w, "confirmation must exactly match TERMINATE "+id, http.StatusUnprocessableEntity)
		return
	}
	if _, err := runDatabaseHelper(a.Config, 15*time.Second, "", "terminate", id); err != nil {
		http.Error(w, "session termination failed", http.StatusConflict)
		return
	}
	recordAudit(a.Config.AuditLog, a.Auth.Username, "database.session_terminated", id, "explicit operator confirmation")
	w.WriteHeader(http.StatusNoContent)
}
