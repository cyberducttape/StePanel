package sitelifecycle

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeJournal struct {
	completed  map[string]bool
	backupPath string
	markErr    map[string]error
	cleaned    bool
	events     *[]string
}

func (j *fakeJournal) IsComplete(step string) bool { return j.completed[step] }
func (j *fakeJournal) MarkComplete(step string) error {
	if err := j.markErr[step]; err != nil {
		return err
	}
	j.completed[step] = true
	*j.events = append(*j.events, "journal:"+step)
	return nil
}
func (j *fakeJournal) BackupPath() string        { return j.backupPath }
func (j *fakeJournal) SetBackupPath(path string) { j.backupPath = path }
func (j *fakeJournal) Cleanup() error {
	j.cleaned = true
	*j.events = append(*j.events, "journal:cleanup")
	return nil
}

type fakeHost struct {
	events     *[]string
	fail       map[string]error
	prereqErr  error
	backupPath string
}

func (h *fakeHost) record(name string) error {
	*h.events = append(*h.events, "host:"+name)
	return h.fail[name]
}
func (h *fakeHost) VerifiedBackup(context.Context) (string, error) {
	return h.backupPath, h.record("backup")
}
func (h *fakeHost) CheckTeardownPrerequisites() error {
	*h.events = append(*h.events, "host:prerequisites")
	return h.prereqErr
}
func (h *fakeHost) RemoveDatabases(context.Context) error { return h.record("databases") }
func (h *fakeHost) RemoveRoutes(context.Context) error    { return h.record("routes") }
func (h *fakeHost) RemoveProxies(context.Context) error   { return h.record("proxies") }
func (h *fakeHost) RemoveTasks(context.Context) error     { return h.record("tasks") }
func (h *fakeHost) RemoveServices(context.Context) error  { return h.record("services") }
func (h *fakeHost) RemoveSiteState(context.Context) error { return h.record("site-state") }
func (h *fakeHost) DetachOwnership(context.Context) error { return h.record("ownership") }

type fakeAuditor struct {
	events *[]string
	fail   map[string]error
}

func (a *fakeAuditor) Audit(action, detail string) error {
	if err := a.fail[action]; err != nil {
		return err
	}
	*a.events = append(*a.events, "audit:"+action+":"+detail)
	return nil
}

type fixture struct {
	events  []string
	journal *fakeJournal
	host    *fakeHost
	audit   *fakeAuditor
}

func newFixture() *fixture {
	f := &fixture{}
	f.journal = &fakeJournal{completed: map[string]bool{}, markErr: map[string]error{}, events: &f.events}
	f.host = &fakeHost{events: &f.events, fail: map[string]error{}, backupPath: "/backups/site-a.tar"}
	f.audit = &fakeAuditor{events: &f.events, fail: map[string]error{}}
	return f
}

func (f *fixture) run() (string, error) {
	return Termination{JobID: "job-1", Journal: f.journal, Host: f.host, Audit: f.audit}.Run(context.Background())
}

func TestTerminationRunsStepsInOrderAndAuditsAroundThem(t *testing.T) {
	f := newFixture()
	path, err := f.run()
	if err != nil || path != "/backups/site-a.tar" {
		t.Fatalf("Run() = %q, %v", path, err)
	}
	want := []string{
		"audit:site.termination.initiated:job=job-1",
		"host:backup", "journal:BACKUP_VERIFIED",
		"host:prerequisites",
		"host:databases", "journal:DATABASES_REMOVED",
		"host:routes", "journal:ROUTES_REMOVED",
		"host:proxies", "journal:PROXIES_REMOVED",
		"host:tasks", "journal:TASKS_REMOVED",
		"host:services", "journal:SERVICES_REMOVED",
		"host:site-state", "journal:SITE_STATE_REMOVED",
		"host:ownership", "journal:OWNERSHIP_REMOVED",
		"audit:site.terminated:verified backup=/backups/site-a.tar",
		"journal:cleanup",
	}
	if !reflect.DeepEqual(f.events, want) {
		t.Fatalf("events =\n%s\nwant\n%s", strings.Join(f.events, "\n"), strings.Join(want, "\n"))
	}
}

func TestTerminationResumesAfterLastCommittedStep(t *testing.T) {
	f := newFixture()
	f.journal.completed[StepBackupVerified] = true
	f.journal.completed[StepDatabasesRemoved] = true
	f.journal.backupPath = "/backups/earlier.tar"
	path, err := f.run()
	if err != nil || path != "/backups/earlier.tar" {
		t.Fatalf("Run() = %q, %v", path, err)
	}
	for _, event := range f.events {
		if event == "host:backup" || event == "host:databases" || strings.HasPrefix(event, "audit:site.termination.initiated") {
			t.Fatalf("committed work was repeated: %v", f.events)
		}
	}
	if f.events[0] != "host:prerequisites" || f.events[1] != "host:routes" {
		t.Fatalf("resume did not start at the first uncommitted step: %v", f.events)
	}
}

