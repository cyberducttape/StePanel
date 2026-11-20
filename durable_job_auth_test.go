package stepanel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuthorizeDurableSiteJobRechecksSuspensionAndOwnership(t *testing.T) {
	app := &App{Auth: Auth{Username: "admin"}, Accounts: &AccountStore{accounts: map[string]HostingAccount{
		"customer": {Username: "customer", Plan: "starter", Sites: []string{"owned"}},
	}}}
	_, err := app.authorizeDurableSiteJob("owned", "customer", false)
	if err != nil {
		t.Fatalf("owned customer job rejected: %v", err)
	}
	app.Accounts.accounts["customer"] = HostingAccount{Username: "customer", Plan: "starter", Sites: []string{"owned"}, Suspended: true}
	_, err = app.authorizeDurableSiteJob("owned", "customer", false)
	if err == nil {
		t.Fatal("suspended customer job was accepted")
	}
	_, err = app.authorizeDurableSiteJob("unowned", "admin", false)
	if err != nil {
		t.Fatalf("administrator job rejected: %v", err)
	}
	_, err = app.authorizeDurableSiteJob("owned", "scheduler", true)
	if err != nil {
		t.Fatalf("scheduled job rejected: %v", err)
	}
}

func TestAuthorizeDurableSiteJobStaysBoundToQueuedTenant(t *testing.T) {
	accounts := &AccountStore{accounts: map[string]HostingAccount{
		"tenant-a": {Username: "tenant-a", Plan: "professional", Sites: []string{"shop"}},
		"member":   {Username: "member", TenantID: "tenant-a", Role: "member", Plan: "professional"},
		"tenant-b": {Username: "tenant-b", Plan: "professional"},
	}}
	app := &App{Auth: Auth{Username: "admin"}, Accounts: accounts}

	access, err := app.authorizeDurableSiteJobForTenant("shop", "member", "tenant-a", true, false)
	if err != nil {
		t.Fatalf("queued tenant job rejected before reassignment: %v", err)
	}
	if access.TenantID() != "tenant-a" {
		t.Fatalf("durable capability tenant = %q, want tenant-a", access.TenantID())
	}

	// Transfer both the member and site. A fresh ownership check alone would
	// authorize the old job under tenant-b; the captured tenant must still match.
	accounts.accounts["tenant-a"] = HostingAccount{Username: "tenant-a", Plan: "professional"}
	accounts.accounts["tenant-b"] = HostingAccount{Username: "tenant-b", Plan: "professional", Sites: []string{"shop"}}
	accounts.accounts["member"] = HostingAccount{Username: "member", TenantID: "tenant-b", Role: "member", Plan: "professional"}
	if _, err := app.authorizeDurableSiteJobForTenant("shop", "member", "tenant-a", true, false); err == nil {
		t.Fatal("durable job was re-authorized after its site and actor moved to another tenant")
	}
	if _, err := app.authorizeDurableSiteJobForTenant("shop", "member", "tenant-b", true, false); err != nil {
		t.Fatalf("newly queued tenant-b job rejected: %v", err)
	}
}

func TestQueuedAdminRestoreAndTerminationStayBoundToTenant(t *testing.T) {
	for _, kind := range []string{"backup.restore", "site.terminate"} {
		t.Run(kind, func(t *testing.T) {
			db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			accounts := &AccountStore{accounts: map[string]HostingAccount{
				"tenant-a": {Username: "tenant-a", Plan: "professional", Sites: []string{"shop"}},
				"tenant-b": {Username: "tenant-b", Plan: "professional"},
			}}
			app := &App{Auth: Auth{Username: "admin"}, Accounts: accounts, Jobs: newJobsWithDB(db, 1)}
			var queued Job
			switch kind {
			case "backup.restore":
				queued, err = app.enqueueBackupRestoreJob(durableBackupRestoreRequest{Mode: "files", Site: "shop", Backup: "backup-1", Actor: "admin"}, "")
			case "site.terminate":
				queued, err = app.enqueueSiteTermination("shop", "admin")
			}
			if err != nil {
				t.Fatal(err)
			}
			var binding struct {
				TenantID    string `json:"tenant_id"`
				TenantBound bool   `json:"tenant_bound"`
			}
			if err := json.Unmarshal(queued.Payload, &binding); err != nil || !binding.TenantBound || binding.TenantID != "tenant-a" {
				t.Fatalf("queued tenant binding = %+v, decode error = %v", binding, err)
			}
			accounts.accounts["tenant-a"] = HostingAccount{Username: "tenant-a", Plan: "professional"}
			accounts.accounts["tenant-b"] = HostingAccount{Username: "tenant-b", Plan: "professional", Sites: []string{"shop"}}
			item, ok, err := app.Jobs.ClaimNext("test-worker", kind)
			if err != nil || !ok {
				t.Fatalf("claim %s: claimed=%v err=%v", kind, ok, err)
			}
			var executionErr error
			if kind == "backup.restore" {
				_, executionErr = app.handleBackupRestoreJob(context.Background(), item)
			} else {
				_, executionErr = app.handleSiteTermination(context.Background(), item)
			}
			if executionErr == nil || !strings.Contains(executionErr.Error(), "ownership changed") {
				t.Fatalf("%s execution error = %v, want reassignment refusal", kind, executionErr)
			}
		})
	}
}

