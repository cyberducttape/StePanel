package rootbroker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const maxDatabaseDumpBytes = uint64(2 << 30)

// dumpExtensionGranule batches reservation growth once a dump outgrows its
// estimate. Without it every pipe-sized write past the estimate would run its
// own ledger transaction and statfs.
const dumpExtensionGranule = uint64(8 << 20)

// capacityDumpWriter extends the durable backup reservation before writing
// bytes beyond the panel's estimate. The reservation includes both the dump
// and its archive copy; written bytes are subtracted only while evaluating
// remaining capacity because they already reduce filesystem free space.
type capacityDumpWriter struct {
	mu sync.Mutex

	ctx           context.Context
	file          *os.File
	db            *sql.DB
	reservationID string
	stagePath     string
	device        uint64
	expected      uint64
	minFree       uint64
	written       uint64
	extended      uint64
}

func newCapacityDumpWriter(ctx context.Context, file *os.File, db *sql.DB, req *DBRequest) (*capacityDumpWriter, error) {
	if db == nil {
		return nil, errors.New("durable capacity database is unavailable")
	}
	if req.DumpCapacityReservation == "" || req.DumpExpectedBytes == 0 {
		return nil, errors.New("database dump requires a durable capacity reservation and positive estimate")
	}
	if req.DumpMinFreeBytes > math.MaxInt64 {
		return nil, errors.New("database dump capacity limits are invalid")
	}
	stage, err := filepath.Abs(req.DumpCapacityStage)
	if err != nil || stage != filepath.Clean(req.DumpCapacityStage) || stage != filepath.Dir(filepath.Clean(req.DumpPath)) {
		return nil, errors.New("database dump reservation is not bound to its staging directory")
	}
	info, err := os.Stat(stage)
	if err != nil || !info.IsDir() {
		return nil, errors.New("database dump staging directory is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("database dump filesystem identity is unavailable")
	}
	device := uint64(stat.Dev)
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect database dump destination: %w", err)
	}
	fileStat, ok := fileInfo.Sys().(*syscall.Stat_t)
	if !ok || uint64(fileStat.Dev) != device {
		return nil, errors.New("database dump destination is on a different filesystem than its reservation")
	}
	var rowBytes int64
	if err := db.QueryRowContext(ctx, `SELECT bytes FROM capacity_reservations WHERE reservation_id=? AND device=? AND workflow='backup' AND staging_path=?`, req.DumpCapacityReservation, device, stage).Scan(&rowBytes); err != nil {
		return nil, fmt.Errorf("verify durable database dump reservation: %w", err)
	}
	if rowBytes <= 0 {
		return nil, errors.New("database dump reservation has no available capacity")
	}
	expected := min(req.DumpExpectedBytes, maxDatabaseDumpBytes)
	minimumHold := expected*2 + (1 << 20)
	if uint64(rowBytes) < minimumHold {
		return nil, errors.New("database dump reservation does not cover the expected dump and archive copies")
	}
	return &capacityDumpWriter{
		ctx: ctx, file: file, db: db, reservationID: req.DumpCapacityReservation,
		stagePath: stage, device: device, expected: expected,
		minFree: req.DumpMinFreeBytes,
	}, nil
}

func (w *capacityDumpWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if uint64(len(p)) > maxDatabaseDumpBytes-w.written {
		return 0, fmt.Errorf("database dump exceeds %d-byte archive entry limit", maxDatabaseDumpBytes)
	}
	next := w.written + uint64(len(p))
	if next > w.expected {
		// Hold twice the overage (dump plus archive copy), rounded up to a
		// granule. Both terms are bounded by 2*maxDatabaseDumpBytes, so the
		// rounding cannot overflow.
		needed := (next - w.expected) * 2
		if needed > w.extended {
			target := (needed + dumpExtensionGranule - 1) / dumpExtensionGranule * dumpExtensionGranule
			if err := w.extend(target - w.extended); err != nil {
				return 0, err
			}
			w.extended = target
		}
	}
	n, err := w.file.Write(p)
	w.written += uint64(n)
	return n, err
}

func (w *capacityDumpWriter) addedBytes() uint64 { return w.extended }

func (w *capacityDumpWriter) extend(additional uint64) error {
	if additional > math.MaxInt64 || w.written > math.MaxInt64 {
		return errors.New("database dump capacity extension exceeds SQLite limits")
	}
	tx, err := w.db.BeginTx(w.ctx, nil)
	if err != nil {
		return fmt.Errorf("begin streaming capacity extension: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(w.ctx, `INSERT INTO state_blobs(name,payload,updated_at,revision) VALUES('capacity-ledger-lock',X'',unixepoch(),0) ON CONFLICT(name) DO UPDATE SET updated_at=excluded.updated_at`); err != nil {
		return fmt.Errorf("lock streaming capacity ledger: %w", err)
	}
	var ownBytes, allBytes sql.NullInt64
	if err := tx.QueryRowContext(w.ctx, `SELECT bytes FROM capacity_reservations WHERE reservation_id=? AND device=? AND workflow='backup' AND staging_path=?`, w.reservationID, w.device, w.stagePath).Scan(&ownBytes); err != nil {
		return fmt.Errorf("revalidate streaming capacity reservation: %w", err)
	}
	if ownBytes.Int64 <= 0 {
		return errors.New("streaming capacity reservation disappeared")
	}
	if err := tx.QueryRowContext(w.ctx, `SELECT SUM(bytes) FROM capacity_reservations WHERE device=?`, w.device).Scan(&allBytes); err != nil {
		return fmt.Errorf("read outstanding streaming capacity: %w", err)
	}
	outstanding := uint64(0)
	if allBytes.Valid && allBytes.Int64 > 0 {
		outstanding = uint64(allBytes.Int64)
	}
	consumed := min(uint64(ownBytes.Int64), w.written)
	remaining := outstanding - consumed
	free, err := availableDumpBytes(w.stagePath)
	if err != nil {
		return err
	}
	if additional > math.MaxInt64-remaining || w.minFree > math.MaxInt64-remaining-additional || free < remaining+additional+w.minFree {
		return fmt.Errorf("insufficient free space to extend database backup reservation by %d bytes", additional)
	}
	result, err := tx.ExecContext(w.ctx, `UPDATE capacity_reservations SET bytes=bytes+? WHERE reservation_id=? AND device=? AND workflow='backup' AND staging_path=?`, int64(additional), w.reservationID, w.device, w.stagePath)
	if err != nil {
		return fmt.Errorf("extend streaming capacity reservation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("extend streaming capacity reservation changed %d rows: %v", changed, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit streaming capacity extension: %w", err)
	}
	return nil
}

func availableDumpBytes(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("inspect backup filesystem capacity: %w", err)
	}
	blockSize := uint64(stat.Bsize)
	blocks := uint64(stat.Bavail)
	if blockSize != 0 && blocks > math.MaxUint64/blockSize {
		return math.MaxUint64, nil
	}
	return blockSize * blocks, nil
}
