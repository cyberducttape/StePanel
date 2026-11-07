package stepanel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// Release content integrity. A release that stops being active is retained
// as a rollback point inside the site's home, where the site's own account
// can write. When it is retired, a digest of its contents is recorded in the
// control-plane database, which the site cannot write. A rollback never
// activates the retained tree itself: it copies it into a panel-owned
// release stage, hashing the bytes it writes, and activates that copy only
// if its digest matches the recorded one. A change the site makes to the
// retained tree after the copy has no effect on what is activated.
//
// The live release is deliberately not covered: a running application may
// write caches or uploads into it, so its contents are fixed only from the
// moment it is retired.
const (
	releaseIntegrityVerified = "verified"
	releaseIntegrityModified = "modified"
	// releaseIntegrityUnrecorded: no digest exists, because the release was
	// retired before digests were recorded.
	releaseIntegrityUnrecorded = "unrecorded"
	// releaseIntegrityUnverified: hashing the release when it was retired
	// failed (for example it exceeded the release limits), so there is no
	// baseline to compare with.
	releaseIntegrityUnverified = "unverified"
	// releaseIntegrityUnreadable: a baseline exists but the retained tree can
	// no longer be read within the release limits (an entry changed type or
	// was replaced while it was read, an unsupported entry appeared, or the
	// tree grew past its limit). Rollback is refused.
	releaseIntegrityUnreadable = "unreadable"
)

// releaseDigestFormat prefixes every recorded digest. A digest without it
// was recorded in an earlier format and is treated as unrecorded.
const releaseDigestFormat = "v3:"

// releaseDigestSlots bounds how many release trees are hashed or copied at
// once on the host, so rollback previews cannot crowd out site I/O.
var releaseDigestSlots = make(chan struct{}, 2)

type releaseDigestStore struct {
	db *sql.DB
}

func newReleaseDigestStore(db *sql.DB) (*releaseDigestStore, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS release_content_digests (
		site TEXT NOT NULL,
		release TEXT NOT NULL,
		digest TEXT NOT NULL,
		files INTEGER NOT NULL,
		bytes INTEGER NOT NULL,
		recorded_at TEXT NOT NULL,
		failure TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (site, release)
	)`); err != nil {
		return nil, fmt.Errorf("initialize release digest store: %w", err)
	}
	var columns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('release_content_digests') WHERE name = 'failure'`).Scan(&columns); err != nil {
		return nil, fmt.Errorf("inspect release digest store: %w", err)
	}
	if columns == 0 {
		if _, err := db.Exec(`ALTER TABLE release_content_digests ADD COLUMN failure TEXT NOT NULL DEFAULT ''`); err != nil {
			return nil, fmt.Errorf("add release digest failure column: %w", err)
		}
	}
	return &releaseDigestStore{db: db}, nil
}

