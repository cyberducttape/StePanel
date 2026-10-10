package rootbroker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	stepanelhelper "github.com/cyberducttape/StePanel/internal/helper"
	"github.com/cyberducttape/StePanel/internal/operations"
	"github.com/cyberducttape/StePanel/internal/siteidentity"
	"golang.org/x/crypto/ssh"
)

// ErrNotImplemented is returned when a broker operation is not yet implemented
var ErrNotImplemented = errors.New("operation not yet implemented in broker")

const maxBrokerDBDumpBytes = 64 << 20

// Every broker subprocess runs through stepanelhelper.RunCapped so output is
// bounded while the child runs and overruns kill the whole process group;
// never buffer first and check the length afterwards.
const (
	maxBrokerCommandOutput = 8 << 20
	maxBrokerCommandStderr = 64 << 10
)

// Broker is the root-privileged operations handler.
// All operations are strongly-typed and validated before execution.
type Broker struct {
	webRoot       string
	recoveryRoot  string
	dbctlPath     string
	certbotPath   string
	systemctlPath string
	appctlPath    string
	vhostctlPath  string
	proxyctlPath  string
	runnerctlPath string
	gitctlPath    string
	gitKeyRoot    string
	validator     *Validator
	logger        *log.Logger
	host          hostOps
	fencingDB     *sql.DB
	// leaseWatchInterval and leaseWatchMargin drive the fencing watchdog
	// for mutating requests; see watchFencing.
	leaseWatchInterval  time.Duration
	leaseWatchMargin    time.Duration
	fencingDBErrorGrace time.Duration
	// accountMutationMu serializes operations that can modify the host's
	// account database (/etc/passwd, /etc/group, and related locks). The
	// panel and worker use separate broker clients, so client-local locking
	// cannot prevent useradd/userdel races at the broker boundary.
	accountMutationMu sync.Mutex
}

// hostOps performs the privileged host account and ownership mutations
// behind site lifecycle operations. Production always uses execHostOps; tests
// inject a fake through newBroker. Whether these mutations run must never be
// inferred from where the web root lives on disk.
type hostOps interface {
	EnsureSystemUser(ctx context.Context, username, home string) error
	ValidateSystemUser(ctx context.Context, username, home string) error
	DeleteSystemUser(ctx context.Context, username string) error
	Chown(ctx context.Context, path, owner, group string, recursive bool) error
	WebGroup() (string, error)
	// RunSiteHelper runs the installed stepanel-sitectl helper, the single
	// implementation of site isolation (account, ownership, ACLs, PHP pool).
	RunSiteHelper(ctx context.Context, args ...string) (string, error)
	RunSiteHelperInput(ctx context.Context, input []byte, args ...string) (string, error)
}

// NewBroker creates a new root broker with default recovery root.
func NewBroker(webRoot string, logger *log.Logger) (*Broker, error) {
	if webRoot == "" {
		return nil, fmt.Errorf("web root is required")
	}
	if logger == nil {
		logger = log.New(os.Stderr, "[rootbroker] ", log.LstdFlags)
	}
	// Use /var/lib/stepanel/recovery as default recovery root
	recoveryRoot := "/var/lib/stepanel/recovery"
	return NewBrokerWithRecoveryRoot(webRoot, recoveryRoot, logger)
}

// NewBrokerWithRecoveryRoot creates a new root broker with a custom recovery
// root. The recovery root must be creatable: the broker fails closed rather
// than running privileged mutations without a durable journal.
func NewBrokerWithRecoveryRoot(webRoot, recoveryRoot string, logger *log.Logger) (*Broker, error) {
	return newBroker(webRoot, recoveryRoot, logger, execHostOps{})
}

// NewBrokerWithFencingDB creates a broker that verifies any fencing tokens
// supplied by the unprivileged panel against the shared control-plane DB.
// A nil database preserves the standalone/test broker behavior.
func NewBrokerWithFencingDB(webRoot, recoveryRoot string, fencingDB *sql.DB, logger *log.Logger) (*Broker, error) {
	return newBrokerWithFencingDB(webRoot, recoveryRoot, logger, execHostOps{}, fencingDB)
}

func newBroker(webRoot, recoveryRoot string, logger *log.Logger, host hostOps) (*Broker, error) {
	return newBrokerWithFencingDB(webRoot, recoveryRoot, logger, host, nil)
}

func newBrokerWithFencingDB(webRoot, recoveryRoot string, logger *log.Logger, host hostOps, fencingDB *sql.DB) (*Broker, error) {
	if webRoot == "" {
		return nil, fmt.Errorf("web root is required")
	}
	if logger == nil {
		logger = log.New(os.Stderr, "[rootbroker] ", log.LstdFlags)
	}
	if host == nil {
		return nil, fmt.Errorf("host operations are required")
	}
	if err := os.MkdirAll(recoveryRoot, 0700); err != nil {
		return nil, fmt.Errorf("create durable recovery root %q: %w", recoveryRoot, err)
	}
	return &Broker{
		webRoot:       webRoot,
		recoveryRoot:  recoveryRoot,
		dbctlPath:     "/usr/local/sbin/stepanel-dbctl",
		certbotPath:   "/usr/local/sbin/stepanel-certbot",
		systemctlPath: "/usr/bin/systemctl",
		appctlPath:    "/usr/local/sbin/stepanel-appctl",
		vhostctlPath:  "/usr/local/sbin/stepanel-vhostctl",
		proxyctlPath:  "/usr/local/sbin/stepanel-proxyctl",
		runnerctlPath: "/usr/local/sbin/stepanel-runnerctl",
		gitctlPath:    "/usr/local/sbin/stepanel-gitctl",
		gitKeyRoot:    "/etc/stepanel/git-keys",
		validator:     NewValidator(webRoot),
		logger:        logger,
		host:          host,
		fencingDB:     fencingDB,

		leaseWatchInterval:  LeaseWatchInterval,
		leaseWatchMargin:    LeaseWatchMargin,
		fencingDBErrorGrace: FencingDBErrorGrace,
	}, nil
}

// LeaseWatchInterval and LeaseWatchMargin bound how long a privileged
// operation can outlive the lease that admitted it. The watchdog re-checks
// the request's fencing tokens every interval and cancels the operation once
// a lease has less than margin left. Callers renew a lease every third of its
// duration, so a live owner always has about two thirds of the lease left:
// the panel's lease duration must stay well above 3/2*(margin+interval).
const (
	LeaseWatchInterval = 5 * time.Second
	LeaseWatchMargin   = 20 * time.Second
	// FencingDBErrorGrace is the maximum bounded interval during which a
	// transient control-plane outage may interrupt a running mutation. The
	// broker fails closed after this interval because it cannot prove ownership.
	FencingDBErrorGrace = 15 * time.Second
)

// Execute handles an RPC request and returns the response.
func (b *Broker) Execute(ctx context.Context, req *Request) (*Response, error) {
	// Validate all inputs before any operations
	if err := b.validator.ValidateRequest(req); err != nil {
		b.logger.Printf("validation error: %v", err)
		return &Response{
			OK:    false,
			Error: fmt.Sprintf("validation error: %v", err),
		}, nil
	}
	if b.fencingDB != nil && requestRequiresFencing(req) {
		if len(req.Fencing) == 0 {
			return &Response{OK: false, Error: "fencing token required for mutating request"}, nil
		}
		for _, token := range req.Fencing {
			if err := operations.VerifyFencingToken(b.fencingDB, token); err != nil {
				return &Response{OK: false, Error: fmt.Sprintf("fencing token rejected: %v", err)}, nil
			}
		}
		watchCtx, stop := b.watchFencing(ctx, req.Fencing)
		resp, err := b.dispatch(watchCtx, req)
		if lost := stop(); lost != nil {
			return &Response{OK: false, Error: fmt.Sprintf("operation cancelled because its fencing lease was lost: %v", lost)}, nil
		}
		return resp, err
	}
	return b.dispatch(ctx, req)
}

