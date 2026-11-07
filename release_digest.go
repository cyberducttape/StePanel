package stepanel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
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
)

// releaseDigestFormat prefixes every recorded digest. A digest without it
// was recorded in an earlier format and is treated as unrecorded.
const releaseDigestFormat = "v2:"

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

// gitReleaseContentDigest hashes a release tree's structure and contents:
// every path and entry type, each regular file's executable bits and bytes,
// and each symlink's target, in lexical order. Nothing is skipped, including
// .git: deployed releases have their Git metadata removed, so one that
// appears later is part of what the digest must cover. Ownership and other
// permission bits are left out because the site helper normalizes them.
func gitReleaseContentDigest(ctx context.Context, root string, maxBytes int64, maxEntries int) (string, int64, int64, error) {
	return copyReleaseContent(ctx, root, "", -1, maxBytes, maxEntries)
}

// copyReleaseContent computes the content digest of root and, when dest is
// not empty, recreates the tree under dest (an existing empty directory) as
// it is read. The digest is computed over the bytes written to dest, so it
// describes the copy exactly even if root changes during the walk. Files are
// opened without following symlinks and must still be the entry that was
// walked. Copies are owned by the caller and writable only by it; when group
// is not -1 they are given that group with read access (setgid directories),
// as the site helper's seal would, so the web server can serve the copy as
// soon as it is activated.
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
	digest := sha256.New()
	var total, files, entries int64
	buffer := make([]byte, 1<<20)
	setGroup := func(path string, mode os.FileMode) error {
		if group < 0 {
			return nil
		}
		if err := os.Lchown(path, -1, group); err != nil {
			return err
		}
		return os.Chmod(path, mode)
	}
	if dest != "" {
		if err := setGroup(dest, 0750|os.ModeSetgid); err != nil {
			return "", 0, 0, err
		}
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > int64(maxEntries) {
			return errors.New("release exceeds the configured entry limit")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		target := ""
		if dest != "" && relative != "." {
			target = filepath.Join(dest, relative)
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if target != "" {
				if err := os.Symlink(link, target); err != nil {
					return err
				}
			}
			fmt.Fprintf(digest, "%q\x00L\x00%q\n", relative, link)
		case info.IsDir():
			if target != "" {
				if err := os.Mkdir(target, 0750); err != nil {
					return err
				}
				if err := setGroup(target, 0750|os.ModeSetgid); err != nil {
					return err
				}
			}
			fmt.Fprintf(digest, "%q\x00D\n", relative)
		case info.Mode().IsRegular():
			if info.Size() > maxBytes-total {
				return errors.New("release exceeds configured size limit")
			}
			executable := info.Mode().Perm() & 0111
			file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if err != nil {
				return err
			}
			defer file.Close()
			opened, err := file.Stat()
			if err != nil {
				return err
			}
			if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
				return fmt.Errorf("release file %s changed while it was hashed", relative)
			}
			fileDigest := sha256.New()
			var sink io.Writer = fileDigest
			var copy *os.File
			if target != "" {
				copy, err = os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0640|executable&0110)
				if err != nil {
					return err
				}
				defer copy.Close()
				if err := setGroup(target, 0640|executable&0110); err != nil {
					return err
				}
				sink = io.MultiWriter(fileDigest, copy)
			}
			copied, err := io.CopyBuffer(sink, contextReader{ctx, io.LimitReader(file, maxBytes-total+1)}, buffer)
			if err != nil {
				return err
			}
			if copied > maxBytes-total {
				return errors.New("release exceeds configured size limit")
			}
			if copy != nil {
				if err := copy.Close(); err != nil {
					return err
				}
			}
			fmt.Fprintf(digest, "%q\x00F%o\x00%d\x00%x\n", relative, executable, copied, fileDigest.Sum(nil))
			total += copied
			files++
		default:
			return errors.New("release contains an unsupported file type")
		}
		return nil
	})
	if err != nil {
		return "", 0, 0, err
	}
	return hex.EncodeToString(digest.Sum(nil)), files, total, nil
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

// releaseIntegrity is the verdict on a retained release, with the reason a
// release is unverified.
type releaseIntegrity struct {
	Status string
	Detail string
}

// compareReleaseDigest classifies a digest computed now (or the error that
// stopped it) against what was recorded at retirement.
func (a *App) compareReleaseDigest(ctx context.Context, site, release, current string, hashErr error) (releaseIntegrity, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return releaseIntegrity{}, ctxErr
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
		// The tree hashed when it was retired. One that no longer hashes —
		// unreadable, over its limits, or changing while it is read — has
		// changed since.
		log.Printf("retained release %s for %s does not hash: %v", filepath.Base(release), site, hashErr)
		return releaseIntegrity{Status: releaseIntegrityModified, Detail: "the release no longer hashes within the release limits"}, nil
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
// release staging directory) and classifies the copy against the digest
// recorded at retirement. Only the copy may be activated. group is the web
// group the copy is readable by, or -1.
func (a *App) copyVerifiedRelease(ctx context.Context, site, release, stage string, group int) (releaseIntegrity, error) {
	started := time.Now()
	current, _, bytes, err := copyReleaseContent(ctx, release, stage, group, a.releaseDigestLimit(), a.Config.MaxEntries)
	observeReleaseDigest(releaseDigestRollback, started, bytes, err)
	integrity, compareErr := a.compareReleaseDigest(ctx, site, release, current, err)
	if compareErr != nil {
		// A copy that failed is never activated, whatever the baseline.
		return integrity, compareErr
	}
	return integrity, nil
}
