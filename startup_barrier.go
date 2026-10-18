package stepanel

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
)

// The startup barrier keeps durable workers and mutating HTTP requests out
// while the panel recovers interrupted operations. The panel opens a new
// epoch in the control-plane database before recovery and marks it open only
// when recovery succeeds, so an external worker process, which does not run
// recovery itself, waits for the same decision. A panel killed during
// recovery leaves the barrier closed until the next startup finishes.
const (
	startupBarrierRecovering = "recovering"
	startupBarrierOpen       = "open"
	startupBarrierFailed     = "failed"
)

// startupBarrierDetailLimit bounds the failure text stored for workers.
const startupBarrierDetailLimit = 2000

type startupBarrier struct {
	db *sql.DB
}

// begin closes the barrier under a new epoch and returns that epoch.
func (b startupBarrier) begin(owner string) (int64, error) {
	var epoch int64
	err := b.db.QueryRow(`INSERT INTO startup_barrier(id, epoch, state, detail, owner, updated_at)
		VALUES(1, 1, ?, '', ?, unixepoch())
		ON CONFLICT(id) DO UPDATE SET epoch = epoch + 1, state = excluded.state, detail = '', owner = excluded.owner, updated_at = excluded.updated_at
		RETURNING epoch`, startupBarrierRecovering, owner).Scan(&epoch)
	if err != nil {
		return 0, fmt.Errorf("close the startup barrier: %w", err)
	}
	return epoch, nil
}

// finish opens the barrier when recovery succeeded and records the failure
// otherwise. A newer epoch, from a panel that started since, is left alone.
func (b startupBarrier) finish(epoch int64, recoveryErr error) error {
	state, detail := startupBarrierOpen, ""
	if recoveryErr != nil {
		state, detail = startupBarrierFailed, recoveryErr.Error()
		if len(detail) > startupBarrierDetailLimit {
			detail = detail[:startupBarrierDetailLimit]
		}
	}
	result, err := b.db.Exec(`UPDATE startup_barrier SET state = ?, detail = ?, updated_at = unixepoch() WHERE id = 1 AND epoch = ?`, state, detail, epoch)
	if err != nil {
		return fmt.Errorf("record startup recovery outcome: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("record startup recovery outcome: %w", err)
	} else if rows == 0 {
		return fmt.Errorf("startup epoch %d was superseded by a newer panel start", epoch)
	}
	return nil
}

// status reports whether durable work may run, and why not.
func (b startupBarrier) status() (bool, string) {
	var state, detail string
	var epoch int64
	err := b.db.QueryRow(`SELECT epoch, state, detail FROM startup_barrier WHERE id = 1`).Scan(&epoch, &state, &detail)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, "no panel has finished startup recovery yet"
	case err != nil:
		return false, "cannot read the startup barrier: " + err.Error()
	case state == startupBarrierOpen:
		return true, ""
	case state == startupBarrierRecovering:
		return false, fmt.Sprintf("panel startup epoch %d is recovering interrupted operations", epoch)
	default:
		return false, fmt.Sprintf("panel startup epoch %d failed recovery: %s", epoch, detail)
	}
}

// mutationsOpen reports whether startup recovery has finished successfully
// in this panel process. Host reconciliation failures do not close it: they
// are per-resource drift that is retried in the background.
func (s *startupState) mutationsOpen() (bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.inProgress && s.err == nil, s.inProgress
}

// startupMutationGate refuses mutating requests until startup recovery has
// succeeded. Reads, /livez, /readyz and signing in and out stay available.
func (a *App) startupMutationGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/login" || r.URL.Path == "/logout" {
			next.ServeHTTP(w, r)
			return
		}
		open, inProgress := a.startup.mutationsOpen()
		if open {
			next.ServeHTTP(w, r)
			return
		}
		if inProgress {
			w.Header().Set("Retry-After", "5")
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "StePanel is recovering interrupted operations after a restart; retry shortly", "retryable": true})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "StePanel startup recovery failed, so changes are paused; an administrator must resolve the failure shown by /readyz and restart StePanel", "retryable": false})
	})
}