// watchFencing cancels a privileged operation whose admitting lease is lost
// while it runs. The broker runs operations detached from the caller's
// connection, so without this a crashed or partitioned owner's helper could
// keep mutating after another owner acquired the resource. Helpers are
// killed exactly as on a timeout, which their recovery journals already
// handle. stop ends the watch and reports the lost lease, if any.
func (b *Broker) watchFencing(parent context.Context, tokens []operations.FencingToken) (context.Context, func() error) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	finished := make(chan struct{})
	var lost error
	errorSince := make(map[string]time.Time)
	go func() {
		defer close(finished)
		ticker := time.NewTicker(b.leaseWatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				for _, token := range tokens {
					err := operations.VerifyFencingTokenWithin(b.fencingDB, token, b.leaseWatchMargin)
					key := fmt.Sprintf("%s/%s/%d", token.ResourceKey, token.OwnerID, token.Generation)
					if errors.Is(err, operations.ErrLeaseLost) {
						lost = fmt.Errorf("lease %s generation %d", token.ResourceKey, token.Generation)
						b.logger.Printf("fencing lease lost during execution; cancelling: %v", lost)
						cancel()
						return
					}
					if err != nil {
						started, ok := errorSince[key]
						if !ok {
							started = time.Now()
							errorSince[key] = started
						}
						if time.Since(started) >= b.fencingDBErrorGrace {
							lost = fmt.Errorf("fencing database unavailable for %s", b.fencingDBErrorGrace)
							b.logger.Printf("fencing watchdog cannot verify lease; cancelling: %v: %v", lost, err)
							cancel()
							return
						}
						b.logger.Printf("fencing watchdog check failed; grace period remains: %v", err)
						continue
					}
					delete(errorSince, key)
				}
			}
		}
	}()
	return ctx, func() error {
		close(done)
		<-finished
		cancel()
		return lost
	}
}

// dispatch routes a validated, admitted request to its handler.
func (b *Broker) dispatch(ctx context.Context, req *Request) (*Response, error) {
	switch req.RequestType {
	case "health":
		return &Response{OK: true}, nil
	case "site":
		return b.handleSiteRequest(ctx, req.Site)
	case "app":
		return b.handleAppRequest(ctx, req.App)
	case "worker":
		return b.handleWorkerRequest(ctx, req.Worker)
	case "runner":
		return b.handleRunnerRequest(ctx, req.Runner)
	case "db":
		return b.handleDBRequest(ctx, req.DB)
	case "vhost":
		return b.handleVhostRequest(ctx, req.Vhost)
	case "proxy":
		return b.handleProxyRequest(ctx, req.Proxy)
	case "git":
		return b.handleGitRequest(ctx, req.Git)
	case "certificate":
		return b.handleCertificateRequest(ctx, req.Certificate)
	case "task":
		return b.handleTaskRequest(ctx, req.Task)
	case "environment":
		return b.handleEnvironmentRequest(ctx, req.Environment)
	case "resource":
		return b.handleResourceRequest(ctx, req.Resource)
	case "wordpress":
		return b.handleWordPressRequest(ctx, req.WordPress)
	default:
		return &Response{
			OK:    false,
			Error: fmt.Sprintf("unknown request type: %s", req.RequestType),
		}, nil
	}
}

// dbReadOnlyActions read database state without mutating it and need no
// fencing token: there is no lease for an administrator viewing diagnostics.
var dbReadOnlyActions = map[string]bool{"inventory": true, "dump": true, "diagnostics": true, "sessions": true, "settings": true}

func requestRequiresFencing(req *Request) bool {
	if req == nil {
		return false
	}
	switch req.RequestType {
	case "health":
		return false
	case "db":
		return req.DB != nil && !dbReadOnlyActions[req.DB.Action]
	case "git":
		return req.Git != nil && req.Git.Action != "verify-key"
	case "task":
		return req.Task != nil && req.Task.Action != "history"
	case "resource":
		return req.Resource != nil && req.Resource.Action != "status"
	case "wordpress":
		return req.WordPress != nil && !wordPressReadOnlyActions[req.WordPress.Action]
	default:
		return true
	}
}

func (b *Broker) handleResourceRequest(ctx context.Context, req *ResourceRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "resource request is nil"}, nil
	}
	var args []string
	switch req.Action {
	case "apply-account":
		args = []string{"account-resource-apply", req.Account, strconv.Itoa(req.CPUPercent), strconv.Itoa(req.CPUWeight), strconv.Itoa(req.MemoryHighMB), strconv.Itoa(req.MemoryMB), strconv.Itoa(req.IOWeight), strconv.Itoa(req.TasksMax)}
	case "apply-site":
		siteUser, err := b.resolveSiteUser(&SiteRequest{Site: req.Site}, false)
		if err != nil {
			return &Response{OK: false, Error: err.Error()}, nil
		}
		args = []string{"resource-apply", req.Site, siteUser, strconv.Itoa(req.CPUPercent), strconv.Itoa(req.CPUWeight), strconv.Itoa(req.MemoryHighMB), strconv.Itoa(req.MemoryMB), strconv.Itoa(req.IOWeight), strconv.Itoa(req.TasksMax), req.Account}
	case "status":
		siteUser, err := b.resolveSiteUser(&SiteRequest{Site: req.Site}, false)
		if err != nil {
			return &Response{OK: false, Error: err.Error()}, nil
		}
		args = []string{"resource-status", req.Site, siteUser}
	default:
		return &Response{OK: false, Error: "unsupported resource action"}, nil
	}
	cmd := stepanelhelper.NewCommand(ctx, b.appctlPath, args...)
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("resource operation failed: %v: %s", err, strings.TrimSpace(string(output)))}, nil
	}
	details, err := json.Marshal(ResourceResponse{Output: string(output)})
	if err != nil {
		return nil, err
	}
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) handleEnvironmentRequest(ctx context.Context, req *EnvironmentRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "environment request is nil"}, nil
	}
	siteUser, err := b.resolveSiteUser(&SiteRequest{Site: req.Site}, false)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	cmd := stepanelhelper.NewCommand(ctx, b.appctlPath, "env-apply", req.Site, siteUser)
	cmd.Stdin = strings.NewReader(req.Content)
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("environment update failed: %v: %s", err, strings.TrimSpace(string(output)))}, nil
	}
	return &Response{OK: true}, nil
}

func (b *Broker) handleRunnerRequest(ctx context.Context, req *RunnerRequest) (*Response, error) {
	if req == nil || req.Action != "build" {
		return &Response{OK: false, Error: "runner request must specify build"}, nil
	}
	siteUser, err := b.resolveSiteUser(&SiteRequest{Site: req.Site, SiteUser: req.SiteUser}, false)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	args := []string{"build", req.Site, siteUser, req.Image, req.Root, req.Script, strconv.Itoa(req.CPUPercent), strconv.Itoa(req.MemoryMB), strconv.Itoa(req.TasksMax), req.NetworkMode, strconv.FormatInt(req.MaxImageBytes, 10)}
	cmd := stepanelhelper.NewCommand(ctx, b.runnerctlPath, args...)
	output, err := stepanelhelper.RunCappedWithCleanup(ctx, cmd, maxBrokerCommandOutput, func() {
		if cmd.Process != nil {
			stopRunnerTransientUnit(b.systemctlPath, cmd.Process.Pid)
		}
	})
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("runner build failed: %v: %s", err, strings.TrimSpace(string(output)))}, nil
	}
	return &Response{OK: true}, nil
}

