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

func TestDatabaseRestoreCapacityReservationsCoordinateAcrossLedgers(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "control.db")
	firstDB, err := openControlPlaneDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer firstDB.Close()
	firstLedger, err := newCapacityLedger(firstDB)
	if err != nil {
		t.Fatal(err)
	}
	secondDB, err := openControlPlaneDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()
	secondLedger, err := newCapacityLedger(secondDB)
	if err != nil {
		t.Fatal(err)
	}
	demand := uint64(8 << 20) // reserveDatabaseRestoreCapacity holds 2x the dump.
	free, err := availableBytes(root)
	if err != nil {
		t.Fatal(err)
	}
	margin := demand / 2
	if free <= demand+margin {
		t.Skipf("filesystem has insufficient free space for the admission boundary test: %d bytes", free)
	}
	cfg := Config{
		Production:     true,
		ControlPlaneDB: dbPath,
		DBEngine:       "mysql",
		DBDataDir:      root,
		MinFreeBytes:   free - demand - margin,
	}
	first, err := reserveDatabaseRestoreCapacity(cfg, demand/2, 0, firstLedger)
	if err != nil {
		t.Fatalf("first database restore reservation: %v", err)
	}
	defer first()
	if _, err := reserveDatabaseRestoreCapacity(cfg, demand/2, 0, secondLedger); err == nil {
		t.Fatal("a second ledger admitted bytes already promised to the first restore")
	}
	first()
	second, err := reserveDatabaseRestoreCapacity(cfg, demand/2, 0, secondLedger)
	if err != nil {
		t.Fatalf("reservation was not available after release: %v", err)
	}
	second()
}

func TestProductionDatabaseRestoreHoldsReservationAcrossBrokerCall(t *testing.T) {
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
	secondDB, err := openControlPlaneDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()
	secondLedger, err := newCapacityLedger(secondDB)
	if err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(root, "restore.sql")
	if err := os.WriteFile(dump, []byte(strings.Repeat("x", 4<<20)), 0600); err != nil {
		t.Fatal(err)
	}
	free, err := availableBytes(root)
	if err != nil {
		t.Fatal(err)
	}
	demand := uint64(12 << 20) // max(2x the 4 MiB dump, 1.5x the 8 MiB live DB).
	margin := demand / 2
	if free <= demand+margin {
		t.Skipf("filesystem has insufficient free space for the admission boundary test: %d bytes", free)
	}
	listener, err := net.Listen("unix", filepath.Join(root, "broker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("STEPANEL_LAB_DIRECT_ROOT_BROKER", "1")
	t.Setenv("STEPANEL_SKIP_STARTUP_HOST_RECONCILE", "1")
	t.Setenv("STEPANEL_LAB_ROOT_BROKER_SOCKET", listener.Addr().String())
	cfg := Config{
		Production:     true,
		DBCtl:          "/not-used/stepanel-dbctl",
		WebRoot:        root,
		ControlPlaneDB: dbPath,
		DBEngine:       "mysql",
		DBDataDir:      root,
		MinFreeBytes:   free - demand - margin,
	}
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- runDatabaseRestoreFromPath(context.Background(), cfg, "restore-dump", "site", "site_db", "restore", "", dump, ledger)
	}()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	var request rootbroker.Request
	if err := json.NewDecoder(conn).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.RequestType != "db" || request.DB == nil || request.DB.Action != "inventory" {
		t.Fatalf("broker request = %#v, want managed database inventory", request)
	}
	inventoryDetails, err := json.Marshal(rootbroker.DBResponse{Output: "site_db\tsite\tuser\t8388608\tutf8mb4\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(rootbroker.Response{OK: true, Details: inventoryDetails}); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	conn, err = listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewDecoder(conn).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.RequestType != "db" || request.DB == nil || request.DB.Action != "restore-dump" {
		t.Fatalf("broker request = %#v, want database restore", request)
	}
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- runDatabaseRestoreFromPath(context.Background(), cfg, "restore-dump", "site", "site_db", "restore", "", dump, secondLedger)
	}()
	inventoryConn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(inventoryConn).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.RequestType != "db" || request.DB == nil || request.DB.Action != "inventory" {
		t.Fatalf("second broker request = %#v, want inventory before admission", request)
	}
	if err := json.NewEncoder(inventoryConn).Encode(rootbroker.Response{OK: true, Details: inventoryDetails}); err != nil {
		t.Fatal(err)
	}
	_ = inventoryConn.Close()
	secondErr := <-secondDone
	var capacityErr *capacityError
	if !errors.As(secondErr, &capacityErr) {
		t.Fatalf("concurrent restore error = %v, want capacity rejection before broker dispatch", secondErr)
	}
	if err := json.NewEncoder(conn).Encode(rootbroker.Response{OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("first restore failed: %v", err)
	}
}
