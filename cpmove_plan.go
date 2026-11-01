package stepanel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

type cpmovePlanRequest struct {
	UploadID         string `json:"upload_id"`
	User             string `json:"username"`
	RestoreDatabases bool   `json:"restore_databases"`
}

type cpmoveDestinationPlan struct {
	Status            string                   `json:"status"`
	TargetAccount     string                   `json:"target_account"`
	RestoreDatabases  bool                     `json:"restore_databases"`
	ArchiveBytes      int64                    `json:"archive_bytes"`
	ExpandedBytes     int64                    `json:"expanded_bytes"`
	DatabaseBytes     int64                    `json:"database_bytes"`
	RequiredFreeBytes uint64                   `json:"required_free_bytes"`
	Mappings          []MigrationDomainMapping `json:"domain_mappings"`
	Blockers          []string                 `json:"blockers"`
	Warnings          []string                 `json:"warnings"`
	RollbackPlan      []string                 `json:"rollback_plan"`
	PreviewToken      string                   `json:"preview_token,omitempty"`
}

func (a *App) makeCPMoveDestinationPlan(upload cpmoveUpload, user string, restoreDB bool) (cpmoveDestinationPlan, error) {
	if safeUser(user) != user {
		return cpmoveDestinationPlan{}, errors.New("invalid destination account")
	}
	p := cpmoveDestinationPlan{Status: "ready", TargetAccount: user, RestoreDatabases: restoreDB, ArchiveBytes: upload.Size, ExpandedBytes: upload.ExpandedBytes, DatabaseBytes: upload.DatabaseBytes,
		Mappings:     append([]MigrationDomainMapping(nil), upload.DomainMappings...),
		RollbackPlan: []string{"A pre-import safety backup is created by the restore workflow before replacing the target site.", "Filesystem and database recovery journals are retained for interrupted operations; inspect them before retrying after an interruption.", "DNS changes are not applied by this import. Keep the source DNS active until destination HTTPS and application checks pass."}}
	if restoreDB && !mysqlCompatible(a.Config) {
		p.Blockers = append(p.Blockers, "Destination database is not MySQL/MariaDB compatible with cPanel SQL restore.")
	}
	if err := a.checkCPMoveCapacity(upload.ExpandedBytes, upload.DatabaseBytes, restoreDB, upload.PathObjects); err != nil {
		p.Blockers = append(p.Blockers, "Capacity admission failed: "+err.Error())
	}
	if a.Accounts != nil {
		if owner, exists, err := a.Accounts.OwnerOfSiteWithError(user); err != nil {
			return p, fmt.Errorf("check destination ownership: %w", err)
		} else if exists && owner != user {
			p.Blockers = append(p.Blockers, fmt.Sprintf("Target site name %q is owned by account %q.", user, owner))
		}
	}
	if upload.DomainInventoryStatus != "complete" {
		p.Warnings = append(p.Warnings, "Source domain inventory is incomplete or unknown; verify domain and document-root mappings manually.")
	}
	if len(upload.DomainMappings) == 0 {
		p.Warnings = append(p.Warnings, "No source domain mappings were identified; destination routing must be configured and verified manually.")
	}
	plannedSites := map[string]bool{}
	for i := range p.Mappings {
		m := &p.Mappings[i]
		if m.Kind == "main" || m.Kind == "parked" {
			m.Destination = "account " + user + " / public_html"
			continue
		}
		if m.Kind != "addon" && m.Kind != "subdomain" {
			continue
		}
		name, err := siteNameForDomain(m.SourceDomain, func(candidate string) bool {
			if plannedSites[candidate] {
				return true
			}
			if candidate == user {
				return true
			}
			if a.Jobs != nil && a.Jobs.db != nil {
				if _, err := sitelayout.Get(a.Jobs.db, candidate); err == nil {
					return true
				}
			}
			if _, err := existingManagedSiteRoot(a.Config.WebRoot, candidate); err == nil {
				return true
			}
			return false
		})
		if err != nil {
			m.Status = "blocked"
			m.Details = err.Error()
			p.Blockers = append(p.Blockers, m.SourceDomain+": "+err.Error())
			continue
		}
		plannedSites[name] = true
		m.Destination = "site " + name + " (account " + user + ")"
		if m.Status == "planned" {
			m.Details = "Destination site name checked against current site placements and filesystem; DNS and route activation remain separate."
		}
	}
	if a.Routes != nil {
		for _, m := range p.Mappings {
			destinationSite := user
			if strings.HasPrefix(m.Destination, "site ") {
				destinationSite = strings.TrimSuffix(strings.TrimPrefix(m.Destination, "site "), " (account "+user+")")
			}
			for _, route := range a.Routes.list() {
				if strings.EqualFold(route.Domain, m.SourceDomain) && route.Site != destinationSite {
					p.Blockers = append(p.Blockers, fmt.Sprintf("Domain %s is already routed to site %s (planned destination %s).", m.SourceDomain, route.Site, destinationSite))
				}
			}
		}
	}
	if p.Status != "blocked" {
		p.Status = "ready"
	}
	if len(p.Blockers) > 0 {
		p.Status = "blocked"
	}
	if required, _, err := cpmoveRequiredFreeBytes(a.Config, upload.Size, upload.ExpandedBytes, upload.PathObjects); err == nil {
		p.RequiredFreeBytes = required
	}
	return p, nil
}

func cpmovePlanPreviewState(upload cpmoveUpload, plan cpmoveDestinationPlan) map[string]any {
	plan.PreviewToken = ""
	return map[string]any{"upload_id": upload.ID, "sha256": upload.SHA256, "size": upload.Size, "destination_plan": plan}
}

func (a *App) cpmovePlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !a.Auth.CSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var request cpmovePlanRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid migration plan request", 400)
		return
	}
	actor := a.Auth.UsernameForRequest(r)
	upload, err := readCPMoveUpload(a.Config.ImportRoot, strings.TrimSpace(request.UploadID))
	if err != nil || upload.Owner != actor {
		http.Error(w, "upload is missing, expired, or belongs to another operator", 404)
		return
	}
	plan, err := a.makeCPMoveDestinationPlan(upload, request.User, request.RestoreDatabases)
	if err != nil {
		http.Error(w, "could not validate destination plan", http.StatusServiceUnavailable)
		return
	}
	if plan.Status != "blocked" {
		before := cpmovePlanPreviewState(upload, plan)
		proposed := map[string]any{"user": request.User, "restore_databases": request.RestoreDatabases}
		preview, err := a.makeChangePreview("cpmove.import:"+upload.ID, before, proposed, []string{"Extract up to the inspected expanded-size and entry limits.", "Replace or create the target site and optionally restore the listed SQL databases.", "No DNS change is made; TLS and live HTTP validation remain cutover steps."}, plan.RollbackPlan)
		if err != nil {
			http.Error(w, "could not sign migration plan", 500)
			return
		}
		plan.PreviewToken = preview.PreviewToken
	}
	writeJSON(w, 200, plan)
}
