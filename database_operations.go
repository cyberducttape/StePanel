package stepanel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
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
	return runDatabaseHelperContext(context.Background(), cfg, timeout, input, args...)
}

func runDatabaseHelperContext(parent context.Context, cfg Config, timeout time.Duration, input string, args ...string) ([]byte, error) {
	if cfg.DBCtl == "" {
		return nil, errors.New("local database lifecycle helper is unavailable")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if cfg.Production {
		// Route managed mutations through their typed broker contract before
		// the generic compatibility adapter. The generic adapter reports every
		// helper request as handled, so placing this below it silently bypasses
		// the typed DB ABI.
		if typedDatabaseMutation(args) {
			return runTypedDatabaseMutation(ctx, cfg, input, args...)
		}
		var helperInput []byte
		if input != "" {
			helperInput = []byte(input + "\n")
		}
		output, err, handled := runAllowlistedHelperOutput(ctx, cfg, helperInput, cfg.DBCtl, args...)
		if handled {
			return output, err
		}
		return nil, errors.New("production database helper is not routed through the root broker")
	}
	cmd := helperCommandContext(ctx, cfg, cfg.DBCtl, args...)
	if input == "" {
		return runBoundedCommand(ctx, cmd)
	}
	return runBoundedCommandInput(ctx, cmd, strings.NewReader(input+"\n"))
}

func typedDatabaseMutation(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "provision", "rotate", "drop-managed", "cleanup-wordpress":
		return true
	default:
		return false
	}
}

func runTypedDatabaseMutation(ctx context.Context, cfg Config, input string, args ...string) ([]byte, error) {
	client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", cfg.WebRoot)
	if err != nil {
		return nil, err
	}
	if len(args) < 2 {
		return nil, errors.New("invalid typed database mutation")
	}
	req := &rootbroker.DBRequest{Action: args[0], Database: args[1]}
	switch args[0] {
	case "provision":
		if len(args) != 5 {
			return nil, errors.New("invalid database provision arguments")
		}
		req.Username, req.Site, req.Encoding, req.Password = args[2], args[3], args[4], input
	case "rotate", "drop-managed", "cleanup-wordpress":
		if len(args) != 3 {
			return nil, errors.New("invalid managed database arguments")
		}
		req.Username, req.Password = args[2], input
	}
	response, err := client.Execute(ctx, &rootbroker.Request{RequestType: "db", DB: req})
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, errors.New(response.Error)
	}
	return response.Details, nil
}

