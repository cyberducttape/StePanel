package main

import (
	"context"
	"database/sql"
	"testing"

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
}
