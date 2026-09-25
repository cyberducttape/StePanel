package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type StagingRequest struct {
	Source         string `json:"source"`
	Site           string `json:"site"`
	Domain         string `json:"domain"`
	Files          bool   `json:"files"`
	Environment    bool   `json:"environment"`
	Database       bool   `json:"database"`
	SourceDatabase string `json:"source_database,omitempty"`
	TargetDatabase string `json:"target_database,omitempty"`
	TargetUser     string `json:"target_user,omitempty"`
	TargetPassword string `json:"target_password,omitempty"`
	NoIndex        *bool  `json:"no_index,omitempty"`
	BasicAuth      *bool  `json:"basic_auth,omitempty"`
	AuthUser       string `json:"auth_user,omitempty"`
	AuthPassword   string `json:"auth_password,omitempty"`
}
type StagingResult struct {
	Source            string    `json:"source"`
	Site              string    `json:"site"`
	Domain            string    `json:"domain"`
	FilesCopied       bool      `json:"files_copied"`
	EnvironmentCopied bool      `json:"environment_copied"`
	DatabaseCopied    bool      `json:"database_copied"`
	TargetDatabase    string    `json:"target_database,omitempty"`
	SecretsCopied     bool      `json:"secrets_copied"`
	CreatedAt         time.Time `json:"created_at"`
}

func stagingBasicAuthSupported(webserver string) bool {
	return webserver == "caddy" || webserver == "apache"
}

