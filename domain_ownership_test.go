package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Finding A: a DNS lookup that started for site A's claim must not write A
// back over a claim that was removed and replaced meanwhile.
func TestStaleDomainVerificationCannotOverwriteANewOwner(t *testing.T) {
	store, err := OpenDomainClaimStore(filepath.Join(t.TempDir(), "domains.json"))
	if err != nil {
		t.Fatal(err)
	}
	claimA, err := store.claim("site-a", "shop.example.org")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	previous := lookupDomainTXT
	lookupDomainTXT = func(ctx context.Context, name string) ([]string, error) {
		close(started)
		<-release
		return []string{claimA.Token}, nil
	}
	t.Cleanup(func() { lookupDomainTXT = previous })

	done := make(chan error, 1)
	go func() {
		_, err := store.verify(context.Background(), "site-a", "shop.example.org")
		done <- err
	}()
	<-started
	if err := store.removeSite(AuthorizedSite{site: "site-a"}); err != nil {
		t.Fatal(err)
	}
	claimB, err := store.claim("site-b", "shop.example.org")
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, errDomainClaimChanged) {
		t.Fatalf("stale verification = %v, want it discarded", err)
	}
	current, ok := store.get("shop.example.org")
	if !ok || current.Site != "site-b" || current.Token != claimB.Token || current.State != "pending" {
		t.Fatalf("claim after the stale verification = %+v", current)
	}
	if store.verifiedFor("site-a", "shop.example.org") {
		t.Fatal("site A was verified for a domain it no longer claims")
	}
}

