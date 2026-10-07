package stepanel

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	httputil "github.com/cyberducttape/StePanel/internal/http"
	"github.com/cyberducttape/StePanel/internal/upload"
)

// errUploadLengthRequired reports an archive upload whose size cannot be
// admitted before its body is read: no Content-Length and no configured
// STEPANEL_MAX_UPLOAD_BYTES ceiling to reserve instead.
var errUploadLengthRequired = errors.New("archive uploads require a Content-Length header")

// uploadIdleTimeout bounds how long an admitted upload may receive no body
// bytes. A stalled client then releases its upload slot and capacity
// reservation instead of holding them until the 60-minute upload ceiling.
var uploadIdleTimeout = 2 * time.Minute

// capacityError reports that a filesystem cannot hold a workflow's demands
// plus the STEPANEL_MIN_FREE_BYTES reserve.
type capacityError struct{ message string }

func (e *capacityError) Error() string { return e.message }

// capacityDemand is the number of bytes a workflow will consume on the
// filesystem that holds Path.
type capacityDemand struct {
	Path  string
	Bytes uint64
}

type deviceDemand struct {
	device uint64
	path   string
	bytes  uint64
}

// groupDemands sums demands per filesystem, so an import root and a site
// root on the same device are charged together.
func groupDemands(workflow string, demands []capacityDemand) ([]deviceDemand, error) {
	var grouped []deviceDemand
	index := map[uint64]int{}
	for _, demand := range demands {
		device, err := filesystemDevice(demand.Path)
		if err != nil {
			return nil, fmt.Errorf("inspect %s capacity at %s: %w", workflow, demand.Path, err)
		}
		i, seen := index[device]
		if !seen {
			i = len(grouped)
			index[device] = i
			grouped = append(grouped, deviceDemand{device: device, path: demand.Path})
		}
		if demand.Bytes > ^uint64(0)-grouped[i].bytes {
			return nil, fmt.Errorf("%s capacity estimate overflow", workflow)
		}
		grouped[i].bytes += demand.Bytes
	}
	return grouped, nil
}

func filesystemDevice(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("filesystem identity unavailable")
	}
	return uint64(stat.Dev), nil
}

// capacityLedger records free space promised to admitted uploads but not yet
// written. Admission charges outstanding promises against free space, so
// concurrent uploads cannot each claim the same bytes. The zero value is
// ready to use.
type capacityLedger struct {
	mu   sync.Mutex
	held map[uint64]uint64
}

// capacityReservation is one upload's outstanding promise.
type capacityReservation struct {
	ledger       *capacityLedger
	workflow     string
	reserve      uint64
	held         map[uint64]uint64
	stagingPath  string
	stagingDev   uint64
	consumedSize uint64
}

// check verifies that every filesystem can absorb the demands on top of
// outstanding reservations and the configured reserve, without holding
// anything. Durable jobs use it: their archive is already on disk.
func (l *capacityLedger) check(cfg Config, workflow string, demands []capacityDemand) error {
	_, err := l.admit(cfg, workflow, demands, false)
	return err
}

// reserve is check plus a hold that lasts until release. stagingPath names
// the filesystem the upload is streamed into; consume shrinks the hold there
// as bytes land.
func (l *capacityLedger) reserve(cfg Config, workflow, stagingPath string, demands []capacityDemand) (*capacityReservation, error) {
	reservation, err := l.admit(cfg, workflow, demands, true)
	if err != nil {
		return nil, err
	}
	device, err := filesystemDevice(stagingPath)
	if err != nil {
		reservation.release()
		return nil, fmt.Errorf("inspect %s staging filesystem: %w", workflow, err)
	}
	reservation.stagingPath, reservation.stagingDev = stagingPath, device
	return reservation, nil
}

