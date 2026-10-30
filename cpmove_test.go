package stepanel

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestVerifyCPMoveUploadRejectsSameSizeMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upload.tar.gz")
	original := []byte("original archive bytes")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	checksum, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	upload := cpmoveUpload{Path: path, Size: int64(len(original)), SHA256: checksum}
	if err := verifyCPMoveUpload(upload); err != nil {
		t.Fatalf("initial upload verification failed: %v", err)
	}
	if err := os.WriteFile(path, []byte("original archive bytez"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyCPMoveUpload(upload); err == nil {
		t.Fatal("same-size archive mutation was accepted")
	}
}

func TestReadCPMoveUploadRejectsPathTraversalID(t *testing.T) {
	if _, err := readCPMoveUpload(t.TempDir(), "../outside"); err == nil {
		t.Fatal("path traversal upload ID was accepted")
	}
}

func TestReadCPMoveUploadRejectsOversizedMetadata(t *testing.T) {
	root := t.TempDir()
	const id = "upload-metadata"
	file, err := os.Create(cpmoveUploadMetadataPath(root, id))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxCPMoveUploadMetadataBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readCPMoveUpload(root, id); !errors.Is(err, errStateFileTooLarge) {
		t.Fatalf("oversized upload metadata error = %v, want errStateFileTooLarge", err)
	}
}

func TestReadCPMoveUploadAcceptsURLSafeBase64LeadingCharacters(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"-abc_def", "_abc-def"} {
		t.Run(id, func(t *testing.T) {
			upload := cpmoveUpload{
				ID:        id,
				Path:      cpmoveUploadPath(root, id),
				Filename:  "cpmove-account.tar.gz",
				Size:      0,
				SHA256:    strings.Repeat("0", 64),
				Owner:     "admin",
				ExpiresAt: time.Now().Add(time.Hour),
			}
			metadata, err := json.Marshal(upload)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cpmoveUploadMetadataPath(root, id), metadata, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readCPMoveUpload(root, id); err != nil {
				t.Fatalf("readCPMoveUpload rejected URL-safe upload ID %q: %v", id, err)
			}
		})
	}
}

func TestRestoreCPMoveContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RestoreCPMoveContext(ctx, Config{}, nil, nil, AuthorizedSite{site: "account"}, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RestoreCPMoveContext error = %v, want context.Canceled", err)
	}
}

func TestSafeArchivePath(t *testing.T) {
	valid := []string{"homedir/public_html/index.php", "mysql/site.sql", "homedir/.well-known/acme-challenge/token"}
	for _, path := range valid {
		if !safeArchivePath(path) {
			t.Errorf("expected archive path to be valid: %q", path)
		}
	}
	invalid := []string{"/etc/passwd", "../../etc/passwd", "homedir/../../etc/passwd", "..", `homedir\\..\\etc\\passwd`}
	for _, path := range invalid {
		if safeArchivePath(path) {
			t.Errorf("expected archive path to be rejected: %q", path)
		}
	}
}

func FuzzSafeArchivePath(f *testing.F) {
	for _, seed := range []string{
		"homedir/public_html/index.php",
		"mysql/site.sql",
		"../etc/passwd",
		"/etc/shadow",
		`homedir\\..\\etc\\passwd`,
		"a/./b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if !safeArchivePath(name) {
			return
		}
		clean := filepath.Clean(name)
		if filepath.IsAbs(name) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || strings.Contains(name, "\\") {
			t.Fatalf("accepted unsafe archive path %q (clean %q)", name, clean)
		}
	})
}

func TestSafeUser(t *testing.T) {
	for _, value := range []string{"stephen", "site_01", "site-name"} {
		if safeUser(value) != value {
			t.Errorf("expected username %q to be accepted", value)
		}
	}
	for _, value := range []string{"Stephen", "site name", "site/../../etc", ""} {
		if safeUser(value) != "" {
			t.Errorf("expected username %q to be rejected", value)
		}
	}
}

