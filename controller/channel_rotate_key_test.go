package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test-only AES-256 keys (base64 of 32 bytes). Never used outside tests.
const (
	rotateTestKeyA = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	rotateTestKeyB = "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA="
)

func newRotateTestChannel(t *testing.T, key string, channelType int) *model.Channel {
	t.Helper()
	channel := &model.Channel{
		Type:   channelType,
		Key:    key,
		Name:   "rotate-test",
		Status: common.ChannelStatusEnabled,
		Models: "gpt-4o",
		Group:  "default",
	}
	require.NoError(t, model.DB.Create(channel).Error)
	return channel
}

// rawStoredRotateChannelKey reads the key column exactly as stored, bypassing
// the model hooks, so a test can assert on ciphertext versus plaintext.
func rawStoredRotateChannelKey(t *testing.T, id int) string {
	t.Helper()
	var raw string
	require.NoError(t, model.DB.Raw("SELECT `key` FROM channels WHERE id = ?", id).Scan(&raw).Error)
	return raw
}

func stubRotateTestSuccess(ctx context.Context, channel *model.Channel, testUserID int, testModel string, endpointType string, isStream bool) testResult {
	return testResult{}
}

func stubRotateTestFailure(ctx context.Context, channel *model.Channel, testUserID int, testModel string, endpointType string, isStream bool) testResult {
	return testResult{localErr: errors.New("upstream rejected the key")}
}

func TestRotateChannelKeySuccessReplacesAndEncrypts(t *testing.T) {
	t.Setenv("CHANNEL_KEY_ENCRYPTION_KEY", rotateTestKeyA)
	rootID := setupChannelCreateTest(t)
	channel := newRotateTestChannel(t, "sk-old-key", constant.ChannelTypeOpenAI)

	rotated, err := rotateChannelKey(context.Background(), channel, "sk-new-key", rootID, "", "", stubRotateTestSuccess)
	require.NoError(t, err)
	require.NotNil(t, rotated)

	stored := rawStoredRotateChannelKey(t, channel.Id)
	assert.True(t, strings.HasPrefix(stored, "enc:v1:"), "rotated key must be stored as ciphertext, got %q", stored)
	assert.NotContains(t, stored, "sk-new-key")

	reloaded, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "sk-new-key", reloaded.Key, "reads must decrypt the rotated key back to plaintext")
}

func TestRotateChannelKeyTestFailureKeepsOldKey(t *testing.T) {
	t.Setenv("CHANNEL_KEY_ENCRYPTION_KEY", rotateTestKeyA)
	rootID := setupChannelCreateTest(t)
	channel := newRotateTestChannel(t, "sk-old-key", constant.ChannelTypeOpenAI)
	before := rawStoredRotateChannelKey(t, channel.Id)

	_, err := rotateChannelKey(context.Background(), channel, "sk-new-key", rootID, "", "", stubRotateTestFailure)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream rejected the key")

	assert.Equal(t, before, rawStoredRotateChannelKey(t, channel.Id), "a failed test must leave the stored key untouched")
	reloaded, err := model.GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, "sk-old-key", reloaded.Key)
}

func newRotateKeyHandlerContext(t *testing.T, rootID, channelID int, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/channel/"+strconv.Itoa(channelID)+"/rotate_key", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(channelID)}}
	c.Set("id", rootID)
	c.Set("username", "root")
	c.Set("role", common.RoleRootUser)
	return c, w
}

func rotateKeyResponseSuccess(t *testing.T, w *httptest.ResponseRecorder) bool {
	t.Helper()
	var resp struct {
		Success bool `json:"success"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp.Success
}

func TestRotateChannelKeyHandlerRejectsEmptyKey(t *testing.T) {
	rootID := setupChannelCreateTest(t)
	channel := newRotateTestChannel(t, "sk-old-key", constant.ChannelTypeOpenAI)

	c, w := newRotateKeyHandlerContext(t, rootID, channel.Id, `{"key": "  "}`)
	RotateChannelKey(c)
	assert.False(t, rotateKeyResponseSuccess(t, w))
	assert.Equal(t, "sk-old-key", rawStoredRotateChannelKey(t, channel.Id))
}

func TestRotateChannelKeyHandlerRejectsMultiKey(t *testing.T) {
	rootID := setupChannelCreateTest(t)
	channel := &model.Channel{
		Type:   constant.ChannelTypeOpenAI,
		Key:    "sk-one\nsk-two",
		Name:   "rotate-multi-test",
		Status: common.ChannelStatusEnabled,
		Models: "gpt-4o",
		Group:  "default",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 2,
		},
	}
	require.NoError(t, model.DB.Create(channel).Error)

	c, w := newRotateKeyHandlerContext(t, rootID, channel.Id, `{"key": "sk-new-key"}`)
	RotateChannelKey(c)
	assert.False(t, rotateKeyResponseSuccess(t, w))
	assert.Equal(t, "sk-one\nsk-two", rawStoredRotateChannelKey(t, channel.Id), "refusing a multi-key rotation must not drop any key")
}

func TestRotateChannelKeyHandlerTestFailureKeepsOldKey(t *testing.T) {
	// A Midjourney channel fails the real channel test before any network
	// call, so the handler exercises the test-before-replace ordering
	// end to end without a live upstream.
	rootID := setupChannelCreateTest(t)
	channel := newRotateTestChannel(t, "sk-old-key", constant.ChannelTypeMidjourney)

	c, w := newRotateKeyHandlerContext(t, rootID, channel.Id, `{"key": "sk-new-key"}`)
	RotateChannelKey(c)
	assert.False(t, rotateKeyResponseSuccess(t, w))
	assert.Equal(t, "sk-old-key", rawStoredRotateChannelKey(t, channel.Id))
}

func TestRotateChannelKeyHandlerRejectsUnknownChannel(t *testing.T) {
	rootID := setupChannelCreateTest(t)

	c, w := newRotateKeyHandlerContext(t, rootID, 999999, `{"key": "sk-new-key"}`)
	RotateChannelKey(c)
	assert.False(t, rotateKeyResponseSuccess(t, w))
}
