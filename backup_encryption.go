package stepanel

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// Backup archive encryption.
//
// Version 2 (written by this release) is a versioned envelope:
//
//	header = "STEPANEL-BACKUP-ENC-2\n" || key id (8) || salt (32) || chunk size (uint32 BE)
//	subkey = HKDF-SHA256(secret = configured key, salt, info = backupEncryptionInfoV2)
//	chunk  = uint32 BE (sealed length | finalChunkFlag) || AES-256-GCM sealed chunk
//	nonce  = 7 zero bytes || uint32 BE chunk index || final flag (0 or 1)
//	AAD    = the complete header
//
// Every backup has a fresh random salt and therefore its own subkey, so the
// deterministic per-chunk nonce is never reused under one key. Binding the
// header as associated data authenticates the key id, salt, and chunk size;
// binding the final flag into the nonce makes truncation at a chunk boundary
// and appended chunks detectable. The key id names the configured key a
// backup needs without revealing it, which is what lets operators rotate
// keys: the current key encrypts, and retired keys listed in
// STEPANEL_BACKUP_ENCRYPTION_PREVIOUS_KEYS still decrypt older backups.
//
// Version 1 (read-only) used SHA-256 of the key directly, a random 12-byte
// base nonce whose last four bytes were the chunk index, no associated data,
// and no final-chunk marker. It is still decrypted so existing backups stay
// restorable; nothing new is written in it.
const (
	backupEncryptionFormatV1 = "STEPANEL-BACKUP-ENC-1\n"
	backupEncryptionNameV1   = "AES-256-GCM-CHUNKED-v1"
	backupEncryptionNonceV1  = 12

	backupEncryptionFormat = "STEPANEL-BACKUP-ENC-2\n"
	backupEncryptionName   = "AES-256-GCM-HKDF-STREAM-v2"
	backupEncryptionInfoV2 = "stepanel backup archive encryption v2"

	backupEncryptionChunk  = 1 << 20
	backupKeyIDBytes       = 8
	backupSaltBytes        = 32
	backupFinalChunkFlag   = uint32(1) << 31
	backupMinEncryptionKey = 32
)

var backupEncryptionHeaderV2Len = len(backupEncryptionFormat) + backupKeyIDBytes + backupSaltBytes + 4

// isBackupEncryptionName reports whether a manifest names a supported scheme.
func isBackupEncryptionName(name string) bool {
	return name == backupEncryptionName || name == backupEncryptionNameV1
}

// backupKeyID identifies a configured backup encryption key without
// revealing it. It is stable for a key and recorded in each backup's header
// and manifest.
func backupKeyID(key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("stepanel backup encryption key id"))
	return hex.EncodeToString(mac.Sum(nil)[:backupKeyIDBytes])
}

func newBackupGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create backup encryption cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create backup encryption AEAD: %w", err)
	}
	return aead, nil
}

func usableBackupKey(key string) error {
	if len(key) < backupMinEncryptionKey {
		return errors.New("backup encryption key is not configured")
	}
	return nil
}

func backupSubkeyV2(key string, salt []byte) (cipher.AEAD, error) {
	subkey, err := hkdf.Key(sha256.New, []byte(key), salt, backupEncryptionInfoV2, 32)
	if err != nil {
		return nil, fmt.Errorf("derive backup encryption subkey: %w", err)
	}
	return newBackupGCM(subkey)
}

func backupChunkNonceV2(index uint32, final bool) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint32(nonce[7:11], index)
	if final {
		nonce[11] = 1
	}
	return nonce
}

func backupCipherV1(key string) (cipher.AEAD, error) {
	if err := usableBackupKey(key); err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(key))
	return newBackupGCM(hash[:])
}

func backupChunkNonceV1(base []byte, index uint32) []byte {
	nonce := append([]byte(nil), base...)
	binary.BigEndian.PutUint32(nonce[len(nonce)-4:], index)
	return nonce
}

