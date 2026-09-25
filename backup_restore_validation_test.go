package main

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
