package backup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsEncryptionSchemeNamesOnlySupportedFormats(t *testing.T) {
	for name, want := range map[string]bool{
		EncryptionScheme:       true,
		backupEncryptionNameV1: true,
		"":                     false,
		"AES-256-CBC":          false,
	} {
		if got := IsEncryptionScheme(name); got != want {
			t.Errorf("IsEncryptionScheme(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestHasKeyRequiresAUsableKey(t *testing.T) {
	if HasKey(nil) || HasKey([]string{"", "too-short"}) {
		t.Fatal("HasKey accepted keys shorter than the minimum")
	}
	if !HasKey([]string{"too-short", testBackupKey}) {
		t.Fatal("HasKey ignored a usable key after an unusable one")
	}
}

func TestWithDecryptedArchiveReaderRemovesThePlaintext(t *testing.T) {
	plain := randomBytes(t, backupEncryptionChunk+17)
	sealedPath, _ := encryptTestArchive(t, plain, testBackupKey)
	sealed, err := os.ReadFile(sealedPath)
	if err != nil {
		t.Fatal(err)
	}

	var seen string
	err = WithDecryptedArchiveReader(bytes.NewReader(sealed), []string{testBackupKey}, EncryptionScheme, func(path string) error {
		seen = path
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, plain) {
			t.Error("decrypted archive differs from the original")
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("decrypted archive mode = %v, want private", info.Mode().Perm())
		}
		return nil
	})
	if err != nil || seen == "" {
		t.Fatalf("WithDecryptedArchiveReader = %v (callback ran: %v)", err, seen != "")
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatalf("decrypted plaintext was left behind: %v", err)
	}

	callbackErr := errors.New("restore failed")
	seen = ""
	err = WithDecryptedArchiveReader(bytes.NewReader(sealed), []string{testBackupKey}, EncryptionScheme, func(path string) error {
		seen = path
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error = %v, want it returned", err)
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatalf("plaintext was left behind after a failed callback: %v", err)
	}
}

func TestWithDecryptedArchiveReaderRefusesBeforeTheCallback(t *testing.T) {
	sealedPath, _ := encryptTestArchive(t, []byte("payload"), testBackupKey)
	sealed, err := os.ReadFile(sealedPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func(func(string) error) error{
		"no usable key": func(fn func(string) error) error {
			return WithDecryptedArchiveReader(bytes.NewReader(sealed), []string{"short"}, EncryptionScheme, fn)
		},
		"wrong key": func(fn func(string) error) error {
			return WithDecryptedArchiveReader(bytes.NewReader(sealed), []string{testBackupKeyPrev}, EncryptionScheme, fn)
		},
		"unsupported scheme": func(fn func(string) error) error {
			return WithDecryptedArchiveReader(bytes.NewReader(sealed), []string{testBackupKey}, "AES-256-CBC", fn)
		},
		"truncated header": func(fn func(string) error) error {
			return WithDecryptedArchiveReader(bytes.NewReader(sealed[:4]), []string{testBackupKey}, EncryptionScheme, fn)
		},
	} {
		called := false
		if err := run(func(string) error { called = true; return nil }); err == nil || called {
			t.Errorf("%s: err = %v, callback ran = %v", name, err, called)
		}
	}
}

func TestEncryptArchiveRefusesUnsafeInputsAndKeepsExistingOutput(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "plain")
	if err := os.WriteFile(src, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := EncryptArchive(src, filepath.Join(dir, "short-key"), "short"); err == nil {
		t.Fatal("EncryptArchive accepted a short key")
	}
	if _, err := EncryptArchive(filepath.Join(dir, "missing"), filepath.Join(dir, "missing.enc"), testBackupKey); err == nil {
		t.Fatal("EncryptArchive accepted a missing source")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(src, link); err != nil {
		t.Fatal(err)
	}
	if _, err := EncryptArchive(link, filepath.Join(dir, "link.enc"), testBackupKey); err == nil {
		t.Fatal("EncryptArchive followed a symlinked source")
	}
	existing := filepath.Join(dir, "existing")
	if err := os.WriteFile(existing, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := EncryptArchive(src, existing, testBackupKey); err == nil {
		t.Fatal("EncryptArchive overwrote an existing destination")
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "keep me" {
		t.Fatalf("existing destination changed: %q, %v", data, err)
	}
}

func TestEncryptArchiveRoundTripsAnEmptyArchive(t *testing.T) {
	sealedPath, keyID := encryptTestArchive(t, nil, testBackupKey)
	if keyID != KeyID(testBackupKey) {
		t.Fatalf("key id = %q, want %q", keyID, KeyID(testBackupKey))
	}
	sealed, err := os.ReadFile(sealedPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptTestArchive(t, sealed, []string{testBackupKey}, EncryptionScheme)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty archive decrypted to %d bytes, %v", len(got), err)
	}
}

func TestDecryptArchiveReaderKeepsAnExistingDestination(t *testing.T) {
	sealedPath, _ := encryptTestArchive(t, []byte("payload"), testBackupKey)
	sealed, err := os.ReadFile(sealedPath)
	if err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(existing, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := decryptArchiveReader(bytes.NewReader(sealed), existing, []string{testBackupKey}, EncryptionScheme); err == nil {
		t.Fatal("decryption overwrote an existing destination")
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "keep me" {
		t.Fatalf("existing destination changed: %q, %v", data, err)
	}
}

func TestVersionOneDecryptionRequiresAUsableKey(t *testing.T) {
	if _, err := backupCipherV1("short"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("backupCipherV1 with a short key = %v", err)
	}
}

// Malformed streams are refused with a specific error, and nothing is left
// at the destination.
func TestDecryptionRefusesMalformedStreams(t *testing.T) {
	sealedPath, _ := encryptTestArchive(t, randomBytes(t, 100), testBackupKey)
	v2, err := os.ReadFile(sealedPath)
	if err != nil {
		t.Fatal(err)
	}
	headerEnd := backupEncryptionHeaderV2Len
	withChunkSize := func(size uint32) []byte {
		out := append([]byte(nil), v2...)
		out[headerEnd-4], out[headerEnd-3], out[headerEnd-2], out[headerEnd-1] = byte(size>>24), byte(size>>16), byte(size>>8), byte(size)
		return out
	}
	withLength := func(length uint32) []byte {
		out := append([]byte(nil), v2[:headerEnd]...)
		return append(out, byte(length>>24), byte(length>>16), byte(length>>8), byte(length))
	}
	v1 := encryptBackupV1ForTest(t, randomBytes(t, 100), testBackupKey)
	v1Header := len(backupEncryptionFormatV1)
	v1WithSize := func(size uint32) []byte {
		out := append([]byte(nil), v1[:v1Header+backupEncryptionNonceV1]...)
		return append(out, byte(size>>24), byte(size>>16), byte(size>>8), byte(size))
	}
	for name, tc := range map[string]struct {
		data   []byte
		scheme string
		want   string
	}{
		"v2 header cut after the magic": {v2[:len(backupEncryptionFormat)+3], EncryptionScheme, "read encrypted backup header"},
		"v2 zero chunk size":            {withChunkSize(0), EncryptionScheme, "chunk size is invalid"},
		"v2 oversized chunk size":       {withChunkSize(17 * backupEncryptionChunk), EncryptionScheme, "chunk size is invalid"},
		"v2 no chunks":                  {v2[:headerEnd], EncryptionScheme, "final chunk is missing"},
		"v2 cut chunk length":           {v2[:headerEnd+2], EncryptionScheme, "read encrypted backup chunk"},
		"v2 chunk below the tag size":   {withLength(backupFinalChunkFlag | 3), EncryptionScheme, "chunk size is invalid"},
		"v2 chunk body cut short":       {v2[:len(v2)-5], EncryptionScheme, "read encrypted backup chunk"},
		"v1 nonce cut short":            {v1[:v1Header+4], backupEncryptionNameV1, "read encrypted backup nonce"},
		"v1 cut chunk length":           {v1[:v1Header+backupEncryptionNonceV1+2], backupEncryptionNameV1, "read encrypted backup chunk"},
		"v1 chunk below the tag size":   {v1WithSize(8), backupEncryptionNameV1, "chunk size is invalid"},
		"v1 chunk body cut short":       {v1[:len(v1)-5], backupEncryptionNameV1, "read encrypted backup chunk"},
	} {
		_, err := decryptTestArchive(t, tc.data, []string{testBackupKey}, tc.scheme)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	if _, err := decryptTestArchive(t, v1, []string{testBackupKeyPrev}, backupEncryptionNameV1); err == nil || !strings.Contains(err.Error(), "no configured backup encryption key") {
		t.Errorf("v1 with the wrong key = %v", err)
	}
}

// A correctly authenticated but short chunk that is not marked final would
// let an attacker who can re-encrypt drop data mid-stream; it is refused.
func TestDecryptionRefusesAShortChunkBeforeTheFinalChunk(t *testing.T) {
	sealedPath, _ := encryptTestArchive(t, []byte("payload"), testBackupKey)
	v2, err := os.ReadFile(sealedPath)
	if err != nil {
		t.Fatal(err)
	}
	header := v2[:backupEncryptionHeaderV2Len]
	salt := header[len(backupEncryptionFormat)+backupKeyIDBytes : len(backupEncryptionFormat)+backupKeyIDBytes+backupSaltBytes]
	aead, err := backupSubkeyV2(testBackupKey, salt)
	if err != nil {
		t.Fatal(err)
	}
	sealed := aead.Seal(nil, backupChunkNonceV2(0, false), []byte("short"), header)
	stream := append(append([]byte(nil), header...), byte(len(sealed)>>24), byte(len(sealed)>>16), byte(len(sealed)>>8), byte(len(sealed)))
	stream = append(stream, sealed...)
	if _, err := decryptTestArchive(t, stream, []string{testBackupKey}, EncryptionScheme); err == nil || !strings.Contains(err.Error(), "short chunk before the final chunk") {
		t.Fatalf("short non-final chunk = %v", err)
	}
}

func TestVersionOneDecryptionAuthenticatesEveryChunk(t *testing.T) {
	v1 := encryptBackupV1ForTest(t, randomBytes(t, backupEncryptionChunk+64), testBackupKey)
	v1[len(v1)-1] ^= 0xff
	if _, err := decryptTestArchive(t, v1, []string{testBackupKey}, backupEncryptionNameV1); err == nil || !strings.Contains(err.Error(), "chunk 1: authentication failed") {
		t.Fatalf("tampered second v1 chunk = %v", err)
	}
}

// The plaintext removal is checked, not assumed: if it cannot be removed as
// expected the caller hears about it, joined with any callback error.
func TestWithDecryptedArchiveReaderReportsRemovalFailures(t *testing.T) {
	sealedPath, _ := encryptTestArchive(t, []byte("payload"), testBackupKey)
	sealed, err := os.ReadFile(sealedPath)
	if err != nil {
		t.Fatal(err)
	}
	removeFirst := func(path string) error { return os.Remove(path) }
	if err := WithDecryptedArchiveReader(bytes.NewReader(sealed), []string{testBackupKey}, EncryptionScheme, removeFirst); err == nil || !strings.Contains(err.Error(), "remove decrypted backup archive") {
		t.Fatalf("removal failure = %v", err)
	}
	callbackErr := errors.New("restore failed")
	err = WithDecryptedArchiveReader(bytes.NewReader(sealed), []string{testBackupKey}, EncryptionScheme, func(path string) error {
		_ = removeFirst(path)
		return callbackErr
	})
	if !errors.Is(err, callbackErr) || !strings.Contains(err.Error(), "remove decrypted backup archive") {
		t.Fatalf("callback and removal failure = %v", err)
	}
}
