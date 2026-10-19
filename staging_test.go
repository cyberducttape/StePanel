package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

func TestCloneManagedDatabaseToStagingContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cloneManagedDatabaseToStagingContext(ctx, Config{}, "source", "target", "user", "password", AuthorizedSite{site: "site"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cloneManagedDatabaseToStagingContext error = %v, want context.Canceled", err)
	}
}

// A production staging clone holds a ledger reservation for its dump and
// must hand the same ledger to the restore, whose admission refuses to run
// without one.
func TestProductionStagingCloneRestoresWithItsCapacityLedger(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "control.db")
	db, err := openControlPlaneDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := newCapacityLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(root, "broker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("STEPANEL_LAB_DIRECT_ROOT_BROKER", "1")
	t.Setenv("STEPANEL_SKIP_STARTUP_HOST_RECONCILE", "1")
	t.Setenv("STEPANEL_LAB_ROOT_BROKER_SOCKET", listener.Addr().String())
	importRoot := filepath.Join(root, "imports")
	if err := os.MkdirAll(importRoot, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Production:     true,
		DBCtl:          "/not-used/stepanel-dbctl",
		WebRoot:        root,
		ImportRoot:     importRoot,
		ControlPlaneDB: dbPath,
		DBEngine:       "mysql",
		DBDataDir:      root,
	}
	actions := make(chan string, 8)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				close(actions)
				return
			}
			var request rootbroker.Request
			if err := json.NewDecoder(conn).Decode(&request); err != nil || request.DB == nil {
				_ = conn.Close()
				continue
			}
			actions <- request.DB.Action
			response := rootbroker.Response{OK: true}
			switch request.DB.Action {
			case "dump":
				_ = os.WriteFile(request.DB.DumpPath, []byte(strings.Repeat("x", 1<<10)), 0600)
				response.Details, _ = json.Marshal(rootbroker.DBResponse{})
			case "inventory":
				response.Details, _ = json.Marshal(rootbroker.DBResponse{Output: "site_staging\tsite\tstaging_user\t1024\tutf8mb4\n"})
			}
			_ = json.NewEncoder(conn).Encode(response)
			_ = conn.Close()
		}
	}()
	if _, err := cloneManagedDatabaseToStagingContext(context.Background(), cfg, "site_db", "site_staging", "staging_user", "StagingPassword1234", AuthorizedSite{site: "site"}, ledger); err != nil {
		t.Fatalf("production staging clone failed: %v", err)
	}
	_ = listener.Close()
	var seen []string
	for action := range actions {
		seen = append(seen, action)
	}
	if len(seen) == 0 || seen[len(seen)-1] != "restore-dump" {
		t.Fatalf("broker actions = %v, want the clone to finish with restore-dump", seen)
	}
}

func TestStagingBasicAuthSupportMatchesVHostHelpers(t *testing.T) {
	for _, webserver := range []string{"caddy", "apache"} {
		if !stagingBasicAuthSupported(webserver) {
			t.Fatalf("%s should support staging Basic Auth", webserver)
		}
	}
	for _, webserver := range []string{"openlitespeed", "", "nginx"} {
		if stagingBasicAuthSupported(webserver) {
			t.Fatalf("%s must not advertise unsupported staging Basic Auth", webserver)
		}
	}
}