// encryptBackupArchive writes src to dst in the current (v2) format and
// returns the id of the key it used.
func encryptBackupArchive(src, dst, key string) (string, error) {
	if err := usableBackupKey(key); err != nil {
		return "", err
	}
	input, _, err := openRegularNoFollow(src, nil)
	if err != nil {
		return "", err
	}
	defer input.Close()
	output, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	removeOnError := true
	defer func() {
		_ = output.Close()
		if removeOnError {
			_ = os.Remove(dst)
		}
	}()
	keyID := backupKeyID(key)
	rawKeyID, err := hex.DecodeString(keyID)
	if err != nil {
		return "", err
	}
	salt := make([]byte, backupSaltBytes)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("generate backup encryption salt: %w", err)
	}
	header := make([]byte, 0, backupEncryptionHeaderV2Len)
	header = append(header, backupEncryptionFormat...)
	header = append(header, rawKeyID...)
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, backupEncryptionChunk)
	aead, err := backupSubkeyV2(key, salt)
	if err != nil {
		return "", err
	}
	if _, err := output.Write(header); err != nil {
		return "", err
	}
	reader := bufio.NewReaderSize(input, backupEncryptionChunk)
	buffer := make([]byte, backupEncryptionChunk)
	sealed := make([]byte, 0, backupEncryptionChunk+aead.Overhead())
	for index := uint32(0); ; index++ {
		read, readErr := io.ReadFull(reader, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return "", readErr
		}
		final := readErr != nil
		if !final {
			// A full chunk is final only when nothing follows it.
			if _, peekErr := reader.Peek(1); peekErr == io.EOF {
				final = true
			} else if peekErr != nil {
				return "", peekErr
			}
		}
		if !final && index == backupFinalChunkFlag-1 {
			return "", errors.New("encrypted backup has too many chunks")
		}
		sealed = aead.Seal(sealed[:0], backupChunkNonceV2(index, final), buffer[:read], header)
		length := uint32(len(sealed))
		if final {
			length |= backupFinalChunkFlag
		}
		if err := binary.Write(output, binary.BigEndian, length); err != nil {
			return "", err
		}
		if _, err := output.Write(sealed); err != nil {
			return "", err
		}
		if final {
			break
		}
	}
	if err := output.Sync(); err != nil {
		return "", err
	}
	if err := output.Close(); err != nil {
		return "", err
	}
	removeOnError = false
	return keyID, nil
}

// decryptBackupArchiveReader decrypts input into a new file at dst. scheme is
// the encryption name from the signed manifest; the archive header must
// match it, so a manifest cannot be paired with a weaker format. keys holds
// every configured key, current first.
func decryptBackupArchiveReader(input io.Reader, dst string, keys []string, scheme string) error {
	var expected string
	switch scheme {
	case backupEncryptionName:
		expected = backupEncryptionFormat
	case backupEncryptionNameV1:
		expected = backupEncryptionFormatV1
	default:
		return fmt.Errorf("unsupported backup encryption scheme %q", scheme)
	}
	magic := make([]byte, len(expected))
	if _, err := io.ReadFull(input, magic); err != nil {
		return fmt.Errorf("read encrypted backup header: %w", err)
	}
	if string(magic) != expected {
		return errors.New("encrypted backup header does not match its manifest")
	}
	output, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	removeOnError := true
	defer func() {
		_ = output.Close()
		if removeOnError {
			_ = os.Remove(dst)
		}
	}()
	if expected == backupEncryptionFormat {
		err = decryptBackupStreamV2(input, output, keys, magic)
	} else {
		err = decryptBackupStreamV1(input, output, keys)
	}
	if err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	removeOnError = false
	return nil
}

func decryptBackupStreamV2(input io.Reader, output io.Writer, keys []string, magic []byte) error {
	header := make([]byte, backupEncryptionHeaderV2Len)
	copy(header, magic)
	if _, err := io.ReadFull(input, header[len(magic):]); err != nil {
		return fmt.Errorf("read encrypted backup header: %w", err)
	}
	keyID := hex.EncodeToString(header[len(magic) : len(magic)+backupKeyIDBytes])
	salt := header[len(magic)+backupKeyIDBytes : len(magic)+backupKeyIDBytes+backupSaltBytes]
	chunkSize := binary.BigEndian.Uint32(header[len(header)-4:])
	if chunkSize == 0 || chunkSize > 16*backupEncryptionChunk {
		return errors.New("encrypted backup chunk size is invalid")
	}
	key := ""
	for _, candidate := range keys {
		if usableBackupKey(candidate) == nil && backupKeyID(candidate) == keyID {
			key = candidate
			break
		}
	}
	if key == "" {
		return fmt.Errorf("encrypted backup needs backup encryption key %s, which is not configured (set it as STEPANEL_BACKUP_ENCRYPTION_KEY or list it in STEPANEL_BACKUP_ENCRYPTION_PREVIOUS_KEYS)", keyID)
	}
	aead, err := backupSubkeyV2(key, salt)
	if err != nil {
		return err
	}
	maxSealed := chunkSize + uint32(aead.Overhead())
	sealed := make([]byte, maxSealed)
	plain := make([]byte, 0, chunkSize)
	for index := uint32(0); ; index++ {
		var length uint32
		if err := binary.Read(input, binary.BigEndian, &length); err != nil {
			if err == io.EOF {
				return errors.New("encrypted backup is truncated: final chunk is missing")
			}
			return fmt.Errorf("read encrypted backup chunk: %w", err)
		}
		final := length&backupFinalChunkFlag != 0
		size := length &^ backupFinalChunkFlag
		if size < uint32(aead.Overhead()) || size > maxSealed {
			return errors.New("encrypted backup chunk size is invalid")
		}
		if _, err := io.ReadFull(input, sealed[:size]); err != nil {
			return fmt.Errorf("read encrypted backup chunk: %w", err)
		}
		plain, err = aead.Open(plain[:0], backupChunkNonceV2(index, final), sealed[:size], header)
		if err != nil {
			return fmt.Errorf("decrypt encrypted backup chunk %d: authentication failed", index)
		}
		if !final && uint32(len(plain)) != chunkSize {
			return errors.New("encrypted backup has a short chunk before the final chunk")
		}
		if _, err := output.Write(plain); err != nil {
			return err
		}
		if final {
			break
		}
		if index == backupFinalChunkFlag-1 {
			return errors.New("encrypted backup has too many chunks")
		}
	}
	var trailing [1]byte
	if n, err := input.Read(trailing[:]); n > 0 || (err != nil && err != io.EOF) {
		return errors.New("encrypted backup has data after its final chunk")
	}
	return nil
}

