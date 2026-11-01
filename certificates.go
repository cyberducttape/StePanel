package stepanel

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/domainname"
)

type CertificateResult struct {
	Domain        string    `json:"domain"`
	Status        string    `json:"status"`
	VerifiedAt    time.Time `json:"verified_at,omitempty"`
	LineageSHA256 string    `json:"lineage_sha256,omitempty"`
	Checks        []string  `json:"checks,omitempty"`
}

func (a *App) issueCertificate(w http.ResponseWriter, r *http.Request) {
	if a.Config.WebServer != "apache" {
		http.Error(w, "Caddy manages certificates automatically; manual issuance is available only for Apache", http.StatusConflict)
		return
	}
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	operationKey, err := requestOperationKey(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	var input struct {
		Domain       string `json:"domain"`
		Email        string `json:"email"`
		PreviewToken string `json:"preview_token,omitempty"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	input.Domain = strings.ToLower(strings.TrimSpace(input.Domain))
	input.Email = strings.TrimSpace(input.Email)
	if !domainname.Valid(input.Domain) {
		http.Error(w, "invalid domain", 422)
		return
	}
	address, err := mail.ParseAddress(input.Email)
	if err != nil || address.Address != input.Email {
		http.Error(w, "invalid email address", 422)
		return
	}
	if a.Config.Certbot == "" {
		http.Error(w, "certificate helper is not installed", 503)
		return
	}
	before, err := certificateLineageState(input.Domain)
	if err != nil {
		http.Error(w, "could not safely inspect the current certificate lineage", http.StatusServiceUnavailable)
		return
	}
	proposed := struct {
		Domain      string `json:"domain"`
		Email       string `json:"email"`
		Certificate string `json:"certificate_path"`
		WebServer   string `json:"web_server"`
	}{input.Domain, input.Email, filepath.Join("/etc/letsencrypt/live", input.Domain, "fullchain.pem"), a.Config.WebServer}
	if strings.HasSuffix(r.URL.Path, "/preview") {
		preview, err := a.makeChangePreview("certificate.issue", before, proposed,
			[]string{"Request a publicly trusted certificate for the domain.", "Certbot may update the Apache TLS virtual-host configuration and reload Apache.", "HTTP or DNS validation must reach this host; issuance can fail without changing the active certificate."},
			[]string{"If issuance succeeds but the site fails validation, inspect the Certbot lineage and Apache configuration before rollback.", "The prior Certbot lineage is retained under /etc/letsencrypt/archive; restore the prior certificate and revalidate Apache if replacement must be undone."})
		if err != nil {
			http.Error(w, "could not create certificate change preview", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, preview)
		return
	}
	if err := changePreviewRequired(input.PreviewToken); err != nil || !a.verifyChangePreview(input.PreviewToken, "certificate.issue", before, proposed) {
		http.Error(w, "certificate state changed or its preview expired; generate a new preview", http.StatusConflict)
		return
	}
	payload, err := json.Marshal(durableCertificateRequest{Domain: input.Domain, Email: input.Email, Actor: a.Auth.UsernameForRequest(r), Before: before})
	if err != nil {
		http.Error(w, "could not encode certificate job", http.StatusInternalServerError)
		return
	}
	job, _, err := a.Jobs.EnqueueIdempotent("certificate.issue", input.Domain, operationKey, payload, 3)
	if err != nil {
		http.Error(w, "could not persist certificate job", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status": "queued"})
}

type certificateLineageSnapshot struct {
	Exists    bool      `json:"exists"`
	SHA256    string    `json:"sha256,omitempty"`
	Bytes     int64     `json:"bytes,omitempty"`
	NotAfter  time.Time `json:"not_after,omitempty"`
	HostMatch bool      `json:"host_match,omitempty"`
}

// sameContent compares the lineage identity. NotAfter and HostMatch are
// derived from the hashed bytes, so they are excluded: that keeps payloads
// queued before those fields existed comparable without weakening the check.
func (s certificateLineageSnapshot) sameContent(other certificateLineageSnapshot) bool {
	return s.Exists == other.Exists && s.SHA256 == other.SHA256 && s.Bytes == other.Bytes
}

func certificateLineageState(domain string) (certificateLineageSnapshot, error) {
	if !domainname.Valid(domain) {
		return certificateLineageSnapshot{}, fmt.Errorf("invalid certificate domain")
	}
	// Certbot's live entries are symlinks into archive/, so root the lookup at
	// /etc/letsencrypt and keep the validated domain as a single path segment.
	root, err := os.OpenRoot("/etc/letsencrypt")
	if err != nil {
		return certificateLineageSnapshot{}, err
	}
	defer root.Close()
	path := filepath.Join("live", domain, "fullchain.pem")
	file, err := root.Open(path)
	if os.IsNotExist(err) {
		return certificateLineageSnapshot{}, nil
	}
	if err != nil {
		return certificateLineageSnapshot{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() > 4<<20 {
		return certificateLineageSnapshot{}, fmt.Errorf("certificate lineage changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4<<20+1))
	if err != nil {
		return certificateLineageSnapshot{}, err
	}
	if len(data) > 4<<20 {
		return certificateLineageSnapshot{}, fmt.Errorf("certificate lineage exceeds 4 MiB")
	}
	evidence, parseErr := certificateLineageEvidence(domain, data)
	if parseErr != nil {
		// Preserve a stable before-state fingerprint even if the current
		// certificate is malformed, so an administrator can preview replacement.
		return certificateLineageSnapshot{Exists: true, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Bytes: int64(len(data))}, nil
	}
	return evidence, nil
}

func certificateLineageEvidence(domain string, data []byte) (certificateLineageSnapshot, error) {
	if !domainname.Valid(domain) || len(data) == 0 || len(data) > 4<<20 {
		return certificateLineageSnapshot{}, errors.New("certificate lineage is invalid or exceeds its size limit")
	}
	digest := sha256.Sum256(data)
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return certificateLineageSnapshot{}, errors.New("active certificate lineage has no PEM certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return certificateLineageSnapshot{}, fmt.Errorf("parse active certificate lineage: %w", err)
	}
	hostErr := certificate.VerifyHostname(domain)
	return certificateLineageSnapshot{Exists: true, SHA256: fmt.Sprintf("%x", digest), Bytes: int64(len(data)), NotAfter: certificate.NotAfter.UTC(), HostMatch: hostErr == nil}, nil
}
