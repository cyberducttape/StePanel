package stepanel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/domainname"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

const (
	wordpressPromotionRecordName = ".stepanel-last-promotion.json"
	wordpressConfigReadTimeout   = 30 * time.Second
)

type stagingPromotionRequest struct {
	Source       string `json:"source"`
	Site         string `json:"site"`
	PreviewToken string `json:"preview_token,omitempty"`
}

type promotionPathState struct {
	Exists  bool      `json:"exists"`
	Mode    string    `json:"mode,omitempty"`
	Bytes   int64     `json:"bytes,omitempty"`
	Changed time.Time `json:"changed,omitempty"`
}

type promotionBefore struct {
	Source          string             `json:"source_staging_site"`
	Production      string             `json:"production_site"`
	ProductionRoute string             `json:"production_domain"`
	StagingRoute    string             `json:"staging_domain"`
	StagingDatabase string             `json:"staging_database"`
	ActiveDatabase  string             `json:"active_production_database"`
	StagingFiles    promotionPathState `json:"staging_files"`
	ProductionFiles promotionPathState `json:"production_files"`
}

type promotionPlan struct {
	SourceFiles  string   `json:"source_files"`
	SourceDB     string   `json:"source_database"`
	TargetSite   string   `json:"target_site"`
	TargetDomain string   `json:"production_domain"`
	DatabasePlan string   `json:"database_plan"`
	Effects      []string `json:"effects"`
}

type wordpressPromotionRecord struct {
	SourceSite       string    `json:"source_staging_site"`
	Backup           string    `json:"pre_promotion_backup"`
	PreviousDatabase string    `json:"previous_production_database"`
	ActiveDatabase   string    `json:"promoted_database"`
	CompletedAt      time.Time `json:"completed_at"`
}

