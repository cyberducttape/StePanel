package stepanel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/upload"
)

var wpressNamePattern = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
var wpressPrefixPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)
var databasePasswordPattern = regexp.MustCompile(`^[A-Za-z0-9!@#%^*_=+.,:-]{16,128}$`)
var wordpressPluginPathPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)
var wordpressThemeSlugPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)

type wpressPackageMetadata struct {
	PluginsPresent    bool
	Plugins           []string
	TemplatePresent   bool
	Template          string
	StylesheetPresent bool
	Stylesheet        string
	HTAccess          []byte
	HTAccessPresent   bool
}

type WPressResult struct {
	Site             string `json:"site"`
	Home             string `json:"home"`
	Database         string `json:"database"`
	DatabaseUser     string `json:"database_user"`
	SourcePrefix     string `json:"source_prefix"`
	TargetPrefix     string `json:"target_prefix"`
	FilesRestored    bool   `json:"files_restored"`
	DatabaseRestored bool   `json:"database_restored"`
	URLReplaced      bool   `json:"url_replaced"`
	MetadataApplied  bool   `json:"metadata_applied"`
	HTAccessRestored bool   `json:"htaccess_restored"`
	StagedAt         string `json:"staged_at"`
}

// verifyWPressUpload revalidates the staged archive immediately before a
// durable restore. The HTTP request and worker are separated in time, so the
// path alone must not be treated as proof of the uploaded bytes.
func verifyWPressUpload(path string, size int64, checksum string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect staged WordPress archive: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return errors.New("staged WordPress archive size does not match upload metadata")
	}
	actual, err := fileSHA256(path)
	if err != nil {
		return fmt.Errorf("hash staged WordPress archive: %w", err)
	}
	if actual != checksum {
		return errors.New("staged WordPress archive checksum does not match upload metadata")
	}
	return nil
}

func WPressPreflight(cfg Config) map[string]bool {
	return map[string]bool{
		"wpress_extract":  commandAvailable(cfg.WPressExtract),
		"wp_cli":          commandAvailable(cfg.WPCLI),
		"database_client": commandAvailable("mariadb") || commandAvailable("mysql"),
		"mysql_engine":    mysqlCompatible(cfg),
	}
}

func commandAvailable(name string) bool {
	if name == "" {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func validateExtractionLayout(stage, extracted string) error {
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(extracted) && entry.Name() != "tmp" {
			return errors.New("WPress extractor wrote outside its designated output directory")
		}
	}
	return nil
}

func (a *App) wpressPreflight(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ready": allReady(WPressPreflight(a.Config)), "checks": WPressPreflight(a.Config)})
}

func allReady(checks map[string]bool) bool {
	for _, ready := range checks {
		if !ready {
			return false
		}
	}
	return true
}

func validateWPressInput(site, dbSuffix, dbUserSuffix, password, targetPrefix, siteURL string) error {
	if safeUser(site) == "" {
		return errors.New("invalid site account")
	}
	if !wpressNamePattern.MatchString(dbSuffix) || !wpressNamePattern.MatchString(dbUserSuffix) {
		return errors.New("database names may contain only letters, numbers, and underscores")
	}
	if !wpressPrefixPattern.MatchString(targetPrefix) {
		return errors.New("invalid WordPress table prefix")
	}
	if !databasePasswordPattern.MatchString(password) {
		return errors.New("database password must be 16-128 characters using letters, numbers, or !@#%^*_=+.,:-")
	}
	if siteURL != "" {
		u, err := url.Parse(siteURL)
		if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
			return errors.New("site URL must be an http or https URL")
		}
	}
	return nil
}

