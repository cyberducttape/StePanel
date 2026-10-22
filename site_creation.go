package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

// Site creation publishes a new, isolated site from a template. It follows
// the lifecycle contract in docs/V1_PRODUCTION_GATES.md (Gate 1): the
// template is written into SiteManager staging, the root broker creates the
// site account and PHP-FPM pool (stepanel-sitectl prepare), SiteManager
// activates the staged tree, and the broker seals ownership. A SiteTransaction
// journal makes an interrupted creation roll back on startup.
//
// Creation does not publish a web route. Domain verification and route
// publication stay with POST /api/sites/deploy; customer plan ceilings are
// applied before activation for customer-provisioning requests.

// siteTemplate fills a staged public root for a new site.
type siteTemplate struct {
	description string
	populate    func(ctx context.Context, a *App, site, staging string) error
}

var siteTemplates = map[string]siteTemplate{
	"php": {description: "blank PHP site", populate: populateBlankPHPSite},
}

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
	Account   string `json:"account,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Directory string `json:"directory,omitempty"`
}

func (req durableSiteCreationRequest) placement() (sitelayout.Placement, bool) {
	if req.Account == "" {
		return sitelayout.Placement{}, false
	}
	return sitelayout.Placement{Site: req.Site, Account: req.Account, UnixUser: req.Account, Kind: sitelayout.Kind(req.Kind), Directory: req.Directory}, true
}

// SiteCreationResult is the output of a completed site.create job.
type SiteCreationResult struct {
	Site       string   `json:"site"`
	Template   string   `json:"template"`
	PublicRoot string   `json:"public_root"`
	NextSteps  []string `json:"next_steps"`
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
		Account string `json:"account"`
		Kind    string `json:"kind"`
		Domain  string `json:"domain"`
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
	request := durableSiteCreationRequest{Site: input.Site, Template: input.Template, Actor: a.Auth.AuditActor(r), CustomerProvisioning: !admin}
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
	operationCtx, release, err := a.acquireSiteMutationLockContext(ctx, req.Site)
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
	defer func() {
		if committed {
			return
		}
		if err := txn.Rollback(); err != nil {
			log.Printf("roll back site creation transaction %s: %v", txn.ID, err)
		}
		if prepared {
			// The site did not exist before this job: remove the account, PHP
			// pool and site root the helper created, through the documented
			// privileged deletion path, then finalize through SiteManager.
			// Keep the job's lease token: the broker rejects unfenced
			// mutations, and ctx may already be cancelled.
			cleanupCtx := context.WithoutCancel(ctx)
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
	nextSteps := []string{
		"Publish a route for the site with POST /api/sites/deploy",
	}
	creationCompleted = true
	return json.Marshal(SiteCreationResult{
		Site:       req.Site,
		Template:   req.Template,
		PublicRoot: canonical,
		NextSteps:  nextSteps,
	})
}