func TestRestoreMailStagesMailboxData(t *testing.T) {
	stage := t.TempDir()
	mailRoot := t.TempDir()
	source := filepath.Join(stage, "homedir", "mail", "example.com", "alice", "cur")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "message"), []byte("mail"), 0600); err != nil {
		t.Fatal(err)
	}
	staged, mailboxes, failures := restoreMail(Config{MailRoot: mailRoot}, stage, "account")
	if !staged || len(failures) != 0 {
		t.Fatalf("restoreMail() = staged %v, failures %v", staged, failures)
	}
	if len(mailboxes) != 1 || mailboxes[0] != "example.com/alice" {
		t.Fatalf("mailboxes = %v, want [example.com/alice]", mailboxes)
	}
	if _, err := os.Stat(filepath.Join(mailRoot, "account", "mail", "example.com", "alice", "cur", "message")); err != nil {
		t.Fatalf("staged message missing: %v", err)
	}
}

func TestRestoreMailDoesNotTreatDiscoveryErrorAsMissingMail(t *testing.T) {
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(stage, "homedir"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	staged, mailboxes, failures := restoreMail(Config{MailRoot: t.TempDir()}, stage, "account")
	if staged || len(mailboxes) != 0 || len(failures) != 1 || !strings.Contains(failures[0], "inspect staged mail") {
		t.Fatalf("restoreMail() = staged %v, mailboxes %v, failures %v", staged, mailboxes, failures)
	}
}

func TestInspectCPMoveHandlesTopLevelCPanelRoot(t *testing.T) {
	archive := makeTarGz(t, map[string]string{
		"cpmove-account/homedir/public_html/index.php":              "ok",
		"cpmove-account/homedir/mail/example.com/alice/cur/message": "mail",
		"cpmove-account/mysql/account_blog.sql":                     "create table posts(id int);",
	})
	file, header := openMultipartArchive(t, archive, "cpmove-account.tar.gz")
	defer file.Close()
	info, err := InspectCPMove(file, header)
	if err != nil {
		t.Fatal(err)
	}
	if info.User != "account" || !info.HasHome || !info.HasMySQL || !info.HasMail {
		t.Fatalf("unexpected inspection result: %+v", info)
	}
	if info.ArchiveBytes <= 0 || info.ExpandedBytes != int64(len("ok")+len("mail")+len("create table posts(id int);")) {
		t.Fatalf("size estimates = archive %d expanded %d", info.ArchiveBytes, info.ExpandedBytes)
	}
	if len(info.Databases) != 1 || info.Databases[0] != "account_blog" {
		t.Fatalf("databases = %v, want [account_blog]", info.Databases)
	}
	if len(info.Mailboxes) != 1 || info.Mailboxes[0] != "example.com/alice" {
		t.Fatalf("mailboxes = %v, want [example.com/alice]", info.Mailboxes)
	}
}

func TestCPMovePreflightReportsDomainsDatabaseAndMailboxEvidence(t *testing.T) {
	archive := makeTarGz(t, map[string]string{
		"cpmove-alice/userdata/main":                            sampleUserdataMain,
		"cpmove-alice/userdata/alice.example.com":               "documentroot: /home/alice/public_html\n",
		"cpmove-alice/userdata/shop.alice.example.com":          "documentroot: /home/alice/public_html/shop\n",
		"cpmove-alice/homedir/public_html/index.php":            "<?php",
		"cpmove-alice/mysql/alice_blog.sql":                     "-- Server version 10.11.6-MariaDB\nCREATE TABLE posts (id INT);\n",
		"cpmove-alice/homedir/mail/example.com/alice/cur/msg-1": "message",
		"cpmove-alice/homedir/mail/example.com/alice/new/msg-2": "message",
		"cpmove-alice/homedir/mail/example.com/alice/tmp/.keep": "",
	})
	file, header := openMultipartArchive(t, archive, "cpmove-alice.tar.gz")
	defer file.Close()
	info, err := InspectCPMove(file, header)
	if err != nil {
		t.Fatal(err)
	}
	if info.DomainInventoryStatus != "complete" || len(info.DomainMappings) < 3 {
		t.Fatalf("domain inventory = %q, %d mappings: %+v", info.DomainInventoryStatus, len(info.DomainMappings), info.DomainMappings)
	}
	if len(info.DatabaseVersions) != 1 || info.DatabaseVersions[0].SourceFamily != "MariaDB" || info.DatabaseVersions[0].SourceVersion != "10.11.6-MariaDB" {
		t.Fatalf("database version evidence = %+v", info.DatabaseVersions)
	}
	if len(info.MailboxValidation) != 1 || info.MailboxValidation[0].Status != "valid" || info.MailboxValidation[0].MessageFiles != 2 {
		t.Fatalf("mailbox evidence = %+v", info.MailboxValidation)
	}
	preview := info.response("upload-1", Config{DBEngine: "mariadb", DBVersion: "10.11.6"})
	if !preview.SourceScanRecommended || len(preview.DNSCutoverChecklist) == 0 || preview.DatabaseVersions[0].Status != "passed" {
		t.Fatalf("preflight response = %+v", preview)
	}
}

func TestCPMovePostMigrationChecksDistinguishStagingFromVerification(t *testing.T) {
	info := CPMoveInfo{DomainInventoryStatus: "complete"}
	result := ImportResult{FilesRestored: true, DatabaseRestoreRequested: true, DatabasesRestoredTotal: 1, MailStaged: true}
	checks := cpmovePostMigrationChecks(info, result)
	statuses := map[string]string{}
	for _, check := range checks {
		statuses[check.Name] = check.Status
	}
	if statuses["Database restore"] != "passed" || statuses["Domain routes"] != "warning" || statuses["Mailbox delivery"] != "warning" || statuses["Application rehearsal"] != "unknown" {
		t.Fatalf("post-migration statuses = %#v", statuses)
	}
	result.DatabaseErrorCount = 1
	checks = cpmovePostMigrationChecks(info, result)
	for _, check := range checks {
		if check.Name == "Database restore" && check.Status != "blocked" {
			t.Fatalf("database failure status = %+v", check)
		}
	}
}

func TestInspectCPMoveEscapesArchiveDisplayName(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"cpmove-account/homedir/public_html/index.php": "ok"})
	file, header := openMultipartArchive(t, archive, "<script>alert(1)</script>.tar.gz")
	defer file.Close()
	info, err := InspectCPMove(file, header)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(info.Archive, "<script>") || !strings.Contains(info.Archive, "&lt;script&gt;") {
		t.Fatalf("archive display name was not escaped: %q", info.Archive)
	}
}