func (l *capacityLedger) admit(cfg Config, workflow string, demands []capacityDemand, hold bool) (*capacityReservation, error) {
	grouped, err := groupDemands(workflow, demands)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, demand := range grouped {
		outstanding := l.held[demand.device]
		if demand.bytes > ^uint64(0)-cfg.MinFreeBytes || demand.bytes+cfg.MinFreeBytes > ^uint64(0)-outstanding {
			return nil, fmt.Errorf("%s capacity estimate overflow", workflow)
		}
		required := demand.bytes + cfg.MinFreeBytes
		free, err := availableBytes(demand.path)
		if err != nil {
			return nil, fmt.Errorf("inspect %s capacity at %s: %w", workflow, demand.path, err)
		}
		if free < required+outstanding {
			message := fmt.Sprintf("insufficient free space at %s: %d bytes available, %d required for this %s", demand.path, free, required, workflow)
			if outstanding > 0 {
				message += fmt.Sprintf(" plus %d reserved by in-progress uploads", outstanding)
			}
			return nil, &capacityError{message}
		}
	}
	reservation := &capacityReservation{ledger: l, workflow: workflow, reserve: cfg.MinFreeBytes, held: map[uint64]uint64{}}
	if hold {
		if l.held == nil {
			l.held = map[uint64]uint64{}
		}
		for _, demand := range grouped {
			l.held[demand.device] += demand.bytes
			reservation.held[demand.device] = demand.bytes
		}
	}
	return reservation, nil
}

// consume records that the upload has written size bytes in total to its
// staging filesystem, releasing that much of the hold, and verifies that
// free space still covers every outstanding reservation plus the reserve.
// Upload streams call it periodically so a consumer outside the ledger
// cannot fill the disk underneath admitted uploads.
func (r *capacityReservation) consume(size int64) error {
	if r == nil || size < 0 {
		return nil
	}
	l := r.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	if written := uint64(size); written > r.consumedSize {
		landed := min(written-r.consumedSize, r.held[r.stagingDev])
		r.held[r.stagingDev] -= landed
		l.held[r.stagingDev] -= landed
		r.consumedSize = written
	}
	free, err := availableBytes(r.stagingPath)
	if err != nil {
		return fmt.Errorf("inspect %s capacity at %s: %w", r.workflow, r.stagingPath, err)
	}
	if required := saturatingAdd(l.held[r.stagingDev], r.reserve); free < required {
		return &capacityError{fmt.Sprintf("free space at %s fell to %d bytes during this %s; %d are needed for in-progress uploads and the reserve", r.stagingPath, free, r.workflow, required)}
	}
	return nil
}

// release returns whatever the reservation still holds. It is idempotent.
func (r *capacityReservation) release() {
	if r == nil {
		return
	}
	l := r.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	for device, bytes := range r.held {
		l.held[device] -= bytes
		delete(r.held, device)
	}
}

// grow adds a newly measured demand to an existing reservation before the
// workflow starts the write that needs it. This is used by backups because
// database dump sizes are not knowable until the helper has produced them.
func (r *capacityReservation) grow(path string, bytes uint64) error {
	if r == nil || bytes == 0 {
		return nil
	}
	device, err := filesystemDevice(path)
	if err != nil {
		return fmt.Errorf("inspect %s capacity at %s: %w", r.workflow, path, err)
	}
	l := r.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	if bytes > ^uint64(0)-l.held[device] {
		return fmt.Errorf("%s capacity estimate overflow", r.workflow)
	}
	free, err := availableBytes(path)
	if err != nil {
		return fmt.Errorf("inspect %s capacity at %s: %w", r.workflow, path, err)
	}
	if free < saturatingAdd(saturatingAdd(l.held[device], bytes), r.reserve) {
		return &capacityError{fmt.Sprintf("insufficient free space at %s for an additional %d bytes needed by this %s", path, bytes, r.workflow)}
	}
	l.held[device] += bytes
	r.held[device] += bytes
	return nil
}

// heldBytes reports the bytes currently promised on path's filesystem.
func (l *capacityLedger) heldBytes(path string) uint64 {
	device, err := filesystemDevice(path)
	if err != nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held[device]
}

// admitCapacity preserves the package-level capacity check used by older
// callers. New request paths should use capacityLedger so concurrent upload
// reservations are accounted for.
func admitCapacity(cfg Config, workflow string, demands []capacityDemand) error {
	return (&capacityLedger{}).check(cfg, workflow, demands)
}

// declaredUploadBytes is the archive size admitted before the body is read.
// Content-Length bounds the archive (multipart framing only adds to it); a
// streamed body without one is admitted at the configured upload ceiling.
func declaredUploadBytes(r *http.Request, maxUpload int64) (uint64, error) {
	switch {
	case r.ContentLength >= 0:
		return uint64(r.ContentLength), nil
	case maxUpload > 0:
		return uint64(maxUpload), nil
	default:
		return 0, errUploadLengthRequired
	}
}

