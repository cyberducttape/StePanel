package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	deploymentstate "github.com/cyberducttape/StePanel/internal/deployment"
)

// Deployment is the durable audit-facing release object shared by Git
// activation and sandboxed builds. It intentionally records independently
// completed stages; later orchestration can compose those stages without
// erasing their actor, commit, artifact, or rollback provenance.
type Deployment = deploymentstate.Record

type DeploymentStore struct {
	inner *deploymentstate.Store
}

func OpenDeploymentStore(path string) (*DeploymentStore, error) {
	inner, err := deploymentstate.Open(path)
	if err != nil {
		return nil, err
	}
	return &DeploymentStore{inner: inner}, nil
}

func OpenDeploymentStoreDB(db *sql.DB, legacyPath string) (*DeploymentStore, error) {
	inner, err := deploymentstate.OpenDB(db, legacyPath)
	if err != nil {
		return nil, err
	}
	return &DeploymentStore{inner: inner}, nil
}

func (s *DeploymentStore) add(item Deployment) error {
	return s.inner.Add(item)
}

func (s *DeploymentStore) list(site string) []Deployment {
	return s.inner.List(site)
}

// recordDeployment appends one stage record to deployment history. A
// persistence failure marks readiness unhealthy and is returned: callers
// must fail closed when the record precedes a host change, and must report
// the gap when the host change has already happened.
func (a *App) recordDeployment(site, stage, state, detail string, result gitDeployResult, artifact string) error {
	if a.Deployments == nil {
		return nil
	}
	id := result.DeploymentID
	if id == "" {
		var err error
		id, err = newJobID("deployment")
		if err != nil {
			a.LogPersistenceFailure("save_deployment", err, "could not create a deployment history identity for "+site)
			return fmt.Errorf("create deployment history identity: %w", err)
		}
	}
	if err := a.Deployments.add(Deployment{
		ID:         id,
		Site:       site,
		Repository: result.Repository,
		Ref:        result.Ref,
		Commit:     result.Commit,
		Stage:      stage,
		State:      state,
		Detail:     detail,
		Artifact:   artifact,
		Previous:   result.Previous,
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		a.LogPersistenceFailure("save_deployment", err, fmt.Sprintf("deployment history for %s stage %s=%s was not persisted", site, stage, state))
		return fmt.Errorf("persist deployment history: %w", err)
	}
	return nil
}

// historyUnavailable reports a deployment history failure that happened
// before any host change, so the deployment is refused rather than run
// without an operator-visible record.
func historyUnavailable(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, r, http.StatusServiceUnavailable, "deployment history is unavailable; no changes were made")
}

func (a *App) deployments(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		a.startNodeDeployment(w, r)
		return
	}
	if !a.requireCustomerScope(w, r, "site:read") {
		return
	}
	site := safeUser(strings.TrimSpace(r.URL.Query().Get("site")))
	if strings.TrimSpace(r.URL.Query().Get("site")) != "" && site == "" {
		http.Error(w, "invalid site", 422)
		return
	}
	if site != "" {
		if _, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", 403); !ok {
			return
		}
	}
	items := a.Deployments.list(site)
	if !a.Auth.IsAdministrator(r) && site == "" {
		filtered := items[:0]
		for _, item := range items {
			if a.canAccessSite(r, item.Site) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	writeJSON(w, 200, map[string]any{"deployments": items})
}
