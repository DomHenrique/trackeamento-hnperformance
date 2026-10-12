package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	EnvelopePrefix = "enc:v1:"
	GCMNonceSize   = 12
	AES256KeySize  = 32
)

var (
	ErrInvalidEnvelope     = errors.New("crypto: invalid credential envelope format")
	ErrUnknownKeyID        = errors.New("crypto: key id not found in keyring")
	ErrInvalidKeySize      = errors.New("crypto: key must be 32 bytes for AES-256")
	ErrMissingCurrentKey   = errors.New("crypto: current key id is empty or not in keyring")
	ErrInvalidKID          = errors.New("crypto: key id must not be empty or contain colon ':'")
	ErrAuthenticationFailed = errors.New("crypto: authentication failed, ciphertext or aad corrupted")
)

// KeyRing holds multiple AES-256 keys by key identifier (kid) and tracks the active key.
type KeyRing struct {
	keys       map[string][]byte
	currentKID string
}

// NewKeyRing constructs a KeyRing from a map of kid to 32-byte raw keys.
func NewKeyRing(rawKeys map[string][]byte, currentKID string) (*KeyRing, error) {
	if strings.TrimSpace(currentKID) == "" || strings.Contains(currentKID, ":") {
		return nil, ErrInvalidKID
	}
	if len(rawKeys) == 0 {
		return nil, errors.New("crypto: keyring cannot be empty")
	}

	keysCopy := make(map[string][]byte, len(rawKeys))
	for kid, key := range rawKeys {
		if strings.TrimSpace(kid) == "" || strings.Contains(kid, ":") {
			return nil, fmt.Errorf("%w: %q", ErrInvalidKID, kid)
		}
		if len(key) != AES256KeySize {
			return nil, fmt.Errorf("%w: key %q has %d bytes", ErrInvalidKeySize, kid, len(key))
		}
		k := make([]byte, AES256KeySize)
		copy(k, key)
		keysCopy[kid] = k
	}

	if _, ok := keysCopy[currentKID]; !ok {
		return nil, fmt.Errorf("%w: current kid %q not found in keys", ErrMissingCurrentKey, currentKID)
	}

	return &KeyRing{
		keys:       keysCopy,
		currentKID: currentKID,
	}, nil
}

// NewKeyRingFromJSON parses a JSON string mapping kid to hex-encoded 32-byte keys.
// Example: {"k1": "0123...cdef", "k2": "abcd...9876"}
func NewKeyRingFromJSON(keysJSON, currentKID string) (*KeyRing, error) {
	if strings.TrimSpace(keysJSON) == "" {
		return nil, errors.New("crypto: keys JSON string is empty")
	}

	var hexMap map[string]string
	if err := json.Unmarshal([]byte(keysJSON), &hexMap); err != nil {
		return nil, fmt.Errorf("crypto: invalid keys JSON format: %w", err)
	}

	rawKeys := make(map[string][]byte, len(hexMap))
	for kid, hexKey := range hexMap {
		rawKey, err := hex.DecodeString(strings.TrimSpace(hexKey))
		if err != nil {
			return nil, fmt.Errorf("crypto: invalid hex key for kid %q: %w", kid, err)
		}
		rawKeys[kid] = rawKey
	}

	return NewKeyRing(rawKeys, currentKID)
}

// CurrentKID returns the active key id for new encryptions.
func (kr *KeyRing) CurrentKID() string {
	return kr.currentKID
}

// BuildAAD creates deterministic Additional Authenticated Data string.
func BuildAAD(siteID, platform, fieldName string) string {
	return fmt.Sprintf("%s|%s|%s", siteID, platform, fieldName)
}

// IsEncrypted returns true if the value has the canonical envelope prefix.
func IsEncrypted(val string) bool {
	return strings.HasPrefix(val, EnvelopePrefix)
}

// ExtractKID extracts the kid from an encrypted envelope string.
func ExtractKID(val string) (string, error) {
	if !IsEncrypted(val) {
		return "", ErrInvalidEnvelope
	}
	parts := strings.Split(val, ":")
	if len(parts) != 5 || parts[0] != "enc" || parts[1] != "v1" {
		return "", ErrInvalidEnvelope
	}
	return parts[2], nil
}

// Encrypt encrypts plaintext using the current active key and returns enc:v1:<kid>:<nonce>:<ciphertext_tag>.
func (kr *KeyRing) Encrypt(plaintext, aad string) (string, error) {
	key, ok := kr.keys[kr.currentKID]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownKeyID, kr.currentKID)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to create gcm: %w", err)
	}

	nonce := make([]byte, GCMNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: failed to generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), []byte(aad))

	nonceB64 := base64.RawURLEncoding.EncodeToString(nonce)
	ciphertextB64 := base64.RawURLEncoding.EncodeToString(ciphertext)

	return fmt.Sprintf("%s%s:%s:%s", EnvelopePrefix, kr.currentKID, nonceB64, ciphertextB64), nil
}

// Decrypt decrypts an envelope value with the corresponding kid.
// If val is not prefixed with enc:v1:, it is returned as plaintext (transparent fallback).
func (kr *KeyRing) Decrypt(val, aad string) (string, error) {
	if !IsEncrypted(val) {
		return val, nil
	}

	parts := strings.Split(val, ":")
	if len(parts) != 5 || parts[0] != "enc" || parts[1] != "v1" {
		return "", ErrInvalidEnvelope
	}

	kid := parts[2]
	nonceB64 := parts[3]
	ciphertextB64 := parts[4]

	key, ok := kr.keys[kid]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownKeyID, kid)
	}

	nonce, err := base64.RawURLEncoding.DecodeString(nonceB64)
	if err != nil || len(nonce) != GCMNonceSize {
		return "", ErrInvalidEnvelope
	}

	ciphertext, err := base64.RawURLEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", ErrInvalidEnvelope
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: failed to create gcm: %w", err)
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		return "", ErrAuthenticationFailed
	}

	return string(plaintext), nil
}

// EncryptField is a helper to encrypt a specific integration credential field.
func (kr *KeyRing) EncryptField(value, siteID, platform, fieldName string) (string, error) {
	aad := BuildAAD(siteID, platform, fieldName)
	return kr.Encrypt(value, aad)
}

// DecryptField is a helper to decrypt a specific integration credential field.
func (kr *KeyRing) DecryptField(value, siteID, platform, fieldName string) (string, error) {
	aad := BuildAAD(siteID, platform, fieldName)
	return kr.Decrypt(value, aad)
}