func (s *releaseDigestStore) record(site, release, digest string, files, bytes int64) error {
	_, err := s.db.Exec(`INSERT INTO release_content_digests (site, release, digest, files, bytes, recorded_at, failure) VALUES (?, ?, ?, ?, ?, ?, '')
		ON CONFLICT(site, release) DO UPDATE SET digest = excluded.digest, files = excluded.files, bytes = excluded.bytes, recorded_at = excluded.recorded_at, failure = ''`,
		site, release, releaseDigestFormat+digest, files, bytes, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// recordFailure notes that a release was retired but could not be hashed,
// so a rollback to it is reported as unverified rather than as a release
// from before digests existed.
func (s *releaseDigestStore) recordFailure(site, release, reason string) error {
	if len(reason) > 256 {
		reason = reason[:256]
	}
	if reason == "" {
		reason = "unknown error"
	}
	_, err := s.db.Exec(`INSERT INTO release_content_digests (site, release, digest, files, bytes, recorded_at, failure) VALUES (?, ?, '', 0, 0, ?, ?)
		ON CONFLICT(site, release) DO UPDATE SET digest = '', files = 0, bytes = 0, recorded_at = excluded.recorded_at, failure = excluded.failure`,
		site, release, time.Now().UTC().Format(time.RFC3339Nano), reason)
	return err
}

// lookup returns the recorded digest (without its format prefix) or the
// reason recording failed. A digest in an earlier format is not found.
func (s *releaseDigestStore) lookup(site, release string) (digest, failure string, found bool, err error) {
	err = s.db.QueryRow(`SELECT digest, failure FROM release_content_digests WHERE site = ? AND release = ?`, site, release).Scan(&digest, &failure)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	if failure != "" {
		return "", failure, true, nil
	}
	if !strings.HasPrefix(digest, releaseDigestFormat) {
		return "", "", false, nil
	}
	return strings.TrimPrefix(digest, releaseDigestFormat), "", true, nil
}

// forgetMissing drops the digests of a site's releases that no longer exist
// under home (pruned, or consumed by a rollback).
func (s *releaseDigestStore) forgetMissing(site, home string) error {
	rows, err := s.db.Query(`SELECT release FROM release_content_digests WHERE site = ?`, site)
	if err != nil {
		return err
	}
	var missing []string
	for rows.Next() {
		var release string
		if err := rows.Scan(&release); err != nil {
			rows.Close()
			return err
		}
		if _, err := os.Lstat(filepath.Join(home, release)); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, release)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, release := range missing {
		if _, err := s.db.Exec(`DELETE FROM release_content_digests WHERE site = ? AND release = ?`, site, release); err != nil {
			return err
		}
	}
	return nil
}

// releaseDigestMetrics counts the release trees read for content digests.
// Operations: retire (recording), preview (in place) and rollback (the
// verified copy).
var releaseDigestOperations = [...]string{"retire", "preview", "rollback"}

const (
	releaseDigestRetire = iota
	releaseDigestPreview
	releaseDigestRollback
)

var releaseDigestMetrics struct {
	runs, failures, bytes, nanos [len(releaseDigestOperations)]atomic.Uint64
}

func observeReleaseDigest(operation int, started time.Time, bytes int64, err error) {
	releaseDigestMetrics.runs[operation].Add(1)
	releaseDigestMetrics.nanos[operation].Add(uint64(time.Since(started)))
	if bytes > 0 {
		releaseDigestMetrics.bytes[operation].Add(uint64(bytes))
	}
	if err != nil {
		releaseDigestMetrics.failures[operation].Add(1)
	}
}

func writeReleaseDigestMetrics(w io.Writer) {
	metrics := []struct {
		name, help string
		values     *[len(releaseDigestOperations)]atomic.Uint64
		seconds    bool
	}{
		{"stepanel_release_digest_runs_total", "Release trees read for a content digest, by operation", &releaseDigestMetrics.runs, false},
		{"stepanel_release_digest_failures_total", "Release content digests that could not be computed, by operation", &releaseDigestMetrics.failures, false},
		{"stepanel_release_digest_bytes_total", "File bytes read for release content digests, by operation", &releaseDigestMetrics.bytes, false},
		{"stepanel_release_digest_seconds_total", "Time spent computing release content digests, including waiting for a slot, by operation", &releaseDigestMetrics.nanos, true},
	}
	for _, metric := range metrics {
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", metric.name, metric.help, metric.name)
		for i, operation := range releaseDigestOperations {
			value := metric.values[i].Load()
			if metric.seconds {
				_, _ = fmt.Fprintf(w, "%s{operation=%q} %.6f\n", metric.name, operation, float64(value)/float64(time.Second))
			} else {
				_, _ = fmt.Fprintf(w, "%s{operation=%q} %d\n", metric.name, operation, value)
			}
		}
	}
}

// releaseCopyError says which side of a release copy failed: reading the
// retained tree (the source, which the site account can change) or writing
// the panel-owned stage (the destination). A destination failure such as a
// full disk says nothing about the retained release's integrity.
type releaseCopyError struct {
	destination bool
	err         error
}

func (e *releaseCopyError) Error() string {
	if e.destination {
		return "write release copy: " + e.err.Error()
	}
	return "read retained release: " + e.err.Error()
}

func (e *releaseCopyError) Unwrap() error { return e.err }

func releaseSourceError(err error) error {
	if err == nil {
		return nil
	}
	var typed *releaseCopyError
	if errors.As(err, &typed) {
		return err
	}
	return &releaseCopyError{err: err}
}

func releaseDestinationError(err error) error {
	if err == nil {
		return nil
	}
	return &releaseCopyError{destination: true, err: err}
}

// isReleaseDestinationError reports whether err came from writing the copy.
func isReleaseDestinationError(err error) bool {
	var typed *releaseCopyError
	return errors.As(err, &typed) && typed.destination
}

// releaseWalkHook lets tests change an entry after the walk classified it.
var releaseWalkHook func(rel string)

// maxReleaseDepth bounds directory nesting in a release tree.
const maxReleaseDepth = 256

// releaseResolve keeps every open below the directory it is relative to and
// refuses symlinks in any component, so an entry the site account swaps
// for a symlink while the tree is read cannot redirect the walk.
const releaseResolve = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS

// gitReleaseContentDigest hashes a release tree's structure and contents:
// every path and entry type, each regular file's bytes and each symlink's
// target, in lexical order. Nothing is skipped, including .git: deployed
// releases have their Git metadata removed, so metadata that appears later
// is part of what the digest must cover. Ownership and permission bits are
// left out: the site helper's seal sets every file to 0640 and every
// directory to 0750, so they are not part of what is activated.
func gitReleaseContentDigest(ctx context.Context, root string, maxBytes int64, maxEntries int) (string, int64, int64, error) {
	return copyReleaseContent(ctx, root, "", -1, maxBytes, maxEntries)
}

// copyReleaseContent computes the content digest of root and, when dest is
// not empty, recreates the tree under dest (an existing empty directory) as
// it is read. The digest is computed over the bytes written to dest, and
// over exactly the entries the copy holds, so it describes the copy even if
// root changes during the walk: hashing the finished copy gives the same
// digest.
//
// The walk is descriptor-relative throughout: every directory and file is
// opened with openat2 (RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS) from its parent's
// descriptor, its type is re-checked on the opened descriptor, and it must
// still be the entry that was classified. Copies are created the same way
// below dest, owned by the caller with files 0640 and directories 0750 (and
// group, readable, with setgid directories, when group is not -1), as the
// seal leaves them. Errors are releaseCopyErrors naming the side that
// failed.
func copyReleaseContent(ctx context.Context, root, dest string, group int, maxBytes int64, maxEntries int) (string, int64, int64, error) {
	if maxEntries < 1 {
		maxEntries = 100000
	}
	select {
	case releaseDigestSlots <- struct{}{}:
		defer func() { <-releaseDigestSlots }()
	case <-ctx.Done():
		return "", 0, 0, ctx.Err()
	}
	sourceFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", 0, 0, releaseSourceError(fmt.Errorf("open release root: %w", err))
	}
	walker := &releaseWalker{ctx: ctx, digest: sha256.New(), group: group, maxBytes: maxBytes, maxEntries: int64(maxEntries), buffer: make([]byte, 1<<20)}
	destFD := -1
	if dest != "" {
		destFD, err = unix.Open(dest, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err == nil {
			err = walker.setGroup(destFD, 0o750|unix.S_ISGID)
		}
		if err != nil {
			unix.Close(sourceFD)
			if destFD >= 0 {
				unix.Close(destFD)
			}
			return "", 0, 0, releaseDestinationError(fmt.Errorf("open release copy root: %w", err))
		}
	}
	fmt.Fprintf(walker.digest, "%q\x00D\n", ".")
	if err := walker.directory(sourceFD, destFD, ".", 1); err != nil {
		return "", 0, 0, err
	}
	return hex.EncodeToString(walker.digest.Sum(nil)), walker.files, walker.total, nil
}

type releaseWalker struct {
	ctx                   context.Context
	digest                hash.Hash
	group                 int
	maxBytes, maxEntries  int64
	total, files, entries int64
	buffer                []byte
}

// setGroup gives a created entry the copy's group and mode.
func (w *releaseWalker) setGroup(fd int, mode uint32) error {
	if w.group >= 0 {
		if err := unix.Fchown(fd, -1, w.group); err != nil {
			return err
		}
	}
	return unix.Fchmod(fd, mode)
}

// directory hashes (and copies) the entries of the directory open on
// sourceFD into the one open on destFD (-1 when only hashing), taking
// ownership of both descriptors.
func (w *releaseWalker) directory(sourceFD, destFD int, rel string, depth int) error {
	source := os.NewFile(uintptr(sourceFD), rel)
	defer source.Close()
	if destFD >= 0 {
		defer unix.Close(destFD)
	}
	if depth > maxReleaseDepth {
		return releaseSourceError(fmt.Errorf("release is nested deeper than %d directories at %q", maxReleaseDepth, rel))
	}
	var names []string
	for {
		batch, err := source.Readdirnames(1024)
		names = append(names, batch...)
		if w.entries+int64(len(names)) > w.maxEntries {
			return releaseSourceError(errors.New("release exceeds the configured entry limit"))
		}
		if errors.Is(err, io.EOF) || err == nil && len(batch) == 0 {
			break
		}
		if err != nil {
			return releaseSourceError(fmt.Errorf("read directory %q: %w", rel, err))
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		w.entries++
		if err := w.entry(sourceFD, destFD, name, path.Join(rel, name), depth); err != nil {
			return err
		}
	}
	return nil
}

func (w *releaseWalker) entry(sourceFD, destFD int, name, rel string, depth int) error {
	var classified unix.Stat_t
	if err := unix.Fstatat(sourceFD, name, &classified, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return releaseSourceError(fmt.Errorf("stat %q: %w", rel, err))
	}
	if releaseWalkHook != nil {
		releaseWalkHook(rel)
	}
	switch classified.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		link, err := readlinkat(sourceFD, name)
		if err != nil {
			return releaseSourceError(fmt.Errorf("read symlink %q: %w", rel, err))
		}
		if destFD >= 0 {
			if err := unix.Symlinkat(link, destFD, name); err != nil {
				return releaseDestinationError(fmt.Errorf("create symlink %q: %w", rel, err))
			}
		}
		fmt.Fprintf(w.digest, "%q\x00L\x00%q\n", rel, link)
		return nil
	case unix.S_IFDIR:
		fd, err := w.openSource(sourceFD, name, rel, unix.O_DIRECTORY, &classified)
		if err != nil {
			return err
		}
		childDest := -1
		if destFD >= 0 {
			err := unix.Mkdirat(destFD, name, 0o750)
			if err == nil {
				childDest, err = unix.Openat2(destFD, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: releaseResolve})
			}
			if err == nil {
				if err = w.setGroup(childDest, 0o750|unix.S_ISGID); err != nil {
					unix.Close(childDest)
				}
			}
			if err != nil {
				unix.Close(fd)
				return releaseDestinationError(fmt.Errorf("create directory %q: %w", rel, err))
			}
		}
		fmt.Fprintf(w.digest, "%q\x00D\n", rel)
		return w.directory(fd, childDest, rel, depth+1)
	case unix.S_IFREG:
		if classified.Size > w.maxBytes-w.total {
			return releaseSourceError(errors.New("release exceeds configured size limit"))
		}
		fd, err := w.openSource(sourceFD, name, rel, unix.O_NONBLOCK, &classified)
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(fd), rel)
		defer file.Close()
		var copy *os.File
		if destFD >= 0 {
			out, err := unix.Openat2(destFD, name, &unix.OpenHow{Flags: unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW, Mode: 0o640, Resolve: releaseResolve})
			if err == nil {
				if err = w.setGroup(out, 0o640); err != nil {
					unix.Close(out)
				}
			}
			if err != nil {
				return releaseDestinationError(fmt.Errorf("create file %q: %w", rel, err))
			}
			copy = os.NewFile(uintptr(out), rel)
			defer copy.Close()
		}
		fileDigest := sha256.New()
		var copied int64
		for {
			if err := w.ctx.Err(); err != nil {
				return err
			}
			n, readErr := file.Read(w.buffer)
			if n > 0 {
				copied += int64(n)
				if copied > w.maxBytes-w.total {
					return releaseSourceError(errors.New("release exceeds configured size limit"))
				}
				fileDigest.Write(w.buffer[:n])
				if copy != nil {
					if _, err := copy.Write(w.buffer[:n]); err != nil {
						return releaseDestinationError(fmt.Errorf("write file %q: %w", rel, err))
					}
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				return releaseSourceError(fmt.Errorf("read file %q: %w", rel, readErr))
			}
		}
		if copy != nil {
			if err := copy.Close(); err != nil {
				return releaseDestinationError(fmt.Errorf("write file %q: %w", rel, err))
			}
		}
		fmt.Fprintf(w.digest, "%q\x00F\x00%d\x00%x\n", rel, copied, fileDigest.Sum(nil))
		w.total += copied
		w.files++
		return nil
	default:
		return releaseSourceError(fmt.Errorf("release contains an unsupported file type at %q", rel))
	}
}

// openSource opens name below sourceFD without following any symlink and
// checks that it is still the entry that was classified.
func (w *releaseWalker) openSource(sourceFD int, name, rel string, flags int, classified *unix.Stat_t) (int, error) {
	fd, err := unix.Openat2(sourceFD, name, &unix.OpenHow{Flags: uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | flags), Resolve: releaseResolve})
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EXDEV) {
			err = fmt.Errorf("%q changed while it was read: %w", rel, err)
		}
		return -1, releaseSourceError(err)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		unix.Close(fd)
		return -1, releaseSourceError(fmt.Errorf("stat %q: %w", rel, err))
	}
	if opened.Mode&unix.S_IFMT != classified.Mode&unix.S_IFMT || opened.Dev != classified.Dev || opened.Ino != classified.Ino {
		unix.Close(fd)
		return -1, releaseSourceError(fmt.Errorf("%q changed while it was read", rel))
	}
	return fd, nil
}

