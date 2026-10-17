package stepanel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreDatabaseIntoStagingContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := restoreDatabaseIntoStagingContext(ctx, Config{}, "", RestoreToStagingRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("restoreDatabaseIntoStagingContext error = %v, want context.Canceled", err)
	}
}

func TestDatabaseRestoreFailureIdentifiesRetainedSafetyBackup(t *testing.T) {
	importErr := errors.New("database import failed: no space left on device")
	root := t.TempDir()
	safety := BackupResult{Path: filepath.Join(root, "site", "backup-20261016T023000Z")}

	err := annotateDatabaseRestoreFailure(importErr, safety)
	if !errors.Is(err, importErr) {
		t.Fatalf("annotated restore error = %v; want to preserve original cause", err)
	}
	if !strings.Contains(err.Error(), "pre-restore safety backup was retained as backup-20261016T023000Z") {
		t.Fatalf("restore failure does not identify retained safety artifact: %v", err)
	}
	if strings.Contains(err.Error(), root) {
		t.Fatalf("restore failure leaked an absolute backup path: %v", err)
	}
}

func TestRestoreDatabaseIntoStagingContextInjectsProvisionFailure(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	if err := os.MkdirAll(filepath.Join(stage, "databases"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "databases", "source.sql"), []byte("CREATE TABLE test (id INT);"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEPANEL_FAIL_AT", "restore:provision")

	_, err := restoreDatabaseIntoStagingContext(context.Background(), Config{DBCtl: filepath.Join(root, "unused-helper")}, stage, RestoreToStagingRequest{
		Database:       "source",
		TargetDatabase: "target",
		TargetUser:     "targetuser",
		TargetPassword: "secure-password-1234567890",
		Site:           "example",
	})
	if err == nil || !strings.Contains(err.Error(), "failure injection") {
		t.Fatalf("database restore error = %v, want provision failure injection", err)
	}
}

func TestValidateRestoreDatabaseInputEmpty(t *testing.T) {
	cfg := Config{DBCtl: "/usr/local/sbin/stepanel-dbctl"}
	manifest := BackupManifest{}

	errs := ValidateRestoreDatabaseInput(cfg, RestoreDatabaseInput{}, manifest)
	if len(errs) > 0 {
		t.Fatalf("empty input should be valid (no restore requested), got: %v", errs)
	}
}

func TestValidateRestoreDatabaseInputMissingFields(t *testing.T) {
	cfg := Config{DBCtl: "/usr/local/sbin/stepanel-dbctl"}
	manifest := BackupManifest{}

	// Only database name provided, others missing
	errs := ValidateRestoreDatabaseInput(cfg, RestoreDatabaseInput{
		Database: "test_db",
	}, manifest)

	if len(errs) == 0 {
		t.Fatal("should require all fields when any are provided")
	}

	// Should complain about missing fields
	foundMissing := false
	for _, err := range errs {
		if err == "target_database name is required" {
			foundMissing = true
			break
		}
	}
	if !foundMissing {
		t.Fatalf("should complain about missing target_database, got: %v", errs)
	}
}

func TestValidateRestoreDatabaseInputNoDatabaseCtl(t *testing.T) {
	cfg := Config{DBCtl: ""} // Database helper not configured
	manifest := BackupManifest{}

	errs := ValidateRestoreDatabaseInput(cfg, RestoreDatabaseInput{
		Database:       "test_db",
		TargetDatabase: "new_db",
		TargetUser:     "newuser",
		TargetPassword: "SecurePass123MoreSecure1234567890",
	}, manifest)

	if len(errs) == 0 {
		t.Fatal("should fail when DBCtl is not configured")
	}

	foundError := false
	for _, err := range errs {
		if err == "database restore is not available on this system" {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Fatalf("should complain about missing DBCtl, got: %v", errs)
	}
}

func TestValidateRestoreDatabaseInputInvalidTargetUser(t *testing.T) {
	cfg := Config{DBCtl: "/usr/local/sbin/stepanel-dbctl"}
	manifest := BackupManifest{
		Databases: []string{"test_db"},
	}

	tests := []struct {
		name      string
		user      string
		expectErr bool
	}{
		{"valid lowercase", "myuser", false},
		{"starts with uppercase", "MyUser", true},
		{"starts with number", "1user", true},
		{"empty string", "", true},
		{"too long", "abcdefghijklmnopqrstuvwxyz1234567", true}, // 34 chars total
	}

	for _, test := range tests {
		errs := ValidateRestoreDatabaseInput(cfg, RestoreDatabaseInput{
			Database:       "test_db",
			TargetDatabase: "new_db",
			TargetUser:     test.user,
			TargetPassword: "SecurePass123MoreSecure1234567890",
		}, manifest)

		if test.expectErr && len(errs) == 0 {
			t.Errorf("test %q: expected error for user %q, got none", test.name, test.user)
		}
		if !test.expectErr && len(errs) > 0 {
			t.Errorf("test %q: unexpected errors for user %q: %v", test.name, test.user, errs)
		}
	}
}

func TestValidateRestoreDatabaseInputValidInput(t *testing.T) {
	cfg := Config{DBCtl: "/usr/local/sbin/stepanel-dbctl"}
	manifest := BackupManifest{
		Databases: []string{"mydb"},
	}

	errs := ValidateRestoreDatabaseInput(cfg, RestoreDatabaseInput{
		Database:       "mydb",
		TargetDatabase: "newdb",
		TargetUser:     "newuser",
		TargetPassword: "SecurePass123MoreSecure1234567890",
	}, manifest)

	if len(errs) > 0 {
		t.Errorf("valid input failed validation: %v", errs)
	}
}

func TestValidateRestoreStagingInput(t *testing.T) {
	tests := []struct {
		name   string
		input  RestoreToStagingRequest
		hasErr bool
	}{
		{
			name: "valid input",
			input: RestoreToStagingRequest{
				Site:   "mysite",
				Backup: "20260914-backup",
				Domain: "example.com",
			},
			hasErr: false,
		},
		{
			name: "missing site",
			input: RestoreToStagingRequest{
				Backup: "20260914-backup",
				Domain: "example.com",
			},
			hasErr: true,
		},
		{
			name: "missing backup",
			input: RestoreToStagingRequest{
				Site:   "mysite",
				Domain: "example.com",
			},
			hasErr: true,
		},
		{
			name: "invalid domain",
			input: RestoreToStagingRequest{
				Site:   "mysite",
				Backup: "20260914-backup",
				Domain: "not a valid domain!!!",
			},
			hasErr: true,
		},
	}

	for _, test := range tests {
		errs := ValidateRestoreStagingInput(test.input)
		if test.hasErr && len(errs) == 0 {
			t.Errorf("test %q: expected error, got none", test.name)
		}
		if !test.hasErr && len(errs) > 0 {
			t.Errorf("test %q: unexpected error: %v", test.name, errs)
		}
	}
}

func TestManagedDatabaseRestoreMarksLoadStartAndRefusesRetryAfterLoadFailure(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	writeTestFile(t, filepath.Join(webRoot, "sites", "account", "public", "index.html"), "site")
	loads := filepath.Join(root, "loads")
	helper := filepath.Join(root, "dbctl")
	script := "#!/bin/sh\ncase \"$1\" in\n  list) printf 'account_blog\\tcpmove:account\\n';;\n  dump) printf '%s\\n' 'CREATE TABLE posts (id INT);';;\n  restore-dump) cat >/dev/null; echo load >> \"" + loads + "\"; echo 'ERROR 3 (HY000): Error writing file (Errcode: 28 \"No space left on device\")' >&2; exit 1;;\n  *) exit 1;;\nesac\n"
	if err := os.WriteFile(helper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{WebRoot: webRoot, BackupRoot: filepath.Join(root, "backups"), ImportRoot: filepath.Join(root, "imports"), DBCtl: helper}
	backup, err := CreateSiteBackup(cfg, AuthorizedSite{site: "account"}, true)
	if err != nil {
		t.Fatal(err)
	}
	backupName := filepath.Base(backup.Path)

	// A failure before the load leaves the database untouched and is retryable.
	refused := errors.New("marker write failed")
	_, err = restoreManagedDatabase(t.Context(), cfg, backupName, "account", "account_blog", func() error { return refused })
	if !errors.Is(err, refused) || isNonRetryableJobError(err) {
		t.Fatalf("pre-load failure = %v, want the retryable marker error", err)
	}
	if _, statErr := os.Stat(loads); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("load ran although the start marker could not be written: %v", statErr)
	}

	marked := false
	_, err = restoreManagedDatabase(t.Context(), cfg, backupName, "account", "account_blog", func() error { marked = true; return nil })
	if !marked {
		t.Fatal("load started without recording its start")
	}
	if err == nil || !isNonRetryableJobError(err) || !strings.Contains(err.Error(), "may be partially restored") {
		t.Fatalf("load failure = %v, want a non-retryable partial-restore error", err)
	}
	if data, readErr := os.ReadFile(loads); readErr != nil || strings.Count(string(data), "load") != 1 {
		t.Fatalf("helper load calls = %q, %v; want exactly one", data, readErr)
	}
}

func TestDatabaseRestoreMarkerRefusesReRunAndNamesFirstSafetyBackup(t *testing.T) {
	recoveryRoot := filepath.Join(t.TempDir(), "recovery")
	if err := checkInterruptedDatabaseRestore(recoveryRoot, "job-1"); err != nil {
		t.Fatalf("fresh job refused: %v", err)
	}
	request := durableBackupRestoreRequest{Site: "account", Database: "account_blog"}
	safety := BackupResult{Path: filepath.Join(t.TempDir(), "20261017-000000.000000000-account")}
	if err := markDatabaseRestoreStarted(recoveryRoot, "job-1", request, safety)(); err != nil {
		t.Fatal(err)
	}
	path, err := databaseRestoreMarkerPath(recoveryRoot, "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("marker = %v, %v; want a 0600 file", info, err)
	}
	err = checkInterruptedDatabaseRestore(recoveryRoot, "job-1")
	if err == nil || !isNonRetryableJobError(err) || !strings.Contains(err.Error(), "restore the pre-restore safety backup 20261017-000000.000000000-account") || strings.Contains(err.Error(), safety.Path) {
		t.Fatalf("re-run error = %v, want non-retryable error naming only the first safety backup", err)
	}
	if err := checkInterruptedDatabaseRestore(recoveryRoot, "job-2"); err != nil {
		t.Fatalf("a different job was refused by job-1's marker: %v", err)
	}
	if err := clearDatabaseRestoreMarker(recoveryRoot, "job-1"); err != nil {
		t.Fatal(err)
	}
	if err := checkInterruptedDatabaseRestore(recoveryRoot, "job-1"); err != nil {
		t.Fatalf("cleared marker still refuses: %v", err)
	}
	if err := clearDatabaseRestoreMarker(recoveryRoot, "job-1"); err != nil {
		t.Fatalf("clearing an absent marker: %v", err)
	}

	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkInterruptedDatabaseRestore(recoveryRoot, "job-1"); err == nil || !isNonRetryableJobError(err) {
		t.Fatalf("unreadable marker = %v, want non-retryable refusal", err)
	}
	for _, id := range []string{"", "../escape", "a/b", "..", strings.Repeat("a", 129)} {
		if _, err := databaseRestoreMarkerPath(recoveryRoot, id); err == nil {
			t.Fatalf("job ID %q was accepted", id)
		}
	}
}
