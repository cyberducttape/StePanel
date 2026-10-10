package stepanel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

// Long-running site operations (Git checkout and build, container builds,
// dependency installs, staging creation, restore-to-staging) are durable
// "site.operation" jobs. The HTTP request is authenticated, CSRF-checked,
// and authorized for the target site, then answered with 202 and a job ID;
// the worker replays the original request against the operation's handler
// as the original requester, so validation, scopes, locks, and audit events
// are exactly those of the synchronous handler. No HTTP connection is held
// for the duration of a build, and a dropped browser does not cancel it.
const siteOperationKind = "site.operation"

// siteOperationDeadline bounds one operation in the worker. The largest
// handler budget is restore-to-staging and staging creation (30 minutes plus
// a database restore).
const siteOperationDeadline = 60 * time.Minute

// siteOperationBodyLimit covers the largest operation request (a release
// pipeline definition, 48 KiB).
const siteOperationBodyLimit = 64 << 10

// siteOperationInterrupted is recorded when a worker stopped mid-operation.
// The operation is not replayed automatically: the worker first repairs the
// site (recoverInterruptedSiteOperation), and repeating a deployment the
// requester may have since superseded is not theirs to have decided for them.
const siteOperationInterrupted = "the operation was interrupted by a restart and was not repeated; check the site and run it again"

type siteOperationRoute struct {
	handler func(*App) http.HandlerFunc
	// site names the site the operation mutates. It owns the job, so
	// operations on one site run one at a time.
	site func(path string, body []byte) string
}

var siteOperationRoutes = map[string]siteOperationRoute{
	"release.pipeline":          {handler: func(a *App) http.HandlerFunc { return a.releasePipeline }, site: siteFromBody},
	"runner.build":              {handler: func(a *App) http.HandlerFunc { return a.runnerBuild }, site: siteFromBody},
	"git.deploy":                {handler: func(a *App) http.HandlerFunc { return a.gitDeploy }, site: siteFromBody},
	"composer.install":          {handler: func(a *App) http.HandlerFunc { return a.composer }, site: siteFromPath("/api/composer/")},
	"node.tooling":              {handler: func(a *App) http.HandlerFunc { return a.nodeTooling }, site: siteFromBody},
	"python.deploy":             {handler: func(a *App) http.HandlerFunc { return a.pythonDeploy }, site: siteFromBody},
	"staging.create":            {handler: func(a *App) http.HandlerFunc { return a.stagingCreate }, site: siteFromBody},
	"backup.restore-to-staging": {handler: func(a *App) http.HandlerFunc { return a.backupRestoreToStaging }, site: siteFromBody},
	"backup.offsite-to-staging": {handler: func(a *App) http.HandlerFunc { return a.backupRestoreOffsiteToStaging }, site: siteFromBody},
}

func siteFromBody(_ string, body []byte) string {
	var input struct {
		Site string `json:"site"`
	}
	if json.Unmarshal(body, &input) != nil {
		return ""
	}
	return safeUser(input.Site)
}

func siteFromPath(prefix string) func(string, []byte) string {
	return func(path string, _ []byte) string {
		site, _, _ := strings.Cut(strings.TrimPrefix(path, prefix), "/")
		return safeUser(site)
	}
}

