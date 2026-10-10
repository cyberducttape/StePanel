package stepanel

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/archivesafe"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

type CPMoveInfo struct {
	Archive           string   `json:"archive"`
	UploadID          string   `json:"upload_id,omitempty"`
	ArchiveBytes      int64    `json:"archive_bytes"`
	ExpandedBytes     int64    `json:"expanded_bytes"`
	DatabaseBytes     int64    `json:"database_bytes"`
	RequiredFreeBytes int64    `json:"required_free_bytes"`
	Entries           int      `json:"entries"`
	User              string   `json:"detected_user"`
	HasHome           bool     `json:"has_home"`
	HasMySQL          bool     `json:"has_mysql"`
	HasMail           bool     `json:"has_mail"`
	Mailboxes         []string `json:"mailboxes,omitempty"`
	Databases         []string `json:"databases"`
}

// CPMoveInspectionResponse contains only metadata needed by the import form.
// Archive-derived names and paths remain server-side so they cannot become
// browser-controlled markup through the inspection response.
type CPMoveInspectionResponse struct {
	UploadID          string `json:"upload_id,omitempty"`
	ArchiveBytes      int64  `json:"archive_bytes"`
	ExpandedBytes     int64  `json:"expanded_bytes"`
	DatabaseBytes     int64  `json:"database_bytes"`
	RequiredFreeBytes int64  `json:"required_free_bytes"`
	Entries           int    `json:"entries"`
	HasHome           bool   `json:"has_home"`
	HasMySQL          bool   `json:"has_mysql"`
	HasMail           bool   `json:"has_mail"`
	DatabaseCount     int    `json:"database_count"`
	MailboxCount      int    `json:"mailbox_count"`
}

func (info CPMoveInfo) response(uploadID string) CPMoveInspectionResponse {
	return CPMoveInspectionResponse{
		UploadID:          uploadID,
		ArchiveBytes:      info.ArchiveBytes,
		ExpandedBytes:     info.ExpandedBytes,
		DatabaseBytes:     info.DatabaseBytes,
		RequiredFreeBytes: info.RequiredFreeBytes,
		Entries:           info.Entries,
		HasHome:           info.HasHome,
		HasMySQL:          info.HasMySQL,
		HasMail:           info.HasMail,
		DatabaseCount:     len(info.Databases),
		MailboxCount:      len(info.Mailboxes),
	}
}

const maxCPMoveExpandedBytes int64 = 80 << 30

