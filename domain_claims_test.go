package main

import (
	"context"
	"path/filepath"
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
	claim, err := store.claim("site-a", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	original := lookupDomainTXT
	t.Cleanup(func() { lookupDomainTXT = original })
	lookupDomainTXT = func(context.Context, string) ([]string, error) { return []string{claim.Token}, nil }
	app := &App{Domains: store}
	if err := app.verifyCustomerDomain(context.Background(), "site-a", "example.com"); err != nil {
		t.Fatalf("ownership gate rejected valid claim: %v", err)
	}
	lookupDomainTXT = func(context.Context, string) ([]string, error) { return nil, nil }
	if err := app.verifyCustomerDomain(context.Background(), "site-a", "example.com"); err == nil {
		t.Fatal("ownership gate accepted missing TXT claim")
	}
}