func (a *App) wpressImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.Config.MaxUpload > 0 && r.ContentLength > maxUploadRequestBytes(a.Config.MaxUpload) {
		http.Error(w, "upload exceeds the configured size limit", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadRequestBytes(a.Config.MaxUpload))
	if !a.Auth.CSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if !allReady(WPressPreflight(a.Config)) {
		http.Error(w, "WPress dependencies are not installed; check /api/wpress/preflight", http.StatusServiceUnavailable)
		return
	}
	if a.Jobs == nil || !a.Jobs.PayloadEncryptionEnabled() {
		http.Error(w, "WordPress durable jobs require STEPANEL_ACCOUNT_KEY for encrypted credential storage", http.StatusServiceUnavailable)
		return
	}
	var site, dbSuffix, dbUserSuffix, password, targetPrefix, siteURL string
	var force bool
	// Every form field must precede the archive part so the restore is
	// validated before any archive byte is accepted.
	validate := func(fields url.Values, filename string) error {
		if fields.Get("confirm") != "WPRESTORE" {
			return &uploadRejection{http.StatusBadRequest, "type WPRESTORE to authorize the restore (form fields must precede the backup file)"}
		}
		site = safeUser(fields.Get("site"))
		dbSuffix = strings.TrimSpace(fields.Get("db_name"))
		dbUserSuffix = strings.TrimSpace(fields.Get("db_user"))
		password = fields.Get("db_password")
		targetPrefix = strings.TrimSpace(fields.Get("table_prefix"))
		if targetPrefix == "" {
			targetPrefix = "wp_"
		}
		siteURL = strings.TrimRight(strings.TrimSpace(fields.Get("site_url")), "/")
		force = fields.Get("overwrite") == "on"
		if err := validateWPressInput(site, dbSuffix, dbUserSuffix, password, targetPrefix, siteURL); err != nil {
			return &uploadRejection{http.StatusUnprocessableEntity, err.Error()}
		}
		if !strings.EqualFold(filepath.Ext(filename), ".wpress") {
			return &uploadRejection{http.StatusBadRequest, "a .wpress archive is required"}
		}
		return nil
	}
	// Stream the archive straight into the private staged object: no
	// multipart spool file, one disk write. The reservation covers the
	// stream; once the archive is on disk the job re-checks capacity for the
	// extracted and staging trees before restoring.
	staged, reservation, ok := a.stageArchiveUpload(w, r, "WPress restore", upload.Options{
		BeforeFile: validate,
		Create: func(string) (*os.File, error) {
			return os.CreateTemp(a.Config.ImportRoot, "wpress-upload-*.wpress")
		},
	})
	if !ok {
		return
	}
	reservation.release()
	tempPath, written := staged.Path, staged.Size
	payload, err := json.Marshal(durableWPressRequest{TempPath: tempPath, Size: written, SHA256: staged.SHA256, Site: site, DBSuffix: dbSuffix, DBUserSuffix: dbUserSuffix, Password: password, SiteURL: siteURL, TargetPrefix: targetPrefix, Force: force, Actor: a.Auth.UsernameForRequest(r)})
	if err != nil {
		_ = os.Remove(tempPath)
		http.Error(w, "could not encode restore job", http.StatusInternalServerError)
		return
	}
	job, _, err := a.Jobs.EnqueueIdempotent("wordpress.restore", site, "", payload, 3)
	if err != nil {
		_ = os.Remove(tempPath)
		http.Error(w, "could not persist restore job", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status_url": filepath.Join("/api/jobs", job.ID)})
}

func RestoreWPress(cfg Config, archive string, access SiteCapability, dbSuffix, dbUserSuffix, dbPassword, siteURL, targetPrefix string, force bool) (WPressResult, error) {
	return RestoreWPressContext(context.Background(), cfg, archive, access, dbSuffix, dbUserSuffix, dbPassword, siteURL, targetPrefix, force)
}

func RestoreWPressContext(parent context.Context, cfg Config, archive string, access SiteCapability, dbSuffix, dbUserSuffix, dbPassword, siteURL, targetPrefix string, force bool) (WPressResult, error) {
	if err := parent.Err(); err != nil {
		return WPressResult{}, err
	}
	site := access.Site()
	if err := validateWPressInput(site, dbSuffix, dbUserSuffix, dbPassword, targetPrefix, siteURL); err != nil {
		return WPressResult{}, err
	}
	if !commandAvailable(cfg.WPressExtract) || !commandAvailable(cfg.WPCLI) {
		return WPressResult{}, errors.New("wpress-extract and wp-cli are required")
	}
	databaseSite := strings.ReplaceAll(site, "-", "_")
	dbName := databaseSite + "_" + dbSuffix
	dbUser := databaseSite + "_" + dbUserSuffix
	if len(dbName) > 64 || len(dbUser) > 32 {
		return WPressResult{}, errors.New("database name or user is too long")
	}
	stage, err := os.MkdirTemp(cfg.ImportRoot, "wpress-restore-")
	if err != nil {
		return WPressResult{}, err
	}
	defer os.RemoveAll(stage)
	if archiveInfo, err := os.Stat(archive); err != nil || !archiveInfo.Mode().IsRegular() || archiveInfo.Size() > cfg.MaxUpload && cfg.MaxUpload > 0 {
		return WPressResult{}, errors.New("WPress archive is missing, not regular, or exceeds the configured upload limit")
	}
	extracted := filepath.Join(stage, "extracted")
	if err := os.MkdirAll(filepath.Join(stage, "tmp"), 0700); err != nil {
		return WPressResult{}, err
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.WPressExtract, "--out", extracted, archive)
	cmd.Dir = stage
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + stage, "TMPDIR=" + filepath.Join(stage, "tmp")}
	if _, err := runBoundedCommand(ctx, cmd); err != nil {
		return WPressResult{}, fmt.Errorf("extract archive: %w", err)
	}
	if err := validateExtractionLayout(stage, extracted); err != nil {
		return WPressResult{}, err
	}
	maxEntries := cfg.MaxEntries
	if maxEntries <= 0 {
		maxEntries = 1000000
	}
	if err := validateWPressTree(extracted, maxEntries); err != nil {
		return WPressResult{}, err
	}
	source, err := findWordPressRoot(extracted)
	if err != nil {
		return WPressResult{}, fmt.Errorf("discover WordPress payload: %w", err)
	}
	if source == "" {
		return WPressResult{}, errors.New("archive must contain WordPress files and database.sql")
	}
	home, err := safePath(cfg.WebRoot, "sites", site, "public")
	if err != nil {
		return WPressResult{}, err
	}
	if _, statErr := os.Lstat(home); statErr == nil && !force {
		return WPressResult{}, errors.New("destination is not empty; enable overwrite only after taking a backup")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return WPressResult{}, fmt.Errorf("inspect destination site: %w", statErr)
	}
	txn, err := BeginSiteTransaction(cfg.RecoveryRoot, home, "wordpress.restore", access)
	if err != nil {
		return WPressResult{}, err
	}
	managerStage, err := createSiteManagerStaging(ctx, cfg, ".stepanel-wpress-")
	if err != nil {
		return WPressResult{}, fmt.Errorf("create lifecycle staging tree: %w", err)
	}
	managerActivated := false
	defer func() {
		if !managerActivated {
			_ = discardSiteManagerStaging(context.Background(), cfg, managerStage)
		}
	}()
	committed := false
	defer func() {
		if !committed {
			if err := txn.cleanupDatabases(cfg); err != nil {
				log.Printf("defer WordPress database recovery for transaction %s: %v", txn.ID, err)
			}
			if err := txn.Rollback(); err != nil {
				log.Printf("defer WordPress filesystem recovery for transaction %s: %v", txn.ID, err)
			}
			if txn.HadExisting {
				if err := siteHelperContext(context.Background(), cfg, "seal", site); err != nil {
					log.Printf("defer WordPress site sealing for transaction %s: %v", txn.ID, err)
				}
			}
		}
	}()
	if err := siteHelperContext(ctx, cfg, "prepare", site); err != nil {
		return WPressResult{}, fmt.Errorf("prepare isolated site: %w", err)
	}
	metadata, err := readWPressPackageMetadata(filepath.Join(source, "package.json"))
	if err != nil {
		return WPressResult{}, fmt.Errorf("read package metadata: %w", err)
	}
	if err := copyWPressTreeContext(ctx, source, managerStage); err != nil {
		return WPressResult{}, fmt.Errorf("restore WordPress files: %w", err)
	}
	if findings, err := scanPHP(managerStage); err != nil {
		return WPressResult{}, fmt.Errorf("scan restored WordPress files: %w", err)
	} else if len(findings) > 0 {
		return WPressResult{}, fmt.Errorf("restore blocked: suspicious PHP heuristic scan found %d file(s) requiring review", len(findings))
	}
	if err := activateStagedSiteWithConfig(ctx, cfg, site, managerStage); err != nil {
		return WPressResult{}, fmt.Errorf("activate restored site through manager: %w", err)
	}
	managerActivated = true
	if cfg.Production && cfg.DBCtl == "" {
		return WPressResult{}, errors.New("production WordPress restores require STEPANEL_DBCTL and the root broker")
	}
	if metadata.HTAccessPresent {
		if err := writeAtomic(filepath.Join(home, ".htaccess"), metadata.HTAccess, 0644); err != nil {
			return WPressResult{}, fmt.Errorf("restore .htaccess: %w", err)
		}
	}
	if findings, err := scanPHP(home); err != nil {
		return WPressResult{}, fmt.Errorf("scan restored WordPress files: %w", err)
	} else if len(findings) > 0 {
		return WPressResult{}, fmt.Errorf("restore blocked: suspicious PHP heuristic scan found %d file(s) requiring review", len(findings))
	}
	if cfg.DBCtl == "" {
		databaseExists, checkErr := mysqlObjectExistsContext(ctx, cfg, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME="+sqlString(dbName))
		if checkErr != nil {
			return WPressResult{}, fmt.Errorf("check WordPress database: %w", checkErr)
		}
		userExists, checkErr := mysqlObjectExistsContext(ctx, cfg, "SELECT COUNT(*) FROM mysql.user WHERE User="+sqlString(dbUser)+" AND Host='localhost'")
		if checkErr != nil {
			return WPressResult{}, fmt.Errorf("check WordPress database user: %w", checkErr)
		}
		if databaseExists || userExists {
			return WPressResult{}, errors.New("database or database user already exists; choose unused names to avoid destructive overwrite")
		}
	}
	if err := txn.TrackDatabase(ManagedDatabase{Name: dbName, User: dbUser, Kind: "wordpress"}); err != nil {
		return WPressResult{}, fmt.Errorf("record database recovery state: %w", err)
	}
	if cfg.DBCtl != "" {
		if err := restoreWPressDatabaseWithHelperContext(ctx, cfg, site, dbName, dbUser, dbPassword, filepath.Join(source, "database.sql")); err != nil {
			return WPressResult{}, err
		}
	} else {
		if err := createWPressDatabaseContext(ctx, cfg, dbName, dbUser, dbPassword); err != nil {
			return WPressResult{}, err
		}
	}
	if cfg.DBCtl == "" {
		if err := importWPressDatabaseContext(ctx, cfg, dbName, filepath.Join(source, "database.sql")); err != nil {
			return WPressResult{}, err
		}
	}
	siteDBConfig := cfg
	if cfg.DBCtl != "" {
		siteDBConfig.DBUser = dbUser
		siteDBConfig.DBPassword = dbPassword
	}
	sourcePrefix, err := detectWPressPrefixContext(ctx, siteDBConfig, dbName)
	if err != nil {
		return WPressResult{}, err
	}
	if sourcePrefix != targetPrefix {
		if err := renameWPressTablesContext(ctx, siteDBConfig, dbName, sourcePrefix, targetPrefix); err != nil {
			return WPressResult{}, fmt.Errorf("normalize table prefix: %w", err)
		}
	}
	if err := configureWordPressContext(ctx, cfg, home, dbName, dbUser, dbPassword, targetPrefix); err != nil {
		return WPressResult{}, err
	}
	if err := applyWPressPackageMetadataContext(ctx, cfg, home, metadata); err != nil {
		return WPressResult{}, err
	}
	urlReplaced := false
	if siteURL != "" {
		oldURL, getErr := runWPOutputContext(ctx, cfg, home, "option", "get", "siteurl")
		if getErr == nil && strings.TrimSpace(oldURL) != "" && strings.TrimSpace(oldURL) != siteURL {
			if err := runWPContext(ctx, cfg, home, "search-replace", strings.TrimSpace(oldURL), siteURL, "--all-tables-with-prefix", "--precise", "--recurse-objects", "--skip-columns=guid", "--quiet"); err != nil {
				return WPressResult{}, fmt.Errorf("replace site URL: %w", err)
			}
			if err := runWPContext(ctx, cfg, home, "option", "update", "home", siteURL); err != nil {
				return WPressResult{}, fmt.Errorf("update home URL: %w", err)
			}
			if err := runWPContext(ctx, cfg, home, "option", "update", "siteurl", siteURL); err != nil {
				return WPressResult{}, fmt.Errorf("update site URL: %w", err)
			}
			urlReplaced = true
		}
	}
	if err := runWPContext(ctx, cfg, home, "rewrite", "flush"); err != nil {
		return WPressResult{}, fmt.Errorf("flush rewrite rules: %w", err)
	}
	if err := runWPContext(ctx, cfg, home, "cache", "flush"); err != nil {
		return WPressResult{}, fmt.Errorf("flush WordPress cache: %w", err)
	}
	if err := siteHelperContext(ctx, cfg, "seal", site); err != nil {
		return WPressResult{}, fmt.Errorf("seal isolated site: %w", err)
	}
	if err := txn.Commit(); err != nil {
		return WPressResult{}, fmt.Errorf("commit site recovery transaction: %w", err)
	}
	committed = true
	return WPressResult{Site: site, Home: home, Database: dbName, DatabaseUser: dbUser, SourcePrefix: sourcePrefix, TargetPrefix: targetPrefix, FilesRestored: true, DatabaseRestored: true, URLReplaced: urlReplaced, MetadataApplied: metadata.PluginsPresent || metadata.TemplatePresent || metadata.StylesheetPresent, HTAccessRestored: metadata.HTAccessPresent, StagedAt: txn.dir}, nil
}

func readWPressPackageMetadata(path string) (wpressPackageMetadata, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return wpressPackageMetadata{}, nil
	}
	if err != nil {
		return wpressPackageMetadata{}, err
	}
	if len(data) > 4<<20 {
		return wpressPackageMetadata{}, errors.New("package.json exceeds the 4 MiB limit")
	}
	var raw struct {
		Plugins    json.RawMessage `json:"Plugins"`
		Template   json.RawMessage `json:"Template"`
		Stylesheet json.RawMessage `json:"Stylesheet"`
		Server     struct {
			HTAccess json.RawMessage `json:".htaccess"`
		} `json:"Server"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return wpressPackageMetadata{}, err
	}
	metadata := wpressPackageMetadata{}
	if raw.Plugins != nil {
		metadata.PluginsPresent = true
		if string(raw.Plugins) == "null" {
			metadata.Plugins = []string{}
		} else if err := json.Unmarshal(raw.Plugins, &metadata.Plugins); err != nil {
			return wpressPackageMetadata{}, errors.New("Plugins must be an array of strings")
		}
		for _, plugin := range metadata.Plugins {
			if !wordpressPluginPathPattern.MatchString(plugin) || strings.HasPrefix(plugin, "/") || strings.Contains(plugin, "..") {
				return wpressPackageMetadata{}, errors.New("Plugins contains an invalid plugin path")
			}
		}
	}
	if raw.Template != nil {
		metadata.TemplatePresent = true
		if err := json.Unmarshal(raw.Template, &metadata.Template); err != nil || metadata.Template != "" && !wordpressThemeSlugPattern.MatchString(metadata.Template) || strings.Contains(metadata.Template, "..") {
			return wpressPackageMetadata{}, errors.New("Template must be a valid theme slug")
		}
	}
	if raw.Stylesheet != nil {
		metadata.StylesheetPresent = true
		if err := json.Unmarshal(raw.Stylesheet, &metadata.Stylesheet); err != nil || metadata.Stylesheet != "" && !wordpressThemeSlugPattern.MatchString(metadata.Stylesheet) || strings.Contains(metadata.Stylesheet, "..") {
			return wpressPackageMetadata{}, errors.New("Stylesheet must be a valid theme slug")
		}
	}
	if raw.Server.HTAccess != nil && string(raw.Server.HTAccess) != "null" {
		var encoded string
		if err := json.Unmarshal(raw.Server.HTAccess, &encoded); err != nil {
			return wpressPackageMetadata{}, errors.New("Server .htaccess must be a base64 string")
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(encoded)
		}
		if err != nil || len(decoded) > 1<<20 {
			return wpressPackageMetadata{}, errors.New("Server .htaccess is invalid or exceeds the 1 MiB limit")
		}
		metadata.HTAccess = decoded
		metadata.HTAccessPresent = true
	}
	return metadata, nil
}

func applyWPressPackageMetadata(cfg Config, home string, metadata wpressPackageMetadata) error {
	return applyWPressPackageMetadataContext(context.Background(), cfg, home, metadata)
}

func applyWPressPackageMetadataContext(ctx context.Context, cfg Config, home string, metadata wpressPackageMetadata) error {
	if metadata.PluginsPresent {
		data, err := json.Marshal(metadata.Plugins)
		if err != nil {
			return fmt.Errorf("encode active plugins: %w", err)
		}
		if err := runWPContext(ctx, cfg, home, "option", "update", "active_plugins", string(data), "--format=json"); err != nil {
			return fmt.Errorf("restore active plugins: %w", err)
		}
	}
	if metadata.TemplatePresent && metadata.Template != "" {
		if err := runWPContext(ctx, cfg, home, "option", "update", "template", metadata.Template); err != nil {
			return fmt.Errorf("restore active theme: %w", err)
		}
	}
	if metadata.StylesheetPresent && metadata.Stylesheet != "" {
		if err := runWPContext(ctx, cfg, home, "option", "update", "stylesheet", metadata.Stylesheet); err != nil {
			return fmt.Errorf("restore active stylesheet: %w", err)
		}
	}
	return nil
}

func validateWPressTree(root string, maxEntries int) error {
	entries := 0
	var total int64
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		entries++
		if entries > maxEntries {
			return errors.New("extracted WPress backup contains too many entries")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("extracted WPress backup contains a symlink: %s", path)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("extracted WPress backup contains a special file: %s", path)
		}
		if info.Mode().IsRegular() {
			size := info.Size()
			if size < 0 || size > 2<<30 || total > maxBackupBytes-size {
				return errors.New("extracted WPress backup exceeds restore size limits")
			}
			total += size
		}
		return nil
	})
}

func findWordPressRoot(root string) (string, error) {
	if info, err := os.Stat(filepath.Join(root, "database.sql")); err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("database.sql is not a regular file")
		}
		return root, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect database.sql: %w", err)
	}
	var found string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if found != "" {
			return filepath.SkipDir
		}
		if !info.IsDir() {
			return nil
		}
		databasePath := filepath.Join(path, "database.sql")
		databaseInfo, databaseErr := os.Stat(databasePath)
		if databaseErr != nil {
			if errors.Is(databaseErr, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("inspect %s: %w", databasePath, databaseErr)
		}
		if !databaseInfo.Mode().IsRegular() {
			return fmt.Errorf("database.sql is not a regular file: %s", databasePath)
		}
		for _, configName := range []string{"wp-config.php", "wp-config-sample.php"} {
			configPath := filepath.Join(path, configName)
			configInfo, configErr := os.Stat(configPath)
			if configErr == nil {
				if !configInfo.Mode().IsRegular() {
					return fmt.Errorf("WordPress config is not a regular file: %s", configPath)
				}
				found = path
				break
			}
			if !errors.Is(configErr, os.ErrNotExist) {
				return fmt.Errorf("inspect %s: %w", configPath, configErr)
			}
		}
		return nil
	})
	return found, err
}

func copyWPressTree(src, dst string) error {
	return copyWPressTreeContext(context.Background(), src, dst)
}

func copyWPressTreeContext(ctx context.Context, src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}
		if path == filepath.Join(src, "database.sql") {
			return nil
		}
		if path == filepath.Join(src, "package.json") {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not allowed: %s", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := rejectSymlinkParents(target, dst); err != nil {
			return err
		}
		if info.IsDir() {
			if existing, statErr := os.Lstat(target); statErr == nil && existing.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("destination symlink is not allowed: %s", target)
			}
			return os.MkdirAll(target, 0750)
		}
		return copyFile(path, target, 0640)
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, _, err := openRegularNoFollow(src, nil)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := openWriteNoFollow(dst, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func runCommand(timeout time.Duration, name string, args ...string) error {
	return runCommandContext(context.Background(), timeout, name, args...)
}

func runCommandContext(parent context.Context, timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	output, err := runBoundedCommand(ctx, exec.CommandContext(ctx, name, args...))
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func runCommandInput(timeout time.Duration, name, input string, args ...string) error {
	return runCommandInputContext(context.Background(), timeout, name, input, args...)
}

func runCommandInputContext(parent context.Context, timeout time.Duration, name, input string, args ...string) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	output, err := runBoundedCommandInput(ctx, exec.CommandContext(ctx, name, args...), strings.NewReader(input))
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func runWP(cfg Config, home string, args ...string) error {
	return runWPContext(context.Background(), cfg, home, args...)
}

func runWPContext(ctx context.Context, cfg Config, home string, args ...string) error {
	base := []string{"--path=" + home, "--skip-plugins", "--skip-themes"}
	base = append(base, args...)
	return runCommandContext(ctx, 10*time.Minute, cfg.WPCLI, base...)
}

func runWPInput(cfg Config, home, input string, args ...string) error {
	return runWPInputContext(context.Background(), cfg, home, input, args...)
}

func runWPInputContext(ctx context.Context, cfg Config, home, input string, args ...string) error {
	base := []string{"--path=" + home, "--skip-plugins", "--skip-themes"}
	base = append(base, args...)
	return runCommandInputContext(ctx, 10*time.Minute, cfg.WPCLI, input, base...)
}

func runWPOutput(cfg Config, home string, args ...string) (string, error) {
	return runWPOutputContext(context.Background(), cfg, home, args...)
}

func runWPOutputContext(parent context.Context, cfg Config, home string, args ...string) (string, error) {
	base := []string{"--path=" + home, "--skip-plugins", "--skip-themes"}
	base = append(base, args...)
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	output, err := runBoundedCommand(ctx, exec.CommandContext(ctx, cfg.WPCLI, base...))
	return string(output), err
}

func configureWordPress(cfg Config, home, dbName, dbUser, dbPassword, prefix string) error {
	return configureWordPressContext(context.Background(), cfg, home, dbName, dbUser, dbPassword, prefix)
}

func configureWordPressContext(ctx context.Context, cfg Config, home, dbName, dbUser, dbPassword, prefix string) error {
	if !fileExists(filepath.Join(home, "wp-config.php")) {
		if err := runWPInputContext(ctx, cfg, home, dbPassword+"\n", "config", "create", "--dbname="+dbName, "--dbuser="+dbUser, "--dbhost="+cfg.DBHost, "--dbprefix="+prefix, "--prompt=dbpass", "--skip-check", "--skip-salts"); err != nil {
			return fmt.Errorf("create wp-config.php: %w", err)
		}
	}
	for _, setting := range [][2]string{{"DB_NAME", dbName}, {"DB_USER", dbUser}, {"DB_HOST", cfg.DBHost}} {
		if err := runWPContext(ctx, cfg, home, "config", "set", setting[0], setting[1], "--type=constant"); err != nil {
			return fmt.Errorf("configure %s: %w", setting[0], err)
		}
	}
	if err := runWPInputContext(ctx, cfg, home, dbPassword+"\n", "config", "set", "DB_PASSWORD", "--type=constant", "--prompt=value"); err != nil {
		return fmt.Errorf("configure DB_PASSWORD: %w", err)
	}
	return nil
}

func detectWPressPrefix(cfg Config, dbName string) (string, error) {
	return detectWPressPrefixContext(context.Background(), cfg, dbName)
}

func detectWPressPrefixContext(ctx context.Context, cfg Config, dbName string) (string, error) {
	query := "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA=" + sqlString(dbName) + " ORDER BY TABLE_NAME"
	output, err := runMySQLContext(ctx, cfg, query)
	if err != nil {
		return "", err
	}
	for _, table := range strings.Split(strings.TrimSpace(output), "\n") {
		for _, suffix := range []string{"commentmeta", "postmeta", "posts", "options", "users", "usermeta", "terms", "termmeta", "term_relationships", "term_taxonomy"} {
			marker := "_" + suffix
			if strings.HasSuffix(table, marker) {
				prefix := strings.TrimSuffix(table, suffix)
				if wpressPrefixPattern.MatchString(prefix) {
					return prefix, nil
				}
			}
		}
	}
	return "", errors.New("could not detect the WordPress table prefix")
}

func renameWPressTables(cfg Config, dbName, sourcePrefix, targetPrefix string) error {
	return renameWPressTablesContext(context.Background(), cfg, dbName, sourcePrefix, targetPrefix)
}

func renameWPressTablesContext(ctx context.Context, cfg Config, dbName, sourcePrefix, targetPrefix string) error {
	if !wpressPrefixPattern.MatchString(sourcePrefix) || !wpressPrefixPattern.MatchString(targetPrefix) {
		return errors.New("invalid WordPress table prefix")
	}
	query := "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA=" + sqlString(dbName) + " AND TABLE_NAME LIKE " + sqlString(sourcePrefix+"%") + " ORDER BY TABLE_NAME"
	output, err := runMySQLContext(ctx, cfg, query)
	if err != nil {
		return err
	}
	statements := make([]string, 0)
	for _, table := range strings.Split(strings.TrimSpace(output), "\n") {
		if table == "" || !strings.HasPrefix(table, sourcePrefix) {
			continue
		}
		newName := targetPrefix + strings.TrimPrefix(table, sourcePrefix)
		if len(table) > 64 || len(newName) > 64 {
			return errors.New("database contains an invalid WordPress table name")
		}
		statements = append(statements, "RENAME TABLE "+sqlIdent(table)+" TO "+sqlIdent(newName))
	}
	if len(statements) == 0 {
		return errors.New("no WordPress tables found for the detected prefix")
	}
	_, err = runMySQLContext(ctx, cfg, strings.Join(statements, "; ")+";")
	return err
}

func createWPressDatabase(cfg Config, dbName, dbUser, password string) error {
	return createWPressDatabaseContext(context.Background(), cfg, dbName, dbUser, password)
}

func createWPressDatabaseContext(ctx context.Context, cfg Config, dbName, dbUser, password string) error {
	databaseExists, err := mysqlObjectExistsContext(ctx, cfg, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME="+sqlString(dbName))
	if err != nil {
		return fmt.Errorf("check WordPress database: %w", err)
	}
	userExists, err := mysqlObjectExistsContext(ctx, cfg, "SELECT COUNT(*) FROM mysql.user WHERE User="+sqlString(dbUser)+" AND Host='localhost'")
	if err != nil {
		return fmt.Errorf("check WordPress database user: %w", err)
	}
	if databaseExists || userExists {
		return errors.New("database or database user already exists; choose unused names to avoid destructive overwrite")
	}
	if _, err := runMySQLContext(ctx, cfg, "CREATE DATABASE "+sqlIdent(dbName)); err != nil {
		return fmt.Errorf("create WordPress database: %w", err)
	}
	query := "CREATE USER " + sqlString(dbUser) + "@'localhost' IDENTIFIED BY " + sqlString(password) + "; GRANT ALL PRIVILEGES ON " + sqlIdent(dbName) + ".* TO " + sqlString(dbUser) + "@'localhost'; FLUSH PRIVILEGES;"
	if _, err := runMySQLContext(ctx, cfg, query); err != nil {
		cleanupErr := cleanupWPressDatabaseContext(context.Background(), cfg, dbName, dbUser)
		if cleanupErr != nil {
			return fmt.Errorf("provision WordPress database user: %w (cleanup failed: %v)", err, cleanupErr)
		}
		return fmt.Errorf("provision WordPress database user: %w", err)
	}
	return nil
}

func mysqlObjectExists(cfg Config, query string) (bool, error) {
	return mysqlObjectExistsContext(context.Background(), cfg, query)
}

func mysqlObjectExistsContext(ctx context.Context, cfg Config, query string) (bool, error) {
	output, err := runMySQLContext(ctx, cfg, query)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output) != "0", nil
}

func cleanupWPressDatabase(cfg Config, dbName, dbUser string) error {
	return cleanupWPressDatabaseContext(context.Background(), cfg, dbName, dbUser)
}

func cleanupWPressDatabaseContext(parent context.Context, cfg Config, dbName, dbUser string) error {
	if cfg.DBCtl != "" {
		ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
		defer cancel()
		output, err, _ := runAllowlistedHelperOutput(ctx, cfg, nil, cfg.DBCtl, "cleanup-wordpress", dbName, dbUser)
		if err != nil {
			return fmt.Errorf("cleanup WordPress database: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	query := "DROP DATABASE IF EXISTS " + sqlIdent(dbName) + "; DROP USER IF EXISTS " + sqlString(dbUser) + "@'localhost'; FLUSH PRIVILEGES;"
	_, err := runMySQLContext(parent, cfg, query)
	return err
}

func restoreWPressDatabaseWithHelper(cfg Config, site, dbName, dbUser, password, dump string) error {
	return restoreWPressDatabaseWithHelperContext(context.Background(), cfg, site, dbName, dbUser, password, dump)
}

func restoreWPressDatabaseWithHelperContext(parent context.Context, cfg Config, site, dbName, dbUser, password, dump string) error {
	ctx, cancel := context.WithTimeout(parent, 20*time.Minute)
	defer cancel()
	if err := runDatabaseRestoreFromPath(ctx, cfg, "restore-wordpress", site, dbName, dbUser, password, dump); err != nil {
		return fmt.Errorf("restore WordPress database: %w", err)
	}
	return nil
}

func importWPressDatabase(cfg Config, dbName, dump string) error {
	return importWPressDatabaseContext(context.Background(), cfg, dbName, dump)
}

func importWPressDatabaseContext(parent context.Context, cfg Config, dbName, dump string) error {
	input, err := os.Open(dump)
	if err != nil {
		return err
	}
	defer input.Close()
	args := append(mysqlArgs(cfg), dbName)
	ctx, cancel := context.WithTimeout(parent, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, mysqlClient(), args...)
	if cfg.DBPassword != "" {
		cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.DBPassword)
	}
	cmd.Stdin = input
	if output, err := runBoundedCommand(ctx, cmd); err != nil {
		return fmt.Errorf("import database: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func runMySQL(cfg Config, query string) (string, error) {
	return runMySQLContext(context.Background(), cfg, query)
}

func runMySQLContext(parent context.Context, cfg Config, query string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	args := append(mysqlArgs(cfg), "--batch", "--skip-column-names")
	cmd := exec.CommandContext(ctx, mysqlClient(), args...)
	if cfg.DBPassword != "" || strings.Contains(query, "IDENTIFIED BY") {
		// Keep credentials and credential-bearing SQL out of argv/procfs.
		cmd.Stdin = strings.NewReader(query + "\n")
	} else {
		cmd.Args = append(cmd.Args, "--execute", query)
	}
	if cfg.DBPassword != "" {
		cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.DBPassword)
	}
	output, err := runBoundedCommand(ctx, cmd)
	return string(output), err
}

func mysqlClient() string {
	if path, err := exec.LookPath("mariadb"); err == nil {
		return path
	}
	return "mysql"
}

func sqlString(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
func sqlIdent(value string) string  { return "`" + strings.ReplaceAll(value, "`", "``") + "`" }
