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
	"syscall"
	"time"
)

// Release content integrity. A release that stops being active is retained
// as a rollback point inside the site's home, where the site's own account
// can write. When it is retired, a digest of its contents is recorded in the
// control-plane database, which the site cannot write; a rollback re-hashes
// the retained tree and refuses to activate it if the contents changed.
//
// The live release is deliberately not covered: a running application may
// write caches or uploads into it, so its contents are fixed only from the
// moment it is retired.
const (
	releaseIntegrityVerified   = "verified"
	releaseIntegrityModified   = "modified"
	releaseIntegrityUnrecorded = "unrecorded"
)

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
		PRIMARY KEY (site, release)
	)`); err != nil {
		return nil, fmt.Errorf("initialize release digest store: %w", err)
	}
	return &releaseDigestStore{db: db}, nil
}

func (s *releaseDigestStore) record(site, release, digest string, files, bytes int64) error {
	_, err := s.db.Exec(`INSERT INTO release_content_digests (site, release, digest, files, bytes, recorded_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(site, release) DO UPDATE SET digest = excluded.digest, files = excluded.files, bytes = excluded.bytes, recorded_at = excluded.recorded_at`,
		site, release, digest, files, bytes, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *releaseDigestStore) lookup(site, release string) (string, bool, error) {
	var digest string
	err := s.db.QueryRow(`SELECT digest FROM release_content_digests WHERE site = ? AND release = ?`, site, release).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return digest, err == nil, err
}

// forgetMissing drops the digests of a site's releases that no longer exist
// under home (pruned, or activated again by a rollback).
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

// gitReleaseContentDigest hashes a release tree's structure and contents:
// every path, type and permission, each regular file's bytes and each
// symlink's target, in lexical order. Files are opened without following
// symlinks and must still be the entry that was walked.
func gitReleaseContentDigest(ctx context.Context, root string, maxBytes int64, maxEntries int) (string, int64, int64, error) {
	if maxEntries < 1 {
		maxEntries = 100000
	}
	digest := sha256.New()
	var total, files, entries int64
	gitDir := filepath.Join(root, ".git")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == gitDir || strings.HasPrefix(path, gitDir+string(os.PathSeparator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
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
		fmt.Fprintf(digest, "%q\x00%s\x00", relative, info.Mode().String())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(digest, "%q\n", target)
		case info.IsDir():
			io.WriteString(digest, "\n")
		case info.Mode().IsRegular():
			if info.Size() > maxBytes-total {
				return errors.New("release exceeds configured size limit")
			}
			file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if err != nil {
				return err
			}
			opened, err := file.Stat()
			if err == nil && (!opened.Mode().IsRegular() || !os.SameFile(info, opened)) {
				err = fmt.Errorf("release file %s changed while it was hashed", relative)
			}
			var copied int64
			if err == nil {
				fileDigest := sha256.New()
				copied, err = io.Copy(fileDigest, io.LimitReader(file, maxBytes-total+1))
				if err == nil && copied > maxBytes-total {
					err = errors.New("release exceeds configured size limit")
				}
				fmt.Fprintf(digest, "%d\x00%x\n", copied, fileDigest.Sum(nil))
			}
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return err
			}
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
// limits is left unrecorded; a rollback to it then reports that.
func (a *App) recordRetiredRelease(ctx context.Context, site, retained string) {
	if a.releaseDigests == nil || retained == "" {
		return
	}
	digest, files, bytes, err := gitReleaseContentDigest(ctx, retained, a.releaseDigestLimit(), a.Config.MaxEntries)
	if err != nil {
		log.Printf("release content digest for %s %s not recorded: %v", site, filepath.Base(retained), err)
		return
	}
	if err := a.releaseDigests.record(site, filepath.Base(retained), digest, files, bytes); err != nil {
		log.Printf("record release content digest for %s %s: %v", site, filepath.Base(retained), err)
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

// retainedReleaseIntegrity compares a retained release with the digest
// recorded when it was retired.
func (a *App) retainedReleaseIntegrity(ctx context.Context, site, release string) (string, error) {
	if a.releaseDigests == nil {
		return releaseIntegrityUnrecorded, nil
	}
	recorded, found, err := a.releaseDigests.lookup(site, filepath.Base(release))
	if err != nil {
		return "", fmt.Errorf("read release content digest: %w", err)
	}
	if !found {
		return releaseIntegrityUnrecorded, nil
	}
	current, _, _, err := gitReleaseContentDigest(ctx, release, a.releaseDigestLimit(), a.Config.MaxEntries)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		// The tree hashed when it was retired. One that no longer hashes —
		// unreadable, over its limits, or changing while it is read — has
		// changed since.
		log.Printf("retained release %s for %s does not hash: %v", filepath.Base(release), site, err)
		return releaseIntegrityModified, nil
	}
	if current != recorded {
		return releaseIntegrityModified, nil
	}
	return releaseIntegrityVerified, nil
}
