package stepanel

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testBackupKey     = "backup-encryption-key-current-0123456789abcdef"
	testBackupKeyPrev = "backup-encryption-key-retired-0123456789abcdef"
)

func encryptTestArchive(t *testing.T, plain []byte, key string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "plain")
	if err := os.WriteFile(src, plain, 0600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "sealed")
	keyID, err := encryptBackupArchive(src, dst, key)
	if err != nil {
		t.Fatal(err)
	}
	return dst, keyID
}

func decryptTestArchive(t *testing.T, sealed []byte, keys []string, scheme string) ([]byte, error) {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "out")
	if err := decryptBackupArchiveReader(bytes.NewReader(sealed), dst, keys, scheme); err != nil {
		if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
			t.Fatalf("failed decryption left output behind: %v", statErr)
		}
		return nil, err
	}
	return os.ReadFile(dst)
}

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestBackupEncryptionRoundTripsAtChunkBoundaries(t *testing.T) {
	for _, size := range []int{0, 1, backupEncryptionChunk - 1, backupEncryptionChunk, backupEncryptionChunk + 1, 2 * backupEncryptionChunk} {
		plain := randomBytes(t, size)
		path, keyID := encryptTestArchive(t, plain, testBackupKey)
		if keyID != backupKeyID(testBackupKey) {
			t.Fatalf("key id = %s, want %s", keyID, backupKeyID(testBackupKey))
		}
		sealed, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decryptTestArchive(t, sealed, []string{testBackupKey}, backupEncryptionName)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d: decrypted data differs", size)
		}
	}
}

// Every backup derives its own subkey from a fresh salt, so encrypting the
// same data twice under one key yields unrelated ciphertexts.
func TestBackupEncryptionUsesAFreshSubkeyPerBackup(t *testing.T) {
	plain := randomBytes(t, 4096)
	first, _ := encryptTestArchive(t, plain, testBackupKey)
	second, _ := encryptTestArchive(t, plain, testBackupKey)
	a, _ := os.ReadFile(first)
	b, _ := os.ReadFile(second)
	headerLen := backupEncryptionHeaderV2Len
	if bytes.Equal(a[headerLen-backupSaltBytes-4:headerLen-4], b[headerLen-backupSaltBytes-4:headerLen-4]) {
		t.Fatal("two backups share a salt")
	}
	if bytes.Equal(a[headerLen:], b[headerLen:]) {
		t.Fatal("two backups of the same data share ciphertext")
	}
}

func TestBackupEncryptionRejectsTampering(t *testing.T) {
	plain := randomBytes(t, 2*backupEncryptionChunk+100)
	path, _ := encryptTestArchive(t, plain, testBackupKey)
	sealed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	header := backupEncryptionHeaderV2Len
	firstChunk := header + 4 + backupEncryptionChunk + 16
	secondChunk := firstChunk + 4 + backupEncryptionChunk + 16
	flipMask := func(offset int, mask byte) []byte {
		changed := append([]byte(nil), sealed...)
		changed[offset] ^= mask
		return changed
	}
	flip := func(offset int) []byte { return flipMask(offset, 0x01) }
	cases := map[string][]byte{
		"salt":                flip(len(backupEncryptionFormat) + backupKeyIDBytes),
		"chunk size":          flip(header - 1),
		"ciphertext":          flip(header + 4 + 10),
		"tag":                 flip(firstChunk - 1),
		"final flag forged":   flipMask(header, 0x80),
		"truncated at chunk":  sealed[:secondChunk],
		"truncated mid chunk": sealed[:secondChunk+10],
		"trailing data":       append(append([]byte(nil), sealed...), 0),
		"chunks reordered": func() []byte {
			chunkLen := 4 + backupEncryptionChunk + 16
			swapped := append([]byte(nil), sealed[:header]...)
			swapped = append(swapped, sealed[header+chunkLen:header+2*chunkLen]...)
			swapped = append(swapped, sealed[header:header+chunkLen]...)
			return append(swapped, sealed[header+2*chunkLen:]...)
		}(),
	}
	for name, data := range cases {
		if _, err := decryptTestArchive(t, data, []string{testBackupKey}, backupEncryptionName); err == nil {
			t.Errorf("%s: tampered archive decrypted", name)
		}
	}
}

