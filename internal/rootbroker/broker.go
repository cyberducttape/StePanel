package rootbroker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
	gitKeyRoot    string
	validator     *Validator
	logger        *log.Logger
	host          hostOps
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
	DeleteSystemUser(ctx context.Context, username string) error
	Chown(ctx context.Context, path, owner, group string, recursive bool) error
	WebGroup() (string, error)
	// RunSiteHelper runs the installed stepanel-sitectl helper, the single
	// implementation of site isolation (account, ownership, ACLs, PHP pool).
	RunSiteHelper(ctx context.Context, args ...string) (string, error)
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

func newBroker(webRoot, recoveryRoot string, logger *log.Logger, host hostOps) (*Broker, error) {
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
		gitKeyRoot:    "/etc/stepanel/git-keys",
		validator:     NewValidator(webRoot),
		logger:        logger,
		host:          host,
	}, nil
}

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

	// Route to appropriate handler
	switch req.RequestType {
	case "health":
		return &Response{OK: true}, nil
	case "site":
		return b.handleSiteRequest(ctx, req.Site)
	case "app":
		return b.handleAppRequest(ctx, req.App)
	case "db":
		return b.handleDBRequest(ctx, req.DB)
	case "vhost":
		return b.handleVhostRequest(ctx, req.Vhost)
	case "proxy":
		return b.handleProxyRequest(ctx, req.Proxy)
	case "git":
		return b.handleGitRequest(ctx, req.Git)
	case "helper":
		return b.handleHelperRequest(ctx, req.Helper)
	case "certificate":
		return b.handleCertificateRequest(ctx, req.Certificate)
	case "task":
		return b.handleTaskRequest(ctx, req.Task)
	default:
		return &Response{
			OK:    false,
			Error: fmt.Sprintf("unknown request type: %s", req.RequestType),
		}, nil
	}
}

func (b *Broker) handleTaskRequest(ctx context.Context, req *TaskRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "invalid task operation"}, nil
	}
	var args []string
	switch req.Action {
	case "kill":
		unit := "stepanel-task-" + req.Site + "-" + req.Name + ".service"
		args = []string{"kill", "--kill-who=all", "--signal=SIGTERM", unit}
	case "delete", "history":
		args = []string{req.Action, req.Site, req.Name}
	case "apply":
		enabled := "0"
		if req.Enabled {
			enabled = "1"
		}
		args = []string{"task-apply", req.Site, req.Name, req.Runtime, req.OnCalendar, enabled,
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

func (b *Broker) handleHelperRequest(ctx context.Context, req *HelperRequest) (*Response, error) {
	paths := map[string]string{
		"appctl": "/usr/local/sbin/stepanel-appctl", "proxyctl": "/usr/local/sbin/stepanel-proxyctl",
		"sitectl": "/usr/local/sbin/stepanel-sitectl", "vhostctl": "/usr/local/sbin/stepanel-vhostctl",
		"runnerctl": "/usr/local/sbin/stepanel-runnerctl", "gitctl": "/usr/local/sbin/stepanel-gitctl",
		"dbctl": "/usr/local/sbin/stepanel-dbctl",
	}
	path := paths[req.Name]
	args := append([]string{req.Action}, req.Args...)
	cmd := stepanelhelper.NewCommand(ctx, path, args...)
	if len(req.Input) > 64<<20 {
		return &Response{OK: false, Error: "helper input exceeds broker limit"}, nil
	}
	if len(req.Input) > 0 {
		cmd.Stdin = bytes.NewReader(req.Input)
	}
	output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput)
	if errors.Is(err, stepanelhelper.ErrOutputLimitExceeded) {
		return &Response{OK: false, Error: "helper output exceeds broker limit"}, nil
	}
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("helper %s failed: %v: %s", req.Name, err, strings.TrimSpace(string(output)))}, nil
	}
	details, marshalErr := json.Marshal(HelperResponse{Output: string(output)})
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &Response{OK: true, Details: details}, nil
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

	// Generate site user
	siteUser := siteidentity.UnixUser(req.Site)

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
		resp, err := b.runLabHelper(ctx, "/usr/local/sbin/stepanel-sitectl", "delete", req.Site)
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

	siteUser := siteidentity.UnixUser(req.Site)

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

	b.logger.Printf("preparing site: %s", req.Site)
	// stepanel-sitectl prepare-root is the single implementation of the
	// site isolation contract: system account, site-root ownership and mode,
	// the ACL that lets the panel publish into the root, the PHP state tree,
	// and the PHP-FPM pool. It deliberately leaves the public tree to the
	// site manager's staged activation.
	output, err := b.host.RunSiteHelper(ctx, "prepare-root", req.Site)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("site isolation preparation failed: %v", err)}, nil
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if reported, want := strings.TrimSpace(lines[len(lines)-1]), siteidentity.UnixUser(req.Site); reported != want {
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
	return unsupportedBrokerResponse("site access")
}