// The same site deleting and re-making its claim also invalidates a lookup
// that started under the old claim's token.
func TestVerificationOfAReplacedClaimForTheSameSiteIsDiscarded(t *testing.T) {
	store, err := OpenDomainClaimStore(filepath.Join(t.TempDir(), "domains.json"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.claim("site-a", "shop.example.org")
	if err != nil {
		t.Fatal(err)
	}
	previous := lookupDomainTXT
	lookupDomainTXT = func(ctx context.Context, name string) ([]string, error) {
		if err := store.removeSite(AuthorizedSite{site: "site-a"}); err != nil {
			t.Error(err)
		}
		if _, err := store.claim("site-a", "shop.example.org"); err != nil {
			t.Error(err)
		}
		return []string{old.Token}, nil
	}
	t.Cleanup(func() { lookupDomainTXT = previous })
	if _, err := store.verify(context.Background(), "site-a", "shop.example.org"); !errors.Is(err, errDomainClaimChanged) {
		t.Fatalf("verification of a replaced claim = %v", err)
	}
	if store.verifiedFor("site-a", "shop.example.org") {
		t.Fatal("the replacement claim was verified with the old token")
	}
}

type customerJourney struct {
	f   *siteCreationFixture
	txt map[string]string
}

func newCustomerJourney(t *testing.T) *customerJourney {
	t.Helper()
	f := newSiteCreationFixture(t, "")
	accounts, err := OpenAccountStore(filepath.Join(f.root, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"customer", "rival"} {
		if _, err := accounts.Create(name, "a sufficiently long customer password", testTOTPSecret, "starter", nil); err != nil {
			t.Fatal(err)
		}
	}
	f.app.Accounts, f.app.Auth.Accounts = accounts, accounts
	domains, err := OpenDomainClaimStore(filepath.Join(f.root, "domains.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.app.Domains = domains
	routes, err := OpenRouteStore(filepath.Join(f.root, "routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.app.Routes = routes
	f.app.Config.WebServer, f.app.Config.DBCtl, f.app.Config.WPCLI, f.app.Config.BackupRoot = "caddy", "/bin/true", "/bin/true", filepath.Join(f.root, "backups")
	j := &customerJourney{f: f, txt: map[string]string{}}
	previous := lookupDomainTXT
	lookupDomainTXT = func(ctx context.Context, name string) ([]string, error) {
		if value, ok := j.txt[name]; ok {
			return []string{value}, nil
		}
		return nil, errors.New("no such record")
	}
	t.Cleanup(func() { lookupDomainTXT = previous })
	return j
}

func (j *customerJourney) call(t *testing.T, user string, handler http.HandlerFunc, path, body string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), apiTokenUsernameKey{}, user))
	response := httptest.NewRecorder()
	handler(response, request)
	var decoded map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &decoded)
	return response.Code, decoded
}

const journeyWordPress = `{"site":"blog","template":"wordpress","domain":"blog.example.org","title":"Blog","admin_user":"owner","admin_email":"owner@example.org","admin_password":"a-very-long-admin-password-123"}`

// Finding B: a customer creates a brand-new WordPress site. The domain is
// claimed (reserving the site name), verified by DNS TXT, and then the site
// is created and assigned to the customer.
func TestCustomerReservesClaimsVerifiesAndCreatesAWordPressSite(t *testing.T) {
	j := newCustomerJourney(t)
	a := j.f.app

	code, claim := j.call(t, "customer", a.domainClaim, "/api/domains/claim", `{"site":"blog","domain":"blog.example.org"}`)
	if code != http.StatusCreated || claim["reserved_until"] == nil {
		t.Fatalf("reserving claim = %d %v", code, claim)
	}
	// Another tenant cannot take the reserved name, claim its domain, or
	// verify the customer's claim.
	if code, _ := j.call(t, "rival", a.domainClaim, "/api/domains/claim", `{"site":"blog","domain":"rival.example.org"}`); code != http.StatusConflict {
		t.Fatalf("rival reservation of the same name = %d", code)
	}
	if code, _ := j.call(t, "rival", a.domainVerify, "/api/domains/verify", `{"site":"blog","domain":"blog.example.org"}`); code != http.StatusForbidden {
		t.Fatalf("rival verification = %d", code)
	}
	if code, _ := j.call(t, "rival", a.siteCreate, "/api/sites", `{"site":"blog","template":"php"}`); code != http.StatusConflict {
		t.Fatalf("rival creation of the reserved name = %d", code)
	}

	// Creation before verification is refused.
	if code, _ := j.call(t, "customer", a.siteCreate, "/api/sites", journeyWordPress); code != http.StatusConflict {
		t.Fatalf("creation before verification = %d", code)
	}
	j.txt["_stepanel.blog.example.org"] = claim["txt_value"].(string)
	if code, body := j.call(t, "customer", a.domainVerify, "/api/domains/verify", `{"site":"blog","domain":"blog.example.org"}`); code != http.StatusOK || body["state"] != "verified" {
		t.Fatalf("verification = %d %v", code, body)
	}
	code, queued := j.call(t, "customer", a.siteCreate, "/api/sites", journeyWordPress)
	if code != http.StatusAccepted || queued["job_id"] == "" {
		t.Fatalf("WordPress creation = %d %v", code, queued)
	}
	if !a.Accounts.OwnsSite("customer", "blog") {
		t.Fatal("the new site is not assigned to the customer")
	}
	stored, _ := a.Domains.get("blog.example.org")
	if !stored.ExpiresAt.IsZero() || stored.Tenant != "customer" || !a.Domains.verifiedFor("blog", "blog.example.org") {
		t.Fatalf("claim after creation = %+v", stored)
	}
	job, ok := a.Jobs.Get(queued["job_id"].(string))
	if !ok || job.Kind != "site.create" || job.User != "blog" {
		t.Fatalf("queued job = %+v", job)
	}

	// Finding A for creation: the job re-checks ownership under its lock.
	// A claim removed after the request was accepted stops it.
	if err := a.Domains.removeSite(AuthorizedSite{site: "blog"}); err != nil {
		t.Fatal(err)
	}
	item, claimed, err := a.Jobs.ClaimNext("worker-test", "site.create")
	if err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if _, err := a.handleSiteCreation(context.Background(), item); err == nil || !strings.Contains(err.Error(), "no longer verified") || !isNonRetryableJobError(err) {
		t.Fatalf("creation after the claim was removed = %v", err)
	}
	if exists, _ := a.siteExists("blog"); exists {
		t.Fatal("the site was created without a verified domain")
	}
}

// A reservation that is never used expires and releases the name.
func TestUnusedSiteNameReservationExpires(t *testing.T) {
	j := newCustomerJourney(t)
	a := j.f.app
	if code, _ := j.call(t, "customer", a.domainClaim, "/api/domains/claim", `{"site":"blog","domain":"blog.example.org"}`); code != http.StatusCreated {
		t.Fatalf("reservation = %d", code)
	}
	a.Domains.mu.Lock()
	claim := a.Domains.values["blog.example.org"]
	claim.ExpiresAt = time.Now().Add(-time.Minute)
	a.Domains.values["blog.example.org"] = claim
	a.Domains.mu.Unlock()
	if _, reserved := a.Domains.reservedBy("blog"); reserved {
		t.Fatal("an expired reservation still holds the name")
	}
	if code, _ := j.call(t, "rival", a.domainClaim, "/api/domains/claim", `{"site":"blog","domain":"blog.example.org"}`); code != http.StatusCreated {
		t.Fatalf("claim after the reservation expired = %d", code)
	}
	if stored, _ := a.Domains.get("blog.example.org"); stored.Tenant != "rival" || stored.Token == claim.Token {
		t.Fatalf("expired reservation was reused: %+v", stored)
	}
}

// Finding C: a retry with the same Idempotency-Key after the site was
// created returns the original job; another request with that key is
// refused.
func TestSiteCreationRetryAfterSuccessReturnsTheOriginalJob(t *testing.T) {
	f := newSiteCreationFixture(t, "")
	post := func(body string) (int, map[string]string) {
		request := httptest.NewRequest(http.MethodPost, "/api/sites", strings.NewReader(body))
		request.Header.Set("Idempotency-Key", "create-once-1")
		request = request.WithContext(context.WithValue(request.Context(), apiTokenUsernameKey{}, "admin"))
		response := httptest.NewRecorder()
		f.app.siteCreate(response, request)
		var decoded map[string]string
		_ = json.Unmarshal(response.Body.Bytes(), &decoded)
		return response.Code, decoded
	}
	code, first := post(`{"site":"retry-done","template":"php"}`)
	if code != http.StatusAccepted {
		t.Fatalf("first create = %d %v", code, first)
	}
	item, ok, err := f.app.Jobs.ClaimNext("worker-test", "site.create")
	if err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	if _, err := f.app.handleSiteCreation(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if exists, err := f.app.siteExists("retry-done"); err != nil || !exists {
		t.Fatalf("site exists = %v, %v", exists, err)
	}
	code, retry := post(`{"site":"retry-done","template":"php"}`)
	if code != http.StatusAccepted || retry["job_id"] != first["job_id"] {
		t.Fatalf("retry after success = %d %v, want the original job %s", code, retry, first["job_id"])
	}
	if code, _ := post(`{"site":"retry-done","template":"wordpress"}`); code != http.StatusConflict {
		t.Fatalf("a different request reusing the key = %d", code)
	}
	// Without a key, the site's existence is still reported.
	request := httptest.NewRequest(http.MethodPost, "/api/sites", strings.NewReader(`{"site":"retry-done","template":"php"}`))
	request = request.WithContext(context.WithValue(request.Context(), apiTokenUsernameKey{}, "admin"))
	response := httptest.NewRecorder()
	f.app.siteCreate(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("keyless create of an existing site = %d", response.Code)
	}
}

// A WordPress creation retried with the same Idempotency-Key replays the job
// only if the administrator password matches too; the password is compared
// with the queued job's copy, never hashed into the request digest.
func TestWordPressCreationRetryMustRepeatThePassword(t *testing.T) {
	j := newCustomerJourney(t)
	post := func(body string) (int, map[string]any) {
		request := httptest.NewRequest(http.MethodPost, "/api/sites", strings.NewReader(body))
		request.Header.Set("Idempotency-Key", "wordpress-once")
		request = request.WithContext(context.WithValue(request.Context(), apiTokenUsernameKey{}, "admin"))
		response := httptest.NewRecorder()
		j.f.app.siteCreate(response, request)
		var decoded map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &decoded)
		return response.Code, decoded
	}
	code, first := post(journeyWordPress)
	if code != http.StatusAccepted {
		t.Fatalf("first create = %d %v", code, first)
	}
	if code, again := post(journeyWordPress); code != http.StatusAccepted || again["job_id"] != first["job_id"] {
		t.Fatalf("identical retry = %d %v", code, again)
	}
	changed := strings.Replace(journeyWordPress, "a-very-long-admin-password-123", "another-long-admin-password-456", 1)
	if code, _ := post(changed); code != http.StatusConflict {
		t.Fatalf("retry with another password = %d, want 409", code)
	}
}