// randomSecret uses base64.RawURLEncoding, whose first character may be '-'
// or '_' as well as an alphanumeric. Keep the full URL-safe alphabet valid.
var cpmoveUploadIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type cpmoveUpload struct {
	ID            string    `json:"id"`
	Path          string    `json:"path"`
	Filename      string    `json:"filename"`
	Size          int64     `json:"size"`
	ExpandedBytes int64     `json:"expanded_bytes"`
	DatabaseBytes int64     `json:"database_bytes"`
	SHA256        string    `json:"sha256"`
	Owner         string    `json:"owner"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func cpmoveUploadPath(root, id string) string {
	return filepath.Join(root, "upload-"+id+".tar.gz")
}

func cpmoveUploadMetadataPath(root, id string) string {
	return filepath.Join(root, "upload-"+id+".json")
}

func readCPMoveUpload(root, id string) (cpmoveUpload, error) {
	if !cpmoveUploadIDPattern.MatchString(id) {
		return cpmoveUpload{}, errors.New("invalid upload ID")
	}
	data, err := os.ReadFile(cpmoveUploadMetadataPath(root, id))
	if err != nil {
		return cpmoveUpload{}, err
	}
	var upload cpmoveUpload
	if err := json.Unmarshal(data, &upload); err != nil || upload.ID != id || upload.Owner == "" || upload.Size < 0 || upload.ExpandedBytes < 0 || upload.DatabaseBytes < 0 || len(upload.SHA256) != 2*sha256.Size {
		return cpmoveUpload{}, errors.New("invalid upload metadata")
	}
	if _, err := hex.DecodeString(upload.SHA256); err != nil {
		return cpmoveUpload{}, errors.New("invalid upload checksum")
	}
	if time.Now().After(upload.ExpiresAt) {
		return cpmoveUpload{}, errors.New("upload has expired")
	}
	if upload.Path != cpmoveUploadPath(root, id) || ensureInside(root, upload.Path) != nil {
		return cpmoveUpload{}, errors.New("invalid upload path")
	}
	return upload, nil
}

// verifyCPMoveUpload revalidates the durable upload immediately before a
// restore. Inspection happens in the HTTP request, but the worker may execute
// much later; metadata and byte length alone are not sufficient to prove that
// the staged archive is still the one that was inspected.
func verifyCPMoveUpload(upload cpmoveUpload) error {
	info, err := os.Stat(upload.Path)
	if err != nil {
		return fmt.Errorf("inspect staged cpmove archive: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() != upload.Size {
		return errors.New("staged cpmove archive size does not match upload metadata")
	}
	checksum, err := fileSHA256(upload.Path)
	if err != nil {
		return fmt.Errorf("hash staged cpmove archive: %w", err)
	}
	if checksum != upload.SHA256 {
		return errors.New("staged cpmove archive checksum does not match upload metadata")
	}
	return nil
}

type cpmovePathInfo struct {
	Path string
	User string
}

type ImportResult struct {
	User              string   `json:"user"`
	Home              string   `json:"home"`
	FilesRestored     bool     `json:"files_restored"`
	DatabasesRestored []string `json:"databases_restored"`
	DatabaseErrors    []string `json:"database_errors,omitempty"`
	MailStaged        bool     `json:"mail_staged"`
	MailboxesStaged   []string `json:"mailboxes_staged,omitempty"`
	MailErrors        []string `json:"mail_errors,omitempty"`
	StagedAt          string   `json:"staged_at"`
}

func InspectCPMove(file multipart.File, header *multipart.FileHeader) (CPMoveInfo, error) {
	return inspectCPMove(file, header, 1000000)
}

func inspectCPMove(file multipart.File, header *multipart.FileHeader, maxEntries int) (CPMoveInfo, error) {
	if header == nil {
		return CPMoveInfo{}, errors.New("backup file metadata is missing")
	}
	if header.Size > 20<<30 {
		return CPMoveInfo{}, errors.New("backup exceeds the 20 GiB upload limit")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return CPMoveInfo{}, err
	}
	gz, err := gzip.NewReader(file)
	if err != nil {
		return CPMoveInfo{}, errors.New("backup is not a valid gzip archive")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	// The filename is returned in the API inspection response. Escape it at
	// the trust boundary so a hostile multipart filename cannot become markup
	// if a client renders the response in a browser.
	info := CPMoveInfo{Archive: html.EscapeString(header.Filename), ArchiveBytes: header.Size, User: html.EscapeString(userFromArchiveName(header.Filename))}
	seen := map[string]bool{}
	seenMail := map[string]bool{}
	paths := archivesafe.NewPathSet()
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return info, errors.New("backup tar stream is damaged")
		}
		if h.Size < 0 || h.Size > 2<<30 || total > maxCPMoveExpandedBytes-h.Size {
			return info, errors.New("archive contents exceed the 80 GiB inspection limit")
		}
		total += h.Size
		if !safeArchivePath(h.Name) {
			return info, fmt.Errorf("unsafe archive path: %s", h.Name)
		}
		// Apply the extractor's rules here so inspection rejects what
		// restore would: unsupported entry types and colliding paths.
		kind, kindErr := archivesafe.TarKind(h)
		if kindErr != nil {
			return info, fmt.Errorf("unsupported archive entry type: %w", kindErr)
		}
		if _, claimErr := paths.Claim(h.Name, kind); claimErr != nil {
			return info, fmt.Errorf("unsafe archive path: %w", claimErr)
		}
		info.Entries++
		if maxEntries > 0 && info.Entries > maxEntries {
			return info, errors.New("archive contains too many entries")
		}
		entry := normalizeCPMovePath(h.Name)
		name := entry.Path
		if info.User == "" && entry.User != "" {
			info.User = html.EscapeString(entry.User)
		}
		parts := strings.Split(name, "/")
		if len(parts) > 1 && parts[0] == "homedir" {
			info.HasHome = true
		}
		if strings.HasPrefix(name, "mysql/") {
			info.HasMySQL = true
			if strings.HasSuffix(name, ".sql") {
				// The dumps bound the database server's data growth; the
				// import admission check reserves for it.
				info.DatabaseBytes += h.Size
				db := html.EscapeString(strings.TrimSuffix(filepath.Base(name), ".sql"))
				if !seen[db] {
					info.Databases = append(info.Databases, db)
					seen[db] = true
				}
			}
		}
		if strings.HasPrefix(name, "homedir/mail/") {
			info.HasMail = true
			if len(parts) >= 4 {
				mailbox := html.EscapeString(parts[2] + "/" + parts[3])
				if !seenMail[mailbox] {
					info.Mailboxes = append(info.Mailboxes, mailbox)
					seenMail[mailbox] = true
				}
			}
		}
	}
	sort.Strings(info.Databases)
	sort.Strings(info.Mailboxes)
	info.ExpandedBytes = total
	if info.Entries == 0 {
		return info, errors.New("backup archive is empty")
	}
	return info, nil
}

func RestoreCPMove(cfg Config, file multipart.File, header *multipart.FileHeader, site SiteCapability, databases bool) (ImportResult, error) {
	return RestoreCPMoveContext(context.Background(), cfg, file, header, site, databases)
}

func RestoreCPMoveContext(ctx context.Context, cfg Config, file multipart.File, header *multipart.FileHeader, site SiteCapability, databases bool) (ImportResult, error) {
	if err := ctx.Err(); err != nil {
		return ImportResult{}, err
	}
	if err := os.MkdirAll(cfg.ImportRoot, 0700); err != nil {
		return ImportResult{}, err
	}
	temp, err := os.CreateTemp(cfg.ImportRoot, "restore-upload-*.tar.gz")
	if err != nil {
		return ImportResult{}, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		temp.Close()
		return ImportResult{}, err
	}
	if _, err := io.Copy(temp, file); err != nil {
		temp.Close()
		return ImportResult{}, err
	}
	if err := temp.Close(); err != nil {
		return ImportResult{}, err
	}
	return restoreCPMoveArchiveContext(ctx, cfg, tempPath, header, site, databases)
}

// restoreCPMoveArchiveContext restores from an immutable archive already held
// under ImportRoot. Keeping the durable upload as the extraction source avoids
// copying a large archive into every restore stage.
func restoreCPMoveArchiveContext(ctx context.Context, cfg Config, archive string, header *multipart.FileHeader, site SiteCapability, databases bool) (ImportResult, error) {
	if ensureInside(cfg.ImportRoot, archive) != nil {
		return ImportResult{}, errors.New("archive is outside the import root")
	}
	input, _, err := openRegularNoFollow(archive, nil)
	if err != nil {
		return ImportResult{}, err
	}
	if _, err := inspectCPMove(input, header, cfg.MaxEntries); err != nil {
		input.Close()
		return ImportResult{}, err
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		input.Close()
		return ImportResult{}, err
	}
	randomID, err := randomSecret()
	if err != nil {
		input.Close()
		return ImportResult{}, fmt.Errorf("create import staging ID: %w", err)
	}
	user := site.Site()
	id := time.Now().UTC().Format("20060102-150405") + "-" + user + "-" + randomID[:12]
	stage := filepath.Join(cfg.ImportRoot, id)
	if err := os.MkdirAll(stage, 0700); err != nil {
		input.Close()
		return ImportResult{}, err
	}
	if err = extractArchiveReaderContext(ctx, input, stage, archive); err != nil {
		_ = input.Close()
		return ImportResult{}, err
	}
	if err = input.Close(); err != nil {
		return ImportResult{}, fmt.Errorf("close verified cpmove archive: %w", err)
	}
	root, err := cpmoveRoot(stage)
	if err != nil {
		return ImportResult{}, err
	}
	home, err := safePath(cfg.WebRoot, "sites", user, "public")
	if err != nil {
		return ImportResult{}, err
	}
	manager, err := siteauthority.NewDefaultManager(cfg.WebRoot)
	if err != nil {
		return ImportResult{}, fmt.Errorf("initialize site manager: %w", err)
	}
	managerStage, err := manager.CreateStaging(ctx, ".stepanel-cpmove-")
	if err != nil {
		return ImportResult{}, fmt.Errorf("create site manager staging tree: %w", err)
	}
	activated := false
	defer func() {
		if !activated {
			_ = manager.DiscardStaging(context.Background(), managerStage)
		}
	}()
	txn, err := BeginSiteTransaction(cfg.RecoveryRoot, home, "cpmove.restore", site)
	if err != nil {
		return ImportResult{}, err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// The operation context may already be cancelled; keep its lease
		// token so the broker accepts the cleanup.
		if err := txn.cleanupDatabases(context.WithoutCancel(ctx), cfg); err != nil {
			log.Printf("defer cpmove database recovery for transaction %s: %v", txn.ID, err)
		}
		if err := txn.Rollback(); err != nil {
			log.Printf("defer cpmove filesystem recovery for transaction %s: %v", txn.ID, err)
		}
		if txn.HadExisting {
			if err := siteHelperContext(context.WithoutCancel(ctx), cfg, "seal", user); err != nil {
				log.Printf("defer cpmove site sealing for transaction %s: %v", txn.ID, err)
			}
		}
	}()
	if err := siteHelperContext(ctx, cfg, "prepare", user); err != nil {
		return ImportResult{}, fmt.Errorf("prepare isolated site: %w", err)
	}
	source := firstExisting(filepath.Join(root, "homedir", "public_html"), filepath.Join(root, "homedir", user, "public_html"))
	if source != "" {
		if err := ctx.Err(); err != nil {
			return ImportResult{}, err
		}
		if findings, scanErr := scanPHP(source); scanErr != nil {
			return ImportResult{}, fmt.Errorf("suspicious PHP heuristic scan failed: %w", scanErr)
		} else if len(findings) > 0 {
			return ImportResult{}, fmt.Errorf("restore blocked: suspicious PHP heuristic scan found %d file(s) requiring review", len(findings))
		}
		if err = copyTreeContext(ctx, source, managerStage); err != nil {
			return ImportResult{}, err
		}
	}
	if _, err := manager.ActivateStaged(ctx, user, managerStage); err != nil {
		return ImportResult{}, fmt.Errorf("activate cpmove site through manager: %w", err)
	}
	activated = true
	// The transaction journal is already durable before activation.  Keep a
	// process-death boundary here so disposable-host recovery drills can kill
	// the worker after the canonical swap and verify that startup rolls the
	// interrupted import back safely.
	processKillInjection("cpmove", "activate")

	result := ImportResult{User: user, Home: home, FilesRestored: source != "", StagedAt: stage}
	if databases {
		result.DatabasesRestored, result.DatabaseErrors = restoreSQLContext(ctx, cfg, root, user, txn)
		if len(result.DatabaseErrors) > 0 {
			return result, fmt.Errorf("database restore completed with %d error(s)", len(result.DatabaseErrors))
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if cfg.MailRoot != "" {
		mailSource := filepath.Join(root, "homedir", "mail")
		if info, statErr := os.Stat(mailSource); statErr == nil && info.IsDir() {
			txn.MailRoot, err = safePath(cfg.MailRoot, user)
			if err != nil {
				return result, fmt.Errorf("resolve destination mail root: %w", err)
			}
			txn.MailBackup = filepath.Join(txn.dir, "mail-before")
			if existing, destinationErr := os.Lstat(txn.MailRoot); destinationErr == nil {
				if existing.Mode()&os.ModeSymlink != 0 {
					return result, errors.New("destination mail root is a symlink")
				}
				txn.MailExisting = true
				if err := txn.persist(); err != nil {
					return result, fmt.Errorf("record mail recovery state: %w", err)
				}
				if err := os.Rename(txn.MailRoot, txn.MailBackup); err != nil {
					return result, fmt.Errorf("snapshot existing mail: %w", err)
				}
			} else if !errors.Is(destinationErr, os.ErrNotExist) {
				return result, fmt.Errorf("inspect destination mail root: %w", destinationErr)
			} else if err := txn.persist(); err != nil {
				return result, fmt.Errorf("record mail recovery state: %w", err)
			}
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return result, fmt.Errorf("inspect staged mail: %w", statErr)
		}
	}
	result.MailStaged, result.MailboxesStaged, result.MailErrors = restoreMailContext(ctx, cfg, root, user)
	if len(result.MailErrors) > 0 {
		return result, fmt.Errorf("mail restore completed with %d error(s)", len(result.MailErrors))
	}
	if err := siteHelperContext(ctx, cfg, "seal", user); err != nil {
		return result, fmt.Errorf("seal isolated site: %w", err)
	}
	if err := txn.Commit(); err != nil {
		return result, fmt.Errorf("commit site recovery transaction: %w", err)
	}
	committed = true
	return result, nil
}

func siteHelperContext(ctx context.Context, cfg Config, action, site string) error {
	if cfg.SiteCtl == "" {
		return nil
	}
	if cfg.Production || labDirectRootBrokerEnabled() {
		client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", cfg.WebRoot)
		if err != nil {
			return err
		}
		resp, err := client.Execute(ctx, &rootbroker.Request{
			RequestType: "site",
			Site:        &rootbroker.SiteRequest{Action: action, Site: site},
		})
		if err != nil {
			return err
		}
		if !resp.OK {
			return errors.New(resp.Error)
		}
		return nil
	}
	if action == "prepare" {
		// Every prepare caller publishes the public tree through the site
		// manager afterwards, so ask the helper for the isolation contract
		// without creating public/ (the typed broker does the same).
		action = "prepare-root"
	}
	return runHelperCommand(ctx, cfg, cfg.SiteCtl, action, site)
}

// restoreMail preserves cPanel mailbox data and account mail metadata under a
// private StePanel root. Host-specific Exim/Dovecot configuration is not
// copied into /etc because it can break the destination mail server.
func restoreMail(cfg Config, stage, user string) (bool, []string, []string) {
	return restoreMailContext(context.Background(), cfg, stage, user)
}

func restoreMailContext(ctx context.Context, cfg Config, stage, user string) (bool, []string, []string) {
	if err := ctx.Err(); err != nil {
		return false, nil, []string{err.Error()}
	}
	sourceMail := filepath.Join(stage, "homedir", "mail")
	sourceEtc := filepath.Join(stage, "homedir", "etc")
	if _, err := os.Stat(sourceMail); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil, nil
		}
		return false, nil, []string{"inspect staged mail: " + err.Error()}
	}
	if cfg.MailRoot == "" {
		return false, nil, []string{"mail root is not configured; set STEPANEL_MAIL_ROOT"}
	}
	root, err := safePath(cfg.MailRoot, user)
	if err != nil {
		return false, nil, []string{"resolve mail root: " + err.Error()}
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return false, nil, []string{"create mail root: " + err.Error()}
	}
	if err := copyTreeContext(ctx, sourceMail, filepath.Join(root, "mail")); err != nil {
		return false, nil, []string{"copy mailbox data: " + err.Error()}
	}
	if _, err := os.Stat(sourceEtc); err == nil {
		if err := copyTreeContext(ctx, sourceEtc, filepath.Join(root, "etc")); err != nil {
			return false, nil, []string{"copy mail metadata: " + err.Error()}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, nil, []string{"inspect mail metadata: " + err.Error()}
	}
	mailboxes := []string{}
	mailRoot := filepath.Join(root, "mail")
	walkErr := filepath.Walk(mailRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && path != mailRoot {
			rel, relErr := filepath.Rel(mailRoot, path)
			if relErr != nil {
				return relErr
			}
			if strings.Count(filepath.ToSlash(rel), "/") == 1 {
				mailboxes = append(mailboxes, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	sort.Strings(mailboxes)
	if walkErr != nil {
		return true, mailboxes, []string{"discover mailbox inventory: " + walkErr.Error()}
	}
	return true, mailboxes, nil
}

func extractArchive(archive, destination string) error {
	return extractArchiveContext(context.Background(), archive, destination)
}

func extractArchiveContext(ctx context.Context, archive, destination string) error {
	f, _, err := openRegularNoFollow(archive, nil)
	if err != nil {
		return err
	}
	defer f.Close()
	return extractArchiveReaderContext(ctx, f, destination, archive)
}

// extractArchiveReaderContext extracts a cPanel or StePanel tar.gz into
// destination through internal/archivesafe: entries cannot escape it,
// collide with one another, or overwrite anything already there.
func extractArchiveReaderContext(ctx context.Context, input io.Reader, destination, archivePath string) error {
	gz, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer gz.Close()
	extractor, err := archivesafe.OpenExtractor(destination)
	if err != nil {
		return err
	}
	defer extractor.Close()
	// The staged upload may live in the destination; no entry may name it.
	stagedUpload := ""
	if archivePath != "" {
		if rel, relErr := filepath.Rel(destination, archivePath); relErr == nil && filepath.IsLocal(rel) {
			stagedUpload = filepath.ToSlash(rel)
			if err := extractor.Reserve(stagedUpload); err != nil {
				return err
			}
		}
	}
	tr := tar.NewReader(gz)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if !safeArchivePath(h.Name) {
			return errors.New("unsafe archive path")
		}
		if stagedUpload != "" {
			if clean, cleanErr := archivesafe.NormalizeName(h.Name); cleanErr == nil && clean == stagedUpload {
				return errors.New("archive entry conflicts with staged upload")
			}
		}
		kind, err := archivesafe.TarKind(h)
		if err != nil {
			return fmt.Errorf("unsupported archive entry type: %w", err)
		}
		if kind == archivesafe.Directory {
			if _, err := extractor.Dir(h.Name, 0o700); err != nil {
				return fmt.Errorf("unsafe archive path: %w", err)
			}
			continue
		}
		if h.Size < 0 || h.Size > 2<<30 || total+h.Size > 20<<30 {
			return errors.New("archive contents exceed the 20 GiB extraction limit")
		}
		_, written, err := extractor.File(h.Name, 0o600, tr, h.Size)
		if err != nil {
			return fmt.Errorf("unsafe archive path: %w", err)
		}
		total += written
	}
}

func restoreSQL(cfg Config, stage, user string, txn *SiteTransaction) ([]string, []string) {
	return restoreSQLContext(context.Background(), cfg, stage, user, txn)
}

func restoreSQLContext(parent context.Context, cfg Config, stage, user string, txn *SiteTransaction) ([]string, []string) {
	matches, discoveryErr := sqlDumps(filepath.Join(stage, "mysql"))
	restored, failures := []string{}, []string{}
	if discoveryErr != nil {
		failures = append(failures, "discover SQL dumps: "+discoveryErr.Error())
		return restored, failures
	}
	if cfg.Production && cfg.DBCtl == "" && len(matches) > 0 {
		return restored, []string{"restore SQL dumps: production database restores require STEPANEL_DBCTL and the root broker"}
	}
	for _, dump := range matches {
		if err := parent.Err(); err != nil {
			failures = append(failures, err.Error())
			break
		}
		db := safeUser(strings.TrimSuffix(filepath.Base(dump), ".sql"))
		if db == "" {
			failures = append(failures, filepath.Base(dump)+": invalid database name")
			continue
		}
		name := databaseName(user, db)
		if len(name) > 64 {
			failures = append(failures, name+": database name exceeds 64 characters")
			continue
		}
		ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
		if cfg.DBCtl != "" {
			if txn != nil {
				if err := txn.TrackDatabase(ManagedDatabase{Name: name, Kind: "cpmove"}); err != nil {
					failures = append(failures, name+": record recovery state: "+err.Error())
					cancel()
					continue
				}
			}
			restoreErr := runDatabaseRestoreFromPath(ctx, cfg, "restore", user, name, user, "", dump)
			cancel()
			if restoreErr != nil {
				failures = append(failures, name+": restore failed: "+restoreErr.Error())
				continue
			}
			restored = append(restored, name)
			continue
		}
		exists, existsErr := mysqlObjectExists(cfg, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME="+sqlString(name))
		if existsErr != nil {
			failures = append(failures, name+": existence check failed: "+existsErr.Error())
			cancel()
			continue
		}
		if exists {
			failures = append(failures, name+": database already exists")
			cancel()
			continue
		}
		if txn != nil {
			if err := txn.TrackDatabase(ManagedDatabase{Name: name, Kind: "cpmove"}); err != nil {
				failures = append(failures, name+": record recovery state: "+err.Error())
				cancel()
				continue
			}
		}
		args := mysqlArgs(cfg)
		args = append(args, "--batch", "--execute", "CREATE DATABASE `"+name+"`")
		cmd := exec.CommandContext(ctx, mysqlClient(), args...)
		if cfg.DBPassword != "" {
			cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.DBPassword)
		}
		output, err := runBoundedCommand(ctx, cmd)
		if err != nil {
			failures = append(failures, name+": create failed: "+strings.TrimSpace(string(output)))
			cancel()
			continue
		}
		input, err := os.Open(dump)
		if err != nil {
			failures = append(failures, name+": open failed: "+err.Error())
			cancel()
			continue
		}
		args = mysqlArgs(cfg)
		args = append(args, name)
		cmd = exec.CommandContext(ctx, mysqlClient(), args...)
		if cfg.DBPassword != "" {
			cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.DBPassword)
		}
		cmd.Stdin = input
		output, err = runBoundedCommand(ctx, cmd)
		closeErr := input.Close()
		if closeErr != nil {
			failures = append(failures, name+": close dump failed: "+closeErr.Error())
		}
		cancel()
		if err != nil {
			failures = append(failures, name+": import failed: "+strings.TrimSpace(string(output)))
			if cleanupErr := dropDatabase(parent, cfg, name); cleanupErr != nil {
				failures = append(failures, name+": cleanup failed: "+cleanupErr.Error())
			}
			continue
		}
		restored = append(restored, name)
	}
	return restored, failures
}

// dropDatabase removes a managed database. parent must carry the site lease
// (its fencing token) when the drop goes through the root broker.
func dropDatabase(parent context.Context, cfg Config, name string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 2*time.Minute)
	defer cancel()
	if cfg.DBCtl != "" {
		output, err := runDatabaseHelperContext(ctx, cfg, 2*time.Minute, "", "drop", name)
		if err != nil {
			return fmt.Errorf("drop database: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	args := append(mysqlArgs(cfg), "--batch", "--execute", "DROP DATABASE IF EXISTS `"+name+"`")
	cmd := exec.CommandContext(ctx, mysqlClient(), args...)
	if cfg.DBPassword != "" {
		cmd.Env = append(os.Environ(), "MYSQL_PWD="+cfg.DBPassword)
	}
	output, err := runBoundedCommand(ctx, cmd)
	if err != nil {
		return fmt.Errorf("drop database: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func mysqlArgs(cfg Config) []string {
	args := []string{}
	if cfg.DBHost != "" {
		args = append(args, "--host", cfg.DBHost)
	}
	if cfg.DBUser != "" {
		args = append(args, "--user", cfg.DBUser)
	}
	return args
}

func sqlDumps(root string) ([]string, error) {
	matches := []string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".sql") {
			matches = append(matches, path)
		}
		return nil
	})
	sort.Strings(matches)
	return matches, err
}

func databaseName(user, db string) string {
	user = strings.ReplaceAll(user, "-", "_")
	db = strings.ReplaceAll(db, "-", "_")
	if strings.HasPrefix(db, user+"_") {
		return db
	}
	return user + "_" + db
}

func copyTree(src, dst string) error {
	return copyTreeContext(context.Background(), src, dst)
}

func copyTreeContext(ctx context.Context, src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
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
		if existing, statErr := os.Lstat(target); statErr == nil && existing.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination symlink is not allowed: %s", target)
		}
		if info.IsDir() {
			if existing, statErr := os.Lstat(target); statErr == nil && existing.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("destination symlink is not allowed: %s", target)
			}
			return os.MkdirAll(target, 0750)
		}
		in, _, err := openRegularNoFollow(path, info)
		if err != nil {
			return err
		}
		out, err := openWriteNoFollow(target, 0640)
		if err != nil {
			_ = in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		inErr := in.Close()
		closeErr := out.Close()
		if err == nil {
			err = inErr
		}
		if err == nil {
			err = closeErr
		}
		return err
	})
}
func firstExisting(paths ...string) string {
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		}
	}
	return ""
}
func safeArchivePath(path string) bool {
	clean := filepath.Clean(path)
	return path != "" && !strings.Contains(path, "\\") && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && !filepath.IsAbs(path)
}

func normalizeCPMovePath(path string) cpmovePathInfo {
	name := strings.Trim(filepath.ToSlash(path), "/")
	parts := strings.Split(name, "/")
	if len(parts) >= 2 && isCPMoveRoot(parts[0]) && (parts[1] == "homedir" || parts[1] == "mysql") {
		return cpmovePathInfo{Path: strings.Join(parts[1:], "/"), User: userFromArchiveName(parts[0])}
	}
	return cpmovePathInfo{Path: name}
}

func isCPMoveRoot(name string) bool {
	return strings.HasPrefix(name, "cpmove-") || strings.HasPrefix(name, "backup-")
}

func userFromArchiveName(name string) string {
	base := filepath.Base(strings.TrimSuffix(strings.TrimSuffix(name, ".gz"), ".tar"))
	if strings.HasPrefix(base, "cpmove-") {
		candidate := strings.TrimPrefix(base, "cpmove-")
		if i := strings.Index(candidate, "."); i > 0 {
			candidate = candidate[:i]
		}
		if safeUser(candidate) != "" {
			return candidate
		}
	}
	if strings.HasPrefix(base, "backup-") {
		candidate := strings.TrimPrefix(base, "backup-")
		if i := strings.LastIndex(candidate, "_"); i >= 0 && i < len(candidate)-1 {
			candidate = candidate[i+1:]
		} else {
			return ""
		}
		if safeUser(candidate) != "" {
			return candidate
		}
	}
	return ""
}

func cpmoveRoot(stage string) (string, error) {
	if firstExisting(filepath.Join(stage, "homedir"), filepath.Join(stage, "mysql")) != "" {
		return stage, nil
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() && firstExisting(filepath.Join(stage, entry.Name(), "homedir"), filepath.Join(stage, entry.Name(), "mysql")) != "" {
			return filepath.Join(stage, entry.Name()), nil
		}
	}
	return "", errors.New("backup does not contain a cPanel homedir or mysql directory")
}
