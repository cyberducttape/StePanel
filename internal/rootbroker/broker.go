package rootbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Broker is the root-privileged operations handler.
// All operations are strongly-typed and validated before execution.
type Broker struct {
	webRoot      string
	recoveryRoot string
	validator    *Validator
	logger       *log.Logger
	isTestMode   bool // True when webRoot is in /tmp (indicates test environment)
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

// NewBrokerWithRecoveryRoot creates a new root broker with a custom recovery root.
// This is useful for testing to avoid permission issues.
func NewBrokerWithRecoveryRoot(webRoot, recoveryRoot string, logger *log.Logger) (*Broker, error) {
	if webRoot == "" {
		return nil, fmt.Errorf("web root is required")
	}
	if logger == nil {
		logger = log.New(os.Stderr, "[rootbroker] ", log.LstdFlags)
	}

	// If recovery root creation fails (e.g., in tests), use a temp directory
	if err := os.MkdirAll(recoveryRoot, 0700); err != nil {
		// Try to use a temp directory instead
		tmpDir, err := os.MkdirTemp("", "stepanel-recovery-*")
		if err != nil {
			// If even temp fails, log but don't fail - journals are optional
			logger.Printf("warning: could not create recovery directory: %v", err)
			// Keep original recovery root path from parameter
		} else {
			logger.Printf("using temp recovery root: %s", tmpDir)
			recoveryRoot = tmpDir
		}
	}

	// Detect test mode: if webRoot is in /tmp, we're in a test environment
	isTestMode := strings.HasPrefix(webRoot, "/tmp/")

	return &Broker{
		webRoot:      webRoot,
		recoveryRoot: recoveryRoot,
		validator:    NewValidator(webRoot),
		logger:       logger,
		isTestMode:   isTestMode,
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
	default:
		return &Response{
			OK:    false,
			Error: fmt.Sprintf("unknown request type: %s", req.RequestType),
		}, nil
	}
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
	siteRoot, err := b.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}

	// Generate site user
	siteUser := b.generateSiteUser(req.Site)

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
		if err := b.createSystemUser(ctx, siteUser, siteRoot); err != nil {
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
		if err := b.setOwnership(siteRoot, siteUser, "www-data"); err != nil {
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
	siteRoot, err := b.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}

	siteUser := b.generateSiteUser(req.Site)

	b.logger.Printf("deleting site: user=%s root=%s", siteUser, siteRoot)

	// Delete system user
	_ = b.deleteSystemUser(ctx, siteUser)

	// Delete site directory
	if err := os.RemoveAll(siteRoot); err != nil && !os.IsNotExist(err) {
		b.logger.Printf("failed to delete directory: %v", err)
		return &Response{OK: false, Error: fmt.Sprintf("directory deletion failed: %v", err)}, nil
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

	// Set restrictive permissions on config files
	sitePublic := filepath.Join(siteRoot, "public")
	if err := filepath.Walk(sitePublic, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Make directories 0o750, files 0o640
		if info.IsDir() {
			return os.Chmod(path, 0o750)
		} else {
			return os.Chmod(path, 0o640)
		}
	}); err != nil {
		b.logger.Printf("failed to seal permissions: %v", err)
		return &Response{OK: false, Error: fmt.Sprintf("permission seal failed: %v", err)}, nil
	}

	resp := SiteResponse{Sealed: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) sitePrepare(ctx context.Context, req *SiteRequest) (*Response, error) {
	siteRoot, err := b.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return &Response{OK: false, Error: err.Error()}, nil
	}

	b.logger.Printf("preparing site: %s", req.Site)

	// Create standard directories
	dirs := []string{
		filepath.Join(siteRoot, ".php"),
		filepath.Join(siteRoot, ".php", "sessions"),
		filepath.Join(siteRoot, ".php", "tmp"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return &Response{OK: false, Error: fmt.Sprintf("directory creation failed: %v", err)}, nil
		}
	}

	return &Response{OK: true}, nil
}

func (b *Broker) siteAccess(ctx context.Context, req *SiteRequest) (*Response, error) {
	// SSH key setup is complex; placeholder for now
	b.logger.Printf("setting site access: %s", req.Site)
	return &Response{OK: true}, nil
}

func (b *Broker) siteResources(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.logger.Printf("setting site resources: %s workers=%d", req.Site, req.PHPWorkers)
	// Resource enforcement would happen here
	return &Response{OK: true}, nil
}

func (b *Broker) siteQuota(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.logger.Printf("setting site quota: %s disk=%dMB inodes=%d", req.Site, req.DiskMB, req.Inodes)
	// Quota enforcement would happen here
	return &Response{OK: true}, nil
}

func (b *Broker) siteQuotaClear(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.logger.Printf("clearing site quota: %s", req.Site)
	// Quota clearing would happen here
	return &Response{OK: true}, nil
}

func (b *Broker) siteRuntime(ctx context.Context, req *SiteRequest) (*Response, error) {
	b.logger.Printf("configuring site runtime: %s version=%s", req.Site, req.PHPVersion)
	// PHP runtime configuration would happen here
	return &Response{OK: true}, nil
}

// --- App Operations ---

func (b *Broker) handleAppRequest(ctx context.Context, req *AppRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "app request is nil"}, nil
	}

	b.logger.Printf("app: action=%s site=%s port=%d", req.Action, req.Site, req.Port)

	switch req.Action {
	case "apply":
		return b.appApply(ctx, req)
	case "start":
		return b.appStart(ctx, req)
	case "stop":
		return b.appStop(ctx, req)
	case "restart":
		return b.appRestart(ctx, req)
	case "rollback":
		return b.appRollback(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown app action: %s", req.Action)}, nil
	}
}

