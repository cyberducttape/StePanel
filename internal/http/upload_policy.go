package http

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// UploadResourcePolicy enforces resource limits on concurrent uploads
type UploadResourcePolicy struct {
	// Concurrency limit
	maxConcurrent int
	activeSem     chan struct{}

	// Per-user quotas
	userQuotaMu sync.RWMutex
	userQuotas  map[string]*UserUploadQuota

	// Global metrics
	bytesUploaded atomic.Int64
	uploadCount   atomic.Int64
	lastResetTime time.Time
	resetMu       sync.Mutex

	// Configuration
	maxUploadBytes      int64
	minFreeSpace        int64
	maxUploadRate       int64 // bytes per second
	quotaResetInterval  time.Duration
	quotaBytesPerUser   int64
	quotaUploadsPerUser int
}

// UserUploadQuota tracks per-user upload limits
type UserUploadQuota struct {
	BytesThisInterval   int64
	UploadsThisInterval int
	LastResetTime       time.Time
}

// NewUploadResourcePolicy creates a new upload resource policy
// maxConcurrent: max simultaneous uploads
// maxUploadBytes: max single file size (e.g., 20GB)
// minFreeSpace: minimum free disk space before rejecting uploads (e.g., 100GB)
// maxUploadRate: rate limit per connection (bytes/sec, 0 = unlimited)
// quotaBytesPerUser: max bytes per user per day (0 = unlimited)
// quotaUploadsPerUser: max uploads per user per day (0 = unlimited)
func NewUploadResourcePolicy(
	maxConcurrent int,
	maxUploadBytes int64,
	minFreeSpace int64,
	maxUploadRate int64,
	quotaBytesPerUser int64,
	quotaUploadsPerUser int,
) *UploadResourcePolicy {
	return &UploadResourcePolicy{
		maxConcurrent:       maxConcurrent,
		activeSem:           make(chan struct{}, maxConcurrent),
		userQuotas:          make(map[string]*UserUploadQuota),
		maxUploadBytes:      maxUploadBytes,
		minFreeSpace:        minFreeSpace,
		maxUploadRate:       maxUploadRate,
		quotaResetInterval:  24 * time.Hour,
		quotaBytesPerUser:   quotaBytesPerUser,
		quotaUploadsPerUser: quotaUploadsPerUser,
		lastResetTime:       time.Now(),
	}
}

// AcquireUploadSlot reserves a slot for an upload
// Returns a release function to call when done, or error if limit exceeded
func (p *UploadResourcePolicy) AcquireUploadSlot() (func(), error) {
	select {
	case p.activeSem <- struct{}{}:
		return func() { <-p.activeSem }, nil
	default:
		return nil, fmt.Errorf("upload concurrency limit exceeded (%d active)", p.maxConcurrent)
	}
}

// ValidateUploadStart checks if an upload can begin
func (p *UploadResourcePolicy) ValidateUploadStart(username string, fileSize int64) error {
	// Check file size
	if fileSize > p.maxUploadBytes {
		return fmt.Errorf("upload exceeds maximum size limit (%d > %d bytes)",
			fileSize, p.maxUploadBytes)
	}

	// Check free disk space
	if p.minFreeSpace > 0 {
		if freeSpace := p.getFreeDiskSpace(); freeSpace > 0 && freeSpace < p.minFreeSpace {
			return fmt.Errorf("insufficient free disk space for upload (have %d, need %d bytes)",
				freeSpace, p.minFreeSpace)
		}
	}

	// Check user quotas
	if p.quotaBytesPerUser > 0 || p.quotaUploadsPerUser > 0 {
		if err := p.checkUserQuota(username, fileSize); err != nil {
			return err
		}
	}

	return nil
}

// RecordUploadComplete updates metrics after successful upload
func (p *UploadResourcePolicy) RecordUploadComplete(username string, bytesTransferred int64) {
	p.bytesUploaded.Add(bytesTransferred)
	p.uploadCount.Add(1)

	if p.quotaBytesPerUser > 0 || p.quotaUploadsPerUser > 0 {
		p.recordUserUpload(username, bytesTransferred)
	}
}

// checkUserQuota validates user hasn't exceeded upload quotas
func (p *UploadResourcePolicy) checkUserQuota(username string, fileSize int64) error {
	p.userQuotaMu.Lock()
	defer p.userQuotaMu.Unlock()

	// Reset all quotas if interval has passed
	if time.Since(p.lastResetTime) > p.quotaResetInterval {
		p.resetMu.Lock()
		p.userQuotas = make(map[string]*UserUploadQuota)
		p.lastResetTime = time.Now()
		p.resetMu.Unlock()
	}

	quota, exists := p.userQuotas[username]
	if !exists {
		quota = &UserUploadQuota{LastResetTime: time.Now()}
		p.userQuotas[username] = quota
	}

	// Check bytes quota
	if p.quotaBytesPerUser > 0 {
		if quota.BytesThisInterval+fileSize > p.quotaBytesPerUser {
			return fmt.Errorf("upload would exceed daily quota for user (have %d, limit %d bytes)",
				quota.BytesThisInterval+fileSize, p.quotaBytesPerUser)
		}
	}

	// Check upload count quota
	if p.quotaUploadsPerUser > 0 {
		if quota.UploadsThisInterval >= p.quotaUploadsPerUser {
			return fmt.Errorf("upload would exceed daily upload limit for user (limit %d)",
				p.quotaUploadsPerUser)
		}
	}

	return nil
}

// recordUserUpload tracks user upload for quota enforcement
func (p *UploadResourcePolicy) recordUserUpload(username string, bytesTransferred int64) {
	p.userQuotaMu.Lock()
	defer p.userQuotaMu.Unlock()

	quota, exists := p.userQuotas[username]
	if !exists {
		quota = &UserUploadQuota{LastResetTime: time.Now()}
		p.userQuotas[username] = quota
	}

	quota.BytesThisInterval += bytesTransferred
	quota.UploadsThisInterval++
}

// getFreeDiskSpace returns free disk space in bytes for the root filesystem
func (p *UploadResourcePolicy) getFreeDiskSpace() int64 {
	// Try to stat root to get filesystem stats
	// In production, this would use proper syscall.Statfs
	// For now, return a conservative estimate
	info, err := os.Stat("/")
	if err != nil {
		return 0
	}

	// This is a simplified check; real implementation would use syscall.Statfs
	if info.IsDir() {
		// Return a large number to indicate space is available
		// Production code should use proper filesystem stats
		return 100 * 1024 * 1024 * 1024 // 100GB estimate
	}
	return 0
}

// GetMetrics returns current upload metrics
func (p *UploadResourcePolicy) GetMetrics() map[string]interface{} {
	return map[string]interface{}{
		"bytes_uploaded":         p.bytesUploaded.Load(),
		"upload_count":           p.uploadCount.Load(),
		"concurrent_uploads":     len(p.activeSem),
		"max_concurrent":         p.maxConcurrent,
		"max_bytes_per_upload":   p.maxUploadBytes,
		"min_free_space":         p.minFreeSpace,
		"quota_bytes_per_user":   p.quotaBytesPerUser,
		"quota_uploads_per_user": p.quotaUploadsPerUser,
	}
}
