package stepanel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cyberducttape/StePanel/internal/audit"
	authpolicy "github.com/cyberducttape/StePanel/internal/auth"
	"github.com/cyberducttape/StePanel/internal/domainname"
	httputil "github.com/cyberducttape/StePanel/internal/http"
	"github.com/cyberducttape/StePanel/internal/metadata"
	"github.com/cyberducttape/StePanel/internal/operations"
	"github.com/cyberducttape/StePanel/internal/recovery"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
	"github.com/cyberducttape/StePanel/internal/safehttp"
	"github.com/cyberducttape/StePanel/internal/siteidentity"
	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
	"github.com/cyberducttape/StePanel/internal/state"
	"github.com/cyberducttape/StePanel/internal/upload"
	"html/template"
	"io"
	"io/fs"
	"log"
	"math/rand/v2"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type App struct {
	Config                   Config
	View                     *template.Template
	AssetVersion             string
	Auth                     Auth
	Jobs                     *Jobs
	Metrics                  *Metrics
	Schedules                *backupSchedules
	Accounts                 *AccountStore
	recovery                 recoveryState
	startup                  startupState
	Environments             *EnvironmentStore
	Redis                    *RedisAllocationStore
	DNSDesired               *DNSDesiredStore
	Routes                   *RouteStore
	Domains                  *DomainClaimStore
	Access                   *SiteAccessStore
	Workers                  *WorkerStore
	Composer                 *ComposerStore
	PHP                      *PHPProfileStore
	Tasks                    *TaskStore
	APITokens                *apiTokenStore
	Deployments              *DeploymentStore
	Resources                *ResourceStore
	Webhooks                 *WebhookConfigStore
	BackupIndex              *metadata.BackupIndex
	Recovery                 *recovery.Store
	ResourceBudget           *ResourceBudget
	MetadataCache            *MetadataCache
	databaseDiagnosticsMu    sync.Mutex
	databaseDiagnosticsCache DatabaseDiagnostics
	databaseInventoryMu      sync.Mutex
	databaseInventoryCache   []DatabaseResource
	databaseInventoryAt      time.Time
	gitActivationMu          sync.Mutex
	webhookReplayCache       *WebhookReplayCache
	siteOperations           operations.Locks
	dbLocks                  *operations.DBLocks
	siteManager              siteauthority.Manager
	// capacity holds free space promised to in-progress archive uploads.
	capacity capacityLedger
	// streams bounds open server-sent event streams.
	streams streamLimiter
}

// startupState separates process liveness from control-plane readiness. The
// HTTP server can therefore expose /livez and an honest 503 /readyz while
// recovery and reconciliation are still running.
type startupState struct {
	mu         sync.RWMutex
	inProgress bool
	err        error
}

const uploadMultipartOverhead int64 = 32 << 20

// cpmoveImportRequestBytes bounds /api/cpmove/import, which references an
// inspected upload by ID and never carries the archive itself.
const cpmoveImportRequestBytes int64 = 1 << 20

func maxUploadRequestBytes(maxArchive int64) int64 {
	if maxArchive <= 0 {
		return maxArchive
	}
	return maxArchive + uploadMultipartOverhead
}

// recoveryState holds the latest required-state persistence failure. It is
// written by persistence paths on request goroutines and read by /readyz, so
// access is synchronized.
type recoveryState struct {
	mu  sync.RWMutex
	err error
}

func (s *recoveryState) set(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *recoveryState) get() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}

func (s *startupState) begin() {
	s.mu.Lock()
	s.inProgress = true
	s.err = nil
	s.mu.Unlock()
}

func (s *startupState) finish(err error) {
	s.mu.Lock()
	s.inProgress = false
	s.err = err
	s.mu.Unlock()
}

func (s *startupState) status() (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inProgress, s.err
}

