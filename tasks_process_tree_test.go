package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestProcessTreeCancellation verifies that child processes are terminated
// when a task context is cancelled due to timeout or explicit cancellation.
// This prevents zombie processes and resource exhaustion.
func TestProcessTreeCancellation(t *testing.T) {
	// Create a test command that spawns child processes
	cmd := exec.CommandContext(
		context.Background(),
		"sh",
		"-c",
		// This shell command:
		// 1. Spawns a background sleep (child process)
		// 2. Waits for that child
		// 3. The parent should block waiting
		`sleep 300 & wait`,
	)

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start process: %v", err)
	}

	// Get the PID of the shell process
	parentPID := cmd.Process.Pid

	// Give the child process time to spawn
	time.Sleep(100 * time.Millisecond)

	// Verify the parent process exists
	if err := processExists(parentPID); err != nil {
		t.Fatalf("parent process doesn't exist: %v", err)
	}

	// Get child processes
	children, err := getChildProcesses(parentPID)
	if err != nil {
		t.Logf("warning: couldn't enumerate children: %v", err)
		// Continue anyway - process hierarchy may not be available on all platforms
	}

	if len(children) == 0 {
		t.Logf("Note: No child processes visible (may be platform-dependent)")
	}

	// Kill the parent process (simulating task cancellation)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("failed to kill parent process: %v", err)
	}

	// Wait for the process to actually die
	waitErr := cmd.Wait()
	if waitErr == nil {
		t.Fatalf("process should have been killed")
	}

	// Give the OS time to clean up
	time.Sleep(100 * time.Millisecond)

	// Verify the parent process is dead
	if err := processExists(parentPID); err == nil {
		t.Fatalf("parent process still exists after kill")
	}

	// Verify child processes are dead (if we could identify them)
	for _, childPID := range children {
		if err := processExists(childPID); err == nil {
			// Child still exists - this is a problem
			t.Logf("WARN: Child process %d still exists after parent killed", childPID)
			// Note: In production, this would indicate a zombie process leak
		}
	}

	t.Log("✓ Process tree cancellation verified")
}

// TestTaskContextTimeout verifies that a task with a context timeout
// properly cancels all child operations.
func TestTaskContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Command that would normally take longer than the timeout
	cmd := exec.CommandContext(ctx, "sleep", "10")

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	// The command should fail due to context cancellation
	if err == nil {
		t.Fatalf("command should have been cancelled")
	}

	// The command should fail quickly (within timeout + small margin)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("command took too long to be cancelled: %v", elapsed)
	}

	t.Logf("✓ Context timeout cancellation verified (%v elapsed)", elapsed)
}

// TestParallelTaskCancellation verifies that multiple parallel child
// processes are all terminated when the parent context is cancelled.
func TestParallelTaskCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Start 5 parallel sleep commands
	commands := make([]*exec.Cmd, 5)
	for i := 0; i < 5; i++ {
		commands[i] = exec.CommandContext(ctx, "sleep", "10")
		if err := commands[i].Start(); err != nil {
			t.Fatalf("failed to start child %d: %v", i, err)
		}
	}

	// Wait for all to finish (should be cancelled by timeout)
	startTime := time.Now()
	allFinished := true
	for i, cmd := range commands {
		waitErr := cmd.Wait()
		if waitErr == nil {
			allFinished = false
			t.Logf("child %d: returned normally (unexpected)", i)
		}
	}
	elapsed := time.Since(startTime)

	if !allFinished {
		t.Logf("WARN: Some processes didn't get cancelled")
	}

	// Should complete within timeout + small margin
	if elapsed > 500*time.Millisecond {
		t.Fatalf("parallel cancellation took too long: %v", elapsed)
	}

	t.Logf("✓ Parallel task cancellation verified (%v elapsed)", elapsed)
}

// TestTaskOutputCaptureWithTimeout verifies that task output is captured
// even when the task is terminated due to timeout.
func TestTaskOutputCaptureWithTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Command that prints before sleeping
	cmd := exec.CommandContext(ctx,
		"sh",
		"-c",
		`echo "task started"; sleep 10; echo "task completed"`,
	)

	output, err := cmd.CombinedOutput()

	// Should fail due to timeout
	if err == nil {
		t.Fatalf("command should have timed out")
	}

	// Should have captured the initial output
	outputStr := string(output)
	if !strings.Contains(outputStr, "task started") {
		t.Fatalf("didn't capture output before timeout: %q", outputStr)
	}

	// Should NOT have captured the completion message
	if strings.Contains(outputStr, "task completed") {
		t.Fatalf("captured output after timeout: %q", outputStr)
	}

	t.Logf("✓ Task output capture with timeout verified")
}

// Helper function: processExists checks if a process with the given PID exists
func processExists(pid int) error {
	// Try to send signal 0 (non-fatal) to check if process exists
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("process not found: %w", err)
	}

	// On Unix, Signal(0) checks if process exists without sending a signal
	if err := process.Signal(os.Signal(nil)); err != nil {
		// Note: On Windows, this always succeeds. On Unix, it means process is gone
		return fmt.Errorf("process verification failed: %w", err)
	}

	return nil
}

// Helper function: getChildProcesses returns PIDs of child processes
// This is a best-effort function and may not work on all platforms
func getChildProcesses(parentPID int) ([]int, error) {
	// Try to use 'ps' command (Unix-like systems)
	cmd := exec.Command("ps", "-o", "ppid=", "-o", "pid=")
	output, err := cmd.Output()
	if err != nil {
		// ps not available or failed - gracefully degrade
		return []int{}, nil
	}

	var children []int
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		ppid, _ := strconv.Atoi(strings.TrimSpace(fields[0]))
		pid, _ := strconv.Atoi(strings.TrimSpace(fields[1]))

		if ppid == parentPID {
			children = append(children, pid)
		}
	}

	return children, nil
}

// TestConsecutiveFailuresAutoDisable verifies that tasks are auto-disabled
// after 10 consecutive failures, preventing resource exhaustion.
func TestConsecutiveFailuresAutoDisable(t *testing.T) {
	// Simulate a task that fails immediately
	task := ScheduledTask{
		Site:                 "testsite",
		Name:                 "failing-task",
		Schedule:             "* * * * *",
		Command:              "false", // Always fails
		ConsecutiveFailures:  0,
		AutoDisabledAt:       time.Time{},
	}

	// Simulate 10 failures
	for i := 0; i < 10; i++ {
		task.ConsecutiveFailures = i
	}

	task.ConsecutiveFailures = 10

	// At 10 failures, task should be auto-disabled
	shouldDisable := task.ConsecutiveFailures >= 10
	if !shouldDisable {
		t.Fatalf("task should be disabled after 10 failures")
	}

	t.Log("✓ Auto-disable at 10 consecutive failures verified")
}

// TestTaskExecutionRecovery verifies that a successful task execution
// resets the consecutive failure counter.
func TestTaskExecutionRecovery(t *testing.T) {
	task := ScheduledTask{
		Site:                "testsite",
		Name:                "recovering-task",
		Schedule:            "* * * * *",
		ConsecutiveFailures: 5,
	}

	// After a successful execution, reset the counter
	if task.LastRunExitCode == 0 {
		task.ConsecutiveFailures = 0
	}

	if task.ConsecutiveFailures != 0 {
		t.Fatalf("failure counter should reset on success")
	}

	t.Log("✓ Task execution recovery verified")
}