func TestRestoreCPMoveHandlesTopLevelCPanelRoot(t *testing.T) {
	root := t.TempDir()
	archive := makeTarGz(t, map[string]string{
		"cpmove-account/homedir/public_html/index.html": "restored",
	})
	file, header := openMultipartArchive(t, archive, "cpmove-account.tar.gz")
	defer file.Close()
	result, err := RestoreCPMove(Config{ImportRoot: filepath.Join(root, "imports"), WebRoot: root, MailRoot: filepath.Join(root, "mail"), RecoveryRoot: filepath.Join(root, "sites", ".stepanel-recovery"), MaxEntries: 1000}, file, header, AuthorizedSite{site: "account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.FilesRestored {
		t.Fatal("FilesRestored = false, want true")
	}
	body, err := os.ReadFile(filepath.Join(root, "sites", "account", "public", "index.html"))
	if err != nil || string(body) != "restored" {
		t.Fatalf("restored file = %q, %v", body, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "sites"))
	if err != nil {
		t.Fatalf("read site root: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stepanel-cpmove-") {
			t.Fatalf("manager staging tree was not consumed: %s", entry.Name())
		}
	}
}

func TestRestoreCPMoveProcessDeathAfterActivationIsRecoverable(t *testing.T) {
	if os.Getenv("STEPANEL_CPMOVE_KILL_CHILD") == "1" {
		root := os.Getenv("STEPANEL_CPMOVE_KILL_ROOT")
		archive := makeTarGz(t, map[string]string{
			"cpmove-account/homedir/public_html/index.html": "interrupted",
		})
		file, header := openMultipartArchive(t, archive, "cpmove-account.tar.gz")
		defer file.Close()
		t.Setenv("STEPANEL_KILL_AT", "cpmove:activate")
		_, _ = RestoreCPMove(Config{
			ImportRoot:   filepath.Join(root, "imports"),
			WebRoot:      root,
			RecoveryRoot: filepath.Join(root, "recovery"),
		}, file, header, AuthorizedSite{site: "account"}, false)
		t.Fatal("cpmove process survived injected SIGKILL")
	}

	root := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=TestRestoreCPMoveProcessDeathAfterActivationIsRecoverable", "-test.v")
	child.Env = append(os.Environ(),
		"STEPANEL_CPMOVE_KILL_CHILD=1",
		"STEPANEL_CPMOVE_KILL_ROOT="+root,
	)
	if err := child.Run(); err == nil {
		t.Fatal("child process unexpectedly survived cpmove SIGKILL")
	} else if exitErr, ok := err.(*exec.ExitError); !ok {
		t.Fatalf("child exit = %v", err)
	} else if status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child exit = %v, want SIGKILL", err)
	}

	recovered, err := RecoverSiteTransactions(filepath.Join(root, "recovery"), root, "")
	if err != nil {
		t.Fatalf("recover interrupted cpmove: %v", err)
	}
	if len(recovered) != 1 {
		t.Fatalf("recovered transactions = %v, want one transaction", recovered)
	}
	if _, err := os.Stat(filepath.Join(root, "sites", "account", "public")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted cpmove site = %v, want absent after rollback", err)
	}
}