func (b *Broker) siteResources(ctx context.Context, req *SiteRequest) (*Response, error) {
	return unsupportedBrokerResponse("site resources")
}

func (b *Broker) siteQuota(ctx context.Context, req *SiteRequest) (*Response, error) {
	return unsupportedBrokerResponse("site quota")
}

func (b *Broker) siteQuotaClear(ctx context.Context, req *SiteRequest) (*Response, error) {
	return unsupportedBrokerResponse("site quota clear")
}

func (b *Broker) siteRuntime(ctx context.Context, req *SiteRequest) (*Response, error) {
	return unsupportedBrokerResponse("site runtime")
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
	case "rollback":
		// Rollback is orchestrated by the panel as an apply of the previous manifest.
		return unsupportedBrokerResponse("app rollback")
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown app action: %s", req.Action)}, nil
	}
}

func (b *Broker) runAppHelper(ctx context.Context, req *AppRequest) (*Response, error) {
	args := []string{req.Action, req.Site}
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
	case "inventory":
		return b.dbInventory(ctx, req)
	case "dump":
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
	/*
		if response, err := unsupportedBrokerResponse("database provisioning"); response != nil || err != nil {
			return response, err
		}
		b.logger.Printf("provisioning database: %s user=%s site=%s", req.Database, req.Username, req.Site)

		// Use database name as unique identifier for journaling
		jobID := "db-provision-" + req.Site + "-" + req.Database
		actor := "root"

		// Load or create durable journal for this database provisioning
		journal, err := loadOrCreateDatabaseJournal(b.recoveryRoot, jobID, req.Site, req.Database, req.Username, actor)
		if err != nil {
			b.logger.Printf("failed to load database journal: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
		}

		// Step 1: Create database
		if !journal.isComplete(stepDBCreated) {
			b.logger.Printf("creating database: %s", req.Database)
			// In real implementation, would run: CREATE DATABASE IF NOT EXISTS
			// For now, just verify we can proceed

			if err := journal.markComplete(stepDBCreated); err != nil {
				b.logger.Printf("failed to journal db creation: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping db creation (already complete)")
		}

		// Step 2: Create database user
		if !journal.isComplete(stepUserCreated) {
			b.logger.Printf("creating database user: %s", req.Username)
			// In real implementation, would run: CREATE USER IF NOT EXISTS

			if err := journal.markComplete(stepUserCreated); err != nil {
				b.logger.Printf("failed to journal user creation: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping user creation (already complete)")
		}

		// Step 3: Grant privileges
		if !journal.isComplete(stepPrivsGranted) {
			b.logger.Printf("granting privileges on %s to %s", req.Database, req.Username)
			// In real implementation, would run: GRANT ALL PRIVILEGES ON ...
			// This is idempotent in SQL

			if err := journal.markComplete(stepPrivsGranted); err != nil {
				b.logger.Printf("failed to journal privilege grant: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping privilege grant (already complete)")
		}

		// Step 4: Save credentials
		if !journal.isComplete(stepCredsSaved) {
			// Generate and save database credentials
			siteRoot, err := b.validator.ValidateSiteRoot(req.Site)
			if err == nil {
				credPath := filepath.Join(siteRoot, ".db-credentials")
				journal.setCredLocation(credPath)

				credentials := map[string]string{
					"database": req.Database,
					"username": req.Username,
					"password": "generated-password", // In real implementation, would generate secure password
				}
				credsJSON, _ := json.Marshal(credentials)

				b.logger.Printf("saving database credentials to: %s", credPath)
				if err := writeAtomicBroker(credPath, credsJSON, 0600); err != nil {
					return &Response{OK: false, Error: fmt.Sprintf("credentials save failed: %v", err)}, nil
				}
			}

			if err := journal.markComplete(stepCredsSaved); err != nil {
				b.logger.Printf("failed to journal credentials: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping credentials save (already complete)")
		}

		// Step 5: Verify connectivity
		if !journal.isComplete(stepConnVerified) {
			b.logger.Printf("verifying database connectivity")
			// In real implementation, would test connection to database
			// For now, just mark complete

			if err := journal.markComplete(stepConnVerified); err != nil {
				b.logger.Printf("failed to journal connectivity: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping connectivity check (already complete)")
		}

		// All steps complete: clean up journal
		if err := journal.cleanup(); err != nil {
			b.logger.Printf("warning: failed to cleanup journal: %v", err)
			// Don't fail the operation if journal cleanup fails
		}

		resp := DBResponse{Provisioned: true, Database: req.Database, Username: req.Username}
		details, _ := json.Marshal(resp)
		return &Response{OK: true, Details: details}, nil
	*/
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
	/*
		if response, err := unsupportedBrokerResponse("database restore"); response != nil || err != nil {
			return response, err
		}
		b.logger.Printf("restoring database dump: %s from %s", req.Database, req.DumpData)

		// Use database + dumpfile as unique identifier for journaling
		jobID := "db-restore-" + req.Site + "-" + req.Database
		actor := "root"

		// Load or create durable journal for this database restoration
		journal, err := loadOrCreateRestorationJournal(b.recoveryRoot, jobID, req.Site, req.Database, "dump-data", actor)
		if err != nil {
			b.logger.Printf("failed to load restoration journal: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
		}

		// Step 1: Validate dump file
		if !journal.isComplete(stepRestoreDumpValidated) {
			b.logger.Printf("validating dump file: %s", req.DumpData)
			// In real implementation, would validate file integrity, checksum, etc.
			// For now, just verify dump data is not empty
			if len(req.DumpData) == 0 {
				return &Response{OK: false, Error: "dump data is empty"}, nil
			}

			// Record file size for progress tracking
			dumpSize := len(req.DumpData)
			if dumpSize > 0 {
				journal.updateProgress(0, int64(dumpSize))
			}

			if err := journal.markComplete(stepRestoreDumpValidated); err != nil {
				b.logger.Printf("failed to journal dump validation: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping dump validation (already complete)")
		}

		// Step 2: Drop existing database (if any)
		if !journal.isComplete(stepRestoreDatabaseDropped) {
			b.logger.Printf("dropping existing database: %s", req.Database)
			// In real implementation, would run: DROP DATABASE IF EXISTS
			// This is idempotent - if database doesn't exist, no error

			if err := journal.markComplete(stepRestoreDatabaseDropped); err != nil {
				b.logger.Printf("failed to journal database drop: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping database drop (already complete)")
		}

		// Step 3: Create empty database
		if !journal.isComplete(stepRestoreDatabaseCreated) {
			b.logger.Printf("creating empty database: %s", req.Database)
			// In real implementation, would run: CREATE DATABASE
			// At this point, if crash happens, database is empty but exists
			// Next retry can proceed to import

			if err := journal.markComplete(stepRestoreDatabaseCreated); err != nil {
				b.logger.Printf("failed to journal database creation: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping database creation (already complete)")
		}

		// Step 4: Import SQL dump
		if !journal.isComplete(stepRestoreDumpImported) {
			b.logger.Printf("importing SQL dump into %s", req.Database)
			// In real implementation, would:
			// 1. Open dump file
			// 2. Parse SQL statements
			// 3. Execute each statement
			// 4. Update progress journal periodically
			// 5. If crash, next retry resumes from checkpoint

			// For now, just mark as complete (simulating successful import)
			dumpSize := len(req.DumpData)
			if dumpSize > 0 {
				journal.updateProgress(int64(dumpSize), int64(dumpSize))
			}

			if err := journal.markComplete(stepRestoreDumpImported); err != nil {
				b.logger.Printf("failed to journal dump import: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping dump import (already complete)")
		}

		// Step 5: Verify restoration
		if !journal.isComplete(stepRestoreVerified) {
			b.logger.Printf("verifying database restoration")
			// In real implementation, would:
			// 1. Check table count matches original
			// 2. Verify key indexes exist
			// 3. Run consistency checks
			// 4. Test connectivity

			if err := journal.markComplete(stepRestoreVerified); err != nil {
				b.logger.Printf("failed to journal verification: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping verification (already complete)")
		}

		// All steps complete: clean up journal
		if err := journal.cleanup(); err != nil {
			b.logger.Printf("warning: failed to cleanup journal: %v", err)
			// Don't fail the operation if journal cleanup fails
		}

		resp := DBResponse{Restored: true, Database: req.Database}
		details, _ := json.Marshal(resp)
		return &Response{OK: true, Details: details}, nil
	*/
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
	helperAction := "drop-managed"
	if req.Action == "cleanup-wordpress" {
		helperAction = "cleanup-wordpress"
	}
	return b.runDBHelper(ctx, []string{helperAction, req.Database, req.Username}, nil, DBResponse{Dropped: true, Database: req.Database, Username: req.Username})
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
	case "delete":
		return b.vhostDelete(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown vhost action: %s", req.Action)}, nil
	}
}

