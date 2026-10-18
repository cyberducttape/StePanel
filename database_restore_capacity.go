package stepanel

import (
	"errors"
	"strings"
)

func databaseServerIsLocal(cfg Config) bool {
	host := strings.TrimSpace(cfg.DBHost)
	return host == "" || host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasPrefix(host, "localhost:") || strings.HasPrefix(host, "/")
}

func requireDatabaseRestoreAdmission(cfg Config, ledger *capacityLedger) error {
	if !cfg.Production || !databaseServerIsLocal(cfg) {
		return nil
	}
	if databaseDataDir(cfg) == "" {
		return errors.New("cannot establish the local database data filesystem; configure STEPANEL_DB_DATA_DIR before restoring databases")
	}
	if ledger == nil {
		return errors.New("database restore requires the shared durable capacity ledger")
	}
	return nil
}

// reserveDatabaseRestoreCapacity holds a cross-process SQLite reservation for
// the estimated shadow database until the helper finishes loading and swapping
// it. The helper independently checks the actual database filesystem before
// mutation; this ledger prevents concurrent StePanel restores from promising
// the same free bytes.
func reserveDatabaseRestoreCapacity(cfg Config, dumpBytes, liveDatabaseBytes uint64, ledger *capacityLedger) (func(), error) {
	if dumpBytes == 0 {
		return func() {}, nil
	}
	dir := databaseDataDir(cfg)
	if dir == "" {
		if err := requireDatabaseRestoreAdmission(cfg, ledger); err != nil {
			return nil, err
		}
		return func() {}, nil // A remote database has its own independent capacity boundary.
	}
	if dumpBytes > ^uint64(0)/2 || liveDatabaseBytes > ^uint64(0)/3 {
		return nil, errors.New("database restore capacity estimate overflow")
	}
	if ledger == nil {
		if err := requireDatabaseRestoreAdmission(cfg, ledger); err != nil {
			return nil, err
		}
		return func() {}, nil
	}
	demand := max(dumpBytes*2, liveDatabaseBytes*3/2)
	reservation, err := ledger.reserve(cfg, "database restore", dir, []capacityDemand{{Path: dir, Bytes: demand}})
	if err != nil {
		return nil, err
	}
	return func() {
		reservation.release()
	}, nil
}
