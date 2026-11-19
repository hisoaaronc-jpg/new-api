package model

import (
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two distinct, valid AES-256 keys (base64 of 32 bytes), plus the plaintext
// keys used below. Test-only values, never used outside this file.
const (
	testChannelKeyA = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	testChannelKeyB = "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA="
)

// setChannelKeyEnv points the encryption key env var at value for the rest
// of the test. The cipher cache keys on the raw value, so switching between
// phases of one test takes effect immediately.
func setChannelKeyEnv(t *testing.T, value string) {
	t.Helper()
	old, had := os.LookupEnv(channelKeyEncryptionEnvVar)
	require.NoError(t, os.Setenv(channelKeyEncryptionEnvVar, value))
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(channelKeyEncryptionEnvVar, old)
		} else {
			_ = os.Unsetenv(channelKeyEncryptionEnvVar)
		}
	})
}

// rawStoredChannelKey reads the key column exactly as stored, bypassing the
// model hooks, so a test can assert on ciphertext versus plaintext.
func rawStoredChannelKey(t *testing.T, id int) string {
	t.Helper()
	var raw string
	require.NoError(t, DB.Raw("SELECT "+commonKeyCol+" FROM channels WHERE id = ?", id).Scan(&raw).Error)
	return raw
}

func newTestChannel(key string) *Channel {
	return &Channel{
		Type:   1,
		Key:    key,
		Name:   "crypto-test",
		Status: common.ChannelStatusEnabled,
		Models: "gpt-4o",
		Group:  "default",
	}
}

func TestChannelKeyEncryptionRoundTrip(t *testing.T) {
	truncateTables(t)
	setChannelKeyEnv(t, testChannelKeyA)

	ch := newTestChannel("sk-test-secret")
	require.NoError(t, DB.Create(ch).Error)
	assert.Equal(t, "sk-test-secret", ch.Key, "in-memory key must be plaintext again after create")

	raw := rawStoredChannelKey(t, ch.Id)
	assert.True(t, strings.HasPrefix(raw, channelKeyCipherPrefix), "stored key must carry the enc:v1: prefix, got %q", raw)
	assert.NotContains(t, raw, "sk-test-secret")

	loaded, err := GetChannelById(ch.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "sk-test-secret", loaded.Key)
	assert.Equal(t, []string{"sk-test-secret"}, loaded.GetKeys())
	key, _, apiErr := loaded.GetNextEnabledKey()
	require.Nil(t, apiErr)
	assert.Equal(t, "sk-test-secret", key)
}