// readlinkat reads a symlink's target relative to a directory descriptor.
func readlinkat(dirFD int, name string) (string, error) {
	for size := 256; size <= 1<<16; size *= 4 {
		buffer := make([]byte, size)
		n, err := unix.Readlinkat(dirFD, name, buffer)
		if err != nil {
			return "", err
		}
		if n < size {
			return string(buffer[:n]), nil
		}
	}
	return "", errors.New("symlink target is too long")
}

func (a *App) releaseDigestLimit() int64 {
	if a.Config.GitReleaseMaxTreeBytes > 0 {
		return a.Config.GitReleaseMaxTreeBytes
	}
	return 1 << 40
}

// recordRetiredRelease records the content digest of the release an
// activation just retired. A tree that cannot be hashed within the release
// limits is recorded as a failure, so a rollback to it is unverified.
func (a *App) recordRetiredRelease(ctx context.Context, site, retained string) {
	if a.releaseDigests == nil || retained == "" {
		return
	}
	name := filepath.Base(retained)
	started := time.Now()
	digest, files, bytes, err := gitReleaseContentDigest(ctx, retained, a.releaseDigestLimit(), a.Config.MaxEntries)
	observeReleaseDigest(releaseDigestRetire, started, bytes, err)
	if err != nil {
		log.Printf("release content digest for %s %s not recorded: %v", site, name, err)
		if recordErr := a.releaseDigests.recordFailure(site, name, err.Error()); recordErr != nil {
			log.Printf("record release content digest failure for %s %s: %v", site, name, recordErr)
		}
		return
	}
	if err := a.releaseDigests.record(site, name, digest, files, bytes); err != nil {
		log.Printf("record release content digest for %s %s: %v", site, name, err)
	}
}