func TestSQLDumpsFindsNestedCPanelDumps(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"mysql/account_blog.sql", "mysql/nested/account_shop.sql"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("-- dump"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	matches, err := sqlDumps(filepath.Join(root, "mysql"))
	if err != nil {
		t.Fatalf("sqlDumps: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("sqlDumps found %d files, want 2: %v", len(matches), matches)
	}
	if got := databaseName("account", "account_blog"); got != "account_blog" {
		t.Fatalf("databaseName duplicated user prefix: %q", got)
	}
	if got := databaseName("my-account", "my-account_blog"); got != "my_account_blog" {
		t.Fatalf("databaseName did not normalize a hyphenated account: %q", got)
	}
}

func TestCPMoveDatabaseNameValidatesNormalizedIdentifier(t *testing.T) {
	malformedUTF8 := string([]byte{0xff, 0xfe})
	for _, invalid := range []string{
		"bad`name",
		"name;DROP DATABASE other",
		"db-name;--",
		" leading-space",
		"données",
		malformedUTF8,
		strings.Repeat("x", 65),
	} {
		if name, err := cpmoveDatabaseName("account", invalid, 64); err == nil {
			t.Errorf("cPanel database name %q accepted as %q", invalid, name)
		}
		if quoted, err := managedDatabaseSQLIdentifier(invalid, 64); err == nil {
			t.Errorf("managed SQL identifier %q accepted as %q", invalid, quoted)
		}
	}
	if _, err := cpmoveDatabaseName("account", strings.Repeat("x", 33), 64); err == nil {
		t.Fatal("source database component exceeding the cPanel name limit was accepted")
	}

	maxName, err := cpmoveDatabaseName(strings.Repeat("a", 32), strings.Repeat("b", 31), 64)
	if err != nil {
		t.Fatalf("maximum supported combined identifier was rejected: %v", err)
	}
	if len(maxName) != 64 {
		t.Fatalf("maximum cPanel identifier length = %d, want 64", len(maxName))
	}
	if _, err := managedDatabaseSQLIdentifier(maxName, 64); err != nil {
		t.Fatalf("maximum supported SQL identifier was rejected: %v", err)
	}
	if _, err := cpmoveDatabaseName(strings.Repeat("a", 32), strings.Repeat("b", 32), 64); err == nil {
		t.Fatal("identifier longer than the engine maximum was accepted")
	}
}

func TestSQLDumpsPropagatesWalkErrors(t *testing.T) {
	if _, err := sqlDumps(filepath.Join(t.TempDir(), "missing-mysql")); err == nil {
		t.Fatal("sqlDumps swallowed a directory traversal error")
	}
}

func TestRestoreSQLDropsPartiallyImportedDatabase(t *testing.T) {
	root := t.TempDir()
	dumpRoot := filepath.Join(root, "mysql")
	if err := os.MkdirAll(dumpRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dumpRoot, "blog.sql"), []byte("broken import"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "mysql.log")
	script := "#!/bin/sh\nIFS= read -r query || query=''\nprintf 'ARGS: %s\\nSQL: %s\\n' \"$*\" \"$query\" >> \"$TEST_MYSQL_LOG\"\ncase \"$query\" in *'SELECT COUNT(*) FROM information_schema.SCHEMATA'*) printf '0\\n'; exit 0;; *'CREATE DATABASE'*) exit 0;; *'DROP DATABASE'*) exit 0;; esac\nexit 1\n"
	for _, name := range []string{"mysql", "mariadb"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_MYSQL_LOG", logPath)
	restored, failures := restoreSQL(Config{}, root, "account", nil)
	if len(restored) != 0 || len(failures) == 0 {
		t.Fatalf("restoreSQL() = restored %v, failures %v", restored, failures)
	}
	logBody, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logBody), "CREATE DATABASE `account_blog`") || !strings.Contains(string(logBody), "DROP DATABASE IF EXISTS `account_blog`") {
		t.Fatalf("database create/cleanup commands were not both issued: %s", logBody)
	}
}

func TestUserFromArchiveName(t *testing.T) {
	cases := map[string]string{
		"cpmove-account.tar.gz":                    "account",
		"backup-8.26.2026_09-15-00_account.tar.gz": "account",
		"backup-account.tar.gz":                    "",
	}
	for name, want := range cases {
		if got := userFromArchiveName(name); got != want {
			t.Fatalf("userFromArchiveName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestExtractArchiveRejectsStagedUploadOverwrite(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "backup.tar.gz")
	if err := writeTarGz(archive, map[string]string{"backup.tar.gz": "overwrite"}); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(archive, root); err == nil || !strings.Contains(err.Error(), "conflicts with staged upload") {
		t.Fatalf("extractArchive error = %v, want staged upload conflict", err)
	}
}

func TestExtractArchiveRejectsTraversalAndLinkEntries(t *testing.T) {
	tests := []struct {
		name   string
		header tar.Header
		want   string
	}{
		{name: "parent traversal", header: tar.Header{Name: "../outside", Mode: 0600, Size: 1}, want: "unsafe archive path"},
		{name: "absolute path", header: tar.Header{Name: "/etc/shadow", Mode: 0600, Size: 1}, want: "unsafe archive path"},
		{name: "symlink", header: tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/shadow"}, want: "unsupported archive entry type"},
		{name: "hardlink", header: tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "target"}, want: "unsupported archive entry type"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "backup.tar.gz")
			if err := writeTarHeaders(archive, []tar.Header{tc.header}, map[string][]byte{tc.header.Name: []byte("x")}); err != nil {
				t.Fatal(err)
			}
			err := extractArchive(archive, root)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("extractArchive() = %v, want %q", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(root, "outside")); !os.IsNotExist(err) {
				t.Fatalf("unexpected extraction outside staging root: %v", err)
			}
		})
	}
}

func TestRestoreFailureRestoresExistingSite(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "sites", "account", "public")
	if err := os.MkdirAll(home, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "marker.txt"), []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "backup.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gz)
	body := []byte("<?php system($_GET['cmd']);")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "homedir/public_html/shell.php", Mode: 0600, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		t.Fatal(err)
	}
	_, restoreErr := RestoreCPMove(Config{ImportRoot: filepath.Join(root, "imports"), WebRoot: root, MailRoot: filepath.Join(root, "mail"), RecoveryRoot: filepath.Join(root, "sites", ".stepanel-recovery")}, input, &multipart.FileHeader{Filename: "cpmove-account.tar.gz", Size: info.Size()}, AuthorizedSite{site: "account"}, false)
	if restoreErr == nil || !strings.Contains(restoreErr.Error(), "restore blocked") {
		t.Fatalf("restore error = %v", restoreErr)
	}
	marker, err := os.ReadFile(filepath.Join(home, "marker.txt"))
	if err != nil || string(marker) != "keep me" {
		t.Fatalf("existing site was not restored: %q, %v", marker, err)
	}
}