func TestBackupEncryptionKeyRotation(t *testing.T) {
	plain := []byte("written under the retired key")
	path, keyID := encryptTestArchive(t, plain, testBackupKeyPrev)
	sealed, _ := os.ReadFile(path)

	_, err := decryptTestArchive(t, sealed, []string{testBackupKey}, backupEncryptionName)
	if err == nil || !strings.Contains(err.Error(), keyID) {
		t.Fatalf("decryption without the retired key = %v, want an error naming key %s", err, keyID)
	}
	got, err := decryptTestArchive(t, sealed, []string{testBackupKey, testBackupKeyPrev}, backupEncryptionName)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("decryption with the retired key listed = %q, %v", got, err)
	}
}

// The signed manifest names the scheme; an archive in a different format
// than its manifest declares is rejected, so v2 manifests cannot be paired
// with v1 archives that lack truncation protection.
func TestBackupEncryptionHeaderMustMatchManifestScheme(t *testing.T) {
	path, _ := encryptTestArchive(t, []byte("data"), testBackupKey)
	sealed, _ := os.ReadFile(path)
	if _, err := decryptTestArchive(t, sealed, []string{testBackupKey}, backupEncryptionNameV1); err == nil {
		t.Fatal("v2 archive accepted under a v1 manifest")
	}
	legacy := encryptBackupV1ForTest(t, []byte("data"), testBackupKey)
	if _, err := decryptTestArchive(t, legacy, []string{testBackupKey}, backupEncryptionName); err == nil {
		t.Fatal("v1 archive accepted under a v2 manifest")
	}
	if _, err := decryptTestArchive(t, sealed, []string{testBackupKey}, "none"); err == nil {
		t.Fatal("unknown scheme accepted")
	}
}

// Backups written by earlier releases stay restorable, including under a
// key that has since been retired.
func TestBackupEncryptionReadsVersionOneArchives(t *testing.T) {
	plain := randomBytes(t, backupEncryptionChunk+7)
	legacy := encryptBackupV1ForTest(t, plain, testBackupKeyPrev)
	got, err := decryptTestArchive(t, legacy, []string{testBackupKey, testBackupKeyPrev}, backupEncryptionNameV1)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("v1 decryption = %v", err)
	}
	if _, err := decryptTestArchive(t, legacy, []string{testBackupKey}, backupEncryptionNameV1); err == nil {
		t.Fatal("v1 archive decrypted without its key")
	}
	tampered := append([]byte(nil), legacy...)
	tampered[len(tampered)-1] ^= 1
	if _, err := decryptTestArchive(t, tampered, []string{testBackupKeyPrev}, backupEncryptionNameV1); err == nil {
		t.Fatal("tampered v1 archive decrypted")
	}
}

// encryptBackupV1ForTest reproduces the format written before v2.
func encryptBackupV1ForTest(t *testing.T, plain []byte, key string) []byte {
	t.Helper()
	aead, err := backupCipherV1(key)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	out.WriteString(backupEncryptionFormatV1)
	base := randomBytes(t, backupEncryptionNonceV1)
	out.Write(base)
	for index := uint32(0); len(plain) > 0; index++ {
		n := min(len(plain), backupEncryptionChunk)
		sealed := aead.Seal(nil, backupChunkNonceV1(base, index), plain[:n], nil)
		if err := binary.Write(&out, binary.BigEndian, uint32(len(sealed))); err != nil {
			t.Fatal(err)
		}
		out.Write(sealed)
		plain = plain[n:]
	}
	return out.Bytes()
}

func TestBackupDecryptionKeysConfig(t *testing.T) {
	t.Setenv("STEPANEL_BACKUP_ENCRYPTION_KEY", testBackupKey)
	t.Setenv("STEPANEL_BACKUP_ENCRYPTION_PREVIOUS_KEYS", " "+testBackupKeyPrev+" , ,")
	cfg := LoadConfig()
	if got := cfg.backupDecryptionKeys(); len(got) != 2 || got[0] != testBackupKey || got[1] != testBackupKeyPrev {
		t.Fatalf("decryption keys = %q", got)
	}
}
