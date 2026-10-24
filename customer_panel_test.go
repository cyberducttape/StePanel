package stepanel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newCustomerPanelApp(t *testing.T) *App {
	t.Helper()
	a, _ := newSiteFilesApp(t)
	dashboard, panel, err := loadViews()
	if err != nil {
		t.Fatal(err)
	}
	a.View, a.PanelView = dashboard, panel
	a.Jobs = &Jobs{}
	return a
}

func asCustomer(r *http.Request, user string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, user))
}

func TestCustomersGetTheirOwnPanel(t *testing.T) {
	a := newCustomerPanelApp(t)
	w := httptest.NewRecorder()
	a.dashboard(w, asCustomer(httptest.NewRequest(http.MethodGet, "/", nil), "alice"))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/panel" {
		t.Fatalf("customer dashboard = %d %q, want a redirect to /panel", w.Code, w.Header().Get("Location"))
	}
	w = httptest.NewRecorder()
	a.customerPanel(w, asCustomer(httptest.NewRequest(http.MethodGet, "/panel", nil), "alice"))
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("customer panel = %d %s", w.Code, body)
	}
	if !strings.Contains(body, "Recent activity is temporarily unavailable") || strings.Contains(body, "No recent operations for your websites.") {
		t.Fatal("customer panel presents unavailable durable job history as an empty history")
	}
	for _, want := range []string{`id="fileManager"`, `id="siteCreateForm"`, `name="kind"`, `id="accountTokenForm"`, "/static/panel.js", "appearanceDialog", `id="jobCenter"`} {
		if !strings.Contains(body, want) {
			t.Errorf("customer panel is missing %s", want)
		}
	}
	for _, operatorOnly := range []string{`href="#migrations"`, `id="accounts"`, "Customers</a>", "/static/admin-accounts.js", `name="account"`} {
		if strings.Contains(body, operatorOnly) {
			t.Errorf("customer panel shows operator content %s", operatorOnly)
		}
	}
	// Without the account store the panel fails closed instead of bouncing
	// the customer between "/" and "/panel".
	a.Accounts, w = nil, httptest.NewRecorder()
	a.customerPanel(w, asCustomer(httptest.NewRequest(http.MethodGet, "/panel", nil), "alice"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("panel without accounts = %d %q", w.Code, w.Header().Get("Location"))
	}
	// The administrator keeps the operator dashboard.
	w = httptest.NewRecorder()
	a.customerPanel(w, asCustomer(httptest.NewRequest(http.MethodGet, "/panel", nil), "admin"))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("administrator panel = %d %q, want a redirect to /", w.Code, w.Header().Get("Location"))
	}
}

func TestAccountHostingListsPlacedSitesMainFirst(t *testing.T) {
	a := newCustomerPanelApp(t)
	w := httptest.NewRecorder()
	a.accountHosting(w, asCustomer(httptest.NewRequest(http.MethodGet, "/api/account/hosting", nil), "alice"))
	var view HostingAccountView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil {
		t.Fatalf("hosting = %d %s", w.Code, w.Body)
	}
	if view.Account != "alice" || view.UnixUser != "alice" || !strings.HasSuffix(view.Home, "/alice") || len(view.Sites) != 3 {
		t.Fatalf("hosting view = %+v", view)
	}
	if view.Sites[0].Site != "alice" || view.Sites[0].Kind != "main" || !strings.HasSuffix(view.Sites[0].DocRoot, "/alice/public_html") ||
		view.Sites[1].Site != "shop" || view.Sites[1].Directory != "shop.example.org" || view.Sites[2].Site != "legacy" || view.Sites[2].Placed {
		t.Fatalf("hosting sites = %+v", view.Sites)
	}
	w = httptest.NewRecorder()
	a.accountHosting(w, asCustomer(httptest.NewRequest(http.MethodGet, "/api/account/hosting", nil), "admin"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("administrator hosting = %d", w.Code)
	}
	// bob's view never includes alice's sites.
	w = httptest.NewRecorder()
	a.accountHosting(w, asCustomer(httptest.NewRequest(http.MethodGet, "/api/account/hosting", nil), "bob"))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "public_html") || strings.Contains(w.Body.String(), "shop") {
		t.Fatalf("bob's hosting = %d %s", w.Code, w.Body)
	}
}
