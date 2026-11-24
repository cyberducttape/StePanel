package sitefiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResumableUploadSurvivesManagerRestartAndPublishesVerifiedBytes(t *testing.T) {
	root := t.TempDir()
	chunk := bytes.Repeat([]byte("s"), int(MaxUploadChunkBytes))
	digest := sha256.New()
	for i := int64(0); i < MaxResumableUploadBytes/MaxUploadChunkBytes; i++ {
		_, _ = digest.Write(chunk)
	}
	expectedDigest := hex.EncodeToString(digest.Sum(nil))
	m, err := NewUploadManager(root)
	if err != nil {
		t.Fatal(err)
	}
	session, err := m.Begin("shop", "tenant-a", "wp-content/archive.bin", MaxResumableUploadBytes, expectedDigest, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		start := int64(i) * MaxUploadChunkBytes
		session, err = m.Append(context.Background(), session.ID, "shop", "tenant-a", start, session.Size, bytes.NewReader(chunk))
		if err != nil {
			t.Fatal(err)
		}
	}
	m, err = NewUploadManager(root) // new process-local locks, same durable session
	if err != nil {
		t.Fatal(err)
	}
	for session.Offset < session.Size {
		start := session.Offset
		end := start + MaxUploadChunkBytes
		if end > session.Size {
			end = session.Size
		}
		if end-start != int64(len(chunk)) {
			t.Fatalf("test chunk has unexpected size %d", end-start)
		}
		session, err = m.Append(context.Background(), session.ID, "shop", "tenant-a", start, session.Size, bytes.NewReader(chunk))
		if err != nil {
			t.Fatal(err)
		}
	}
	var publishedDigest string
	var publishedBytes int64
	err = m.Complete(context.Background(), session.ID, "shop", "tenant-a", func(got UploadSession, r io.Reader) error {
		if got.Path != session.Path || got.SHA256 != expectedDigest {
			t.Fatal("published metadata changed")
		}
		hash := sha256.New()
		publishedBytes, err = io.Copy(hash, r)
		publishedDigest = hex.EncodeToString(hash.Sum(nil))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if publishedBytes != MaxResumableUploadBytes || publishedDigest != expectedDigest {
		t.Fatalf("published stream size=%d digest=%s; want size=%d digest=%s", publishedBytes, publishedDigest, MaxResumableUploadBytes, expectedDigest)
	}
	if _, err := m.Info(session.ID, "shop", "tenant-a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed session remains: %v", err)
	}
}

func TestResumableUploadEnforcesTenantOffsetAndChecksum(t *testing.T) {
	m, err := NewUploadManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Begin("shop", "tenant-a", "../outside.bin", 1, strings.Repeat("0", sha256.Size*2), ""); err == nil {
		t.Fatal("upload session accepted a path outside the site document root")
	}
	data := []byte("verified content")
	digest := sha256.Sum256(data)
	s, err := m.Begin("shop", "tenant-a", "file.bin", int64(len(data)), hex.EncodeToString(digest[:]), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Info(s.ID, "shop", "tenant-b"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cross-tenant info: %v", err)
	}
	if _, err := m.Append(context.Background(), s.ID, "shop", "tenant-a", 1, s.Size, bytes.NewReader(data)); !errors.Is(err, ErrUploadOffset) {
		t.Fatalf("wrong offset: %v", err)
	}
	s, err = m.Append(context.Background(), s.ID, "shop", "tenant-a", 0, s.Size, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Complete(context.Background(), s.ID, "shop", "tenant-b", func(UploadSession, io.Reader) error { t.Fatal("cross-tenant publish called"); return nil }); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cross-tenant complete: %v", err)
	}
	s.SHA256 = hex.EncodeToString(make([]byte, sha256.Size))
	if err := m.save(s); err != nil {
		t.Fatal(err)
	}
	if err := m.Complete(context.Background(), s.ID, "shop", "tenant-a", func(UploadSession, io.Reader) error { t.Fatal("bad checksum published"); return nil }); !errors.Is(err, ErrUploadChecksum) {
		t.Fatalf("checksum error: %v", err)
	}
	if _, err := m.Info(s.ID, "shop", "tenant-a"); err != nil {
		t.Fatalf("failed session should remain resumable: %v", err)
	}
}

func TestResumableUploadPrunesExpiredSession(t *testing.T) {
	m, err := NewUploadManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("x"))
	s, err := m.Begin("shop", "tenant-a", "file.bin", 1, hex.EncodeToString(sum[:]), "")
	if err != nil {
		t.Fatal(err)
	}
	s.UpdatedAt = time.Now().Add(-UploadSessionTTL - time.Minute)
	if err := m.save(s); err != nil {
		t.Fatal(err)
	}
	removed := m.PruneExpired(time.Now())
	if len(removed) != 1 || removed[0] != s.ID {
		t.Fatalf("pruned IDs = %v", removed)
	}
	if _, err := os.Stat(filepath.Join(m.root, s.ID+".part")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged data remains: %v", err)
	}
}

func TestResumableUploadBoundsActiveSessionsPerTenant(t *testing.T) {
	m, err := NewUploadManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("x"))
	for i := 0; i < MaxUploadSessionsPerTenant; i++ {
		if _, err := m.Begin("shop", "tenant-a", "file-"+string(rune('a'+i)), 1, hex.EncodeToString(sum[:]), ""); err != nil {
			t.Fatalf("begin %d: %v", i, err)
		}
	}
	if _, err := m.Begin("shop", "tenant-a", "file-over", 1, hex.EncodeToString(sum[:]), ""); !errors.Is(err, ErrUploadSessionLimit) {
		t.Fatalf("fifth session error = %v", err)
	}
	if _, err := m.Begin("shop", "tenant-b", "file-b", 1, hex.EncodeToString(sum[:]), ""); err != nil {
		t.Fatalf("other tenant was blocked: %v", err)
	}
}

func TestResumableUploadDoesNotRetainLocksForProbedIDs(t *testing.T) {
	m, err := NewUploadManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		id := strings.Repeat("0", 30) + hex.EncodeToString([]byte{byte(i)})
		if _, err := m.Info(id, "shop", "tenant-a"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unknown upload %s: %v", id, err)
		}
	}
	sum := sha256.Sum256([]byte("x"))
	s, err := m.Begin("shop", "tenant-a", "file.bin", 1, hex.EncodeToString(sum[:]), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Cancel(s.ID, "shop", "tenant-a"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	retained := len(m.locks)
	m.mu.Unlock()
	if retained != 0 {
		t.Fatalf("upload manager retained %d session locks", retained)
	}
}