// Main runs the stepanel executable: it dispatches the command-line
// subcommands and otherwise starts the panel. cmd/stepanel is the binary
// entry point; the application itself lives in this importable package.
func Main() {
	workerMode := len(os.Args) == 2 && os.Args[1] == "worker"
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		_, _ = fmt.Fprintf(os.Stdout, "StePanel %s\ncommit: %s\nbuilt: %s\n", Version, Commit, BuildDate)
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "task-webhook" {
		if err := runTaskWebhook(context.Background(), safehttp.Policy{}, os.Args[2:], os.Stdin); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) >= 2 && (os.Args[1] == "setup" || os.Args[1] == "init") {
		runSetupCommand(os.Args[2:])
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "convert-htaccess" {
		content, err := io.ReadAll(io.LimitReader(os.Stdin, maxHTAccessBytes+1))
		if err != nil || len(content) > maxHTAccessBytes {
			log.Fatal(".htaccess input exceeds the 256 KiB limit")
		}
		conversion, err := translateHTAccess(string(content))
		if err != nil {
			log.Fatal(err)
		}
		_, _ = fmt.Fprint(os.Stdout, conversion.CaddyDirectives)
		for _, warning := range conversion.Warnings {
			_, _ = fmt.Fprintln(os.Stderr, "warning:", warning)
		}
		if len(conversion.Warnings) > 0 {
			os.Exit(2)
		}
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "verify-backup" {
		cfg := LoadConfig()
		manifest, err := VerifySiteBackupStrict(os.Args[2], cfg.BackupSigningKey, cfg.backupDecryptionKeys()...)
		if err != nil {
			log.Fatal(err)
		}
		_, _ = fmt.Fprintf(os.Stdout, "%s  %s\n", manifest.ArchiveSHA256, filepath.Join(os.Args[2], manifest.Archive))
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "offsite-recovery-proof" {
		result, err := RunOffsiteRecoveryProof(LoadConfig())
		if err != nil {
			log.Fatal(err)
		}
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			log.Fatal(err)
		}
		_, _ = fmt.Fprintln(os.Stdout, string(data))
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "verify-audit" {
		if err := audit.Verify(os.Args[2]); err != nil {
			log.Fatal(err)
		}
		_, _ = fmt.Fprintf(os.Stdout, "audit chain verified: %s\n", os.Args[2])
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "dr-check" {
		if err := runDRCheck(LoadConfig()); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "backup-control-plane" {
		if err := backupControlPlane(LoadConfig().ControlPlaneDB, os.Args[2]); err != nil {
			log.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, os.Args[2])
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "restore-control-plane" && os.Args[2] == "--dry-run" {
		log.Fatal("restore-control-plane requires SOURCE --dry-run")
	}
	if len(os.Args) == 4 && os.Args[1] == "restore-control-plane" && os.Args[3] == "--dry-run" {
		if err := verifyControlPlaneBackup(os.Args[2]); err != nil {
			log.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "control-plane backup verified:", os.Args[2])
		return
	}
	if len(os.Args) == 4 && os.Args[1] == "restore-control-plane" && os.Args[3] == "--replace" {
		cfg := LoadConfig()
		panelLock, err := acquireProcessLock(cfg.JobState + ".lock")
		if err != nil {
			log.Fatal("stop StePanel before live control-plane restore: ", err)
		}
		defer panelLock.Close()
		workerLock, err := acquireProcessLock(cfg.JobState + ".lock.worker")
		if err != nil {
			log.Fatal("stop stepanel-worker before live control-plane restore: ", err)
		}
		defer workerLock.Close()
		if err := restoreControlPlane(os.Args[2], cfg.ControlPlaneDB); err != nil {
			log.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "control-plane restored:", cfg.ControlPlaneDB)
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "hash-password" {
		password, err := io.ReadAll(io.LimitReader(os.Stdin, 1025))
		if err != nil || len(password) == 0 || len(password) > 1024 || strings.ContainsAny(string(password), "\r\n") {
			log.Fatal("password must be 1-1024 bytes and contain no newlines")
		}
		hash, err := hashPassword(string(password))
		if err != nil {
			log.Fatal(err)
		}
		_, _ = fmt.Fprintln(os.Stdout, hash)
		return
	}
	cfg := LoadConfig()
	if cfg.Production && os.Getenv("STEPANEL_ACCOUNT_STATE") == "" {
		cfg.AccountState = filepath.Join(filepath.Dir(cfg.SessionState), "accounts.json")
	}
	if cfg.Production && strings.TrimSpace(cfg.AccountKey) == "" {
		log.Fatal("production requires STEPANEL_ACCOUNT_KEY to encrypt customer TOTP secrets")
	}
	if err := ValidateConfig(cfg); err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}
	if bypasses := activeSafetyBypasses(); cfg.Production && len(bypasses) > 0 {
		detail := "unsafe lab mode: " + strings.Join(bypasses, ", ")
		log.Printf("CRITICAL: production safety invariants are bypassed (%s); this host is not production-safe", strings.Join(bypasses, ", "))
		TelemetryAudit(cfg.AuditLog, "system", "control_plane.safety_bypass_active", Version, detail)
	}
	if strings.TrimSpace(cfg.AccountState) == "" || strings.ContainsAny(cfg.AccountState, "\x00\r\n") || cfg.Production && !filepath.IsAbs(cfg.AccountState) {
		log.Fatal("STEPANEL_ACCOUNT_STATE must be a non-empty filesystem path and absolute in production")
	}
	if strings.TrimSpace(cfg.ControlPlaneDB) == "" || strings.ContainsAny(cfg.ControlPlaneDB, "\x00\r\n") || cfg.Production && !filepath.IsAbs(cfg.ControlPlaneDB) {
		log.Fatal("STEPANEL_CONTROL_PLANE_DB must be a non-empty filesystem path and absolute in production")
	}
	// The install smoke test reaches the production-configured backend directly
	// over HTTP. Keep production cookies Secure by default, while allowing that
	// explicitly isolated lab path to exercise the authenticated workflows.
	secureCookies := cfg.Production
	if os.Getenv("STEPANEL_LAB_HTTP_COOKIES") == "1" {
		secureCookies = false
	}
	auth, err := NewAuth(secureCookies)
	if err != nil {
		log.Fatal(err)
	}
	if cfg.Production && !auth.Enabled {
		log.Fatal("authentication must be configured in production")
	}
	if !auth.Enabled && !loopbackListenAddress(cfg.Listen) {
		log.Fatal("authentication must be configured before listening on a non-loopback address")
	}
	auth.AuditLog = cfg.AuditLog
	audit.SetDefault(audit.New(cfg.AuditLog))
	// Wire the trusted-proxy CIDRs into auth. The prior code assigned a
	// bare bool (auth.TrustProxy = cfg.TLSAlreadyTerminated), which meant
	// any client that could reach the panel could spoof X-Forwarded-For
	// and impersonate any IP in rate limits and audit records. Now we
	// parse an explicit CIDR list from STEPANEL_TRUSTED_PROXY_CIDRS and
	// only consult forwarded headers when the direct peer is inside one
	// of them. If TLS is terminated but no CIDRs are configured, we
	// default to loopback so a local reverse proxy on the same host
	// continues to work and admins running a remote proxy get a clear
	// signal that they must configure it explicitly.
	trustedProxies, tpErr := authpolicy.ParseTrustedProxyCIDRs(cfg.TrustedProxyCIDRs)
	if tpErr != nil {
		log.Fatalf("invalid STEPANEL_TRUSTED_PROXY_CIDRS: %v", tpErr)
	}
	if len(trustedProxies) == 0 && cfg.TLSAlreadyTerminated {
		defaults, err := authpolicy.ParseTrustedProxyCIDRs("127.0.0.1/32,::1/128")
		if err != nil {
			log.Fatalf("parse default trusted-proxy CIDRs: %v", err)
		}
		trustedProxies = defaults
		log.Printf("STEPANEL_TLS_TERMINATED=1 with no STEPANEL_TRUSTED_PROXY_CIDRS; defaulting to loopback. Configure explicitly if your reverse proxy is not co-located.")
	}
	auth.TrustedProxies = trustedProxies
	for _, directory := range []struct {
		path string
		mode os.FileMode
	}{{cfg.ImportRoot, 0700}, {cfg.BackupRoot, 0700}, {filepath.Dir(cfg.JobState), 0750}, {filepath.Dir(cfg.SessionState), 0750}, {filepath.Dir(cfg.AccountState), 0750}, {filepath.Dir(cfg.ControlPlaneDB), 0750}, {cfg.RecoveryRoot, 0700}} {
		if err := os.MkdirAll(directory.path, directory.mode); err != nil {
			log.Fatalf("initialize managed directory %s: %v", directory.path, err)
		}
	}
	processLockPath := cfg.JobState + ".lock"
	if workerMode {
		processLockPath += ".worker"
	}
	processLock, err := acquireProcessLock(processLockPath)
	if err != nil {
		log.Fatalf("acquire process lock: %v", err)
	}
	defer processLock.Close()
	controlPlaneDB, err := openControlPlaneDB(cfg.ControlPlaneDB)
	if err != nil {
		log.Fatalf("open control-plane database: %v", err)
	}
	defer controlPlaneDB.Close()
	if err := siteidentity.MigrateExisting(controlPlaneDB, cfg.WebRoot); err != nil {
		log.Fatalf("migrate site Unix identities: %v", err)
	}
	auditOutbox, err := newAuditOutboxStore(controlPlaneDB)
	if err != nil {
		log.Fatalf("initialize audit outbox: %v", err)
	}
	defaultAuditOutbox = auditOutbox
	siteManager, err := siteauthority.NewDefaultManager(cfg.WebRoot)
	if err != nil {
		log.Fatalf("initialize site lifecycle manager: %v", err)
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "localhost"
	}
	dbLocks, err := operations.NewDBLocks(controlPlaneDB, fmt.Sprintf("stepanel-%s-%d-%d", hostname, os.Getpid(), time.Now().UnixNano()), controlPlaneLeaseTime)
	if err != nil {
		log.Fatalf("initialize durable operation locks: %v", err)
	}
	if err := auth.ConfigureSessionStoreDB(controlPlaneDB, cfg.SessionState); err != nil {
		log.Fatalf("open persistent session state: %v", err)
	}
	if err := auth.ConfigureTOTPReplayDB(controlPlaneDB); err != nil {
		log.Fatalf("open persistent TOTP replay state: %v", err)
	}
	if err := auth.ConfigureLegacyTokenDeprecation(controlPlaneDB); err != nil {
		log.Fatalf("configure legacy token deprecation tracking: %v", err)
	}
	webhookConfigStore := NewWebhookConfigStore(controlPlaneDB)
	if err := webhookConfigStore.InitializeSchema(); err != nil {
		log.Fatalf("initialize webhook configuration schema: %v", err)
	}
	backupIndex, err := metadata.NewBackupIndex(controlPlaneDB)
	if err != nil {
		log.Fatalf("initialize backup index: %v", err)
	}
	recoveryStore, err := recovery.NewStore(controlPlaneDB)
	if err != nil {
		log.Fatalf("initialize recovery rehearsal store: %v", err)
	}
	auth.apiTokens = &apiTokenStore{db: controlPlaneDB}
	if strings.TrimSpace(cfg.AccountKey) != "" {
		if err := auth.apiTokens.configureIdempotencyKey(cfg.AccountKey); err != nil {
			log.Fatalf("configure API token idempotency: %v", err)
		}
	}
	if err := auth.enforceLegacyTokenCutoff(); err != nil {
		log.Fatalf("enforce legacy API token cutoff: %v", err)
	}
	accounts, err := OpenAccountStoreDB(controlPlaneDB, cfg.AccountState, cfg.AccountKey)
	if err != nil {
		log.Fatalf("open persistent shared-hosting account state: %v", err)
	}
	if err := accounts.SetAdministratorUsername(auth.Username); err != nil {
		log.Fatalf("validate administrator/customer identity boundary: %v", err)
	}
	auth.Accounts = accounts
	environments, err := OpenEnvironmentStore(cfg.EnvironmentState, cfg.EnvironmentKey)
	if err != nil {
		log.Fatalf("open site environment state: %v", err)
	}
	redisAllocations, err := OpenRedisAllocationStore(cfg.RedisState)
	if err != nil {
		log.Fatalf("open Redis allocation state: %v", err)
	}
	access, err := OpenSiteAccessStore(siteAccessStatePath(cfg))
	if err != nil {
		log.Fatalf("open site SSH access state: %v", err)
	}
	workers, err := OpenWorkerStore(filepath.Join(filepath.Dir(cfg.JobState), "workers.json"))
	if err != nil {
		log.Fatalf("open worker state: %v", err)
	}
	composer, err := OpenComposerStore(filepath.Join(filepath.Dir(cfg.JobState), "composer-operations.json"))
	if err != nil {
		log.Fatalf("open Composer state: %v", err)
	}
	phpProfiles, err := OpenPHPProfileStore(filepath.Join(filepath.Dir(cfg.JobState), "php-profiles.json"))
	if err != nil {
		log.Fatalf("open PHP profile state: %v", err)
	}
	tasks, err := OpenTaskStore(filepath.Join(filepath.Dir(cfg.JobState), "scheduled-tasks.json"))
	if err != nil {
		log.Fatalf("open scheduled task state: %v", err)
	}
	deployments, err := OpenDeploymentStoreDB(controlPlaneDB, filepath.Join(filepath.Dir(cfg.JobState), "deployments.json"))
	if err != nil {
		log.Fatalf("open deployment state: %v", err)
	}
	resources, err := OpenResourceStore(filepath.Join(filepath.Dir(cfg.JobState), "resource-profiles.json"))
	if err != nil {
		log.Fatalf("open resource profile state: %v", err)
	}
	dnsDesired, err := OpenDNSDesiredStore(filepath.Join(filepath.Dir(cfg.JobState), "dns-desired.json"))
	if err != nil {
		log.Fatalf("open DNS desired state: %v", err)
	}
	routes, err := OpenRouteStore(filepath.Join(filepath.Dir(cfg.JobState), "routes.json"))
	if err != nil {
		log.Fatalf("open route desired state: %v", err)
	}
	domains, err := OpenDomainClaimStore(filepath.Join(filepath.Dir(cfg.JobState), "domain-claims.json"))
	if err != nil {
		log.Fatalf("open domain claim state: %v", err)
	}
	bindState := func(store any, name string, target any, persist func() error) {
		found, bindErr := bindControlPlaneState(store, controlPlaneDB, name, target)
		if bindErr != nil {
			log.Fatalf("load control-plane state %s: %v", name, bindErr)
		}
		if !found {
			if err := persist(); err != nil {
				log.Fatalf("migrate control-plane state %s: %v", name, err)
			}
		}
	}
	// The store is its own codec: durable payloads hold encrypted secrets and
	// must be decrypted by the store, never unmarshalled into live state.
	environmentLegacy, err := legacyCiphertextAllowed(controlPlaneDB, encryptionStoreEnvironment)
	if err != nil {
		log.Fatalf("read environment encryption format: %v", err)
	}
	environments.setLegacyCiphertextAllowed(environmentLegacy)
	bindState(environments, "environment", environments, environments.persistLocked)
	if err := environments.completeEncryptionMigration(controlPlaneDB); err != nil {
		log.Fatalf("migrate environment secrets to context-bound encryption: %v", err)
	}
	bindState(redisAllocations, "redis", &redisAllocations.values, redisAllocations.persistLocked)
	bindState(access, "site-access", &access.values, access.persistLocked)
	bindState(workers, "workers", &workers.values, workers.persistLocked)
	bindState(composer, "composer", &composer.latest, func() error {
		data, err := json.Marshal(composer.latest)
		if err != nil {
			return err
		}
		_, err = persistBoundControlPlaneState(composer, data)
		return err
	})
	bindState(phpProfiles, "php", &phpProfiles.values, func() error {
		data, err := json.Marshal(phpProfiles.values)
		if err != nil {
			return err
		}
		_, err = persistBoundControlPlaneState(phpProfiles, data)
		return err
	})
	bindState(tasks, "tasks", &tasks.values, tasks.persistLocked)
	if err := tasks.normalizeSafeguards(); err != nil {
		log.Fatalf("normalize scheduled task safeguards: %v", err)
	}
	bindState(resources, "resources", &resources.values, resources.persistLocked)
	bindState(dnsDesired, "dns-desired", &dnsDesired.values, dnsDesired.persistLocked)
	bindState(routes, "routes", &routes.values, routes.persistLocked)
	bindState(domains, "domain-claims", &domains.values, domains.persistLocked)
	// Recovery and reconciliation are started after the HTTP control plane is
	// listening. Readiness remains false until this work has completed.
	viewData, err := webAssets.ReadFile("web/index.html")
	if err != nil {
		log.Fatalf("load embedded dashboard: %v", err)
	}
	view := template.Must(template.New("index.html").Funcs(template.FuncMap{"add": func(a, b int) int { return a + b }}).Parse(string(viewData)))
	staticAssets, err := fs.Sub(webAssets, "web/static")
	if err != nil {
		log.Fatalf("load embedded static assets: %v", err)
	}
	assetVersion, err := embeddedAssetVersion()
	if err != nil {
		log.Fatalf("fingerprint embedded static assets: %v", err)
	}
	jobs, err := openDurableJobsDBWithKey(controlPlaneDB, cfg.JobState, cfg.AccountKey, cfg.MaxConcurrentJobs)
	if err != nil {
		log.Fatalf("open durable control-plane job state: %v", err)
	}
	if _, err := jobs.RequeueExpired(); err != nil {
		log.Fatalf("requeue expired durable jobs: %v", err)
	}
	schedules, err := openBackupSchedules(filepath.Join(filepath.Dir(cfg.JobState), "backup-schedules.json"))
	if err != nil {
		log.Fatalf("open backup schedules: %v", err)
	}
	bindState(schedules, "backup-schedules", &schedules.items, schedules.persistLocked)
	capacity, err := newCapacityLedger(controlPlaneDB)
	if err != nil {
		log.Fatalf("initialize capacity ledger: %v", err)
	}
	app := &App{Config: cfg, View: view, AssetVersion: assetVersion, Auth: auth, Jobs: jobs, Metrics: NewMetrics(), Schedules: schedules, Accounts: accounts, Environments: environments, Redis: redisAllocations, DNSDesired: dnsDesired, Routes: routes, Domains: domains, Access: access, Workers: workers, Composer: composer, PHP: phpProfiles, Tasks: tasks, APITokens: auth.apiTokens, Deployments: deployments, Resources: resources, Webhooks: webhookConfigStore, BackupIndex: backupIndex, Recovery: recoveryStore, webhookReplayCache: NewDurableWebhookReplayCache(controlPlaneDB, 5*time.Minute), dbLocks: dbLocks, siteManager: siteManager, capacity: capacityLedger{db: capacity.db}}
	app.startup.begin()
	// Categorized state errors feed stepanel_state_errors_total and the logs.
	jobs.SetStateErrorObserver(app.observeStateError)
	recoveryCorruptionObserver = func(dir string, cause error) {
		app.observeStateError(state.NewCorruptionError("recovery_journal", cause, "quarantined "+filepath.Base(dir)))
	}
	var startupAuditErr error
	// Reconcile domains independently. A single shared deadline allowed a slow
	// host/helper operation in an early domain to starve every later domain.
	// Each domain remains bounded, and failures are retained in its own report.
	reconcile := func(name string, fn func(context.Context) ([]string, map[string]string)) {
		// Use ServiceLifecycleTimeout for reconciliation operations (config mutations, user setup, etc.)
		// This gives each domain up to 60 seconds to reconcile, allowing for slower helper operations.
		reconcileCtx, cancelReconcile := context.WithTimeout(context.Background(), helperServiceLifecycleTimeout)
		defer cancelReconcile()
		reconciled, failed := fn(reconcileCtx)
		if len(failed) > 0 {
			log.Printf("%s reconciliation incomplete: reconciled=%d failed=%d", name, len(reconciled), len(failed))
		}
	}
	runStartup := func() {
		var failures []error
		if startupAuditErr != nil {
			failures = append(failures, fmt.Errorf("initialize audit chain: %w", startupAuditErr))
		}
		if err := auditOutbox.flush(context.Background(), cfg.AuditLog); err != nil {
			failures = append(failures, fmt.Errorf("flush audit outbox during startup: %w", err))
		}
		if cfg.DBCtl != "" && os.Getenv("STEPANEL_SKIP_STARTUP_DB_RECONCILE") != "1" {
			if err := reconcileDatabaseOperations(cfg, app.acquireSiteMutationLockContext); err != nil {
				failures = append(failures, err)
			}
		}
		failures = append(failures, recoverUncleanShutdown(cfg, siteManager, app.acquireSiteMutationLockContext)...)
		if _, err := recoverAppActivationJournals(cfg); err != nil {
			failures = append(failures, fmt.Errorf("recover application activations: %w", err))
		}
		failures = append(failures, recoverWordPressMaintenance(cfg, app.acquireSiteMutationLockContext, recoveredSiteLockWait)...)
		if replayed, err := app.replaySpooledDeployments(); err != nil {
			failures = append(failures, fmt.Errorf("replay spooled deployment history: %w", err))
		} else if replayed > 0 {
			log.Printf("replayed %d spooled deployment history record(s)", replayed)
		}
		if orphaned, err := app.reconcileOrphanedDNSDesired(); err != nil {
			failures = append(failures, fmt.Errorf("reconcile DNS desired state: %w", err))
		} else if orphaned > 0 {
			log.Printf("marked %d pending DNS change(s) without a durable job as failed", orphaned)
		}
		if err := CleanupImportStages(cfg.ImportRoot, time.Duration(cfg.StageRetentionHours)*time.Hour); err != nil {
			app.observeStateError(state.NewCleanupError("import_stage_cleanup", err, "import stage cleanup during startup"))
		}
		if err := CleanupBackupStages(cfg.BackupRoot, orphanedStagingMinAge); err != nil {
			app.observeStateError(state.NewCleanupError("backup_stage_cleanup", err, "backup stage cleanup during startup"))
		}
		if err := CleanupSiteTransactions(cfg.RecoveryRoot, time.Duration(cfg.StageRetentionHours)*time.Hour, cfg.WebRoot, cfg.MailRoot); err != nil {
			app.observeStateError(state.NewCleanupError("site_recovery_cleanup", err, "site recovery cleanup during startup"))
		}
		if os.Getenv("STEPANEL_SKIP_STARTUP_HOST_RECONCILE") != "1" {
			reconcile("routes", app.reconcileRoutes)
			reconcile("SSH access", app.reconcileSiteAccess)
			reconcile("workers", app.reconcileWorkers)
			reconcile("PHP profile", app.reconcilePHPProfiles)
			reconcile("Python application", app.reconcilePythonApps)
			reconcile("scheduled task", app.reconcileTasks)
			reconcile("resource profile", app.reconcileResourceProfiles)
			reconcile("environment", app.reconcileEnvironments)
		}
		if err := pruneAllGitReleases(cfg); err != nil {
			app.observeStateError(state.NewCleanupError("git_release_retention", err, "Git release retention during startup"))
		}
		startupError := errors.Join(failures...)
		app.startup.finish(startupError)
		if startupError != nil {
			log.Printf("startup recovery completed with degraded readiness: %v", startupError)
		} else {
			log.Printf("startup recovery and reconciliation completed")
		}
	}
	startupAuditErr = SecurityAuditRequired(cfg.AuditLog, "system", "service.started", "stepanel", "control plane initialized")
	if startupAuditErr != nil {
		log.Printf("initialize audit chain: %v", startupAuditErr)
	}
	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	app.ResourceBudget = NewResourceBudget(cfg.MaxConcurrentJobs)
	if workerMode {
		app.startup.finish(startupAuditErr)
		log.Printf("StePanel durable worker started with pid %d", os.Getpid())
		err := app.Jobs.RunWorkerPool(runCtx, fmt.Sprintf("worker-%d", os.Getpid()), durableWorkerJobKinds, 500*time.Millisecond, cfg.MaxConcurrentJobs, app.handleDurableJob)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Fatalf("durable worker stopped: %v", err)
		}
		return
	}
	if !app.Auth.Enabled {
		log.Println("warning: authentication is disabled; set STEPANEL_ADMIN_PASSWORD and STEPANEL_SESSION_SECRET")
	}
	if cfg.WorkerMode != "external" {
		go func() {
			err := app.Jobs.RunWorkerPool(runCtx, fmt.Sprintf("panel-%d", os.Getpid()), durableWorkerJobKinds, 500*time.Millisecond, cfg.MaxConcurrentJobs, app.handleDurableJob)
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("durable worker stopped: %v", err)
			}
		}()
	}
	go func() {
		scheduleTicker := time.NewTicker(time.Minute)
		cleanupTicker := time.NewTicker(15 * time.Minute)
		defer scheduleTicker.Stop()
		defer cleanupTicker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-scheduleTicker.C:
				app.runDueBackups()
			case <-cleanupTicker.C:
				auditOutbox.closePending(context.Background(), app.Config.AuditLog)
				// Also covers a worker killed mid-backup while the panel kept
				// running; a backup still holding the site lease is skipped.
				if failures := recoverWordPressMaintenance(app.Config, app.acquireSiteMutationLockContext, 5*time.Second); len(failures) > 0 {
					app.observeStateError(state.NewCleanupError("wordpress_maintenance_recovery", errors.Join(failures...), "WordPress maintenance recovery"))
				}
				app.Jobs.Cleanup(24 * time.Hour)
				if err := CleanupImportStages(app.Config.ImportRoot, time.Duration(app.Config.StageRetentionHours)*time.Hour); err != nil {
					app.observeStateError(state.NewCleanupError("import_stage_cleanup", err, "import stage cleanup"))
				}
				if err := CleanupBackupStages(app.Config.BackupRoot, orphanedStagingMinAge); err != nil {
					app.observeStateError(state.NewCleanupError("backup_stage_cleanup", err, "backup stage cleanup"))
				}
				if _, err := siteManager.CleanupOrphanedStaging(runCtx, orphanedStagingMinAge); err != nil {
					app.observeStateError(state.NewCleanupError("orphaned_staging_cleanup", err, "orphaned staging cleanup"))
				}
				if err := CleanupSiteTransactions(app.Config.RecoveryRoot, time.Duration(app.Config.StageRetentionHours)*time.Hour, app.Config.WebRoot, app.Config.MailRoot); err != nil {
					app.observeStateError(state.NewCleanupError("site_recovery_cleanup", err, "site recovery cleanup"))
				}
				app.gitActivationMu.Lock()
				if err := pruneAllGitReleases(app.Config); err != nil {
					app.observeStateError(state.NewCleanupError("git_release_retention", err, "Git release retention"))
				}
				app.gitActivationMu.Unlock()
			}
		}
	}()
	mux := http.NewServeMux()
	app.MetadataCache = NewMetadataCache(10 * time.Second) // 10-second TTL for metadata
	mux.Handle("/livez", allowMethods(http.HandlerFunc(app.livez), http.MethodGet, http.MethodHead))
	mux.Handle("/readyz", allowMethods(http.HandlerFunc(app.readyz), http.MethodGet, http.MethodHead))
	mux.Handle("/static/", allowMethods(http.StripPrefix("/static/", http.FileServer(http.FS(staticAssets))), http.MethodGet, http.MethodHead))
	mux.Handle("/login", allowMethods(http.HandlerFunc(app.Auth.Login), http.MethodGet, http.MethodPost))
	mux.Handle("/logout", allowMethods(http.HandlerFunc(app.Auth.Logout), http.MethodPost))
	mux.Handle("/", allowMethods(app.Auth.Require(http.HandlerFunc(app.dashboard)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/health", allowMethods(http.HandlerFunc(app.health), http.MethodGet, http.MethodHead))
	mux.Handle("/api/health/operational", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.operationalHealth)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/capabilities", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.handleCapabilities)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/services", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.services)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/database", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.database)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/database/diagnostics", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.databaseDiagnostics)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/database/sessions", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.databaseSessions)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/database/sessions/", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.databaseSessionTerminate)), http.MethodDelete))
	mux.Handle("/api/database/settings", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.databaseSettings)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/databases", allowMethods(app.Auth.Require(http.HandlerFunc(app.databaseCollection)), http.MethodGet, http.MethodHead, http.MethodPost))
	mux.Handle("/api/databases/", allowMethods(app.Auth.Require(http.HandlerFunc(app.databaseResource)), http.MethodGet, http.MethodHead, http.MethodPatch, http.MethodDelete))
	mux.Handle("/api/ftp", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.ftpEndpoint)), http.MethodGet, http.MethodHead, http.MethodPost))
	mux.Handle("/api/security/audit", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.securityAudit)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/security/center", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.securityCenter)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/audit/events", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.auditEvents)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/cloud", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.cloudInventory)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/cloud/action", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.cloudAction)), http.MethodPost))
	mux.Handle("/api/cloud/dns", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.cloudDNS)), http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete))
	mux.Handle("/api/dns/capabilities", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.dnsCapabilities)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/cloud/loadbalancer", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.cloudLoadBalancer)), http.MethodPost))
	mux.Handle("/api/cloud/snapshots", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.cloudSnapshots)), http.MethodGet, http.MethodHead, http.MethodDelete))
	mux.Handle("/api/ssh", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.sshInventory)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/ssh/action", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.sshAction)), http.MethodPost))
	mux.Handle("/api/security/scan", allowMethods(app.Auth.RequireAdministrator(app.limitConcurrentResource(http.HandlerFunc(app.malwareScan), "malware_scan")), http.MethodPost))
	mux.Handle("/api/certificates/issue", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.issueCertificate)), http.MethodPost))
	mux.Handle("/api/node/versions", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.nodeVersions)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/node/select", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.selectNode)), http.MethodPost))
	mux.Handle("/api/node/tooling", allowMethods(app.Auth.Require(app.siteOperation("node.tooling")), http.MethodPost))
	mux.Handle("/api/proxy/deploy", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.deployProxy)), http.MethodPost))
	mux.Handle("/api/proxy", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.proxyList)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/proxy/test", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.proxyTest)), http.MethodPost))
	mux.Handle("/api/proxy/", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.proxyManage)), http.MethodDelete))
	mux.Handle("/api/sites", allowMethods(app.Auth.Require(http.HandlerFunc(app.sites)), http.MethodGet, http.MethodHead, http.MethodPost))
	mux.Handle("/api/sites/overview", allowMethods(app.Auth.Require(http.HandlerFunc(app.siteOverviewList)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/sites/overview/", allowMethods(app.Auth.Require(http.HandlerFunc(app.siteOverviewResource)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/sites/recovery/", allowMethods(app.Auth.Require(http.HandlerFunc(app.siteRecovery)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/sites/environment/", allowMethods(app.Auth.Require(http.HandlerFunc(app.siteEnvironment)), http.MethodGet, http.MethodPut, http.MethodDelete))
	mux.Handle("/api/sites/redis/", allowMethods(app.Auth.Require(http.HandlerFunc(app.siteRedis)), http.MethodGet, http.MethodPut, http.MethodDelete))
	mux.Handle("/api/sites/access/", allowMethods(app.Auth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			app.siteAccessKey(w, r)
			return
		}
		app.siteAccess(w, r)
	})), http.MethodGet, http.MethodPatch, http.MethodPost, http.MethodDelete))
	mux.Handle("/api/workers/", allowMethods(app.Auth.Require(http.HandlerFunc(app.workers)), http.MethodGet, http.MethodPut, http.MethodPost, http.MethodDelete))
	mux.Handle("/api/tasks/{site}/{name}/kill", allowMethods(app.Auth.Require(http.HandlerFunc(app.tasks)), http.MethodPost))
	mux.Handle("/api/tasks/", allowMethods(app.Auth.Require(http.HandlerFunc(app.tasks)), http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete))
	mux.Handle("/api/python/deploy", allowMethods(app.Auth.Require(app.siteOperation("python.deploy")), http.MethodPost))
	mux.Handle("/api/python/", allowMethods(app.Auth.Require(http.HandlerFunc(app.pythonAction)), http.MethodPost))
	mux.Handle("/api/composer/", allowMethods(app.Auth.Require(app.siteOperation("composer.install")), http.MethodGet, http.MethodHead, http.MethodPost))
	mux.Handle("/api/sites/php/", allowMethods(app.Auth.Require(http.HandlerFunc(app.phpRuntime)), http.MethodGet, http.MethodHead, http.MethodPut))
	mux.Handle("/api/sites/resources/", allowMethods(app.Auth.Require(http.HandlerFunc(app.siteResources)), http.MethodGet, http.MethodHead, http.MethodPut))
	mux.Handle("/api/sites/usage/", allowMethods(app.Auth.Require(http.HandlerFunc(app.siteUsage)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/reconcile/resources", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.reconcileResources)), http.MethodPost))
	mux.Handle("/api/reconcile/tasks", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.reconcileTasksHTTP)), http.MethodPost))
	mux.Handle("/api/staging", allowMethods(app.Auth.Require(app.siteOperation("staging.create")), http.MethodPost))
	mux.Handle("/api/runner/build", allowMethods(app.Auth.Require(app.siteOperation("runner.build")), http.MethodPost))
	mux.Handle("/api/deployments", allowMethods(app.Auth.Require(http.HandlerFunc(app.deployments)), http.MethodGet, http.MethodHead, http.MethodPost))
	mux.Handle("/api/deployments/run", allowMethods(app.Auth.Require(app.siteOperation("release.pipeline")), http.MethodPost))
	mux.Handle("/api/sites/logs/", allowMethods(app.Auth.Require(http.HandlerFunc(app.siteLogs)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/sites/deploy", allowMethods(app.Auth.Require(http.HandlerFunc(app.siteDeploy)), http.MethodPost))
	mux.Handle("/api/sites/domains/claim", allowMethods(app.Auth.Require(http.HandlerFunc(app.domainClaim)), http.MethodPost))
	mux.Handle("/api/sites/domains/verify", allowMethods(app.Auth.Require(http.HandlerFunc(app.domainVerify)), http.MethodPost))
	mux.Handle("/api/sites/", allowMethods(app.Auth.Require(http.HandlerFunc(app.siteManage)), http.MethodDelete))
	mux.Handle("/api/sites/terminate", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.siteTermination)), http.MethodPost))
	mux.Handle("/api/backups", allowMethods(app.Auth.Require(http.HandlerFunc(app.backups)), http.MethodGet, http.MethodHead, http.MethodPost))
	mux.Handle("/api/backups/rehearse", allowMethods(app.Auth.Require(http.HandlerFunc(app.backupRehearse)), http.MethodPost))
	mux.Handle("/api/backups/restore-to-staging", allowMethods(app.Auth.Require(app.siteOperation("backup.restore-to-staging")), http.MethodPost))
	mux.Handle("/api/backups/restore-offsite-to-staging", allowMethods(app.Auth.Require(app.siteOperation("backup.offsite-to-staging")), http.MethodPost))
	mux.Handle("/api/backups/restore-files", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.backupRestoreFilesHTTP)), http.MethodPost))
	mux.Handle("/api/backups/restore-database", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.backupRestoreDatabaseHTTP)), http.MethodPost))
	mux.Handle("/api/backups/restore-offsite-files", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.backupRestoreOffsiteFilesHTTP)), http.MethodPost))
	mux.Handle("/api/backups/restore-offsite-database", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.backupRestoreOffsiteDatabaseHTTP)), http.MethodPost))
	mux.Handle("/api/backups/verify", allowMethods(app.Auth.Require(http.HandlerFunc(app.backupVerify)), http.MethodPost))
	mux.Handle("/api/backup-schedules", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.backupSchedules)), http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete))
	mux.Handle("/api/apps", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.appList)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/apps/deploy", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.appDeploy)), http.MethodPost))
	mux.Handle("/api/sites/git-deploy", allowMethods(app.Auth.RequireAdministrator(app.siteOperation("git.deploy")), http.MethodPost))
	mux.Handle("/api/sites/git-key/", allowMethods(app.Auth.Require(http.HandlerFunc(app.siteGitKey)), http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete))
	// Webhook endpoint now includes site in path for per-site secret isolation.
	// Old endpoint /api/sites/git-webhook supported global secret only and is deprecated.
	mux.Handle("/api/sites/git-webhook/", allowMethods(http.HandlerFunc(app.gitWebhook), http.MethodPost))
	mux.Handle("/api/admin/webhooks/", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.webhookConfig)), http.MethodPost, http.MethodPut, http.MethodDelete))
	mux.Handle("/api/sites/git-rollback", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.gitRollback)), http.MethodPost))
	mux.Handle("/api/caddy/htaccess", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.htaccessMigration)), http.MethodPost))
	mux.Handle("/api/apps/", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.appAction)), http.MethodPost))
	mux.Handle("/api/cpmove/inspect", allowMethods(app.Auth.RequireAdministrator(app.limitConcurrentResource(http.HandlerFunc(app.inspect), "extract")), http.MethodPost))
	mux.Handle("/api/cpmove/import", allowMethods(app.Auth.RequireAdministrator(app.limitConcurrentResource(http.HandlerFunc(app.importBackup), "extract")), http.MethodPost))
	mux.Handle("/api/wpress/preflight", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.wpressPreflight)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/wpress/import", allowMethods(app.Auth.RequireAdministrator(app.limitConcurrentResource(http.HandlerFunc(app.wpressImport), "extract")), http.MethodPost))
	mux.Handle("/api/wordpress/status/", allowMethods(app.Auth.Require(http.HandlerFunc(app.wordpressStatus)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/wordpress/", allowMethods(app.Auth.Require(http.HandlerFunc(app.wordpressAction)), http.MethodPost))
	mux.Handle("/api/accounts", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.accounts)), http.MethodGet, http.MethodHead, http.MethodPost))
	mux.Handle("/api/accounts/", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.accounts)), http.MethodPatch, http.MethodDelete, http.MethodPost))
	mux.Handle("/api/admin/plan-status", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.accountPlanStatus)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/admin/suspend", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.accountSuspend)), http.MethodPost))
	mux.Handle("/api/admin/unsuspend", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.accountUnsuspend)), http.MethodPost))
	mux.Handle("/api/admin/migration-doctor", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.migrationDoctor)), http.MethodPost))
	mux.Handle("/api/admin/migration-doctor/status", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.migrationAnalysisStatus)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/admin/production-readiness", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.productionReadiness)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/admin/support-bundle", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.supportBundle)), http.MethodGet))
	mux.Handle("/api/admin/resources/status", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.resourceStatus)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/admin/archive/inspect", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.inspectArchive)), http.MethodPost))
	mux.Handle("/api/admin/archive/inspect/status", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.inspectArchiveStatus)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/admin/archive/import", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.archiveImportStart)), http.MethodPost))
	mux.Handle("/api/admin/archive/import/status", allowMethods(app.Auth.RequireAdministrator(http.HandlerFunc(app.archiveImportStatus)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/account/password", allowMethods(app.Auth.Require(http.HandlerFunc(app.customerPassword)), http.MethodPost))
	mux.Handle("/api/account/mfa", allowMethods(app.Auth.Require(http.HandlerFunc(app.customerMFA)), http.MethodPost))
	mux.Handle("/api/account/sessions/revoke", allowMethods(app.Auth.Require(http.HandlerFunc(app.customerSessionsRevoke)), http.MethodPost))
	mux.Handle("/api/account/tokens", allowMethods(app.Auth.Require(http.HandlerFunc(app.apiTokens)), http.MethodGet, http.MethodPost))
	mux.Handle("/api/account/tokens/", allowMethods(app.Auth.Require(http.HandlerFunc(app.apiTokens)), http.MethodDelete))
	mux.Handle("/api/account/security", allowMethods(app.Auth.Require(http.HandlerFunc(app.customerSecurityCenter)), http.MethodGet))
	mux.Handle("/api/account/me", allowMethods(app.Auth.Require(http.HandlerFunc(app.accountMe)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/account/members", allowMethods(app.Auth.Require(http.HandlerFunc(app.accountMembers)), http.MethodGet, http.MethodHead, http.MethodPost))
	mux.Handle("/api/account/members/", allowMethods(app.Auth.Require(http.HandlerFunc(app.accountMembers)), http.MethodPatch, http.MethodDelete))
	mux.Handle("/api/account/activity", allowMethods(app.Auth.Require(http.HandlerFunc(app.accountActivity)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/admin/tokens", allowMethods(app.Auth.Require(http.HandlerFunc(app.adminAPITokens)), http.MethodGet, http.MethodPost))
	mux.Handle("/api/admin/tokens/", allowMethods(app.Auth.Require(http.HandlerFunc(app.adminAPITokens)), http.MethodDelete))
	mux.Handle("/api/jobs/", allowMethods(app.Auth.Require(http.HandlerFunc(app.jobStatus)), http.MethodGet, http.MethodHead, http.MethodPost))
	mux.Handle("/api/jobs", allowMethods(app.Auth.Require(http.HandlerFunc(app.jobList)), http.MethodGet, http.MethodHead))
	mux.Handle("/api/jobs/events", allowMethods(app.Auth.Require(http.HandlerFunc(app.jobEvents)), http.MethodGet, http.MethodHead))
	metricsHandler := http.Handler(http.HandlerFunc(app.metrics))
	if os.Getenv("STEPANEL_METRICS_PUBLIC") != "1" {
		metricsHandler = app.Auth.RequireAdministrator(metricsHandler)
	}
	mux.Handle("/metrics", allowMethods(metricsHandler, http.MethodGet, http.MethodHead))
	timeoutCfg := httputil.DefaultTimeouts()
	// Wrap with security headers middleware (must be outermost)
	// Apply route-aware request deadlines before security/logging middleware so
	// every handler sees the bounded context. The server-level deadlines below
	// use the longest supported request classes; the middleware keeps ordinary
	// endpoints short without making large archive uploads impossible.
	timedHandler := timeoutCfg.Middleware()(mux)
	secureHandler := securityHeadersMiddleware(cfg)(logging(normalizeAPIErrors(timedHandler), app.Metrics, cfg.Production))

	// Configure differentiated timeouts to prevent malicious clients from holding
	// connections. Normal API requests use short timeouts; uploads/downloads use
	// longer timeouts. This prevents resource exhaustion from slow uploads.
	server := &http.Server{
		Addr:           cfg.Listen,
		Handler:        secureHandler,
		MaxHeaderBytes: 1 << 20, // 1 MiB
	}

	timeoutCfg.ApplyToServer(server)

	// Keep the header and idle deadlines short to resist slowloris clients;
	// request body/response deadlines are extended to cover the supported
	// upload/download classes and tightened per route by timedHandler.
	server.ReadHeaderTimeout = 5 * time.Second
	server.ReadTimeout = timeoutCfg.UploadRead
	server.WriteTimeout = timeoutCfg.DownloadWrite
	server.IdleTimeout = 30 * time.Second
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		log.Printf("StePanel listening on %s", cfg.Listen)
		if cfg.TLSCertFile != "" {
			err := server.ServeTLS(listener, cfg.TLSCertFile, cfg.TLSKeyFile)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatal(err)
			}
		} else {
			err := server.Serve(listener)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatal(err)
			}
		}
	}()
	// The listener is bound above, so readiness probes can connect before
	// startup recovery finishes.
	go runStartup()
	<-runCtx.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown: %v", err)
	}
	jobCtx, jobCancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer jobCancel()
	if err := app.Jobs.Wait(jobCtx); err != nil {
		log.Printf("timed out waiting for active jobs: %v", err)
	}
	auditCtx, auditCancel := context.WithTimeout(context.Background(), 10*time.Second)
	auditOutbox.closePending(auditCtx, app.Config.AuditLog)
	auditCancel()
}

func (a *App) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	csrf := ""
	if cookie, err := r.Cookie("stepanel_csrf"); err == nil {
		csrf = cookie.Value
	}
	isAdministrator := a.Auth.IsAdministrator(r)
	servers := ServiceSummaries(a.Config)
	jobs := a.Jobs.List(8)
	var account HostingAccount
	accountSiteCount := 0
	if !isAdministrator {
		servers = nil
		jobs = filterAccountJobs(jobs, a.Accounts, a.Auth.UsernameForRequest(r))
		if a.Accounts != nil {
			account, _ = a.Accounts.Get(a.Auth.UsernameForRequest(r))
			if sites, err := a.Accounts.GetSitesWithError(a.Auth.UsernameForRequest(r)); err == nil {
				accountSiteCount = len(sites)
			}
		}
	}
	healthy, alerts := 0, 0
	for _, server := range servers {
		switch server.Status {
		case "active", "enabled", "installed":
			healthy++
		default:
			alerts++
		}
	}
	security := []SecurityCheck{}
	if isAdministrator {
		security = a.SecurityChecks()
	}
	if err := a.View.Execute(w, map[string]any{"Title": "StePanel", "Config": a.Config, "AssetVersion": a.AssetVersion, "CSRF": csrf, "AuthEnabled": a.Auth.Enabled, "Username": a.Auth.UsernameForRequest(r), "Now": time.Now(), "Servers": servers, "Healthy": healthy, "Alerts": alerts, "Security": security, "Jobs": jobs, "Capabilities": a.Capabilities(), "Database": a.DatabaseAdmin(), "IsAdministrator": isAdministrator, "Account": account, "AccountSiteCount": accountSiteCount}); err != nil {
		log.Printf("dashboard render failed: %v", err)
	}
}

func filterAccountJobs(jobs []Job, accounts *AccountStore, username string) []Job {
	if accounts == nil {
		return nil
	}
	filtered := make([]Job, 0, len(jobs))
	for _, job := range jobs {
		if accounts.OwnsSite(username, job.User) {
			filtered = append(filtered, job)
		}
	}
	return filtered
}
func (a *App) health(w http.ResponseWriter, r *http.Request) {
	response := map[string]any{"ok": true, "version": Version, "commit": Commit, "time": time.Now().UTC()}
	if !a.Auth.Enabled || a.Auth.IsAdministrator(r) {
		response["services"] = ServiceStatus()
	}
	writeJSON(w, http.StatusOK, response)
}
func (a *App) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	a.Metrics.Write(w)
	writeJobMetrics(w, a.Jobs)
	writeDatabaseMetrics(w, a.cachedDatabaseDiagnostics(15*time.Second))
	writeReadinessMetrics(w, readinessChecks(a.Config, a.Jobs))
	if a.Schedules != nil {
		writeBackupScheduleMetrics(w, a.Schedules.list())
	}
	writeGitReleaseMetrics(w, a.Config.WebRoot)
}
func (a *App) services(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"services": ServiceSummaries(a.Config), "time": time.Now().UTC()})
}
func (a *App) securityAudit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"checks": a.SecurityChecks(), "time": time.Now().UTC()})
}
func (a *App) Capabilities() map[string]bool {
	nodeVersions, _ := os.ReadDir(filepath.Join(a.Config.NVMDir, "versions", "node"))
	hasNode := false
	for _, entry := range nodeVersions {
		if entry.IsDir() && nodeVersionPattern.MatchString(entry.Name()) {
			hasNode = true
			break
		}
	}
	return map[string]bool{
		"certificates":       a.Config.WebServer == "apache" && commandAvailable(a.Config.Certbot),
		"mysql_restores":     mysqlCompatible(a.Config),
		"database_lifecycle": a.DatabaseAdmin().LifecycleReady,
		"database_pitr":      false,
		"database_failover":  false,
		"node_apps":          hasNode && commandAvailable(a.Config.AppCtl) && commandAvailable(a.Config.ProxyCtl),
		"wpress":             allReady(WPressPreflight(a.Config)),
		"htaccess_import":    a.Config.WebServer == "caddy" && commandAvailable(a.Config.VHostCtl),
	}
}
func (a *App) capabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"capabilities": a.Capabilities(), "time": time.Now().UTC()})
}
func (a *App) inspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if a.Config.MaxUpload > 0 && r.ContentLength > maxUploadRequestBytes(a.Config.MaxUpload) {
		http.Error(w, "upload exceeds the configured size limit", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadRequestBytes(a.Config.MaxUpload))
	if !a.Auth.CSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	uploadID, err := randomSecret()
	if err != nil {
		http.Error(w, "could not create upload ID", 500)
		return
	}
	// Stream the archive straight into the immutable upload object: no
	// multipart spool file, one disk write.
	archivePath := cpmoveUploadPath(a.Config.ImportRoot, uploadID)
	staged, reservation, ok := a.stageArchiveUpload(w, r, "cPanel upload", upload.Options{
		Create: func(string) (*os.File, error) {
			return os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		},
	})
	if !ok {
		return
	}
	// The archive is fully on disk; the post-inspection check below charges
	// the inspected expanded size instead of the upload-time estimate.
	reservation.release()
	written := staged.Size
	stored, err := os.Open(archivePath)
	if err != nil {
		_ = os.Remove(archivePath)
		http.Error(w, "could not inspect staged upload", 500)
		return
	}
	info, err := InspectCPMove(stored, &multipart.FileHeader{Filename: staged.Filename, Size: written})
	storedCloseErr := stored.Close()
	if err != nil {
		_ = os.Remove(archivePath)
		http.Error(w, "backup archive could not be inspected", http.StatusUnprocessableEntity)
		return
	}
	if storedCloseErr != nil {
		_ = os.Remove(archivePath)
		http.Error(w, "could not finalize staged upload", http.StatusInternalServerError)
		return
	}
	// Whether databases will be restored is chosen at import; check them then.
	if err := a.checkCPMoveCapacity(info.ExpandedBytes, info.DatabaseBytes, false); err != nil {
		_ = os.Remove(archivePath)
		http.Error(w, "insufficient free space for archive inspection", http.StatusInsufficientStorage)
		return
	}
	if required, _, err := cpmoveRequiredFreeBytes(a.Config, written, info.ExpandedBytes); err == nil {
		info.RequiredFreeBytes = int64(required)
	}
	info.UploadID = uploadID
	owner := a.Auth.UsernameForRequest(r)
	metadata := cpmoveUpload{ID: uploadID, Path: archivePath, Filename: staged.Filename, Size: written, ExpandedBytes: info.ExpandedBytes, DatabaseBytes: info.DatabaseBytes, SHA256: staged.SHA256, Owner: owner, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}
	metadataBytes, marshalErr := json.Marshal(metadata)
	metadataErr := marshalErr
	if metadataErr == nil && owner != "" {
		metadataErr = writeAtomic(cpmoveUploadMetadataPath(a.Config.ImportRoot, uploadID), metadataBytes, 0600)
	}
	if metadataErr != nil {
		_ = os.Remove(archivePath)
		_ = os.Remove(cpmoveUploadMetadataPath(a.Config.ImportRoot, uploadID))
		http.Error(w, "could not persist upload", 500)
		return
	}
	writeJSON(w, 200, info.response(uploadID))
}