func (a *App) stagingPromotionPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var input stagingPromotionRequest
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Source, input.Site = safeUser(input.Source), safeUser(input.Site)
	if input.Source == "" || input.Site == "" || input.Source == input.Site {
		http.Error(w, "invalid staging or production site", http.StatusUnprocessableEntity)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, input.Site, "production site is not assigned to this account", http.StatusForbidden); !ok {
		return
	}
	if _, ok := a.requireSiteAccess(w, r, input.Source, "staging site is not assigned to this account", http.StatusForbidden); !ok {
		return
	}
	if !a.requireCustomerScope(w, r, "site:deploy") {
		return
	}
	before, proposed, err := a.stagingPromotionState(input.Source, input.Site)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	preview, err := a.makeChangePreview("wordpress.staging-promote:"+input.Site, before, proposed,
		[]string{
			"Create and verify a database-inclusive safety backup of the current production site.",
			"Copy staging files into a production filesystem transaction and clone the staging database into a new production-owned database.",
			"Switch WordPress configuration and URLs to the new database and production domain, then verify WP-CLI database access.",
			"Keep the former production database intact for operator-guided rollback; it is not dropped by promotion.",
		},
		[]string{
			"Before promotion, the verified safety backup contains the prior files and database.",
			"If activation fails, the site transaction restores the prior filesystem and removes the candidate database. Incomplete compensation remains journaled for operator recovery.",
			"After success, use the recorded pre-promotion backup and retained previous database for rollback; verify the restored application before deleting either recovery copy.",
		})
	if err != nil {
		http.Error(w, "could not create promotion preview", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (a *App) stagingPromotionState(source, site string) (promotionBefore, promotionPlan, error) {
	var before promotionBefore
	if !validSiteName(source) || !validSiteName(site) || source == site || a.Config.DBEngine == "postgresql" {
		return before, promotionPlan{}, errors.New("WordPress promotion requires distinct managed sites and a MySQL-compatible database")
	}
	markerPath, err := sitePrivatePath(a.Config, source, stagingOriginMarker)
	if err != nil {
		log.Printf("WordPress promotion for %s: resolve staging origin metadata of %s: %v", site, source, err)
		return before, promotionPlan{}, errors.New("staging clone metadata is unavailable")
	}
	data, err := readStateFile(markerPath, 16<<10)
	if err != nil {
		return before, promotionPlan{}, errors.New("staging site has no valid StePanel WordPress clone relationship; create it through Clone to staging first")
	}
	var origin stagingOrigin
	if err := json.Unmarshal(data, &origin); err != nil || origin.ProductionSite != site || origin.StagingDomain == "" || origin.ProductionDomain == "" || !validSiteName(origin.ProductionSite) || !domainname.Valid(origin.StagingDomain) || !domainname.Valid(origin.ProductionDomain) || !validManagedDatabaseIdentifier(origin.StagingDB, databaseNameLimit(a.Config)) || !validManagedDatabaseIdentifier(origin.StagingUser, 32) || !validManagedDatabaseIdentifier(origin.ProductionDB, databaseNameLimit(a.Config)) {
		return before, promotionPlan{}, errors.New("staging clone relationship is invalid or belongs to another production site")
	}
	productionRoot, err := siteDocRoot(a.Config, site)
	if err != nil {
		log.Printf("WordPress promotion for %s: resolve production document root: %v", site, err)
		return before, promotionPlan{}, errors.New("production document root is unavailable")
	}
	stagingRoot, err := siteDocRoot(a.Config, source)
	if err != nil {
		log.Printf("WordPress promotion for %s: resolve staging document root of %s: %v", site, source, err)
		return before, promotionPlan{}, errors.New("staging document root is unavailable")
	}
	productionFiles, err := promotionPathStateAt(productionRoot)
	if err != nil || !productionFiles.Exists {
		return before, promotionPlan{}, errors.New("production WordPress document root is unavailable")
	}
	stagingFiles, err := promotionPathStateAt(stagingRoot)
	if err != nil || !stagingFiles.Exists {
		return before, promotionPlan{}, errors.New("staging WordPress document root is unavailable")
	}
	for _, root := range []string{productionRoot, stagingRoot} {
		configPath, pathErr := safePath(root, "wp-config.php")
		if pathErr != nil {
			return before, promotionPlan{}, errors.New("WordPress configuration path is unsafe")
		}
		info, statErr := os.Lstat(configPath)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return before, promotionPlan{}, errors.New("WordPress configuration is missing or is not a regular file")
		}
	}
	productionDB, err := wordpressConfigValue(a.Config, site, "DB_NAME")
	if err != nil || !validManagedDatabaseIdentifier(productionDB, databaseNameLimit(a.Config)) {
		return before, promotionPlan{}, errors.New("could not determine the active production WordPress database safely")
	}
	stagingDB, err := wordpressConfigValue(a.Config, source, "DB_NAME")
	if err != nil || stagingDB != origin.StagingDB {
		return before, promotionPlan{}, errors.New("staging database configuration no longer matches its recorded clone")
	}
	inventory, err := managedDatabaseInventory(a.Config)
	if err != nil {
		return before, promotionPlan{}, errors.New("managed database ownership inventory is unavailable")
	}
	owned := map[string]string{}
	for _, database := range inventory {
		owned[database.Name] = database.Site
	}
	if owned[productionDB] != site || owned[stagingDB] != source {
		return before, promotionPlan{}, errors.New("production or staging database is not registered to its owning site")
	}
	prodDomain := origin.ProductionDomain
	if !managedSiteRouteExists(a.Routes, site, prodDomain) {
		return before, promotionPlan{}, errors.New("production route changed since this staging clone was created")
	}
	stageDomain := origin.StagingDomain
	if !managedSiteRouteExists(a.Routes, source, stageDomain) {
		return before, promotionPlan{}, errors.New("staging route changed since this clone was created")
	}
	before = promotionBefore{Source: source, Production: site, ProductionRoute: prodDomain, StagingRoute: stageDomain, StagingDatabase: stagingDB, ActiveDatabase: productionDB, StagingFiles: stagingFiles, ProductionFiles: productionFiles}
	proposed := promotionPlan{
		SourceFiles: source + "/public", SourceDB: stagingDB, TargetSite: site, TargetDomain: prodDomain,
		DatabasePlan: "Create a new managed production database, import staging data, switch wp-config.php, and retain the previous production database.",
		Effects:      []string{"Replace production files with the staged copy.", "Promote database content using a new database identity; never overwrite the live production database in place.", "Rewrite site URLs to the existing production HTTPS domain.", "Keep the pre-promotion recovery backup and prior production database until an operator verifies the promoted site."},
	}
	return before, proposed, nil
}

// stagingPromotionLockKeys fences both sites and both databases a promotion
// reads or retains, so drops and rotations of either cannot overlap it.
func stagingPromotionLockKeys(state promotionBefore) []string {
	return []string{state.Production, state.Source, databaseLockKey(state.StagingDatabase), databaseLockKey(state.ActiveDatabase)}
}

func promotionPathStateAt(path string) (promotionPathState, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return promotionPathState{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return promotionPathState{}, errors.New("site root is not a real directory")
	}
	return promotionPathState{Exists: true, Mode: info.Mode().String(), Bytes: info.Size(), Changed: info.ModTime().UTC()}, nil
}

func wordpressConfigValue(cfg Config, site, name string) (string, error) {
	// config-get reads wp-config.php only; bound it so a wedged wp-cli
	// cannot hold the promotion locks or a preview request indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), wordpressConfigReadTimeout)
	defer cancel()
	result, err := runWordPress(ctx, cfg, rootbroker.WordPressRequest{Action: "config-get", Site: site, Name: name})
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(result.Output)
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("WordPress configuration value is empty or malformed")
	}
	return value, nil
}

