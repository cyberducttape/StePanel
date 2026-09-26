package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

type CPMoveInfo struct {
	Archive   string   `json:"archive"`
	Entries   int      `json:"entries"`
	User      string   `json:"detected_user"`
	HasHome   bool     `json:"has_home"`
	HasMySQL  bool     `json:"has_mysql"`
	HasMail   bool     `json:"has_mail"`
	Mailboxes []string `json:"mailboxes,omitempty"`
	Databases []string `json:"databases"`
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
	info := CPMoveInfo{Archive: header.Filename, User: userFromArchiveName(header.Filename)}
	seen := map[string]bool{}
	seenMail := map[string]bool{}
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return info, errors.New("backup tar stream is damaged")
		}
		if h.Size < 0 || h.Size > 2<<30 || total+h.Size > 20<<30 {
			return info, errors.New("archive contents exceed the 20 GiB inspection limit")
		}
		total += h.Size
		if !safeArchivePath(h.Name) {
			return info, fmt.Errorf("unsafe archive path: %s", h.Name)
		}
		info.Entries++
		if maxEntries > 0 && info.Entries > maxEntries {
			return info, errors.New("archive contains too many entries")
		}
		entry := normalizeCPMovePath(h.Name)
		name := entry.Path
		if info.User == "" && entry.User != "" {
			info.User = entry.User
		}
		parts := strings.Split(name, "/")
		if len(parts) > 1 && parts[0] == "homedir" {
			info.HasHome = true
		}
		if strings.HasPrefix(name, "mysql/") {
			info.HasMySQL = true
			if strings.HasSuffix(name, ".sql") {
				db := strings.TrimSuffix(filepath.Base(name), ".sql")
				if !seen[db] {
					info.Databases = append(info.Databases, db)
					seen[db] = true
				}
			}
		}
		if strings.HasPrefix(name, "homedir/mail/") {
			info.HasMail = true
			if len(parts) >= 4 {
				mailbox := parts[2] + "/" + parts[3]
				if !seenMail[mailbox] {
					info.Mailboxes = append(info.Mailboxes, mailbox)
					seenMail[mailbox] = true
				}
			}
		}
	}
	sort.Strings(info.Databases)
	sort.Strings(info.Mailboxes)
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
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ImportResult{}, err
	}
	if _, err := inspectCPMove(file, header, cfg.MaxEntries); err != nil {
		return ImportResult{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ImportResult{}, err
	}
	randomID, err := randomSecret()
	if err != nil {
		return ImportResult{}, fmt.Errorf("create import staging ID: %w", err)
	}
	user := site.Site()
	id := time.Now().UTC().Format("20060102-150405") + "-" + user + "-" + randomID[:12]
	stage := filepath.Join(cfg.ImportRoot, id)
	if err := os.MkdirAll(stage, 0700); err != nil {
		return ImportResult{}, err
	}
	archive := filepath.Join(stage, "backup.tar.gz")
	out, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ImportResult{}, err
	}
	if _, err = io.Copy(out, file); err != nil {
		out.Close()
		return ImportResult{}, err
	}
	out.Close()
	if err = extractArchiveContext(ctx, archive, stage); err != nil {
		return ImportResult{}, err
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
		if err := txn.cleanupDatabases(cfg); err != nil {
			log.Printf("defer cpmove database recovery for transaction %s: %v", txn.ID, err)
			return
		}
		_ = txn.Rollback()
		if txn.HadExisting {
			_ = siteHelperContext(context.Background(), cfg, "seal", user)
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
			return ImportResult{}, fmt.Errorf("malware scan failed: %w", scanErr)
		} else if len(findings) > 0 {
			return ImportResult{}, fmt.Errorf("restore blocked: malware scan detected %d suspicious PHP file(s)", len(findings))
		}
		if err = copyTreeContext(ctx, source, managerStage); err != nil {
			return ImportResult{}, err
		}
	}
	if _, err := manager.ActivateStaged(ctx, user, managerStage); err != nil {
		return ImportResult{}, fmt.Errorf("activate cpmove site through manager: %w", err)
	}
	activated = true

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
		return false, nil, nil
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
	}
	mailboxes := []string{}
	mailRoot := filepath.Join(root, "mail")
	_ = filepath.Walk(mailRoot, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() && path != mailRoot {
			rel, relErr := filepath.Rel(mailRoot, path)
			if relErr == nil && strings.Count(filepath.ToSlash(rel), "/") == 1 {
				mailboxes = append(mailboxes, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	sort.Strings(mailboxes)
	return true, mailboxes, nil
}

func extractArchive(archive, destination string) error {
	return extractArchiveContext(context.Background(), archive, destination)
}

func extractArchiveContext(ctx context.Context, archive, destination string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
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
		target := filepath.Join(destination, filepath.Clean(h.Name))
		if !strings.HasPrefix(target, filepath.Clean(destination)+string(os.PathSeparator)) {
			return errors.New("archive escapes staging directory")
		}
		if samePath(target, archive) {
			return errors.New("archive entry conflicts with staged upload")
		}
		if h.FileInfo().IsDir() {
			if err = os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return fmt.Errorf("unsupported archive entry type: %s", h.Name)
		}
		if h.Size < 0 || h.Size > 2<<30 || total+h.Size > 20<<30 {
			return errors.New("archive contents exceed the 20 GiB extraction limit")
		}
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		written, copyErr := io.Copy(dst, io.LimitReader(tr, h.Size))
		dst.Close()
		if copyErr != nil {
			return copyErr
		}
		if written != h.Size {
			return errors.New("archive entry is truncated")
		}
		total += written
	}
}
func restoreSQL(cfg Config, stage, user string, txn *SiteTransaction) ([]string, []string) {
	return restoreSQLContext(context.Background(), cfg, stage, user, txn)
}

func restoreSQLContext(parent context.Context, cfg Config, stage, user string, txn *SiteTransaction) ([]string, []string) {
	matches := sqlDumps(filepath.Join(stage, "mysql"))
	restored, failures := []string{}, []string{}
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
			input, openErr := os.Open(dump)
			if openErr != nil {
				failures = append(failures, name+": open failed: "+openErr.Error())
				cancel()
				continue
			}
			cmd := helperCommandContext(ctx, cfg, cfg.DBCtl, "restore", name, user)
			cmd.Stdin = input
			output, restoreErr := runBoundedCommand(ctx, cmd)
			_ = input.Close()
			cancel()
			if restoreErr != nil {
				failures = append(failures, name+": restore failed: "+strings.TrimSpace(string(output)))
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
		input.Close()
		cancel()
		if err != nil {
			failures = append(failures, name+": import failed: "+strings.TrimSpace(string(output)))
			if cleanupErr := dropDatabase(cfg, name); cleanupErr != nil {
				failures = append(failures, name+": cleanup failed: "+cleanupErr.Error())
			}
			continue
		}
		restored = append(restored, name)
	}
	return restored, failures
}

func dropDatabase(cfg Config, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if cfg.DBCtl != "" {
		output, err := runBoundedCommand(ctx, helperCommandContext(ctx, cfg, cfg.DBCtl, "drop", name))
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

func sqlDumps(root string) []string {
	matches := []string{}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".sql") {
			matches = append(matches, path)
		}
		return nil
	})
	sort.Strings(matches)
	return matches
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

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	return errA == nil && errB == nil && aa == bb
}
