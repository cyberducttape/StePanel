package stepanel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
	"github.com/cyberducttape/StePanel/internal/sitefiles"
)

// Customer file manager (ADR-0007). Every operation is confined to one placed
// site's document root and, on a production host, runs as the hosting
// account's Unix user through the root broker and stepanel-filectl.
//
//	GET    /api/sites/files/{site}?path=P             list a directory
//	GET    /api/sites/files/{site}/content?path=P     read a file (JSON)
//	GET    /api/sites/files/{site}/content?path=P&download=1
//	PUT    /api/sites/files/{site}/content?path=P     replace a file (raw body)
//	POST   /api/sites/files/{site}/mkdir   {"path"}
//	POST   /api/sites/files/{site}/rename  {"path","destination"}
//	POST   /api/sites/files/{site}/delete  {"path"}

const siteFilesPrefix = "/api/sites/files/"

const (
	maxConcurrentSiteFileReadsPerTenant     = 4
	maxConcurrentSiteFileReadsHost          = 32
	maxConcurrentSiteFileDownloadsPerTenant = 2
	maxConcurrentSiteFileDownloadsHost      = 8
	maxConcurrentSiteFileWritesPerTenant    = 2
	maxConcurrentSiteFileWritesHost         = 8
)

// siteFileAdmissionLimiter bounds file operations per tenant and across the
// panel process. Counts exist only while requests are active, so old or
// revoked tenants do not accumulate keys. Admission is nonblocking: a tenant
// at its limit cannot occupy slots reserved for other active tenants.
type siteFileAdmissionLimiter struct {
	mu        sync.Mutex
	perTenant int
	global    int
	active    map[string]int
	total     int
}

func newSiteFileAdmissionLimiter(perTenant, global int) *siteFileAdmissionLimiter {
	return &siteFileAdmissionLimiter{
		perTenant: perTenant,
		global:    global,
		active:    make(map[string]int),
	}
}

func (l *siteFileAdmissionLimiter) acquire(tenantID string) (func(), bool) {
	if strings.TrimSpace(tenantID) == "" || l.perTenant < 1 || l.global < 1 {
		return nil, false
	}
	l.mu.Lock()
	if l.active[tenantID] >= l.perTenant || l.total >= l.global {
		l.mu.Unlock()
		return nil, false
	}
	l.active[tenantID]++
	l.total++
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.active[tenantID] <= 1 {
				delete(l.active, tenantID)
			} else {
				l.active[tenantID]--
			}
			l.total--
		})
	}, true
}

var siteFileReads = newSiteFileAdmissionLimiter(
	maxConcurrentSiteFileReadsPerTenant,
	maxConcurrentSiteFileReadsHost,
)

var siteFileWrites = newSiteFileAdmissionLimiter(
	maxConcurrentSiteFileWritesPerTenant,
	maxConcurrentSiteFileWritesHost,
)

var siteFileDownloads = newSiteFileAdmissionLimiter(
	maxConcurrentSiteFileDownloadsPerTenant,
	maxConcurrentSiteFileDownloadsHost,
)

// errSiteNotPlaced reports a legacy site, whose tree the file manager does
// not manage.
var errSiteNotPlaced = errors.New("the file manager is available for sites in a hosting account")

// brokerFileStore is the production sitefiles.Store.
type brokerFileStore struct{ cfg Config }

type siteFileUploadReservation struct {
	mu          sync.Mutex
	reservation *capacityReservation
	baseOffset  int64
	offset      int64
}

func (a *App) siteFileUploadManager() (*sitefiles.UploadManager, string, error) {
	a.fileUploadMu.Lock()
	defer a.fileUploadMu.Unlock()
	root, err := filepath.Abs(filepath.Join(a.Config.ImportRoot, "site-file-uploads"))
	if err != nil {
		return nil, "", err
	}
	if a.fileUploads == nil {
		manager, err := sitefiles.NewUploadManager(root)
		if err != nil {
			return nil, "", err
		}
		a.fileUploads = manager
		if a.fileUploadReservations == nil {
			a.fileUploadReservations = make(map[string]*siteFileUploadReservation)
		}
	}
	return a.fileUploads, root, nil
}