func (b *Broker) vhostApply(ctx context.Context, req *VhostRequest) (*Response, error) {
	return unsupportedBrokerResponse("vhost apply")
	/*
		if response, err := unsupportedBrokerResponse("vhost apply"); response != nil || err != nil {
			return response, err
		}
		b.logger.Printf("applying vhost: %s -> %s", req.Domain, req.Site)

		// Validate domain
		if req.Domain == "" {
			return &Response{OK: false, Error: "domain is required"}, nil
		}

		// Use domain as unique identifier for journaling
		jobID := "vhost-apply-" + req.Site + "-" + req.Domain
		actor := "root"

		// Load or create durable journal for this vhost configuration
		journal, err := loadOrCreateVhostJournal(b.recoveryRoot, jobID, req.Site, req.Domain, actor)
		if err != nil {
			b.logger.Printf("failed to load vhost journal: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
		}

		// Step 1: Validate domain and site
		if !journal.isComplete(stepVhostValidated) {
			b.logger.Printf("validating vhost configuration for %s", req.Domain)
			// In real implementation, would validate domain format, DNS, etc.

			if err := journal.markComplete(stepVhostValidated); err != nil {
				b.logger.Printf("failed to journal validation: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping validation (already complete)")
		}

		// Step 2: Generate vhost configuration
		if !journal.isComplete(stepVhostConfigGen) {
			b.logger.Printf("generating vhost configuration")
			// In real implementation, would generate webserver config based on domain

			if err := journal.markComplete(stepVhostConfigGen); err != nil {
				b.logger.Printf("failed to journal config generation: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping config generation (already complete)")
		}

		// Step 3: Write configuration file
		if !journal.isComplete(stepVhostConfigWrite) {
			// Try to use /etc/nginx in production, but fall back to temp in tests
			configPath := filepath.Join("/etc/nginx/sites-available", req.Domain+".conf")

			// If /etc/nginx doesn't exist (e.g., in tests), use temp directory
			if _, err := os.Stat("/etc/nginx"); err != nil {
				tmpDir, err := os.MkdirTemp("", "stepanel-vhost-*")
				if err == nil {
					configPath = filepath.Join(tmpDir, req.Domain+".conf")
					b.logger.Printf("using temp vhost config directory: %s", tmpDir)
				}
			}

			journal.setConfigPath(configPath)

			b.logger.Printf("writing vhost configuration to %s", configPath)
			// In real implementation, would write actual webserver config
			configContent := []byte("# Vhost configuration for " + req.Domain + "\n")
			if err := writeAtomicBroker(configPath, configContent, 0644); err != nil {
				return &Response{OK: false, Error: fmt.Sprintf("config write failed: %v", err)}, nil
			}

			if err := journal.markComplete(stepVhostConfigWrite); err != nil {
				b.logger.Printf("failed to journal config write: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping config write (already complete)")
		}

		// Step 4: Apply configuration to webserver
		if !journal.isComplete(stepVhostApplied) {
			b.logger.Printf("applying configuration to webserver")
			// In real implementation, would enable the site and test config

			if err := journal.markComplete(stepVhostApplied); err != nil {
				b.logger.Printf("failed to journal apply: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping apply (already complete)")
		}

		// Step 5: Verify vhost is responsive
		if !journal.isComplete(stepVhostVerified) {
			b.logger.Printf("verifying vhost responsiveness")
			// In real implementation, would test HTTP endpoint

			if err := journal.markComplete(stepVhostVerified); err != nil {
				b.logger.Printf("failed to journal verification: %v", err)
				return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
			}
		} else {
			b.logger.Printf("skipping verification (already complete)")
		}

		// All steps complete: clean up journal
		if err := journal.cleanup(); err != nil {
			b.logger.Printf("warning: failed to cleanup journal: %v", err)
			// Don't fail the operation if journal cleanup fails
		}

		resp := VhostResponse{Applied: true, Domain: req.Domain}
		details, _ := json.Marshal(resp)
		return &Response{OK: true, Details: details}, nil
	*/
}