func (b *Broker) handleWorkerRequest(ctx context.Context, req *WorkerRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "worker request is nil"}, nil
	}
	siteUser, err := b.resolveSiteUser(&SiteRequest{Site: req.Site}, false)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	args := []string{"worker-" + req.Action, req.Site, siteUser, req.Name}
	switch req.Action {
	case "apply":
		args = []string{"worker-apply", req.Site, siteUser, req.Name, req.Type, req.Root,
			strconv.Itoa(req.Processes), strconv.Itoa(req.MemoryMB), strconv.Itoa(req.Retries)}
	case "delete", "start", "stop", "restart":
	default:
		return &Response{OK: false, Error: "unknown worker action"}, nil
	}
	output, err := b.runAppCtl(ctx, args)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("worker %s failed: %v: %s", req.Action, err, strings.TrimSpace(string(output)))}, nil
	}
	result := WorkerResponse{Output: strings.TrimSpace(string(output))}
	switch req.Action {
	case "apply":
		result.Applied = true
	case "delete":
		result.Deleted = true
	case "start":
		result.Started = true
	case "stop":
		result.Stopped = true
	case "restart":
		result.Restarted = true
	}
	details, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) runAppCtl(ctx context.Context, args []string) ([]byte, error) {
	cmd := stepanelhelper.NewCommand(ctx, b.appctlPath, args...)
	return stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
}

func (b *Broker) handleTaskRequest(ctx context.Context, req *TaskRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "invalid task operation"}, nil
	}
	var args []string
	siteUser, err := b.resolveSiteUser(&SiteRequest{Site: req.Site}, false)
	if err != nil && req.Action != "kill" {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	switch req.Action {
	case "kill":
		unit := "stepanel-task-" + req.Site + "-" + req.Name + ".service"
		args = []string{"kill", "--kill-who=all", "--signal=SIGTERM", unit}
	case "delete", "history":
		args = []string{req.Action, req.Site, siteUser, req.Name}
	case "apply":
		enabled := "0"
		if req.Enabled {
			enabled = "1"
		}
		args = []string{"task-apply", req.Site, siteUser, req.Name, req.Runtime, req.OnCalendar, enabled,
			strconv.Itoa(req.TimeoutSec), base64.StdEncoding.EncodeToString([]byte(req.Command)),
			strconv.Itoa(req.MinIntervalSeconds), req.MissedRunPolicy, strconv.Itoa(req.CPUPercent),
			strconv.Itoa(req.MemoryMB), strconv.Itoa(req.TasksMax), req.NotifyWebhook}
	default:
		return &Response{OK: false, Error: "invalid task operation"}, nil
	}
	path := b.appctlPath
	if req.Action == "kill" {
		path = b.systemctlPath
	}
	cmd := stepanelhelper.NewCommand(ctx, path, args...)
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("task %s failed: %v: %s", req.Action, err, strings.TrimSpace(string(output)))}, nil
	}
	result := TaskResponse{Output: string(output)}
	switch req.Action {
	case "kill":
		result.Killed = true
	case "delete":
		result.Deleted = true
	case "apply":
		result.Applied = true
	}
	details, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) handleCertificateRequest(ctx context.Context, req *CertificateRequest) (*Response, error) {
	cmd := stepanelhelper.NewCommand(ctx, b.certbotPath, req.Domain, req.Email)
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("certificate issuance failed: %v: %s", err, strings.TrimSpace(string(output)))}, nil
	}
	details, err := json.Marshal(CertificateResponse{Issued: true, Domain: req.Domain, Output: strings.TrimSpace(string(output))})
	if err != nil {
		return nil, err
	}
	return &Response{OK: true, Details: details}, nil
}

func stopRunnerTransientUnit(systemctl string, helperPID int) {
	if helperPID <= 0 {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(cleanupCtx, systemctl, "--no-block", "stop", fmt.Sprintf("stepanel-runner-%d.service", helperPID)).Run()
}

// --- Site Operations ---

func (b *Broker) handleSiteRequest(ctx context.Context, req *SiteRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "site request is nil"}, nil
	}

	b.logger.Printf("site: action=%s site=%s", req.Action, req.Site)

	switch req.Action {
	case "create":
		return b.siteCreate(ctx, req)
	case "delete":
		return b.siteDelete(ctx, req)
	case "seal":
		return b.siteSeal(ctx, req)
	case "prepare":
		return b.sitePrepare(ctx, req)
	case "access":
		return b.siteAccess(ctx, req)
	case "ftp":
		return b.siteFTP(ctx, req)
	case "resources":
		return b.siteResources(ctx, req)
	case "quota":
		return b.siteQuota(ctx, req)
	case "quota-clear":
		return b.siteQuotaClear(ctx, req)
	case "runtime":
		return b.siteRuntime(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown site action: %s", req.Action)}, nil
	}
}

