package stepanel

import (
	"net/http"
	"path/filepath"
	"sort"

	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

// HostingSite is one of the account's sites as the customer panel shows it.
type HostingSite struct {
	Site      string `json:"site"`
	Placed    bool   `json:"placed"`
	Kind      string `json:"kind,omitempty"`
	Directory string `json:"directory,omitempty"`
	// DocRoot is the absolute document root, as seen over SFTP.
	DocRoot string `json:"doc_root"`
}

// HostingAccountView is the customer's hosting account: its Unix user, home
// and the sites they can reach (ADR-0007).
type HostingAccountView struct {
	Account  string        `json:"account"`
	UnixUser string        `json:"unix_user,omitempty"`
	Home     string        `json:"home,omitempty"`
	Sites    []HostingSite `json:"sites"`
}

// accountHosting answers GET /api/account/hosting for a customer session or
// a token with site:read. Sites are those of the caller's tenant that the
// caller can access, main site first.
func (a *App) accountHosting(w http.ResponseWriter, r *http.Request) {
	username := a.Auth.UsernameForRequest(r)
	if username == "" || a.Auth.IsAdministrator(r) || a.Accounts == nil {
		http.Error(w, "customer account required", http.StatusForbidden)
		return
	}
	if !a.requireCustomerScope(w, r, "site:read") {
		return
	}
	account, ok := a.Accounts.Get(username)
	if !ok {
		http.Error(w, "customer account required", http.StatusForbidden)
		return
	}
	view := HostingAccountView{Account: accountTenantID(account), Sites: []HostingSite{}}
	if db := sitePlacementDB.Load(); db != nil {
		user, active, err := sitelayout.AccountUser(db, view.Account)
		if err != nil {
			http.Error(w, "hosting account is unavailable", http.StatusServiceUnavailable)
			return
		}
		if active {
			view.UnixUser = user
			view.Home = filepath.Join(a.Config.HomeRoot, user)
		}
	}
	sites, err := a.Accounts.GetSitesWithError(username)
	if err != nil {
		http.Error(w, "site assignments are unavailable", http.StatusServiceUnavailable)
		return
	}
	for _, site := range sites {
		if !a.canAccessSite(r, site) {
			continue
		}
		location, err := siteLocationFor(a.Config, site)
		if err != nil {
			http.Error(w, "site location is unavailable", http.StatusServiceUnavailable)
			return
		}
		entry := HostingSite{Site: site, Placed: location.Placed, DocRoot: location.DocRoot}
		if location.Placed {
			entry.Kind, entry.Directory = string(location.Placement.Kind), location.Placement.Directory
		}
		view.Sites = append(view.Sites, entry)
	}
	rank := map[string]int{string(sitelayout.KindMain): 0, string(sitelayout.KindAddon): 1, string(sitelayout.KindSubdomain): 2, "": 3}
	sort.SliceStable(view.Sites, func(i, j int) bool {
		if rank[view.Sites[i].Kind] != rank[view.Sites[j].Kind] {
			return rank[view.Sites[i].Kind] < rank[view.Sites[j].Kind]
		}
		return view.Sites[i].Site < view.Sites[j].Site
	})
	writeJSON(w, http.StatusOK, view)
}