type durableCPMoveRequest struct {
	UploadID   string `json:"upload_id"`
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	User       string `json:"user"`
	Actor      string `json:"actor"`
	RestoreDBs bool   `json:"restore_databases"`
}

type durableBackupRequest struct {
	Site             string    `json:"site"`
	IncludeDatabases bool      `json:"include_databases"`
	Scheduled        bool      `json:"scheduled"`
	KeepLast         int       `json:"keep_last,omitempty"`
	Actor            string    `json:"actor"`
	StartedAt        time.Time `json:"started_at,omitempty"`
	BackupPath       string    `json:"backup_path,omitempty"`
	BackupSHA256     string    `json:"backup_sha256,omitempty"`
	BackupBytes      int64     `json:"backup_bytes,omitempty"`
}

type durableCertificateRequest struct {
	Domain string `json:"domain"`
	Email  string `json:"email"`
	Actor  string `json:"actor"`
}

type durableWPressRequest struct {
	TempPath     string `json:"temp_path"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Site         string `json:"site"`
	DBSuffix     string `json:"db_suffix"`
	DBUserSuffix string `json:"db_user_suffix"`
	Password     string `json:"password"`
	SiteURL      string `json:"site_url"`
	TargetPrefix string `json:"target_prefix"`
	Force        bool   `json:"force"`
	Actor        string `json:"actor"`
}

func (a *App) handleCPMoveJob(ctx context.Context, item Job) ([]byte, error) {
	var request durableCPMoveRequest
	if err := json.Unmarshal(item.Payload, &request); err != nil {
		return nil, fmt.Errorf("decode cpmove job payload: %w", err)
	}
	upload, uploadErr := readCPMoveUpload(a.Config.ImportRoot, request.UploadID)
	if safeUser(request.User) == "" || request.Filename == "" || request.Size < 0 || uploadErr != nil || upload.Owner != request.Actor || request.Filename != upload.Filename || request.Size != upload.Size {
		return nil, errors.New("invalid durable cpmove job payload")
	}
	access, err := a.authorizeDurableSiteJob(request.User, request.Actor, false)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := verifyCPMoveUpload(upload); err != nil {
		return nil, err
	}
	if a.Jobs.CancellationRequested(item.ID) {
		return nil, errors.New("cpmove restore cancelled before execution")
	}
	removeUpload := false
	defer func() {
		if removeUpload {
			_ = os.Remove(upload.Path)
			_ = os.Remove(cpmoveUploadMetadataPath(a.Config.ImportRoot, request.UploadID))
		}
	}()
	if err := a.checkCPMoveCapacity(upload.ExpandedBytes, upload.DatabaseBytes, request.RestoreDBs); err != nil {
		return nil, err
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(ctx, request.User)
	if lockErr != nil {
		return nil, fmt.Errorf("acquire cpmove site lock: %w", lockErr)
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		return nil, err
	}
	if err := a.reconcileInterruptedSiteWithLease(operationCtx, request.User, a.siteManager); err != nil {
		return nil, fmt.Errorf("reconcile interrupted cpmove restore: %w", err)
	}
	intent, err := BeginSecurityAudit(a.Config.AuditLog, request.Actor, "cpmove.restore", request.User, request.Filename)
	if err != nil {
		return nil, err
	}
	a.Metrics.RestoreStarted()
	result, restoreErr := restoreCPMoveArchiveContext(operationCtx, a.Config, upload.Path, &multipart.FileHeader{Filename: request.Filename, Size: request.Size}, access, request.RestoreDBs)
	a.Metrics.RestoreFinished(restoreErr)
	if restoreErr != nil {
		intent.Failed(restoreErr.Error())
		return nil, restoreErr
	}
	intent.Completed(result.StagedAt)
	removeUpload = true

	// Invalidate metadata caches after successful site restore
	if a.MetadataCache != nil {
		a.MetadataCache.InvalidateSite(request.User)
	}

	output, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode cpmove result: %w", err)
	}
	return output, nil
}

func (a *App) handleBackupJob(ctx context.Context, item Job) ([]byte, error) {
	var request durableBackupRequest
	if err := json.Unmarshal(item.Payload, &request); err != nil {
		return nil, fmt.Errorf("decode backup job payload: %w", err)
	}
	if safeUser(request.Site) == "" || request.Actor == "" || (request.Scheduled && request.KeepLast < 1) {
		return nil, errors.New("invalid durable backup job payload")
	}
	access, err := a.authorizeDurableSiteJob(request.Site, request.Actor, request.Scheduled)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil || a.Jobs.CancellationRequested(item.ID) {
		return nil, context.Canceled
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(ctx, request.Site)
	if lockErr != nil {
		return nil, fmt.Errorf("acquire backup site lock: %w", lockErr)
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		return nil, err
	}
	var result BackupResult
	if request.BackupPath != "" {
		root, rootErr := filepath.Abs(a.Config.BackupRoot)
		candidate, pathErr := filepath.Abs(request.BackupPath)
		if rootErr != nil || pathErr != nil || candidate == root || !strings.HasPrefix(candidate, root+string(filepath.Separator)) {
			return nil, errors.New("persisted backup path is outside the backup root")
		}
		manifest, verifyErr := VerifySiteBackupStrict(candidate, a.Config.BackupSigningKey, a.Config.backupDecryptionKeys()...)
		if verifyErr != nil || manifest.Site != request.Site || manifest.ArchiveSHA256 != request.BackupSHA256 || (request.BackupBytes > 0 && manifest.Bytes != request.BackupBytes) {
			if verifyErr == nil {
				verifyErr = errors.New("persisted backup metadata does not match the job")
			}
			return nil, fmt.Errorf("persisted backup is not reusable: %w", verifyErr)
		}
		result = BackupResult{Site: request.Site, Path: candidate, ArchiveSHA256: manifest.ArchiveSHA256, Bytes: manifest.Bytes, Databases: manifest.Databases, CreatedAt: manifest.CreatedAt, VerifiedAt: manifest.VerifiedAt, Consistency: manifest.Consistency, ManifestSigned: manifest.SignatureAlgorithm != "", Encrypted: manifest.Encryption != ""}
	} else {
		result, err = createSiteBackupContext(operationCtx, a.Config, access, request.IncludeDatabases, &a.capacity)
		if err != nil {
			TelemetryAudit(a.Config.AuditLog, request.Actor, "site.backup.failed", request.Site, err.Error())
			if request.Scheduled {
				started := request.StartedAt
				if started.IsZero() {
					started = time.Now()
				}
				a.Schedules.recordResult(request.Site, started, err)
			}
			return nil, err
		}
		request.BackupPath, request.BackupSHA256, request.BackupBytes = result.Path, result.ArchiveSHA256, result.Bytes
		if a.Jobs != nil && item.LeaseOwner != "" {
			payload, marshalErr := json.Marshal(request)
			if marshalErr != nil {
				return nil, fmt.Errorf("encode reusable backup state: %w", marshalErr)
			}
			if updateErr := a.Jobs.UpdateClaimPayload(item.ID, item.LeaseOwner, payload); updateErr != nil {
				return nil, fmt.Errorf("persist reusable backup state: %w", updateErr)
			}
		}
	}
	if err = a.uploadOffsiteBackup(operationCtx, result); err != nil {
		TelemetryAudit(a.Config.AuditLog, request.Actor, "site.backup.offsite_failed", request.Site, err.Error())
		if request.Scheduled {
			started := request.StartedAt
			if started.IsZero() {
				started = time.Now()
			}
			a.Schedules.recordResult(request.Site, started, err)
		}
		return nil, err
	}
	TelemetryAudit(a.Config.AuditLog, request.Actor, "site.backup.completed", request.Site, result.ArchiveSHA256)
	if request.Scheduled {
		started := request.StartedAt
		if started.IsZero() {
			started = time.Now()
		}
		a.Schedules.recordResult(request.Site, started, nil)
		if err := pruneSiteBackups(a.Config.BackupRoot, access, request.KeepLast); err != nil {
			TelemetryAudit(a.Config.AuditLog, request.Actor, "backup.retention.failed", request.Site, err.Error())
		}
		a.maybeEnqueueScheduledRehearsal(ctx, request.Site, filepath.Base(result.Path))
	}
	output, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode backup result: %w", err)
	}
	return output, nil
}

func (a *App) handleCertificateJob(ctx context.Context, item Job) ([]byte, error) {
	var request durableCertificateRequest
	if err := json.Unmarshal(item.Payload, &request); err != nil {
		return nil, fmt.Errorf("decode certificate job payload: %w", err)
	}
	if !domainname.Valid(request.Domain) || request.Email == "" || request.Actor == "" {
		return nil, errors.New("invalid durable certificate job payload")
	}
	certificateCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", a.Config.WebRoot)
	if err != nil {
		return nil, fmt.Errorf("configure certificate broker: %w", err)
	}
	response, err := client.IssueCertificate(certificateCtx, request.Domain, request.Email)
	if err != nil {
		return nil, fmt.Errorf("certificate broker failed: %w", err)
	}
	if !response.OK {
		return nil, fmt.Errorf("certificate helper failed: %s", response.Error)
	}
	TelemetryAudit(a.Config.AuditLog, request.Actor, "certificate.issued", request.Domain, "Let's Encrypt certificate requested")
	output, err := json.Marshal(CertificateResult{Domain: request.Domain, Status: "issued"})
	if err != nil {
		return nil, err
	}
	return output, nil
}

func (a *App) handleWPressJob(ctx context.Context, item Job) ([]byte, error) {
	var request durableWPressRequest
	if err := json.Unmarshal(item.Payload, &request); err != nil {
		return nil, fmt.Errorf("decode WordPress job payload: %w", err)
	}
	if err := validateWPressInput(request.Site, request.DBSuffix, request.DBUserSuffix, request.Password, request.TargetPrefix, request.SiteURL); err != nil || request.Actor == "" || request.Size < 0 || len(request.SHA256) != 64 || ensureInside(a.Config.ImportRoot, request.TempPath) != nil {
		if err == nil {
			err = errors.New("invalid WordPress job payload")
		}
		return nil, err
	}
	access, err := a.authorizeDurableSiteJob(request.Site, request.Actor, false)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil || a.Jobs.CancellationRequested(item.ID) {
		return nil, context.Canceled
	}
	if err := verifyWPressUpload(request.TempPath, request.Size, request.SHA256); err != nil {
		return nil, err
	}
	removeStaged := false
	defer func() {
		if removeStaged {
			_ = os.Remove(request.TempPath)
		}
	}()
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(ctx, request.Site)
	if lockErr != nil {
		return nil, fmt.Errorf("acquire WordPress site lock: %w", lockErr)
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		return nil, err
	}
	if err := a.reconcileInterruptedSiteWithLease(operationCtx, request.Site, a.siteManager); err != nil {
		return nil, fmt.Errorf("reconcile interrupted WordPress restore: %w", err)
	}
	// The staged archive is already on disk; extraction and the site staging
	// tree each need about its size again.
	if err := a.capacity.check(a.Config, "WPress restore", stagedArchiveDemands(a.Config, uint64(request.Size))); err != nil {
		return nil, err
	}
	intent, err := BeginSecurityAudit(a.Config.AuditLog, request.Actor, "wordpress.restore", request.Site, "staged WPress archive")
	if err != nil {
		return nil, err
	}
	a.Metrics.RestoreStarted()
	result, restoreErr := RestoreWPressContext(operationCtx, a.Config, request.TempPath, access, request.DBSuffix, request.DBUserSuffix, request.Password, request.SiteURL, request.TargetPrefix, request.Force)
	a.Metrics.RestoreFinished(restoreErr)
	if restoreErr != nil {
		intent.Failed(restoreErr.Error())
		return nil, restoreErr
	}
	detail := fmt.Sprintf("%s; metadata=%t; htaccess=%t", result.StagedAt, result.MetadataApplied, result.HTAccessRestored)
	intent.Completed(detail)
	removeStaged = true
	output, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode WordPress result: %w", err)
	}
	return output, nil
}

func (a *App) handleDurableJob(ctx context.Context, item Job) ([]byte, error) {
	if a.ResourceBudget != nil {
		workload := durableWorkloadClass(item.Kind)
		if workload != "" {
			if !a.ResourceBudget.AcquireSlotContext(ctx, workload) {
				return nil, ctx.Err()
			}
			defer a.ResourceBudget.ReleaseSlot(workload)
		}
	}
	switch item.Kind {
	case "cpmove.restore":
		return a.handleCPMoveJob(ctx, item)
	case "site.backup":
		return a.handleBackupJob(ctx, item)
	case "certificate.issue":
		return a.handleCertificateJob(ctx, item)
	case "wordpress.restore":
		return a.handleWPressJob(ctx, item)
	case "backup.restore":
		return a.handleBackupRestoreJob(ctx, item)
	case "backup.rehearsal":
		return a.handleBackupRehearsalJob(ctx, item)
	case "cloud.action":
		return a.handleCloudJob(ctx, item)
	case "node.deployment":
		return a.handleNodeDeploymentJob(ctx, item)
	case "site.terminate":
		return a.handleSiteTermination(ctx, item)
	case "migration.analysis":
		return a.handleMigrationAnalysisJob(&item)
	case "archive.inspect":
		return a.handleArchiveInspectionJob(ctx, &item)
	case "archive.import":
		return a.handleArchiveImportJob(ctx, &item)
	case "site.create":
		return a.handleSiteCreation(ctx, item)
	case siteOperationKind:
		return a.handleSiteOperationJob(ctx, item)
	default:
		return nil, fmt.Errorf("no durable worker handler for job kind %q", item.Kind)
	}
}

func durableWorkloadClass(kind string) string {
	switch kind {
	case "cpmove.restore", "wordpress.restore", "backup.restore", "backup.rehearsal":
		return "restore"
	case "site.backup":
		return "backup"
	case "archive.inspect", "archive.import", "migration.analysis", "site.create":
		return "extract"
	case "cloud.action", "certificate.issue", "node.deployment", siteOperationKind:
		return "build"
	case "site.terminate":
		return "db_restore"
	default:
		return ""
	}
}

func (a *App) importBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	// The archive was already staged by inspection; this request carries only
	// form fields, so a small body cap keeps multipart parsing in memory and
	// prevents a file part from being spooled to disk.
	if r.ContentLength > cpmoveImportRequestBytes {
		http.Error(w, "import request is too large; upload the archive through inspection", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, cpmoveImportRequestBytes)
	if !a.Auth.CSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	err := r.ParseMultipartForm(cpmoveImportRequestBytes)
	defer cleanupMultipartForm(r)
	if err != nil {
		http.Error(w, "invalid upload form", http.StatusBadRequest)
		return
	}
	if r.FormValue("confirm") != "IMPORT" {
		http.Error(w, "type IMPORT to authorize restore", 400)
		return
	}
	databaseRestore := r.FormValue("restore_databases") == "on"
	if databaseRestore && !mysqlCompatible(a.Config) {
		http.Error(w, "cPanel SQL restores require MySQL or MariaDB; PostgreSQL dump conversion is not supported", http.StatusUnprocessableEntity)
		return
	}
	uploadID := strings.TrimSpace(r.FormValue("upload_id"))
	actor := a.Auth.UsernameForRequest(r)
	upload, err := readCPMoveUpload(a.Config.ImportRoot, uploadID)
	if err != nil || upload.Owner != actor {
		http.Error(w, "upload is missing, expired, or belongs to another operator", http.StatusNotFound)
		return
	}
	if err := a.checkCPMoveCapacity(upload.ExpandedBytes, upload.DatabaseBytes, databaseRestore); err != nil {
		http.Error(w, "insufficient free space for archive restore", http.StatusInsufficientStorage)
		return
	}
	user := safeUser(r.FormValue("username"))
	if user == "" {
		http.Error(w, "a valid account username is required", 400)
		return
	}
	operationKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if operationKey != "" && !validJobOperationKey(operationKey) {
		http.Error(w, "invalid Idempotency-Key", http.StatusUnprocessableEntity)
		return
	}
	payload, err := json.Marshal(durableCPMoveRequest{UploadID: uploadID, Filename: upload.Filename, Size: upload.Size, User: user, Actor: actor, RestoreDBs: databaseRestore})
	if err != nil {
		http.Error(w, "could not encode restore job", http.StatusInternalServerError)
		return
	}
	queued, existing, err := a.Jobs.EnqueueIdempotent("cpmove.restore", user, operationKey, payload, 3)
	if err != nil {
		http.Error(w, "could not persist restore job", http.StatusInternalServerError)
		return
	}
	_ = existing
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": queued.ID, "status_url": filepath.Join("/api/jobs", queued.ID)})
}
func (a *App) jobStatus(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/retry") {
		if r.Method != http.MethodPost || !a.Auth.IsAdministrator(r) || !a.Auth.CSRF(r) {
			http.Error(w, "administrator dead-letter retry required", http.StatusForbidden)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/jobs/"), "/retry")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		if err := a.Jobs.RequeueDeadLetter(id); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "job.dead_letter.requeued", id, "operator-approved retry")
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "requeued", "job_id": id})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	job, ok, getErr := a.Jobs.GetWithError(id)
	if getErr != nil {
		http.Error(w, "job status is temporarily unavailable: control-plane database read failed", http.StatusServiceUnavailable)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, job.User, "job is not assigned to this account", http.StatusForbidden); !ok {
		return
	}
	if r.Method == http.MethodPost {
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		if err := a.Jobs.RequestCancel(id); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancellation-requested"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}
func (a *App) jobList(w http.ResponseWriter, r *http.Request) {
	jobs := a.Jobs.List(100)
	if !a.Auth.IsAdministrator(r) {
		jobs = filterAccountJobs(jobs, a.Accounts, a.Auth.UsernameForRequest(r))
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs, "time": time.Now().UTC()})
}

// jobEventsHeartbeat keeps intermediaries from idling out the job stream.
var jobEventsHeartbeat = 25 * time.Second

func (a *App) jobEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "event streaming is unavailable", http.StatusNotImplemented)
		return
	}
	release, admitted := a.streams.acquire(a.Auth.UsernameForRequest(r), maxEventStreamsPerPrincipal, maxEventStreamsTotal)
	if !admitted {
		a.Metrics.StreamRejected()
		w.Header().Set("Retry-After", "30")
		http.Error(w, "too many open event streams; close other dashboard tabs", http.StatusTooManyRequests)
		return
	}
	defer release()
	a.Metrics.StreamOpened()
	defer a.Metrics.StreamClosed()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	visible := func(job Job) bool {
		if a.Auth.IsAdministrator(r) {
			return true
		}
		return a.Accounts != nil && a.Accounts.OwnsSite(a.Auth.UsernameForRequest(r), job.User)
	}
	// The timeout middleware leaves this stream without a context deadline.
	// Each write gets its own deadline instead, which both detects a dead
	// peer and lifts the server-wide WriteTimeout that would otherwise kill a
	// healthy stream. Every heartbeat re-checks the credential, and the
	// stream rotates after a jittered lifetime so clients reconnect without
	// arriving in lockstep after a restart.
	timeouts := httputil.DefaultTimeouts()
	lifetime := timeouts.StreamLifetime - rand.N(timeouts.StreamLifetime/6)
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(lifetime + timeouts.StreamWrite)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		http.Error(w, "event streaming is unavailable", http.StatusInternalServerError)
		return
	}
	write := func(payload string) bool {
		if err := controller.SetWriteDeadline(time.Now().Add(timeouts.StreamWrite)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		if _, err := io.WriteString(w, payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	send := func(event string, value any) bool {
		data, err := json.Marshal(value)
		if err != nil {
			return false
		}
		return write(fmt.Sprintf("event: %s\ndata: %s\n\n", event, data))
	}

	updates, unsubscribe := a.Jobs.Subscribe()
	defer unsubscribe()
	jobs := a.Jobs.List(100)
	if !a.Auth.IsAdministrator(r) {
		jobs = filterAccountJobs(jobs, a.Accounts, a.Auth.UsernameForRequest(r))
	}
	if !send("snapshot", map[string]any{"jobs": jobs, "time": time.Now().UTC()}) {
		return
	}
	heartbeat := time.NewTicker(jobEventsHeartbeat)
	defer heartbeat.Stop()
	rotate := time.NewTimer(lifetime)
	defer rotate.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-rotate.C:
			return
		case event, open := <-updates:
			if !open {
				return
			}
			if visible(event.Job) && !send("job", event.Job) {
				return
			}
		case <-heartbeat.C:
			if !a.Auth.StillAuthenticated(r) || !write(": keep-alive\n\n") {
				return
			}
		}
	}
}

func logging(next http.Handler, metrics *Metrics, production bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID, err := randomSecret()
		if err != nil {
			requestID = fmt.Sprintf("fallback-%d", started.UnixNano())
		} else if len(requestID) > 20 {
			requestID = requestID[:20]
		}
		r = r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, requestID))
		wrapped := &statusWriter{ResponseWriter: w}
		w.Header().Set("X-Request-ID", requestID)
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			w.Header().Set("Cache-Control", "no-store")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		next.ServeHTTP(wrapped, r)
		if wrapped.status == 0 {
			wrapped.status = http.StatusOK
		}
		// An accepted event stream lives for up to 30 minutes; it is tracked
		// by the stream gauges instead of the request latency histogram.
		if metrics != nil && !(httputil.IsStreamPath(r.URL.Path) && wrapped.status == http.StatusOK) {
			metrics.ObserveHTTP(wrapped.status, time.Since(started))
		}
		logJSON(r, wrapped.status, time.Since(started))
	})
}

func allowMethods(next http.Handler, methods ...string) http.Handler {
	allowed := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		allowed[method] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := allowed[r.Method]; !ok {
			w.Header().Set("Allow", strings.Join(methods, ", "))
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// limitConcurrentResource enforces per-workload-class concurrency limits from the global
// resource budget. This prevents any single workload type from starving others by
// overwhelming the host I/O or connection pools. Returns 429 if budget is exhausted.
func (a *App) limitConcurrentResource(next http.Handler, workloadClass string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.ResourceBudget.AcquireSlot(workloadClass) {
			http.Error(w, "server is busy; retry shortly", http.StatusTooManyRequests)
			return
		}
		defer a.ResourceBudget.ReleaseSlot(workloadClass)
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, destination any) error {
	// Validate limit to prevent potential DoS. All callers use hardcoded constants.
	const maxLimit = 1 << 20 // 1 MiB max
	if limit <= 0 || limit > maxLimit {
		return fmt.Errorf("invalid decode limit: %d", limit)
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func cleanupMultipartForm(r *http.Request) {
	if r.MultipartForm != nil {
		_ = r.MultipartForm.RemoveAll()
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func logJSON(r *http.Request, status int, duration time.Duration) {
	if status == 0 {
		status = http.StatusOK
	}
	requestID, _ := r.Context().Value(requestIDContextKey{}).(string)
	log.Printf(`{"level":"info","request_id":%q,"method":%q,"path":%q,"status":%d,"duration_ms":%.3f}`, requestID, r.Method, r.URL.Path, status, float64(duration.Microseconds())/1000)
}

// controlPlaneLeaseTime is the duration of durable resource leases. Owners
// renew every third of it; the root broker's fencing watchdog cancels an
// operation whose lease has under rootbroker.LeaseWatchMargin left, so this
// must stay well above that margin (see TestLeaseTimeLeavesWatchdogHeadroom).
const controlPlaneLeaseTime = 2 * time.Minute

type requestIDContextKey struct{}

// maxAPIErrorBody bounds how much of a plain-text error body is captured
// for the JSON error envelope.
const maxAPIErrorBody = 64 << 10

// apiErrorWriter passes successful API responses straight through (so
// downloads stream and server-sent events flush) and captures only
// plain-text error bodies so they can be rewritten as a JSON envelope.
type apiErrorWriter struct {
	w           http.ResponseWriter
	wroteHeader bool
	capture     bool
	status      int
	body        bytes.Buffer
}

func (e *apiErrorWriter) Header() http.Header { return e.w.Header() }

func (e *apiErrorWriter) WriteHeader(status int) {
	if e.wroteHeader {
		return
	}
	e.wroteHeader = true
	e.status = status
	contentType := strings.ToLower(e.w.Header().Get("Content-Type"))
	if status >= 400 && !strings.HasPrefix(contentType, "application/json") && !strings.HasPrefix(contentType, "application/problem+json") {
		e.capture = true
		return
	}
	// Headers are snapshotted when WriteHeader is forwarded, so the safe
	// default must be chosen here: an untyped API body is never sniffed
	// as HTML/JS by a browser.
	if contentType == "" && bodyAllowedForStatus(status) {
		e.w.Header().Set("Content-Type", "application/octet-stream")
	}
	e.w.WriteHeader(status)
}

// bodyAllowedForStatus reports whether status may carry a response body.
func bodyAllowedForStatus(status int) bool {
	switch {
	case status >= 100 && status <= 199:
		return false
	case status == http.StatusNoContent, status == http.StatusNotModified:
		return false
	}
	return true
}

func (e *apiErrorWriter) Write(body []byte) (int, error) {
	if !e.wroteHeader {
		e.WriteHeader(http.StatusOK)
	}
	if !e.capture {
		// Copy through the underlying writer without making this middleware a
		// response-body sink. The API middleware only forwards responses whose
		// handler selected their content type; error bodies are captured above.
		// lgtm [go/reflected-xss] -- multipart upload handlers bypass this
		// writer; JSON handlers use encoding/json's HTML escaping and untyped
		// bodies are forced to application/octet-stream in WriteHeader.
		return e.w.Write(body)
	}
	if remaining := maxAPIErrorBody - e.body.Len(); remaining > 0 {
		if len(body) > remaining {
			e.body.Write(body[:remaining])
		} else {
			e.body.Write(body)
		}
	}
	return len(body), nil
}

func (e *apiErrorWriter) Flush() {
	if e.capture {
		return
	}
	if flusher, ok := e.w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (e *apiErrorWriter) Unwrap() http.ResponseWriter { return e.w }

// apiError is the JSON error envelope for every /api/ error response.
type apiError struct {
	Error      string `json:"error"`
	Code       int    `json:"code"`
	ErrorCode  string `json:"error_code,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	Resource   string `json:"resource,omitempty"`
	Retryable  bool   `json:"retryable"`
	NextAction string `json:"next_action"`
}