func (b *Broker) siteCreate(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.accountMutationMu.Lock()
	defer b.accountMutationMu.Unlock()

	siteRoot, err := b.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}

	// Allocate or retrieve the immutable identity before touching the host.
	siteUser, err := b.resolveSiteUser(req, true)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}

	// Use site name as job ID for journaling. In real usage, this comes from
	// the durable job system. Here we use the site name for simplicity.
	jobID := "site-create-" + req.Site
	actor := "root"

	b.logger.Printf("creating site: user=%s root=%s jobID=%s", siteUser, siteRoot, jobID)

	// Load or create durable journal for this site creation
	journal, err := loadOrCreateCreationJournal(b.recoveryRoot, jobID, req.Site, actor)
	if err != nil {
		b.logger.Printf("failed to load creation journal: %v", err)
		return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
	}

	// Step 1: Initialize (create system user, base directories)
	if !journal.isComplete(stepInitialized) {
		if err := b.host.EnsureSystemUser(ctx, siteUser, siteRoot); err != nil {
			b.logger.Printf("failed at init: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("user creation failed: %v", err)}, nil
		}

		// Create base directory structure
		subdirs := []string{
			filepath.Join(siteRoot, "public"),
			filepath.Join(siteRoot, ".config"),
			filepath.Join(siteRoot, ".cache"),
		}
		for _, dir := range subdirs {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				b.logger.Printf("failed to create directory %s: %v", dir, err)
				return &Response{OK: false, Error: fmt.Sprintf("directory creation failed: %v", err)}, nil
			}
		}

		// Mark step complete in journal
		if err := journal.markComplete(stepInitialized); err != nil {
			b.logger.Printf("failed to journal init: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
		}
	} else {
		b.logger.Printf("skipping init (already complete)")
	}

	// Step 2: Persist metadata
	if !journal.isComplete(stepPersisted) {
		// Create metadata file with site configuration
		metadataPath := filepath.Join(siteRoot, ".metadata")
		metadata := map[string]interface{}{
			"site":       req.Site,
			"user":       siteUser,
			"created_at": time.Now().UTC(),
		}
		metadataJSON, _ := json.Marshal(metadata)
		if err := writeAtomicBroker(metadataPath, metadataJSON, 0600); err != nil {
			b.logger.Printf("failed to persist metadata: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("metadata persistence failed: %v", err)}, nil
		}

		// Mark step complete in journal
		if err := journal.markComplete(stepPersisted); err != nil {
			b.logger.Printf("failed to journal persist: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
		}
	} else {
		b.logger.Printf("skipping persist (already complete)")
	}

	// Step 3: Set proper ownership (this is actually part of initialization,
	// but we journal it separately for fine-grained recovery tracking)
	if !journal.isComplete(stepCompleted) {
		webGroup, err := b.host.WebGroup()
		if err != nil {
			b.logger.Printf("failed to resolve web group: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("ownership change failed: %v", err)}, nil
		}
		if err := b.host.Chown(ctx, siteRoot, siteUser, webGroup, true); err != nil {
			b.logger.Printf("failed to set ownership: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("ownership change failed: %v", err)}, nil
		}

		// Mark completion in journal
		if err := journal.markComplete(stepCompleted); err != nil {
			b.logger.Printf("failed to journal completion: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
		}
	} else {
		b.logger.Printf("skipping completion (already complete)")
	}

	// All steps complete: clean up journal
	if err := journal.cleanup(); err != nil {
		b.logger.Printf("warning: failed to cleanup journal: %v", err)
		// Don't fail the operation if journal cleanup fails - the operation
		// already succeeded and was properly journaled
	}

	resp := SiteResponse{
		Username: siteUser,
		Created:  true,
	}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) siteDelete(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.accountMutationMu.Lock()
	defer b.accountMutationMu.Unlock()

	if os.Getenv("STEPANEL_LAB_ROOT_BROKER_HELPERS") == "1" {
		siteUser, resolveErr := b.resolveSiteUser(req, false)
		if resolveErr != nil {
			return &Response{OK: false, Error: resolveErr.Error()}, nil
		}
		resp, err := b.runLabHelper(ctx, "/usr/local/sbin/stepanel-sitectl", "delete", req.Site, siteUser)
		if err != nil || resp == nil || !resp.OK {
			return resp, err
		}
		// The lab helper only removes the tree when its derived site user
		// exists. Imported recovery sites can have no such user, so finish
		// the validated site-root cleanup here while still keeping the
		// executable and arguments fixed to the lab-only path.
		siteRoot, validateErr := b.validator.ValidateSiteRoot(req.Site)
		if validateErr != nil {
			return &Response{OK: false, Error: validateErr.Error()}, nil
		}
		if removeErr := os.RemoveAll(siteRoot); removeErr != nil && !os.IsNotExist(removeErr) {
			return &Response{OK: false, Error: fmt.Sprintf("lab site cleanup failed: %v", removeErr)}, nil
		}
		return resp, nil
	}
	siteRoot, err := b.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}

	siteUser, err := b.resolveSiteUser(req, false)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	if err := b.host.ValidateSystemUser(ctx, siteUser, siteRoot); err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("refusing to delete site with mismatched system account: %v", err)}, nil
	}

	b.logger.Printf("deleting site: user=%s root=%s", siteUser, siteRoot)

	// Remove the site tree first. If this fails, keep the system user in place
	// so a retry can safely complete the same deletion.
	if err := os.RemoveAll(siteRoot); err != nil && !os.IsNotExist(err) {
		b.logger.Printf("failed to delete directory: %v", err)
		return &Response{OK: false, Error: fmt.Sprintf("directory deletion failed: %v", err)}, nil
	}

	// Account cleanup is part of the mutation contract. Do not report success
	// when the site tree is gone but its privileged system user remains.
	if err := b.host.DeleteSystemUser(ctx, siteUser); err != nil {
		b.logger.Printf("failed to delete system user: %v", err)
		return &Response{OK: false, Error: fmt.Sprintf("system user deletion failed: %v", err)}, nil
	}

	resp := SiteResponse{Deleted: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) siteSeal(ctx context.Context, req *SiteRequest) (*Response, error) {
	siteRoot, err := b.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}

	b.logger.Printf("sealing site: %s", req.Site)

	// Set restrictive permissions on config files.
	sitePublic := filepath.Join(siteRoot, "public")
	if err := secureSealTree(ctx, sitePublic); err != nil {
		b.logger.Printf("failed to seal permissions: %v", err)
		return &Response{OK: false, Error: fmt.Sprintf("permission seal failed: %v", err)}, nil
	}

	resp := SiteResponse{Sealed: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) sitePrepare(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.accountMutationMu.Lock()
	defer b.accountMutationMu.Unlock()

	siteRoot, err := b.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}

	siteUser, err := b.resolveSiteUser(req, true)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	b.logger.Printf("preparing site: %s user=%s", req.Site, siteUser)
	// stepanel-sitectl prepare-root is the single implementation of the
	// site isolation contract: system account, site-root ownership and mode,
	// the ACL that lets the panel publish into the root, the PHP state tree,
	// and the PHP-FPM pool. It deliberately leaves the public tree to the
	// site manager's staged activation.
	output, err := b.host.RunSiteHelper(ctx, "prepare-root", req.Site, siteUser)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("site isolation preparation failed: %v", err)}, nil
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		return &Response{OK: false, Error: "site helper returned no identity"}, nil
	}
	if reported, want := strings.TrimSpace(lines[len(lines)-1]), siteUser; reported != want {
		return &Response{OK: false, Error: fmt.Sprintf("site helper prepared account %q but the broker derives %q; refusing to continue with divergent site identity", reported, want)}, nil
	}
	if os.Getenv("STEPANEL_LAB_ROOT_BROKER_HELPERS") == "1" {
		if err := ensureLabManagerSiteRoot(siteRoot); err != nil {
			return &Response{OK: false, Error: fmt.Sprintf("site root preparation failed: %v", err)}, nil
		}
	}

	return &Response{OK: true}, nil
}

