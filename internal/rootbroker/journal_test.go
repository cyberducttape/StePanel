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

func TestJournalPathsRejectEmptyOrUnsafeIDs(t *testing.T) {
	for _, fn := range []func(string, string) (string, error){
		creationJournalPath,
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

// Production backups dump through the broker into a file the caller
// created; the broker writes only into an empty, single-link regular file
// under an approved root, so a dump of any size bypasses the JSON response.
func TestBrokerDumpsIntoCallerCreatedFile(t *testing.T) {
	webRoot := t.TempDir()
	helper := filepath.Join(t.TempDir(), "dbctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n[ \"$1\" = dump ] || exit 64\nprintf 'DUMP OF %s\\n' \"$2\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	broker, err := newBroker(webRoot, t.TempDir(), log.New(io.Discard, "", 0), &fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	broker.dbctlPath = helper
	dump := func(path string) *Response {
		t.Helper()
		response, err := broker.Execute(context.Background(), &Request{RequestType: "db", DB: &DBRequest{
			Action: "dump", Database: "site_db", Username: "dump", Site: "dump", DumpPath: path,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	create := func(name, content string) string {
		path := filepath.Join(webRoot, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	target := create("dump.sql", "")
	if response := dump(target); !response.OK {
		t.Fatalf("dump into caller-created file failed: %s", response.Error)
	}
	if data, _ := os.ReadFile(target); string(data) != "DUMP OF site_db\n" {
		t.Fatalf("dump file = %q", data)
	}

	if response := dump(create("existing.sql", "keep")); response.OK {
		t.Fatal("broker overwrote a non-empty file")
	}
	linked := create("linked.sql", "")
	if err := os.Link(linked, filepath.Join(webRoot, "second-link.sql")); err != nil {
		t.Fatal(err)
	}
	if response := dump(linked); response.OK {
		t.Fatal("broker wrote into a file with more than one link")
	}
	symlink := filepath.Join(webRoot, "symlink.sql")
	if err := os.Symlink(create("victim.sql", ""), symlink); err != nil {
		t.Fatal(err)
	}
	if response := dump(symlink); response.OK {
		t.Fatal("broker followed a symlinked dump destination")
	}
	if response := dump(filepath.Join(webRoot, "missing.sql")); response.OK {
		t.Fatal("broker created a dump destination itself")
	}
	if _, err := os.Stat(filepath.Join(webRoot, "missing.sql")); !os.IsNotExist(err) {
		t.Fatal("broker created a file the caller did not")
	}
	outside := filepath.Join(t.TempDir(), "outside.sql")
	if err := os.WriteFile(outside, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if response := dump(outside); response.OK {
		t.Fatal("broker wrote outside the approved staging roots")
	}
}
