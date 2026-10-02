package rootbroker

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientConvenienceMethodsUseTheSocketBoundary(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	const calls = 19
	serverErr := make(chan error, 1)
	go func() {
		for i := 0; i < calls; i++ {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				serverErr <- acceptErr
				return
			}
			var request Request
			decodeErr := json.NewDecoder(conn).Decode(&request)
			if decodeErr == nil {
				decodeErr = json.NewEncoder(conn).Encode(Response{OK: true})
			}
			_ = conn.Close()
			if decodeErr != nil {
				serverErr <- decodeErr
				return
			}
		}
		serverErr <- nil
	}()
	t.Setenv("STEPANEL_ROOT_BROKER_SOCKET", socketPath)
	client, err := NewClient("unused", "/var/www")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	callsToMake := []func() error{
		func() error { _, e := client.SiteCreate(ctx, "site", ""); return e },
		func() error { _, e := client.SiteDelete(ctx, "site"); return e },
		func() error { _, e := client.AppApply(ctx, "site", "8.3", 8080); return e },
		func() error { _, e := client.AppOperation(ctx, AppRequest{Action: "start", Site: "site"}); return e },
		func() error { _, e := client.AppDelete(ctx, "site"); return e },
		func() error {
			_, e := client.DBProvision(ctx, "site", "db", "user", "utf8mb4", "long-test-password")
			return e
		},
		func() error {
			_, e := client.DBMutation(ctx, "rotate", "site", "db", "user", "utf8mb4", "long-test-password", nil)
			return e
		},
		func() error { _, e := client.DBInventory(ctx); return e },
		func() error { _, e := client.DBInventoryDirect(ctx); return e },
		func() error { _, e := client.DBDumpDirect(ctx, "db"); return e },
		func() error { _, e := client.DBRestoreDumpDirect(ctx, "site", "db", []byte("SELECT 1;")); return e },
		func() error { _, e := client.VhostApply(ctx, "site", "example.test", "caddy"); return e },
		func() error { _, e := client.IssueCertificate(ctx, "example.test", "admin@example.test"); return e },
		func() error { _, e := client.TaskKill(ctx, "site", "cron"); return e },
		func() error {
			_, e := client.TaskOperation(ctx, TaskRequest{Action: "history", Site: "site", Name: "cron"})
			return e
		},
		func() error {
			_, e := client.GitClone(ctx, "https://github.com/example/repo.git", "main", "/var/www/sites/site/.stepanel-release-1")
			return e
		},
		func() error { _, e := client.GitDelete(ctx, "site"); return e },
		func() error { _, e := client.GitPublic(ctx, "site"); return e },
	}
	for i, call := range callsToMake {
		if err := call(); err != nil {
			t.Fatalf("convenience call %d failed: %v", i, err)
		}
	}
	if _, err := client.GitGenerate(ctx, "site"); err != nil {
		t.Fatalf("GitGenerate failed: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestValidJobIDForJournalRejectsPathAmbiguity(t *testing.T) {
	for _, id := range []string{"", ".hidden", "..", "a..b", "a/b", `a\\b`, "a b", strings.Repeat("x", 129)} {
		if validJobIDForJournal(id) {
			t.Errorf("validJobIDForJournal(%q) = true", id)
		}
	}
	for _, id := range []string{"job-1", "job_2", "job:3", "job.4", "ABC123"} {
		if !validJobIDForJournal(id) {
			t.Errorf("validJobIDForJournal(%q) = false", id)
		}
	}
}

func TestCreationJournalPersistsReloadsAndCleansUp(t *testing.T) {
	root := t.TempDir()
	j, err := loadOrCreateCreationJournal(root, "job-1", "example", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if j.isComplete(stepPersisted) || j.hasAnyPersistence() {
		t.Fatal("fresh creation journal unexpectedly has completed state")
	}
	if err := j.markComplete(stepPersisted); err != nil {
		t.Fatal(err)
	}
	if !j.isComplete(stepPersisted) || !j.hasAnyPersistence() {
		t.Fatal("creation journal did not record persistence")
	}
	reloaded, err := loadOrCreateCreationJournal(root, "job-1", "example", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.isComplete(stepPersisted) {
		t.Fatal("creation journal did not reload completed state")
	}
	if err := reloaded.cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "site-creation-job-1.json")); !os.IsNotExist(err) {
		t.Fatalf("creation journal remains after cleanup: %v", err)
	}
	if err := reloaded.cleanup(); err != nil {
		t.Fatal("cleanup should tolerate a missing journal: ", err)
	}
	if (*creationJournal)(nil).isComplete(stepPersisted) || (*creationJournal)(nil).hasAnyPersistence() {
		t.Fatal("nil creation journal reported state")
	}
}

func TestDatabaseJournalPersistsAndRejectsMismatches(t *testing.T) {
	root := t.TempDir()
	j, err := loadOrCreateDatabaseJournal(root, "db-job", "example", "app_db", "app_user", "admin")
	if err != nil {
		t.Fatal(err)
	}
	j.setCredLocation("/var/lib/stepanel-privileged/db.json")
	if j.CredLocation == "" {
		t.Fatal("credential location was not recorded")
	}
	if err := j.markComplete(stepDBCreated); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadOrCreateDatabaseJournal(root, "db-job", "example", "app_db", "app_user", "admin")
	if err != nil || !reloaded.isComplete(stepDBCreated) {
		t.Fatalf("database journal reload = %#v, err %v", reloaded, err)
	}
	if _, err := loadOrCreateDatabaseJournal(root, "db-job", "other", "app_db", "app_user", "admin"); err == nil {
		t.Fatal("database journal accepted a site mismatch")
	}
	if err := reloaded.cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := (*databaseJournal)(nil).markComplete(stepDBCreated); err == nil {
		t.Fatal("nil database journal mark unexpectedly succeeded")
	}
	if err := (*databaseJournal)(nil).cleanup(); err != nil {
		t.Fatal("nil database journal cleanup: ", err)
	}
	(*databaseJournal)(nil).setCredLocation("ignored")
}

func TestRestorationJournalPersistsProgressAndRejectsCorruption(t *testing.T) {
	root := t.TempDir()
	j, err := loadOrCreateRestorationJournal(root, "restore-1", "example", "app_db", "/imports/db.sql", "admin")
	if err != nil {
		t.Fatal(err)
	}
	bytesImported, totalBytes := j.getProgress()
	if bytesImported != 0 || totalBytes != 0 {
		t.Fatalf("fresh restore progress = %d/%d", bytesImported, totalBytes)
	}
	j.updateProgress(12, 100)
	if err := j.markComplete(stepRestoreDumpImported); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadOrCreateRestorationJournal(root, "restore-1", "example", "app_db", "/imports/db.sql", "admin")
	if err != nil {
		t.Fatal(err)
	}
	bytesImported, totalBytes = reloaded.getProgress()
	if !reloaded.isComplete(stepRestoreDumpImported) || bytesImported != 12 || totalBytes != 100 {
		t.Fatalf("restoration journal reload = %#v progress=%d/%d", reloaded, bytesImported, totalBytes)
	}
	path, err := restorationJournalPath(root, "restore-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateRestorationJournal(root, "restore-1", "example", "app_db", "/imports/db.sql", "admin"); err == nil {
		t.Fatal("corrupt restoration journal was accepted")
	}
	if err := reloaded.cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := (*restorationJournal)(nil).markComplete(stepRestoreVerified); err == nil {
		t.Fatal("nil restoration journal mark unexpectedly succeeded")
	}
	if err := (*restorationJournal)(nil).cleanup(); err != nil {
		t.Fatal("nil restoration journal cleanup: ", err)
	}
	(*restorationJournal)(nil).updateProgress(1, 2)
	bytesImported, totalBytes = (*restorationJournal)(nil).getProgress()
	if bytesImported != 0 || totalBytes != 0 {
		t.Fatalf("nil restoration progress = %d/%d", bytesImported, totalBytes)
	}
}

func TestVhostJournalPersistsAndValidatesIdentity(t *testing.T) {
	root := t.TempDir()
	j, err := loadOrCreateVhostJournal(root, "vhost-1", "example", "example.test", "admin")
	if err != nil {
		t.Fatal(err)
	}
	j.setConfigPath("/etc/caddy/stepanel-sites/example.conf")
	if j.ConfigPath == "" || j.isComplete(stepVhostValidated) {
		t.Fatal("fresh vhost journal has unexpected state")
	}
	if err := j.markComplete(stepVhostValidated); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadOrCreateVhostJournal(root, "vhost-1", "example", "example.test", "admin")
	if err != nil || !reloaded.isComplete(stepVhostValidated) {
		t.Fatalf("vhost journal reload = %#v, err %v", reloaded, err)
	}
	if _, err := loadOrCreateVhostJournal(root, "vhost-1", "other", "example.test", "admin"); err == nil {
		t.Fatal("vhost journal accepted a site mismatch")
	}
	if err := reloaded.cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := (*vhostJournal)(nil).markComplete(stepVhostVerified); err == nil {
		t.Fatal("nil vhost journal mark unexpectedly succeeded")
	}
	if err := (*vhostJournal)(nil).cleanup(); err != nil {
		t.Fatal("nil vhost journal cleanup: ", err)
	}
	(*vhostJournal)(nil).setConfigPath("ignored")
}

func TestJournalPathsRejectEmptyOrUnsafeIDs(t *testing.T) {
	for _, fn := range []func(string, string) (string, error){
		creationJournalPath,
		databaseJournalPath,
		restorationJournalPath,
		vhostJournalPath,
	} {
		if _, err := fn("", "job"); err == nil {
			t.Fatal("journal path accepted an empty recovery root")
		}
		if _, err := fn(t.TempDir(), "../escape"); err == nil {
			t.Fatal("journal path accepted an unsafe job id")
		}
	}
}

func TestBrokerStreamsValidatedDumpPathWithoutEmbeddingDumpBytes(t *testing.T) {
	webRoot := t.TempDir()
	dumpPath := filepath.Join(webRoot, "dump.sql")
	if err := os.WriteFile(dumpPath, []byte("CREATE TABLE streamed (id INT);\n"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "received.sql")
	helper := filepath.Join(t.TempDir(), "dbctl")
	script := "#!/bin/sh\ncat > " + marker + "\nprintf '%s' ok\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	broker, err := newBroker(webRoot, t.TempDir(), log.New(io.Discard, "", 0), &fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	broker.dbctlPath = helper
	response, err := broker.Execute(context.Background(), &Request{RequestType: "db", DB: &DBRequest{
		Action: "restore-dump", Database: "streamed_db", Username: "restore", Site: "site", DumpPath: dumpPath,
	}})
	if err != nil || !response.OK {
		t.Fatalf("streaming restore response = %#v, err=%v", response, err)
	}
	received, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(received) != "CREATE TABLE streamed (id INT);\n" {
		t.Fatalf("broker streamed %q", received)
	}
	if err := broker.validator.validateDumpPath(filepath.Join(t.TempDir(), "outside.sql")); err == nil {
		t.Fatal("dump validator accepted a path outside approved staging roots")
	}
}
