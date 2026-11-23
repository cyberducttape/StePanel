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
	"testing"
	"time"
)

func TestResumableUploadSurvivesManagerRestartAndPublishesVerifiedBytes(t *testing.T) {
	root := t.TempDir()
	content := bytes.Repeat([]byte("s"), int(MaxResumableUploadBytes))
	digest := sha256.Sum256(content)
	m, err := NewUploadManager(root)
	if err != nil {
		t.Fatal(err)
	}
	session, err := m.Begin("shop", "tenant-a", "wp-content/archive.bin", int64(len(content)), hex.EncodeToString(digest[:]), "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		start := int64(i) * MaxUploadChunkBytes
		session, err = m.Append(context.Background(), session.ID, "shop", "tenant-a", start, session.Size, bytes.NewReader(content[start:start+MaxUploadChunkBytes]))
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
		session, err = m.Append(context.Background(), session.ID, "shop", "tenant-a", start, session.Size, bytes.NewReader(content[start:end]))
		if err != nil {
			t.Fatal(err)
		}
	}
	var published []byte
	err = m.Complete(context.Background(), session.ID, "shop", "tenant-a", func(got UploadSession, r io.Reader) error {
		if got.Path != session.Path || got.SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatal("published metadata changed")
		}
		published, err = io.ReadAll(r)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(published, content) {
		t.Fatal("published content mismatch")
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
