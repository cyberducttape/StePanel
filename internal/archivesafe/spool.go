package archivesafe

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// spoolCheckInterval is how often a spool re-checks capacity while writing.
const spoolCheckInterval = 8 << 20

// ErrSpoolLimit reports a stream longer than the spool's limit.
var ErrSpoolLimit = errors.New("archive exceeds the spool size limit")

// DirSpool stores a streamed archive (a ZIP needs random access to its
// central directory) in Dir, which should be capacity-managed storage on
// the same terms as uploads, never the system temporary directory.
type DirSpool struct {
	Dir string
	// Check, when set, is called with the total bytes written so far, at
	// least every 8 MiB and at the end; an error aborts the spool. Callers
	// use it to verify free space against their capacity reservation.
	Check func(written int64) error
}

// Spooled is an archive on disk. Close removes it.
type Spooled struct {
	*os.File
	Size int64
}

// Close closes and removes the spool file.
func (s *Spooled) Close() error {
	closeErr := s.File.Close()
	removeErr := os.Remove(s.File.Name())
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(closeErr, removeErr)
}

// Store copies at most limit bytes of src into a private file and returns
// it positioned at the start.
func (d DirSpool) Store(src io.Reader, limit int64) (*Spooled, error) {
	if d.Dir == "" {
		return nil, errors.New("archive spool directory is not configured")
	}
	file, err := os.CreateTemp(d.Dir, ".stepanel-spool-*")
	if err != nil {
		return nil, fmt.Errorf("create archive spool: %w", err)
	}
	spooled := &Spooled{File: file}
	fail := func(err error) (*Spooled, error) {
		return nil, errors.Join(err, spooled.Close())
	}
	if err := file.Chmod(0o600); err != nil {
		return fail(fmt.Errorf("secure archive spool: %w", err))
	}
	buffer := make([]byte, 1<<20)
	var sinceCheck int64
	for {
		n, readErr := src.Read(buffer)
		if n > 0 {
			if spooled.Size+int64(n) > limit {
				return fail(ErrSpoolLimit)
			}
			if _, err := file.Write(buffer[:n]); err != nil {
				return fail(fmt.Errorf("write archive spool: %w", err))
			}
			spooled.Size += int64(n)
			sinceCheck += int64(n)
			if d.Check != nil && sinceCheck >= spoolCheckInterval {
				sinceCheck = 0
				if err := d.Check(spooled.Size); err != nil {
					return fail(err)
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fail(fmt.Errorf("read archive: %w", readErr))
		}
	}
	if d.Check != nil {
		if err := d.Check(spooled.Size); err != nil {
			return fail(err)
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(fmt.Errorf("rewind archive spool: %w", err))
	}
	return spooled, nil
}
