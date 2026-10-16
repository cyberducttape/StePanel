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
	// Reserve acquires a progressive capacity hold for one spool before bytes
	// are written. The consume callback transfers reserved bytes to actual disk
	// usage as the spool grows; release is retained until Close.
	Reserve func(limit int64) (consume func(written int64) error, release func(), err error)
	// Check, when set, is called with the total bytes written so far, at
	// least every 8 MiB and at the end; an error aborts the spool. Callers
	// use it to verify free space against their capacity reservation.
	Check func(written int64) error
}

// Spooled is an archive on disk. Close removes it.
type Spooled struct {
	*os.File
	Size    int64
	release func()
}

// Close closes and removes the spool file.
func (s *Spooled) Close() error {
	closeErr := s.File.Close()
	removeErr := os.Remove(s.File.Name())
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	if s.release != nil {
		s.release()
		s.release = nil
	}
	return errors.Join(closeErr, removeErr)
}

// Store copies at most limit bytes of src into a private file and returns
// it positioned at the start.
func (d DirSpool) Store(src io.Reader, limit int64) (*Spooled, error) {
	if d.Dir == "" {
		return nil, errors.New("archive spool directory is not configured")
	}
	check := d.Check
	var release func()
	if d.Reserve != nil {
		consume, releaseReservation, err := d.Reserve(limit)
		if err != nil {
			if releaseReservation != nil {
				releaseReservation()
			}
			return nil, fmt.Errorf("reserve archive spool capacity: %w", err)
		}
		release = releaseReservation
		if release == nil {
			release = func() {}
		}
		if consume != nil {
			check = func(written int64) error {
				if err := consume(written); err != nil {
					return err
				}
				if d.Check != nil {
					return d.Check(written)
				}
				return nil
			}
		}
	}
	file, err := os.CreateTemp(d.Dir, ".stepanel-spool-*")
	if err != nil {
		if release != nil {
			release()
		}
		return nil, fmt.Errorf("create archive spool: %w", err)
	}
	spooled := &Spooled{File: file, release: release}
	fail := func(err error) (*Spooled, error) {
		return nil, errors.Join(err, spooled.Close())
	}
	if err := file.Chmod(0o600); err != nil {
		return fail(fmt.Errorf("secure archive spool: %w", err))
	}
	if check != nil {
		if err := check(0); err != nil {
			return fail(err)
		}
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
			if check != nil && sinceCheck >= spoolCheckInterval {
				sinceCheck = 0
				if err := check(spooled.Size); err != nil {
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
	if check != nil {
		if err := check(spooled.Size); err != nil {
			return fail(err)
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(fmt.Errorf("rewind archive spool: %w", err))
	}
	return spooled, nil
}
