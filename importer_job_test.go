package stepanel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cyberducttape/StePanel/internal/importer"
)

func TestArchiveImportJobDoesNotPersistWebRootAuthority(t *testing.T) {
	payload, err := json.Marshal(durableArchiveImportRequest{
		ArchiveURL: "https://example.test/site.tar.gz",
		ConfigPath: "wp-config.php",
		SiteName:   "example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(`"web_root"`)) {
		t.Fatalf("archive job payload persisted deployment authority: %s", payload)
	}
}

func TestArchiveSpoolHoldsAndReleasesProgressiveCapacity(t *testing.T) {
	dir := t.TempDir()
	db, err := openControlPlaneDB(filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := newCapacityLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{ImportRoot: dir, MinFreeBytes: 1}, capacity: ledger}
	spooled, err := app.archiveSpool("archive import").Store(strings.NewReader("archive"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	var held int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(bytes),0) FROM capacity_reservations`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 1024-int64(len("archive")) {
		t.Fatalf("remaining spool reservation = %d, want %d", held, 1024-int64(len("archive")))
	}
	if err := spooled.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM capacity_reservations`).Scan(&held); err != nil || held != 0 {
		t.Fatalf("reservation rows after spool close = %d, err=%v; want zero", held, err)
	}
}

func TestArchiveImportRejectsDurableDatabaseRestoreWithoutPayloadEncryption(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs, err := openDurableJobsDBWithKey(db, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Auth: Auth{}, Jobs: jobs}
	request := httptest.NewRequest(http.MethodPost, "/api/import/archive", strings.NewReader(`{"url":"https://example.test/site.tar.gz","config_path":"wp-config.php","site_name":"example","database_password":"archive-db-password-123456","auto_restore_db":true}`))
	response := httptest.NewRecorder()
	app.archiveImportStart(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "archive-db-password") || !strings.Contains(response.Body.String(), "STEPANEL_ACCOUNT_KEY") {
		t.Fatalf("response exposed an unsafe or unhelpful error: %s", response.Body.String())
	}
	if len(jobs.List(10)) != 0 {
		t.Fatal("archive import was enqueued without encrypted payload storage")
	}
}

func TestArchiveImportEncryptsDurableDatabaseRestorePayload(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs, err := openDurableJobsDBWithKey(db, "", "test-account-key", 1)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Auth: Auth{}, Jobs: jobs}
	request := httptest.NewRequest(http.MethodPost, "/api/import/archive", strings.NewReader(`{"url":"https://example.test/site.tar.gz","config_path":"wp-config.php","site_name":"example","database_password":"archive-db-password-123456","auto_restore_db":true}`))
	response := httptest.NewRecorder()
	app.archiveImportStart(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var stored []byte
	if err := db.QueryRow(`SELECT payload FROM jobs LIMIT 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "archive-db-password") {
		t.Fatal("encrypted archive import payload contains plaintext database password")
	}
}

type testArchiveInspector struct {
	inspection *importer.ArchiveInspection
	err        error
}

func (t testArchiveInspector) InspectArchive(context.Context, string, string) (*importer.ArchiveInspection, error) {
	return t.inspection, t.err
}

func TestArchiveInspectionDurableHandlerReturnsResultOutput(t *testing.T) {
	previous := newArchiveAnalyzer
	newArchiveAnalyzer = func(importer.ArchiveSpool) archiveInspector {
		return testArchiveInspector{inspection: &importer.ArchiveInspection{URL: "https://example.test/archive.zip", ConfigPath: "wp-config.php"}}
	}
	t.Cleanup(func() { newArchiveAnalyzer = previous })

	payload, err := json.Marshal(durableArchiveInspectionRequest{ArchiveURL: "https://example.test/archive.zip", ConfigPath: "wp-config.php"})
	if err != nil {
		t.Fatal(err)
	}
	output, err := (&App{}).handleDurableJob(context.Background(), Job{Kind: "archive.inspect", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result["success"] != true || result["inspection"] == nil {
		t.Fatalf("durable inspection result = %#v", result)
	}
}

func TestArchiveInspectionFailureIsAJobFailureWithResultContext(t *testing.T) {
	previous := newArchiveAnalyzer
	newArchiveAnalyzer = func(importer.ArchiveSpool) archiveInspector {
		return testArchiveInspector{err: errors.New("archive is corrupt")}
	}
	t.Cleanup(func() { newArchiveAnalyzer = previous })

	payload, err := json.Marshal(durableArchiveInspectionRequest{ArchiveURL: "https://example.test/archive.zip", ConfigPath: "wp-config.php"})
	if err != nil {
		t.Fatal(err)
	}
	output, err := (&App{}).handleDurableJob(context.Background(), Job{Kind: "archive.inspect", Payload: payload})
	if err == nil || !strings.Contains(err.Error(), "archive inspection failed: archive is corrupt") {
		t.Fatalf("inspection error = %v", err)
	}
	if !strings.Contains(string(output), `"success":false`) || !strings.Contains(string(output), "archive is corrupt") {
		t.Fatalf("inspection failure result = %s", output)
	}
}

// Archive imports create sites in hosting accounts (ADR-0007). A placement
// that cannot be made is refused before a job is queued.
func TestArchiveImportPlacesSitesInHostingAccounts(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registerSitePlacementDB(db)
	t.Cleanup(func() { registerSitePlacementDB(nil) })
	jobs, err := openDurableJobsDBWithKey(db, "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Auth: Auth{}, Jobs: jobs}
	for name, body := range map[string]string{
		"addon without a domain": `{"url":"https://example.test/site.tar.gz","config_path":"wp-config.php","site_name":"shop","account":"alice","kind":"addon"}`,
		"reserved account":       `{"url":"https://example.test/site.tar.gz","config_path":"wp-config.php","site_name":"shop","account":"root"}`,
	} {
		response := httptest.NewRecorder()
		app.archiveImportStart(response, httptest.NewRequest(http.MethodPost, "/api/import/archive", strings.NewReader(body)))
		if response.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, body = %s", name, response.Code, response.Body.String())
		}
	}
	if len(jobs.List(10)) != 0 {
		t.Fatal("an archive import with an invalid placement was queued")
	}
	response := httptest.NewRecorder()
	app.archiveImportStart(response, httptest.NewRequest(http.MethodPost, "/api/import/archive", strings.NewReader(`{"url":"https://example.test/site.tar.gz","config_path":"wp-config.php","site_name":"shop","account":"alice","kind":"addon","domain":"shop.example.org"}`)))
	if response.Code != http.StatusAccepted && response.Code != http.StatusOK {
		t.Fatalf("valid placement status = %d, body = %s", response.Code, response.Body.String())
	}
	queued := jobs.List(10)
	if len(queued) != 1 {
		t.Fatalf("queued jobs = %d", len(queued))
	}
	var payload durableArchiveImportRequest
	if err := json.Unmarshal(queued[0].Payload, &payload); err != nil || payload.Account != "alice" || payload.Kind != "addon" || payload.Domain != "shop.example.org" {
		t.Fatalf("queued payload = %+v, %v", payload, err)
	}
	if got := importPlacementRequest("blog", "", "", ""); got.Account != "blog" || got.Kind != "" {
		t.Fatalf("default import placement = %+v, want a new account named after the site", got)
	}
}

// Archive imports are queued as the configured administrator (which need
// not be named "admin"), carry the requesting actor for the audit, and are
// refused before any change when the audit intent cannot be recorded.
func TestArchiveImportUsesConfiguredAdministratorAndAuditsIntent(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs, err := openDurableJobsDBWithKey(db, "", "test-account-key", 1)
	if err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	app := &App{Auth: Auth{Username: "ops"}, Jobs: jobs,
		Config: Config{WebRoot: t.TempDir(), AuditLog: filepath.Join(blocker, "audit.jsonl")}}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/archive/import", strings.NewReader(`{"url":"https://example.test/site.tar.gz?token=secret","config_path":"wp-config.php","site_name":"example"}`))
	request = request.WithContext(context.WithValue(request.Context(), apiTokenUsernameKey{}, "ops"))
	response := httptest.NewRecorder()
	app.archiveImportStart(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var queued []Job
	for _, job := range jobs.List(100) {
		if job.Kind == "archive.import" {
			queued = append(queued, job)
		}
	}
	if len(queued) != 1 || queued[0].User != "ops" {
		t.Fatalf("queued archive import jobs = %+v, want one owned by the configured administrator", queued)
	}
	var payload durableArchiveImportRequest
	if err := json.Unmarshal(queued[0].Payload, &payload); err != nil || payload.Actor != "ops" {
		t.Fatalf("payload actor = %q (%v), want ops", payload.Actor, err)
	}
	if detail := archiveImportAuditDetail(payload); strings.Contains(detail, "token") || !strings.Contains(detail, "example.test") {
		t.Fatalf("audit detail %q must name the host without the URL's query", detail)
	}
	job := queued[0]
	if _, err := app.handleArchiveImportJob(context.Background(), &job); err == nil || !strings.Contains(err.Error(), "security audit intent") {
		t.Fatalf("archive import without an audit intent = %v, want a refusal", err)
	}
	if entries, err := os.ReadDir(app.Config.WebRoot); err != nil || len(entries) != 0 {
		t.Fatalf("a refused import changed the web root: %v, %v", entries, err)
	}
}
