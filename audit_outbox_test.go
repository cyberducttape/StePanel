package stepanel

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestAuditOutboxPersistsBeforePublicationAndFlushes(t *testing.T) {
	db, err := sql.Open("sqlite", "file:audit-outbox-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	outbox, err := newAuditOutboxStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.enqueue(context.Background(), "", "admin", "site.updated", "demo", "changed"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	var delivered, pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_outbox WHERE delivered_at IS NOT NULL`).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_outbox WHERE delivered_at IS NULL`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 || pending != 0 {
		t.Fatalf("outbox state = delivered %d, pending %d; want 1, 0", delivered, pending)
	}
}

func TestAuditOutboxRetainsFailedPublication(t *testing.T) {
	db, err := sql.Open("sqlite", "file:audit-outbox-failure-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	outbox, err := newAuditOutboxStore(db)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-auditOutboxRetention - time.Hour).UnixNano()
	if _, err := db.Exec(`INSERT INTO audit_outbox(actor,action,target,detail,created_at,delivered_at)
		VALUES ('admin','site.updated','old','published',?,?)`, old, old); err != nil {
		t.Fatal(err)
	}
	// A directory cannot be used as the append-only audit file. The intent
	// must nevertheless remain durable for a later retry.
	if err := outbox.enqueue(context.Background(), t.TempDir(), "admin", "site.deleted", "demo", "started"); err == nil {
		t.Fatal("expected publication failure")
	}
	var pending, attempts int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(attempts), 0) FROM audit_outbox WHERE delivered_at IS NULL`).Scan(&pending, &attempts); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || attempts != 1 {
		t.Fatalf("failed outbox state = pending %d, attempts %d; want 1, 1", pending, attempts)
	}
	var oldDelivered int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_outbox WHERE target='old'`).Scan(&oldDelivered); err != nil {
		t.Fatal(err)
	}
	if oldDelivered != 0 {
		t.Fatalf("retention did not run before failed publication; old row count=%d", oldDelivered)
	}
}

func TestAuditOutboxPrunesOnlyDeliveredRowsInBoundedBatches(t *testing.T) {
	db, err := sql.Open("sqlite", "file:audit-outbox-retention?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	outbox, err := newAuditOutboxStore(db)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-auditOutboxRetention - time.Hour).UnixNano()
	recent := time.Now().UTC().UnixNano()
	for i := 0; i < auditOutboxPruneBatch+1; i++ {
		if _, err := db.Exec(`INSERT INTO audit_outbox(actor, action, target, detail, created_at, delivered_at)
			VALUES ('admin','site.updated','demo','done',?,?)`, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO audit_outbox(actor, action, target, detail, created_at)
		VALUES ('admin','site.deleted','demo','pending',?)`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_outbox(actor, action, target, detail, created_at, delivered_at)
		VALUES ('admin','site.updated','demo','recent',?,?)`, recent, recent); err != nil {
		t.Fatal(err)
	}
	if err := outbox.pruneDelivered(context.Background()); err != nil {
		t.Fatalf("first retention pass: %v", err)
	}
	var oldDelivered, pending, recentDelivered int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_outbox WHERE delivered_at < ?`, recent-1).Scan(&oldDelivered); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_outbox WHERE delivered_at IS NULL`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_outbox WHERE delivered_at >= ?`, recent-1).Scan(&recentDelivered); err != nil {
		t.Fatal(err)
	}
	if oldDelivered != 1 || pending != 1 || recentDelivered != 1 {
		t.Fatalf("after bounded prune: old delivered=%d pending=%d recent=%d; want 1,1,1", oldDelivered, pending, recentDelivered)
	}
	if err := outbox.pruneDelivered(context.Background()); err != nil {
		t.Fatalf("second retention pass: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_outbox WHERE delivered_at < ?`, recent-1).Scan(&oldDelivered); err != nil {
		t.Fatal(err)
	}
	if oldDelivered != 0 {
		t.Fatalf("second bounded prune left %d old delivered rows", oldDelivered)
	}
}