func publicSiteFileUpload(session sitefiles.UploadSession) map[string]any {
	return map[string]any{"id": session.ID, "path": session.Path, "size": session.Size, "offset": session.Offset, "sha256": session.SHA256}
}

func (a *App) uploadReservation(id string, session sitefiles.UploadSession, root, targetRoot string) (*siteFileUploadReservation, error) {
	a.fileUploadMu.Lock()
	defer a.fileUploadMu.Unlock()
	if current := a.fileUploadReservations[id]; current != nil {
		return current, nil
	}
	remaining := session.Size - session.Offset
	demands := []capacityDemand{{Path: root, Bytes: uint64(remaining)}, {Path: os.TempDir(), Bytes: uint64(remaining)}, {Path: targetRoot, Bytes: uint64(remaining)}}
	reservation, err := a.capacityLedger().reserve(a.Config, "resumable site file upload", root, demands)
	if err != nil {
		return nil, err
	}
	current := &siteFileUploadReservation{reservation: reservation, baseOffset: session.Offset, offset: session.Offset}
	a.fileUploadReservations[id] = current
	return current, nil
}

func (a *App) releaseUploadReservation(id string) {
	a.fileUploadMu.Lock()
	current := a.fileUploadReservations[id]
	delete(a.fileUploadReservations, id)
	a.fileUploadMu.Unlock()
	if current != nil {
		current.mu.Lock()
		defer current.mu.Unlock()
	}
	if current != nil && current.reservation != nil {
		current.reservation.release()
	}
}

var _ sitefiles.SiteAccess = AuthorizedSite{}

func (s brokerFileStore) run(ctx context.Context, req rootbroker.FilesRequest) (rootbroker.FilesResponse, error) {
	client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", s.cfg.WebRoot)
	if err != nil {
		return rootbroker.FilesResponse{}, err
	}
	return client.Files(ctx, req)
}

type authorizedFileTarget struct {
	site     string
	tenantID string
}

func resolveAuthorizedFileTarget(site sitefiles.SiteAccess) (authorizedFileTarget, error) {
	if site == nil || site.SiteID() == "" || site.TenantID() == "" {
		return authorizedFileTarget{}, errors.New("file operation requires an authorized site and tenant")
	}
	return authorizedFileTarget{site: site.SiteID(), tenantID: site.TenantID()}, nil
}

func (s brokerFileStore) List(ctx context.Context, site sitefiles.SiteAccess, rel string) (sitefiles.Listing, error) {
	target, err := resolveAuthorizedFileTarget(site)
	if err != nil {
		return sitefiles.Listing{}, err
	}
	resp, err := s.run(ctx, rootbroker.FilesRequest{Action: "list", Site: target.site, TenantID: target.tenantID, Path: rel})
	if err != nil {
		return sitefiles.Listing{}, err
	}
	if resp.Listing == nil {
		return sitefiles.Listing{}, errors.New("file listing is missing from the broker response")
	}
	return *resp.Listing, nil
}

func (s brokerFileStore) Read(ctx context.Context, site sitefiles.SiteAccess, rel string) ([]byte, error) {
	target, err := resolveAuthorizedFileTarget(site)
	if err != nil {
		return nil, err
	}
	resp, err := s.run(ctx, rootbroker.FilesRequest{Action: "read", Site: target.site, TenantID: target.tenantID, Path: rel})
	return resp.Content, err
}

func (s brokerFileStore) Open(ctx context.Context, site sitefiles.SiteAccess, rel string) (io.ReadCloser, int64, error) {
	target, err := resolveAuthorizedFileTarget(site)
	if err != nil {
		return nil, 0, err
	}
	client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", s.cfg.WebRoot)
	if err != nil {
		return nil, 0, err
	}
	return client.FilesOpen(ctx, target.site, target.tenantID, rel)
}

func (s brokerFileStore) Write(ctx context.Context, site sitefiles.SiteAccess, rel string, content io.Reader, expectedRevision string) error {
	target, err := resolveAuthorizedFileTarget(site)
	if err != nil {
		return err
	}
	client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", s.cfg.WebRoot)
	if err != nil {
		return err
	}
	return client.FilesWrite(ctx, target.site, target.tenantID, rel, content, expectedRevision)
}

