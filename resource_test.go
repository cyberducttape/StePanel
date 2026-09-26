package main

import (
	"testing"
)

// TestResourceBudgetAllocation verifies that the budget correctly allocates
// capacity across workload classes based on MaxConcurrentJobs.
func TestResourceBudgetAllocation(t *testing.T) {
	rb := NewResourceBudget(2) // MaxConcurrentJobs = 2

	tests := []struct {
		workload string
		expected int // Expected max capacity
	}{
		{"restore", 1},
		{"backup", 1},
		{"extract", 1}, // max(1, 2/2) = 1
		{"upload", 1},
		{"malware_scan", 1},
		{"build", 1},
		{"db_restore", 1},
	}

	for _, tt := range tests {
		// Try to acquire slots without releasing; count how many succeed before hitting capacity
		acquired := 0
		for i := 0; i < 10; i++ {
			if rb.AcquireSlot(tt.workload) {
				acquired++
			} else {
				break
			}
		}

		if acquired != tt.expected {
			t.Errorf("workload %s: expected capacity %d, got %d", tt.workload, tt.expected, acquired)
		}

		// Clean up by releasing all acquired slots
		for i := 0; i < acquired; i++ {
			rb.ReleaseSlot(tt.workload)
		}
	}
}

// TestResourceBudgetEnforcement verifies that exceeding capacity returns false
// and doesn't allow additional operations.
func TestResourceBudgetEnforcement(t *testing.T) {
	rb := NewResourceBudget(2)

	// Acquire one slot for "backup"
	if !rb.AcquireSlot("backup") {
		t.Fatal("first backup acquisition should succeed")
	}

	// Second acquisition should fail (capacity is 1)
	if rb.AcquireSlot("backup") {
		t.Fatal("second backup acquisition should fail (capacity exceeded)")
	}

	// Other workloads should still work
	if !rb.AcquireSlot("extract") {
		t.Fatal("extract should have independent capacity")
	}

	// Release and retry
	rb.ReleaseSlot("backup")
	if !rb.AcquireSlot("backup") {
		t.Fatal("backup acquisition should succeed after release")
	}
}

// TestResourceBudgetUtilization verifies utilization reporting.
func TestResourceBudgetUtilization(t *testing.T) {
	rb := NewResourceBudget(2)

	// Initially empty
	util := rb.Utilization("extract")
	if util != 0 {
		t.Errorf("initial utilization should be 0, got %d", util)
	}

	// Acquire one slot
	rb.AcquireSlot("extract")
	util = rb.Utilization("extract")
	// With max=1, using 1 slot = 100% utilization
	if util != 100 {
		t.Errorf("utilization with 1 slot acquired and max=1 should be 100, got %d", util)
	}

	rb.ReleaseSlot("extract")
	util = rb.Utilization("extract")
	if util != 0 {
		t.Errorf("utilization after release should be 0, got %d", util)
	}
}

// TestResourceBudgetStatus verifies that status report includes all workload classes.
func TestResourceBudgetStatus(t *testing.T) {
	rb := NewResourceBudget(2)

	status := rb.Status()

	expectedWorkloads := []string{"restore", "backup", "extract", "upload", "malware_scan", "build", "db_restore"}
	for _, workload := range expectedWorkloads {
		if _, exists := status[workload]; !exists {
			t.Errorf("workload %s missing from status report", workload)
		}
	}
}

// TestResourceBudgetUnknownWorkload verifies that unknown workload classes
// are rejected to prevent unbounded growth.
func TestResourceBudgetUnknownWorkload(t *testing.T) {
	rb := NewResourceBudget(2)

	// Attempt to acquire slot for non-existent workload
	if rb.AcquireSlot("nonexistent_workload") {
		t.Fatal("unknown workload should be rejected")
	}

	// Release should also be a no-op
	rb.ReleaseSlot("nonexistent_workload") // Should not panic
}
