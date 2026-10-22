package stepanel

import (
	"log"
	"net/http"
	"time"
)

// customerPanel renders the customer panel (web/panel.html): the hosting
// account's websites, files, domains, databases, backups, runtime and
// account settings. The operator dashboard stays at "/".
func (a *App) customerPanel(w http.ResponseWriter, r *http.Request) {
	if a.Auth.IsAdministrator(r) || a.Accounts == nil || a.PanelView == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	username := a.Auth.UsernameForRequest(r)
	account, ok := a.Accounts.Get(username)
	if !ok {
		http.Error(w, "customer account required", http.StatusForbidden)
		return
	}
	csrf := ""
	if cookie, err := r.Cookie("stepanel_csrf"); err == nil {
		csrf = cookie.Value
	}
	var jobs []Job
	sites, err := a.Accounts.GetSitesWithError(username)
	if err != nil {
		log.Printf("customer panel site assignments unavailable: %v", err)
	} else if jobs, err = a.Jobs.ListForOwnersSummaries(8, sites); err != nil {
		log.Printf("customer panel job history unavailable: %v", err)
		jobs = nil
	}
	data := map[string]any{
		"Title": "StePanel", "Config": a.Config, "AssetVersion": a.AssetVersion, "CSRF": csrf,
		"AuthEnabled": a.Auth.Enabled, "Username": username, "Now": time.Now(), "Jobs": jobs,
		"Account": account, "AccountSiteCount": len(sites),
		"CanCreateSite": accountRole(account) == "owner" && !account.Suspended,
	}
	if err := a.PanelView.Execute(w, data); err != nil {
		log.Printf("customer panel render failed: %v", err)
	}
}
