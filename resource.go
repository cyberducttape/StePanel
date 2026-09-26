package main

import (
	"sync"
)

// ResourceBudget manages host-wide concurrency limits across different workload classes.
// Each workload class (uploads, backups, restores, etc.) has independent concurrency
// limits, but they all draw from the same host capacity to prevent I/O storms where
// N independent "2 concurrent" limits add up to overwhelming the host.
type ResourceBudget struct {
	mu sync.Mutex

	// Workload class names and their active counts
	classes map[string]*workloadClass
}

type workloadClass struct {
	name     string
	maxSlots int
	active   int
	slots    chan struct{}
}

// NewResourceBudget creates a new resource budget with default allocation based on
// MaxConcurrentJobs. Allocation strategy:
// - Restores (most I/O intensive): 1
// - Backups (I/O intensive): 1
// - Extractions (temporary I/O): min(2, MaxConcurrentJobs)
// - Uploads (temporary I/O): min(2, MaxConcurrentJobs)
// - Malware scans (I/O + memory): 1
// - Builds (CPU-bound, less I/O): min(2, MaxConcurrentJobs)
// - Database restores (concurrent DB ops): 1
func NewResourceBudget(maxConcurrentJobs int) *ResourceBudget {
	rb := &ResourceBudget{
		classes: make(map[string]*workloadClass),
	}

	// Define workload classes with their concurrency limits
	// These are defaults; can be overridden via environment or config
	workloads := map[string]int{
		"restore":      1,                           // Database restore: serial to prevent connection pool exhaustion
		"backup":       1,                           // Backup: serial to prevent I/O contention
		"extract":      max(1, maxConcurrentJobs/2), // Archive extraction: share the budget
		"upload":       max(1, maxConcurrentJobs/2), // Upload: share the budget
		"malware_scan": 1,                           // Malware scan: serial, I/O + memory intensive
		"build":        max(1, maxConcurrentJobs/2), // Build: CPU-bound, less I/O impact
		"db_restore":   1,                           // Database restore from dump: serial
	}

	for name, maxSlots := range workloads {
		rb.classes[name] = &workloadClass{
			name:     name,
			maxSlots: maxSlots,
			slots:    make(chan struct{}, maxSlots),
		}
		// Fill the slots channel
		for i := 0; i < maxSlots; i++ {
			rb.classes[name].slots <- struct{}{}
		}
	}

	return rb
}

// AcquireSlot tries to acquire a slot for the given workload class.
// Returns true if successful, false if the class is at capacity.
func (rb *ResourceBudget) AcquireSlot(workloadClass string) bool {
	rb.mu.Lock()
	class, exists := rb.classes[workloadClass]
	rb.mu.Unlock()

	if !exists {
		// Unknown workload class; deny to prevent unbounded growth
		return false
	}

	select {
	case <-class.slots:
		return true
	default:
		return false
	}
}

// ReleaseSlot releases a slot for the given workload class.
func (rb *ResourceBudget) ReleaseSlot(workloadClass string) {
	rb.mu.Lock()
	class, exists := rb.classes[workloadClass]
	rb.mu.Unlock()

	if !exists {
		return
	}

	select {
	case class.slots <- struct{}{}:
	default:
		// Slot was already available; shouldn't happen in normal operation
	}
}

// Utilization returns the current utilization (0-100) for a workload class.
// Useful for monitoring and alerting.
func (rb *ResourceBudget) Utilization(workloadClass string) int {
	rb.mu.Lock()
	class, exists := rb.classes[workloadClass]
	rb.mu.Unlock()

	if !exists {
		return 0
	}

	available := len(class.slots)
	used := class.maxSlots - available
	if class.maxSlots == 0 {
		return 0
	}
	return (used * 100) / class.maxSlots
}

// Status returns the current status of all workload classes.
// Useful for the admin dashboard.
func (rb *ResourceBudget) Status() map[string]map[string]int {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	status := make(map[string]map[string]int)
	for name, class := range rb.classes {
		available := len(class.slots)
		used := class.maxSlots - available
		status[name] = map[string]int{
			"active":      used,
			"max":         class.maxSlots,
			"utilization": (used * 100) / class.maxSlots,
		}
	}
	return status
}