func (a *App) stagingPromote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var input stagingPromotionRequest
	if err := decodeJSON(w, r, stagingPromotionBodyLimit, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	result, err := a.promoteStaging(r.Context(), httpSiteRequester{a, r}, input)
	if err != nil {
		writeSiteOperationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

const stagingPromotionBodyLimit = 4096

func validateStagingPromotionPayload(body []byte) error {
	var input stagingPromotionRequest
	if err := decodeOperationBody(body, stagingPromotionBodyLimit, &input); err != nil {
		return errors.New("invalid JSON")
	}
	return nil
}

// executeStagingPromotionCommand runs a queued staging promotion. The service
// repeats the ownership, scope and preview checks against current state.
func executeStagingPromotionCommand(a *App, ctx context.Context, command siteOperationCommand, principal siteOperationPrincipal) ([]byte, error) {
	var input stagingPromotionRequest
	if err := decodeOperationBody(command.Body, stagingPromotionBodyLimit, &input); err != nil || safeUser(input.Site) != command.Site {
		return nil, errors.New("invalid staging promotion command")
	}
	return siteOperationResult(a.promoteStaging(ctx, durableSiteRequester{a: a, principal: principal, operation: command.Operation}, input))
}

// promoteStaging replaces a production WordPress site with its verified
// staging copy. It is the service behind the HTTP handler and the worker.
func (a *App) promoteStaging(ctx context.Context, req siteRequester, input stagingPromotionRequest) (map[string]any, error) {
	input.Source, input.Site = safeUser(input.Source), safeUser(input.Site)
	// Validate before any lock is taken: an empty or identical pair must never
	// reach the mutation locks or the promotion state checks.
	if input.Source == "" || input.Site == "" || input.Source == input.Site {
		return nil, operationFailure(http.StatusUnprocessableEntity, "invalid staging or production site")
	}
	if _, ok := req.siteAccess(input.Site); !ok {
		return nil, operationFailure(http.StatusForbidden, "production site is not assigned to this account")
	}
	if _, ok := req.siteAccess(input.Source); !ok {
		return nil, operationFailure(http.StatusForbidden, "staging site is not assigned to this account")
	}
	if !req.customerScope("site:deploy") {
		return nil, operationFailure(http.StatusForbidden, "API token lacks the site:deploy scope")
	}
	// Database drops and credential rotations lock only "database:<name>",
	// so discover both databases first, lock them with the sites, and
	// require the locked names to still be the active ones.
	unlocked, _, err := a.stagingPromotionState(input.Source, input.Site)
	if err != nil {
		return nil, operationFailure(http.StatusConflict, err.Error())
	}
	operationCtx, release, err := a.acquireSiteMutationLocksContext(ctx, stagingPromotionLockKeys(unlocked)...)
	if err != nil {
		return nil, operationFailure(http.StatusConflict, "production or staging site is busy")
	}
	defer release()
	before, proposed, err := a.stagingPromotionState(input.Source, input.Site)
	if err != nil {
		return nil, operationFailure(http.StatusConflict, err.Error())
	}
	if before.StagingDatabase != unlocked.StagingDatabase || before.ActiveDatabase != unlocked.ActiveDatabase {
		return nil, operationFailure(http.StatusConflict, "promotion databases changed while waiting for locks; generate a new preview")
	}
	if err := changePreviewRequired(input.PreviewToken); err != nil || !a.verifyChangePreview(input.PreviewToken, "wordpress.staging-promote:"+input.Site, before, proposed) {
		return nil, operationFailure(http.StatusConflict, "promotion state changed or the preview expired; generate a new preview")
	}
	access, err := a.authorizeDurableSiteJob(input.Site, req.actor(), false)
	if err != nil {
		return nil, operationFailure(http.StatusForbidden, "production site access is no longer active")
	}
	if a.Config.BackupRoot == "" || a.Config.DBCtl == "" || a.Config.WPCLI == "" {
		return nil, operationFailure(http.StatusServiceUnavailable, "verified backup, managed database, and WP-CLI services are required for promotion")
	}
	intent, err := BeginSecurityAudit(a.Config.AuditLog, req.actor(), "wordpress.staging.promoted", input.Site, "staging_site="+input.Source)
	if err != nil {
		return nil, errAuditRefusal
	}
	var outcome string
	defer intent.Finish(&outcome, "promotion did not complete")
	host := a.promotionHost()
	safety, err := host.SafetyBackup(operationCtx, a, access)
	if err == nil {
		err = failureInjection("promote", "backup")
	}
	if err != nil {
		return nil, operationFailureCause(http.StatusServiceUnavailable, "WordPress promotion", input.Site, "promotion refused because the production safety backup failed", err)
	}
	productionRoot, err := siteDocRoot(a.Config, input.Site)
	if err != nil {
		return nil, operationFailure(http.StatusConflict, "production document root is unavailable")
	}
	txn, err := beginManagedSiteTransaction(operationCtx, a.Config, productionRoot, "staging.promote", access)
	if err != nil {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not journal promotion rollback state")
	}
	manager := a.siteManager
	if manager == nil {
		manager, err = newSiteManagerForConfig(a.Config)
	}
	if err != nil || manager == nil {
		rollbackPromotionSetup(txn, input.Site)
		return nil, operationFailure(http.StatusServiceUnavailable, "could not initialize site lifecycle manager")
	}
	stage, stageOwner, err := manager.CreateSiteStagingLease(operationCtx, input.Site, ".stepanel-promote-")
	if err != nil {
		rollbackPromotionSetup(txn, input.Site)
		return nil, operationFailure(http.StatusServiceUnavailable, "could not create production activation staging")
	}
	defer func() {
		if err := manager.ReleaseStagingLease(stage, stageOwner); err != nil {
			log.Printf("release promotion staging lease for %s: %v", input.Site, err)
		}
	}()
	activated, committed := false, false
	var candidateDB, candidateUser string
	privatePath := ""
	previousPromotion, promotionMarkerErr := []byte(nil), error(nil)
	promotionMarkerExisted := false
	markerWritten := false
	defer func() {
		if activated || committed {
			return
		}
		if discardErr := manager.DiscardStaging(context.Background(), stage); discardErr != nil {
			log.Printf("discard production promotion staging for %s: %v", input.Site, discardErr)
		}
	}()
	defer func() {
		if committed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(operationCtx), helperConfigMutationTimeout)
		var compensationErrors []error
		rollbackErr := txn.Rollback()
		if rollbackErr != nil {
			compensationErrors = append(compensationErrors, fmt.Errorf("restore production files: %w", rollbackErr))
			// The live tree may still reference the candidate database. Keep it
			// until journal recovery has restored and verified the old generation.
		} else if err := txn.cleanupDatabases(cleanupCtx, a.Config); err != nil {
			compensationErrors = append(compensationErrors, fmt.Errorf("remove candidate database: %w", err))
		}
		if markerWritten && privatePath != "" {
			if promotionMarkerExisted {
				if err := writeAtomic(privatePath, previousPromotion, 0600); err != nil {
					compensationErrors = append(compensationErrors, fmt.Errorf("restore prior promotion record: %w", err))
				}
			} else if err := os.Remove(privatePath); err != nil && !errors.Is(err, os.ErrNotExist) {
				compensationErrors = append(compensationErrors, fmt.Errorf("remove incomplete promotion record: %w", err))
			}
		}
		if len(compensationErrors) > 0 {
			cause := errors.Join(compensationErrors...)
			log.Printf("WordPress promotion rollback requires operator recovery for %s: %v", input.Site, cause)
			if err := txn.MarkRecoveryRequired(cause); err != nil {
				log.Printf("persist WordPress promotion recovery requirement for %s: %v", input.Site, err)
			}
		}
		cancel()
	}()
	stagingRoot, err := siteDocRoot(a.Config, input.Source)
	if err != nil {
		return nil, operationFailure(http.StatusConflict, "staging document root is unavailable")
	}
	if err := copyTreeContext(operationCtx, stagingRoot, stage); err != nil || failureInjection("promote", "copy") != nil {
		return nil, operationFailureCause(http.StatusBadGateway, "WordPress promotion", input.Site, "could not copy staging files", err)
	}
	if err := writeWordPressMaintenanceMarker(stage); err != nil {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not protect the production cutover with WordPress maintenance mode")
	}
	password, err := randomWordPressDBPassword()
	if err != nil {
		return nil, operationFailure(http.StatusInternalServerError, "could not allocate a new production database credential")
	}
	if candidateDB, candidateUser, err = newPromotionDatabaseIdentity(input.Site); err != nil {
		return nil, operationFailure(http.StatusInternalServerError, "could not allocate a candidate database identity")
	}
	if err := txn.TrackDatabase(ManagedDatabase{Name: candidateDB, User: candidateUser, Kind: "panel"}); err != nil {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not record candidate database recovery intent")
	}
	sourceDB := before.StagingDatabase
	err = host.CloneDatabase(operationCtx, a, sourceDB, candidateDB, candidateUser, password, access)
	if err == nil {
		err = failureInjection("promote", "clone")
	}
	if err != nil {
		return nil, operationFailureCause(http.StatusBadGateway, "WordPress promotion", input.Site, "could not create and validate promoted database", err)
	}
	if err := failureInjection("promote", "activate"); err != nil {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not atomically activate staged production files")
	}
	if err := a.activateStagedSite(operationCtx, input.Site, stage); err != nil {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not atomically activate staged production files")
	}
	activated = true
	if err := siteHelperContext(operationCtx, a.Config, "seal", input.Site); err != nil || failureInjection("promote", "seal") != nil {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not seal promoted production files")
	}
	dbHost := strings.TrimSpace(a.Config.DBHost)
	if dbHost == "" {
		dbHost = "localhost"
	}
	if err := failureInjection("promote", "configure"); err != nil {
		return nil, operationFailure(http.StatusBadGateway, "could not connect promoted files to their isolated database")
	}
	for _, setting := range []struct{ name, value string }{{"DB_NAME", candidateDB}, {"DB_USER", candidateUser}, {"DB_HOST", dbHost}} {
		if _, err := runWordPress(operationCtx, a.Config, rootbroker.WordPressRequest{Action: "config-set", Site: input.Site, Name: setting.name, Value: setting.value}); err != nil {
			return nil, operationFailureCause(http.StatusBadGateway, "WordPress promotion", input.Site, "could not connect promoted files to their isolated database", err)
		}
	}
	if _, err := runWordPress(operationCtx, a.Config, rootbroker.WordPressRequest{Action: "config-set-password", Site: input.Site, Secret: password}); err != nil {
		return nil, operationFailureCause(http.StatusBadGateway, "WordPress promotion", input.Site, "could not configure promoted database credentials", err)
	}
	stagingURL := "https://" + before.StagingRoute
	productionURL := "https://" + before.ProductionRoute
	if _, err := runWordPress(operationCtx, a.Config, rootbroker.WordPressRequest{Action: "search-replace", Site: input.Site, Search: stagingURL, Replace: productionURL}); err != nil {
		return nil, operationFailureCause(http.StatusBadGateway, "WordPress promotion", input.Site, "could not rewrite staged WordPress URLs for production", err)
	}
	for _, option := range []string{"home", "siteurl"} {
		if _, err := runWordPress(operationCtx, a.Config, rootbroker.WordPressRequest{Action: "option-update", Site: input.Site, Name: option, Value: productionURL}); err != nil {
			return nil, operationFailure(http.StatusBadGateway, "could not verify the promoted WordPress URL configuration")
		}
	}
	if _, err := runWordPress(operationCtx, a.Config, rootbroker.WordPressRequest{Action: "db-check", Site: input.Site}); err != nil {
		return nil, operationFailure(http.StatusBadGateway, "promoted WordPress database did not pass WP-CLI database integrity validation")
	}
	activeDB, err := wordpressConfigValue(a.Config, input.Site, "DB_NAME")
	if err != nil || activeDB != candidateDB {
		return nil, operationFailure(http.StatusBadGateway, "promoted WordPress database identity did not verify")
	}
	privatePath, err = sitePrivatePath(a.Config, input.Site, wordpressPromotionRecordName)
	if err != nil {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not resolve promotion recovery metadata path")
	}
	previousPromotion, promotionMarkerErr = readStateFile(privatePath, 16<<10)
	if promotionMarkerErr != nil && !errors.Is(promotionMarkerErr, os.ErrNotExist) {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not inspect the previous promotion recovery record")
	}
	promotionMarkerExisted = promotionMarkerErr == nil
	record := wordpressPromotionRecord{SourceSite: input.Source, Backup: filepath.Base(safety.Path), PreviousDatabase: before.ActiveDatabase, ActiveDatabase: candidateDB, CompletedAt: time.Now().UTC()}
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, operationFailure(http.StatusInternalServerError, "could not encode promotion recovery record")
	}
	if err := writeAtomic(privatePath, append(encoded, '\n'), 0600); err != nil {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not persist promotion recovery record")
	}
	markerWritten = true
	if err := failureInjection("promote", "record"); err != nil {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not persist promotion recovery record")
	}
	if _, err := runWordPress(operationCtx, a.Config, rootbroker.WordPressRequest{Action: "maintenance-deactivate", Site: input.Site}); err != nil {
		return nil, operationFailure(http.StatusBadGateway, "could not remove the production maintenance gate after promotion")
	}
	if err := failureInjection("promote", "commit"); err != nil {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not commit the promotion recovery journal")
	}
	if err := txn.Commit(); err != nil {
		return nil, operationFailure(http.StatusServiceUnavailable, "could not commit the promotion recovery journal")
	}
	committed = true
	if a.MetadataCache != nil {
		a.MetadataCache.InvalidateSite(input.Site)
	}
	outcome = "staging=" + input.Source + " previous_database=" + before.ActiveDatabase + " promoted_database=" + candidateDB + " safety_backup=" + filepath.Base(safety.Path)
	result := map[string]any{"site": input.Site, "source": input.Source, "domain": before.ProductionRoute, "database": candidateDB, "previous_database_retained": before.ActiveDatabase, "safety_backup": filepath.Base(safety.Path), "database_verified": true, "application_verification": "WP-CLI database bootstrap passed; public HTTP response still requires operator validation."}
	return result, nil
}

