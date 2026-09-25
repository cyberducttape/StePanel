package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateDatabaseSafetyBackupContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := createDatabaseSafetyBackupContext(ctx, Config{}, "database")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("createDatabaseSafetyBackupContext error = %v, want context.Canceled", err)
	}
}

func TestRestoreSQLUsesRestrictedHelper(t *testing.T) {
	root := t.TempDir()
	dumpRoot := filepath.Join(root, "mysql")
	if err := os.MkdirAll(dumpRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dumpRoot, "blog.sql"), []byte("CREATE TABLE posts (id INT);"), 0600); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(root, "args")
	inputPath := filepath.Join(root, "input")
	helper := filepath.Join(root, "dbctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$TEST_ARGS\"\ncat > \"$TEST_INPUT\"\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_ARGS", argsPath)
	t.Setenv("TEST_INPUT", inputPath)
	restored, failures := restoreSQL(Config{DBCtl: helper}, root, "account", nil)
	if len(failures) != 0 || len(restored) != 1 || restored[0] != "account_blog" {
		t.Fatalf("restored = %#v, failures = %#v", restored, failures)
	}
	assertTestFile(t, argsPath, "restore account_blog account\n")
	assertTestFile(t, inputPath, "CREATE TABLE posts (id INT);")
}

func TestWPressDatabaseHelperStreamsPasswordAndDump(t *testing.T) {
	root := t.TempDir()
	dump := filepath.Join(root, "database.sql")
	if err := os.WriteFile(dump, []byte("SELECT 1;"), 0600); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(root, "args")
	inputPath := filepath.Join(root, "input")
	helper := filepath.Join(root, "dbctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$TEST_ARGS\"\ncat > \"$TEST_INPUT\"\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_ARGS", argsPath)
	t.Setenv("TEST_INPUT", inputPath)
	password := "Strong-Database_2026!"
	if err := restoreWPressDatabaseWithHelper(Config{DBCtl: helper}, "site", "site_wordpress", "site_wordpress", password, dump); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, argsPath, "restore-wordpress site_wordpress site_wordpress site\n")
	data, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != password+"\nSELECT 1;" {
		t.Fatalf("helper input = %q", data)
	}
}

func TestValidateWPressPasswordPolicy(t *testing.T) {
	if err := validateWPressInput("site", "wordpress", "wordpress", "short-password", "wp_", ""); err == nil {
		t.Fatal("short password was accepted")
	}
	if err := validateWPressInput("site", "wordpress", "wordpress", "Strong-Database_2026!", "wp_", ""); err != nil {
		t.Fatal(err)
	}
	if err := validateWPressInput("site", "wordpress", "wordpress", strings.Repeat("a", 129), "wp_", ""); err == nil {
		t.Fatal("oversized password was accepted")
	}
	if err := validateWPressInput("site", "WordPress", "wordpress", "Strong-Database_2026!", "wp_", ""); err == nil {
		t.Fatal("uppercase database suffix was accepted")
	}
}

func TestRunDatabaseHelperContextHonorsCancellation(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "dbctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nsleep 5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := runDatabaseHelperContext(ctx, Config{DBCtl: helper}, time.Minute, "", "provision")
	if err == nil {
		t.Fatal("cancelled database helper unexpectedly succeeded")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancelled database helper took %v", elapsed)
	}
}