// apiErrorMetadata supplies stable, machine-readable guidance while retaining
// the legacy string error and numeric HTTP code fields for existing clients.
func apiErrorMetadata(status int, resource string) (code, nextAction string, retryable bool) {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request", "Correct the request and try again.", false
	case http.StatusUnauthorized:
		return "unauthorized", "Authenticate again and retry.", false
	case http.StatusForbidden:
		return "forbidden", "Use an authorized account or token.", false
	case http.StatusNotFound:
		return "not_found", "Verify the resource identifier and try again.", false
	case http.StatusConflict:
		return "conflict", "Wait for the current operation to finish or cancel it, then retry.", true
	case http.StatusUnprocessableEntity:
		return "validation_failed", "Correct the request fields and try again.", false
	case http.StatusTooManyRequests:
		return "rate_limited", "Wait briefly and retry.", true
	case http.StatusInsufficientStorage:
		return "insufficient_storage", "Free disk space or reduce the operation size, then retry.", false
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return "upstream_unavailable", "Restore service readiness and retry.", true
	case http.StatusNotImplemented:
		return "unsupported", "Enable the required capability or use a supported operation.", false
	default:
		if status >= 500 {
			return "internal_error", "Use the request ID when checking server logs before retrying.", true
		}
		return fmt.Sprintf("http_%d", status), "Review the response and try again if appropriate.", false
	}
}