func ensureLabManagerSiteRoot(siteRoot string) error {
	if err := os.MkdirAll(siteRoot, 0o750); err != nil {
		return err
	}
	appUser, err := user.Lookup("stepanel")
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(appUser.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(appUser.Gid)
	if err != nil {
		return err
	}
	if err := os.Chown(siteRoot, uid, gid); err != nil {
		return err
	}
	if err := os.Chmod(siteRoot, 0o750); err != nil {
		return err
	}
	managerStageRoot := filepath.Join(filepath.Dir(siteRoot), ".stepanel-manager-staging")
	if info, err := os.Stat(managerStageRoot); err == nil && info.IsDir() {
		if err := os.Chown(managerStageRoot, uid, gid); err != nil {
			return err
		}
		if err := os.Chmod(managerStageRoot, 0o750); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (b *Broker) siteAccess(ctx context.Context, req *SiteRequest) (*Response, error) {
	// stepanel-sitectl ensures the site account exists (useradd/usermod)
	// before every action, so it shares the account mutation lock.
	b.accountMutationMu.Lock()
	defer b.accountMutationMu.Unlock()
	if req.SFTPEnabled == nil || req.ShellEnabled == nil {
		return &Response{OK: false, Error: "SSH access flags are required"}, nil
	}
	siteUser, err := b.resolveSiteUser(req, false)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	output, err := b.host.RunSiteHelperInput(ctx, []byte(req.SSHKeys), "access", req.Site, siteUser, boolArg(*req.SFTPEnabled), boolArg(*req.ShellEnabled))
	return siteHelperResponse("site access", output, err)
}

func (b *Broker) siteFTP(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.accountMutationMu.Lock()
	defer b.accountMutationMu.Unlock()
	if req.FTPEnabled == nil {
		return &Response{OK: false, Error: "FTPS flag is required"}, nil
	}
	siteUser, err := b.resolveSiteUser(req, false)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	output, err := b.host.RunSiteHelperInput(ctx, []byte(req.FTPPassword), "ftp", req.Site, siteUser, boolArg(*req.FTPEnabled))
	return siteHelperResponse("site FTPS", output, err)
}

func (b *Broker) siteResources(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.accountMutationMu.Lock()
	defer b.accountMutationMu.Unlock()
	siteUser, err := b.resolveSiteUser(req, false)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	output, err := b.host.RunSiteHelper(ctx, "resources", req.Site, siteUser, strconv.Itoa(req.PHPWorkers))
	return siteHelperResponse("site resources", output, err)
}

func (b *Broker) siteQuota(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.accountMutationMu.Lock()
	defer b.accountMutationMu.Unlock()
	siteUser, err := b.resolveSiteUser(req, false)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	output, err := b.host.RunSiteHelper(ctx, "quota", req.Site, siteUser, strconv.Itoa(req.DiskMB), strconv.Itoa(req.Inodes))
	return siteHelperResponse("site quota", output, err)
}

func (b *Broker) siteQuotaClear(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.accountMutationMu.Lock()
	defer b.accountMutationMu.Unlock()
	siteUser, err := b.resolveSiteUser(req, false)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	output, err := b.host.RunSiteHelper(ctx, "quota-clear", req.Site, siteUser)
	return siteHelperResponse("site quota clear", output, err)
}

func (b *Broker) siteRuntime(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.accountMutationMu.Lock()
	defer b.accountMutationMu.Unlock()
	siteUser, err := b.resolveSiteUser(req, false)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	output, err := b.host.RunSiteHelper(ctx, "runtime", req.Site, siteUser, req.PHPVersion,
		req.MemoryLimit, strconv.Itoa(req.ExecTimeout), req.UploadMaxFilesize,
		req.PostMaxSize, strconv.Itoa(req.MaxInputVars), boolArg(req.OPcache),
		boolArg(req.DisplayErrors), req.ErrorReporting)
	return siteHelperResponse("site runtime", output, err)
}

// resolveSiteUser is the broker-side identity authority. Production brokers
// always use the root-owned control-plane mapping; they never derive an
// account name from customer-controlled site text. The legacy fallback exists
// only for standalone unit-test brokers that intentionally have no database.
func (b *Broker) resolveSiteUser(req *SiteRequest, allocate bool) (string, error) {
	if req == nil {
		return "", errors.New("site request is nil")
	}
	if b.fencingDB == nil {
		if req.SiteUser != "" {
			return req.SiteUser, nil
		}
		return siteidentity.UnixUser(req.Site), nil
	}
	var username string
	err := b.fencingDB.QueryRow(`SELECT username FROM site_identities WHERE site = ?`, req.Site).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) && allocate {
		username, err = siteidentity.ResolveOrAllocate(b.fencingDB, req.Site)
	}
	if err != nil {
		return "", fmt.Errorf("resolve persisted Unix account for site %q: %w", req.Site, err)
	}
	if req.SiteUser != "" && req.SiteUser != username {
		return "", fmt.Errorf("site identity mismatch for %q: request %q, persisted %q", req.Site, req.SiteUser, username)
	}
	return username, nil
}

func boolArg(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func siteHelperResponse(operation, output string, err error) (*Response, error) {
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("%s failed: %v: %s", operation, err, strings.TrimSpace(output))}, nil
	}
	return &Response{OK: true}, nil
}

func unsupportedBrokerResponse(operation string) (*Response, error) {
	return &Response{OK: false, Error: operation + " is not implemented by the root broker"}, nil
}

// --- App Operations ---

func (b *Broker) handleAppRequest(ctx context.Context, req *AppRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "app request is nil"}, nil
	}

	b.logger.Printf("app: action=%s site=%s port=%d", req.Action, req.Site, req.Port)

	switch req.Action {
	case "apply", "delete", "start", "stop", "restart":
		return b.runAppHelper(ctx, req)
	case "composer-install", "node-tool", "python-apply", "python-start", "python-stop", "python-restart":
		return b.runAppTooling(ctx, req)
	case "rollback":
		// Rollback is orchestrated by the panel as an apply of the previous manifest.
		return unsupportedBrokerResponse("app rollback")
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown app action: %s", req.Action)}, nil
	}
}

func (b *Broker) runAppTooling(ctx context.Context, req *AppRequest) (*Response, error) {
	siteUser, err := b.resolveSiteUser(&SiteRequest{Site: req.Site, SiteUser: req.SiteUser}, false)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	var args []string
	switch req.Action {
	case "composer-install":
		args = []string{"composer-install", req.Site, siteUser, req.Root, boolArg(req.Development), boolArg(req.OptimizeAutoloader)}
	case "node-tool":
		args = []string{"node-tool", req.Site, siteUser, req.ToolAction, req.PackageManager, req.Root}
	case "python-apply":
		args = []string{"python-apply", req.Site, siteUser, req.Version, req.Root, req.EntryPoint, strconv.Itoa(req.Port), strconv.Itoa(req.Workers)}
	case "python-start", "python-stop", "python-restart":
		args = []string{req.Action, req.Site, siteUser}
	default:
		return &Response{OK: false, Error: "unknown application tooling action"}, nil
	}
	output, err := b.runAppCtl(ctx, args)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("application operation %s failed: %v: %s", req.Action, err, strings.TrimSpace(string(output)))}, nil
	}
	return &Response{OK: true}, nil
}

func (b *Broker) runAppHelper(ctx context.Context, req *AppRequest) (*Response, error) {
	siteUser, err := b.resolveSiteUser(&SiteRequest{Site: req.Site, SiteUser: req.SiteUser}, false)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	args := []string{req.Action, req.Site, siteUser}
	if req.Action == "apply" {
		// The helper only accepts bare X.Y.Z versions; the validator permits a leading "v".
		args = append(args, strings.TrimPrefix(req.Version, "v"), filepath.Clean(req.Root), strconv.Itoa(req.Port))
	}
	cmd := stepanelhelper.NewCommand(ctx, b.appctlPath, args...)
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("application %s failed: %v: %s", req.Action, err, strings.TrimSpace(string(output)))}, nil
	}
	result := AppResponse{Output: string(output), Port: req.Port}
	switch req.Action {
	case "apply":
		result.Applied = true
	case "delete":
		result.Deleted = true
	case "start":
		result.Started = true
	case "stop":
		result.Stopped = true
	case "restart":
		result.Restarted = true
	}
	details, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Response{OK: true, Details: details}, nil
}

// --- Database Operations ---

func (b *Broker) handleDBRequest(ctx context.Context, req *DBRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "db request is nil"}, nil
	}

	b.logger.Printf("db: action=%s database=%s", req.Action, req.Database)

	switch req.Action {
	case "reconcile":
		return b.dbReconcile(ctx)
	case "inventory":
		return b.dbInventory(ctx, req)
	case "diagnostics", "sessions", "settings":
		return b.dbReadAction(ctx, req.Action, nil)
	case "terminate":
		return b.dbReadAction(ctx, req.Action, []string{req.SessionID})
	case "dump":
		if req.DumpPath != "" {
			return b.dbDumpToPath(ctx, req)
		}
		return b.dbDump(ctx, req)
	case "provision":
		return b.dbProvision(ctx, req)
	case "restore-dump":
		return b.dbRestoreDump(ctx, req)
	case "restore":
		return b.dbRestoreFromPath(ctx, req, "restore")
	case "restore-wordpress":
		return b.dbRestoreFromPath(ctx, req, "restore-wordpress")
	case "drop":
		return b.dbDrop(ctx, req)
	case "drop-managed", "cleanup-wordpress":
		return b.dbDropManaged(ctx, req)
	case "rotate":
		return b.dbRotate(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown db action: %s", req.Action)}, nil
	}
}

func (b *Broker) dbReadAction(ctx context.Context, action string, args []string) (*Response, error) {
	helperArgs := append([]string{action}, args...)
	cmd := stepanelhelper.NewCommand(ctx, b.dbctlPath, helperArgs...)
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("database %s failed: %v: %s", action, err, strings.TrimSpace(string(output)))}, nil
	}
	details, err := json.Marshal(DBResponse{Output: string(output)})
	if err != nil {
		return nil, err
	}
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) dbReconcile(ctx context.Context) (*Response, error) {
	cmd := stepanelhelper.NewCommand(ctx, b.dbctlPath, "reconcile")
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("database reconciliation failed: %v: %s", err, strings.TrimSpace(string(output)))}, nil
	}
	details, err := json.Marshal(DBResponse{Output: string(output)})
	if err != nil {
		return nil, err
	}
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) dbDump(ctx context.Context, req *DBRequest) (*Response, error) {
	cmd := stepanelhelper.NewCommand(ctx, b.dbctlPath, "dump", req.Database)
	output, stderr, err := stepanelhelper.RunCappedSeparate(ctx, cmd, maxBrokerDBDumpBytes, maxBrokerCommandStderr)
	if errors.Is(err, stepanelhelper.ErrOutputLimitExceeded) {
		return &Response{OK: false, Error: "database dump exceeds the broker limit; use the compatibility helper path"}, nil
	}
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("database dump failed: %v: %s", err, strings.TrimSpace(string(stderr)))}, nil
	}
	details, marshalErr := json.Marshal(DBResponse{Database: req.Database, DumpData: output})
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &Response{OK: true, Details: details}, nil
}

