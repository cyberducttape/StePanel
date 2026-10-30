package stepanel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/domainname"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

// Site creation publishes a new, isolated site from a template. It follows
// the lifecycle contract in docs/V1_PRODUCTION_GATES.md (Gate 1): the
// template is written into SiteManager staging, the root broker creates the
// site account and PHP-FPM pool (stepanel-sitectl prepare), SiteManager
// activates the staged tree, and the broker seals ownership. A SiteTransaction
// journal makes an interrupted creation roll back on startup.
//
// Blank PHP creation leaves routing to POST /api/sites/deploy. WordPress
// creation is an end-to-end durable workflow and also provisions a managed
// database, publishes the domain route, configures TLS, and records a first
// database-inclusive recovery point.

// siteTemplate fills a staged public root for a new site.
type siteTemplate struct {
	description string
	populate    func(ctx context.Context, a *App, site, staging string) error
}

var siteTemplates = map[string]siteTemplate{
	"php":       {description: "blank PHP site", populate: populateBlankPHPSite},
	"wordpress": {description: "new WordPress site", populate: populateEmptyWordPressSite},
}

var wordpressAdminUserPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,60}$`)

func populateEmptyWordPressSite(_ context.Context, _ *App, _ string, _ string) error { return nil }

// blankPHPIndex is the placeholder page of a blank PHP site. It shows that
// PHP is executing for the site and names no product or domain.
const blankPHPIndex = `<?php
// Placeholder page created with the site. Replace it with your application.
$phpVersion = htmlspecialchars(PHP_VERSION, ENT_QUOTES, 'UTF-8');
?>
<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Site ready</title></head>
<body>
<h1>This site is ready</h1>
<p>PHP <?= $phpVersion ?> is serving this page. Upload your application to replace it.</p>
</body>
</html>
`

func populateBlankPHPSite(_ context.Context, _ *App, _ string, staging string) error {
	root, err := os.OpenRoot(staging)
	if err != nil {
		return fmt.Errorf("open site staging: %w", err)
	}
	defer root.Close()
	file, err := root.OpenFile("index.php", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create index.php: %w", err)
	}
	if _, err := file.WriteString(blankPHPIndex); err != nil {
		_ = file.Close()
		return fmt.Errorf("write index.php: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync index.php: %w", err)
	}
	return file.Close()
}

func wordpressDatabaseName(site string) string {
	digest := sha256.Sum256([]byte(site))
	stem := strings.NewReplacer("-", "_").Replace(strings.Trim(safeUser(site), "_-"))
	return "spwp_" + stem + "_" + hex.EncodeToString(digest[:8])
}

func wordpressDatabaseUser(site string) string {
	digest := sha256.Sum256([]byte(site))
	stem := strings.NewReplacer("-", "_").Replace(strings.Trim(safeUser(site), "_-"))
	if len(stem) > 12 {
		stem = stem[:12]
	}
	return "wp_" + stem + "_" + hex.EncodeToString(digest[:8])
}

func randomWordPressDBPassword() (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#%^*_=+.,:-"
	var entropy [40]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	password := make([]byte, len(entropy))
	for i, value := range entropy {
		password[i] = alphabet[int(value)%len(alphabet)]
	}
	return string(password), nil
}

func (a *App) provisionWordPressOnNewSite(ctx context.Context, req durableSiteCreationRequest, _ SiteCapability, txn *SiteTransaction) error {
	if req.WordPress == nil || txn == nil {
		return errors.New("WordPress provisioning intent is missing")
	}
	if a.Config.DBEngine == "postgresql" {
		return errors.New("new WordPress provisioning currently requires MySQL or MariaDB")
	}
	database, username := wordpressDatabaseName(req.Site), wordpressDatabaseUser(req.Site)
	if err := txn.TrackDatabase(ManagedDatabase{Name: database, User: username, Kind: "panel"}); err != nil {
		return fmt.Errorf("record WordPress database recovery intent: %w", err)
	}
	password, err := randomWordPressDBPassword()
	if err != nil {
		return fmt.Errorf("generate WordPress database credential: %w", err)
	}
	if _, err := runDatabaseHelperContext(ctx, a.Config, time.Minute, password, "provision", database, username, req.Site, "utf8mb4"); err != nil {
		return fmt.Errorf("create isolated WordPress database and user: %w", err)
	}
	wp := func(action string, extra rootbroker.WordPressRequest) error {
		extra.Action, extra.Site = action, req.Site
		if _, err := runWordPress(ctx, a.Config, extra); err != nil {
			return fmt.Errorf("WordPress %s: %w", action, err)
		}
		return nil
	}
	if err := wp("core-download", rootbroker.WordPressRequest{}); err != nil {
		return err
	}
	dbHost := strings.TrimSpace(a.Config.DBHost)
	if dbHost == "" {
		dbHost = "localhost"
	}
	if err := wp("config-create", rootbroker.WordPressRequest{DBName: database, DBUser: username, DBHost: dbHost, DBPrefix: "wp_", Secret: password}); err != nil {
		return fmt.Errorf("write protected WordPress database configuration: %w", err)
	}
	for _, setting := range []struct{ name, value string }{
		{"DISALLOW_FILE_EDIT", "true"},
		{"FORCE_SSL_ADMIN", "true"},
		{"WP_AUTO_UPDATE_CORE", "true"},
		{"WP_DEBUG", "false"},
		{"WP_DEBUG_DISPLAY", "false"},
		{"WP_ENVIRONMENT_TYPE", "production"},
	} {
		if err := wp("config-set", rootbroker.WordPressRequest{Name: setting.name, Value: setting.value}); err != nil {
			return fmt.Errorf("apply WordPress security setting %s: %w", setting.name, err)
		}
	}
	if _, err := runWordPress(ctx, a.Config, rootbroker.WordPressRequest{Action: "core-install", Site: req.Site, URL: "https://" + req.WordPress.Domain, Title: req.WordPress.Title, AdminUser: req.WordPress.AdminUser, AdminEmail: req.WordPress.AdminEmail, Secret: req.WordPress.AdminPassword}); err != nil {
		return fmt.Errorf("install WordPress: %w", err)
	}
	return nil
}

func (a *App) publishWordPressRoute(ctx context.Context, req durableSiteCreationRequest, txn *SiteTransaction) (string, error) {
	name := siteVHostConfigName(a.Config.WebServer, req.Site, req.WordPress.Domain)
	if err := txn.TrackRoute(name); err != nil {
		return "", fmt.Errorf("record WordPress route recovery intent: %w", err)
	}
	if err := runVhostMutation(ctx, a.Config, "apply", req.Site, req.WordPress.Domain); err != nil {
		return "", fmt.Errorf("publish WordPress HTTPS route: %w", err)
	}
	if err := txn.MarkRouteApplied(name); err != nil {
		return "", fmt.Errorf("record WordPress route activation: %w", err)
	}
	if a.Routes != nil {
		if err := a.Routes.save(routeState(name, req.Site, req.WordPress.Domain, "applied")); err != nil {
			return "", fmt.Errorf("persist WordPress route state: %w", err)
		}
	}
	if a.Config.WebServer == "caddy" {
		return "Caddy automatic HTTPS is configured; issuance requires public DNS and reachable ACME ports 80/443.", nil
	}
	client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", a.Config.WebRoot)
	if err != nil {
		return "", fmt.Errorf("configure certificate broker: %w", err)
	}
	response, err := client.IssueCertificate(ctx, req.WordPress.Domain, req.WordPress.AdminEmail)
	if err != nil {
		return "", fmt.Errorf("request Apache TLS certificate: %w", err)
	}
	if !response.OK {
		return "", fmt.Errorf("Apache TLS certificate issuance failed: %s", response.Error)
	}
	return "Apache certificate issued and HTTP redirects to HTTPS.", nil
}

// siteCreationTransactionKind marks recovery journals of site creation, whose
// rollback also removes the prepared site account (startup_recovery.go).
const siteCreationTransactionKind = "site.create"

type durableSiteCreationRequest struct {
	Site                 string `json:"site"`
	Template             string `json:"template"`
	Actor                string `json:"actor"`
	CustomerProvisioning bool   `json:"customer_provisioning,omitempty"`
	// Account, Kind and Directory place the site inside a hosting account's
	// home (ADR-0007). Empty means the legacy per-site layout, which only
	// runs without a control-plane placement database (unit tests, tooling).
	Account   string                     `json:"account,omitempty"`
	Kind      string                     `json:"kind,omitempty"`
	Directory string                     `json:"directory,omitempty"`
	WordPress *wordpressProvisionRequest `json:"wordpress,omitempty"`
}

type wordpressProvisionRequest struct {
	Domain        string `json:"domain"`
	Title         string `json:"title"`
	AdminUser     string `json:"admin_user"`
	AdminEmail    string `json:"admin_email"`
	AdminPassword string `json:"admin_password"`
}

func (req durableSiteCreationRequest) placement() (sitelayout.Placement, bool) {
	if req.Account == "" {
		return sitelayout.Placement{}, false
	}
	return sitelayout.Placement{Site: req.Site, Account: req.Account, UnixUser: req.Account, Kind: sitelayout.Kind(req.Kind), Directory: req.Directory}, true
}

// SiteCreationResult is the output of a completed site.create job.
type SiteCreationResult struct {
	Site               string    `json:"site"`
	Template           string    `json:"template"`
	PublicRoot         string    `json:"public_root"`
	NextSteps          []string  `json:"next_steps"`
	Database           string    `json:"database,omitempty"`
	Domain             string    `json:"domain,omitempty"`
	HTTPS              string    `json:"https,omitempty"`
	RecoveryPoint      string    `json:"recovery_point,omitempty"`
	RecoveryVerifiedAt time.Time `json:"recovery_verified_at,omitempty"`
	RecoverySHA256     string    `json:"recovery_sha256,omitempty"`
	RecoveryEvidence   string    `json:"recovery_evidence,omitempty"`
	BackupSchedule     string    `json:"backup_schedule,omitempty"`
}

// sites serves GET (list) and POST (create) on /api/sites.
func (a *App) sites(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		a.siteCreate(w, r)
		return
	}
	a.siteList(w, r)
}

func (a *App) siteCreate(w http.ResponseWriter, r *http.Request) {
	if !a.Auth.CSRF(r) {
		http.Error(w, "CSRF request required", http.StatusForbidden)
		return
	}
	admin := a.Auth.IsAdministrator(r)
	actor := a.Auth.UsernameForRequest(r)
	if !admin {
		if a.Accounts == nil || !a.Auth.HasRequiredCustomerScope(r, "site:deploy") {
			http.Error(w, "site creation is not permitted for this account", http.StatusForbidden)
			return
		}
		account, ok := a.Accounts.Get(actor)
		if !ok || accountRole(account) != "owner" || a.Accounts.TenantSuspended(actor) {
			http.Error(w, "only an active tenant owner may create sites", http.StatusForbidden)
			return
		}
	}
	operationKey, err := requestOperationKey(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	var input struct {
		Site     string `json:"site"`
		Template string `json:"template"`
		// Account, Kind and Domain choose where the site lives (ADR-0007).
		Account       string `json:"account"`
		Kind          string `json:"kind"`
		Domain        string `json:"domain"`
		Title         string `json:"title"`
		AdminUser     string `json:"admin_user"`
		AdminEmail    string `json:"admin_email"`
		AdminPassword string `json:"admin_password"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Site = strings.TrimSpace(input.Site)
	if !validSiteName(input.Site) || safeUser(input.Site) != input.Site {
		http.Error(w, "invalid site name", http.StatusUnprocessableEntity)
		return
	}
	if input.Template == "" {
		input.Template = "php"
	}
	if _, ok := siteTemplates[input.Template]; !ok {
		http.Error(w, "unsupported site template", http.StatusUnprocessableEntity)
		return
	}
	var wordpress *wordpressProvisionRequest
	if input.Template == "wordpress" {
		input.Domain = strings.ToLower(strings.TrimSpace(input.Domain))
		input.Title = strings.TrimSpace(input.Title)
		input.AdminUser = strings.TrimSpace(input.AdminUser)
		input.AdminEmail = strings.TrimSpace(input.AdminEmail)
		address, addressErr := mail.ParseAddress(input.AdminEmail)
		if !domainname.Valid(input.Domain) || input.Title == "" || len(input.Title) > 150 || strings.ContainsAny(input.Title, "\x00\r\n") || !wordpressAdminUserPattern.MatchString(input.AdminUser) || addressErr != nil || address.Address != input.AdminEmail || len(input.AdminEmail) > 254 || len(input.AdminPassword) < 20 || len(input.AdminPassword) > 128 || strings.ContainsAny(input.AdminPassword, "\x00\r\n") {
			http.Error(w, "WordPress requires a valid domain, title, administrator email and username, and a 20-128 character administrator password", http.StatusUnprocessableEntity)
			return
		}
		if a.Config.DBCtl == "" || a.Config.WPCLI == "" || a.Config.BackupRoot == "" || (a.Config.WebServer != "caddy" && a.Config.WebServer != "apache") || a.Config.DBEngine == "postgresql" {
			http.Error(w, "WordPress provisioning requires the managed database helper, WP-CLI, and a configured backup root", http.StatusServiceUnavailable)
			return
		}
		if a.Config.WebServer == "apache" && a.Config.Certbot == "" {
			http.Error(w, "Apache WordPress provisioning requires the managed Certbot helper", http.StatusServiceUnavailable)
			return
		}
		if a.Routes == nil {
			http.Error(w, "WordPress provisioning requires durable route state", http.StatusServiceUnavailable)
			return
		}
		if !admin {
			verifyCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			verifyErr := a.verifyCustomerDomain(verifyCtx, input.Site, input.Domain)
			cancel()
			if verifyErr != nil {
				http.Error(w, "verify the site's domain ownership before WordPress provisioning", http.StatusConflict)
				return
			}
		}
		wordpress = &wordpressProvisionRequest{Domain: input.Domain, Title: input.Title, AdminUser: input.AdminUser, AdminEmail: input.AdminEmail, AdminPassword: input.AdminPassword}
		input.AdminPassword = ""
	}
	if a.Jobs == nil {
		http.Error(w, "site creation requires the durable job store", http.StatusServiceUnavailable)
		return
	}
	if exists, err := a.siteExists(input.Site); err != nil {
		http.Error(w, "site state is unavailable", http.StatusServiceUnavailable)
		return
	} else if exists {
		http.Error(w, "site already exists", http.StatusConflict)
		return
	}
	// New sites live in a hosting account's home: an administrator names the
	// account (or the site name becomes a new account); a customer's sites go
	// into their tenant owner's account.
	var placement sitelayout.Placement
	placed := false
	if db := sitePlacementDB.Load(); db != nil {
		account := strings.TrimSpace(input.Account)
		if !admin {
			owner, _ := a.Accounts.Get(actor)
			account = accountTenantID(owner)
		} else if account == "" {
			account = input.Site
		}
		placement, err = resolveSitePlacement(db, input.Site, sitePlacementRequest{Account: account, Kind: sitelayout.Kind(strings.TrimSpace(input.Kind)), Domain: input.Domain})
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		placed = true
	}
	assigned := false
	if !admin {
		if _, newlyAssigned, err := a.Accounts.EnsureSiteAssigned(actor, input.Site); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		} else {
			assigned = newlyAssigned
		}
	}
	request := durableSiteCreationRequest{Site: input.Site, Template: input.Template, Actor: a.Auth.AuditActor(r), CustomerProvisioning: !admin, WordPress: wordpress}
	if placed {
		request.Account, request.Kind, request.Directory = placement.Account, string(placement.Kind), placement.Directory
	}
	payload, err := json.Marshal(request)
	if err != nil {
		if assigned {
			a.releaseSiteReservation(actor, input.Site)
		}
		http.Error(w, "could not encode site creation job", http.StatusInternalServerError)
		return
	}
	job, _, err := a.Jobs.EnqueueIdempotent("site.create", input.Site, operationKey, payload, 3)
	if err != nil {
		if assigned {
			a.releaseSiteReservation(actor, input.Site)
		}
		http.Error(w, "could not persist site creation job", http.StatusInternalServerError)
		return
	}
	response := map[string]string{"job_id": job.ID, "site": input.Site, "template": input.Template, "status_url": "/api/jobs/" + job.ID}
	if placed {
		response["account"], response["kind"], response["document_root"] = placement.Account, string(placement.Kind), placement.DocRoot(a.Config.HomeRoot)
	}
	writeJSON(w, http.StatusAccepted, response)
}

