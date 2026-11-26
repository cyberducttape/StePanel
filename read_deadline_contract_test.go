package stepanel

import (
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/session"
)

// controlPlaneBusyTimeout is the busy_timeout in the control-plane DSN.
const controlPlaneBusyTimeout = 5 * time.Second

// With one connection per process, a request-path read can queue behind a
// write that is waiting out the full busy timeout for the other process's
// lock. A read deadline at or below that wait turns a routine worker restart
// into 503s (recovery cohort 45834181842 hit this with a 2-second session
// deadline), so every read deadline must outlast it.
func TestRequestReadDeadlinesOutlastOneBusyWait(t *testing.T) {
	for name, deadline := range map[string]time.Duration{
		"session.ReadTimeout":          session.ReadTimeout,
		"durableJobReadTimeout":        durableJobReadTimeout,
		"controlPlaneStateReadTimeout": controlPlaneStateReadTimeout,
	} {
		if deadline <= controlPlaneBusyTimeout+time.Second {
			t.Errorf("%s = %s, want more than the %s busy timeout plus a second", name, deadline, controlPlaneBusyTimeout)
		}
	}
	db := openTestControlPlane(t)
	var busy int64
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if time.Duration(busy)*time.Millisecond != controlPlaneBusyTimeout {
		t.Fatalf("control-plane busy_timeout = %d ms, want %s; update the read deadlines with it", busy, controlPlaneBusyTimeout)
	}
}