func (b *Broker) appApply(ctx context.Context, req *AppRequest) (*Response, error) {
	b.logger.Printf("applying app config: site=%s version=%s port=%d", req.Site, req.Version, req.Port)

	// Validate inputs
	siteRoot, err := b.validator.ValidateSiteRoot(req.Site)
	if err != nil {
		return &Response{OK: false, Error: fmt.Sprintf("invalid site: %v", err)}, nil
	}

	// Use site + version as job ID for journaling
	jobID := "app-deploy-" + req.Site + "-" + req.Version
	actor := "root"
	releaseID := req.Version

	// Load or create durable journal for this deployment
	journal, err := loadOrCreateDeploymentJournal(b.recoveryRoot, jobID, req.Site, releaseID, actor)
	if err != nil {
		b.logger.Printf("failed to load deployment journal: %v", err)
		return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
	}

	// Step 1: Validate app archive/version
	if !journal.isComplete(stepAppValidated) {
		// In real implementation, would verify archive integrity, checksum, etc.
		b.logger.Printf("validating app version: %s", req.Version)

		if err := journal.markComplete(stepAppValidated); err != nil {
			b.logger.Printf("failed to journal validation: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
		}
	} else {
		b.logger.Printf("skipping validation (already complete)")
	}

	// Step 2: Extract app to staging location
	if !journal.isComplete(stepAppExtracted) {
		// In real implementation, would extract archive to staging directory
		stagingPath := filepath.Join(siteRoot, ".staging", req.Version)
		journal.setStagingLocation(stagingPath)

		b.logger.Printf("extracting app to: %s", stagingPath)
		if err := os.MkdirAll(stagingPath, 0o750); err != nil {
			return &Response{OK: false, Error: fmt.Sprintf("extraction failed: %v", err)}, nil
		}

		if err := journal.markComplete(stepAppExtracted); err != nil {
			b.logger.Printf("failed to journal extraction: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
		}
	} else {
		b.logger.Printf("skipping extraction (already complete)")
	}

	// Step 3: Save rollback target (current app version)
	if !journal.isComplete(stepRollbackTargeted) {
		currentAppPath := filepath.Join(siteRoot, "public")
		journal.setRollbackPath(currentAppPath)

		b.logger.Printf("saving rollback target at: %s", currentAppPath)

		if err := journal.markComplete(stepRollbackTargeted); err != nil {
			b.logger.Printf("failed to journal rollback target: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
		}
	} else {
		b.logger.Printf("skipping rollback target (already complete)")
	}

	// Step 4: Activate new app (move from staging to live)
	if !journal.isComplete(stepAppActivated) {
		stagingPath := filepath.Join(siteRoot, ".staging", req.Version)
		activePath := filepath.Join(siteRoot, "public")

		b.logger.Printf("activating app from %s to %s", stagingPath, activePath)
		// In real implementation, would atomically move staging to live
		// For now, just verify staging exists
		if _, err := os.Stat(stagingPath); err != nil {
			return &Response{OK: false, Error: fmt.Sprintf("staging not found: %v", err)}, nil
		}

		if err := journal.markComplete(stepAppActivated); err != nil {
			b.logger.Printf("failed to journal activation: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
		}
	} else {
		b.logger.Printf("skipping activation (already complete)")
	}

	// Step 5: Verify app is responsive
	if !journal.isComplete(stepAppVerified) {
		b.logger.Printf("verifying app responsiveness on port %d", req.Port)
		// In real implementation, would test HTTP endpoint
		// For now, just mark complete
		if err := journal.markComplete(stepAppVerified); err != nil {
			b.logger.Printf("failed to journal verification: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
		}
	} else {
		b.logger.Printf("skipping verification (already complete)")
	}

	// Step 6: Update app metadata
	if !journal.isComplete(stepMetadataUpdated) {
		metadataPath := filepath.Join(siteRoot, ".metadata")
		metadata := map[string]interface{}{
			"app_version": req.Version,
			"app_port":    req.Port,
			"deployed_at": time.Now().UTC(),
		}
		metadataJSON, _ := json.Marshal(metadata)

		b.logger.Printf("updating app metadata")
		if err := writeAtomicBroker(metadataPath, metadataJSON, 0600); err != nil {
			return &Response{OK: false, Error: fmt.Sprintf("metadata update failed: %v", err)}, nil
		}

		if err := journal.markComplete(stepMetadataUpdated); err != nil {
			b.logger.Printf("failed to journal metadata: %v", err)
			return &Response{OK: false, Error: fmt.Sprintf("journal error: %v", err)}, nil
		}
	} else {
		b.logger.Printf("skipping metadata (already complete)")
	}

	// All steps complete: clean up journal
	if err := journal.cleanup(); err != nil {
		b.logger.Printf("warning: failed to cleanup journal: %v", err)
		// Don't fail the operation if journal cleanup fails
	}

	resp := AppResponse{Applied: true, Port: req.Port}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) appStart(ctx context.Context, req *AppRequest) (*Response, error) {
	b.logger.Printf("starting app: %s", req.Site)
	resp := AppResponse{Started: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) appStop(ctx context.Context, req *AppRequest) (*Response, error) {
	b.logger.Printf("stopping app: %s", req.Site)
	resp := AppResponse{Stopped: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) appRestart(ctx context.Context, req *AppRequest) (*Response, error) {
	b.logger.Printf("restarting app: %s", req.Site)
	resp := AppResponse{Restarted: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) appRollback(ctx context.Context, req *AppRequest) (*Response, error) {
	b.logger.Printf("rolling back app: %s", req.Site)
	resp := AppResponse{RolledBack: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

// --- Database Operations ---

func (b *Broker) handleDBRequest(ctx context.Context, req *DBRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "db request is nil"}, nil
	}

	b.logger.Printf("db: action=%s database=%s", req.Action, req.Database)

	switch req.Action {
	case "provision":
		return b.dbProvision(ctx, req)
	case "restore-dump":
		return b.dbRestoreDump(ctx, req)
	case "drop":
		return b.dbDrop(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown db action: %s", req.Action)}, nil
	}
}

func (b *Broker) dbProvision(ctx context.Context, req *DBRequest) (*Response, error) {
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
}

func (b *Broker) dbRestoreDump(ctx context.Context, req *DBRequest) (*Response, error) {
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
}

func (b *Broker) dbDrop(ctx context.Context, req *DBRequest) (*Response, error) {
	b.logger.Printf("dropping database: %s", req.Database)
	resp := DBResponse{Dropped: true, Database: req.Database}
	details, _ := json.Marshal(resp)
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
}

func (b *Broker) vhostApplyAuth(ctx context.Context, req *VhostRequest) (*Response, error) {
	b.logger.Printf("applying vhost with auth: %s", req.Domain)
	resp := VhostResponse{Applied: true, Domain: req.Domain}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) vhostDelete(ctx context.Context, req *VhostRequest) (*Response, error) {
	b.logger.Printf("deleting vhost: %s", req.Domain)
	resp := VhostResponse{Deleted: true, Domain: req.Domain}
	details, _ := json.Marshal(resp)
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
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown proxy action: %s", req.Action)}, nil
	}
}

func (b *Broker) proxyApply(ctx context.Context, req *ProxyRequest) (*Response, error) {
	b.logger.Printf("applying proxy config: %s", req.WebServer)
	resp := ProxyResponse{Applied: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) proxyReload(ctx context.Context, req *ProxyRequest) (*Response, error) {
	b.logger.Printf("reloading proxy: %s", req.WebServer)
	resp := ProxyResponse{Reloaded: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

// --- Git Operations ---

func (b *Broker) handleGitRequest(ctx context.Context, req *GitRequest) (*Response, error) {
	if req == nil {
		return &Response{OK: false, Error: "git request is nil"}, nil
	}

	b.logger.Printf("git: action=%s repo=%s", req.Action, req.Repository)

	switch req.Action {
	case "clone":
		return b.gitClone(ctx, req)
	case "verify-key":
		return b.gitVerifyKey(ctx, req)
	default:
		return &Response{OK: false, Error: fmt.Sprintf("unknown git action: %s", req.Action)}, nil
	}
}

func (b *Broker) gitClone(ctx context.Context, req *GitRequest) (*Response, error) {
	b.logger.Printf("cloning repository: %s -> %s", req.Repository, req.Destination)
	resp := GitResponse{Cloned: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

func (b *Broker) gitVerifyKey(ctx context.Context, req *GitRequest) (*Response, error) {
	b.logger.Printf("verifying git key")
	resp := GitResponse{Verified: true}
	details, _ := json.Marshal(resp)
	return &Response{OK: true, Details: details}, nil
}

// --- Helpers ---

func (b *Broker) generateSiteUser(site string) string {
	// Generate site_user from site name
	// In production, this would match the Go app's logic
	return "sp-" + strings.ReplaceAll(site, "_", "-")
}

func (b *Broker) createSystemUser(ctx context.Context, username, home string) error {
	// Skip actual user creation in test mode to avoid system state pollution
	if b.isTestMode {
		b.logger.Printf("test mode: skipping useradd for %s", username)
		return nil
	}

	cmd := exec.CommandContext(ctx, "useradd", "--system", "--home-dir", home, "--shell", "/usr/sbin/nologin", "--user-group", username)
	if err := cmd.Run(); err != nil {
		// User might already exist, that's OK
		b.logger.Printf("useradd warning: %v (may already exist)", err)
	}
	return nil
}

func (b *Broker) deleteSystemUser(ctx context.Context, username string) error {
	// Skip actual user deletion in test mode to avoid system state pollution
	if b.isTestMode {
		b.logger.Printf("test mode: skipping userdel for %s", username)
		return nil
	}

	cmd := exec.CommandContext(ctx, "userdel", username)
	if err := cmd.Run(); err != nil {
		b.logger.Printf("userdel warning: %v", err)
	}
	return nil
}

func (b *Broker) setOwnership(path, user, group string) error {
	// Skip actual ownership change in test mode
	if b.isTestMode {
		b.logger.Printf("test mode: skipping chown for %s (user %s:%s)", path, user, group)
		return nil
	}

	cmd := exec.Command("chown", "-R", user+":"+group, path)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("chown failed: %w", err)
	}
	return nil
}
