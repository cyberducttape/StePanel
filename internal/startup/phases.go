package startup

import (
	"context"
	"fmt"
	"time"
)

// Phase represents a startup lifecycle phase with status
type Phase string

const (
	PhaseValidate   Phase = "validate"
	PhaseOpenState  Phase = "open_state"
	PhaseRecover    Phase = "recover"
	PhaseStartAPI   Phase = "start_api"
	PhaseReconcile  Phase = "reconcile"
	PhaseReady      Phase = "ready"
)

// Status represents the result of a phase
type Status struct {
	Phase       Phase
	Timestamp   time.Time
	Duration    time.Duration
	Success     bool
	Error       error
	Details     string
	Subcomponents []SubcomponentStatus
}

// SubcomponentStatus tracks status of individual components
type SubcomponentStatus struct {
	Component string
	Status    string
	Error     string
	Duration  time.Duration
}

// Timeline tracks all phase completions
type Timeline struct {
	phases   []Status
	startedAt time.Time
}

// NewTimeline creates a new startup timeline
func NewTimeline() *Timeline {
	return &Timeline{
		startedAt: time.Now(),
		phases:    []Status{},
	}
}

// RecordPhase marks a phase as complete
func (t *Timeline) RecordPhase(phase Phase, success bool, err error, details string) Status {
	status := Status{
		Phase:     phase,
		Timestamp: time.Now(),
		Success:   success,
		Error:     err,
		Details:   details,
	}

	if len(t.phases) > 0 {
		status.Duration = status.Timestamp.Sub(t.phases[len(t.phases)-1].Timestamp)
	} else {
		status.Duration = status.Timestamp.Sub(t.startedAt)
	}

	t.phases = append(t.phases, status)
	return status
}

// GetPhaseStatus returns the current status of a specific phase
func (t *Timeline) GetPhaseStatus(phase Phase) *Status {
	for i := range t.phases {
		if t.phases[i].Phase == phase {
			return &t.phases[i]
		}
	}
	return nil
}

// IsReady returns true if all critical phases completed successfully
func (t *Timeline) IsReady() bool {
	readyPhase := t.GetPhaseStatus(PhaseReady)
	return readyPhase != nil && readyPhase.Success
}

// TotalDuration returns time from start to now
func (t *Timeline) TotalDuration() time.Duration {
	return time.Since(t.startedAt)
}

// Summary returns a human-readable summary of startup
func (t *Timeline) Summary() string {
	if len(t.phases) == 0 {
		return "No phases recorded"
	}

	summary := fmt.Sprintf("Startup completed in %s\n", t.TotalDuration())
	for _, phase := range t.phases {
		status := "✅"
		if !phase.Success && phase.Error != nil {
			status = "❌"
		}
		summary += fmt.Sprintf("  %s %-20s %s (%.1fs)\n", status, phase.Phase, phase.Details, phase.Duration.Seconds())
	}
	return summary
}

// JSON returns structured status for API responses
func (t *Timeline) JSON() map[string]interface{} {
	phases := []map[string]interface{}{}
	for _, phase := range t.phases {
		phaseData := map[string]interface{}{
			"phase":      string(phase.Phase),
			"timestamp":  phase.Timestamp.Unix(),
			"duration_ms": int64(phase.Duration.Milliseconds()),
			"success":    phase.Success,
			"details":    phase.Details,
		}
		if phase.Error != nil {
			phaseData["error"] = phase.Error.Error()
		}
		if len(phase.Subcomponents) > 0 {
			subs := []map[string]interface{}{}
			for _, sub := range phase.Subcomponents {
				subs = append(subs, map[string]interface{}{
					"component":  sub.Component,
					"status":     sub.Status,
					"error":      sub.Error,
					"duration_ms": int64(sub.Duration.Milliseconds()),
				})
			}
			phaseData["subcomponents"] = subs
		}
		phases = append(phases, phaseData)
	}

	return map[string]interface{}{
		"phases":         phases,
		"ready":          t.IsReady(),
		"total_duration_ms": int64(t.TotalDuration().Milliseconds()),
		"started_at":     t.startedAt.Unix(),
	}
}