func makeTarGz(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := writeTarGz(path, entries); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTarGz(path string, entries map[string]string) error {
	headers := make([]tar.Header, 0, len(entries))
	bodies := make(map[string][]byte, len(entries))
	for name, content := range entries {
		body := []byte(content)
		headers = append(headers, tar.Header{Name: name, Mode: 0600, Size: int64(len(body))})
		bodies[name] = body
	}
	return writeTarHeaders(path, headers, bodies)
}

func writeTarHeaders(path string, headers []tar.Header, bodies map[string][]byte) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gz)
	for _, header := range headers {
		if err := tarWriter.WriteHeader(&header); err != nil {
			_ = file.Close()
			return err
		}
		body := bodies[header.Name]
		if header.Typeflag != tar.TypeSymlink && header.Typeflag != tar.TypeLink && len(body) > 0 {
			if _, err := tarWriter.Write(body); err != nil {
				_ = file.Close()
				return err
			}
		}
		if header.Size > int64(len(body)) && header.Typeflag != tar.TypeSymlink && header.Typeflag != tar.TypeLink {
			_ = file.Close()
			return io.ErrShortWrite
		}
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			continue
		}
		if header.Size < int64(len(body)) {
			_ = file.Close()
			return io.ErrShortWrite
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = file.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func openMultipartArchive(t *testing.T, path, name string) (multipart.File, *multipart.FileHeader) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	return multipartFile{File: file}, &multipart.FileHeader{Filename: name, Size: info.Size()}
}

