package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/cyberducttape/StePanel/internal/domainname"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
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
	Source                  string    `json:"source"`
	Site                    string    `json:"site"`
	Domain                  string    `json:"domain"`
	FilesCopied             bool      `json:"files_copied"`
	EnvironmentCopied       bool      `json:"environment_copied"`
	DatabaseCopied          bool      `json:"database_copied"`
	TargetDatabase          string    `json:"target_database,omitempty"`
	SecretsCopied           bool      `json:"secrets_copied"`
	CreatedAt               time.Time `json:"created_at"`
	WordPressPromotionReady bool      `json:"wordpress_promotion_ready,omitempty"`
}

const stagingOriginMarker = ".stepanel-staging-origin.json"

type stagingOrigin struct {
	ProductionSite   string    `json:"production_site"`
	ProductionDomain string    `json:"production_domain"`
	StagingDomain    string    `json:"staging_domain"`
	ProductionDB     string    `json:"production_database"`
	StagingDB        string    `json:"staging_database"`
	StagingUser      string    `json:"staging_database_user"`
	CreatedAt        time.Time `json:"created_at"`
}

func stagingBasicAuthSupported(webserver string) bool {
	return webserver == "caddy" || webserver == "apache"
}

func managedSiteRouteExists(routes *RouteStore, site, domain string) bool {
	if routes == nil {
		return false
	}
	for _, route := range routes.list() {
		if route.Site == site && route.State == "applied" && strings.EqualFold(route.Domain, domain) {
			return true
		}
	}
	return false
}