// archiveUploadDemands budgets an archive before its body is accepted: the
// staged archive and the extracted tree in the import root, plus the
// site-manager staging tree beside the canonical sites. WPress archives are
// stored uncompressed, so each tree is about the archive size; a cPanel
// archive's expanded size is unknown until inspection, so the gzip size is a
// lower bound that the post-inspection check later refines.
func archiveUploadDemands(cfg Config, archive uint64) []capacityDemand {
	return []capacityDemand{
		{Path: cfg.ImportRoot, Bytes: saturatingAdd(archive, archive)},
		{Path: filepath.Join(cfg.WebRoot, "sites"), Bytes: archive},
	}
}

// stagedArchiveDemands budgets a durable job whose archive is already on
// disk: only the extracted tree and the site-manager staging tree remain.
func stagedArchiveDemands(cfg Config, expanded uint64) []capacityDemand {
	return []capacityDemand{
		{Path: cfg.ImportRoot, Bytes: expanded},
		{Path: filepath.Join(cfg.WebRoot, "sites"), Bytes: expanded},
	}
}

func saturatingAdd(a, b uint64) uint64 {
	if a > ^uint64(0)-b {
		return ^uint64(0)
	}
	return a + b
}

// uploadSpaceCheck preserves the pre-ledger streaming check for callers that
// need a standalone, non-reserving callback. Upload handlers use
// capacityReservation.consume instead.
func uploadSpaceCheck(cfg Config, declared uint64) func(int64) error {
	return func(written int64) error {
		if written < 0 {
			return fmt.Errorf("invalid upload byte count")
		}
		remaining := uint64(0)
		if uint64(written) < declared {
			remaining = declared - uint64(written)
		}
		return admitCapacity(cfg, "upload", []capacityDemand{{Path: cfg.ImportRoot, Bytes: remaining}})
	}
}

// idleDeadlineBody refreshes the connection read deadline as body bytes
// arrive, so a stalled upload fails after uploadIdleTimeout while an active
// one may run until the request's absolute ceiling.
type idleDeadlineBody struct {
	body        io.ReadCloser
	controller  *http.ResponseController
	idle        time.Duration
	ceiling     time.Time
	nextRefresh time.Time
}

// guardUploadBody installs the idle deadline on r.Body. The ceiling is the
// route's context deadline (the 60-minute upload class).
func guardUploadBody(w http.ResponseWriter, r *http.Request) {
	ceiling, ok := r.Context().Deadline()
	if !ok {
		ceiling = time.Now().Add(httputil.DefaultTimeouts().UploadRead)
	}
	r.Body = &idleDeadlineBody{body: r.Body, controller: http.NewResponseController(w), idle: uploadIdleTimeout, ceiling: ceiling}
}

func (b *idleDeadlineBody) Read(p []byte) (int, error) {
	// Refreshing at most once a second keeps per-read overhead negligible;
	// a stall is therefore detected between idle-1s and idle after the last
	// byte arrived.
	if now := time.Now(); !now.Before(b.nextRefresh) {
		deadline := now.Add(b.idle)
		if deadline.After(b.ceiling) {
			deadline = b.ceiling
		}
		if err := b.controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return 0, fmt.Errorf("set upload read deadline: %w", err)
		}
		b.nextRefresh = now.Add(time.Second)
	}
	return b.body.Read(p)
}

func (b *idleDeadlineBody) Close() error { return b.body.Close() }

// uploadRejection is a client-facing validation failure raised before any
// archive byte is staged.
type uploadRejection struct {
	status  int
	message string
}

func (e *uploadRejection) Error() string { return e.message }