type multipartFile struct {
	*os.File
}

func (f multipartFile) ReadAt(p []byte, off int64) (int, error) {
	return f.File.ReadAt(p, off)
}

func (f multipartFile) Seek(offset int64, whence int) (int64, error) {
	return f.File.Seek(offset, whence)
}

var _ io.ReaderAt = multipartFile{}

// A tar that names a file twice could have one copy inspected and the other
// restored. Inspection and extraction both reject it.
func TestCPMoveRejectsDuplicateAndCollidingEntries(t *testing.T) {
	for name, headers := range map[string][]tar.Header{
		"duplicate file":     {{Name: "cpmove-account/homedir/public_html/index.php", Mode: 0o600, Size: 1}, {Name: "cpmove-account/homedir/public_html/index.php", Mode: 0o600, Size: 1}},
		"aliased duplicate":  {{Name: "cpmove-account/mysql/db.sql", Mode: 0o600, Size: 1}, {Name: "cpmove-account/mysql//./db.sql", Mode: 0o600, Size: 1}},
		"entry under a file": {{Name: "cpmove-account/homedir", Mode: 0o600, Size: 1}, {Name: "cpmove-account/homedir/x", Mode: 0o600, Size: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "cpmove-account.tar.gz")
			bodies := map[string][]byte{}
			for _, header := range headers {
				bodies[header.Name] = []byte("x")
			}
			if err := writeTarHeaders(archive, headers, bodies); err != nil {
				t.Fatal(err)
			}
			file, header := openMultipartArchive(t, archive, "cpmove-account.tar.gz")
			defer file.Close()
			if _, err := InspectCPMove(file, header); err == nil {
				t.Error("inspection accepted the archive")
			}
			stage := filepath.Join(root, "stage")
			if err := os.Mkdir(stage, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := extractArchive(archive, stage); err == nil {
				t.Error("extraction accepted the archive")
			}
		})
	}
}