func (s brokerFileStore) WriteLarge(ctx context.Context, site sitefiles.SiteAccess, rel string, content io.Reader, expectedRevision string) error {
	target, err := resolveAuthorizedFileTarget(site)
	if err != nil {
		return err
	}
	client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", s.cfg.WebRoot)
	if err != nil {
		return err
	}
	return client.FilesWriteLarge(ctx, target.site, target.tenantID, rel, content, expectedRevision)
}

func (s brokerFileStore) Mkdir(ctx context.Context, site sitefiles.SiteAccess, rel string) error {
	target, err := resolveAuthorizedFileTarget(site)
	if err != nil {
		return err
	}
	_, err = s.run(ctx, rootbroker.FilesRequest{Action: "mkdir", Site: target.site, TenantID: target.tenantID, Path: rel})
	return err
}

func (s brokerFileStore) Rename(ctx context.Context, site sitefiles.SiteAccess, rel, destination string) error {
	target, err := resolveAuthorizedFileTarget(site)
	if err != nil {
		return err
	}
	_, err = s.run(ctx, rootbroker.FilesRequest{Action: "rename", Site: target.site, TenantID: target.tenantID, Path: rel, Destination: destination})
	return err
}

func (s brokerFileStore) Delete(ctx context.Context, site sitefiles.SiteAccess, rel string) error {
	target, err := resolveAuthorizedFileTarget(site)
	if err != nil {
		return err
	}
	_, err = s.run(ctx, rootbroker.FilesRequest{Action: "delete", Site: target.site, TenantID: target.tenantID, Path: rel})
	return err
}

// siteFileStore returns the store for this host: the broker on production
// (and lab broker) hosts, the document root itself in development.
func (a *App) siteFileStore() sitefiles.Store {
	if a.Config.Production || labDirectRootBrokerEnabled() {
		return brokerFileStore{cfg: a.Config}
	}
	cfg := a.Config
	return sitefiles.Local{DocRoot: func(site string) (string, error) {
		location, err := siteLocationFor(cfg, site)
		if err != nil {
			return "", err
		}
		if !location.Placed {
			return "", errSiteNotPlaced
		}
		return existingRealDirectory(location.DocRoot, "site document root")
	}}
}

func (a *App) siteFiles(w http.ResponseWriter, r *http.Request) {
	site, remainder, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, siteFilesPrefix), "/")
	if safeUser(site) != site || site == "" {
		http.Error(w, "invalid site", http.StatusUnprocessableEntity)
		return
	}
	access, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", http.StatusForbidden)
	if !ok {
		return
	}
	location, err := siteLocationFor(a.Config, site)
	if err != nil {
		http.Error(w, "site location is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !location.Placed {
		http.Error(w, errSiteNotPlaced.Error(), http.StatusConflict)
		return
	}
	store := a.siteFileStore()
	operation, tail, _ := strings.Cut(remainder, "/")
	if operation == "uploads" {
		a.siteFileUpload(w, r, site, tail, location.DocRoot, access, store)
		return
	}
	switch {
	case operation == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		a.siteFilesList(w, r, store, access)
	case operation == "content" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		a.siteFilesRead(w, r, store, access)
	case operation == "content" && r.Method == http.MethodPut:
		rel, err := sitefiles.CleanTarget(r.URL.Query().Get("path"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		expectedRevision := r.Header.Get("If-Match")
		if expectedRevision != "" && !sitefiles.ValidRevision(expectedRevision) {
			http.Error(w, "If-Match must contain a StePanel file revision", http.StatusBadRequest)
			return
		}
		releaseWrite, admitted := siteFileWrites.acquire(access.TenantID())
		if !admitted {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "too many file uploads are in progress for this tenant or host; try again shortly", http.StatusTooManyRequests)
			return
		}
		defer releaseWrite()
		r.Body = http.MaxBytesReader(w, r.Body, sitefiles.MaxWriteBytes)
		a.siteFilesMutate(w, r, access, "site.files.write", rel+" (streamed upload)", func(ctx context.Context) error {
			return store.Write(ctx, access, rel, r.Body, expectedRevision)
		})
	case r.Method == http.MethodPost && (operation == "mkdir" || operation == "rename" || operation == "delete"):
		var input struct {
			Path        string `json:"path"`
			Destination string `json:"destination"`
		}
		if err := decodeJSON(w, r, 16<<10, &input); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		rel, err := sitefiles.CleanTarget(input.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		switch operation {
		case "mkdir":
			a.siteFilesMutate(w, r, access, "site.files.mkdir", rel, func(ctx context.Context) error { return store.Mkdir(ctx, access, rel) })
		case "delete":
			a.siteFilesMutate(w, r, access, "site.files.delete", rel, func(ctx context.Context) error { return store.Delete(ctx, access, rel) })
		case "rename":
			destination, err := sitefiles.CleanTarget(input.Destination)
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnprocessableEntity)
				return
			}
			a.siteFilesMutate(w, r, access, "site.files.rename", rel+" -> "+destination, func(ctx context.Context) error {
				return store.Rename(ctx, access, rel, destination)
			})
		}
	default:
		http.Error(w, "unsupported file manager operation", http.StatusMethodNotAllowed)
	}
}

