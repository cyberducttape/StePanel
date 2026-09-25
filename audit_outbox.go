package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"
)

// auditOutboxStore makes the mutation-to-audit boundary durable. The signed
// audit file remains the tamper-evident publication format, while this table
// records the intent before publication can fail because of disk pressure,
// permissions, or a transient lock.
type auditOutboxStore struct {
	db *sql.DB
	mu sync.Mutex
}

var defaultAuditOutbox *auditOutboxStore

func newAuditOutboxStore(db *sql.DB) (*auditOutboxStore, error) {
	if db == nil {
		return nil, fmt.Errorf("audit outbox database is required")
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS audit_outbox (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			actor TEXT NOT NULL,
			action TEXT NOT NULL,
			target TEXT NOT NULL,
			detail TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			delivered_at INTEGER,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_audit_outbox_pending
			ON audit_outbox(delivered_at, id);
	`); err != nil {
		return nil, fmt.Errorf("create audit outbox: %w", err)
	}
	return &auditOutboxStore{db: db}, nil
}

func (o *auditOutboxStore) enqueue(ctx context.Context, auditLog, actor, action, target, detail string) error {
	if actor == "" || action == "" {
		return fmt.Errorf("audit actor and action are required")
	}
	if _, err := o.db.ExecContext(ctx, `
		INSERT INTO audit_outbox(actor, action, target, detail, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, actor, action, target, detail, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("persist audit outbox event: %w", err)
	}
	return o.flush(ctx, auditLog)
}

func (o *auditOutboxStore) flush(ctx context.Context, auditLog string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	rows, err := o.db.QueryContext(ctx, `
		SELECT id, actor, action, target, detail
		FROM audit_outbox
		WHERE delivered_at IS NULL
		ORDER BY id
		LIMIT 32
	`)
	if err != nil {
		return fmt.Errorf("read audit outbox: %w", err)
	}
	type event struct {
		id                            int64
		actor, action, target, detail string
	}
	events := make([]event, 0, 32)
	for rows.Next() {
		var item event
		if err := rows.Scan(&item.id, &item.actor, &item.action, &item.target, &item.detail); err != nil {
			rows.Close()
			return fmt.Errorf("scan audit outbox: %w", err)
		}
		events = append(events, item)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close audit outbox rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range events {
		if err := ShouldAudit(auditLog, item.actor, item.action, item.target, item.detail); err != nil {
			if _, updateErr := o.db.ExecContext(ctx, `
				UPDATE audit_outbox
				SET attempts = attempts + 1, last_error = ?
				WHERE id = ? AND delivered_at IS NULL
			`, err.Error(), item.id); updateErr != nil {
				log.Printf("audit outbox failure state could not be persisted: %v", updateErr)
			}
			return err
		}
		if _, err := o.db.ExecContext(ctx, `
			UPDATE audit_outbox
			SET delivered_at = ?, last_error = NULL
			WHERE id = ? AND delivered_at IS NULL
		`, time.Now().UTC().UnixNano(), item.id); err != nil {
			return fmt.Errorf("mark audit outbox event delivered: %w", err)
		}
	}
	return nil
}

func (o *auditOutboxStore) pendingCount(ctx context.Context) (int, error) {
	var count int
	err := o.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_outbox WHERE delivered_at IS NULL`).Scan(&count)
	return count, err
}

func (o *auditOutboxStore) closePending(ctx context.Context, auditLog string) {
	if err := o.flush(ctx, auditLog); err != nil {
		if count, countErr := o.pendingCount(ctx); countErr == nil && count > 0 {
			log.Printf("audit outbox still has %d pending event(s): %v", count, err)
		} else {
			log.Printf("audit outbox flush failed: %v", err)
		}
	}
}
