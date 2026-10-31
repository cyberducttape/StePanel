package stepanel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	auditOutboxRetention            = 90 * 24 * time.Hour
	auditOutboxPruneBatch           = 500
	securityAuditOperationRetention = 90 * 24 * time.Hour
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
		CREATE INDEX IF NOT EXISTS idx_audit_outbox_retention
			ON audit_outbox(delivered_at, id) WHERE delivered_at IS NOT NULL;
		CREATE TABLE IF NOT EXISTS security_audit_operations (
			operation_id TEXT PRIMARY KEY,
			actor TEXT NOT NULL,
			action TEXT NOT NULL,
		target TEXT NOT NULL,
			intent_detail TEXT NOT NULL,
			started_at INTEGER NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('pending', 'completed', 'reconciled')),
			outcome_action TEXT,
			outcome_detail TEXT,
			finished_at INTEGER
		);
		CREATE INDEX IF NOT EXISTS idx_security_audit_operations_pending
			ON security_audit_operations(state, started_at);
	`); err != nil {
		return nil, fmt.Errorf("create audit outbox: %w", err)
	}
	return &auditOutboxStore{db: db}, nil
}

// beginSecurityAudit atomically records the initiation event and a durable
// operation row. If the process dies after the host mutation, the pending row
// remains available for operator reconciliation.
func (o *auditOutboxStore) beginSecurityAudit(ctx context.Context, auditLog, id, actor, action, target, detail string) error {
	if actor == "" || action == "" || id == "" {
		return errors.New("audit operation identity, actor, and action are required")
	}
	now := time.Now().UTC().UnixNano()
	initDetail := securityAuditOperationDetail(id, detail)
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin security audit intent: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO security_audit_operations
		(operation_id, actor, action, target, intent_detail, started_at, state)
		VALUES (?, ?, ?, ?, ?, ?, 'pending')`, id, actor, action, target, detail, now); err != nil {
		return fmt.Errorf("persist security audit operation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_outbox(actor, action, target, detail, created_at)
		VALUES (?, ?, ?, ?, ?)`, actor, action+".initiated", target, initDetail, now); err != nil {
		return fmt.Errorf("persist security audit intent event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit security audit intent: %w", err)
	}
	if err := o.flush(ctx, auditLog); err != nil {
		log.Printf("[ERROR] security audit intent publication deferred for %s/%s: %v", action, target, err)
	}
	return nil
}

// completeSecurityAudit atomically records the terminal event and closes the
// operation row. A later publication failure is safe: the outbox is durable
// and retries independently of the host mutation.
func (o *auditOutboxStore) completeSecurityAudit(ctx context.Context, auditLog, id, actor, baseAction, action, target, detail string) error {
	if id == "" {
		return errors.New("security audit operation ID is missing")
	}
	now := time.Now().UTC().UnixNano()
	fullDetail := securityAuditOperationDetail(id, detail)
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin security audit outcome: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE security_audit_operations
		SET state='completed', outcome_action=?, outcome_detail=?, finished_at=?
		WHERE operation_id=? AND state='pending' AND actor=? AND action=? AND target=?`,
		action, detail, now, id, actor, baseAction, target)
	if err != nil {
		return fmt.Errorf("persist security audit outcome state: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check security audit outcome state: %w", err)
	}
	if changed == 0 {
		var state, priorAction, priorDetail string
		if err := tx.QueryRowContext(ctx, `SELECT state, COALESCE(outcome_action,''), COALESCE(outcome_detail,'')
			FROM security_audit_operations WHERE operation_id=? AND actor=? AND target=?`, id, actor, target).
			Scan(&state, &priorAction, &priorDetail); err != nil {
			return fmt.Errorf("find pending security audit operation: %w", err)
		}
		if state == "completed" && priorAction == action && priorDetail == detail {
			return nil // idempotent retry after an ambiguous commit result
		}
		return errors.New("security audit operation is not pending or outcome conflicts")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_outbox(actor, action, target, detail, created_at)
		VALUES (?, ?, ?, ?, ?)`, actor, action, target, fullDetail, now); err != nil {
		return fmt.Errorf("persist security audit outcome event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit security audit outcome: %w", err)
	}
	if err := o.flush(ctx, auditLog); err != nil {
		log.Printf("[ERROR] security audit outcome publication deferred for %s/%s: %v", action, target, err)
	}
	return nil
}

func (o *auditOutboxStore) pendingSecurityAuditOperations(ctx context.Context) (int, error) {
	var count int
	err := o.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_audit_operations WHERE state='pending'`).Scan(&count)
	return count, err
}

type pendingSecurityAuditOperation struct {
	ID        string `json:"operation_id"`
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	StartedAt int64  `json:"started_at"`
}

