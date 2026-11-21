package stepanel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDomainClaimStorePersistsAndProtectsOwnership(t *testing.T) {
	store, err := OpenDomainClaimStore(filepath.Join(t.TempDir(), "domain-claims.json"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.claim("site-a", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if claim.Token == "" || claim.State != "pending" {
		t.Fatalf("claim = %#v", claim)
	}
	if _, err := store.claim("site-b", "example.com"); err == nil {
		t.Fatal("expected a domain claimed by one site to reject another site")
	}
	repeated, err := store.claim("site-a", "example.com")
	if err != nil || repeated.Token != claim.Token {
		t.Fatalf("pending claim was not idempotent: claim=%#v err=%v", repeated, err)
	}

	reopened, err := OpenDomainClaimStore(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reopened.get("example.com"); !ok || got.Site != "site-a" || got.Token != claim.Token {
		t.Fatalf("reopened claim = %#v, ok=%v", got, ok)
	}
	if reopened.verifiedFor("site-a", "example.com") {
		t.Fatal("pending claim was treated as verified")
	}

	claim.State = "verified"
	claim.VerifiedAt = time.Now().UTC()
	if err := reopened.save(claim); err != nil {
		t.Fatal(err)
	}
	if !reopened.verifiedFor("site-a", "example.com") {
		t.Fatal("verified claim was not accepted")
	}
	if got, err := reopened.claim("site-a", "example.com"); err != nil || got.Token != claim.Token {
		t.Fatalf("verified claim was not idempotent: claim=%#v err=%v", got, err)
	}

	if err := reopened.removeSite(AuthorizedSite{site: "site-a"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.get("example.com"); ok {
		t.Fatal("domain claim remained after site removal")
	}
}

func TestOpenDomainClaimStoreBoundsFileRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "domain-claims.json")
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDomainClaimStore(path); err != nil {
		t.Fatalf("valid domain claim state: %v", err)
	}
	if err := os.Truncate(path, maxDomainClaimStateBytes+1); err != nil {
		t.Fatal(err)
	}
	_, err := OpenDomainClaimStore(path)
	if err == nil || !strings.Contains(err.Error(), "exceeds 4 MiB") {
		t.Fatalf("oversized domain claim state error = %v", err)
	}
}

func TestDomainClaimVerificationRevalidatesExistingProof(t *testing.T) {
	store, err := OpenDomainClaimStore(filepath.Join(t.TempDir(), "domain-claims.json"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.claim("site-a", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	claim.State = "verified"
	claim.VerifiedAt = time.Now().UTC()
	if err := store.save(claim); err != nil {
		t.Fatal(err)
	}
	original := lookupDomainTXT
	t.Cleanup(func() { lookupDomainTXT = original })
	lookupDomainTXT = func(context.Context, string) ([]string, error) { return []string{claim.Token}, nil }
	if _, err := store.verify(context.Background(), "site-a", "example.com"); err != nil || !store.verifiedFor("site-a", "example.com") {
		t.Fatalf("valid TXT revalidation failed: err=%v", err)
	}
	lookupDomainTXT = func(context.Context, string) ([]string, error) { return nil, nil }
	if _, err := store.verify(context.Background(), "site-a", "example.com"); err == nil || store.verifiedFor("site-a", "example.com") {
		t.Fatalf("removed TXT record remained verified: err=%v", err)
	}
}

func TestAppDomainOwnershipGateUsesTheDurableClaim(t *testing.T) {
	store, err := OpenDomainClaimStore(filepath.Join(t.TempDir(), "domain-claims.json"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.claimFor("site-a", "example.com", "tenant-a", false)
	if err != nil {
		t.Fatal(err)
	}
	original := lookupDomainTXT
	t.Cleanup(func() { lookupDomainTXT = original })
	lookupDomainTXT = func(context.Context, string) ([]string, error) { return []string{claim.Token}, nil }
	app := &App{Domains: store}
	if err := app.verifyCustomerDomain(context.Background(), "site-a", "example.com", "tenant-a"); err != nil {
		t.Fatalf("ownership gate rejected valid claim: %v", err)
	}
	lookupDomainTXT = func(context.Context, string) ([]string, error) { return nil, nil }
	if err := app.verifyCustomerDomain(context.Background(), "site-a", "example.com", "tenant-a"); err == nil {
		t.Fatal("ownership gate accepted missing TXT claim")
	}
}

func TestDomainVerificationRequiresCurrentTenantOwnership(t *testing.T) {
	store, err := OpenDomainClaimStore(filepath.Join(t.TempDir(), "domain-claims.json"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.claimFor("blog", "a.example.com", "tenant-a", true)
	if err != nil {
		t.Fatal(err)
	}
	old.State, old.VerifiedAt = "verified", time.Now().UTC()
	old.ExpiresAt = time.Now().Add(-time.Minute).UTC()
	if err := store.save(old); err != nil {
		t.Fatal(err)
	}
	current, err := store.claimFor("blog", "b.example.com", "tenant-b", true)
	if err != nil {
		t.Fatal(err)
	}
	current.State, current.VerifiedAt = "verified", time.Now().UTC()
	if err := store.save(current); err != nil {
		t.Fatal(err)
	}
	refs := store.reservationGenerations("blog", "tenant-b")
	if len(refs) != 1 || refs[0].Domain != current.Domain {
		t.Fatalf("tenant B reservation generations = %+v", refs)
	}
	if err := store.setReservation("blog", "tenant-b", refs, false); err != nil {
		t.Fatal(err)
	}
	gotOld, _ := store.get(old.Domain)
	gotCurrent, _ := store.get(current.Domain)
	if !gotOld.expired(time.Now()) || gotOld.Tenant != "tenant-a" || gotOld.Token != old.Token {
		t.Fatalf("tenant A's expired claim changed: %+v", gotOld)
	}
	if !gotCurrent.ExpiresAt.IsZero() || gotCurrent.Tenant != "tenant-b" {
		t.Fatalf("tenant B's claim was not confirmed: %+v", gotCurrent)
	}
	if store.verifiedForTenant("blog", old.Domain, "tenant-b") || store.verifiedForTenant("blog", old.Domain, "tenant-a") {
		t.Fatal("an expired historical verification authorized the current site owner")
	}
	if !store.verifiedForTenant("blog", current.Domain, "tenant-b") || store.verifiedForTenant("blog", current.Domain, "tenant-a") {
		t.Fatal("verified claim was not bound exclusively to tenant B")
	}
	// A failed asynchronous creation may re-arm only B's exact generation.
	if err := store.setReservation("blog", "tenant-b", refs, true); err != nil {
		t.Fatal(err)
	}
	gotOld, _ = store.get(old.Domain)
	gotCurrent, _ = store.get(current.Domain)
	if !gotOld.expired(time.Now()) || gotCurrent.ExpiresAt.IsZero() || gotCurrent.Token != current.Token {
		t.Fatalf("generation-scoped re-arm affected the wrong claim: old=%+v current=%+v", gotOld, gotCurrent)
	}
}

func TestDomainClaimPruningIsBoundedAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "domain-claims.json")
	store, err := OpenDomainClaimStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		claim, err := store.claimFor("site", fmt.Sprintf("old-%d.example.com", i), "tenant", true)
		if err != nil {
			t.Fatal(err)
		}
		claim.ExpiresAt = now.Add(-time.Minute)
		if err := store.save(claim); err != nil {
			t.Fatal(err)
		}
	}
	confirmed, err := store.claimFor("site", "live.example.com", "tenant", false)
	if err != nil {
		t.Fatal(err)
	}
	confirmed.State, confirmed.VerifiedAt = "verified", now
	if err := store.save(confirmed); err != nil {
		t.Fatal(err)
	}
	if n, err := store.pruneExpired(now, 2); err != nil || n != 2 {
		t.Fatalf("first prune = %d, %v; want two", n, err)
	}
	if len(store.values) != 2 {
		t.Fatalf("claims after bounded prune = %d, want one expired and one confirmed", len(store.values))
	}
	reopened, err := OpenDomainClaimStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := reopened.pruneExpired(now, domainClaimPruneBatch); err != nil || n != 1 {
		t.Fatalf("second prune after reopen = %d, %v; want one", n, err)
	}
	if _, ok := reopened.get("live.example.com"); !ok || len(reopened.values) != 1 {
		t.Fatal("pruning removed a confirmed claim or failed to persist")
	}
}

func TestDomainClaimPendingLimitAndStateSizeAreEnforced(t *testing.T) {
	store, err := OpenDomainClaimStore(filepath.Join(t.TempDir(), "domain-claims.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxPendingDomainClaimsPerTenant; i++ {
		if _, err := store.claimFor("site", fmt.Sprintf("pending-%d.example.com", i), "tenant", true); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
	}
	if _, err := store.claimFor("site", "over-limit.example.com", "tenant", true); err == nil || !strings.Contains(err.Error(), "pending domain claim limit") {
		t.Fatalf("over-limit claim error = %v", err)
	}
	claim, _ := store.get("pending-0.example.com")
	claim.LastError = strings.Repeat("x", maxDomainClaimStateBytes)
	if err := store.save(claim); err == nil || !strings.Contains(err.Error(), "exceeds 4 MiB") {
		t.Fatalf("oversized state error = %v", err)
	}
	after, _ := store.get(claim.Domain)
	if after.LastError != "" {
		t.Fatal("failed oversized persistence changed in-memory state")
	}
}