func (a *App) siteFileUpload(w http.ResponseWriter, r *http.Request, site string, tail, targetRoot string, access sitefiles.SiteAccess, store sitefiles.Store) {
	if !a.requireCustomerScope(w, r, "files:write") {
		return
	}
	if !a.Auth.CSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	manager, root, err := a.siteFileUploadManager()
	if err != nil {
		http.Error(w, "large upload storage is unavailable", http.StatusServiceUnavailable)
		return
	}
	// Expired resumptions release their corresponding capacity holds.
	for _, id := range manager.PruneExpired(time.Now()) {
		a.releaseUploadReservation(id)
	}

	if tail == "" && r.Method == http.MethodPost {
		var input struct {
			Path             string `json:"path"`
			Size             int64  `json:"size"`
			SHA256           string `json:"sha256"`
			ExpectedRevision string `json:"expected_revision"`
		}
		if err := decodeJSON(w, r, 16<<10, &input); err != nil {
			http.Error(w, "invalid upload request", http.StatusBadRequest)
			return
		}
		rel, err := sitefiles.CleanTarget(input.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if input.Size < 1 || input.Size > sitefiles.MaxResumableUploadBytes {
			http.Error(w, sitefiles.ErrTooLarge.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		release, admitted := siteFileWrites.acquire(access.TenantID())
		if !admitted {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "too many uploads are in progress", http.StatusTooManyRequests)
			return
		}
		defer release()
		if err := os.MkdirAll(root, 0o700); err != nil {
			http.Error(w, "upload storage is unavailable", http.StatusServiceUnavailable)
			return
		}
		// The broker stages once before its atomic same-directory publish. Hold
		// capacity for both the durable resumable file and that bounded broker
		// staging copy, including when they share a filesystem.
		demands := []capacityDemand{{Path: root, Bytes: uint64(input.Size)}, {Path: os.TempDir(), Bytes: uint64(input.Size)}, {Path: targetRoot, Bytes: uint64(input.Size)}}
		reservation, err := a.capacityLedger().reserve(a.Config, "resumable site file upload", root, demands)
		if err != nil {
			a.writeUploadError(w, r, err)
			return
		}
		session, err := manager.Begin(site, access.TenantID(), rel, input.Size, input.SHA256, input.ExpectedRevision)
		if err != nil {
			reservation.release()
			if errors.Is(err, sitefiles.ErrUploadSessionLimit) {
				w.Header().Set("Retry-After", "300")
				http.Error(w, err.Error(), http.StatusTooManyRequests)
				return
			}
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		a.fileUploadMu.Lock()
		if a.fileUploadReservations == nil {
			a.fileUploadReservations = make(map[string]*siteFileUploadReservation)
		}
		a.fileUploadReservations[session.ID] = &siteFileUploadReservation{reservation: reservation}
		a.fileUploadMu.Unlock()
		writeJSON(w, http.StatusCreated, publicSiteFileUpload(session))
		return
	}
	id, action, hasAction := strings.Cut(tail, "/")
	if !hasAction {
		action = ""
	}
	session, err := manager.Info(id, site, access.TenantID())
	if err != nil {
		http.Error(w, "upload session not found", http.StatusNotFound)
		return
	}
	if action == "" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, publicSiteFileUpload(session))
		return
	}
	if action == "" && r.Method == http.MethodDelete {
		if err := manager.Cancel(id, site, access.TenantID()); err != nil {
			http.Error(w, "could not cancel upload", http.StatusConflict)
			return
		}
		a.releaseUploadReservation(id)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if action == "" && r.Method == http.MethodPut {
		release, admitted := siteFileWrites.acquire(access.TenantID())
		if !admitted {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "too many uploads are in progress", http.StatusTooManyRequests)
			return
		}
		defer release()
		start, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
		if err != nil || start < 0 {
			http.Error(w, "Upload-Offset is required", http.StatusBadRequest)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, sitefiles.MaxUploadChunkBytes+1)
		guardUploadBody(w, r)
		uploadReservation, err := a.uploadReservation(id, session, root, targetRoot)
		if err != nil {
			a.writeUploadError(w, r, err)
			return
		}
		updated, err := manager.Append(r.Context(), id, site, access.TenantID(), start, session.Size, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		uploadReservation.mu.Lock()
		defer uploadReservation.mu.Unlock()
		if updated.Offset > uploadReservation.offset {
			if err := uploadReservation.reservation.consume(updated.Offset - uploadReservation.baseOffset); err != nil {
				http.Error(w, "upload capacity accounting failed; the session is resumable", http.StatusServiceUnavailable)
				return
			}
			uploadReservation.offset = updated.Offset
		}
		writeJSON(w, http.StatusOK, publicSiteFileUpload(updated))
		return
	}
	if action == "complete" && r.Method == http.MethodPost {
		a.siteFilesMutate(w, r, access, "site.files.upload.completed", session.Path, func(ctx context.Context) error {
			large, ok := store.(sitefiles.LargeWriter)
			if !ok {
				return errors.New("large file publication is unavailable")
			}
			return manager.Complete(ctx, id, site, access.TenantID(), func(s sitefiles.UploadSession, content io.Reader) error {
				return large.WriteLarge(ctx, access, s.Path, content, s.ExpectedRevision)
			})
		})
		if _, infoErr := manager.Info(id, site, access.TenantID()); errors.Is(infoErr, os.ErrNotExist) {
			a.releaseUploadReservation(id)
		}
		return
	}
	http.Error(w, "unsupported upload operation", http.StatusMethodNotAllowed)
}

func (a *App) siteFilesList(w http.ResponseWriter, r *http.Request, store sitefiles.Store, site sitefiles.SiteAccess) {
	if !a.requireCustomerScope(w, r, "files:read") {
		return
	}
	rel, err := sitefiles.Clean(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	release, admitted := siteFileReads.acquire(site.TenantID())
	if !admitted {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "too many file reads or directory listings are in progress; try again shortly", http.StatusTooManyRequests)
		return
	}
	defer release()
	listing, err := store.List(r.Context(), site, rel)
	if err != nil {
		writeSiteFilesError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (a *App) siteFilesRead(w http.ResponseWriter, r *http.Request, store sitefiles.Store, site sitefiles.SiteAccess) {
	if !a.requireCustomerScope(w, r, "files:read") {
		return
	}
	rel, err := sitefiles.CleanTarget(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		a.siteFilesDownload(w, r, store, site, rel)
		return
	}
	release, admitted := siteFileReads.acquire(site.TenantID())
	if !admitted {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "too many file reads or directory listings are in progress; try again shortly", http.StatusTooManyRequests)
		return
	}
	defer release()
	data, err := store.Read(r.Context(), site, rel)
	if err != nil {
		writeSiteFilesError(w, err)
		return
	}
	text := utf8.Valid(data) && !strings.ContainsRune(string(data), 0)
	result := map[string]any{"path": rel, "size": len(data), "text": text, "revision": sitefiles.Revision(data)}
	if text {
		result["content"] = string(data)
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) siteFilesDownload(w http.ResponseWriter, r *http.Request, store sitefiles.Store, site sitefiles.SiteAccess, rel string) {
	if !a.requireCustomerScope(w, r, "files:read") {
		return
	}
	release, admitted := siteFileDownloads.acquire(site.TenantID())
	if !admitted {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "too many file downloads are in progress for this tenant or host; try again shortly", http.StatusTooManyRequests)
		return
	}
	defer release()
	file, size, err := store.Open(r.Context(), site, rel)
	if err != nil {
		writeSiteFilesError(w, err)
		return
	}
	defer file.Close()
	if size < 0 || size > sitefiles.MaxDownloadBytes {
		http.Error(w, "file exceeds the download size limit", http.StatusRequestEntityTooLarge)
		return
	}
	// Never render customer content on the panel origin.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(path.Base(rel)))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	buffer := make([]byte, 64<<10)
	remaining := size
	for remaining > 0 {
		if err := controller.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			log.Printf("set file-download write deadline for %s: %v", site.SiteID(), err)
		}
		chunk := int64(len(buffer))
		if remaining < chunk {
			chunk = remaining
		}
		n, readErr := io.ReadFull(file, buffer[:int(chunk)])
		if n > 0 {
			written, writeErr := w.Write(buffer[:n])
			remaining -= int64(written)
			if writeErr != nil || written != n {
				return
			}
		}
		if readErr != nil {
			log.Printf("file download for %s ended before declared size: %v", site.SiteID(), readErr)
			return
		}
	}
}

// siteFilesMutate runs one file change under the site's mutation lease (the
// broker requires its fencing token) with a Class A audit record.
func (a *App) siteFilesMutate(w http.ResponseWriter, r *http.Request, site sitefiles.SiteAccess, action, detail string, change func(context.Context) error) {
	if !a.Auth.CSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if !a.requireCustomerScope(w, r, "files:write") {
		return
	}
	operationCtx, release, err := a.acquireSiteMutationLockContext(r.Context(), site.SiteID())
	if err != nil {
		http.Error(w, "the site has another change in progress; try again shortly", http.StatusConflict)
		return
	}
	defer release()
	intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), action, site.SiteID(), detail)
	if err != nil {
		refuseWithoutSecurityAudit(w)
		return
	}
	if err := change(operationCtx); err != nil {
		intent.Failed(err.Error())
		writeSiteFilesError(w, err)
		return
	}
	if err := operationCtx.Err(); err != nil {
		intent.Failed("site mutation lease lost")
		http.Error(w, "the change was interrupted because the site lock was lost; reload to check its state", http.StatusConflict)
		return
	}
	intent.Completed(detail)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// writeSiteFilesError shows the customer why an operation was refused. Only
// refusals about the customer's own files carry their message; anything else
// is a host failure and stays generic.
func writeSiteFilesError(w http.ResponseWriter, err error) {
	var refused *sitefiles.RefusedError
	switch {
	case errors.Is(err, sitefiles.ErrUploadIncomplete), errors.Is(err, sitefiles.ErrUploadOffset):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, sitefiles.ErrUploadChecksum):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, sitefiles.ErrRevisionConflict):
		http.Error(w, sitefiles.ErrRevisionConflict.Error(), http.StatusConflict)
	case errors.Is(err, sitefiles.ErrNotFound):
		http.Error(w, "the file or directory does not exist", http.StatusNotFound)
	case errors.Is(err, errSiteNotPlaced):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.As(err, &refused):
		http.Error(w, refused.Message, http.StatusUnprocessableEntity)
	case errors.Is(err, sitefiles.ErrTooLarge):
		http.Error(w, fmt.Sprintf("file content must be at most %d bytes", sitefiles.MaxWriteBytes), http.StatusRequestEntityTooLarge)
	case errors.Is(err, sitefiles.ErrDurabilityUncertain):
		http.Error(w, "the file was replaced, but crash durability could not be confirmed; check its contents before retrying", http.StatusServiceUnavailable)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "the file operation was interrupted", http.StatusServiceUnavailable)
	default:
		log.Printf("site file operation failed: %v", err)
		http.Error(w, "the file operation failed", http.StatusBadGateway)
	}
}
