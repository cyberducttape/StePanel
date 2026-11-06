package stepanel

import (
	"context"
	"database/sql"
	"fmt"
)

// immediateTx is a SQLite transaction that holds the database write lock
// from its first statement. A deferred transaction that reads and then
// writes cannot upgrade a read snapshot that another process's commit made
// stale: SQLite fails the write at once instead of waiting out the busy
// timeout. Taking the lock first (BEGIN IMMEDIATE) makes the transaction
// wait for the other writer instead.
type immediateTx struct {
	ctx  context.Context
	conn *sql.Conn
	done bool
}

func beginImmediate(ctx context.Context, db *sql.DB) (*immediateTx, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("open transaction connection: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("begin immediate transaction: %w", err)
	}
	return &immediateTx{ctx: ctx, conn: conn}, nil
}

func (t *immediateTx) Exec(query string, args ...any) (sql.Result, error) {
	return t.conn.ExecContext(t.ctx, query, args...)
}

func (t *immediateTx) QueryRow(query string, args ...any) *sql.Row {
	return t.conn.QueryRowContext(t.ctx, query, args...)
}

func (t *immediateTx) Commit() error {
	if t.done {
		return sql.ErrTxDone
	}
	t.done = true
	defer t.conn.Close()
	if _, err := t.conn.ExecContext(t.ctx, "COMMIT"); err != nil {
		// Never return a connection to the pool with the transaction open.
		_, _ = t.conn.ExecContext(context.Background(), "ROLLBACK")
		return err
	}
	return nil
}

// Rollback is a no-op after Commit, so it can always be deferred.
func (t *immediateTx) Rollback() error {
	if t.done {
		return nil
	}
	t.done = true
	defer t.conn.Close()
	_, err := t.conn.ExecContext(context.Background(), "ROLLBACK")
	return err
}