func configureStagingWordPress(ctx context.Context, cfg Config, input StagingRequest) error {
	if !validManagedDatabaseIdentifier(input.TargetDatabase, databaseNameLimit(cfg)) || !validManagedDatabaseIdentifier(input.TargetUser, 32) || !validDatabasePassword(input.TargetPassword) {
		return errors.New("staging database identity is invalid")
	}
	dbHost := strings.TrimSpace(cfg.DBHost)
	if dbHost == "" {
		dbHost = "localhost"
	}
	for _, setting := range []struct{ name, value string }{
		{"DB_NAME", input.TargetDatabase},
		{"DB_USER", input.TargetUser},
		{"DB_HOST", dbHost},
	} {
		if _, err := runWordPress(ctx, cfg, rootbroker.WordPressRequest{Action: "config-set", Site: input.Site, Name: setting.name, Value: setting.value}); err != nil {
			return fmt.Errorf("set staging %s: %w", setting.name, err)
		}
	}
	if _, err := runWordPress(ctx, cfg, rootbroker.WordPressRequest{Action: "config-set-password", Site: input.Site, Secret: input.TargetPassword}); err != nil {
		return fmt.Errorf("set staging database password: %w", err)
	}
	stagingURL := "https://" + input.Domain
	for _, option := range []string{"siteurl", "home"} {
		current, err := runWordPress(ctx, cfg, rootbroker.WordPressRequest{Action: "option-get", Site: input.Site, Name: option})
		if err != nil {
			return fmt.Errorf("read staging %s URL: %w", option, err)
		}
		oldURL := strings.TrimSpace(current.Output)
		parsed, parseErr := url.Parse(oldURL)
		if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || !domainname.Valid(parsed.Hostname()) {
			return fmt.Errorf("source WordPress %s URL is invalid", option)
		}
		if oldURL == stagingURL {
			continue
		}
		if _, err := runWordPress(ctx, cfg, rootbroker.WordPressRequest{Action: "search-replace", Site: input.Site, Search: oldURL, Replace: stagingURL}); err != nil {
			return fmt.Errorf("rewrite staging %s URL: %w", option, err)
		}
	}
	if _, err := runWordPress(ctx, cfg, rootbroker.WordPressRequest{Action: "rewrite-flush", Site: input.Site}); err != nil {
		return fmt.Errorf("flush staging rewrite rules: %w", err)
	}
	return nil
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
	if input.Source == "" || input.Site == "" || input.Source == input.Site || isRecoveryProofSiteName(input.Site) || !domainname.Valid(input.Domain) {
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
	if !a.requireCustomerScope(w, r, "site:deploy") {
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
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLocksContext(r.Context(), input.Source, input.Site)
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
	source, err := siteDocRoot(a.Config, input.Source)
	if err != nil {
		http.Error(w, "invalid source", 422)
		return
	}
	dest, err := siteDocRoot(a.Config, input.Site)
	if err != nil {
		http.Error(w, "invalid destination", 422)
		return
	}
	if _, err := os.Stat(source); err != nil {
		http.Error(w, "source site does not exist", 422)
		return
	}
	wordpressClone := false
	productionDomain := ""
	if input.Files {
		wpConfig, pathErr := safePath(source, "wp-config.php")
		if pathErr != nil {
			http.Error(w, "could not safely inspect source WordPress configuration", http.StatusServiceUnavailable)
			return
		}
		if info, statErr := os.Lstat(wpConfig); statErr == nil {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				http.Error(w, "source wp-config.php is not a regular file", http.StatusUnprocessableEntity)
				return
			}
			wordpressClone = true
		} else if !errors.Is(statErr, os.ErrNotExist) {
			http.Error(w, "could not inspect source WordPress configuration", http.StatusServiceUnavailable)
			return
		}
		if wordpressClone && !input.Database {
			http.Error(w, "WordPress file cloning requires a separate staging database; production database sharing is refused", http.StatusUnprocessableEntity)
			return
		}
		if wordpressClone {
			current, readErr := runWordPress(operationCtx, a.Config, rootbroker.WordPressRequest{Action: "option-get", Site: input.Source, Name: "siteurl"})
			parsed, parseErr := url.Parse(strings.TrimSpace(current.Output))
			if readErr != nil || parseErr != nil || !domainname.Valid(parsed.Hostname()) || !managedSiteRouteExists(a.Routes, input.Source, parsed.Hostname()) {
				http.Error(w, "WordPress canonical URL must match an applied managed production domain", http.StatusConflict)
				return
			}
			productionDomain = strings.ToLower(parsed.Hostname())
		}
	}
	marker, err := sitePrivatePath(a.Config, input.Site, ".stepanel-staging-noindex")
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
	originPath, originPathErr := sitePrivatePath(a.Config, input.Site, stagingOriginMarker)
	if originPathErr != nil {
		http.Error(w, "invalid staging origin metadata path", http.StatusUnprocessableEntity)
		return
	}
	previousOrigin, originErr := os.ReadFile(originPath)
	if originErr != nil && !errors.Is(originErr, os.ErrNotExist) {
		http.Error(w, "could not inspect staging origin metadata", http.StatusServiceUnavailable)
		return
	}
	originExisted := originErr == nil
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
	stateRollback := func() {
		if routeApplied {
			if err := runVhostMutation(operationCtx, a.Config, "delete", routeName); err != nil {
				log.Printf("staging route cleanup failed for %s: %v", input.Site, err)
			}
		}
		if input.Environment && a.Environments != nil {
			if err := a.Environments.replaceSiteValues(input.Site, previousEnvironment, previousEnvironmentExists); err != nil {
				log.Printf("staging environment rollback failed for %s: %v", input.Site, err)
			}
		}
		if a.Routes != nil {
			if err := a.Routes.remove(routeName); err != nil {
				log.Printf("staging route state cleanup failed for %s: %v", routeName, err)
			}
		}
		if originExisted {
			if err := writeAtomic(originPath, previousOrigin, 0600); err != nil {
				log.Printf("staging origin metadata rollback failed for %s: %v", input.Site, err)
			}
		} else if err := os.Remove(originPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("staging origin metadata cleanup failed for %s: %v", input.Site, err)
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
	if err := siteHelperContext(operationCtx, a.Config, "prepare", input.Site); err != nil {
		http.Error(w, "could not prepare staging site", 502)
		return
	}
	noIndex := input.NoIndex == nil || *input.NoIndex
	if wordpressClone {
		// WordPress staging contains a production data copy. Keep crawlers out
		// even if a caller explicitly asks to disable the ordinary staging flag.
		noIndex = true
	}
	basicAuth := input.BasicAuth != nil && *input.BasicAuth
	if basicAuth && !stagingBasicAuthSupported(a.Config.WebServer) {
		http.Error(w, "Basic Auth staging protection is currently supported only for Caddy and Apache", http.StatusUnprocessableEntity)
		return
	}
	if wordpressClone && !basicAuth {
		http.Error(w, "WordPress staging clones must enable Basic Auth before database-backed customer content is published", http.StatusUnprocessableEntity)
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
	txn, err := beginManagedSiteTransaction(operationCtx, a.Config, dest, "staging.clone", targetAccess)
	if err != nil {
		http.Error(w, "could not begin staging transaction", 503)
		return
	}
	managerStage, managerStageOwner, err := createSiteManagerStaging(operationCtx, a.Config, input.Site, ".stepanel-staging-")
	if err != nil {
		http.Error(w, "could not create lifecycle staging tree", 503)
		return
	}
	defer func() {
		if err := releaseSiteManagerStagingLease(a.Config, managerStage, managerStageOwner); err != nil {
			log.Printf("release staging-clone lease for %s: %v", input.Site, err)
		}
	}()
	managerActivated := false
	defer func() {
		if !managerActivated {
			logStagingDiscardFailure(managerStage, a.discardSiteStaging(context.Background(), managerStage))
		}
	}()
	var ok bool
	defer func() {
		if !ok {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(operationCtx), helperConfigMutationTimeout)
			if err := txn.cleanupRoutes(cleanupCtx, a.Config); err != nil {
				log.Printf("staging transaction route cleanup failed for %s: %v", input.Site, err)
			}
			if rollbackErr := txn.Rollback(); rollbackErr != nil {
				log.Printf("staging clone rollback failed for transaction %s: %v", txn.ID, rollbackErr)
			}
			if err := txn.cleanupDatabases(cleanupCtx, a.Config); err != nil {
				log.Printf("staging database transaction cleanup failed for %s: %v", input.Site, err)
			}
			cancel()
		}
	}()
	if input.Files {
		if err := copyTreeContext(operationCtx, source, managerStage); err != nil {
			http.Error(w, "copy staging files: "+err.Error(), 502)
			return
		}
	}
	if err := a.activateStagedSite(operationCtx, input.Site, managerStage); err != nil {
		http.Error(w, "could not publish staging site through lifecycle manager", 503)
		return
	}
	managerActivated = true
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
		if err = a.Environments.replaceSiteValues(input.Site, copied, true); err != nil {
			http.Error(w, "could not persist staging environment", 503)
			return
		}
	}
	if err := siteHelperContext(operationCtx, a.Config, "seal", input.Site); err != nil {
		http.Error(w, "could not seal staging site", 502)
		return
	}
	if input.Database {
		if err := txn.TrackDatabase(ManagedDatabase{Name: input.TargetDatabase, User: input.TargetUser, Kind: "panel"}); err != nil {
			http.Error(w, "could not record staging database recovery intent", http.StatusServiceUnavailable)
			return
		}
		_, err = cloneManagedDatabaseToStagingContext(operationCtx, a.Config, input.SourceDatabase, input.TargetDatabase, input.TargetUser, input.TargetPassword, targetAccess, a.capacityLedger())
		if err != nil {
			http.Error(w, "could not clone staging database: "+err.Error(), 502)
			return
		}
	}
	if wordpressClone {
		if err := configureStagingWordPress(operationCtx, a.Config, input); err != nil {
			http.Error(w, "could not isolate the staging WordPress database and URLs: "+err.Error(), http.StatusBadGateway)
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
	if err := txn.TrackRoute(routeName); err != nil {
		http.Error(w, "could not record staging route recovery intent", http.StatusServiceUnavailable)
		return
	}
	if err := runVhostMutation(operationCtx, a.Config, vhostAction, args[1:]...); err != nil {
		http.Error(w, "could not activate staging route", 502)
		return
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "staging route activation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	routeApplied = true
	if err := txn.MarkRouteApplied(routeName); err != nil {
		http.Error(w, "could not record staging route activation", http.StatusServiceUnavailable)
		return
	}
	if a.Routes != nil {
		if err := a.Routes.save(routeState(routeName, input.Site, input.Domain, "applied")); err != nil {
			http.Error(w, "staging route applied but route state could not be persisted", http.StatusServiceUnavailable)
			return
		}
	}
	if wordpressClone {
		origin := stagingOrigin{ProductionSite: input.Source, ProductionDomain: productionDomain, StagingDomain: input.Domain, ProductionDB: input.SourceDatabase, StagingDB: input.TargetDatabase, StagingUser: input.TargetUser, CreatedAt: time.Now().UTC()}
		encoded, err := json.Marshal(origin)
		if err != nil {
			http.Error(w, "could not encode staging origin", http.StatusInternalServerError)
			return
		}
		if err := writeAtomic(originPath, append(encoded, '\n'), 0600); err != nil {
			http.Error(w, "could not persist staging origin for guarded promotion", http.StatusServiceUnavailable)
			return
		}
	}
	if err := txn.Commit(); err != nil {
		http.Error(w, "could not commit staging transaction", 503)
		return
	}
	ok = true
	stateCommitted = true
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "staging creation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	result := StagingResult{Source: input.Source, Site: input.Site, Domain: input.Domain, FilesCopied: input.Files, EnvironmentCopied: input.Environment, DatabaseCopied: input.Database, TargetDatabase: input.TargetDatabase, SecretsCopied: false, CreatedAt: time.Now().UTC(), WordPressPromotionReady: wordpressClone}
	TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "staging.created", input.Site, input.Source+" -> "+input.Domain)
	writeJSON(w, 202, result)
}

func cloneManagedDatabaseToStagingContext(ctx context.Context, cfg Config, source, target, user, password string, site SiteCapability, ledger *capacityLedger) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	root, stageLock, err := createImportStage(cfg.ImportRoot, "staging-db-")
	if err != nil {
		return false, err
	}
	defer removeImportStage(root, stageLock)
	var reservation *capacityReservation
	if ledger != nil {
		reservation, err = ledger.reserve(cfg, "backup", root, []capacityDemand{{Path: root, Bytes: backupDatabasePeakReservation(backupDatabaseReservation)}})
		if err != nil {
			return false, err
		}
		defer reservation.release()
	} else if cfg.Production {
		return false, errors.New("production staging database clones require the durable capacity ledger")
	}
	dump := filepath.Join(root, "database.sql")
	if _, err := dumpManagedDatabaseContext(ctx, cfg, source, dump, reservation, backupDatabaseReservation); err != nil {
		return false, fmt.Errorf("dump source database: %w", err)
	}
	encoding := "utf8mb4"
	if cfg.DBEngine == "postgresql" {
		encoding = "UTF8"
	}
	if _, err := runDatabaseHelperContext(ctx, cfg, time.Minute, password, "provision", target, user, site.Site(), encoding); err != nil {
		return false, fmt.Errorf("provision staging database: %w", err)
	}
	file, err := os.Open(dump)
	if err != nil {
		return true, err
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if err := runDatabaseRestoreFromPath(ctx, cfg, "restore-dump", site.Site(), target, "restore", "", dump, ledger); err != nil {
		return true, fmt.Errorf("import staging database: %w", err)
	}
	return true, nil
}