func (b *Broker) vhostApplyAuth(ctx context.Context, req *VhostRequest) (*Response, error) {
	return unsupportedBrokerResponse("vhost authentication")
}

func (b *Broker) vhostDelete(ctx context.Context, req *VhostRequest) (*Response, error) {
	return unsupportedBrokerResponse("vhost deletion")
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
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown proxy action: %s", req.Action)}, nil
	}
}

func (b *Broker) proxyApply(ctx context.Context, req *ProxyRequest) (*Response, error) {
	return unsupportedBrokerResponse("proxy apply")
}

func (b *Broker) proxyReload(ctx context.Context, req *ProxyRequest) (*Response, error) {
	return unsupportedBrokerResponse("proxy reload")
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
	b.logger.Printf("git clone requested but broker implementation is unavailable: %s -> %s", req.Repository, req.Destination)
	return unsupportedBrokerResponse("git clone")
}

func (b *Broker) gitVerifyKey(ctx context.Context, req *GitRequest) (*Response, error) {
	b.logger.Printf("verifying git key")
	return unsupportedBrokerResponse("git key verification")
}

// --- Helpers ---

// execHostOps is the production hostOps implementation. Site account names
// and the web group come from internal/siteidentity, the same derivation the
// stepanel-sitectl shell helper uses.
type execHostOps struct{}

func (execHostOps) EnsureSystemUser(ctx context.Context, username, home string) error {
	// Make the operation idempotent only for the specific existing-user case.
	// Other useradd failures must stop the workflow before it creates a site
	// tree that cannot be owned by the intended account.
	if err := stepanelhelper.NewCommand(ctx, "id", "-u", username).Run(); err == nil {
		return nil
	}
	cmd := stepanelhelper.NewCommand(ctx, "useradd", "--system", "--home-dir", home, "--shell", "/usr/sbin/nologin", "--user-group", username)
	if output, err := stepanelhelper.RunCapped(ctx, cmd, maxBrokerCommandOutput); err != nil {
		return fmt.Errorf("useradd failed: %w (output: %s)", err, output)
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

func (execHostOps) WebGroup() (string, error) {
	return siteidentity.WebGroup(func(name string) bool {
		_, err := user.LookupGroup(name)
		return err == nil
	})
}