// dbDumpToPath streams a database dump into a file the caller created, so
// dumps of any size never pass through the JSON response. The broker never
// creates or chooses the file: it writes only into an existing, empty,
// single-link regular file owned by a non-root user under an approved
// staging root (validateDumpPath). The checks run on the opened descriptor,
// so swapping a path component after validation can at worst redirect the
// write to another empty file the unprivileged caller already owns.
func (b *Broker) dbDumpToPath(ctx context.Context, req *DBRequest) (*Response, error) {
	file, err := openDumpTarget(req.DumpPath)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	defer file.Close()
	cmd := stepanelhelper.NewCommand(ctx, b.dbctlPath, "dump", req.Database)
	if stderr, err := stepanelhelper.RunCappedToFile(ctx, cmd, file, maxBrokerCommandStderr); err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("database dump failed: %v: %s", err, strings.TrimSpace(string(stderr)))}, nil
	}
	if err := file.Sync(); err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("sync database dump: %v", err)}, nil
	}
	details, err := json.Marshal(DBResponse{Database: req.Database})
	if err != nil {
		return nil, err
	}
	return &Response{OK: true, Details: details}, nil
}

// openDumpTarget opens the caller-created dump destination for writing and
// verifies, on the descriptor, that it is safe for root to write into.
func openDumpTarget(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open dump destination: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect dump destination: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !ok || !info.Mode().IsRegular():
		err = errors.New("dump destination must be a regular file")
	case stat.Nlink != 1:
		err = errors.New("dump destination must have exactly one link")
	case stat.Uid == 0:
		err = errors.New("dump destination must be created by the unprivileged caller")
	case info.Size() != 0:
		err = errors.New("dump destination must be empty")
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

// dbInventory is intentionally narrow: the application uses the packaged
// database helper as the source of truth, but an isolated lab container may
// prohibit sudo elevation with no_new_privs. The root broker can run this
// read-only helper without changing the panel or worker service identity.
func (b *Broker) dbInventory(ctx context.Context, _ *DBRequest) (*Response, error) {
	cmd := stepanelhelper.NewCommand(ctx, b.dbctlPath, "inventory")
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("database inventory failed: %v: %s", err, strings.TrimSpace(string(output)))}, nil
	}
	details, _ := json.Marshal(DBResponse{Output: string(output)})
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) dbProvision(ctx context.Context, req *DBRequest) (*Response, error) {
	if err := b.validator.ValidateSiteName(req.Site); err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	if err := b.validator.ValidateEncoding(req.Encoding); err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	if err := validateDBSecret(req.Password, 20); err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	return b.runDBHelper(ctx, []string{"provision", req.Database, req.Username, req.Site, req.Encoding}, []byte(req.Password+"\n"), DBResponse{Provisioned: true, Database: req.Database, Username: req.Username})
}

func (b *Broker) dbRestoreDump(ctx context.Context, req *DBRequest) (*Response, error) {
	if err := b.validator.ValidateSiteName(req.Site); err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	if req.DumpPath != "" {
		return b.dbRestoreFromPath(ctx, req, "restore-dump")
	}
	if len(req.DumpData) == 0 {
		return &Response{OK: false, Error: "dump data is empty"}, nil
	}
	if len(req.DumpData) > maxBrokerDBDumpBytes {
		return &Response{OK: false, Error: "dump data exceeds the broker limit; use the streaming restore path"}, nil
	}
	return b.runDBHelper(ctx, []string{"restore-dump", req.Database, req.Site}, req.DumpData, DBResponse{Restored: true, Database: req.Database})
}

func (b *Broker) dbRestoreFromPath(ctx context.Context, req *DBRequest, action string) (*Response, error) {
	if req.DumpPath == "" {
		return &Response{OK: false, Error: "dump path is required for streaming restore"}, nil
	}
	if err := b.validator.ValidateSiteName(req.Site); err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	if action == "restore-wordpress" {
		if err := b.validator.ValidateUsername(req.Username); err != nil {
			return &Response{OK: false, Error: err.Error()}, nil
		}
		if err := validateDBSecret(req.Password, 16); err != nil {
			return &Response{OK: false, Error: err.Error()}, nil
		}
	}
	file, _, err := stepanelhelper.OpenRegularNoFollow(req.DumpPath, nil)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("open database dump: %v", err)}, nil
	}
	defer file.Close()
	var input io.Reader = file
	args := []string{action, req.Database}
	switch action {
	case "restore", "restore-dump":
		args = append(args, req.Site)
	case "restore-wordpress":
		args = append(args, req.Username, req.Site)
		input = io.MultiReader(strings.NewReader(req.Password+"\n"), file)
	default:
		return &Response{OK: false, Error: "unsupported streaming database action"}, nil
	}
	return b.runDBHelperReader(ctx, args, input, DBResponse{Restored: true, Database: req.Database})
}

func (b *Broker) dbDrop(ctx context.Context, req *DBRequest) (*Response, error) {
	return b.runDBHelper(ctx, []string{"drop", req.Database}, nil, DBResponse{Dropped: true, Database: req.Database})
}

func (b *Broker) dbDropManaged(ctx context.Context, req *DBRequest) (*Response, error) {
	if err := b.validator.ValidateUsername(req.Username); err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	dbctlAction := "drop-managed"
	if req.Action == "cleanup-wordpress" {
		dbctlAction = "cleanup-wordpress"
	}
	return b.runDBHelper(ctx, []string{dbctlAction, req.Database, req.Username}, nil, DBResponse{Dropped: true, Database: req.Database, Username: req.Username})
}

func (b *Broker) dbRotate(ctx context.Context, req *DBRequest) (*Response, error) {
	if err := b.validator.ValidateUsername(req.Username); err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	if err := validateDBSecret(req.Password, 20); err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	return b.runDBHelper(ctx, []string{"rotate", req.Database, req.Username}, []byte(req.Password+"\n"), DBResponse{Database: req.Database, Username: req.Username})
}

func (b *Broker) runDBHelper(ctx context.Context, args []string, input []byte, result DBResponse) (*Response, error) {
	cmd := stepanelhelper.NewCommand(ctx, b.dbctlPath, args...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	return b.runDBHelperCommand(ctx, cmd, result)
}

func (b *Broker) runDBHelperReader(ctx context.Context, args []string, input io.Reader, result DBResponse) (*Response, error) {
	cmd := stepanelhelper.NewCommand(ctx, b.dbctlPath, args...)
	cmd.Stdin = input
	return b.runDBHelperCommand(ctx, cmd, result)
}

func (b *Broker) runDBHelperCommand(ctx context.Context, cmd *exec.Cmd, result DBResponse) (*Response, error) {
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("database operation failed: %v: %s", err, strings.TrimSpace(string(output)))}, nil
	}
	details, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &Response{OK: true, Details: details}, nil
}

// --- Vhost Operations ---

func (b *Broker) handleVhostRequest(ctx context.Context, req *VhostRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "vhost request is nil"}, nil
	}

	b.logger.Printf("vhost: action=%s domain=%s", req.Action, req.Domain)

	switch req.Action {
	case "apply":
		return b.vhostApply(ctx, req)
	case "apply-auth":
		return b.vhostApplyAuth(ctx, req)
	case "import-htaccess":
		return b.vhostImportHtaccess(ctx, req)
	case "delete":
		return b.vhostDelete(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown vhost action: %s", req.Action)}, nil
	}
}

