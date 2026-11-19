package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"gorm.io/gorm"
)

// Channel key encryption at rest.
//
// Upstream channel keys (Channel.Key) would otherwise sit in the channels
// table as plaintext, so anyone who reads the database — a leaked backup, a
// read-only replica, a SQL injection elsewhere — walks away with every
// upstream credential. When the CHANNEL_KEY_ENCRYPTION_KEY environment
// variable holds a base64-encoded 32-byte key, channel keys are stored as
// AES-256-GCM ciphertext instead:
//
//	enc:v1:<base64(nonce || ciphertext)>
//
// The enc:v1: prefix versions the format and marks a value as encrypted.
// Rows written before the feature was enabled carry no prefix and are read
// back unchanged, so an existing deployment keeps working and migrates
// lazily: a row is encrypted the next time its key is written. When the
// variable is unset, encryption is disabled and every hook below is a no-op,
// so behaviour is identical to upstream.
//
// The encryption key lives only in the environment, never in the database
// or the repository. WARNING: losing it makes every encrypted channel key
// unrecoverable — back it up separately from the database.

const (
	channelKeyEncryptionEnvVar = "CHANNEL_KEY_ENCRYPTION_KEY"
	channelKeyCipherPrefix     = "enc:v1:"
)

var (
	channelKeyCipherMu     sync.Mutex
	channelKeyCipherRaw    string
	channelKeyCipherAEAD   cipher.AEAD
	channelKeyCipherErr    error
	channelKeyCipherLoaded bool
)

// channelKeyCipher returns the configured AEAD, or (nil, nil) when channel
// key encryption is not configured. A malformed key is a sticky error: the
// process must never silently fall back to plaintext or to a wrong key.
func channelKeyCipher() (cipher.AEAD, error) {
	raw := strings.TrimSpace(os.Getenv(channelKeyEncryptionEnvVar))
	channelKeyCipherMu.Lock()
	defer channelKeyCipherMu.Unlock()
	if channelKeyCipherLoaded && channelKeyCipherRaw == raw {
		return channelKeyCipherAEAD, channelKeyCipherErr
	}
	channelKeyCipherLoaded = true
	channelKeyCipherRaw = raw
	channelKeyCipherAEAD = nil
	channelKeyCipherErr = nil
	if raw == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		channelKeyCipherErr = fmt.Errorf("%s is not valid base64: %w", channelKeyEncryptionEnvVar, err)
		return nil, channelKeyCipherErr
	}
	if len(key) != 32 {
		channelKeyCipherErr = fmt.Errorf("%s must decode to 32 bytes for AES-256, got %d bytes", channelKeyEncryptionEnvVar, len(key))
		return nil, channelKeyCipherErr
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		channelKeyCipherErr = err
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		channelKeyCipherErr = err
		return nil, err
	}
	channelKeyCipherAEAD = aead
	return aead, nil
}

// ValidateChannelKeyEncryption fails fast at startup when a configured
// encryption key is malformed, instead of on the first channel read or
// write. It is a no-op when encryption is not configured.
func ValidateChannelKeyEncryption() error {
	_, err := channelKeyCipher()
	return err
}

// encryptChannelKey seals a plaintext channel key for storage. The value
// passes through unchanged when encryption is not configured, the value is
// empty, or the value is already encrypted — re-encrypting ciphertext would
// make it unreadable, so the prefix check is also the double-encryption
// guard for every write path.
func encryptChannelKey(plain string) (string, error) {
	if plain == "" || strings.HasPrefix(plain, channelKeyCipherPrefix) {
		return plain, nil
	}
	aead, err := channelKeyCipher()
	if err != nil {
		return "", err
	}
	if aead == nil {
		return plain, nil
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(plain), nil)
	return channelKeyCipherPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// decryptChannelKey opens a stored channel key. Values without the enc:v1:
// prefix are legacy plaintext and pass through unchanged. A prefixed value
// that cannot be opened — wrong key, missing key, corrupt ciphertext — is
// an error, never silently returned garbage.
func decryptChannelKey(stored string) (string, error) {
	if !strings.HasPrefix(stored, channelKeyCipherPrefix) {
		return stored, nil
	}
	aead, err := channelKeyCipher()
	if err != nil {
		return "", err
	}
	if aead == nil {
		return "", errors.New("channel key is encrypted but " + channelKeyEncryptionEnvVar + " is not set")
	}
	sealed, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, channelKeyCipherPrefix))
	if err != nil {
		return "", fmt.Errorf("channel key ciphertext is not valid base64: %w", err)
	}
	if len(sealed) <= aead.NonceSize() {
		return "", errors.New("channel key ciphertext is too short")
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt channel key (wrong %s?): %w", channelKeyEncryptionEnvVar, err)
	}
	return string(plain), nil
}

// GORM hooks on Channel: Key is encrypted on the way to the database and
// decrypted on the way back, so every caller above the model layer —
// GetKeys, GetNextEnabledKey, the channel cache, the admin API — keeps
// working with plaintext. AfterCreate and AfterUpdate restore the in-memory
// plaintext that the Before hooks replaced with ciphertext, so an object
// that was just written, or one that lives in the channel cache and is
// saved again for an unrelated column, never leaks the stored form into
// another write.

func (channel *Channel) encryptKeyForStorage() error {
	encrypted, err := encryptChannelKey(channel.Key)
	if err != nil {
		return err
	}
	channel.Key = encrypted
	return nil
}

func (channel *Channel) decryptKeyInMemory() error {
	plain, err := decryptChannelKey(channel.Key)
	if err != nil {
		return err
	}
	channel.Key = plain
	return nil
}

func (channel *Channel) BeforeCreate(tx *gorm.DB) error { return channel.encryptKeyForStorage() }

func (channel *Channel) BeforeUpdate(tx *gorm.DB) error { return channel.encryptKeyForStorage() }

func (channel *Channel) AfterCreate(tx *gorm.DB) error { return channel.decryptKeyInMemory() }

func (channel *Channel) AfterUpdate(tx *gorm.DB) error { return channel.decryptKeyInMemory() }

func (channel *Channel) AfterFind(tx *gorm.DB) error { return channel.decryptKeyInMemory() }
