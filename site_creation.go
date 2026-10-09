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

	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

// Site creation publishes a new, isolated site from a template. It follows
// the lifecycle contract in docs/V1_PRODUCTION_GATES.md (Gate 1): the
// template is written into SiteManager staging, the root broker creates the
// site account and PHP-FPM pool (stepanel-sitectl prepare), SiteManager
// activates the staged tree, and the broker seals ownership. A SiteTransaction
// journal makes an interrupted creation roll back on startup.
//
// Creation does not publish a web route or assign the site to a customer
// account. Those stay with their existing workflows, which own domain
// verification (POST /api/sites/deploy) and plan resource enforcement
// (PATCH /api/accounts/{username}).

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
	Site     string `json:"site"`
	Template string `json:"template"`
	Actor    string `json:"actor"`
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
	if !a.Auth.IsAdministrator(r) || !a.Auth.CSRF(r) {
		http.Error(w, "administrator CSRF request required", http.StatusForbidden)
		return
	}
	operationKey, err := requestOperationKey(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	var input struct {
		Site     string `json:"site"`
		Template string `json:"template"`
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
	payload, err := json.Marshal(durableSiteCreationRequest{Site: input.Site, Template: input.Template, Actor: a.Auth.AuditActor(r)})
	if err != nil {
		http.Error(w, "could not encode site creation job", http.StatusInternalServerError)
		return
	}
	job, _, err := a.Jobs.EnqueueIdempotent("site.create", input.Site, operationKey, payload, 3)
	if err != nil {
		http.Error(w, "could not persist site creation job", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "site": input.Site, "template": input.Template, "status_url": "/api/jobs/" + job.ID})
}

// siteExists reports whether the site's root directory exists. The public
// tree is not enough: a site whose public tree is missing still owns its
// account, PHP pool and state.
func (a *App) siteExists(site string) (bool, error) {
	root, err := safePath(a.Config.WebRoot, "sites", site)
	if err != nil {
		return false, err
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
	operationCtx, release, err := a.acquireSiteMutationLockContext(ctx, req.Site)
	if err != nil {
		return nil, fmt.Errorf("acquire site mutation lock: %w", err)
	}
	defer release()
	if exists, err := a.siteExists(req.Site); err != nil {
		return nil, fmt.Errorf("inspect site: %w", err)
	} else if exists {
		return nil, fmt.Errorf("site %q already exists", req.Site)
	}
	canonical, err := safePath(a.Config.WebRoot, "sites", req.Site, "public")
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
		manager, err = siteauthority.NewDefaultManager(a.Config.WebRoot)
		if err != nil {
			return nil, fmt.Errorf("initialize site manager: %w", err)
		}
	}
	staging, err := manager.CreateStaging(operationCtx, ".stepanel-create-")
	if err != nil {
		return nil, fmt.Errorf("create site staging: %w", err)
	}
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

	txn, err := BeginSiteTransaction(a.Config.RecoveryRoot, canonical, siteCreationTransactionKind, access)
	if err != nil {
		return nil, fmt.Errorf("begin site creation transaction: %w", err)
	}
	committed, prepared := false, false
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
			if err := siteHelperContext(cleanupCtx, a.Config, "delete", req.Site); err != nil {
				log.Printf("remove partially created site %s: %v", req.Site, err)
			} else if err := manager.Delete(cleanupCtx, req.Site); err != nil {
				log.Printf("finalize removal of partially created site %s: %v", req.Site, err)
			}
		}
	}()

	prepared = true
	if err := siteHelperContext(operationCtx, a.Config, "prepare", req.Site); err != nil {
		return nil, fmt.Errorf("prepare isolated site: %w", err)
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
	return json.Marshal(SiteCreationResult{
		Site:       req.Site,
		Template:   req.Template,
		PublicRoot: canonical,
		NextSteps: []string{
			"Publish a route for the site with POST /api/sites/deploy",
			"Assign the site to a customer account with PATCH /api/accounts/{username}",
		},
	})
}
