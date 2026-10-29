package stepanel

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"

	"github.com/cyberducttape/StePanel/internal/domainname"
)

type CertificateResult struct {
	Domain string `json:"domain"`
	Status string `json:"status"`
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
	Exists bool   `json:"exists"`
	SHA256 string `json:"sha256,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}

func certificateLineageState(domain string) (certificateLineageSnapshot, error) {
	path := filepath.Join("/etc/letsencrypt/live", domain, "fullchain.pem")
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return certificateLineageSnapshot{}, nil
	}
	if err != nil {
		return certificateLineageSnapshot{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return certificateLineageSnapshot{}, fmt.Errorf("certificate lineage is not a bounded regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return certificateLineageSnapshot{}, err
	}
	digest := sha256.Sum256(data)
	return certificateLineageSnapshot{Exists: true, SHA256: fmt.Sprintf("%x", digest), Bytes: info.Size()}, nil
}
