// Package sitelifecycle holds site lifecycle orchestration as domain logic,
// independent of HTTP handlers and of how each step reaches the host.
//
// The layering is:
//
//	HTTP handler / durable job   (authorization, locking, persistence)
//	  ↓
//	sitelifecycle                (step order, recovery gates, audit rules)
//	  ↓
//	TerminationHost              (privileged and host-state operations)
//
// Callers supply the host operations, the step journal and the audit sink as
// interfaces, so the sequencing rules can be reasoned about — and tested —
// without a filesystem, helpers or a database.
package sitelifecycle

import (
	"context"
	"fmt"
)

// Journal step names. They are persisted in termination journals on disk, so
// they must never be renamed: an in-flight journal written by an older
// release must still resume correctly.
const (
	StepBackupVerified   = "BACKUP_VERIFIED"
	StepDatabasesRemoved = "DATABASES_REMOVED"
	StepRoutesRemoved    = "ROUTES_REMOVED"
	StepProxiesRemoved   = "PROXIES_REMOVED"
	StepTasksRemoved     = "TASKS_REMOVED"
	StepServicesRemoved  = "SERVICES_REMOVED"
	StepSiteStateRemoved = "SITE_STATE_REMOVED"
	StepOwnershipRemoved = "OWNERSHIP_REMOVED"
)

// TerminationStepOrder is the order in which termination steps commit.
var TerminationStepOrder = []string{
	StepBackupVerified,
	StepDatabasesRemoved,
	StepRoutesRemoved,
	StepProxiesRemoved,
	StepTasksRemoved,
	StepServicesRemoved,
	StepSiteStateRemoved,
	StepOwnershipRemoved,
}

// Audit actions emitted by a termination.
const (
	AuditTerminationInitiated = "site.termination.initiated"
	AuditSiteTerminated       = "site.terminated"
)

// Journal records which steps of one termination have committed. It must
// persist MarkComplete and the backup path durably before returning.
type Journal interface {
	IsComplete(step string) bool
	MarkComplete(step string) error
	BackupPath() string
	SetBackupPath(path string)
	Cleanup() error
}

// TerminationHost performs the host-side work of terminating one site. Every
// removal must be idempotent: after a crash, a step that did not commit is
// run again.
type TerminationHost interface {
	// VerifiedBackup creates and verifies the backup that is the sole
	// recovery point, returning its path.
	VerifiedBackup(ctx context.Context) (string, error)
	// CheckTeardownPrerequisites runs after the backup gate on every attempt
	// and refuses to start teardown when a required helper is unavailable.
	CheckTeardownPrerequisites() error
	RemoveDatabases(ctx context.Context) error
	// RemoveRoutes must remove desired route state before live routes, so
	// the startup reconciler cannot recreate a route for a removed site.
	RemoveRoutes(ctx context.Context) error
	RemoveProxies(ctx context.Context) error
	RemoveTasks(ctx context.Context) error
	RemoveServices(ctx context.Context) error
	RemoveSiteState(ctx context.Context) error
	DetachOwnership(ctx context.Context) error
}

// Auditor durably records an audit event for the site and actor of this
// termination. A returned error means the event was not persisted.
type Auditor interface {
	Audit(action, detail string) error
}

// FaultInjector lets failure drills interrupt the sequence at named stages.
// Fail returns an error to abort before a stage; Kill may terminate the
// process after a stage has committed.
type FaultInjector interface {
	Fail(stage string) error
	Kill(stage string)
}

// Termination is one durable site-termination run.
type Termination struct {
	JobID   string
	Journal Journal
	Host    TerminationHost
	Audit   Auditor
	// Faults is optional.
	Faults FaultInjector
}

type teardownStep struct {
	name  string
	stage string
	run   func(context.Context) error
}

// Run executes the termination with roll-forward recovery and returns the
// verified backup path. Steps already recorded in the journal are skipped,
// so a crash never re-runs a committed step and a retry resumes with the
// first uncommitted one. BACKUP_VERIFIED is the gate before any destructive
// work; everything after it rolls forward. The journal is removed only after
// the completion audit event has been persisted.
func (t Termination) Run(ctx context.Context) (string, error) {
	// Record the initiation before any destructive work, so an audit entry
	// exists even if the run crashes before completion.
	if !t.Journal.IsComplete(StepBackupVerified) {
		if err := t.Audit.Audit(AuditTerminationInitiated, "job="+t.JobID); err != nil {
			return "", fmt.Errorf("record termination initiation: %w", err)
		}
	}
	if err := t.fail("init"); err != nil {
		return "", err
	}
	t.kill("init")

	var backupPath string
	if t.Journal.IsComplete(StepBackupVerified) {
		backupPath = t.Journal.BackupPath()
	} else {
		if err := t.fail("backup"); err != nil {
			return "", err
		}
		path, err := t.Host.VerifiedBackup(ctx)
		if err != nil {
			return "", err
		}
		backupPath = path
		t.Journal.SetBackupPath(backupPath)
		if err := t.Journal.MarkComplete(StepBackupVerified); err != nil {
			return "", fmt.Errorf("journal %s: %w", StepBackupVerified, err)
		}
		t.kill("backup")
	}

	if err := t.Host.CheckTeardownPrerequisites(); err != nil {
		return "", err
	}

	steps := []teardownStep{
		{StepDatabasesRemoved, "database", t.Host.RemoveDatabases},
		{StepRoutesRemoved, "routes", t.Host.RemoveRoutes},
		{StepProxiesRemoved, "proxies", t.Host.RemoveProxies},
		{StepTasksRemoved, "tasks", t.Host.RemoveTasks},
		{StepServicesRemoved, "services", t.Host.RemoveServices},
		{StepSiteStateRemoved, "site-state", t.Host.RemoveSiteState},
		{StepOwnershipRemoved, "ownership", t.Host.DetachOwnership},
	}
	for _, step := range steps {
		if t.Journal.IsComplete(step.name) {
			continue
		}
		if err := t.fail(step.stage); err != nil {
			return "", err
		}
		if err := step.run(ctx); err != nil {
			return "", err
		}
		if err := t.Journal.MarkComplete(step.name); err != nil {
			return "", fmt.Errorf("journal %s: %w", step.name, err)
		}
		t.kill(step.stage)
	}

	// Persist the completion record before removing the journal: if audit
	// fails, the journal stays and a retry re-emits the event.
	if err := t.Audit.Audit(AuditSiteTerminated, "verified backup="+backupPath); err != nil {
		return "", fmt.Errorf("record termination completion: %w", err)
	}
	if err := t.Journal.Cleanup(); err != nil {
		// The termination is complete and audited; a leftover journal is a
		// housekeeping issue reported to the operator, not a rollback.
		return "", fmt.Errorf("cleanup termination journal: %w", err)
	}
	return backupPath, nil
}

func (t Termination) fail(stage string) error {
	if t.Faults == nil {
		return nil
	}
	return t.Faults.Fail(stage)
}

func (t Termination) kill(stage string) {
	if t.Faults != nil {
		t.Faults.Kill(stage)
	}
}