// writeUploadError maps admission and streaming failures to responses and
// records the rejection reason. Unclassified failures are logged with their
// cause; the client receives a generic message.
func writeUploadErrorResponse(w http.ResponseWriter, r *http.Request, err error) int {
	var rejection *uploadRejection
	var capacity *capacityError
	reason := uploadRejectInvalid
	switch {
	case errors.As(err, &rejection):
		http.Error(w, "invalid upload request", rejection.status)
	case errors.As(err, &capacity):
		reason = uploadRejectCapacity
		http.Error(w, capacity.Error(), http.StatusInsufficientStorage)
	case errors.Is(err, errUploadLengthRequired):
		reason = uploadRejectLength
		http.Error(w, "archive uploads require a Content-Length header", http.StatusLengthRequired)
	case errors.Is(err, upload.ErrFileTooLarge):
		reason = uploadRejectTooLarge
		http.Error(w, "upload exceeds the configured size limit", http.StatusRequestEntityTooLarge)
	case errors.Is(err, os.ErrDeadlineExceeded):
		reason = uploadRejectStalled
		http.Error(w, fmt.Sprintf("upload stalled: no data received for %s", uploadIdleTimeout), http.StatusRequestTimeout)
	case errors.Is(err, upload.ErrMissingFile):
		http.Error(w, "backup file is required", http.StatusBadRequest)
	case errors.Is(err, upload.ErrMalformed), errors.Is(err, upload.ErrUnexpectedPart), errors.Is(err, upload.ErrFieldTooLarge), errors.Is(err, http.ErrNotMultipart), errors.Is(err, http.ErrMissingBoundary):
		// Multipart parser errors can include request-controlled headers, field
		// names, or filenames. Keep those details out of the response; the
		// parser's text is not safe to reflect into an HTTP response.
		http.Error(w, "invalid multipart upload", http.StatusBadRequest)
	default:
		reason = uploadRejectInternal
		if r != nil {
			requestID, _ := r.Context().Value(requestIDContextKey{}).(string)
			log.Printf(`{"level":"error","request_id":%q,"path":%q,"error":%q}`, requestID, r.URL.Path, "stage upload: "+err.Error())
		}
		http.Error(w, "could not stage upload", http.StatusInternalServerError)
	}
	return reason
}

// writeUploadError is retained for package-level callers that predate the
// application metrics hook.
func writeUploadError(w http.ResponseWriter, err error) {
	writeUploadErrorResponse(w, nil, err)
}

func (a *App) writeUploadError(w http.ResponseWriter, r *http.Request, err error) {
	reason := writeUploadErrorResponse(w, r, err)
	a.Metrics.ObserveUploadRejected(reason)
}

// stageArchiveUpload admits and streams the "backup" file part of r into the
// object opts.Create opens. It reserves capacity for the declared archive
// size before reading the body, refreshes an idle read deadline while
// streaming, and charges written bytes against the reservation. On failure
// it has already written the response. On success the caller must release
// the reservation once the upload no longer needs its hold.
func (a *App) stageArchiveUpload(w http.ResponseWriter, r *http.Request, workflow string, opts upload.Options) (*upload.Result, *capacityReservation, bool) {
	if err := os.MkdirAll(a.Config.ImportRoot, 0700); err != nil {
		a.writeUploadError(w, r, fmt.Errorf("prepare upload storage: %w", err))
		return nil, nil, false
	}
	declared, err := declaredUploadBytes(r, a.Config.MaxUpload)
	var reservation *capacityReservation
	if err == nil {
		reservation, err = a.capacity.reserve(a.Config, workflow, a.Config.ImportRoot, archiveUploadDemands(a.Config, declared))
	}
	if err != nil {
		a.writeUploadError(w, r, err)
		return nil, nil, false
	}
	guardUploadBody(w, r)
	reader, err := r.MultipartReader()
	if err != nil {
		reservation.release()
		a.writeUploadError(w, r, err)
		return nil, nil, false
	}
	opts.FileField = "backup"
	opts.MaxFileBytes = a.Config.MaxUpload
	opts.CheckSpace = reservation.consume
	staged, err := upload.Stream(reader, opts)
	if err != nil {
		reservation.release()
		a.writeUploadError(w, r, err)
		return nil, nil, false
	}
	a.Metrics.ObserveUploadStaged(staged.Size)
	return staged, reservation, true
}

// checkCPMoveCapacity admits a cPanel restore whose archive is already
// staged: the inspected expanded tree in the import root and the
// site-manager staging tree, on top of in-progress upload reservations.
func (a *App) checkCPMoveCapacity(expandedBytes int64) error {
	if expandedBytes < 0 {
		return errors.New("invalid cpmove size estimate")
	}
	return a.capacity.check(a.Config, "cpmove", stagedArchiveDemands(a.Config, uint64(expandedBytes)))
}