// runDatabaseRestoreFromPath keeps large SQL streams inside the privileged
// broker boundary. Native production installs send only the validated staging
// path; the root broker opens it and streams it to the fixed database helper.
// Development/lab callers retain the same helper ABI without requiring the
// production socket.
func runDatabaseRestoreFromPath(ctx context.Context, cfg Config, action, site, database, username, password, dumpPath string) error {
	if cfg.DBCtl == "" {
		return errors.New("local database lifecycle helper is unavailable")
	}
	if cfg.Production {
		client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", cfg.WebRoot)
		if err != nil {
			return err
		}
		response, err := client.DBRestoreFromPath(ctx, action, site, database, username, password, dumpPath)
		if err != nil {
			return err
		}
		if !response.OK {
			return errors.New(response.Error)
		}
		return nil
	}
	input, _, err := openRegularNoFollow(dumpPath, nil)
	if err != nil {
		return err
	}
	defer input.Close()
	var stream io.Reader = input
	args := []string{action, database}
	switch action {
	case "restore", "restore-dump":
		args = append(args, site)
	case "restore-wordpress":
		args = append(args, username, site)
		stream = io.MultiReader(strings.NewReader(password+"\n"), input)
	default:
		return errors.New("unsupported database restore action")
	}
	output, err := runBoundedCommandInput(ctx, helperCommandContext(ctx, cfg, cfg.DBCtl, args...), stream)
	if err != nil {
		return fmt.Errorf("database restore failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func labDirectRootBrokerEnabled() bool {
	if os.Getenv("STEPANEL_LAB_DIRECT_ROOT_BROKER") == "1" && os.Getenv("STEPANEL_SKIP_STARTUP_HOST_RECONCILE") == "1" {
		return true
	}
	// The termination recovery smoke injects this boundary into the worker
	// drop-in. It is the one lab marker proven to survive the worker restart;
	// pair it with the lab-only startup skip so production cannot select this
	// direct execution path accidentally.
	if os.Getenv("STEPANEL_KILL_AT") == "terminate:site-state" && os.Getenv("STEPANEL_SKIP_STARTUP_HOST_RECONCILE") == "1" {
		return true
	}
	info, err := os.Stat("/run/stepanel-lab-direct-root-broker")
	if err == nil && info.Mode().IsRegular() && info.Mode().Perm() == 0600 {
		return true
	}
	info, err = os.Stat("/etc/stepanel-lab-direct-root-broker")
	if err == nil && info.Mode().IsRegular() && info.Mode().Perm() == 0600 {
		return true
	}
	return false
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

// overviewInventoryMaxAge bounds how stale the database counts on the site
// overview pages may be. Database mutations through the API invalidate the
// cache immediately; other paths (restores, terminations) show up within it.
const overviewInventoryMaxAge = 5 * time.Second

// cachedManagedDatabaseInventory serves the read-only site overview pages.
// Every uncached inventory is a helper call (a root broker round trip in
// production), and the dashboard requests overviews constantly; one caller
// refreshes an expired copy while concurrent callers wait. Failures are not
// cached. Callers get their own copy and may filter it in place.
func (a *App) cachedManagedDatabaseInventory() ([]DatabaseResource, error) {
	a.databaseInventoryMu.Lock()
	defer a.databaseInventoryMu.Unlock()
	if a.databaseInventoryCache == nil || time.Since(a.databaseInventoryAt) >= overviewInventoryMaxAge {
		items, err := managedDatabaseInventory(a.Config)
		if err != nil {
			return nil, err
		}
		a.databaseInventoryCache, a.databaseInventoryAt = items, time.Now()
	}
	return append([]DatabaseResource(nil), a.databaseInventoryCache...), nil
}

func (a *App) invalidateDatabaseInventory() {
	a.databaseInventoryMu.Lock()
	a.databaseInventoryCache = nil
	a.databaseInventoryMu.Unlock()
}

func (a *App) databaseCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		defer a.invalidateDatabaseInventory()
	}
	switch r.Method {
	case http.MethodGet:
		if !a.requireCustomerScope(w, r, "database:read") {
			return
		}
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
		if _, err := existingManagedSitePublicRoot(a.Config.WebRoot, in.Site); err != nil {
			http.Error(w, "owning site document root does not exist", http.StatusUnprocessableEntity)
			return
		}
		lockKeys := []string{in.Site, "database:" + in.Name}
		if !a.Auth.IsAdministrator(r) {
			lockKeys = append(lockKeys, a.customerTenantLockKey(r))
		}
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLocksContext(r.Context(), lockKeys...)
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
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "database mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		if _, err := runDatabaseHelperContext(operationCtx, a.Config, time.Minute, in.Password, "provision", in.Name, in.User, in.Site, in.Encoding); err != nil {
			log.Printf("database provision rejected for %s: %v", in.Name, err)
			http.Error(w, "database or user already exists, or provisioning failed", http.StatusConflict)
			return
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "database provisioning cancelled because the mutation lock was lost", http.StatusConflict)
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

func (a *App) databaseResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		defer a.invalidateDatabaseInventory()
	}
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
		if !a.requireCustomerScope(w, r, "database:read") {
			return
		}
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
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "database:"+name)
		if lockErr != nil {
			http.Error(w, "database mutation is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "database credential rotation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		if _, err := runDatabaseHelperContext(operationCtx, a.Config, 30*time.Second, in.Password, "rotate", name, in.User); err != nil {
			http.Error(w, "credential rotation failed", http.StatusConflict)
			return
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "database credential rotation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "database.credentials_rotated", name, "user="+in.User)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && !credentialRotation:
		if in.Confirm != "DROP "+name {
			http.Error(w, "confirmation must exactly match DROP "+name, http.StatusUnprocessableEntity)
			return
		}
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "database:"+name)
		if lockErr != nil {
			http.Error(w, "database mutation is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
		safetyBackup, err := createDatabaseSafetyBackupContext(operationCtx, a.Config, name)
		if err != nil {
			log.Printf("database safety backup failed for %s: %v", name, err)
			http.Error(w, "database deletion refused because its safety backup failed", http.StatusServiceUnavailable)
			return
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "database deletion cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		if _, err := runDatabaseHelperContext(operationCtx, a.Config, time.Minute, "", "drop-managed", name, in.User); err != nil {
			http.Error(w, "database deletion failed", http.StatusConflict)
			return
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "database deletion cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "database.deleted", name, "user="+in.User+" safety_backup="+safetyBackup.Path+" sha256="+safetyBackup.SHA256)
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