// decryptBackupStreamV1 reads the legacy format. It has no key id, so the
// key is the configured one that authenticates the first chunk.
func decryptBackupStreamV1(input io.Reader, output io.Writer, keys []string) error {
	baseNonce := make([]byte, backupEncryptionNonceV1)
	if _, err := io.ReadFull(input, baseNonce); err != nil {
		return fmt.Errorf("read encrypted backup nonce: %w", err)
	}
	var aead cipher.AEAD
	for index := uint32(0); ; index++ {
		var size uint32
		if err := binary.Read(input, binary.BigEndian, &size); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read encrypted backup chunk: %w", err)
		}
		if size < 16 || size > backupEncryptionChunk+16 {
			return errors.New("encrypted backup chunk size is invalid")
		}
		sealed := make([]byte, size)
		if _, err := io.ReadFull(input, sealed); err != nil {
			return fmt.Errorf("read encrypted backup chunk: %w", err)
		}
		nonce := backupChunkNonceV1(baseNonce, index)
		var plain []byte
		if aead == nil {
			for _, key := range keys {
				candidate, err := backupCipherV1(key)
				if err != nil {
					continue
				}
				if opened, err := candidate.Open(nil, nonce, sealed, nil); err == nil {
					aead, plain = candidate, opened
					break
				}
			}
			if aead == nil {
				return errors.New("no configured backup encryption key decrypts this backup")
			}
		} else {
			opened, err := aead.Open(nil, nonce, sealed, nil)
			if err != nil {
				return fmt.Errorf("decrypt encrypted backup chunk %d: authentication failed", index)
			}
			plain = opened
		}
		if _, err := output.Write(plain); err != nil {
			return err
		}
		if index == ^uint32(0) {
			return errors.New("encrypted backup has too many chunks")
		}
	}
}

// withDecryptedBackupArchiveReader decrypts the already-open encrypted input
// into a private temporary file. Keeping the input descriptor owned by the
// caller prevents a path replacement between checksum verification and
// decryption from changing the bytes that are consumed.
func withDecryptedBackupArchiveReader(input io.Reader, keys []string, scheme string, fn func(string) error) (returnErr error) {
	if !hasBackupKey(keys) {
		return errors.New("backup encryption key is not configured")
	}
	temp, err := os.CreateTemp("", ".backup-decrypt-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	_ = os.Remove(tempPath)
	// Keep a deferred fallback for panics, but do the normal removal
	// explicitly so a filesystem failure cannot silently leave plaintext
	// backup data behind.
	removed := false
	defer func() {
		if !removed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := decryptBackupArchiveReader(input, tempPath, keys, scheme); err != nil {
		return err
	}
	callbackErr := fn(tempPath)
	removeErr := os.Remove(tempPath)
	removed = removeErr == nil || os.IsNotExist(removeErr)
	if callbackErr != nil && removeErr != nil {
		return errors.Join(callbackErr, fmt.Errorf("remove decrypted backup archive: %w", removeErr))
	}
	if callbackErr != nil {
		return callbackErr
	}
	if removeErr != nil {
		return fmt.Errorf("remove decrypted backup archive: %w", removeErr)
	}
	return nil
}

func hasBackupKey(keys []string) bool {
	for _, key := range keys {
		if usableBackupKey(key) == nil {
			return true
		}
	}
	return false
}