func TestChannelKeyEncryptionLegacyPlaintextRow(t *testing.T) {
	truncateTables(t)
	setChannelKeyEnv(t, "")

	ch := newTestChannel("sk-legacy")
	require.NoError(t, DB.Create(ch).Error)
	require.Equal(t, "sk-legacy", rawStoredChannelKey(t, ch.Id))

	// Enable encryption after the row exists: the legacy plaintext row must
	// still read back, and must be encrypted on its next key write.
	setChannelKeyEnv(t, testChannelKeyA)
	loaded, err := GetChannelById(ch.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "sk-legacy", loaded.Key)

	stored, err := UpdateChannelLocked(ch.Id, func(channel *Channel) error {
		assert.Equal(t, "sk-legacy", channel.Key, "locked update must see plaintext")
		channel.Key = "sk-rotated"
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "sk-rotated", stored.Key)
	raw := rawStoredChannelKey(t, ch.Id)
	assert.True(t, strings.HasPrefix(raw, channelKeyCipherPrefix), "rotated key must be stored encrypted, got %q", raw)

	reloaded, err := GetChannelById(ch.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "sk-rotated", reloaded.Key)
}

func TestChannelKeyEncryptionWrongKeyFails(t *testing.T) {
	truncateTables(t)
	setChannelKeyEnv(t, testChannelKeyA)

	ch := newTestChannel("sk-guard")
	require.NoError(t, DB.Create(ch).Error)
	require.True(t, strings.HasPrefix(rawStoredChannelKey(t, ch.Id), channelKeyCipherPrefix))

	setChannelKeyEnv(t, testChannelKeyB)
	_, err := GetChannelById(ch.Id, true)
	require.Error(t, err, "reading with the wrong key must fail, not return garbage")

	setChannelKeyEnv(t, "")
	_, err = GetChannelById(ch.Id, true)
	require.Error(t, err, "reading an encrypted row without the key configured must fail")

	setChannelKeyEnv(t, testChannelKeyA)
	loaded, err := GetChannelById(ch.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "sk-guard", loaded.Key)
}

func TestChannelKeyEncryptionDisabledIsIdentity(t *testing.T) {
	truncateTables(t)
	setChannelKeyEnv(t, "")

	ch := newTestChannel("sk-plain")
	require.NoError(t, DB.Create(ch).Error)
	assert.Equal(t, "sk-plain", rawStoredChannelKey(t, ch.Id))

	loaded, err := GetChannelById(ch.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "sk-plain", loaded.Key)
}

func TestChannelKeyEncryptionNoDoubleEncrypt(t *testing.T) {
	truncateTables(t)
	setChannelKeyEnv(t, testChannelKeyA)

	once, err := encryptChannelKey("sk-once")
	require.NoError(t, err)
	twice, err := encryptChannelKey(once)
	require.NoError(t, err)
	assert.Equal(t, once, twice, "encrypting ciphertext must be a no-op")
	plain, err := decryptChannelKey(twice)
	require.NoError(t, err)
	assert.Equal(t, "sk-once", plain)

	ch := newTestChannel("sk-stable")
	require.NoError(t, DB.Create(ch).Error)
	before := rawStoredChannelKey(t, ch.Id)

	// A locked update that does not touch the key must not rewrite it.
	_, err = UpdateChannelLocked(ch.Id, func(channel *Channel) error {
		channel.Status = common.ChannelStatusManuallyDisabled
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, before, rawStoredChannelKey(t, ch.Id))

	// Saving an unrelated column through the struct hooks must leave both
	// the stored ciphertext and the in-memory plaintext untouched.
	loaded, err := GetChannelById(ch.Id, true)
	require.NoError(t, err)
	require.NoError(t, loaded.SaveChannelInfo())
	assert.Equal(t, "sk-stable", loaded.Key)
	assert.Equal(t, before, rawStoredChannelKey(t, ch.Id))
}

func TestChannelKeyEncryptionMultiKey(t *testing.T) {
	truncateTables(t)
	setChannelKeyEnv(t, testChannelKeyA)

	ch := newTestChannel("sk-a\nsk-b")
	ch.ChannelInfo.IsMultiKey = true
	ch.ChannelInfo.MultiKeySize = 2
	require.NoError(t, DB.Create(ch).Error)
	raw := rawStoredChannelKey(t, ch.Id)
	assert.True(t, strings.HasPrefix(raw, channelKeyCipherPrefix))
	assert.NotContains(t, raw, "sk-a")

	loaded, err := GetChannelById(ch.Id, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"sk-a", "sk-b"}, loaded.GetKeys())

	stored, err := UpdateChannelLocked(ch.Id, func(channel *Channel) error {
		channel.Key = "sk-c\nsk-d"
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sk-c", "sk-d"}, stored.GetKeys())
	assert.True(t, strings.HasPrefix(rawStoredChannelKey(t, ch.Id), channelKeyCipherPrefix))
}

func TestValidateChannelKeyEncryption(t *testing.T) {
	setChannelKeyEnv(t, "not-valid-base64!!!")
	require.Error(t, ValidateChannelKeyEncryption())

	setChannelKeyEnv(t, "MDEyMzQ1Njc4OWFiY2RlZg==") // base64 of 16 bytes, not 32
	require.Error(t, ValidateChannelKeyEncryption())

	setChannelKeyEnv(t, testChannelKeyA)
	require.NoError(t, ValidateChannelKeyEncryption())

	setChannelKeyEnv(t, "")
	require.NoError(t, ValidateChannelKeyEncryption())
}