// stagingPromotionHost is the promotion's boundary to verified backups and
// managed database cloning. Tests replace it to inject failures; production
// uses defaultPromotionHost.
type stagingPromotionHost interface {
	SafetyBackup(ctx context.Context, a *App, access SiteCapability) (BackupResult, error)
	CloneDatabase(ctx context.Context, a *App, source, target, user, password string, access SiteCapability) error
}

type defaultPromotionHost struct{}

func (defaultPromotionHost) SafetyBackup(ctx context.Context, a *App, access SiteCapability) (BackupResult, error) {
	// Production database backups require the durable capacity ledger.
	return createSiteBackupContext(ctx, a.Config, access, true, a.capacityLedger())
}

func (defaultPromotionHost) CloneDatabase(ctx context.Context, a *App, source, target, user, password string, access SiteCapability) error {
	_, err := cloneManagedDatabaseToStagingContext(ctx, a.Config, source, target, user, password, access, a.capacityLedger())
	return err
}

func (a *App) promotionHost() stagingPromotionHost {
	if a.stagingPromotionHost != nil {
		return a.stagingPromotionHost
	}
	return defaultPromotionHost{}
}

// rollbackPromotionSetup restores the journaled production snapshot when
// promotion fails before its compensating rollback is armed.
func rollbackPromotionSetup(txn *SiteTransaction, site string) {
	if err := txn.Rollback(); err != nil {
		log.Printf("WordPress promotion for %s: roll back setup: %v", site, err)
	}
}

// writeOperationFailure answers with public text only. Helper and filesystem
// errors name host paths and tool output, so the cause goes to the log.
func writeOperationFailure(w http.ResponseWriter, status int, operation, site, public string, err error) {
	log.Printf("%s for %s: %s: %v", operation, site, public, err)
	http.Error(w, public+"; an administrator can find the cause in the panel log", status)
}

func newPromotionDatabaseIdentity(site string) (string, string, error) {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", "", err
	}
	short := hex.EncodeToString(suffix[:])
	base := strings.NewReplacer("-", "_").Replace(strings.Trim(safeUser(site), "_-"))
	if len(base) > 14 {
		base = base[:14]
	}
	return "spwp_" + base + "_" + short, "wp_" + base + "_" + short, nil
}

func writeWordPressMaintenanceMarker(rootPath string) error {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(".maintenance"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := root.OpenFile(".maintenance", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString("<?php $upgrading = time(); ?>\n")
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return syncDirectory(rootPath)
}