func TestQueuedCPanelAndWPressRestoresStayBoundToTenant(t *testing.T) {
	for _, kind := range []string{"cpmove.restore", "wordpress.restore"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			db, err := openControlPlaneDB(filepath.Join(root, "control.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			accounts := &AccountStore{accounts: map[string]HostingAccount{
				"tenant-a": {Username: "tenant-a", Plan: "professional", Sites: []string{"shop"}},
				"tenant-b": {Username: "tenant-b", Plan: "professional"},
			}}
			app := &App{Auth: Auth{Username: "admin"}, Accounts: accounts, Jobs: newJobsWithDB(db, 1), Config: Config{ImportRoot: filepath.Join(root, "imports")}}
			if err := os.MkdirAll(app.Config.ImportRoot, 0700); err != nil {
				t.Fatal(err)
			}
			tenantID, tenantBound, err := app.durableTenantBinding("shop")
			if err != nil || !tenantBound || tenantID != "tenant-a" {
				t.Fatalf("site binding = (%q, %t), error = %v", tenantID, tenantBound, err)
			}
			item := Job{ID: "import-job", User: "admin"}
			if kind == "cpmove.restore" {
				upload := cpmoveUpload{ID: "upload-1", Path: cpmoveUploadPath(app.Config.ImportRoot, "upload-1"), Filename: "cpmove.tar.gz", Owner: "admin", SHA256: strings.Repeat("0", 64), ExpiresAt: time.Now().Add(time.Hour)}
				metadata, err := json.Marshal(upload)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(cpmoveUploadMetadataPath(app.Config.ImportRoot, upload.ID), metadata, 0600); err != nil {
					t.Fatal(err)
				}
				item.Payload, err = json.Marshal(durableCPMoveRequest{UploadID: upload.ID, Filename: upload.Filename, User: "shop", Actor: "admin", TenantID: tenantID, TenantBound: tenantBound})
			} else {
				item.Payload, err = json.Marshal(durableWPressRequest{TempPath: filepath.Join(app.Config.ImportRoot, "staged.wpress"), Size: 0, SHA256: strings.Repeat("0", 64), Site: "shop", DBSuffix: "db", DBUserSuffix: "db", Password: "0123456789abcdef", SiteURL: "https://example.com", TargetPrefix: "wp_", Actor: "admin", TenantID: tenantID, TenantBound: tenantBound})
			}
			if err != nil {
				t.Fatal(err)
			}
			accounts.accounts["tenant-a"] = HostingAccount{Username: "tenant-a", Plan: "professional"}
			accounts.accounts["tenant-b"] = HostingAccount{Username: "tenant-b", Plan: "professional", Sites: []string{"shop"}}
			var executionErr error
			if kind == "cpmove.restore" {
				_, executionErr = app.handleCPMoveJob(context.Background(), item)
			} else {
				_, executionErr = app.handleWPressJob(context.Background(), item)
			}
			if executionErr == nil || !strings.Contains(executionErr.Error(), "ownership changed") {
				t.Fatalf("execution error = %v, want reassignment refusal", executionErr)
			}
		})
	}
}