type siteOperationPayload struct {
	Operation string   `json:"operation"`
	Path      string   `json:"path"`
	Query     string   `json:"query,omitempty"`
	Body      []byte   `json:"body,omitempty"`
	Site      string   `json:"site"`
	Actor     string   `json:"actor,omitempty"`
	Token     bool     `json:"token,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
	Webhook   bool     `json:"webhook,omitempty"`
}

// replayedIdentityKey carries a browser-session requester into the worker
// that replays their site operation. API-token requesters are replayed with
// the token identity and scopes that Auth.Require recorded.
type replayedIdentityKey struct{}

// siteOperation answers POST requests for a long operation with a queued
// durable job. Other methods (Composer status) are served synchronously.
func (a *App) siteOperation(name string) http.HandlerFunc {
	route, ok := siteOperationRoutes[name]
	if !ok {
		panic("unknown site operation " + name)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			route.handler(a)(w, r)
			return
		}
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, siteOperationBodyLimit))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "request body is too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		site := route.site(r.URL.Path, body)
		if site == "" {
			http.Error(w, "a valid site is required", http.StatusUnprocessableEntity)
			return
		}
		if _, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", http.StatusForbidden); !ok {
			return
		}
		scopes, _ := r.Context().Value(apiTokenScopesKey{}).([]string)
		a.enqueueSiteOperation(w, r, siteOperationPayload{
			Operation: name,
			Path:      r.URL.Path,
			Query:     r.URL.RawQuery,
			Body:      body,
			Site:      site,
			Actor:     a.Auth.UsernameForRequest(r),
			Token:     a.Auth.IsAPITokenRequest(r),
			Scopes:    scopes,
		})
	}
}

func (a *App) enqueueSiteOperation(w http.ResponseWriter, r *http.Request, payload siteOperationPayload) {
	operationKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if operationKey != "" && !validJobOperationKey(operationKey) {
		http.Error(w, "invalid Idempotency-Key", http.StatusUnprocessableEntity)
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "could not encode operation", http.StatusInternalServerError)
		return
	}
	job, _, err := a.Jobs.EnqueueIdempotent(siteOperationKind, payload.Site, operationKey, data, 1)
	if err != nil {
		http.Error(w, "could not queue operation", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID, "operation": payload.Operation, "site": payload.Site})
}

// handleSiteOperationJob replays a queued site operation. A 2xx response
// body becomes the job output; any other response fails the job with the
// handler's message.
func (a *App) handleSiteOperationJob(ctx context.Context, item Job) ([]byte, error) {
	var payload siteOperationPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		return nil, fmt.Errorf("decode site operation: %w", err)
	}
	route, ok := siteOperationRoutes[payload.Operation]
	if !ok || payload.Site == "" || !strings.HasPrefix(payload.Path, "/api/") {
		return nil, fmt.Errorf("invalid site operation %q", payload.Operation)
	}
	if item.Progress > 0 {
		return nil, a.recoverInterruptedSiteOperation(ctx, payload.Site)
	}
	// Mark the operation started, so a worker that dies mid-operation leaves
	// a record that the next claim recognizes as interrupted.
	if err := a.Jobs.UpdateClaim(item.ID, item.LeaseOwner, 1); err != nil {
		return nil, fmt.Errorf("record operation start: %w", err)
	}
	operationCtx, cancel := context.WithTimeout(ctx, siteOperationDeadline)
	defer cancel()
	switch {
	case payload.Webhook:
		// Re-read the webhook policy: a secret or allowlist revoked while the
		// delivery was queued must stop the deployment.
		if a.Webhooks == nil {
			return nil, errors.New("per-site webhook configuration is unavailable")
		}
		config, err := a.Webhooks.GetWebhookConfig(payload.Site)
		if err != nil {
			return nil, fmt.Errorf("load webhook configuration: %w", err)
		}
		if config == nil || config.WebhookSecret == "" {
			return nil, errors.New("the webhook for this site was removed before the deployment ran")
		}
		config.Site = payload.Site
		operationCtx = context.WithValue(operationCtx, gitWebhookSiteKey{}, newAuthenticatedWebhook(config))
	case payload.Token:
		operationCtx = context.WithValue(operationCtx, apiTokenUsernameKey{}, payload.Actor)
		operationCtx = context.WithValue(operationCtx, apiTokenScopesKey{}, payload.Scopes)
	case payload.Actor != "":
		operationCtx = context.WithValue(operationCtx, replayedIdentityKey{}, payload.Actor)
	}
	target := payload.Path
	if payload.Query != "" {
		target += "?" + payload.Query
	}
	request := httptest.NewRequestWithContext(operationCtx, http.MethodPost, target, bytes.NewReader(payload.Body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "127.0.0.1:0"
	response := httptest.NewRecorder()
	route.handler(a)(response, request)
	body := bytes.TrimSpace(response.Body.Bytes())
	if response.Code < 200 || response.Code > 299 {
		return nil, errors.New(siteOperationFailure(response.Code, body))
	}
	if len(body) == 0 {
		return []byte(`{}`), nil
	}
	if json.Valid(body) {
		return body, nil
	}
	return json.Marshal(map[string]string{"message": string(body)})
}

// siteOperationFailure is the job error for a non-2xx handler response: the
// handler's own message, which is already written for the requester.
func siteOperationFailure(status int, body []byte) string {
	var structured struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	message := string(body)
	if json.Unmarshal(body, &structured) == nil {
		if structured.Message != "" {
			message = structured.Message
		} else if structured.Error != "" {
			message = structured.Error
		}
	}
	if message == "" {
		message = http.StatusText(status)
	}
	const limit = 2000
	if len(message) > limit {
		message = message[:limit] + "…"
	}
	return message
}

// recoverInterruptedSiteOperation repairs a site whose operation a worker
// crash interrupted, using the same recovery panel startup runs (journals of
// interrupted release activations and site transactions), limited to this
// site and under its lease, then reports the operation as interrupted.
func (a *App) recoverInterruptedSiteOperation(ctx context.Context, site string) error {
	siteCtx, unlock, err := a.acquireSiteMutationLockContext(ctx, site)
	if err != nil {
		return fmt.Errorf("%s; recovery could not lock the site: %w", siteOperationInterrupted, err)
	}
	defer unlock()
	manager := a.siteManager
	if manager == nil {
		if manager, err = newSiteManagerForConfig(a.Config); err != nil {
			return fmt.Errorf("%s; recovery is unavailable: %w", siteOperationInterrupted, err)
		}
	}
	if err := a.reconcileInterruptedSiteWithLease(siteCtx, site, manager); err != nil {
		return fmt.Errorf("%s; recovery of the site is incomplete and is retried when the panel restarts: %w", siteOperationInterrupted, err)
	}
	return errors.New(siteOperationInterrupted)
}

// reconcileInterruptedSiteWithLease completes recovery journals left by an
// earlier attempt while the caller holds the site's mutation lease. Durable
// retries use this before creating a new journal; otherwise a retry can leave
// two generations of site state and a later lifecycle operation can strand the
// older journal permanently.
func (a *App) reconcileInterruptedSiteWithLease(ctx context.Context, site string, manager siteauthority.Manager) error {
	if manager == nil {
		var err error
		manager, err = newSiteManagerForConfig(a.Config)
		if err != nil {
			return err
		}
	}
	held := func(context.Context, string) (context.Context, func(), error) {
		return ctx, func() {}, nil
	}
	if failures := recoverInterruptedState(a.Config, manager, held, site); len(failures) > 0 {
		return errors.Join(failures...)
	}
	return nil
}