// forgetMissingReleaseDigests drops digests of a site's releases that are
// gone after retention or a rollback.
func (a *App) forgetMissingReleaseDigests(site string) {
	if a.releaseDigests == nil {
		return
	}
	location, err := siteLocationFor(a.Config, site)
	if err == nil {
		err = a.releaseDigests.forgetMissing(site, location.Home)
	}
	if err != nil {
		log.Printf("prune release content digests for %s: %v", site, err)
	}
}

// releaseIntegrity is the verdict on a retained release, with the reason
// for anything but verified.
type releaseIntegrity struct {
	Status string
	Detail string
}

// compareReleaseDigest classifies a digest computed now, or the error that
// stopped it, against what was recorded at retirement. The outcomes are
// distinct: a digest mismatch is "modified"; a retained tree that can no
// longer be read is "unreadable"; a failure writing the copy is returned as
// an error and says nothing about the release. With no baseline, any read
// error is returned too, because there is nothing complete to activate.
func (a *App) compareReleaseDigest(ctx context.Context, site, release, current string, hashErr error) (releaseIntegrity, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return releaseIntegrity{}, ctxErr
	}
	if isReleaseDestinationError(hashErr) {
		return releaseIntegrity{}, hashErr
	}
	if a.releaseDigests == nil {
		return releaseIntegrity{Status: releaseIntegrityUnrecorded, Detail: "no digest store is configured"}, hashErr
	}
	recorded, failure, found, err := a.releaseDigests.lookup(site, filepath.Base(release))
	if err != nil {
		return releaseIntegrity{}, fmt.Errorf("read release content digest: %w", err)
	}
	if !found {
		return releaseIntegrity{Status: releaseIntegrityUnrecorded, Detail: "the release was retired before content digests were recorded"}, hashErr
	}
	if failure != "" {
		return releaseIntegrity{Status: releaseIntegrityUnverified, Detail: "the release could not be hashed when it was retired: " + failure}, hashErr
	}
	if hashErr != nil {
		log.Printf("retained release %s for %s cannot be read: %v", filepath.Base(release), site, hashErr)
		return releaseIntegrity{Status: releaseIntegrityUnreadable, Detail: hashErr.Error()}, nil
	}
	if current != recorded {
		return releaseIntegrity{Status: releaseIntegrityModified, Detail: "the contents differ from the digest recorded at retirement"}, nil
	}
	return releaseIntegrity{Status: releaseIntegrityVerified}, nil
}