func (o *auditOutboxStore) listPendingSecurityAuditOperations(ctx context.Context, limit int) ([]pendingSecurityAuditOperation, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := o.db.QueryContext(ctx, `SELECT operation_id, actor, action, target, started_at
		FROM security_audit_operations WHERE state='pending' ORDER BY started_at, operation_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]pendingSecurityAuditOperation, 0)
	for rows.Next() {
		var item pendingSecurityAuditOperation
		if err := rows.Scan(&item.ID, &item.Actor, &item.Action, &item.Target, &item.StartedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// reconcileSecurityAuditOperation records an operator's evidence-based review.
// It closes only the audit reconciliation record; it does not replay or alter
// the underlying host operation.
func (o *auditOutboxStore) reconcileSecurityAuditOperation(ctx context.Context, auditLog, id, actor, detail string) error {
	if id == "" || actor == "" || strings.TrimSpace(detail) == "" {
		return errors.New("operation ID, operator, and reconciliation evidence are required")
	}
	now := time.Now().UTC().UnixNano()
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE security_audit_operations SET state='reconciled',
		outcome_action='security.audit.operation.reconciled', outcome_detail=?, finished_at=?
		WHERE operation_id=? AND state='pending'`, detail, now, id)
	if err != nil {
		return fmt.Errorf("record security audit reconciliation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("security audit operation is not pending or was already reconciled")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_outbox(actor, action, target, detail, created_at)
		VALUES (?, 'security.audit.operation.reconciled', ?, ?, ?)`, actor, id, securityAuditOperationDetail(id, detail), now); err != nil {
		return fmt.Errorf("persist security audit reconciliation event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit security audit reconciliation: %w", err)
	}
	if err := o.flush(ctx, auditLog); err != nil {
		log.Printf("[ERROR] security audit reconciliation publication deferred for %s: %v", id, err)
	}
	return nil
}

func (o *auditOutboxStore) enqueue(ctx context.Context, auditLog, actor, action, target, detail string) error {
	if err := o.persist(ctx, actor, action, target, detail); err != nil {
		return err
	}
	return o.flush(ctx, auditLog)
}

// recordDurably persists an event in the outbox and then attempts
// publication. Success means the event is durable: a publication failure is
// logged and retried later, and must not cause the caller to treat the event
// as unrecorded (it will still be published).
func (o *auditOutboxStore) recordDurably(ctx context.Context, auditLog, actor, action, target, detail string) error {
	if err := o.persist(ctx, actor, action, target, detail); err != nil {
		return err
	}
	if err := o.flush(ctx, auditLog); err != nil {
		log.Printf("[ERROR] audit outbox publication deferred for %s/%s: %v", action, target, err)
	}
	return nil
}

func (o *auditOutboxStore) persist(ctx context.Context, actor, action, target, detail string) error {
	if actor == "" || action == "" {
		return fmt.Errorf("audit actor and action are required")
	}
	if _, err := o.db.ExecContext(ctx, `
		INSERT INTO audit_outbox(actor, action, target, detail, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, actor, action, target, detail, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("persist audit outbox event: %w", err)
	}
	return nil
}

func (o *auditOutboxStore) flush(ctx context.Context, auditLog string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.pruneDelivered(ctx); err != nil {
		// Retention is maintenance, not an audit publication prerequisite.
		// Keep the cleanup failure observable without preventing a retry.
		log.Printf("audit outbox retention cleanup deferred: %v", err)
	}
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
		if err := publishAuditEvent(auditLog, item.actor, item.action, item.target, item.detail); err != nil {
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

// pruneDelivered removes only a bounded batch of rows already represented in
// the signed audit chain. That chain, not the retry outbox, is the long-term
// evidence source. Repeated flushes drain larger backlogs incrementally.
func (o *auditOutboxStore) pruneDelivered(ctx context.Context) error {
	cutoff := time.Now().UTC().Add(-auditOutboxRetention).UnixNano()
	result, err := o.db.ExecContext(ctx, `DELETE FROM audit_outbox WHERE id IN (
		SELECT id FROM audit_outbox
		WHERE delivered_at IS NOT NULL AND delivered_at < ?
		ORDER BY delivered_at, id LIMIT ?
	)`, cutoff, auditOutboxPruneBatch)
	if err != nil {
		return fmt.Errorf("prune delivered audit outbox: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count pruned audit outbox rows: %w", err)
	}
	if deleted > 0 {
		log.Printf("pruned %d delivered audit outbox row(s) older than %s; signed audit log retains long-term evidence", deleted, auditOutboxRetention)
	}
	operationCutoff := time.Now().UTC().Add(-securityAuditOperationRetention).UnixNano()
	result, err = o.db.ExecContext(ctx, `DELETE FROM security_audit_operations WHERE operation_id IN (
		SELECT operation_id FROM security_audit_operations
		WHERE state IN ('completed', 'reconciled') AND finished_at < ?
		ORDER BY finished_at, operation_id LIMIT ?
	)`, operationCutoff, auditOutboxPruneBatch)
	if err != nil {
		return fmt.Errorf("prune completed security audit operations: %w", err)
	}
	if deleted, err = result.RowsAffected(); err != nil {
		return fmt.Errorf("count pruned security audit operations: %w", err)
	}
	if deleted > 0 {
		log.Printf("pruned %d completed security audit operation(s) older than %s; signed audit log retains long-term evidence", deleted, securityAuditOperationRetention)
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
