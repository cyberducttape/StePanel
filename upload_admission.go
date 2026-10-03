package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"

	"github.com/cyberducttape/StePanel/internal/upload"
)

// errUploadLengthRequired reports an archive upload whose size cannot be
// admitted before its body is read: no Content-Length and no configured
// STEPANEL_MAX_UPLOAD_BYTES ceiling to reserve instead.
var errUploadLengthRequired = errors.New("archive uploads require a Content-Length header")

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

// admitCapacity sums demands per filesystem, so an import root and a site
// root on the same device are charged together, and requires each
// filesystem to keep the configured reserve free after every demand lands.
func admitCapacity(cfg Config, workflow string, demands []capacityDemand) error {
	type filesystem struct {
		path string
		need uint64
	}
	var order []uint64
	byDevice := map[uint64]*filesystem{}
	for _, demand := range demands {
		info, err := os.Stat(demand.Path)
		if err != nil {
			return fmt.Errorf("inspect %s capacity at %s: %w", workflow, demand.Path, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("inspect %s capacity at %s: filesystem identity unavailable", workflow, demand.Path)
		}
		device := uint64(stat.Dev)
		entry := byDevice[device]
		if entry == nil {
			entry = &filesystem{path: demand.Path}
			byDevice[device] = entry
			order = append(order, device)
		}
		if demand.Bytes > ^uint64(0)-entry.need {
			return fmt.Errorf("%s capacity estimate overflow", workflow)
		}
		entry.need += demand.Bytes
	}
	for _, device := range order {
		entry := byDevice[device]
		if entry.need > ^uint64(0)-cfg.MinFreeBytes {
			return fmt.Errorf("%s capacity estimate overflow", workflow)
		}
		required := entry.need + cfg.MinFreeBytes
		free, err := availableBytes(entry.path)
		if err != nil {
			return fmt.Errorf("inspect %s capacity at %s: %w", workflow, entry.path, err)
		}
		if free < required {
			return &capacityError{fmt.Sprintf("insufficient free space at %s: %d bytes available, %d required for this %s", entry.path, free, required, workflow)}
		}
	}
	return nil
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
// lower bound that restoreCPMoveCapacity later refines.
func archiveUploadDemands(cfg Config, archive uint64) []capacityDemand {
	return []capacityDemand{
		{Path: cfg.ImportRoot, Bytes: saturatingAdd(archive, archive)},
		{Path: filepath.Join(cfg.WebRoot, "sites"), Bytes: archive},
	}
}

func saturatingAdd(a, b uint64) uint64 {
	if a > ^uint64(0)-b {
		return ^uint64(0)
	}
	return a + b
}

// uploadSpaceCheck re-verifies the import root while an admitted upload
// streams, so concurrent consumers cannot fill the filesystem underneath it:
// the bytes still expected plus the reserve must remain free.
func uploadSpaceCheck(cfg Config, declared uint64) func(int64) error {
	return func(written int64) error {
		remaining := uint64(0)
		if uint64(written) < declared {
			remaining = declared - uint64(written)
		}
		return admitCapacity(cfg, "upload", []capacityDemand{{Path: cfg.ImportRoot, Bytes: remaining}})
	}
}

// uploadRejection is a client-facing validation failure raised before any
// archive byte is staged.
type uploadRejection struct {
	status  int
	message string
}

func (e *uploadRejection) Error() string { return e.message }

// writeUploadError maps admission and streaming failures to responses.
func writeUploadError(w http.ResponseWriter, err error) {
	var rejection *uploadRejection
	var capacity *capacityError
	switch {
	case errors.As(err, &rejection):
		http.Error(w, rejection.message, rejection.status)
	case errors.As(err, &capacity):
		http.Error(w, capacity.Error(), http.StatusInsufficientStorage)
	case errors.Is(err, errUploadLengthRequired):
		http.Error(w, err.Error(), http.StatusLengthRequired)
	case errors.Is(err, upload.ErrFileTooLarge):
		http.Error(w, "upload exceeds the configured size limit", http.StatusRequestEntityTooLarge)
	case errors.Is(err, upload.ErrMissingFile):
		http.Error(w, "backup file is required", http.StatusBadRequest)
	case errors.Is(err, upload.ErrMalformed), errors.Is(err, upload.ErrUnexpectedPart), errors.Is(err, upload.ErrFieldTooLarge), errors.Is(err, http.ErrNotMultipart), errors.Is(err, http.ErrMissingBoundary):
		http.Error(w, "invalid upload: "+err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, "could not stage upload", http.StatusInternalServerError)
	}
}
