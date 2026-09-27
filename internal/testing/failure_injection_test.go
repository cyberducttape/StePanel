package testing

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

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
