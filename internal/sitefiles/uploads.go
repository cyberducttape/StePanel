package sitefiles

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// MaxResumableUploadBytes caps one assembled customer file upload.
	MaxResumableUploadBytes int64 = 100 << 20
	// MaxUploadChunkBytes keeps each request and browser retry bounded.
	MaxUploadChunkBytes        int64 = 4 << 20
	UploadSessionTTL                 = 24 * time.Hour
	MaxActiveUploadSessions          = 256
	MaxUploadSessionsPerTenant       = 4
)

var uploadIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var uploadSitePattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// UploadSession is durable transfer metadata. Site and tenant are never
// returned by the public API; every operation checks them against the current
// authorized principal before exposing or changing a session.
type UploadSession struct {
	ID               string    `json:"id"`
	Site             string    `json:"site"`
	Tenant           string    `json:"tenant"`
	Path             string    `json:"path"`
	Size             int64     `json:"size"`
	Offset           int64     `json:"offset"`
	SHA256           string    `json:"sha256"`
	ExpectedRevision string    `json:"expected_revision,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// UploadManager stores resumable upload chunks in a private directory. The
// staged file is synced before its durable offset advances, so after a crash
// the manifest is authoritative and any uncommitted tail is truncated on the
// next chunk request.
type UploadManager struct {
	root    string
	mu      sync.Mutex
	beginMu sync.Mutex
	locks   map[string]*sync.Mutex
}

func NewUploadManager(root string) (*UploadManager, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return nil, errors.New("resumable upload directory must be an absolute path")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create resumable upload directory: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect resumable upload directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("resumable upload path must be a real directory")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("secure resumable upload directory: %w", err)
	}
	return &UploadManager{root: root, locks: make(map[string]*sync.Mutex)}, nil
}

func (m *UploadManager) sessionLock(id string) (*sync.Mutex, error) {
	if m == nil || !uploadIDPattern.MatchString(id) {
		return nil, errors.New("invalid upload ID")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	lock := m.locks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[id] = lock
	}
	return lock, nil
}

func (m *UploadManager) manifestPath(id string) string { return filepath.Join(m.root, id+".json") }
func (m *UploadManager) dataPath(id string) string     { return filepath.Join(m.root, id+".part") }

func (m *UploadManager) Begin(site, tenant, target string, size int64, digest, expectedRevision string) (UploadSession, error) {
	if !uploadSitePattern.MatchString(site) || strings.TrimSpace(tenant) == "" {
		return UploadSession{}, errors.New("upload requires an authorized site and tenant")
	}
	target, err := CleanTarget(target)
	if err != nil || target == "" {
		return UploadSession{}, errors.New("upload target must be a file inside the site document root")
	}
	if size < 1 || size > MaxResumableUploadBytes {
		return UploadSession{}, ErrTooLarge
	}
	if len(digest) != sha256.Size*2 {
		return UploadSession{}, errors.New("upload SHA-256 must be 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return UploadSession{}, errors.New("upload SHA-256 must be 64 hexadecimal characters")
	}
	if expectedRevision != "" && !ValidRevision(expectedRevision) {
		return UploadSession{}, errors.New("expected revision is invalid")
	}
	m.beginMu.Lock()
	defer m.beginMu.Unlock()
	active, tenantActive, err := m.activeCounts(tenant, time.Now().UTC())
	if err != nil {
		return UploadSession{}, err
	}
	if active >= MaxActiveUploadSessions || tenantActive >= MaxUploadSessionsPerTenant {
		return UploadSession{}, ErrUploadSessionLimit
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return UploadSession{}, fmt.Errorf("create upload ID: %w", err)
	}
	id := hex.EncodeToString(entropy[:])
	now := time.Now().UTC()
	session := UploadSession{ID: id, Site: site, Tenant: tenant, Path: target, Size: size, SHA256: strings.ToLower(digest), ExpectedRevision: expectedRevision, CreatedAt: now, UpdatedAt: now}
	data, err := os.OpenFile(m.dataPath(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return UploadSession{}, fmt.Errorf("create upload staging file: %w", err)
	}
	if err := data.Sync(); err != nil {
		_ = data.Close()
		_ = os.Remove(m.dataPath(id))
		return UploadSession{}, fmt.Errorf("sync upload staging file: %w", err)
	}
	if err := data.Close(); err != nil {
		_ = os.Remove(m.dataPath(id))
		return UploadSession{}, fmt.Errorf("close upload staging file: %w", err)
	}
	if err := m.save(session); err != nil {
		_ = os.Remove(m.dataPath(id))
		return UploadSession{}, err
	}
	return session, nil
}

func (m *UploadManager) activeCounts(tenant string, now time.Time) (int, int, error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return 0, 0, err
	}
	active, tenantActive := 0, 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !uploadIDPattern.MatchString(id) {
			continue
		}
		session, err := m.load(id)
		if err != nil {
			active++ // malformed state consumes a slot until operator cleanup
			continue
		}
		if now.Sub(session.UpdatedAt) > UploadSessionTTL {
			continue
		}
		active++
		if session.Tenant == tenant {
			tenantActive++
		}
	}
	return active, tenantActive, nil
}

func (m *UploadManager) Info(id, site, tenant string) (UploadSession, error) {
	lock, err := m.sessionLock(id)
	if err != nil {
		return UploadSession{}, err
	}
	lock.Lock()
	defer lock.Unlock()
	session, err := m.load(id)
	if err != nil {
		return UploadSession{}, err
	}
	if session.Site != site || session.Tenant != tenant {
		return UploadSession{}, os.ErrNotExist
	}
	return session, nil
}

// Append stores exactly the next byte range. Repeated or out-of-order chunks
// return the current offset without altering staged bytes.
func (m *UploadManager) Append(ctx context.Context, id, site, tenant string, start, total int64, chunk io.Reader) (UploadSession, error) {
	lock, err := m.sessionLock(id)
	if err != nil {
		return UploadSession{}, err
	}
	lock.Lock()
	defer lock.Unlock()
	session, err := m.load(id)
	if err != nil {
		return UploadSession{}, err
	}
	if session.Site != site || session.Tenant != tenant {
		return UploadSession{}, os.ErrNotExist
	}
	if total != session.Size || start != session.Offset {
		return session, ErrUploadOffset
	}
	if chunk == nil {
		return session, errors.New("upload chunk is required")
	}
	file, err := os.OpenFile(m.dataPath(id), os.O_WRONLY, 0)
	if err != nil {
		return session, fmt.Errorf("open upload staging file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return session, fmt.Errorf("inspect upload staging file: %w", err)
	}
	if info.Size() < session.Offset {
		return session, errors.New("upload staging file is shorter than its durable offset")
	}
	if info.Size() > session.Offset {
		if err := file.Truncate(session.Offset); err != nil {
			return session, fmt.Errorf("discard uncommitted upload tail: %w", err)
		}
	}
	limited := io.LimitReader(chunk, MaxUploadChunkBytes+1)
	written, err := io.Copy(&fileAtWriter{file: file, offset: session.Offset}, limited)
	if err != nil {
		_ = file.Truncate(session.Offset)
		return session, fmt.Errorf("write upload chunk: %w", err)
	}
	if written == 0 || written > MaxUploadChunkBytes || session.Offset+written > session.Size || (written != MaxUploadChunkBytes && session.Offset+written != session.Size) {
		_ = file.Truncate(session.Offset)
		return session, errors.New("upload chunks must be full-sized except for the final chunk")
	}
	if err := ctx.Err(); err != nil {
		_ = file.Truncate(session.Offset)
		return session, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Truncate(session.Offset)
		return session, fmt.Errorf("sync upload chunk: %w", err)
	}
	previousOffset := session.Offset
	session.Offset += written
	session.UpdatedAt = time.Now().UTC()
	if err := m.save(session); err != nil {
		session.Offset = previousOffset
		_ = file.Truncate(previousOffset)
		_ = file.Sync()
		return session, err
	}
	return session, nil
}

type fileAtWriter struct {
	file   *os.File
	offset int64
}

func (w *fileAtWriter) Write(p []byte) (int, error) {
	n, err := w.file.WriteAt(p, w.offset)
	w.offset += int64(n)
	return n, err
}

// Complete verifies the full assembled checksum before invoking the atomic
// site-store publication. On any failure the session remains resumable.
func (m *UploadManager) Complete(ctx context.Context, id, site, tenant string, publish func(UploadSession, io.Reader) error) error {
	if publish == nil {
		return errors.New("upload publication handler is required")
	}
	lock, err := m.sessionLock(id)
	if err != nil {
		return err
	}
	lock.Lock()
	defer lock.Unlock()
	session, err := m.load(id)
	if err != nil {
		return err
	}
	if session.Site != site || session.Tenant != tenant {
		return os.ErrNotExist
	}
	if session.Offset != session.Size {
		return ErrUploadIncomplete
	}
	file, err := os.Open(m.dataPath(id))
	if err != nil {
		return fmt.Errorf("open completed upload: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != session.Size {
		return errors.New("completed upload size does not match its manifest")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, contextReader{ctx: ctx, r: file}); err != nil {
		return fmt.Errorf("verify completed upload: %w", err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != session.SHA256 {
		return ErrUploadChecksum
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind completed upload: %w", err)
	}
	session.UpdatedAt = time.Now().UTC()
	if err := m.save(session); err != nil {
		return err
	}
	if err := publish(session, contextReader{ctx: ctx, r: file}); err != nil {
		return err
	}
	if err := os.Remove(m.dataPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove published upload staging file: %w", err)
	}
	if err := os.Remove(m.manifestPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove published upload manifest: %w", err)
	}
	return syncDirectory(m.root)
}

func (m *UploadManager) Cancel(id, site, tenant string) error {
	lock, err := m.sessionLock(id)
	if err != nil {
		return err
	}
	lock.Lock()
	defer lock.Unlock()
	session, err := m.load(id)
	if err != nil {
		return err
	}
	if session.Site != site || session.Tenant != tenant {
		return os.ErrNotExist
	}
	if err := os.Remove(m.dataPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(m.manifestPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(m.root)
}

// PruneExpired removes inactive sessions. It returns their IDs so the caller
// can release matching durable capacity reservations.
func (m *UploadManager) PruneExpired(now time.Time) []string {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil
	}
	var removed []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".part") {
			id := strings.TrimSuffix(name, ".part")
			if !uploadIDPattern.MatchString(id) {
				continue
			}
			if _, err := os.Lstat(m.manifestPath(id)); errors.Is(err, os.ErrNotExist) {
				if info, statErr := entry.Info(); statErr == nil && now.Sub(info.ModTime()) > UploadSessionTTL {
					lock, lockErr := m.sessionLock(id)
					if lockErr == nil {
						lock.Lock()
						_ = os.Remove(m.dataPath(id))
						lock.Unlock()
					}
				}
			}
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		lock, err := m.sessionLock(id)
		if err != nil {
			continue
		}
		lock.Lock()
		session, err := m.load(id)
		if err == nil && now.Sub(session.UpdatedAt) > UploadSessionTTL {
			dataErr := os.Remove(m.dataPath(id))
			if dataErr == nil || errors.Is(dataErr, os.ErrNotExist) {
				manifestErr := os.Remove(m.manifestPath(id))
				if manifestErr == nil || errors.Is(manifestErr, os.ErrNotExist) {
					removed = append(removed, id)
				}
			}
		}
		lock.Unlock()
	}
	if len(removed) > 0 {
		_ = syncDirectory(m.root)
	}
	return removed
}

func (m *UploadManager) load(id string) (UploadSession, error) {
	var session UploadSession
	data, err := os.ReadFile(m.manifestPath(id))
	if err != nil {
		return session, err
	}
	if len(data) > 16<<10 {
		return session, errors.New("upload session manifest exceeds size limit")
	}
	if err := json.Unmarshal(data, &session); err != nil {
		return session, fmt.Errorf("decode upload session: %w", err)
	}
	if session.ID != id || session.Offset < 0 || session.Offset > session.Size || session.Size < 1 || session.Size > MaxResumableUploadBytes || !uploadSitePattern.MatchString(session.Site) || strings.TrimSpace(session.Tenant) == "" {
		return session, errors.New("upload session manifest is invalid")
	}
	if target, err := CleanTarget(session.Path); err != nil || target != session.Path {
		return session, errors.New("upload session path is invalid")
	}
	if len(session.SHA256) != sha256.Size*2 {
		return session, errors.New("upload session checksum is invalid")
	}
	if _, err := hex.DecodeString(session.SHA256); err != nil {
		return session, errors.New("upload session checksum is invalid")
	}
	if session.ExpectedRevision != "" && !ValidRevision(session.ExpectedRevision) {
		return session, errors.New("upload session revision is invalid")
	}
	return session, nil
}

func (m *UploadManager) save(session UploadSession) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(m.root, ".manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create upload manifest: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write upload manifest: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync upload manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, m.manifestPath(session.ID)); err != nil {
		return fmt.Errorf("publish upload manifest: %w", err)
	}
	return syncDirectory(m.root)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

var (
	ErrUploadOffset       = errors.New("upload offset does not match the next expected byte")
	ErrUploadIncomplete   = errors.New("upload is incomplete")
	ErrUploadChecksum     = errors.New("upload checksum does not match the expected SHA-256")
	ErrUploadSessionLimit = errors.New("too many resumable uploads are active")
)
