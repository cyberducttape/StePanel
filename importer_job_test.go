package stepanel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	newArchiveAnalyzer = func() archiveInspector {
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
	newArchiveAnalyzer = func() archiveInspector {
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
