package sitefiles

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Local is the development Store: it works directly on the document root
// that DocRoot returns, through os.Root so no path or link escapes it.
// Production hosts use the broker-backed store instead, which runs as the
// account user.
type Local struct {
	DocRoot func(site string) (string, error)
}

func (l Local) open(site SiteAccess) (*os.Root, error) {
	if l.DocRoot == nil {
		return nil, errors.New("file manager has no document root resolver")
	}
	if site == nil || site.SiteID() == "" {
		return nil, errors.New("file manager requires an authorized site")
	}
	dir, err := l.DocRoot(site.SiteID())
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open site document root: %w", err)
	}
	return root, nil
}

// name maps a cleaned relative path onto an os.Root name.
func name(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

// noLinks fails when an existing component of rel is a symbolic link,
// matching stepanel-filectl. through=false allows the last component to be
// a link (a delete removes only the link).
func noLinks(root *os.Root, rel string, through bool) error {
	if rel == "" {
		return nil
	}
	components := strings.Split(rel, "/")
	for i := range components {
		current := strings.Join(components[:i+1], "/")
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 && (through || i < len(components)-1) {
			return Refused("refusing to follow the symbolic link %s", current)
		}
	}
	return nil
}

func (l Local) List(_ context.Context, site SiteAccess, rel string) (Listing, error) {
	root, err := l.open(site)
	if err != nil {
		return Listing{}, err
	}
	defer root.Close()
	if err := noLinks(root, rel, true); err != nil {
		return Listing{}, err
	}
	dir, err := root.Open(name(rel))
	if err != nil {
		return Listing{}, notFound(err)
	}
	defer dir.Close()
	if info, err := dir.Stat(); err != nil || !info.IsDir() {
		return Listing{}, Refused("not a directory")
	}
	entries, err := dir.ReadDir(MaxEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return Listing{}, fmt.Errorf("list directory: %w", err)
	}
	listing := Listing{Path: rel, Entries: []Entry{}}
	for _, entry := range entries {
		if len(listing.Entries) == MaxEntries {
			listing.Truncated = true
			break
		}
		info, err := entry.Info()
		if err != nil {
			return Listing{}, fmt.Errorf("stat %s: %w", entry.Name(), err)
		}
		kind := "other"
		switch {
		case info.Mode().IsRegular():
			kind = "file"
		case info.IsDir():
			kind = "dir"
		case info.Mode()&fs.ModeSymlink != 0:
			kind = "link"
		}
		listing.Entries = append(listing.Entries, Entry{
			Name: entry.Name(), Type: kind, Size: info.Size(),
			Mode: strconv.FormatUint(uint64(info.Mode().Perm()), 8), ModTime: info.ModTime().UTC().Truncate(1e9),
		})
	}
	sortEntries(listing.Entries)
	return listing, nil
}

func (l Local) Read(_ context.Context, site SiteAccess, rel string) ([]byte, error) {
	root, err := l.open(site)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := noLinks(root, rel, true); err != nil {
		return nil, err
	}
	info, err := root.Lstat(name(rel))
	if err != nil {
		return nil, notFound(err)
	}
	if !info.Mode().IsRegular() {
		return nil, Refused("not a regular file")
	}
	if info.Size() > MaxReadBytes {
		return nil, Refused("file is larger than %d bytes", MaxReadBytes)
	}
	file, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat opened file: %w", err)
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, Refused("not a regular file")
	}
	return readBounded(file, MaxReadBytes)
}

// readBounded distinguishes a complete file at the limit from a file that
// grew after its earlier size check. The extra byte is never returned.
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, Refused("file is larger than %d bytes", limit)
	}
	return data, nil
}

// Write replaces a file atomically: a temporary file beside it, then a
// rename. It syncs the file before publication and the parent directory after
// rename for crash durability on filesystems that support directory fsync.
// An existing file keeps its permissions; a new one is 0640.
func (l Local) Write(_ context.Context, site SiteAccess, rel string, content io.Reader) (err error) {
	if content == nil {
		return errors.New("file content is required")
	}
	root, err := l.open(site)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := noLinks(root, rel, true); err != nil {
		return err
	}
	mode := fs.FileMode(0o640)
	if info, err := root.Lstat(rel); err == nil {
		if !info.Mode().IsRegular() {
			return Refused("not a regular file")
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if info, err := root.Stat(name(path.Dir(rel))); err != nil || !info.IsDir() {
		return Refused("parent directory does not exist")
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temp := path.Join(path.Dir(rel), ".stepanel-upload-"+hex.EncodeToString(suffix))
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			if removeErr := root.Remove(temp); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("remove temporary file: %w", removeErr))
			}
		}
	}()
	written, err := io.Copy(file, io.LimitReader(content, MaxWriteBytes+1))
	if err != nil {
		file.Close()
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return ErrTooLarge
		}
		return fmt.Errorf("write file: %w", err)
	}
	if written > MaxWriteBytes {
		file.Close()
		return ErrTooLarge
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}
	if err := root.Chmod(temp, mode); err != nil {
		return fmt.Errorf("set file permissions: %w", err)
	}
	parent := path.Dir(rel)
	if err := root.Rename(temp, rel); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	committed = true
	parentDir, err := root.Open(name(parent))
	if err != nil {
		return fmt.Errorf("%w: replacement is visible but opening its parent failed: %v", ErrDurabilityUncertain, err)
	}
	defer parentDir.Close()
	if err := parentDir.Sync(); err != nil {
		return fmt.Errorf("%w: replacement is visible but parent directory sync failed: %v", ErrDurabilityUncertain, err)
	}
	return nil
}

func (l Local) Mkdir(_ context.Context, site SiteAccess, rel string) error {
	root, err := l.open(site)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := noLinks(root, rel, true); err != nil {
		return err
	}
	if err := root.Mkdir(rel, 0o750); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Refused("path already exists")
		}
		return fmt.Errorf("create directory: %w", err)
	}
	return nil
}

func (l Local) Rename(_ context.Context, site SiteAccess, rel, destination string) error {
	if destination == rel || strings.HasPrefix(destination, rel+"/") {
		return Refused("cannot move a directory into itself")
	}
	root, err := l.open(site)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := noLinks(root, rel, true); err != nil {
		return err
	}
	if err := noLinks(root, destination, true); err != nil {
		return err
	}
	if _, err := root.Lstat(rel); err != nil {
		return notFound(err)
	}
	if _, err := root.Lstat(destination); err == nil {
		return Refused("destination already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := root.Rename(rel, destination); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func (l Local) Delete(_ context.Context, site SiteAccess, rel string) error {
	root, err := l.open(site)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := noLinks(root, rel, false); err != nil {
		return err
	}
	if _, err := root.Lstat(rel); err != nil {
		return notFound(err)
	}
	if err := root.RemoveAll(rel); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// sortEntries orders directories first, then by name.
func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if (entries[i].Type == "dir") != (entries[j].Type == "dir") {
			return entries[i].Type == "dir"
		}
		return entries[i].Name < entries[j].Name
	})
}
