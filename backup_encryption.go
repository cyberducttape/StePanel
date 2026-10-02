package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	backupEncryptionFormat = "STEPANEL-BACKUP-ENC-1\n"
	backupEncryptionName   = "AES-256-GCM-CHUNKED-v1"
	backupEncryptionChunk  = 1 << 20
	backupEncryptionNonce  = 12
)

func backupCipher(key string) (cipher.AEAD, error) {
	if len(key) < 32 {
		return nil, errors.New("backup encryption key is not configured")
	}
	hash := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(hash[:])
	if err != nil {
		return nil, fmt.Errorf("create backup encryption cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create backup encryption AEAD: %w", err)
	}
	return aead, nil
}

func backupChunkNonce(base []byte, index uint32) []byte {
	nonce := append([]byte(nil), base...)
	binary.BigEndian.PutUint32(nonce[len(nonce)-4:], index)
	return nonce
}

func encryptBackupArchive(src, dst, key string) error {
	aead, err := backupCipher(key)
	if err != nil {
		return err
	}
	input, _, err := openRegularNoFollow(src, nil)
	if err != nil {
		return err
	}
	defer input.Close()
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
	if _, err := io.WriteString(output, backupEncryptionFormat); err != nil {
		return err
	}
	baseNonce := make([]byte, backupEncryptionNonce)
	if _, err := io.ReadFull(rand.Reader, baseNonce); err != nil {
		return err
	}
	if _, err := output.Write(baseNonce); err != nil {
		return err
	}
	buffer := make([]byte, backupEncryptionChunk)
	var index uint32
	for {
		read, readErr := io.ReadFull(input, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return readErr
		}
		if read > 0 {
			if index == ^uint32(0) {
				return errors.New("encrypted backup has too many chunks")
			}
			sealed := aead.Seal(nil, backupChunkNonce(baseNonce, index), buffer[:read], nil)
			if err := binary.Write(output, binary.BigEndian, uint32(len(sealed))); err != nil {
				return err
			}
			if _, err := output.Write(sealed); err != nil {
				return err
			}
			index++
		}
		if readErr != nil {
			break
		}
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

func decryptBackupArchive(src, dst, key string) error {
	aead, err := backupCipher(key)
	if err != nil {
		return err
	}
	input, _, err := openRegularNoFollow(src, nil)
	if err != nil {
		return err
	}
	defer input.Close()
	magic := make([]byte, len(backupEncryptionFormat))
	if _, err := io.ReadFull(input, magic); err != nil {
		return fmt.Errorf("read encrypted backup header: %w", err)
	}
	if string(magic) != backupEncryptionFormat {
		return errors.New("unsupported encrypted backup format")
	}
	baseNonce := make([]byte, backupEncryptionNonce)
	if _, err := io.ReadFull(input, baseNonce); err != nil {
		return fmt.Errorf("read encrypted backup nonce: %w", err)
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
	var index uint32
	for {
		var size uint32
		if err := binary.Read(input, binary.BigEndian, &size); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("read encrypted backup chunk: %w", err)
		}
		if size < uint32(aead.Overhead()) || size > backupEncryptionChunk+uint32(aead.Overhead()) {
			return errors.New("encrypted backup chunk size is invalid")
		}
		sealed := make([]byte, size)
		if _, err := io.ReadFull(input, sealed); err != nil {
			return fmt.Errorf("read encrypted backup chunk: %w", err)
		}
		plain, err := aead.Open(nil, backupChunkNonce(baseNonce, index), sealed, nil)
		if err != nil {
			return fmt.Errorf("decrypt encrypted backup chunk %d: %w", index, err)
		}
		if _, err := output.Write(plain); err != nil {
			return err
		}
		if index == ^uint32(0) {
			return errors.New("encrypted backup has too many chunks")
		}
		index++
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

func withDecryptedBackupArchive(path, key string, fn func(string) error) (returnErr error) {
	if key == "" {
		return fn(path)
	}
	probe, _, err := openRegularNoFollow(path, nil)
	if err != nil {
		return err
	}
	magic := make([]byte, len(backupEncryptionFormat))
	_, probeErr := io.ReadFull(probe, magic)
	_ = probe.Close()
	if probeErr == io.EOF || probeErr == io.ErrUnexpectedEOF || string(magic) != backupEncryptionFormat {
		return fn(path)
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
		if removed {
			return
		}
		_ = os.Remove(tempPath)
	}()
	if err := decryptBackupArchive(path, tempPath, key); err != nil {
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