// normalizeAPIErrors gives API clients one predictable JSON error envelope
// carrying the request ID. Server errors (5xx) never echo handler text that
// may include internal paths or helper output; that detail is logged
// against the request ID instead.
func normalizeAPIErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		// Multipart upload handlers stream request-controlled bytes through
		// their own admission writer. Do not route those responses through the
		// generic passthrough writer, which would make static analysis treat the
		// request body as potentially reflected into an HTTP response.
		if r.URL.Path == "/api/cpmove/inspect" || r.URL.Path == "/api/wpress/restore" {
			next.ServeHTTP(w, r)
			return
		}
		captured := &apiErrorWriter{w: w}
		next.ServeHTTP(captured, r)
		if !captured.capture {
			return
		}
		requestID, _ := r.Context().Value(requestIDContextKey{}).(string)
		message := strings.TrimSpace(captured.body.String())
		if captured.status >= 500 {
			log.Printf(`{"level":"error","request_id":%q,"path":%q,"status":%d,"error":%q}`, requestID, r.URL.Path, captured.status, message)
			message = safeServerErrorMessage(captured.status)
		}
		errorCode, nextAction, retryable := apiErrorMetadata(captured.status, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(captured.status)
		if r.Method == http.MethodHead {
			return
		}
		_ = json.NewEncoder(w).Encode(apiError{Error: message, Code: captured.status, ErrorCode: errorCode, RequestID: requestID, Resource: r.URL.Path, Retryable: retryable, NextAction: nextAction})
	})
}

