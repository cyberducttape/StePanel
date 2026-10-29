package rootbroker

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCertbotLineage lays out a store the way Certbot does: live/<domain>
// holds relative symlinks into archive/<domain>.
func writeCertbotLineage(t *testing.T, store, domain string, dnsNames []string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: dnsNames[0]}, DNSNames: dnsNames, NotBefore: notAfter.Add(-90 * 24 * time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(store, "archive", domain)
	live := filepath.Join(store, "live", domain)
	for _, dir := range []string{archive, live} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(archive, "fullchain1.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "archive", domain, "fullchain1.pem"), filepath.Join(live, "fullchain.pem")); err != nil {
		t.Fatal(err)
	}
}

func TestInspectCertificateStatuses(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	store := t.TempDir()
	writeCertbotLineage(t, store, "valid.example.test", []string{"valid.example.test"}, now.Add(60*24*time.Hour))
	writeCertbotLineage(t, store, "soon.example.test", []string{"soon.example.test"}, now.Add(5*24*time.Hour))
	writeCertbotLineage(t, store, "old.example.test", []string{"old.example.test"}, now.Add(-24*time.Hour))
	writeCertbotLineage(t, store, "wrong.example.test", []string{"other.example.test"}, now.Add(60*24*time.Hour))
	if err := os.MkdirAll(filepath.Join(store, "live", "garbage.example.test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "live", "garbage.example.test", "fullchain.pem"), []byte("not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}
	for domain, want := range map[string]string{
		"valid.example.test":   "valid",
		"soon.example.test":    "expiring",
		"old.example.test":     "expired",
		"wrong.example.test":   "hostname_mismatch",
		"garbage.example.test": "failed",
		"absent.example.test":  "missing",
		"../etc":               "unknown",
	} {
		if got := InspectCertificate(store, domain, now); got.Status != want || got.Detail == "" {
			t.Errorf("%s: status = %q (%q), want %q", domain, got.Status, got.Detail, want)
		}
	}
	if got := InspectCertificate(store, "valid.example.test", now); got.DaysLeft != 60 || !got.NotAfter.Equal(now.Add(60*24*time.Hour)) {
		t.Fatalf("valid certificate expiry = %v / %d days", got.NotAfter, got.DaysLeft)
	}
}

func TestInspectCertificateDoesNotFollowLinksOutOfStore(t *testing.T) {
	now := time.Now()
	outside := t.TempDir()
	writeCertbotLineage(t, outside, "escape.example.test", []string{"escape.example.test"}, now.Add(60*24*time.Hour))
	store := t.TempDir()
	if err := os.MkdirAll(filepath.Join(store, "live", "escape.example.test"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "archive", "escape.example.test", "fullchain1.pem")
	if err := os.Symlink(target, filepath.Join(store, "live", "escape.example.test", "fullchain.pem")); err != nil {
		t.Fatal(err)
	}
	if got := InspectCertificate(store, "escape.example.test", now); got.Status == "valid" {
		t.Fatalf("certificate outside the store was read: %#v", got)
	}
}

func TestCertificateStatusRequestValidation(t *testing.T) {
	validator := NewValidator("/var/www")
	if err := validator.validateSiteRequest(&SiteRequest{Action: "certificate-status", Site: "demo", Domain: "demo.example.test"}); err != nil {
		t.Fatalf("valid certificate status request rejected: %v", err)
	}
	for name, req := range map[string]*SiteRequest{
		"missing domain":        {Action: "certificate-status", Site: "demo"},
		"traversal domain":      {Action: "certificate-status", Site: "demo", Domain: "../../etc"},
		"caller-selected user":  {Action: "certificate-status", Site: "demo", Domain: "demo.example.test", SiteUser: "root"},
		"extra field":           {Action: "certificate-status", Site: "demo", Domain: "demo.example.test", PHPVersion: "8.3"},
		"domain on other route": {Action: "fpm-status", Site: "demo", Domain: "demo.example.test"},
	} {
		if err := validator.validateSiteRequest(req); err == nil {
			t.Errorf("%s: request accepted", name)
		}
	}
	if requestRequiresFencing(&Request{RequestType: "site", Site: &SiteRequest{Action: "certificate-status", Site: "demo", Domain: "demo.example.test"}}) {
		t.Fatal("read-only certificate status must not require a fencing token")
	}
}

func TestBrokerCertificateStatusReadsConfiguredStore(t *testing.T) {
	broker, err := newTestBroker(t, t.TempDir(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	broker.certificateStore = t.TempDir()
	writeCertbotLineage(t, broker.certificateStore, "demo.example.test", []string{"demo.example.test"}, time.Now().Add(60*24*time.Hour))
	response, err := broker.Execute(context.Background(), &Request{RequestType: "site", Site: &SiteRequest{Action: "certificate-status", Site: "demo", Domain: "demo.example.test"}})
	if err != nil || !response.OK {
		t.Fatalf("certificate status response = %#v, error = %v", response, err)
	}
	var details SiteResponse
	if err := json.Unmarshal(response.Details, &details); err != nil || details.Certificate == nil || details.Certificate.Status != "valid" {
		t.Fatalf("certificate status details = %s, error = %v", response.Details, err)
	}
}