// retainedReleaseIntegrity hashes a retained release in place for a
// preview. It is advisory: the rollback itself verifies the copy it
// activates.
func (a *App) retainedReleaseIntegrity(ctx context.Context, site, release string) (releaseIntegrity, error) {
	if a.releaseDigests != nil {
		if _, failure, found, err := a.releaseDigests.lookup(site, filepath.Base(release)); err != nil {
			return releaseIntegrity{}, fmt.Errorf("read release content digest: %w", err)
		} else if !found || failure != "" {
			// Nothing to compare with; skip reading the tree.
			return a.compareReleaseDigest(ctx, site, release, "", nil)
		}
	}
	started := time.Now()
	current, _, bytes, err := gitReleaseContentDigest(ctx, release, a.releaseDigestLimit(), a.Config.MaxEntries)
	observeReleaseDigest(releaseDigestPreview, started, bytes, err)
	integrity, err := a.compareReleaseDigest(ctx, site, release, current, err)
	if err != nil && integrity.Status != "" {
		// Unrecorded or unverified releases have no baseline; a read error
		// does not change that verdict.
		err = nil
	}
	return integrity, err
}

// copyVerifiedRelease copies a retained release into stage (a panel-owned
// release staging directory), reading at most maxBytes, and classifies the
// copy against the digest recorded at retirement. Only the copy may be
// activated. group is the web group the copy is readable by, or -1. An
// error means nothing may be activated; a destination error
// (isReleaseDestinationError) is a staging failure, not a verdict on the
// release.
func (a *App) copyVerifiedRelease(ctx context.Context, site, release, stage string, group int, maxBytes int64) (releaseIntegrity, error) {
	started := time.Now()
	current, _, bytes, err := copyReleaseContent(ctx, release, stage, group, maxBytes, a.Config.MaxEntries)
	observeReleaseDigest(releaseDigestRollback, started, bytes, err)
	return a.compareReleaseDigest(ctx, site, release, current, err)
}
