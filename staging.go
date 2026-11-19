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
	if err := decodeJSON(w, r, stagingCreateBodyLimit, &input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	result, err := a.createStaging(r.Context(), httpSiteRequester{a, r}, input)
	if err != nil {
		writeSiteOperationError(w, r, err)
		return
	}
	writeJSON(w, 202, result)
}

const stagingCreateBodyLimit = 4096

func validateStagingCreatePayload(body []byte) error {
	var input StagingRequest
	if err := decodeOperationBody(body, stagingCreateBodyLimit, &input); err != nil {
		return errors.New("invalid JSON")
	}
	return nil
}

// executeStagingCreateCommand runs a queued staging clone. The service
// repeats every ownership, scope and domain check against current state.
func executeStagingCreateCommand(a *App, ctx context.Context, command siteOperationCommand, principal siteOperationPrincipal) ([]byte, error) {
	var input StagingRequest
	if err := decodeOperationBody(command.Body, stagingCreateBodyLimit, &input); err != nil || safeUser(input.Site) != command.Site {
		return nil, errors.New("invalid staging command")
	}
	return siteOperationResult(a.createStaging(ctx, durableSiteRequester{a: a, principal: principal, operation: command.Operation}, input))
}

// createStaging clones a source site into a staging site and publishes its
// route. It is the service behind the HTTP handler and the durable worker.
func (a *App) createStaging(ctx context.Context, req siteRequester, input StagingRequest) (*StagingResult, error) {
	input.Source = safeUser(input.Source)
	input.Site = safeUser(input.Site)
	input.Domain = strings.ToLower(strings.TrimSpace(input.Domain))
	if input.Source == "" || input.Site == "" || input.Source == input.Site || isRecoveryProofSiteName(input.Site) || !domainname.Valid(input.Domain) {
		return nil, operationFailure(422, "invalid staging source, site, or domain")
	}
	if _, ok := req.siteAccess(input.Source); !ok {
		return nil, operationFailure(403, "site is not assigned to this account")
	}
	targetAccess, okTarget := req.siteAccess(input.Site)
	if !okTarget {
		return nil, operationFailure(403, "site is not assigned to this account")
	}
	if !req.customerScope("site:deploy") {
		return nil, operationFailure(http.StatusForbidden, "API token lacks the site:deploy scope")
	}
	if !req.administrator() {
		if a.Domains == nil {
			return nil, operationFailure(http.StatusConflict, "domain ownership must be verified before staging activation")
		}
		verifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		verifyErr := a.verifyCustomerDomain(verifyCtx, input.Site, input.Domain)
		cancel()
		if verifyErr != nil {
			return nil, operationFailure(http.StatusConflict, "domain ownership must be verified before staging activation")
		}
	}
	input.SourceDatabase = strings.ToLower(strings.TrimSpace(input.SourceDatabase))
	input.TargetDatabase = strings.ToLower(strings.TrimSpace(input.TargetDatabase))
	input.TargetUser = strings.ToLower(strings.TrimSpace(input.TargetUser))
	lockKeys := []string{input.Source, input.Site}
	if input.Database {
		// Database drops and credential rotations lock only the database
		// name, so a clone must hold those keys to exclude them.
		lockKeys = append(lockKeys, databaseLockKey(input.SourceDatabase), databaseLockKey(input.TargetDatabase))
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLocksContext(ctx, lockKeys...)
	if lockErr != nil {
		return nil, operationFailure(http.StatusConflict, "site is busy")
	}
	defer releaseUnlock()
	if !req.routeStillAuthorized(input.Site, input.Domain) {
		return nil, operationFailure(http.StatusConflict, "site access or domain ownership changed before staging could be activated")
	}
	if input.Database {
		if a.Config.DBCtl == "" || !validManagedDatabaseIdentifier(input.SourceDatabase, databaseNameLimit(a.Config)) || !validManagedDatabaseIdentifier(input.TargetDatabase, databaseNameLimit(a.Config)) || !validManagedDatabaseIdentifier(input.TargetUser, 32) || input.TargetUser[0] < 'a' || input.TargetUser[0] > 'z' || !validDatabasePassword(input.TargetPassword) {
			return nil, operationFailure(422, "source_database, target_database, target_user, and target_password are required for database cloning")
		}
		inventory, err := managedDatabaseInventory(a.Config)
		if err != nil {
			return nil, operationFailure(503, "managed database inventory is unavailable")
		}
		owned := false
		for _, database := range inventory {
			if database.Name == input.SourceDatabase && database.Site == input.Source {
				owned = true
				break
			}
		}
		if !owned {
			return nil, operationFailure(403, "source database is not registered to the source site")
		}
	}
	source, err := siteDocRoot(a.Config, input.Source)
	if err != nil {
		return nil, operationFailure(422, "invalid source")
	}
	dest, err := siteDocRoot(a.Config, input.Site)
	if err != nil {
		return nil, operationFailure(422, "invalid destination")
	}
	if _, err := os.Stat(source); err != nil {
		return nil, operationFailure(422, "source site does not exist")
	}
	wordpressClone := false
	productionDomain := ""
	if input.Files {
		wpConfig, pathErr := safePath(source, "wp-config.php")
		if pathErr != nil {
			return nil, operationFailure(http.StatusServiceUnavailable, "could not safely inspect source WordPress configuration")
		}
		if info, statErr := os.Lstat(wpConfig); statErr == nil {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return nil, operationFailure(http.StatusUnprocessableEntity, "source wp-config.php is not a regular file")
			}
			wordpressClone = true
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, operationFailure(http.StatusServiceUnavailable, "could not inspect source WordPress configuration")
		}
		if wordpressClone && !input.Database {
			return nil, operationFailure(http.StatusUnprocessableEntity, "WordPress file cloning requires a separate staging database; production database sharing is refused")
		}
		if wordpressClone {
			current, readErr := runWordPress(operationCtx, a.Config, rootbroker.WordPressRequest{Action: "option-get", Site: input.Source, Name: "siteurl"})
			parsed, parseErr := url.Parse(strings.TrimSpace(current.Output))
			if readErr != nil || parseErr != nil || !domainname.Valid(parsed.Hostname()) || !managedSiteRouteExists(a.Routes, input.Source, parsed.Hostname()) {
				return nil, operationFailure(http.StatusConflict, "WordPress canonical URL must match an applied managed production domain")
			}
			productionDomain = strings.ToLower(parsed.Hostname())
		}
	}
	marker, err := sitePrivatePath(a.Config, input.Site, ".stepanel-staging-noindex")
	if err != nil {
		return nil, operationFailure(422, "invalid staging marker path")
	}
	previousMarker, markerErr := os.ReadFile(marker)
	if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		return nil, operationFailure(503, "could not inspect staging indexing protection")
	}
	markerExisted := markerErr == nil
	originPath, originPathErr := sitePrivatePath(a.Config, input.Site, stagingOriginMarker)
	if originPathErr != nil {
		return nil, operationFailure(http.StatusUnprocessableEntity, "invalid staging origin metadata path")
	}
	previousOrigin, originErr := os.ReadFile(originPath)
	if originErr != nil && !errors.Is(originErr, os.ErrNotExist) {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not inspect staging origin metadata")
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
			if err := deleteSiteRoute(operationCtx, a.Config, routeName, input.Site, input.Domain); err != nil {
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
		return nil, operationFailure(502, "could not prepare staging site")
	}
	noIndex := input.NoIndex == nil || *input.NoIndex
	if wordpressClone {
		// WordPress staging contains a production data copy. Keep crawlers out
		// even if a caller explicitly asks to disable the ordinary staging flag.
		noIndex = true
	}
	basicAuth := input.BasicAuth != nil && *input.BasicAuth
	if basicAuth && !stagingBasicAuthSupported(a.Config.WebServer) {
		return nil, operationFailure(http.StatusUnprocessableEntity, "Basic Auth staging protection is currently supported only for Caddy and Apache")
	}
	if wordpressClone && !basicAuth {
		return nil, operationFailure(http.StatusUnprocessableEntity, "WordPress staging clones must enable Basic Auth before database-backed customer content is published")
	}
	authHash := ""
	if basicAuth {
		input.AuthUser = strings.TrimSpace(input.AuthUser)
		if len(input.AuthUser) < 1 || len(input.AuthUser) > 64 || strings.ContainsAny(input.AuthUser, "\x00\r\n:") || len(input.AuthPassword) < 12 || len(input.AuthPassword) > 256 || strings.ContainsAny(input.AuthPassword, "\x00\r\n") {
			return nil, operationFailure(422, "valid Basic Auth username and password are required")
		}
		var err error
		hash, err := bcrypt.GenerateFromPassword([]byte(input.AuthPassword), bcrypt.DefaultCost)
		if err != nil {
			return nil, operationFailure(500, "could not hash staging credentials")
		}
		authHash = string(hash)
		input.AuthPassword = ""
	}
	if noIndex {
		if err := writeAtomic(marker, []byte("managed staging noindex\n"), 0600); err != nil {
			return nil, operationFailure(503, "could not apply staging indexing protection")
		}
	}
	txn, err := beginManagedSiteTransaction(operationCtx, a.Config, dest, "staging.clone", targetAccess)
	if err != nil {
		return nil, operationFailure(503, "could not begin staging transaction")
	}
	managerStage, managerStageOwner, err := createSiteManagerStaging(operationCtx, a.Config, input.Site, ".stepanel-staging-")
	if err != nil {
		return nil, operationFailure(503, "could not create lifecycle staging tree")
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
			return nil, operationFailureCause(http.StatusBadGateway, "staging clone", input.Site, "could not copy staging files", err)
		}
	}
	if err := a.activateStagedSite(operationCtx, input.Site, managerStage); err != nil {
		return nil, operationFailure(503, "could not publish staging site through lifecycle manager")
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
			return nil, operationFailure(503, "could not persist staging environment")
		}
	}
	if err := siteHelperContext(operationCtx, a.Config, "seal", input.Site); err != nil {
		return nil, operationFailure(502, "could not seal staging site")
	}
	if input.Database {
		if err := txn.TrackDatabase(ManagedDatabase{Name: input.TargetDatabase, User: input.TargetUser, Kind: "panel"}); err != nil {
			return nil, operationFailure(http.StatusServiceUnavailable, "could not record staging database recovery intent")
		}
		_, err = cloneManagedDatabaseToStagingContext(operationCtx, a.Config, input.SourceDatabase, input.TargetDatabase, input.TargetUser, input.TargetPassword, targetAccess, a.capacityLedger())
		if err != nil {
			return nil, operationFailureCause(http.StatusBadGateway, "staging clone", input.Site, "could not clone staging database", err)
		}
	}
	if wordpressClone {
		if err := configureStagingWordPress(operationCtx, a.Config, input); err != nil {
			return nil, operationFailureCause(http.StatusBadGateway, "staging clone", input.Site, "could not isolate the staging WordPress database and URLs", err)
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
	if err := txn.TrackRoute(routeName, input.Domain); err != nil {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not record staging route recovery intent")
	}
	if err := runVhostMutation(operationCtx, a.Config, vhostAction, args[1:]...); err != nil {
		return nil, operationFailure(502, "could not activate staging route")
	}
	if err := operationCtx.Err(); err != nil {
		return nil, operationFailure(http.StatusConflict, "staging route activation cancelled because the mutation lock was lost")
	}
	routeApplied = true
	if err := txn.MarkRouteApplied(routeName); err != nil {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not record staging route activation")
	}
	if a.Routes != nil {
		if err := a.Routes.save(routeState(routeName, input.Site, input.Domain, "applied")); err != nil {
			return nil, operationFailure(http.StatusServiceUnavailable, "staging route applied but route state could not be persisted")
		}
	}
	if wordpressClone {
		origin := stagingOrigin{ProductionSite: input.Source, ProductionDomain: productionDomain, StagingDomain: input.Domain, ProductionDB: input.SourceDatabase, StagingDB: input.TargetDatabase, StagingUser: input.TargetUser, CreatedAt: time.Now().UTC()}
		encoded, err := json.Marshal(origin)
		if err != nil {
			return nil, operationFailure(http.StatusInternalServerError, "could not encode staging origin")
		}
		if err := writeAtomic(originPath, append(encoded, '\n'), 0600); err != nil {
			return nil, operationFailure(http.StatusServiceUnavailable, "could not persist staging origin for guarded promotion")
		}
	}
	if err := txn.Commit(); err != nil {
		return nil, operationFailure(503, "could not commit staging transaction")
	}
	ok = true
	stateCommitted = true
	if err := operationCtx.Err(); err != nil {
		return nil, operationFailure(http.StatusConflict, "staging creation cancelled because the mutation lock was lost")
	}
	result := StagingResult{Source: input.Source, Site: input.Site, Domain: input.Domain, FilesCopied: input.Files, EnvironmentCopied: input.Environment, DatabaseCopied: input.Database, TargetDatabase: input.TargetDatabase, SecretsCopied: false, CreatedAt: time.Now().UTC(), WordPressPromotionReady: wordpressClone}
	TelemetryAudit(a.Config.AuditLog, req.actor(), "staging.created", input.Site, input.Source+" -> "+input.Domain)
	return &result, nil
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
