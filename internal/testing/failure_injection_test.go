package testing

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestFailureInjectorRecordsRecoveryAndSuccessCounts(t *testing.T) {
	injector := NewFailureInjector()
	injector.SetFailure(FailurePointInit, FailureTypeSIGTERM)
	if err := injector.InjectAt(context.Background(), "restore", FailurePointInit); err == nil {
		t.Fatal("InjectAt unexpectedly succeeded")
	}
	time.Sleep(time.Millisecond)
	injector.RecordRecovery("restore")

	stats := injector.Stats()
	if stats.FailureCount != 1 || stats.RecoveryCount != 1 {
		t.Fatalf("stats = %+v, want one failure and recovery", stats)
	}
	if len(stats.Events) != 1 || stats.Events[0].RecoveryTime <= 0 {
		t.Fatalf("recovery event = %+v, want positive recovery duration", stats.Events)
	}

	var workflowInjector *FailureInjector
	workflow := WorkflowFailureTest{
		Name:      "success-count",
		Operation: "restore",
		ExecuteWorkflow: func(_ context.Context, injector *FailureInjector) error {
			workflowInjector = injector
			if err := injector.InjectAt(context.Background(), "restore", FailurePointInit); err != nil {
				return err
			}
			return nil
		},
		FailurePoints: []FailurePoint{FailurePointInit},
		FailureTypes:  []FailureType{FailureTypeSIGTERM},
	}
	if err := RunFailureTest(workflow); err != nil {
		t.Fatal(err)
	}
	if workflowInjector == nil {
		t.Fatal("workflow did not receive an injector")
	}
	if got := workflowInjector.Stats().SuccessCount; got != 1 {
		t.Fatalf("success count = %d, want 1", got)
	}

	var verified bool
	workflow.VerifyRecovery = func() error {
		verified = true
		return nil
	}
	if err := RunFailureTest(workflow); err != nil {
		t.Fatal(err)
	}
	if !verified {
		t.Fatal("recovery verification hook was not called")
	}
}

func TestRunFailureTestRejectsUnreachedFailurePoint(t *testing.T) {
	err := RunFailureTest(WorkflowFailureTest{
		Name:      "unreached",
		Operation: "restore",
		ExecuteWorkflow: func(context.Context, *FailureInjector) error {
			return nil
		},
		FailurePoints: []FailurePoint{FailurePointFinalWrite},
		FailureTypes:  []FailureType{FailureTypeSIGTERM},
	})
	if err == nil {
		t.Fatal("RunFailureTest unexpectedly accepted an unreached failure point")
	}
}

func TestFailureInjectorSIGKILLUsesRealSignal(t *testing.T) {
	if os.Getenv("STEPANEL_FAILURE_INJECTOR_CHILD") == "1" {
		injector := NewFailureInjector()
		injector.SetFailure(FailurePointInit, FailureTypeSIGKILL)
		_ = injector.InjectAt(nil, "test", FailurePointInit)
		t.Fatal("SIGKILL injection returned")
	}

	child := exec.Command(os.Args[0], "-test.run=TestFailureInjectorSIGKILLUsesRealSignal", "-test.v")
	child.Env = append(os.Environ(), "STEPANEL_FAILURE_INJECTOR_CHILD=1")
	err := child.Run()
	if err == nil {
		t.Fatal("child process unexpectedly survived SIGKILL injection")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("child exit = %v, want SIGKILL", err)
	}
	status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child exit = %v, want SIGKILL", err)
	}
}