func TestTerminationRefusesToStartWithoutInitiationAudit(t *testing.T) {
	f := newFixture()
	f.audit.fail[AuditTerminationInitiated] = errors.New("audit offline")
	if _, err := f.run(); err == nil || !strings.Contains(err.Error(), "record termination initiation") {
		t.Fatalf("Run() error = %v", err)
	}
	if len(f.events) != 0 {
		t.Fatalf("work ran without an initiation record: %v", f.events)
	}
}

func TestTerminationNeverTearsDownWithoutVerifiedBackup(t *testing.T) {
	f := newFixture()
	f.host.fail["backup"] = errors.New("backup verification failed")
	if _, err := f.run(); err == nil {
		t.Fatal("Run() succeeded without a verified backup")
	}
	for _, event := range f.events {
		if strings.HasPrefix(event, "journal:") || event == "host:databases" {
			t.Fatalf("teardown progressed past a failed backup: %v", f.events)
		}
	}
}

func TestTerminationChecksPrerequisitesOnEveryAttempt(t *testing.T) {
	f := newFixture()
	f.journal.completed[StepBackupVerified] = true
	f.journal.completed[StepDatabasesRemoved] = true
	f.host.prereqErr = errors.New("database helper missing")
	if _, err := f.run(); err == nil || err.Error() != "database helper missing" {
		t.Fatalf("Run() error = %v", err)
	}
	if !reflect.DeepEqual(f.events, []string{"host:prerequisites"}) {
		t.Fatalf("teardown ran despite missing prerequisites: %v", f.events)
	}
}

func TestTerminationStepFailureIsNotJournaled(t *testing.T) {
	f := newFixture()
	f.host.fail["proxies"] = errors.New("proxy helper failed")
	if _, err := f.run(); err == nil {
		t.Fatal("Run() succeeded after a failed step")
	}
	if f.journal.completed[StepProxiesRemoved] || f.journal.completed[StepTasksRemoved] {
		t.Fatalf("failed or later step was journaled: %v", f.journal.completed)
	}
	if !f.journal.completed[StepRoutesRemoved] {
		t.Fatal("earlier committed step was lost")
	}
}

func TestTerminationKeepsJournalWhenCompletionAuditFails(t *testing.T) {
	f := newFixture()
	f.audit.fail[AuditSiteTerminated] = errors.New("audit offline")
	if _, err := f.run(); err == nil || !strings.Contains(err.Error(), "record termination completion") {
		t.Fatalf("Run() error = %v", err)
	}
	if f.journal.cleaned {
		t.Fatal("journal removed before the completion record was persisted")
	}
}

func TestTerminationWrapsJournalFailures(t *testing.T) {
	f := newFixture()
	f.journal.markErr[StepRoutesRemoved] = errors.New("disk full")
	_, err := f.run()
	if err == nil || err.Error() != "journal ROUTES_REMOVED: disk full" {
		t.Fatalf("Run() error = %v", err)
	}
}

type recordingFaults struct {
	failAt string
	seen   []string
}

func (r *recordingFaults) Fail(stage string) error {
	r.seen = append(r.seen, "fail:"+stage)
	if stage == r.failAt {
		return errors.New("injected")
	}
	return nil
}
func (r *recordingFaults) Kill(stage string) { r.seen = append(r.seen, "kill:"+stage) }

func TestTerminationExposesEveryDrillStage(t *testing.T) {
	f := newFixture()
	faults := &recordingFaults{}
	_, err := Termination{JobID: "job-1", Journal: f.journal, Host: f.host, Audit: f.audit, Faults: faults}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var stages []string
	for _, event := range faults.seen {
		if strings.HasPrefix(event, "fail:") {
			stages = append(stages, strings.TrimPrefix(event, "fail:"))
		}
	}
	want := []string{"init", "backup", "database", "routes", "proxies", "tasks", "services", "site-state", "ownership"}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("drill stages = %v, want %v", stages, want)
	}

	g := newFixture()
	_, err = Termination{JobID: "job-1", Journal: g.journal, Host: g.host, Audit: g.audit, Faults: &recordingFaults{failAt: "tasks"}}.Run(context.Background())
	if err == nil || g.journal.completed[StepTasksRemoved] || !g.journal.completed[StepProxiesRemoved] {
		t.Fatalf("injected failure at tasks: err=%v journal=%v", err, g.journal.completed)
	}
}