// releaseSiteReservation undoes a tenant reservation made for a request that
// could not queue its creation job. The client already receives an error; a
// failed release leaves the plan slot held, so it is logged for the operator.
func (a *App) releaseSiteReservation(actor, site string) {
	if err := a.Accounts.UnassignSite(actor, site); err != nil {
		log.Printf("release site creation reservation for %s/%s: %v", actor, site, err)
	}
}

// siteExists reports whether the site's root directory exists. The public
// tree is not enough: a site whose public tree is missing still owns its
// account, PHP pool and state.
func (a *App) siteExists(site string) (bool, error) {
	// A placed site exists once its document root is published; its
	// placement alone is recorded before creation (ADR-0007).
	location, err := siteLocationFor(a.Config, site)
	if err != nil {
		return false, err
	}
	root := location.Home
	if location.Placed {
		root = location.DocRoot
	}
	if _, err := os.Lstat(root); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return false, nil
}

func (a *App) handleSiteCreation(ctx context.Context, item Job) ([]byte, error) {
	var req durableSiteCreationRequest
	if err := json.Unmarshal(item.Payload, &req); err != nil {
		return nil, fmt.Errorf("decode site creation request: %w", err)
	}
	terminalAttempt := item.MaxAttempts > 0 && item.Attempts+1 >= item.MaxAttempts
	creationCompleted := false
	defer func() {
		if creationCompleted || !terminalAttempt || !req.CustomerProvisioning || a.Accounts == nil {
			return
		}
		// Retries with a new Idempotency-Key reuse the tenant's reservation
		// and can queue a second job for the same site. When that job fails
		// because another job created the site, the reservation now belongs to
		// a live site and must stay.
		if exists, err := a.siteExists(req.Site); err != nil || exists {
			if err != nil {
				log.Printf("keep site creation reservation for %s/%s: inspect site: %v", req.Actor, req.Site, err)
			}
			return
		}
		if err := a.Accounts.UnassignSite(req.Actor, req.Site); err != nil {
			log.Printf("release site creation reservation for %s/%s: %v", req.Actor, req.Site, err)
		}
	}()
	if !validSiteName(req.Site) || safeUser(req.Site) != req.Site {
		return nil, errors.New("invalid site name in site creation payload")
	}
	template, ok := siteTemplates[req.Template]
	if !ok {
		return nil, fmt.Errorf("unsupported site template %q", req.Template)
	}
	if a.Config.WebRoot == "" {
		return nil, errors.New("site creation requires a configured web root")
	}
	access, err := a.authorizeDurableSiteJob(req.Site, req.Actor, false)
	if err != nil {
		return nil, fmt.Errorf("site creation authorization: %w", err)
	}
	var customerAccount HostingAccount
	lockKeys := []string{req.Site}
	if req.WordPress != nil {
		address, addressErr := mail.ParseAddress(req.WordPress.AdminEmail)
		if req.Template != "wordpress" || !domainname.Valid(req.WordPress.Domain) || strings.TrimSpace(req.WordPress.Title) == "" || len(req.WordPress.Title) > 150 || strings.ContainsAny(req.WordPress.Title, "\x00\r\n") || !wordpressAdminUserPattern.MatchString(req.WordPress.AdminUser) || addressErr != nil || address.Address != req.WordPress.AdminEmail || len(req.WordPress.AdminPassword) < 20 || len(req.WordPress.AdminPassword) > 128 || strings.ContainsAny(req.WordPress.AdminPassword, "\x00\r\n") {
			return nil, errors.New("invalid durable WordPress provisioning request")
		}
		if a.Config.WebServer != "caddy" && a.Config.WebServer != "apache" {
			return nil, errors.New("WordPress provisioning currently requires Caddy or Apache")
		}
		lockKeys = append(lockKeys, "vhost:"+siteVHostConfigName(a.Config.WebServer, req.Site, req.WordPress.Domain))
	} else if req.Template == "wordpress" {
		return nil, errors.New("WordPress provisioning details are missing")
	}
	operationCtx, release, err := a.acquireSiteMutationLocksContext(ctx, lockKeys...)
	if err != nil {
		return nil, fmt.Errorf("acquire site mutation lock: %w", err)
	}
	defer release()
	if req.CustomerProvisioning {
		var found bool
		if a.Accounts == nil {
			return nil, errors.New("customer account state is unavailable")
		}
		customerAccount, found = a.Accounts.Get(req.Actor)
		if !found || !a.Accounts.OwnsSite(req.Actor, req.Site) || a.Accounts.TenantSuspended(req.Actor) {
			return nil, errors.New("customer site assignment is no longer active")
		}
	}
	if exists, err := a.siteExists(req.Site); err != nil {
		return nil, fmt.Errorf("inspect site: %w", err)
	} else if exists {
		return nil, fmt.Errorf("site %q already exists", req.Site)
	}
	if req.WordPress != nil && a.Routes != nil {
		for _, route := range a.Routes.list() {
			if strings.EqualFold(route.Domain, req.WordPress.Domain) {
				return nil, fmt.Errorf("domain %q is already assigned to a managed site", req.WordPress.Domain)
			}
		}
	}
	// Place the site in its hosting account before staging, so its stage is
	// allocated on the home filesystem (ADR-0007).
	placement, placed := req.placement()
	placedByJob := false
	if placed {
		db := sitePlacementDB.Load()
		if db == nil {
			return nil, errors.New("hosting-account placement requires the control-plane database")
		}
		if placedByJob, err = placeSiteInAccount(operationCtx, a.Config, db, placement); err != nil {
			return nil, fmt.Errorf("place site in hosting account: %w", err)
		}
		// Registered before the transaction's cleanup, so it runs after the
		// partially created site tree is gone.
		defer func() {
			if placedByJob && !creationCompleted {
				if err := unplaceSite(context.WithoutCancel(ctx), a.Config, db, req.Site); err != nil {
					log.Printf("forget placement of partially created site %s: %v", req.Site, err)
				}
			}
		}()
	}
	canonical, err := siteDocRoot(a.Config, req.Site)
	if err != nil {
		return nil, fmt.Errorf("resolve site root: %w", err)
	}

	intent, err := BeginSecurityAudit(a.Config.AuditLog, req.Actor, "site.created", req.Site, "template="+req.Template)
	if err != nil {
		return nil, err
	}
	var outcome string
	defer intent.Finish(&outcome, "site was not created")

	manager := a.siteManager
	if manager == nil {
		manager, err = newSiteManagerForConfig(a.Config)
		if err != nil {
			return nil, fmt.Errorf("initialize site manager: %w", err)
		}
	}
	staging, stagingOwner, err := manager.CreateSiteStagingLease(operationCtx, req.Site, ".stepanel-create-")
	if err != nil {
		return nil, fmt.Errorf("create site staging: %w", err)
	}
	defer func() {
		if err := manager.ReleaseStagingLease(staging, stagingOwner); err != nil {
			log.Printf("release site-creation staging lease for %s: %v", req.Site, err)
		}
	}()
	activated := false
	defer func() {
		if !activated {
			if err := manager.DiscardStaging(context.Background(), staging); err != nil {
				log.Printf("discard site creation staging for %s: %v", req.Site, err)
			}
		}
	}()
	if err := template.populate(operationCtx, a, req.Site, staging); err != nil {
		return nil, fmt.Errorf("populate %s: %w", template.description, err)
	}

	txn, err := beginManagedSiteTransaction(operationCtx, a.Config, canonical, siteCreationTransactionKind, access)
	if err != nil {
		return nil, fmt.Errorf("begin site creation transaction: %w", err)
	}
	committed, prepared := false, false
	createdResourceProfile := false
	backupScheduleCreated := false
	defer func() {
		if committed {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(operationCtx), helperConfigMutationTimeout)
		var compensationErrors []error
		if err := txn.cleanupRoutes(cleanupCtx, a.Config); err != nil {
			compensationErrors = append(compensationErrors, fmt.Errorf("remove newly published route: %w", err))
		}
		if err := txn.Rollback(); err != nil {
			compensationErrors = append(compensationErrors, fmt.Errorf("restore site filesystem: %w", err))
		}
		if req.WordPress != nil && a.Routes != nil {
			name := siteVHostConfigName(a.Config.WebServer, req.Site, req.WordPress.Domain)
			if err := a.Routes.remove(name); err != nil {
				compensationErrors = append(compensationErrors, fmt.Errorf("remove route state %s: %w", name, err))
			}
		}
		if backupScheduleCreated && a.Schedules != nil {
			a.Schedules.mu.Lock()
			if removeErr := persistMapKeyChange(a.Schedules.items, req.Site, (*BackupSchedule)(nil), a.Schedules.persistLocked); removeErr != nil {
				log.Printf("remove backup schedule for failed site creation %s: %v", req.Site, removeErr)
			}
			a.Schedules.mu.Unlock()
		}
		if prepared {
			// The site did not exist before this job: remove the account, PHP
			// pool and site root the helper created, through the documented
			// privileged deletion path, then finalize through SiteManager.
			// Keep the job's lease token: the broker rejects unfenced
			// mutations, and ctx may already be cancelled.
			removed := false
			if err := siteHelperContext(cleanupCtx, a.Config, "delete", req.Site); err != nil {
				log.Printf("remove partially created site %s: %v", req.Site, err)
			} else if err := manager.Delete(cleanupCtx, req.Site); err != nil {
				log.Printf("finalize removal of partially created site %s: %v", req.Site, err)
			} else {
				removed = true
			}
			if removed && createdResourceProfile {
				if err := a.removeProvisionedSiteResourceProfile(req.Actor, req.Site); err != nil {
					log.Printf("remove failed provisioning resource profile for %s: %v", req.Site, err)
				}
			}
		}
		if prepared {
			if err := txn.cleanupDatabases(cleanupCtx, a.Config); err != nil {
				compensationErrors = append(compensationErrors, fmt.Errorf("remove provisioned WordPress database: %w", err))
			}
		}
		if len(compensationErrors) > 0 {
			cause := errors.Join(compensationErrors...)
			log.Printf("site creation compensation is incomplete for %s: %v", req.Site, cause)
			if err := txn.MarkRecoveryRequired(cause); err != nil {
				log.Printf("persist site creation recovery requirement for %s: %v", req.Site, err)
			}
		}
		cleanupCancel()
	}()

	prepared = true
	if err := siteHelperContext(operationCtx, a.Config, "prepare", req.Site); err != nil {
		return nil, fmt.Errorf("prepare isolated site: %w", err)
	}
	if req.CustomerProvisioning {
		createdResourceProfile, err = a.prepareProvisionedSiteResources(operationCtx, customerAccount, req.Site)
		if err != nil {
			return nil, err
		}
	}
	if _, err := manager.ActivateStaged(operationCtx, req.Site, staging); err != nil {
		return nil, fmt.Errorf("activate site: %w", err)
	}
	activated = true
	processKillInjection("create", "activate")
	if err := siteHelperContext(operationCtx, a.Config, "seal", req.Site); err != nil {
		return nil, fmt.Errorf("seal isolated site: %w", err)
	}
	result := SiteCreationResult{Site: req.Site, Template: req.Template, PublicRoot: canonical}
	if req.WordPress != nil {
		if err := a.provisionWordPressOnNewSite(operationCtx, req, access, txn); err != nil {
			return nil, err
		}
		if a.Routes != nil {
			for _, route := range a.Routes.list() {
				if strings.EqualFold(route.Domain, req.WordPress.Domain) {
					return nil, fmt.Errorf("domain %q became assigned to another managed route", req.WordPress.Domain)
				}
			}
		}
		httpsState, err := a.publishWordPressRoute(operationCtx, req, txn)
		if err != nil {
			return nil, err
		}
		result.Domain = req.WordPress.Domain
		result.Database = wordpressDatabaseName(req.Site)
		result.HTTPS = httpsState
		backup, err := createSiteBackupContext(operationCtx, a.Config, access, true, a.capacityLedger())
		if err != nil {
			return nil, fmt.Errorf("create the initial verified WordPress recovery point: %w", err)
		}
		result.RecoveryPoint = filepath.Base(backup.Path)
		result.RecoveryVerifiedAt = backup.VerifiedAt
		result.RecoverySHA256 = backup.ArchiveSHA256
		result.RecoveryEvidence = "Archive and database dump integrity verified. Application-level restore rehearsal has not yet run."
		if a.Schedules == nil {
			return nil, errors.New("initial recovery point exists, but automatic backup scheduling is unavailable")
		}
		schedule := BackupSchedule{Site: req.Site, IntervalMinutes: 1440, KeepLast: 7, IncludeDatabases: true, Enabled: true, NextRun: time.Now().UTC().Add(24 * time.Hour)}
		a.Schedules.mu.Lock()
		scheduleErr := persistMapKeyChange(a.Schedules.items, req.Site, &schedule, a.Schedules.persistLocked)
		a.Schedules.mu.Unlock()
		if scheduleErr != nil {
			return nil, fmt.Errorf("initial recovery point was created but daily backup schedule could not be saved: %w", scheduleErr)
		}
		backupScheduleCreated = true
		result.BackupSchedule = "daily; keep last 7; include database"
		result.NextSteps = []string{"Open the new domain over HTTPS and complete an application smoke check.", "The documented recovery point is " + result.RecoveryPoint + "; daily database-inclusive backups are scheduled."}
	} else {
		result.NextSteps = []string{"Publish a route for the site with POST /api/sites/deploy"}
	}
	if err := operationCtx.Err(); err != nil {
		return nil, err
	}
	if err := txn.Commit(); err != nil {
		return nil, fmt.Errorf("commit site creation transaction: %w", err)
	}
	committed = true
	outcome = "template=" + req.Template
	if a.MetadataCache != nil {
		a.MetadataCache.InvalidateSite(req.Site)
	}
	creationCompleted = true
	return json.Marshal(result)
}
