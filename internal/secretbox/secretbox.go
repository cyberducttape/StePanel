// Package secretbox seals small secrets at rest (environment secrets, MFA
// seeds, durable job payloads) so that a ciphertext only opens in the context
// it was written for.
//
// Each Box derives its own AES-256 key from the operator's master key with
// HKDF-SHA256, using the purpose as the HKDF info, and authenticates the
// purpose plus a caller-supplied context (for example the site and variable
// name) as AES-GCM associated data. Someone who can rewrite the state
// database therefore cannot move a ciphertext to another record, another
// owner or another purpose: it fails to open instead of silently decrypting
// as someone else's secret.
//
// Earlier releases sealed with AES-GCM under SHA-256(master key) and no
// associated data. OpenLegacy reads that format only so stores can migrate
// it; callers must stop accepting it once a store has been re-sealed.
//
// To rotate a master key, NewWithPrevious builds a box that seals with the
// new key and still opens values sealed under listed previous keys. A store
// that opened anything with a previous key re-seals its values under the new
// key; the operator removes the previous key once every store has done so.
package secretbox

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
)

// Magic prefixes every sealed value in the current format.
var Magic = []byte("SPB2")

const aadLabel = "stepanel-secretbox-v2"

// ErrNotSealed reports a value that is not in the current sealed format.
var ErrNotSealed = errors.New("secretbox: value is not in the context-bound format")

// Box seals and opens values for one purpose.
type Box struct {
	purpose string
	aead    cipher.AEAD
	// previous holds the keys of earlier master keys, newest first. They
	// only open; Seal always uses aead.
	previous      []cipher.AEAD
	previousOpens atomic.Uint64
}

// New derives the key for purpose from the operator master key.
func New(masterKey []byte, purpose string) (*Box, error) {
	return NewWithPrevious(masterKey, nil, purpose)
}

// NewWithPrevious derives the key for purpose from masterKey and also from
// each previous master key (newest first), which are used only to open.
func NewWithPrevious(masterKey []byte, previous [][]byte, purpose string) (*Box, error) {
	if len(masterKey) == 0 {
		return nil, errors.New("secretbox: master key is empty")
	}
	if purpose == "" {
		return nil, errors.New("secretbox: purpose is required")
	}
	aead, err := deriveAEAD(masterKey, purpose)
	if err != nil {
		return nil, err
	}
	box := &Box{purpose: purpose, aead: aead}
	for i, key := range previous {
		if len(key) == 0 {
			return nil, fmt.Errorf("secretbox: previous key %d is empty", i+1)
		}
		if bytes.Equal(key, masterKey) {
			return nil, fmt.Errorf("secretbox: previous key %d repeats the current key", i+1)
		}
		old, err := deriveAEAD(key, purpose)
		if err != nil {
			return nil, err
		}
		box.previous = append(box.previous, old)
	}
	return box, nil
}

func deriveAEAD(masterKey []byte, purpose string) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, masterKey, nil, "stepanel/secretbox/v2/"+purpose, 32)
	if err != nil {
		return nil, fmt.Errorf("secretbox: derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// associatedData encodes the purpose and context unambiguously: every field
// is length-prefixed, so ("ab","c") and ("a","bc") differ.
func (b *Box) associatedData(context []string) []byte {
	var buf bytes.Buffer
	buf.WriteString(aadLabel)
	writeField := func(value string) {
		var length [binary.MaxVarintLen64]byte
		buf.Write(length[:binary.PutUvarint(length[:], uint64(len(value)))])
		buf.WriteString(value)
	}
	writeField(b.purpose)
	for _, field := range context {
		writeField(field)
	}
	return buf.Bytes()
}

// Seal encrypts plaintext bound to context. The result is
// Magic || nonce || ciphertext.
func (b *Box) Seal(plaintext []byte, context ...string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	maxInt := int(^uint(0) >> 1)
	prefixLen := len(Magic) + len(nonce)
	if len(plaintext) > maxInt-prefixLen-b.aead.Overhead() {
		return nil, errors.New("secretbox: plaintext is too large")
	}
	out := make([]byte, 0, prefixLen+len(plaintext)+b.aead.Overhead())
	out = append(out, Magic...)
	out = append(out, nonce...)
	return b.aead.Seal(out, nonce, plaintext, b.associatedData(context)), nil
}

// Open decrypts a value sealed by Seal with the same purpose and context,
// under the current key or a previous one.
func (b *Box) Open(sealed []byte, context ...string) ([]byte, error) {
	plain, _, err := b.OpenReportingPrevious(sealed, context...)
	return plain, err
}

// OpenReportingPrevious is Open that also reports whether the value opened
// only with a previous key, so the caller can re-seal it under the current
// key.
func (b *Box) OpenReportingPrevious(sealed []byte, context ...string) ([]byte, bool, error) {
	if !IsSealed(sealed) {
		return nil, false, ErrNotSealed
	}
	body := sealed[len(Magic):]
	// Every key uses AES-256-GCM, so the nonce and tag sizes are shared.
	if len(body) < b.aead.NonceSize()+b.aead.Overhead() {
		return nil, false, errors.New("secretbox: sealed value is truncated")
	}
	nonce, ciphertext := body[:b.aead.NonceSize()], body[b.aead.NonceSize():]
	associated := b.associatedData(context)
	if plain, err := b.aead.Open(nil, nonce, ciphertext, associated); err == nil {
		return plain, false, nil
	}
	for _, old := range b.previous {
		if plain, err := old.Open(nil, nonce, ciphertext, associated); err == nil {
			b.previousOpens.Add(1)
			return plain, true, nil
		}
	}
	return nil, false, errors.New("secretbox: value does not open in this context")
}

// HasPrevious reports whether the box was built with previous keys.
func (b *Box) HasPrevious() bool {
	return len(b.previous) > 0
}

// OpenedWithPrevious reports how many values this box has opened only with
// a previous key since it was created.
func (b *Box) OpenedWithPrevious() uint64 {
	return b.previousOpens.Load()
}

// IsSealed reports whether value carries the current format prefix.
func IsSealed(value []byte) bool {
	return bytes.HasPrefix(value, Magic)
}

// OpenLegacy decrypts the pre-v2 format: AES-GCM under SHA-256(masterKey),
// nonce || ciphertext, no associated data. Use it only to migrate a store.
func OpenLegacy(masterKey, nonceAndCiphertext []byte) ([]byte, error) {
	return OpenLegacyWithKey(LegacyKey(masterKey), nonceAndCiphertext)
}

// LegacyKey is the pre-v2 key derivation, SHA-256 of the master key.
func LegacyKey(masterKey []byte) []byte {
	digest := sha256.Sum256(masterKey)
	return digest[:]
}

// OpenLegacyWithKey decrypts the pre-v2 format with an already derived key.
func OpenLegacyWithKey(key, nonceAndCiphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonceAndCiphertext) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("secretbox: legacy value is truncated")
	}
	nonce := nonceAndCiphertext[:aead.NonceSize()]
	return aead.Open(nil, nonce, nonceAndCiphertext[aead.NonceSize():], nil)
}