func (b *Broker) vhostApply(ctx context.Context, req *VhostRequest) (*Response, error) {
	args := []string{"apply", req.Site, req.Domain}
	if req.Action == "apply-auth" {
		args = []string{"apply-auth", req.Site, req.Domain, req.BasicAuthUser, req.BasicAuthHash}
	}
	return b.runVhostCommand(ctx, args, VhostResponse{Applied: true, Domain: req.Domain})
}

func (b *Broker) vhostApplyAuth(ctx context.Context, req *VhostRequest) (*Response, error) {
	return b.runVhostCommand(ctx, []string{"apply-auth", req.Site, req.Domain, req.BasicAuthUser, req.BasicAuthHash}, VhostResponse{Applied: true, Domain: req.Domain})
}

func (b *Broker) vhostDelete(ctx context.Context, req *VhostRequest) (*Response, error) {
	return b.runVhostCommand(ctx, []string{"delete", req.Name}, VhostResponse{Deleted: true, Domain: req.Domain})
}

func (b *Broker) vhostImportHtaccess(ctx context.Context, req *VhostRequest) (*Response, error) {
	return b.runVhostCommandInput(ctx, []string{"import-htaccess", req.Site, req.Domain}, []byte(req.Directives), VhostResponse{Applied: true, Domain: req.Domain})
}

func (b *Broker) runVhostCommand(ctx context.Context, args []string, result VhostResponse) (*Response, error) {
	cmd := stepanelhelper.NewCommand(ctx, b.vhostctlPath, args...)
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("vhost operation failed: %v: %s", err, strings.TrimSpace(string(output)))}, nil
	}
	details, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) runVhostCommandInput(ctx context.Context, args []string, input []byte, result VhostResponse) (*Response, error) {
	cmd := stepanelhelper.NewCommand(ctx, b.vhostctlPath, args...)
	cmd.Stdin = bytes.NewReader(input)
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("vhost operation failed: %v: %s", err, strings.TrimSpace(string(output)))}, nil
	}
	details, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &Response{OK: true, Details: details}, nil
}

// --- Proxy Operations ---

func (b *Broker) handleProxyRequest(ctx context.Context, req *ProxyRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "proxy request is nil"}, nil
	}

	b.logger.Printf("proxy: action=%s webserver=%s", req.Action, req.WebServer)

	switch req.Action {
	case "apply":
		return b.proxyApply(ctx, req)
	case "reload":
		return b.proxyReload(ctx, req)
	case "delete":
		return b.proxyDelete(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown proxy action: %s", req.Action)}, nil
	}
}

func (b *Broker) proxyApply(ctx context.Context, req *ProxyRequest) (*Response, error) {
	return b.runProxyCommand(ctx, []string{"apply", req.Site, req.Domain, req.Backend}, ProxyResponse{Applied: true})
}

func (b *Broker) proxyReload(ctx context.Context, req *ProxyRequest) (*Response, error) {
	return b.runProxyCommand(ctx, []string{"reload"}, ProxyResponse{Reloaded: true})
}

func (b *Broker) proxyDelete(ctx context.Context, req *ProxyRequest) (*Response, error) {
	return b.runProxyCommand(ctx, []string{"delete", req.Name}, ProxyResponse{Deleted: true})
}

func (b *Broker) runProxyCommand(ctx context.Context, args []string, result ProxyResponse) (*Response, error) {
	cmd := stepanelhelper.NewCommand(ctx, b.proxyctlPath, args...)
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("proxy operation failed: %v: %s", err, strings.TrimSpace(string(output)))}, nil
	}
	details, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &Response{OK: true, Details: details}, nil
}

// --- Git Operations ---

func (b *Broker) handleGitRequest(ctx context.Context, req *GitRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "git request is nil"}, nil
	}

	b.logger.Printf("git: action=%s repo=%s", req.Action, req.Repository)

	switch req.Action {
	case "delete":
		return b.gitDelete(req)
	case "generate":
		return b.gitGenerate(req)
	case "public":
		return b.gitPublic(req)
	case "clone":
		return b.gitClone(ctx, req)
	case "verify-key":
		return b.gitVerifyKey(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown git action: %s", req.Action)}, nil
	}
}

func (b *Broker) openGitKeyRoot(create bool) (*os.Root, error) {
	if create {
		if err := os.MkdirAll(b.gitKeyRoot, 0700); err != nil {
			return nil, fmt.Errorf("create Git key directory: %w", err)
		}
	}
	info, err := os.Lstat(b.gitKeyRoot)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("Git key path is not a private directory")
	}
	if os.Geteuid() == 0 {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return nil, errors.New("Git key directory is not owned by root")
		}
	}
	root, err := os.OpenRoot(b.gitKeyRoot)
	if err != nil {
		return nil, fmt.Errorf("open Git key directory: %w", err)
	}
	openedInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(info, openedInfo) {
		_ = root.Close()
		return nil, errors.New("Git key directory changed while opening")
	}
	return root, nil
}

func lockGitKeySite(root *os.Root, site string, exclusive bool) (*os.File, error) {
	lock, err := root.OpenFile("."+site+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open Git key lock: %w", err)
	}
	if err := lock.Chmod(0600); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("secure Git key lock: %w", err)
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(lock.Fd()), mode); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("lock Git key: %w", err)
	}
	return lock, nil
}

func unlockGitKeySite(lock *os.File) {
	if lock != nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}
}

