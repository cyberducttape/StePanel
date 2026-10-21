package stepanel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cyberducttape/StePanel/internal/domainname"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

var gitRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@+-]{0,127}$`)
var gitCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var webhookDeliveryIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

const (
	gitCloneAddressSpaceBytes = uint64(2 << 30)
	gitCloneMemoryHeadroom    = uint64(512 << 20)
	gitCloneStageMultiplier   = uint64(3) // packed objects + checkout + filesystem metadata/headroom
	gitCloneMaxStageBytes     = int64(1 << 40)
)

func gitCloneStageLimit(treeLimit int64) (int64, error) {
	if treeLimit < 1 || uint64(treeLimit) > uint64(gitCloneMaxStageBytes)/gitCloneStageMultiplier {
		return 0, errors.New("configured Git release limit cannot be safely admitted within the 1 TiB staging quota ceiling")
	}
	return int64(uint64(treeLimit) * gitCloneStageMultiplier), nil
}

// enableGitStageQuota installs the aggregate staging quota through the root
// broker. Development runs have no broker and no root helper, so they keep
// the capacity reservation and per-file limit only and report project ID 0;
// production and the lab broker always go through the broker and fail closed.
func (a *App) enableGitStageQuota(ctx context.Context, site, destination string, limit int64) (uint32, error) {
	if !a.Config.Production && !labDirectRootBrokerEnabled() {
		return 0, nil
	}
	client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", a.Config.WebRoot)
	if err != nil {
		return 0, err
	}
	return client.GitStageLimit(ctx, site, destination, limit)
}

func gitStageCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	// Cleanup must outlive ordinary request cancellation while retaining the
	// fencing token. The broker still rejects cleanup after lease takeover.
	return context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
}

func (a *App) disableGitStageQuota(ctx context.Context, site, destination string, projectID uint32) error {
	if projectID == 0 && !a.Config.Production && !labDirectRootBrokerEnabled() {
		return nil // enableGitStageQuota installed no quota in development.
	}
	ctx, cancel := gitStageCleanupContext(ctx)
	defer cancel()
	client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", a.Config.WebRoot)
	if err != nil {
		return err
	}
	return client.GitStageClear(ctx, site, destination, projectID)
}

func (a *App) acquireGitCloneAdmission(ctx context.Context) (func(), error) {
	available, err := availableSystemMemoryBytes()
	if err != nil {
		return nil, fmt.Errorf("inspect available memory before Git clone: %w", err)
	}
	if available < gitCloneAddressSpaceBytes+gitCloneMemoryHeadroom {
		return nil, fmt.Errorf("insufficient memory for Git clone: %d bytes available; need at least %d", available, gitCloneAddressSpaceBytes+gitCloneMemoryHeadroom)
	}
	if a.ResourceBudget != nil {
		if !a.ResourceBudget.AcquireSlotContext(ctx, "git_clone") {
			return nil, ctx.Err()
		}
		return func() { a.ResourceBudget.ReleaseSlot("git_clone") }, nil
	}
	return func() {}, nil
}

func availableSystemMemoryBytes() (uint64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "MemAvailable:" {
			continue
		}
		kib, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || kib > ^uint64(0)/1024 {
			return 0, errors.New("invalid MemAvailable value")
		}
		available := kib * 1024
		if remaining, limited, limitErr := cgroupMemoryRemaining(); limitErr != nil {
			return 0, limitErr
		} else if limited {
			available = min(available, remaining)
		}
		return available, nil
	}
	return 0, errors.New("MemAvailable is missing from /proc/meminfo")
}

func cgroupMemoryRemaining() (uint64, bool, error) {
	memberships, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return 0, false, err
	}
	var unifiedPath, memoryPath string
	for _, line := range strings.Split(string(memberships), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "0" && parts[1] == "" {
			unifiedPath = parts[2]
		}
		for _, controller := range strings.Split(parts[1], ",") {
			if controller == "memory" {
				memoryPath = parts[2]
			}
		}
	}
	if unifiedPath != "" {
		return readCgroupMemoryAncestors("/sys/fs/cgroup", unifiedPath, "memory.max", "memory.current", true)
	}
	if memoryPath != "" {
		return readCgroupMemoryAncestors("/sys/fs/cgroup/memory", memoryPath, "memory.limit_in_bytes", "memory.usage_in_bytes", false)
	}
	// Hosts without a cgroup memory controller still use MemAvailable.
	return 0, false, nil
}

func readCgroupMemoryAncestors(base, groupPath, limitName, usageName string, supportsMax bool) (uint64, bool, error) {
	for _, component := range strings.Split(filepath.ToSlash(groupPath), "/") {
		if component == ".." {
			return 0, false, errors.New("invalid cgroup path")
		}
	}
	cleanGroup := filepath.Clean("/" + strings.TrimLeft(groupPath, "/"))
	currentDir := filepath.Join(base, strings.TrimLeft(cleanGroup, "/"))
	base = filepath.Clean(base)
	if rel, err := filepath.Rel(base, currentDir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return 0, false, errors.New("invalid cgroup path")
	}
	var smallest uint64
	limited := false
	for dir := currentDir; ; {
		limit, limitErr := os.ReadFile(filepath.Join(dir, limitName))
		usage, usageErr := os.ReadFile(filepath.Join(dir, usageName))
		if limitErr == nil && usageErr == nil {
			remaining, applies, err := parseCgroupMemoryRemaining(string(limit), string(usage), supportsMax)
			if err != nil {
				return 0, false, err
			}
			if applies && (!limited || remaining < smallest) {
				smallest, limited = remaining, true
			}
		}
		if dir == base {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return smallest, limited, nil
}

func parseCgroupMemoryRemaining(limitText, currentText string, supportsMax bool) (uint64, bool, error) {
	limitText, currentText = strings.TrimSpace(limitText), strings.TrimSpace(currentText)
	if supportsMax && limitText == "max" {
		return 0, false, nil
	}
	limit, limitErr := strconv.ParseUint(limitText, 10, 64)
	current, currentErr := strconv.ParseUint(currentText, 10, 64)
	// The cgroup-v1 unlimited sentinel is close to MaxInt64 on 64-bit hosts.
	if !supportsMax && limitErr == nil && limit >= 1<<60 {
		return 0, false, nil
	}
	if limitErr != nil || currentErr != nil || current > limit {
		return 0, false, errors.New("invalid cgroup memory limit or usage")
	}
	return limit - current, true, nil
}

// WebhookReplayCache prevents replay attacks on webhook deliveries
type WebhookReplayCache struct {
	mu     sync.RWMutex
	db     *sql.DB
	cache  map[string]time.Time // legacy in-memory fallback for isolated tests
	maxAge time.Duration
}

// NewWebhookReplayCache creates a cache with bounded lifetime
func NewWebhookReplayCache(maxAge time.Duration) *WebhookReplayCache {
	rc := &WebhookReplayCache{
		cache:  make(map[string]time.Time),
		maxAge: maxAge,
	}
	return rc
}

func NewDurableWebhookReplayCache(db *sql.DB, maxAge time.Duration) *WebhookReplayCache {
	rc := NewWebhookReplayCache(maxAge)
	rc.db = db
	return rc
}

// Accept atomically admits a delivery ID for one site. The database primary
// key is the replay gate, so concurrent requests and process restarts cannot
// accept the same delivery twice.
func (rc *WebhookReplayCache) Accept(site, deliveryID string, timestamp time.Time) (bool, error) {
	if rc == nil || strings.TrimSpace(site) == "" || !webhookDeliveryIDPattern.MatchString(deliveryID) {
		return false, errors.New("webhook replay protection requires site and delivery ID")
	}
	now := time.Now().UTC()
	if timestamp.Before(now.Add(-rc.maxAge)) {
		return false, errors.New("webhook timestamp too old")
	}
	if timestamp.After(now.Add(rc.maxAge)) {
		return false, errors.New("webhook timestamp is too far in the future")
	}
	if rc.db != nil {
		_, err := rc.db.Exec(`DELETE FROM webhook_deliveries WHERE expires_at <= ?`, now.UnixNano())
		if err != nil {
			return false, fmt.Errorf("prune webhook deliveries: %w", err)
		}
		result, err := rc.db.Exec(`INSERT INTO webhook_deliveries(site, delivery_id, received_at, expires_at) VALUES (?, ?, ?, ?) ON CONFLICT(site, delivery_id) DO NOTHING`, site, deliveryID, now.UnixNano(), now.Add(rc.maxAge).UnixNano())
		if err != nil {
			return false, fmt.Errorf("record webhook delivery: %w", err)
		}
		count, err := result.RowsAffected()
		return count == 1, err
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	key := site + "\x00" + deliveryID
	if previous, exists := rc.cache[key]; exists && previous.After(now) {
		return false, nil
	}
	for existingKey, expiresAt := range rc.cache {
		if !expiresAt.After(now) {
			delete(rc.cache, existingKey)
		}
	}
	if len(rc.cache) >= 10000 {
		var oldestKey string
		var oldest time.Time
		for existingKey, expiresAt := range rc.cache {
			if oldestKey == "" || expiresAt.Before(oldest) {
				oldestKey, oldest = existingKey, expiresAt
			}
		}
		if oldestKey != "" {
			delete(rc.cache, oldestKey)
		}
	}
	rc.cache[key] = now.Add(rc.maxAge)
	return true, nil
}

type gitDeployRequest struct {
	Site       string `json:"site"`
	Repository string `json:"repository"`
	Ref        string `json:"ref"`
}

// gitRepository is deliberately narrower than Git's general URL syntax.  HTTPS
// remains available for public sources; SSH is available only as git@host:path
// and only after a per-site deploy key has been generated by the privileged
// helper.  Personal tokens, passwords, query strings, and arbitrary SSH users
// are never accepted.
type gitRepository struct {
	URL     string
	Host    string
	Private bool
}

var gitSSHRepositoryPattern = regexp.MustCompile(`^git@([A-Za-z0-9.-]+):([A-Za-z0-9._/-]+\.git)$`)

func parseGitRepository(raw, allowedHosts string) (gitRepository, error) {
	raw = strings.TrimSpace(raw)
	if match := gitSSHRepositoryPattern.FindStringSubmatch(raw); match != nil {
		if !gitHostAllowed(allowedHosts, strings.ToLower(match[1])) {
			return gitRepository{}, errors.New("repository host is not in STEPANEL_GIT_ALLOWED_HOSTS")
		}
		return gitRepository{URL: raw, Host: strings.ToLower(match[1]), Private: true}, nil
	}
	repository, err := url.Parse(raw)
	if err != nil || repository.Scheme != "https" || repository.User != nil || repository.Host == "" || repository.Port() != "" || repository.RawQuery != "" || repository.Fragment != "" || strings.ContainsAny(raw, "\x00\r\n") || !strings.HasSuffix(strings.ToLower(repository.Path), ".git") {
		return gitRepository{}, errors.New("repository must be an HTTPS .git URL without credentials or an SSH git@host:path.git URL")
	}
	if !gitHostAllowed(allowedHosts, repository.Hostname()) {
		return gitRepository{}, errors.New("repository host is not in STEPANEL_GIT_ALLOWED_HOSTS")
	}
	return gitRepository{URL: raw, Host: strings.ToLower(repository.Hostname())}, nil
}

type gitDeployResult struct {
	DeploymentID string `json:"deployment_id,omitempty"`
	Site         string `json:"site"`
	Repository   string `json:"repository"`
	Ref          string `json:"ref"`
	Commit       string `json:"commit"`
	Previous     string `json:"previous_release,omitempty"`
	// Activated names the release directory that is live once activation
	// has committed.
	Activated string `json:"activated_release,omitempty"`
	// Outcome is releaseOutcomeActivated or
	// releaseOutcomeReconciliationPending once the release is live.
	Outcome string `json:"outcome,omitempty"`
	// HistoryError is set when the host change succeeded but its deployment
	// history record could not be persisted.
	HistoryError string `json:"history_error,omitempty"`
}

// Activation is the commit point of a deployment or rollback: once the site
// serves the new release, the operation must never be reported as failed or
// cancelled, or an operator could retry a change that is already live. If the
// mutation lock or request context is lost after activation, the release is
// still reported as committed, and lock-dependent follow-up work (release
// retention) is left for the next locked operation.
const (
	releaseOutcomeActivated             = "activated"
	releaseOutcomeReconciliationPending = "committed_reconciliation_pending"
)

// finishCommittedRelease records the outcome of a release that activation
// has already made live.
func (a *App) finishCommittedRelease(ctx context.Context, siteRoot, stage, detail, release string, result *gitDeployResult, prune bool) {
	result.Activated = filepath.Base(release)
	result.Outcome = releaseOutcomeActivated
	if ctx.Err() != nil {
		result.Outcome = releaseOutcomeReconciliationPending
		detail += "; mutation lock lost after activation, release retention deferred"
	} else if prune {
		if err := pruneGitReleasesWithPolicy(siteRoot, a.Config.GitReleaseRetention, time.Duration(a.Config.GitReleaseMaxAgeHours)*time.Hour, a.Config.GitReleaseMaxBytes); err != nil {
			log.Printf("Git release retention for %s: %v", result.Site, err)
		}
	}
	if err := a.recordDeployment(result.Site, stage, "completed", detail, *result, release); err != nil {
		// The release is live; say so rather than reporting a failed deploy.
		result.HistoryError = "release activated but deployment history was not persisted: " + err.Error()
	}
}

type gitRollbackRequest struct {
	Site    string `json:"site"`
	Confirm string `json:"confirm"`
}

// gitWebhookSiteKey carries the authenticatedWebhook for a request that
// passed webhook signature verification. The site is derived from the URL
// path (/api/sites/git-webhook/:site) and validated against that site's
// per-site HMAC secret, so an authenticated webhook proves authority over
// exactly one site and no other. gitDeploy uses the value in this key —
// never input.Site from the body — as the trusted deploy target when the
// request came in via the webhook path.
type gitWebhookSiteKey struct{}

// authenticatedWebhook is the immutable authorization that gitWebhook
// verified: the site whose secret validated the signature and the
// repository/ref allowlists loaded in that same configuration read.
// gitDeploy enforces exactly this policy and never re-reads it, so a failed
// or changed second lookup can neither skip nor alter the restrictions.
type authenticatedWebhook struct {
	Site         string
	Repositories []string
	AllowedRefs  []string
}

func newAuthenticatedWebhook(config *GitWebhookConfig) authenticatedWebhook {
	return authenticatedWebhook{
		Site:         config.Site,
		Repositories: append([]string(nil), config.Repositories...),
		AllowedRefs:  append([]string(nil), config.AllowedRefs...),
	}
}

// webhookAuthorization returns the verified webhook authorization if the
// request came in through the webhook path and passed signature
// verification.
func webhookAuthorization(r *http.Request) (authenticatedWebhook, bool) {
	auth, ok := r.Context().Value(gitWebhookSiteKey{}).(authenticatedWebhook)
	return auth, ok && auth.Site != ""
}

// GitWebhookConfig stores per-site webhook configuration.
// Each site can have its own webhook secret, allowed repositories, and allowed refs.
// This prevents a single leaked secret from compromising all sites.
type GitWebhookConfig struct {
	Site          string    `json:"site"`
	Repositories  []string  `json:"repositories"` // allowed repository URLs
	AllowedRefs   []string  `json:"allowed_refs"` // allowed Git refs (branches/tags)
	WebhookSecret string    `json:"webhook_secret"`
	EnabledAt     time.Time `json:"enabled_at"`
	LastWebhookAt time.Time `json:"last_webhook_at,omitempty"`
}

type webhookConfigRequest struct {
	Secret       string   `json:"secret,omitempty"`
	Repositories []string `json:"repositories"`
	AllowedRefs  []string `json:"allowed_refs"`
}

// webhookConfig manages the per-site authorization material used by the
// signed webhook endpoint. A shared global secret is deliberately not an
// alternative: each site must have its own secret and explicit repository/ref
// allowlists.
func (a *App) webhookConfig(w http.ResponseWriter, r *http.Request) {
	if !a.Auth.CSRF(r) || !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator CSRF request required", http.StatusForbidden)
		return
	}
	prefix := "/api/admin/webhooks/"
	site := strings.TrimPrefix(r.URL.Path, prefix)
	if site == r.URL.Path || strings.ContainsAny(site, "/?") {
		http.Error(w, "site name required", http.StatusBadRequest)
		return
	}
	site = safeUser(site)
	if site == "" {
		http.Error(w, "invalid site", http.StatusUnprocessableEntity)
		return
	}
	if a.Webhooks == nil {
		http.Error(w, "webhook configuration unavailable", http.StatusServiceUnavailable)
		return
	}
	actor := a.Auth.UsernameForRequest(r)
	if actor == "" {
		actor = a.Auth.Username
	}
	operationCtx, release, lockErr := a.acquireSiteMutationLockContext(r.Context(), site)
	if lockErr != nil {
		http.Error(w, "webhook configuration is busy", http.StatusConflict)
		return
	}
	defer release()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "webhook configuration cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		// Disabling only removes deploy authority: a revocation, which the
		// audit ledger never blocks.
		if err := a.Webhooks.DisableWebhookConfig(site); err != nil {
			http.Error(w, "could not disable webhook", http.StatusInternalServerError)
			return
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "webhook configuration cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		RevocationAudit(a.Config.AuditLog, actor, "webhook.config.disabled", site, "per-site webhook disabled")
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodPost, http.MethodPut:
		var input webhookConfigRequest
		if err := decodeJSON(w, r, 8192, &input); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		secret := strings.TrimSpace(input.Secret)
		generated := false
		if secret == "" {
			var err error
			secret, err = randomSecret()
			if err != nil {
				http.Error(w, "could not generate webhook secret", http.StatusInternalServerError)
				return
			}
			generated = true
		}
		if len(secret) < 32 || strings.ContainsAny(secret, "\x00\r\n") {
			http.Error(w, "webhook secret must contain at least 32 characters", http.StatusUnprocessableEntity)
			return
		}
		if len(input.Repositories) == 0 || len(input.AllowedRefs) == 0 {
			http.Error(w, "at least one repository and ref pattern are required", http.StatusUnprocessableEntity)
			return
		}
		for _, repository := range input.Repositories {
			if _, err := parseGitRepository(repository, a.Config.GitAllowedHosts); err != nil {
				http.Error(w, "invalid webhook repository: "+err.Error(), http.StatusUnprocessableEntity)
				return
			}
		}
		for _, pattern := range input.AllowedRefs {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" || len(pattern) > 128 || strings.ContainsAny(pattern, "\x00\r\n") || strings.Contains(pattern, "..") || !strings.HasPrefix(pattern, "refs/") && !gitRefPattern.MatchString(pattern) {
				http.Error(w, "invalid webhook ref pattern", http.StatusUnprocessableEntity)
				return
			}
		}
		detail := fmt.Sprintf("repositories=%d refs=%d", len(input.Repositories), len(input.AllowedRefs))
		intent, err := BeginSecurityAudit(a.Config.AuditLog, actor, "webhook.config.updated", site, detail)
		if err != nil {
			refuseWithoutSecurityAudit(w)
			return
		}
		if err := a.Webhooks.SetWebhookConfig(site, secret, input.Repositories, input.AllowedRefs); err != nil {
			intent.Failed(err.Error())
			http.Error(w, "could not persist webhook configuration", http.StatusInternalServerError)
			return
		}
		intent.Completed(detail)
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "webhook configuration cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"site": site, "secret": secret, "generated": generated, "repositories": input.Repositories, "allowed_refs": input.AllowedRefs})
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// gitWebhookSitePath extracts site from webhook URL: /api/sites/git-webhook/:site
func gitWebhookSitePath(path string) string {
	// Path format: /api/sites/git-webhook/:site
	prefix := "/api/sites/git-webhook/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	site := strings.TrimPrefix(path, prefix)
	// Remove any trailing slashes or query parameters
	if idx := strings.IndexAny(site, "/?"); idx >= 0 {
		site = site[:idx]
	}
	return site
}

func webhookSignatureMessage(timestamp, deliveryID string, body []byte) []byte {
	message := make([]byte, 0, len(timestamp)+len(deliveryID)+len(body)+2)
	message = append(message, timestamp...)
	message = append(message, '\n')
	message = append(message, deliveryID...)
	message = append(message, '\n')
	return append(message, body...)
}

// verifyWebhookSignature verifies HMAC-SHA256 over the exact replay metadata
// and body using the per-site webhook secret.
func verifyWebhookSignature(body []byte, timestamp, deliveryID, signature string, webhookSecret string) bool {
	provided, err := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(signature, "sha256=")))
	if err != nil {
		return false
	}
	digest := hmac.New(sha256.New, []byte(webhookSecret))
	_, _ = digest.Write(webhookSignatureMessage(timestamp, deliveryID, body))
	return hmac.Equal(provided, digest.Sum(nil))
}

// verifyWebhookTimestamp checks if the timestamp is recent (prevents stale/old replays)
func verifyWebhookTimestamp(timestamp string, maxAge time.Duration) (time.Time, error) {
	ts, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return time.Time{}, err
	}
	now := time.Now()
	if ts.Before(now.Add(-maxAge)) {
		return time.Time{}, errors.New("webhook timestamp too old")
	}
	if ts.After(now.Add(maxAge)) {
		return time.Time{}, errors.New("webhook timestamp is too far in the future")
	}
	return ts, nil
}

// matchRefPattern reports whether the Git ref ref is authorized by any of
// the configured patterns. Matching is anchored (whole-ref) and
// case-sensitive, mirroring Git's own ref semantics on case-sensitive
// filesystems.
//
// Supported pattern forms:
//
//   - Exact ref:  "refs/heads/main"
//   - Glob:       "refs/heads/release/*"  (path.Match: '*' does not cross '/')
//   - Recursive:  "refs/heads/release/**" (matches "refs/heads/release" and
//     everything nested underneath it)
//   - Bare branch/tag shorthand: "main", "release/*", "v1.*" — matched
//     against ref stripped of a "refs/heads/" or "refs/tags/" prefix. Kept
//     for backward compatibility with configs that pre-date full-ref
//     patterns; new patterns should be written in full-ref form.
//
// This replaces the previous implementation which stripped '*' from the
// pattern and did a substring match, so "refs/heads/release/*" effectively
// authorized any ref containing the substring "refs/heads/release/" —
// including unrelated branches like "refs/heads/other-release/main".
func matchRefPattern(ref string, patterns []string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	shortHead, shortHeadOK := "", false
	shortTag, shortTagOK := "", false
	if strings.HasPrefix(ref, "refs/heads/") {
		shortHead = strings.TrimPrefix(ref, "refs/heads/")
		shortHeadOK = shortHead != ""
	}
	if strings.HasPrefix(ref, "refs/tags/") {
		shortTag = strings.TrimPrefix(ref, "refs/tags/")
		shortTagOK = shortTag != ""
	}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if refMatchesPattern(ref, pattern) {
			return true
		}
		// Backward-compat: a bare pattern (no "refs/..." prefix) matches
		// against the short ref name. This lets a config that says just
		// "main" authorize a ref of "refs/heads/main".
		if strings.HasPrefix(pattern, "refs/") {
			continue
		}
		if shortHeadOK && refMatchesPattern(shortHead, pattern) {
			return true
		}
		if shortTagOK && refMatchesPattern(shortTag, pattern) {
			return true
		}
	}
	return false
}

// refMatchesPattern is the single-pattern anchored-glob primitive.
//   - '**' as a suffix matches the base and every descendant.
//   - Otherwise path.Match is used: '*' does not cross '/', '?' matches one
//     non-'/' rune, character classes are supported.
//   - A pattern with no glob metacharacters is treated as an exact match.
func refMatchesPattern(ref, pattern string) bool {
	if pattern == ref {
		return true
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return ref == prefix || strings.HasPrefix(ref, prefix+"/")
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return false
	}
	matched, err := path.Match(pattern, ref)
	return err == nil && matched
}

func (a *App) gitWebhook(w http.ResponseWriter, r *http.Request) {
	site := gitWebhookSitePath(r.URL.Path)
	if site == "" {
		http.Error(w, "site name required in webhook URL path", http.StatusBadRequest)
		return
	}
	site = safeUser(site)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		http.Error(w, "invalid webhook body", 400)
		return
	}

	if a.Webhooks == nil {
		http.Error(w, "per-site webhook configuration is unavailable", http.StatusNotFound)
		return
	}
	config, err := a.Webhooks.GetWebhookConfig(site)
	if err != nil {
		http.Error(w, "webhook configuration error", http.StatusInternalServerError)
		TelemetryAudit(a.Config.AuditLog, "webhook", "webhook.auth.error", site, fmt.Sprintf("get webhook config: %v", err))
		return
	}
	if config == nil || config.WebhookSecret == "" {
		http.Error(w, "webhook not configured for this site", http.StatusNotFound)
		return
	}
	webhookSecret := config.WebhookSecret

	deliveryID := r.Header.Get("X-StePanel-Delivery-ID")
	timestamp := r.Header.Get("X-StePanel-Delivery-Timestamp")
	if !webhookDeliveryIDPattern.MatchString(deliveryID) || timestamp == "" {
		http.Error(w, "missing or invalid delivery ID or timestamp", 400)
		return
	}
	ts, err := verifyWebhookTimestamp(timestamp, 5*time.Minute)
	if err != nil {
		http.Error(w, "invalid or stale webhook timestamp", 400)
		return
	}

	signature := r.Header.Get("X-StePanel-Signature")
	if !verifyWebhookSignature(body, timestamp, deliveryID, signature, webhookSecret) {
		http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
		TelemetryAudit(a.Config.AuditLog, "webhook", "webhook.auth.failed", site, "invalid signature")
		return
	}

	// Replay protection: verify timestamp is recent and delivery ID hasn't been seen
	accepted, err := a.webhookReplayCache.Accept(site, deliveryID, ts)
	if err != nil {
		http.Error(w, "webhook replay protection unavailable", http.StatusServiceUnavailable)
		return
	}
	if !accepted {
		http.Error(w, "duplicate or stale webhook delivery", 409)
		return
	}

	// Carry the site the signature actually authorized, and the policy loaded
	// with it, into gitDeploy's context.
	// gitDeploy treats that value — not input.Site from the body — as the
	// trusted deploy target, so a valid signature for siteA can never trigger a
	// deploy on siteB regardless of what the body claims.
	//
	// The deployment runs as a durable job (see site_operations.go), which
	// re-reads this site's webhook policy before it starts. The sender gets
	// 202 and the job ID instead of waiting on a checkout and build.
	if input := siteFromBody("", body); input != "" && input != site {
		TelemetryAudit(a.Config.AuditLog, "webhook", "webhook.site.mismatch", site, fmt.Sprintf("body claimed site %q", input))
		http.Error(w, "webhook body targets a different site than the URL signature authorized", http.StatusForbidden)
		return
	}
	a.enqueueSiteOperation(w, r, siteOperationPayload{Operation: "git.deploy", Path: "/api/sites/git-deploy", Body: body, Site: site, Actor: "webhook", Webhook: true})
}

// containsString checks if a string is in a list (case-insensitive for URLs)
func containsString(list []string, value string) bool {
	valueLower := strings.ToLower(value)
	for _, item := range list {
		if strings.ToLower(item) == valueLower {
			return true
		}
	}
	return false
}

// sanitizeGitError returns a customer-safe error message without leaking infrastructure details
func sanitizeGitError(output string) string {
	output = strings.ToLower(output)
	switch {
	case strings.Contains(output, "repository not found"):
		return "Repository not found. Check your repository URL."
	case strings.Contains(output, "permission denied"), strings.Contains(output, "authentication failed"):
		return "Authentication failed. Check your repository credentials."
	case strings.Contains(output, "could not read"):
		return "Could not read repository. Check your credentials and try again."
	case strings.Contains(output, "not a git repository"):
		return "Invalid Git repository. Check your repository URL."
	case strings.Contains(output, "timed out"), strings.Contains(output, "connection timeout"):
		return "Repository connection timed out. The server may be temporarily unavailable."
	case strings.Contains(output, "connection refused"):
		return "Could not connect to repository server. Check that the repository is accessible."
	case strings.Contains(output, "no such file"):
		return "Repository or branch not found."
	default:
		return "Git operation failed. Please check your repository details and try again."
	}
}

// gitDeploy intentionally does not evaluate repository-provided build scripts.
// Build execution belongs in a separately sandboxed runner.
//
// SECURITY: Webhook requests bypass CSRF and per-site HTTP-auth checks because
// the webhook signature verification already binds the request to a specific
// site (the one whose per-site HMAC secret validated the body). That binding
// is only meaningful if the deploy actually targets *that* site — so when a
// webhook-authenticated site is present in the context we treat it as the
// only trusted deploy target and refuse any body claim that disagrees.
func (a *App) gitDeploy(w http.ResponseWriter, r *http.Request) {
	webhookAuth, isWebhook := webhookAuthorization(r)
	webhookSite := webhookAuth.Site
	if r.Method != http.MethodPost || (!a.Auth.CSRF(r) && !isWebhook) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var input gitDeployRequest
	if err := decodeJSON(w, r, 16<<10, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Site = safeUser(input.Site)
	input.Repository = strings.TrimSpace(input.Repository)
	input.Ref = strings.TrimSpace(input.Ref)
	if input.Ref == "" {
		input.Ref = "main"
	}
	if isWebhook {
		// The signature only proved authority over webhookSite. Any body value
		// that disagrees is a cross-site attempt: 403, audited. An absent body
		// site is fine — we substitute the authenticated one — so payload
		// formats that omit "site" continue to work.
		if input.Site != "" && input.Site != webhookSite {
			TelemetryAudit(a.Config.AuditLog, "webhook", "webhook.site.mismatch", webhookSite, fmt.Sprintf("body claimed site %q", input.Site))
			http.Error(w, "webhook body targets a different site than the URL signature authorized", http.StatusForbidden)
			return
		}
		input.Site = webhookSite
	}
	if input.Site == "" || !gitRefPattern.MatchString(input.Ref) {
		http.Error(w, "invalid site or Git ref", http.StatusUnprocessableEntity)
		return
	}
	if !isWebhook {
		if _, ok := a.requireSiteAccess(w, r, input.Site, "site is not assigned to this account", http.StatusForbidden); !ok {
			return
		}
	}
	if !isWebhook && !a.Auth.HasRequiredCustomerScope(r, "deploy:write") {
		http.Error(w, "API token lacks the deploy:write scope", http.StatusForbidden)
		return
	}

	if isWebhook {
		// Enforce the allowlists gitWebhook loaded alongside the secret that
		// authenticated this request. Re-reading them here would fail open on
		// a transient store error and race configuration changes.
		repoLower := strings.ToLower(strings.TrimSpace(input.Repository))
		if len(webhookAuth.Repositories) == 0 || !containsString(webhookAuth.Repositories, repoLower) {
			http.Error(w, "repository is not authorized for this webhook", http.StatusForbidden)
			TelemetryAudit(a.Config.AuditLog, "webhook", "webhook.deploy.unauthorized", input.Site, fmt.Sprintf("repository not in whitelist: %s", input.Repository))
			return
		}
		if len(webhookAuth.AllowedRefs) == 0 || !matchRefPattern(input.Ref, webhookAuth.AllowedRefs) {
			http.Error(w, "ref is not authorized for this webhook", http.StatusForbidden)
			TelemetryAudit(a.Config.AuditLog, "webhook", "webhook.deploy.unauthorized", input.Site, fmt.Sprintf("ref not in whitelist: %s", input.Ref))
			return
		}
	}
	actor := a.Auth.UsernameForRequest(r)
	if isWebhook {
		actor = "webhook"
	}
	// A deployment changes the code a site serves: Class A. Record the intent
	// before any host state changes (webhook deploys are not routed through
	// Auth.Require's request audit, so this is their only pre-mutation
	// record), and record an outcome on every path.
	intent, err := BeginSecurityAudit(a.Config.AuditLog, actor, "site.git-deployed", input.Site, input.Repository+"@"+input.Ref)
	if err != nil {
		refuseWithoutSecurityAudit(w)
		return
	}
	deployed := false
	defer func() {
		if !deployed {
			intent.Failed("deployment did not activate a release")
		}
	}()
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), input.Site)
	if lockErr != nil {
		http.Error(w, "site is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "Git deployment cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	repository, err := parseGitRepository(input.Repository, a.Config.GitAllowedHosts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	publicRoot, err := existingManagedSitePublicRoot(a.Config.WebRoot, input.Site)
	if err != nil {
		http.Error(w, "invalid site root", http.StatusUnprocessableEntity)
		return
	}
	siteRoot := filepath.Dir(publicRoot)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		http.Error(w, "Git is not installed", http.StatusServiceUnavailable)
		return
	}
	releaseGitSlot, err := a.acquireGitCloneAdmission(operationCtx)
	if err != nil {
		http.Error(w, "Git deployment rejected by host memory admission: "+err.Error(), http.StatusInsufficientStorage)
		return
	}
	defer releaseGitSlot()
	release, releaseOwner, err := a.createSiteReleaseStaging(operationCtx, input.Site, ".stepanel-release-")
	if err != nil {
		http.Error(w, "unable to create release staging directory", http.StatusInternalServerError)
		return
	}
	defer func() {
		logStagingDiscardFailure(release, a.discardSiteReleaseStaging(context.Background(), input.Site, release))
		_ = releaseOwner.Close()
	}()
	stageLimit, err := gitCloneStageLimit(a.Config.GitReleaseMaxTreeBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInsufficientStorage)
		return
	}
	reservation, err := a.capacityLedger().reserve(a.Config, "git-release-staging", release, []capacityDemand{{Path: release, Bytes: uint64(stageLimit)}})
	if err != nil {
		http.Error(w, "insufficient capacity for aggregate Git clone staging", http.StatusInsufficientStorage)
		return
	}
	defer reservation.release()
	projectID, err := a.enableGitStageQuota(operationCtx, input.Site, release, stageLimit)
	if err != nil {
		http.Error(w, "Git deployment requires enforceable project quotas on its staging filesystem: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	quotaActive := true
	defer func() {
		if quotaActive {
			if clearErr := a.disableGitStageQuota(operationCtx, input.Site, release, projectID); clearErr != nil {
				log.Printf("failed to clear Git staging project quota for %s: %v", filepath.Base(release), clearErr)
			}
		}
	}()
	ctx, cancel := context.WithTimeout(operationCtx, 10*time.Minute)
	defer cancel()
	var cloneOutput []byte
	if repository.Private {
		cloneOutput, err = clonePrivateRepository(ctx, a.Config, input.Site, repository.URL, input.Ref, release, a.Config.GitReleaseMaxTreeBytes, projectID)
	} else {
		prlimit, lookupErr := exec.LookPath("prlimit")
		if lookupErr != nil {
			http.Error(w, "prlimit is required to enforce Git clone memory and per-file limits", http.StatusServiceUnavailable)
			return
		}
		clone := exec.CommandContext(ctx, prlimit, "--as="+strconv.FormatUint(gitCloneAddressSpaceBytes, 10), "--fsize="+strconv.FormatInt(a.Config.GitReleaseMaxTreeBytes, 10), "--", gitPath, "-c", "credential.helper=", "clone", "--depth", "1", "--branch", input.Ref, "--single-branch", "--no-tags", repository.URL, release)
		clone.Env = gitCommandEnv()
		cloneOutput, err = runBoundedCommand(ctx, clone)
	}
	if err != nil {
		log.Printf("Git clone failed for site %s (repo %s): %v\nOutput: %s", input.Site, input.Repository, err, strings.TrimSpace(string(cloneOutput)))
		http.Error(w, sanitizeGitError(string(cloneOutput)), http.StatusBadGateway)
		return
	}
	commit, err := identifyGitCommit(ctx, gitPath, release)
	if err != nil {
		log.Printf("Git checkout commit identification failed for site %s: %v", input.Site, err)
		http.Error(w, "unable to identify checked-out commit", http.StatusBadGateway)
		return
	}
	if err := validateGitRelease(release, a.Config.MaxEntries); err != nil {
		http.Error(w, "repository contains an unsafe release tree: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if err := validateGitReleaseSize(release, a.Config.GitReleaseMaxTreeBytes); err != nil {
		http.Error(w, "repository exceeds the configured release size limit", http.StatusRequestEntityTooLarge)
		return
	}
	if err := os.RemoveAll(filepath.Join(release, ".git")); err != nil {
		http.Error(w, "unable to remove Git metadata from release", http.StatusInternalServerError)
		return
	}
	if err := a.disableGitStageQuota(operationCtx, input.Site, release, projectID); err != nil {
		http.Error(w, "unable to remove temporary Git staging quota before activation: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	quotaActive = false
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "Git activation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	deploymentID, err := newJobID("deployment")
	if err != nil {
		http.Error(w, "could not create deployment identity", http.StatusInternalServerError)
		return
	}
	result := gitDeployResult{DeploymentID: deploymentID, Site: input.Site, Repository: input.Repository, Ref: input.Ref, Commit: commit}
	if err := a.recordDeployment(input.Site, "activation", "running", "atomic Git release activation started", result, ""); err != nil {
		historyUnavailable(w, r)
		return
	}
	previous, err := a.activatePipelineRelease(operationCtx, input.Site, release)
	if err != nil {
		_ = a.recordDeployment(input.Site, "activation", "failed", err.Error(), result, "") // failure already marks readiness
		http.Error(w, "unable to activate the new release", http.StatusInternalServerError)
		return
	}
	// The release is live from here on; record that before any later check
	// can return early.
	deployed = true
	intent.Completed(input.Repository + "@" + commit)
	result.Previous = previous
	a.finishCommittedRelease(operationCtx, siteRoot, "activation", "atomic Git release activated", release, &result, true)
	writeJSON(w, http.StatusAccepted, result)
}

func (a *App) gitRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var input gitRollbackRequest
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Site = safeUser(input.Site)
	if input.Site == "" || input.Confirm != "ROLLBACK "+input.Site {
		http.Error(w, "confirmation must exactly match ROLLBACK <site>", http.StatusUnprocessableEntity)
		return
	}
	siteRoot, err := safePath(a.Config.WebRoot, "sites", input.Site)
	if err != nil {
		http.Error(w, "invalid site root", http.StatusUnprocessableEntity)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), input.Site)
	if lockErr != nil {
		http.Error(w, "site is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "Git rollback cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	previous, err := latestPreviousRelease(siteRoot)
	if err != nil {
		http.Error(w, "no previous Git release is available", http.StatusConflict)
		return
	}
	if err := validateGitRelease(previous, a.Config.MaxEntries); err != nil {
		http.Error(w, "previous release failed safety validation", http.StatusConflict)
		return
	}
	if _, err := existingManagedSitePublicRoot(a.Config.WebRoot, input.Site); err != nil {
		http.Error(w, "active site release is unavailable", http.StatusConflict)
		return
	}
	intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "site.git-rolled-back", input.Site, filepath.Base(previous))
	if err != nil {
		refuseWithoutSecurityAudit(w)
		return
	}
	deploymentID, err := newJobID("deployment")
	if err != nil {
		intent.Failed("could not create deployment identity")
		http.Error(w, "could not create deployment identity", http.StatusInternalServerError)
		return
	}
	replaced, err := a.activatePipelineRelease(operationCtx, input.Site, previous)
	if err != nil {
		intent.Failed(err.Error())
		http.Error(w, "unable to preserve the active release", http.StatusInternalServerError)
		return
	}
	intent.Completed(filepath.Base(previous))
	result := gitDeployResult{DeploymentID: deploymentID, Site: input.Site, Previous: filepath.Base(replaced)}
	a.finishCommittedRelease(operationCtx, siteRoot, "rollback", "rolled back to an earlier Git release", previous, &result, true)
	response := map[string]string{"site": input.Site, "activated": result.Activated, "replaced_release": filepath.Base(replaced), "deployment_id": deploymentID, "outcome": result.Outcome}
	if result.HistoryError != "" {
		response["history_error"] = result.HistoryError
	}
	writeJSON(w, http.StatusOK, response)
}

func pruneGitReleases(siteRoot string, retain int) error {
	return pruneGitReleasesWithPolicy(siteRoot, retain, 0, 0)
}

func pruneGitReleasesWithPolicy(siteRoot string, retain int, maxAge time.Duration, maxBytes int64) error {
	if retain < 1 {
		return errors.New("release retention must preserve at least one rollback release")
	}
	entries, err := os.ReadDir(siteRoot)
	if err != nil {
		return err
	}
	type candidate struct {
		path     string
		modified time.Time
		bytes    int64
	}
	items := []candidate{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(entry.Name(), ".stepanel-previous-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		releasePath, err := safePath(siteRoot, entry.Name())
		if err != nil {
			return err
		}
		size, err := gitReleaseSize(releasePath)
		if err != nil {
			return err
		}
		items = append(items, candidate{releasePath, info.ModTime(), size})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].modified.After(items[j].modified) })
	if len(items) == 0 {
		return nil
	}
	remove := make(map[string]bool)
	for i, item := range items {
		// The newest previous tree is the immediate rollback target and is
		// never removed, even when it exceeds age or storage policy.
		if i == 0 {
			continue
		}
		if i >= retain || maxAge > 0 && time.Since(item.modified) > maxAge {
			remove[item.path] = true
		}
	}
	if maxBytes > 0 {
		var total int64
		for _, item := range items {
			total += item.bytes
		}
		for i := len(items) - 1; i > 0 && total > maxBytes; i-- {
			if !remove[items[i].path] {
				remove[items[i].path] = true
			}
			total -= items[i].bytes
		}
	}
	for _, item := range items {
		if !remove[item.path] {
			continue
		}
		if err := validateGitRelease(item.path, 1000000); err != nil {
			return fmt.Errorf("refuse to prune invalid release: %w", err)
		}
		if err := os.RemoveAll(item.path); err != nil {
			return err
		}
	}
	return nil
}

func gitReleaseSize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return filepath.SkipDir
		}
		if info.Mode().IsRegular() {
			if info.Size() < 0 || total > (1<<63-1)-info.Size() {
				return errors.New("Git release size overflow")
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func pruneAllGitReleases(cfg Config) error {
	root := filepath.Join(cfg.WebRoot, "sites")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || safeUser(entry.Name()) == "" {
			continue
		}
		if err := pruneGitReleasesWithPolicy(filepath.Join(root, entry.Name()), cfg.GitReleaseRetention, time.Duration(cfg.GitReleaseMaxAgeHours)*time.Hour, cfg.GitReleaseMaxBytes); err != nil {
			return fmt.Errorf("%s: %w", entry.Name(), err)
		}
	}
	return nil
}

func latestPreviousRelease(siteRoot string) (string, error) {
	entries, err := os.ReadDir(siteRoot)
	if err != nil {
		return "", err
	}
	type candidate struct {
		path string
		time time.Time
	}
	var latest candidate
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(entry.Name(), ".stepanel-previous-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if latest.path == "" || info.ModTime().After(latest.time) || info.ModTime().Equal(latest.time) && entry.Name() > filepath.Base(latest.path) {
			latest = candidate{path: filepath.Join(siteRoot, entry.Name()), time: info.ModTime()}
		}
	}
	if latest.path == "" {
		return "", errors.New("no previous release")
	}
	return latest.path, nil
}

// clonePrivateRepository clones a repository that needs the site's deploy
// key. Production (and the direct lab broker) use the typed root-broker clone,
// which binds the destination to the site's release staging directory;
// development runs the helper directly.
func clonePrivateRepository(ctx context.Context, cfg Config, site, repositoryURL, ref, release string, maxFileBytes int64, projectID uint32) ([]byte, error) {
	if !cfg.Production && !labDirectRootBrokerEnabled() {
		output, err, _ := runAllowlistedHelperOutput(ctx, cfg, nil, cfg.GitCtl, "clone", site, repositoryURL, ref, release, cfg.GitAllowedHosts, strconv.FormatInt(maxFileBytes, 10), strconv.FormatUint(uint64(projectID), 10))
		return output, err
	}
	client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", cfg.WebRoot)
	if err != nil {
		return nil, fmt.Errorf("root broker is unavailable: %w", err)
	}
	allowedHosts := strings.FieldsFunc(cfg.GitAllowedHosts, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	response, err := client.GitClone(ctx, site, repositoryURL, ref, release, maxFileBytes, projectID, allowedHosts)
	if err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, errors.New(response.Error)
	}
	return nil, nil
}

// gitCommandEnv deliberately does not inherit the panel process environment.
// Git gives a large set of GIT_*, proxy, credential, and askpass variables
// process-wide meaning; inheriting them would let deployment behavior change
// with an operator's environment or a service-manager environment file. The
// public clone path needs only a stable executable search path and locale,
// while Git configuration is supplied explicitly on the command line.
func gitCommandEnv() []string {
	pathValue := os.Getenv("PATH")
	if pathValue == "" {
		pathValue = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	return []string{
		"PATH=" + pathValue,
		"LANG=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}
}

func gitCommitInspectionArgs(repositoryPath string) ([]string, error) {
	absolutePath, err := filepath.Abs(repositoryPath)
	if err != nil {
		return nil, fmt.Errorf("resolve Git checkout path: %w", err)
	}
	checkoutPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("resolve Git checkout path: %w", err)
	}
	// Staged releases are intentionally owned by the target site account, not
	// the panel process. Trust only this exact, already-resolved checkout path;
	// never inherit a user's global safe.directory wildcard or bless all repos.
	return []string{"-c", "safe.directory=" + checkoutPath, "-C", checkoutPath, "rev-parse", "HEAD"}, nil
}

func identifyGitCommit(ctx context.Context, gitPath, repositoryPath string) (string, error) {
	args, err := gitCommitInspectionArgs(repositoryPath)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, gitPath, args...)
	cmd.Env = gitCommandEnv()
	output, err := runBoundedCommand(ctx, cmd)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return "", fmt.Errorf("git rev-parse HEAD: %w: %s", err, detail)
		}
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	commit := strings.TrimSpace(string(output))
	if !gitCommitPattern.MatchString(commit) {
		return "", errors.New("git rev-parse HEAD returned an invalid commit identifier")
	}
	return commit, nil
}

func validateGitAllowedHosts(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("STEPANEL_GIT_ALLOWED_HOSTS must contain at least one hostname")
	}
	for _, host := range strings.Split(value, ",") {
		host = strings.TrimSpace(strings.ToLower(host))
		if host == "" || strings.ContainsAny(host, "\x00\r\n:/") || net.ParseIP(host) != nil || !domainname.Valid(host) {
			return fmt.Errorf("STEPANEL_GIT_ALLOWED_HOSTS contains invalid hostname %q", host)
		}
	}
	return nil
}

func gitHostAllowed(allowed, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, candidate := range strings.Split(allowed, ",") {
		if host == strings.TrimSpace(strings.ToLower(candidate)) {
			return true
		}
	}
	return false
}

func validateGitRelease(root string, maxEntries int) error {
	if maxEntries < 1 {
		maxEntries = 100000
	}
	entries := 0
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > maxEntries {
			return errors.New("release exceeds the configured entry limit")
		}
		if path == root || strings.HasPrefix(path, filepath.Join(root, ".git")+string(os.PathSeparator)) || path == filepath.Join(root, ".git") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file or directory", filepath.Base(path))
		}
		return nil
	})
}

func validateGitReleaseSize(root string, maxBytes int64) error {
	_, err := gitReleaseTreeSize(root, maxBytes)
	return err
}

func gitReleaseTreeSize(root string, maxBytes int64) (int64, error) {
	if maxBytes < 1 {
		return 0, errors.New("release size limit must be positive")
	}
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == filepath.Join(root, ".git") || strings.HasPrefix(path, filepath.Join(root, ".git")+string(os.PathSeparator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", filepath.Base(path))
		}
		if info.Size() > maxBytes-total {
			return errors.New("release exceeds the configured byte limit")
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

func newRequestID() string {
	id, err := randomSecret()
	if err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return id
}

// WebhookConfigStore manages per-site webhook configuration in the control-plane database
type WebhookConfigStore struct {
	mu sync.RWMutex
	db *sql.DB
}

// NewWebhookConfigStore creates a webhook configuration manager
func NewWebhookConfigStore(database *sql.DB) *WebhookConfigStore {
	return &WebhookConfigStore{db: database}
}

// InitializeSchema creates the webhook configuration tables
func (wcs *WebhookConfigStore) InitializeSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS webhook_configs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		site TEXT NOT NULL UNIQUE,
		webhook_secret TEXT NOT NULL,
		enabled BOOLEAN DEFAULT 1,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS webhook_allowed_repos (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		webhook_id INTEGER NOT NULL,
		repository_url TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (webhook_id) REFERENCES webhook_configs(id) ON DELETE CASCADE,
		UNIQUE(webhook_id, repository_url)
	);

	CREATE TABLE IF NOT EXISTS webhook_allowed_refs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		webhook_id INTEGER NOT NULL,
		ref_pattern TEXT NOT NULL,
		pattern_type TEXT DEFAULT 'glob',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (webhook_id) REFERENCES webhook_configs(id) ON DELETE CASCADE,
		UNIQUE(webhook_id, ref_pattern)
	);

	CREATE INDEX IF NOT EXISTS idx_webhook_site ON webhook_configs(site);
	`

	_, err := wcs.db.Exec(schema)
	return err
}