// writeAPIError writes the JSON error envelope directly. Use it for a server
// error whose message is deliberately client-safe and actionable; the
// envelope is not rewritten because it is not plain text.
func writeAPIError(w http.ResponseWriter, r *http.Request, status int, message string) {
	requestID, _ := r.Context().Value(requestIDContextKey{}).(string)
	errorCode, nextAction, retryable := apiErrorMetadata(status, r.URL.Path)
	writeJSON(w, status, apiError{Error: message, Code: status, ErrorCode: errorCode, RequestID: requestID, Resource: r.URL.Path, Retryable: retryable, NextAction: nextAction})
}

// safeServerErrorMessage returns client-safe text for a server error status.
func safeServerErrorMessage(status int) string {
	switch status {
	case http.StatusServiceUnavailable:
		return "the service is temporarily unavailable; retry later or check readiness"
	case http.StatusBadGateway, http.StatusGatewayTimeout:
		return "an upstream service or host helper failed; see the server log for this request ID"
	case http.StatusInsufficientStorage:
		return "insufficient storage to complete the request"
	case http.StatusNotImplemented:
		return "this operation is not available on this installation"
	default:
		return "internal error; see the server log for this request ID"
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}
func safeUser(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 32 || value == "" {
		return ""
	}
	for _, r := range value {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return value
}

// Backward compatibility wrappers for audit package
type AuditEvent = audit.Event

// Test compatibility - auditKeyPath is used by tests to mock the key file location
// When set, it calls through to the audit package's test helper
var auditKeyPath string = "/etc/stepanel-audit.key"

// auditMu and auditPersistenceErr are used by tests to interact with audit state
// For real usage, the audit package manages these internally
var auditMu sync.Mutex
var auditPersistenceErr error

// resetAuditPersistenceErr is called by tests to clear persistence error state
func resetAuditPersistenceErr() {
	auditMu.Lock()
	defer auditMu.Unlock()
	auditPersistenceErr = nil
	audit.TestResetPersistenceError()
}

func validateAuditEvent(event AuditEvent) error {
	if event.Sequence == 0 || strings.TrimSpace(event.Actor) == "" || strings.TrimSpace(event.Action) == "" {
		return errors.New("audit log contains an event with invalid identity or sequence")
	}
	if _, err := time.Parse(time.RFC3339Nano, event.Time); err != nil {
		return errors.New("audit log contains an invalid event timestamp")
	}
	return nil
}

func VerifyAuditLog(path string) error {
	// Create a temporary logger for the specific path to verify it
	logger := audit.New(path)
	return logger.Verify(path)
}

func AuditPersistenceError() error {
	return audit.PersistenceError()
}