func (b *Broker) gitGenerate(req *GitRequest) (*Response, error) {
	root, err := b.openGitKeyRoot(true)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	defer root.Close()
	lock, err := lockGitKeySite(root, req.Site, true)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	defer unlockGitKeySite(lock)
	privateName, publicName := req.Site, req.Site+".pub"
	for _, name := range []string{privateName, publicName} {
		if _, err := root.Lstat(name); err == nil {
			return &Response{OK: false, Error: "deploy key already exists"}, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return &Response{OK: false, Error: fmt.Sprintf("inspect existing deploy key: %v", err)}, nil
		}
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("generate deploy key: %v", err)}, nil
	}
	privateBlock, err := ssh.MarshalPrivateKey(private, "stepanel-"+req.Site+"-deploy")
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("encode deploy key: %v", err)}, nil
	}
	privateData := pem.EncodeToMemory(privateBlock)
	publicKey, err := ssh.NewPublicKey(public)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("encode public deploy key: %v", err)}, nil
	}
	publicData := append(bytes.TrimSpace(ssh.MarshalAuthorizedKey(publicKey)), []byte(" stepanel-"+req.Site+"-deploy\n")...)
	created := make([]string, 0, 2)
	cleanup := func() {
		for _, name := range created {
			_ = root.Remove(name)
		}
	}
	for _, item := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{{privateName, privateData, 0600}, {publicName, publicData, 0644}} {
		file, err := root.OpenFile(item.name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, item.mode)
		if err != nil {
			cleanup()
			return &Response{OK: false, Error: fmt.Sprintf("create deploy key: %v", err)}, nil
		}
		created = append(created, item.name)
		if written, writeErr := file.Write(item.data); writeErr != nil || written != len(item.data) {
			_ = file.Close()
			cleanup()
			if writeErr == nil {
				writeErr = io.ErrShortWrite
			}
			return &Response{OK: false, Error: fmt.Sprintf("write deploy key: %v", writeErr)}, nil
		}
		if err := file.Chmod(item.mode); err != nil {
			_ = file.Close()
			cleanup()
			return &Response{OK: false, Error: fmt.Sprintf("secure deploy key: %v", err)}, nil
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			cleanup()
			return &Response{OK: false, Error: fmt.Sprintf("sync deploy key: %v", err)}, nil
		}
		if err := file.Close(); err != nil {
			cleanup()
			return &Response{OK: false, Error: fmt.Sprintf("close deploy key: %v", err)}, nil
		}
	}
	directory, err := root.Open(".")
	if err != nil {
		cleanup()
		return &Response{OK: false, Error: fmt.Sprintf("open Git key directory for sync: %v", err)}, nil
	}
	if err := directory.Sync(); err != nil {
		if directory != nil {
			_ = directory.Close()
		}
		cleanup()
		return &Response{OK: false, Error: fmt.Sprintf("sync Git key directory: %v", err)}, nil
	}
	if err := directory.Close(); err != nil {
		cleanup()
		return &Response{OK: false, Error: fmt.Sprintf("close Git key directory: %v", err)}, nil
	}
	details, err := json.Marshal(GitResponse{PublicKey: strings.TrimSpace(string(publicData))})
	if err != nil {
		cleanup()
		return nil, err
	}
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) gitPublic(req *GitRequest) (*Response, error) {
	root, err := b.openGitKeyRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return &Response{OK: false, Error: "Git deploy key is not configured"}, nil
	}
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	defer root.Close()
	lock, err := lockGitKeySite(root, req.Site, false)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	defer unlockGitKeySite(lock)
	file, err := root.OpenFile(req.Site+".pub", os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return &Response{OK: false, Error: "Git deploy key is not configured"}, nil
	}
	defer file.Close()
	fileInfo, err := file.Stat()
	if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Size() <= 0 || fileInfo.Size() > 16<<10 {
		return &Response{OK: false, Error: "Git public key is not a bounded regular file"}, nil
	}
	data, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	if err != nil || len(data) == 0 || len(data) > 16<<10 {
		return &Response{OK: false, Error: "Git public key could not be read safely"}, nil
	}
	details, err := json.Marshal(GitResponse{PublicKey: strings.TrimSpace(string(data))})
	if err != nil {
		return nil, err
	}
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) gitDelete(req *GitRequest) (*Response, error) {
	root, err := b.openGitKeyRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return gitDeleteResponse()
	}
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	defer root.Close()
	lock, err := lockGitKeySite(root, req.Site, true)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}
	defer unlockGitKeySite(lock)
	for _, name := range []string{req.Site, req.Site + ".pub"} {
		if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return &Response{OK: false, Error: fmt.Sprintf("remove Git deploy key %s: %v", name, err)}, nil
		}
	}
	return gitDeleteResponse()
}

func gitDeleteResponse() (*Response, error) {
	details, err := json.Marshal(GitResponse{Deleted: true})
	if err != nil {
		return nil, err
	}
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) runLabHelper(ctx context.Context, path string, args ...string) (*Response, error) {
	cmd := stepanelhelper.NewCommand(ctx, path, args...)
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("lab helper failed: %v: %s", err, strings.TrimSpace(string(output)))}, nil
	}
	return &Response{OK: true}, nil
}

func (b *Broker) gitClone(ctx context.Context, req *GitRequest) (*Response, error) {
	if strings.TrimSpace(req.Site) == "" || len(req.AllowedHosts) == 0 {
		return &Response{OK: false, Error: "private Git clone requires a site and host allowlist"}, nil
	}
	allowedHosts := make([]string, 0, len(req.AllowedHosts))
	for _, host := range req.AllowedHosts {
		host = strings.TrimSpace(host)
		if host == "" || strings.ContainsAny(host, "\r\n \t") {
			return &Response{OK: false, Error: "invalid Git host allowlist"}, nil
		}
		allowedHosts = append(allowedHosts, host)
	}
	args := []string{"clone", req.Site, req.Repository, req.Ref, req.Destination, strings.Join(allowedHosts, ",")}
	cmd := stepanelhelper.NewCommand(ctx, b.gitctlPath, args...)
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("private Git clone failed: %v: %s", err, strings.TrimSpace(string(output)))}, nil
	}
	details, marshalErr := json.Marshal(GitResponse{Cloned: true})
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) gitVerifyKey(ctx context.Context, req *GitRequest) (*Response, error) {
	b.logger.Printf("verifying git key")
	return unsupportedBrokerResponse("git key verification")
}

// --- Helpers ---

// execHostOps is the production hostOps implementation. Site account names
// come from internal/siteidentity, while the web group comes from the
// installer-owned /etc/ste-panel.env contract consumed by stepanel-sitectl.
type execHostOps struct{}

func (execHostOps) EnsureSystemUser(ctx context.Context, username, home string) error {
	// Make the operation idempotent only for the specific existing-user case.
	// Other useradd failures must stop the workflow before it creates a site
	// tree that cannot be owned by the intended account.
	if err := stepanelhelper.NewCommand(ctx, "id", "-u", username).Run(); err == nil {
		return (execHostOps{}).ValidateSystemUser(ctx, username, home)
	}
	cmd := stepanelhelper.NewCommand(ctx, "useradd", "--system", "--home-dir", home, "--shell", "/usr/sbin/nologin", "--user-group", username)
	if output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput); err != nil {
		return fmt.Errorf("useradd failed: %w (output: %s)", err, output)
	}
	return nil
}

func (execHostOps) ValidateSystemUser(_ context.Context, username, home string) error {
	account, err := user.Lookup(username)
	if err != nil {
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			return nil
		}
		return fmt.Errorf("lookup existing system user: %w", err)
	}
	if account.HomeDir != home {
		return fmt.Errorf("account home %q does not match site root %q", account.HomeDir, home)
	}
	return nil
}

func (execHostOps) DeleteSystemUser(ctx context.Context, username string) error {
	cmd := stepanelhelper.NewCommand(ctx, "userdel", username)
	if output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput); err != nil {
		var exitErr *exec.ExitError
		// userdel exits with status 6 when the account is already absent. Treat
		// that case as idempotent, but surface every other failure.
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 6 {
			return nil
		}
		return fmt.Errorf("userdel failed: %w (output: %s)", err, output)
	}
	return nil
}

func (execHostOps) Chown(ctx context.Context, path, owner, group string, recursive bool) error {
	args := []string{"--no-dereference", owner + ":" + group, path}
	if recursive {
		args = append([]string{"-R"}, args...)
	}
	cmd := stepanelhelper.NewCommand(ctx, "chown", args...)
	if output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput); err != nil {
		return fmt.Errorf("chown failed: %w (output: %s)", err, output)
	}
	return nil
}

// siteHelperPath is the installed site isolation helper.
const siteHelperPath = "/usr/local/sbin/stepanel-sitectl"

func (execHostOps) RunSiteHelper(ctx context.Context, args ...string) (string, error) {
	cmd := stepanelhelper.NewCommand(ctx, siteHelperPath, args...)
	stdout, stderr, err := stepanelhelper.RunCappedSeparate(ctx, cmd, maxBrokerCommandOutput, maxBrokerCommandStderr)
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", siteHelperPath, strings.Join(args, " "), err, strings.TrimSpace(string(stderr)))
	}
	return string(stdout), nil
}

func (execHostOps) RunSiteHelperInput(ctx context.Context, input []byte, args ...string) (string, error) {
	cmd := stepanelhelper.NewCommand(ctx, siteHelperPath, args...)
	cmd.Stdin = bytes.NewReader(input)
	stdout, stderr, err := stepanelhelper.RunCappedSeparate(ctx, cmd, maxBrokerCommandOutput, maxBrokerCommandStderr)
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", siteHelperPath, strings.Join(args, " "), err, strings.TrimSpace(string(stderr)))
	}
	return string(stdout), nil
}

func (execHostOps) WebGroup() (string, error) {
	return siteidentity.WebGroupFromEnv("/etc/ste-panel.env", func(name string) bool {
		_, err := user.LookupGroup(name)
		return err == nil
	})
}