// SetWebhookConfig creates or updates a webhook configuration for a site
func (wcs *WebhookConfigStore) SetWebhookConfig(site, secret string, repos, refPatterns []string) error {
	wcs.mu.Lock()
	defer wcs.mu.Unlock()

	tx, err := wcs.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRow("SELECT id FROM webhook_configs WHERE site = ?", site).Scan(&id)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	if err == sql.ErrNoRows {
		result, err := tx.Exec(
			"INSERT INTO webhook_configs (site, webhook_secret) VALUES (?, ?)",
			site, secret,
		)
		if err != nil {
			return err
		}
		id, _ = result.LastInsertId()
	} else {
		_, err := tx.Exec(
			"UPDATE webhook_configs SET webhook_secret = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
			secret, id,
		)
		if err != nil {
			return err
		}
	}

	if _, err := tx.Exec("DELETE FROM webhook_allowed_repos WHERE webhook_id = ?", id); err != nil {
		return err
	}

	for _, repo := range repos {
		if repo == "" {
			continue
		}
		_, err := tx.Exec(
			"INSERT INTO webhook_allowed_repos (webhook_id, repository_url) VALUES (?, ?)",
			id, strings.TrimSpace(strings.ToLower(repo)),
		)
		if err != nil {
			return err
		}
	}

	if _, err := tx.Exec("DELETE FROM webhook_allowed_refs WHERE webhook_id = ?", id); err != nil {
		return err
	}

	for _, pattern := range refPatterns {
		if pattern == "" {
			continue
		}
		_, err := tx.Exec(
			"INSERT INTO webhook_allowed_refs (webhook_id, ref_pattern) VALUES (?, ?)",
			id, strings.TrimSpace(pattern),
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetWebhookConfig retrieves the webhook configuration for a site
func (wcs *WebhookConfigStore) GetWebhookConfig(site string) (*GitWebhookConfig, error) {
	wcs.mu.RLock()
	defer wcs.mu.RUnlock()

	var id int64
	var secret string
	var enabledAt time.Time

	err := wcs.db.QueryRow(
		"SELECT id, webhook_secret, created_at FROM webhook_configs WHERE site = ? AND enabled = 1",
		site,
	).Scan(&id, &secret, &enabledAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	config := &GitWebhookConfig{
		Site:          site,
		WebhookSecret: secret,
		EnabledAt:     enabledAt,
		Repositories:  []string{},
		AllowedRefs:   []string{},
	}

	rows, err := wcs.db.Query(
		"SELECT repository_url FROM webhook_allowed_repos WHERE webhook_id = ? ORDER BY repository_url",
		id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var repo string
		if err := rows.Scan(&repo); err != nil {
			return nil, err
		}
		config.Repositories = append(config.Repositories, repo)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	rows, err = wcs.db.Query(
		"SELECT ref_pattern FROM webhook_allowed_refs WHERE webhook_id = ? ORDER BY ref_pattern",
		id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var pattern string
		if err := rows.Scan(&pattern); err != nil {
			return nil, err
		}
		config.AllowedRefs = append(config.AllowedRefs, pattern)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	return config, nil
}

// DisableWebhookConfig disables webhooks for a site
func (wcs *WebhookConfigStore) DisableWebhookConfig(site string) error {
	wcs.mu.Lock()
	defer wcs.mu.Unlock()

	_, err := wcs.db.Exec(
		"UPDATE webhook_configs SET enabled = 0, updated_at = CURRENT_TIMESTAMP WHERE site = ?",
		site,
	)
	return err
}
