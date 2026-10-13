package stepanel

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupportBundleIsRedactedAndContainsOperationalEvidence(t *testing.T) {
	app := &App{
		Config: Config{
			WebServer: "caddy", DBEngine: "sqlite", DBVersion: "default", WorkerMode: "panel",
			DBPassword: "database-password", AccountKey: "account-key-secret", BackupSigningKey: "backup-signing-secret",
			EnvironmentKey: "environment-key-secret", GitWebhookSecret: "webhook-secret", Production: false,
		},
		Metrics: NewMetrics(),
	}
	app.Metrics.ObserveHTTP(200, 10)

	var output bytes.Buffer
	if err := writeSupportBundle(&output, app); err != nil {
		t.Fatalf("write support bundle: %v", err)
	}
	archive, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatalf("read support bundle: %v", err)
	}
	want := map[string]bool{"README.txt": false, "metadata.json": false, "configuration.json": false, "readiness.json": false, "operational-readiness.json": false, "production-readiness.json": false, "metrics.txt": false, "jobs.json": false, "job-list-status.json": false}
	for _, entry := range archive.File {
		if _, ok := want[entry.Name]; !ok {
			t.Errorf("unexpected support bundle entry %q", entry.Name)
			continue
		}
		want[entry.Name] = true
		reader, openErr := entry.Open()
		if openErr != nil {
			t.Fatalf("open %s: %v", entry.Name, openErr)
		}
		data, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil {
			t.Fatalf("read %s: %v", entry.Name, readErr)
		}
		if strings.Contains(string(data), "database-password") || strings.Contains(string(data), "account-key-secret") || strings.Contains(string(data), "backup-signing-secret") || strings.Contains(string(data), "environment-key-secret") || strings.Contains(string(data), "webhook-secret") {
			t.Errorf("support bundle entry %s contains a configured secret", entry.Name)
		}
		if entry.Name == "metrics.txt" && !strings.Contains(string(data), "stepanel_http_requests_total") {
			t.Error("metrics evidence is missing from support bundle")
		}
	}
	for name, present := range want {
		if !present {
			t.Errorf("support bundle is missing %s", name)
		}
	}
}

func TestSupportBundleMarksDurableJobHistoryUnavailable(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := newJobsWithDB(db, 1)
	if _, err := jobs.Enqueue("support.test", "site", "", nil, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.ListWithError(10); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := writeSupportBundle(&output, &App{Jobs: jobs}); err != nil {
		t.Fatalf("write support bundle: %v", err)
	}
	archive, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range archive.File {
		if entry.Name != "job-list-status.json" {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		var status struct {
			Available bool `json:"available"`
		}
		decodeErr := json.NewDecoder(reader).Decode(&status)
		_ = reader.Close()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if status.Available {
			t.Fatal("support bundle reported stale cached jobs as authoritative")
		}
		return
	}
	t.Fatal("support bundle omitted job-list-status.json")
}