func (a *App) stagingCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	var input StagingRequest
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	input.Source = safeUser(input.Source)
	input.Site = safeUser(input.Site)
	input.Domain = strings.ToLower(strings.TrimSpace(input.Domain))
	if input.Source == "" || input.Site == "" || input.Source == input.Site || !domainPattern.MatchString(input.Domain) {
		http.Error(w, "invalid staging source, site, or domain", 422)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, input.Source, "site is not assigned to this account", 403); !ok {
		return
	}
	targetAccess, okTarget := a.requireSiteAccess(w, r, input.Site, "site is not assigned to this account", 403)
	if !okTarget {
		return
	}
	if !a.Auth.IsAdministrator(r) {
		if a.Domains == nil {
			http.Error(w, "domain ownership must be verified before staging activation", http.StatusConflict)
			return
		}
		verifyCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		verifyErr := a.verifyCustomerDomain(verifyCtx, input.Site, input.Domain)
		cancel()
		if verifyErr != nil {
			http.Error(w, "domain ownership must be verified before staging activation", http.StatusConflict)
			return
		}
	}
	releaseUnlock, lockErr := a.acquireSiteMutationLock(r.Context(), input.Site)
	if lockErr != nil {
		http.Error(w, "site is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	input.SourceDatabase = strings.ToLower(strings.TrimSpace(input.SourceDatabase))
	input.TargetDatabase = strings.ToLower(strings.TrimSpace(input.TargetDatabase))
	input.TargetUser = strings.ToLower(strings.TrimSpace(input.TargetUser))
	if input.Database {
		if a.Config.DBCtl == "" || !validManagedDatabaseIdentifier(input.SourceDatabase, databaseNameLimit(a.Config)) || !validManagedDatabaseIdentifier(input.TargetDatabase, databaseNameLimit(a.Config)) || !validManagedDatabaseIdentifier(input.TargetUser, 32) || input.TargetUser[0] < 'a' || input.TargetUser[0] > 'z' || !validDatabasePassword(input.TargetPassword) {
			http.Error(w, "source_database, target_database, target_user, and target_password are required for database cloning", 422)
			return
		}
		inventory, err := managedDatabaseInventory(a.Config)
		if err != nil {
			http.Error(w, "managed database inventory is unavailable", 503)
			return
		}
		owned := false
		for _, database := range inventory {
			if database.Name == input.SourceDatabase && database.Site == input.Source {
				owned = true
				break
			}
		}
		if !owned {
			http.Error(w, "source database is not registered to the source site", 403)
			return
		}
	}
	source, err := safePath(a.Config.WebRoot, "sites", input.Source, "public")
	if err != nil {
		http.Error(w, "invalid source", 422)
		return
	}
	dest, err := safePath(a.Config.WebRoot, "sites", input.Site, "public")
	if err != nil {
		http.Error(w, "invalid destination", 422)
		return
	}
	if _, err := os.Stat(source); err != nil {
		http.Error(w, "source site does not exist", 422)
		return
	}
	marker, err := safePath(a.Config.WebRoot, "sites", input.Site, ".stepanel-staging-noindex")
	if err != nil {
		http.Error(w, "invalid staging marker path", 422)
		return
	}
	previousMarker, markerErr := os.ReadFile(marker)
	if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		http.Error(w, "could not inspect staging indexing protection", 503)
		return
	}
	markerExisted := markerErr == nil
	var previousEnvironment map[string]environmentValue
	previousEnvironmentExists := false
	if input.Environment && a.Environments != nil {
		a.Environments.mu.RLock()
		if values, exists := a.Environments.values[input.Site]; exists {
			previousEnvironmentExists = true
			previousEnvironment = cloneEnvironmentValues(values)
		}
		a.Environments.mu.RUnlock()
	}
	routeExtension := ".conf"
	if a.Config.WebServer == "caddy" {
		routeExtension = ".caddy"
	}
	routeName := "site-" + input.Site + "-" + strings.ReplaceAll(input.Domain, ".", "_") + routeExtension
	routeApplied := false
	createdDatabase := false
	stateRollback := func() {
		if routeApplied {
			if err := runHelperCommandWithTimeout(r.Context(), a.Config, helperConfigMutationTimeout, a.Config.VHostCtl, "delete", routeName); err != nil {
				log.Printf("staging route cleanup failed for %s: %v", input.Site, err)
			}
		}
		if input.Environment && a.Environments != nil {
			a.Environments.mu.Lock()
			if previousEnvironmentExists {
				a.Environments.values[input.Site] = previousEnvironment
			} else {
				delete(a.Environments.values, input.Site)
			}
			err := a.Environments.persistLocked()
			a.Environments.mu.Unlock()
			if err != nil {
				log.Printf("staging environment rollback failed for %s: %v", input.Site, err)
			}
		}
		if createdDatabase {
			if _, err := runDatabaseHelper(a.Config, time.Minute, "", "drop-managed", input.TargetDatabase, input.TargetUser); err != nil {
				log.Printf("staging database cleanup failed for %s: %v", input.TargetDatabase, err)
			}
		}
		if markerExisted {
			if err := writeAtomic(marker, previousMarker, 0600); err != nil {
				log.Printf("staging marker rollback failed for %s: %v", input.Site, err)
			}
		} else if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("staging marker cleanup failed for %s: %v", input.Site, err)
		}
	}
	stateCommitted := false
	defer func() {
		if !stateCommitted {
			stateRollback()
		}
	}()
	if err := siteHelper(a.Config, "prepare", input.Site); err != nil {
		http.Error(w, "could not prepare staging site", 502)
		return
	}
	noIndex := input.NoIndex == nil || *input.NoIndex
	basicAuth := input.BasicAuth != nil && *input.BasicAuth
	if basicAuth && !stagingBasicAuthSupported(a.Config.WebServer) {
		http.Error(w, "Basic Auth staging protection is currently supported only for Caddy and Apache", http.StatusUnprocessableEntity)
		return
	}
	authHash := ""
	if basicAuth {
		input.AuthUser = strings.TrimSpace(input.AuthUser)
		if len(input.AuthUser) < 1 || len(input.AuthUser) > 64 || strings.ContainsAny(input.AuthUser, "\x00\r\n:") || len(input.AuthPassword) < 12 || len(input.AuthPassword) > 256 || strings.ContainsAny(input.AuthPassword, "\x00\r\n") {
			http.Error(w, "valid Basic Auth username and password are required", 422)
			return
		}
		var err error
		hash, err := bcrypt.GenerateFromPassword([]byte(input.AuthPassword), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "could not hash staging credentials", 500)
			return
		}
		authHash = string(hash)
		input.AuthPassword = ""
	}
	if noIndex {
		if err := writeAtomic(marker, []byte("managed staging noindex\n"), 0600); err != nil {
			http.Error(w, "could not apply staging indexing protection", 503)
			return
		}
	}
	txn, err := BeginSiteTransaction(a.Config.RecoveryRoot, dest, "staging.clone", targetAccess)
	if err != nil {
		http.Error(w, "could not begin staging transaction", 503)
		return
	}
	var ok bool
	defer func() {
		if !ok {
			_ = txn.Rollback()
		}
	}()
	if input.Files {
		if err := copyTree(source, dest); err != nil {
			http.Error(w, "copy staging files: "+err.Error(), 502)
			return
		}
	}
	if input.Environment && a.Environments != nil {
		a.Environments.mu.RLock()
		vars := a.Environments.values[input.Source]
		copied := map[string]environmentValue{}
		for name, value := range vars {
			if !value.Secret {
				copied[name] = value
			}
		}
		a.Environments.mu.RUnlock()
		a.Environments.mu.Lock()
		a.Environments.values[input.Site] = copied
		err = a.Environments.persistLocked()
		a.Environments.mu.Unlock()
		if err != nil {
			http.Error(w, "could not persist staging environment", 503)
			return
		}
	}
	if err := siteHelper(a.Config, "seal", input.Site); err != nil {
		http.Error(w, "could not seal staging site", 502)
		return
	}
	if input.Database {
		createdDatabase, err = cloneManagedDatabaseToStaging(a.Config, input.SourceDatabase, input.TargetDatabase, input.TargetUser, input.TargetPassword, targetAccess)
		if err != nil {
			http.Error(w, "could not clone staging database: "+err.Error(), 502)
			return
		}
	}
	vhostAction := "apply"
	if basicAuth {
		vhostAction = "apply-auth"
	}
	args := []string{vhostAction, input.Site, input.Domain}
	if basicAuth {
		args = append(args, input.AuthUser, authHash)
	}
	if err := runHelperCommandWithTimeout(r.Context(), a.Config, helperConfigMutationTimeout, a.Config.VHostCtl, args...); err != nil {
		http.Error(w, "could not activate staging route", 502)
		return
	}
	routeApplied = true
	if err := txn.Commit(); err != nil {
		http.Error(w, "could not commit staging transaction", 503)
		return
	}
	ok = true
	stateCommitted = true
	result := StagingResult{Source: input.Source, Site: input.Site, Domain: input.Domain, FilesCopied: input.Files, EnvironmentCopied: input.Environment, DatabaseCopied: input.Database, TargetDatabase: input.TargetDatabase, SecretsCopied: false, CreatedAt: time.Now().UTC()}
	recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "staging.created", input.Site, input.Source+" -> "+input.Domain)
	writeJSON(w, 202, result)
}

func cloneManagedDatabaseToStaging(cfg Config, source, target, user, password string, site SiteCapability) (bool, error) {
	root, err := os.MkdirTemp(cfg.ImportRoot, "staging-db-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(root)
	dump := filepath.Join(root, "database.sql")
	if err := dumpManagedDatabase(cfg, source, dump); err != nil {
		return false, fmt.Errorf("dump source database: %w", err)
	}
	encoding := "utf8mb4"
	if cfg.DBEngine == "postgresql" {
		encoding = "UTF8"
	}
	if _, err := runDatabaseHelper(cfg, time.Minute, password, "provision", target, user, site.Site(), encoding); err != nil {
		return false, fmt.Errorf("provision staging database: %w", err)
	}
	file, err := os.Open(dump)
	if err != nil {
		return true, err
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := helperCommandContext(ctx, cfg, cfg.DBCtl, "restore-dump", target, site.Site())
	cmd.Stdin = file
	output, err := runBoundedCommand(ctx, cmd)
	if err != nil {
		return true, fmt.Errorf("import staging database: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return true, nil
}
