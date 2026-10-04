package stepanel

import (
	"encoding/json"
	"net/http"
	"net/mail"
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
	var input struct{ Domain, Email string }
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
	payload, err := json.Marshal(durableCertificateRequest{Domain: input.Domain, Email: input.Email, Actor: a.Auth.UsernameForRequest(r)})
	if err != nil {
		http.Error(w, "could not encode certificate job", http.StatusInternalServerError)
		return
	}
	job, _, err := a.Jobs.EnqueueIdempotent("certificate.issue", input.Domain, "", payload, 3)
	if err != nil {
		http.Error(w, "could not persist certificate job", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status": "queued"})
}
