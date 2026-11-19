package controller

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// RotateChannelKeyRequest is the body of POST /api/channel/:id/rotate_key.
type RotateChannelKeyRequest struct {
	Key          string `json:"key" binding:"required"`
	Model        string `json:"model"`
	EndpointType string `json:"endpoint_type"`
}

// RotateChannelKey tests a replacement upstream key against the channel and,
// only when the test call succeeds, atomically replaces the stored key. The
// new key never lands in the database untested, and a failed test leaves the
// old key untouched. The replacement goes through UpdateChannelLocked, so it
// is stored as enc:v1: ciphertext when channel key encryption is enabled.
func RotateChannelKey(c *gin.Context) {
	channelId, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var req RotateChannelKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	newKey := strings.TrimSpace(req.Key)
	if newKey == "" {
		common.ApiErrorT(c, "Key is required")
		return
	}
	channel, err := model.GetChannelById(channelId, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	// A multi-key channel holds a key list, not one key: replacing the whole
	// list with a single key would silently drop the other keys, so rotation
	// refuses and the administrator edits the list instead.
	if channel.ChannelInfo.IsMultiKey {
		common.ApiErrorT(c, "Multi-key channels cannot rotate a single key; edit the channel instead")
		return
	}
	testUserID, err := resolveChannelTestUserID(c)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	requestCtx := context.Background()
	if c.Request != nil {
		requestCtx = c.Request.Context()
	}
	rotated, err := rotateChannelKey(requestCtx, channel, newKey, testUserID, req.Model, req.EndpointType, testChannel)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "channel.rotate_key", map[string]any{
		"channel_id":   channelId,
		"channel_name": rotated.Name,
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    gin.H{"id": rotated.Id},
	})
}

// rotateChannelKeyTest is the channel test used by rotateChannelKey. It is a
// variable so tests can stub the upstream call and cover the replace path
// without a live provider.
type rotateChannelKeyTest func(ctx context.Context, channel *model.Channel, testUserID int, testModel string, endpointType string, isStream bool) testResult

// rotateChannelKey tests newKey against a copy of channel and, only when the
// test succeeds, atomically replaces the stored key. The copy keeps the test
// from mutating the loaded channel, and the replacement is a single locked
// write, so no request can read a half-rotated key.
func rotateChannelKey(ctx context.Context, channel *model.Channel, newKey string, testUserID int, testModel, endpointType string, test rotateChannelKeyTest) (*model.Channel, error) {
	testChannelCopy := *channel
	testChannelCopy.Key = newKey
	result := test(ctx, &testChannelCopy, testUserID, testModel, endpointType, false)
	if result.localErr != nil {
		return nil, result.localErr
	}
	if result.newAPIError != nil {
		return nil, result.newAPIError
	}
	return model.UpdateChannelLocked(channel.Id, func(ch *model.Channel) error {
		ch.Key = newKey
		return nil
	})
}
