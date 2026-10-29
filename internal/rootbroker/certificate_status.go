package rootbroker

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/cyberducttape/StePanel/internal/domainname"
)

// CertbotStoreRoot is the local Certbot store. Its live/ and archive/
// directories are root-only, so production reads go through the broker.
const CertbotStoreRoot = "/etc/letsencrypt"

const certificateFileLimit = 4 << 20

// CertificateStatus is the public metadata of a local certificate: what a TLS
// handshake would disclose anyway. Private keys are never opened.
type CertificateStatus struct {
	Domain   string    `json:"domain"`
	Status   string    `json:"status"`
	NotAfter time.Time `json:"not_after,omitempty"`
	DaysLeft int       `json:"days_left,omitempty"`
	Detail   string    `json:"detail,omitempty"`
}

// InspectCertificate reads live/<domain>/fullchain.pem below storeRoot. The
// os.Root keeps Certbot's relative live -> archive links inside the store.
// Details are generic because they reach the site's customers.
func InspectCertificate(storeRoot, domain string, now time.Time) CertificateStatus {
	check := CertificateStatus{Domain: domain, Status: "unknown"}
	if !domainname.Valid(domain) {
		check.Detail = "The stored route domain is invalid; certificate inspection was skipped."
		return check
	}
	root, err := os.OpenRoot(storeRoot)
	if err != nil {
		check.Detail = "The local certificate store is unavailable."
		return check
	}
	defer root.Close()
	file, err := root.Open(filepath.Join("live", domain, "fullchain.pem"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			check.Status = "missing"
			check.Detail = "No local Certbot certificate was found for this route."
		} else {
			check.Detail = "The local certificate could not be read safely."
		}
		return check
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > certificateFileLimit {
		check.Status = "failed"
		check.Detail = "The certificate file is not a bounded regular file."
		return check
	}
	data, err := io.ReadAll(io.LimitReader(file, certificateFileLimit+1))
	if err != nil || len(data) > certificateFileLimit {
		check.Status = "failed"
		check.Detail = "The certificate file could not be read within its size limit."
		return check
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		check.Status = "failed"
		check.Detail = "The certificate file does not contain a parseable PEM certificate."
		return check
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		check.Status = "failed"
		check.Detail = "The certificate is malformed."
		return check
	}
	check.NotAfter = cert.NotAfter.UTC()
	check.DaysLeft = int(cert.NotAfter.Sub(now).Hours() / 24)
	switch {
	case cert.VerifyHostname(domain) != nil:
		check.Status = "hostname_mismatch"
		check.Detail = "The local certificate does not cover this route's hostname."
	case now.After(cert.NotAfter):
		check.Status = "expired"
		check.Detail = "The local certificate has expired."
	case check.DaysLeft <= 14:
		check.Status = "expiring"
		check.Detail = "The local certificate expires within 14 days."
	default:
		check.Status = "valid"
		check.Detail = "The local certificate covers this hostname and is not near expiry; the live listener was not probed."
	}
	return check
}
