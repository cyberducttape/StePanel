package main

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	deploymentstate "github.com/itchyitchy123/StePanel/internal/deployment"
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
func (a *App) recordDeployment(site, stage, state, detail string, result gitDeployResult, artifact string) {
	if a.Deployments == nil {
		return
	}
	id := result.DeploymentID
	if id == "" {
		id, _ = newJobID("deployment")
	}
	_ = a.Deployments.add(Deployment{ID: id, Site: site, Repository: result.Repository, Ref: result.Ref, Commit: result.Commit, Stage: stage, State: state, Detail: detail, Artifact: artifact, Previous: result.Previous, CreatedAt: time.Now().UTC()})
}
func (a *App) deployments(w http.ResponseWriter, r *http.Request) {
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
