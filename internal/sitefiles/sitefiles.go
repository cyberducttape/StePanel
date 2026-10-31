// Package sitefiles is the customer file manager domain: the rules for paths
// inside a site's document root and the Store interface the panel uses to
// list, read and change files there (ADR-0007).
//
// Production stores run every operation as the hosting account's Unix user
// through the root broker and stepanel-filectl, so the file manager can never
// do more than the account's own SFTP login. Local is the development store.
package sitefiles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Limits shared with deploy/integrations/stepanel-filectl.
const (
	MaxReadBytes  = 2 << 20
	MaxWriteBytes = 4 << 20
	MaxEntries    = 5000
	maxPathBytes  = 4096
	maxNameBytes  = 255
)

// ErrNotFound reports a path that does not exist.
var ErrNotFound = errors.New("path does not exist")
var ErrTooLarge = errors.New("file exceeds the configured size limit")
var ErrDurabilityUncertain = errors.New("file replacement durability is uncertain")

// RefusedError is an operation refused because of the customer's own files
// or request ("path already exists"). Its message is safe to show to the
// customer; every other error is a host failure.
type RefusedError struct{ Message string }

func (e *RefusedError) Error() string { return e.Message }

// Refused returns a RefusedError.
func Refused(format string, args ...any) error {
	return &RefusedError{Message: fmt.Sprintf(format, args...)}
}

// Entry is one directory entry.
type Entry struct {
	Name    string    `json:"name"`
	Type    string    `json:"type"` // file, dir, link or other
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"` // octal permission bits, e.g. "644"
	ModTime time.Time `json:"mod_time"`
}

// Listing is one directory's entries, sorted directories first.
type Listing struct {
	Path      string  `json:"path"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

// SiteAccess is an authorization capability resolved by the control plane
// after checking the current principal against the site's tenant. Stores
// accept this capability instead of a raw site name so a caller cannot
// accidentally skip the authorization step at the data-access boundary.
// Implementations should keep the concrete capability opaque to callers.
type SiteAccess interface {
	SiteID() string
	TenantID() string
}

// Store performs file operations inside one authorized site's document root.
// Paths are relative to that root and must pass Clean.
type Store interface {
	List(ctx context.Context, site SiteAccess, path string) (Listing, error)
	Read(ctx context.Context, site SiteAccess, path string) ([]byte, error)
	Write(ctx context.Context, site SiteAccess, path string, content io.Reader) error
	Mkdir(ctx context.Context, site SiteAccess, path string) error
	Rename(ctx context.Context, site SiteAccess, path, destination string) error
	Delete(ctx context.Context, site SiteAccess, path string) error
}

// Clean validates a path relative to a document root and returns it in the
// form the helper accepts: "" for the root itself, otherwise slash-separated
// components with no empty, "." or ".." component and no control
// characters. It never rewrites a suspicious path into an acceptable one.
func Clean(path string) (string, error) {
	if path == "" || path == "." || path == "/" {
		return "", nil
	}
	path = strings.TrimPrefix(path, "/")
	if len(path) > maxPathBytes {
		return "", errors.New("path is too long")
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return "", errors.New("path contains a control character")
		}
	}
	for _, component := range strings.Split(path, "/") {
		switch {
		case component == "", component == ".", component == "..":
			return "", fmt.Errorf("invalid path %q", path)
		case len(component) > maxNameBytes:
			return "", errors.New("a path component is too long")
		}
	}
	return path, nil
}

// CleanTarget is Clean for a path that must name something below the root.
func CleanTarget(path string) (string, error) {
	cleaned, err := Clean(path)
	if err != nil {
		return "", err
	}
	if cleaned == "" {
		return "", errors.New("the document root itself cannot be changed")
	}
	return cleaned, nil
}

// ParseListing decodes stepanel-filectl list output: one
// "type\tsize\tmode\tmtime\tname" line per entry, find(1) %y type letters.
// More than MaxEntries lines means the listing was cut short.
func ParseListing(path string, output []byte) (Listing, error) {
	listing := Listing{Path: path, Entries: []Entry{}}
	for _, line := range strings.Split(strings.TrimSuffix(string(output), "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 5)
		if len(fields) != 5 || fields[4] == "" || strings.Contains(fields[4], "/") {
			return Listing{}, fmt.Errorf("malformed listing line %q", line)
		}
		if len(listing.Entries) == MaxEntries {
			listing.Truncated = true
			break
		}
		size, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || size < 0 {
			return Listing{}, fmt.Errorf("malformed entry size %q", fields[1])
		}
		if _, err := strconv.ParseUint(fields[2], 8, 32); err != nil {
			return Listing{}, fmt.Errorf("malformed entry mode %q", fields[2])
		}
		seconds, err := strconv.ParseFloat(fields[3], 64)
		if err != nil {
			return Listing{}, fmt.Errorf("malformed entry time %q", fields[3])
		}
		listing.Entries = append(listing.Entries, Entry{
			Name: fields[4], Type: entryType(fields[0]), Size: size, Mode: fields[2],
			ModTime: time.Unix(int64(seconds), 0).UTC(),
		})
	}
	sortEntries(listing.Entries)
	return listing, nil
}

func entryType(letter string) string {
	switch letter {
	case "f":
		return "file"
	case "d":
		return "dir"
	case "l":
		return "link"
	default:
		return "other"
	}
}
