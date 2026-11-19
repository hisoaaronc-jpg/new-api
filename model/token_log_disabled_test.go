package model

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLogDisabledTestToken creates a user and a token owned by them. The token
// starts with log retention on, matching the pre-existing default.
func newLogDisabledTestToken(t *testing.T, logDisabled bool) *Token {
	t.Helper()
	user := &User{Username: "log-disabled-test", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
	require.NoError(t, DB.Create(user).Error)
	token := &Token{
		UserId:      user.Id,
		Name:        "log-disabled-test",
		Key:         "sk-log-disabled-test",
		Status:      common.TokenStatusEnabled,
		LogDisabled: logDisabled,
	}
	require.NoError(t, DB.Create(token).Error)
	return token
}

func newConsumeLogTestContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	return c
}

func countConsumeLogs(t *testing.T, tokenId int) int64 {
	t.Helper()
	var count int64
	require.NoError(t, LOG_DB.Model(&Log{}).Where("token_id = ? AND type = ?", tokenId, LogTypeConsume).Count(&count).Error)
	return count
}

func TestTokenLogDisabledSkipsConsumeLog(t *testing.T) {
	truncateTables(t)
	token := newLogDisabledTestToken(t, true)
	c := newConsumeLogTestContext(t)

	RecordConsumeLog(c, token.UserId, RecordConsumeLogParams{
		TokenId: token.Id, TokenName: token.Name, ModelName: "gpt-4o",
		PromptTokens: 10, CompletionTokens: 5, Quota: 100,
		Content: []*common.Message{common.NewMessage("test consume")},
		Other:   NewLogOther(),
	})
	assert.Equal(t, int64(0), countConsumeLogs(t, token.Id), "log-disabled token must not leave a consume log row")

	// Flipping the flag off restores the previous behaviour: the same call
	// writes one row with the billed quantities.
	token.LogDisabled = false
	require.NoError(t, token.Update())
	RecordConsumeLog(c, token.UserId, RecordConsumeLogParams{
		TokenId: token.Id, TokenName: token.Name, ModelName: "gpt-4o",
		PromptTokens: 10, CompletionTokens: 5, Quota: 100,
		Content: []*common.Message{common.NewMessage("test consume")},
		Other:   NewLogOther(),
	})
	require.Equal(t, int64(1), countConsumeLogs(t, token.Id))
	var log Log
	require.NoError(t, LOG_DB.Where("token_id = ?", token.Id).First(&log).Error)
	assert.Equal(t, 100, log.Quota)
	assert.Equal(t, 10, log.PromptTokens)
	assert.Equal(t, 5, log.CompletionTokens)
}

func TestTokenLogDisabledSkipsTaskBillingLog(t *testing.T) {
	truncateTables(t)
	token := newLogDisabledTestToken(t, true)

	RecordTaskBillingLog(RecordTaskBillingLogParams{
		UserId: token.UserId, LogType: LogTypeConsume, ChannelId: 1,
		ModelName: "gpt-4o", Quota: 50, TokenId: token.Id,
		Content: []*common.Message{common.NewMessage("test task consume")},
		Other:   NewLogOther(),
	})
	assert.Equal(t, int64(0), countConsumeLogs(t, token.Id))

	token.LogDisabled = false
	require.NoError(t, token.Update())
	RecordTaskBillingLog(RecordTaskBillingLogParams{
		UserId: token.UserId, LogType: LogTypeConsume, ChannelId: 1,
		ModelName: "gpt-4o", Quota: 50, TokenId: token.Id,
		Content: []*common.Message{common.NewMessage("test task consume")},
		Other:   NewLogOther(),
	})
	assert.Equal(t, int64(1), countConsumeLogs(t, token.Id))
}

func TestTokenLogDisabledLeavesQuotaUntouched(t *testing.T) {
	truncateTables(t)
	token := newLogDisabledTestToken(t, true)
	token.RemainQuota = 1000
	token.UsedQuota = 100
	require.NoError(t, DB.Model(token).Select("remain_quota", "used_quota").Updates(token).Error)
	c := newConsumeLogTestContext(t)

	// The log writer itself must never move quota: in production the charge
	// is settled (SettleBilling) before RecordConsumeLog is called, so
	// skipping the row cannot skip the charge. This test pins the writer's
	// side of that split — no log row, and no quota movement from the call.
	RecordConsumeLog(c, token.UserId, RecordConsumeLogParams{
		TokenId: token.Id, TokenName: token.Name, ModelName: "gpt-4o",
		PromptTokens: 10, CompletionTokens: 5, Quota: 100,
		Content: []*common.Message{common.NewMessage("test consume")},
		Other:   NewLogOther(),
	})
	assert.Equal(t, int64(0), countConsumeLogs(t, token.Id))

	stored, err := GetTokenById(token.Id)
	require.NoError(t, err)
	assert.Equal(t, 1000, stored.RemainQuota)
	assert.Equal(t, 100, stored.UsedQuota)
}

func TestTokenLogDisabledFlag(t *testing.T) {
	truncateTables(t)

	assert.False(t, tokenLogDisabled(0), "no token id keeps logging")
	assert.False(t, tokenLogDisabled(999999), "an unknown token keeps logging")

	token := newLogDisabledTestToken(t, false)
	assert.False(t, tokenLogDisabled(token.Id))

	token.LogDisabled = true
	require.NoError(t, token.Update())
	stored, err := GetTokenById(token.Id)
	require.NoError(t, err)
	require.True(t, stored.LogDisabled, "the flag must survive a round trip through Update()")
	assert.True(t, tokenLogDisabled(token.Id))
}
