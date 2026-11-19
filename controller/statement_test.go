package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupStatementTestData creates two users, two channels and a fixed set of
// consume logs inside November 2026 (plus two rows that must be excluded:
// one outside the month, one top-up log). Expected numbers for user A in
// 2026-11: gpt-4o 2 requests / 300 prompt / 150 completion / quota 3000,
// claude-sonnet-4-5 1 request / 10 prompt / 10 completion / quota 500,
// total 3 requests / 310 prompt / 160 completion / quota 3500. User B has
// one gpt-4o row: 1 request / 1000 prompt / 1000 completion / quota 9999.
func setupStatementTestData(t *testing.T) (userAId int, userBId int, channel1Id int, channel2Id int) {
	t.Helper()
	setupChannelCreateTest(t)
	userA := &model.User{Username: "statement-a", AffCode: "stmt-a", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(userA).Error)
	userB := &model.User{Username: "statement-b", AffCode: "stmt-b", Role: common.RoleCommonUser, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(userB).Error)
	channel1 := &model.Channel{Type: constant.ChannelTypeOpenAI, Key: "sk-test-1", Name: "chan-one", Status: common.ChannelStatusEnabled, Models: "gpt-4o", Group: "default"}
	require.NoError(t, model.DB.Create(channel1).Error)
	channel2 := &model.Channel{Type: constant.ChannelTypeOpenAI, Key: "sk-test-2", Name: "chan-two", Status: common.ChannelStatusEnabled, Models: "claude-sonnet-4-5", Group: "default"}
	require.NoError(t, model.DB.Create(channel2).Error)

	nov10 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local).Unix()
	nov20 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.Local).Unix()
	oct15 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local).Unix()
	addLog := func(userId int, username string, logType int, modelName string, channelId int, prompt, completion, quota int, createdAt int64) {
		require.NoError(t, model.LOG_DB.Create(&model.Log{
			UserId: userId, Username: username, CreatedAt: createdAt, Type: logType,
			ModelName: modelName, ChannelId: channelId, Group: "default",
			PromptTokens: prompt, CompletionTokens: completion, Quota: quota,
		}).Error)
	}
	addLog(userA.Id, "statement-a", model.LogTypeConsume, "gpt-4o", channel1.Id, 100, 50, 1000, nov10)
	addLog(userA.Id, "statement-a", model.LogTypeConsume, "gpt-4o", channel1.Id, 200, 100, 2000, nov20)
	addLog(userA.Id, "statement-a", model.LogTypeConsume, "claude-sonnet-4-5", channel2.Id, 10, 10, 500, nov10)
	addLog(userA.Id, "statement-a", model.LogTypeConsume, "gpt-4o", channel1.Id, 999, 999, 9999, oct15)
	addLog(userA.Id, "statement-a", model.LogTypeTopup, "gpt-4o", channel1.Id, 0, 0, 7777, nov10)
	addLog(userB.Id, "statement-b", model.LogTypeConsume, "gpt-4o", channel1.Id, 1000, 1000, 9999, nov10)
	return userA.Id, userB.Id, channel1.Id, channel2.Id
}

func newStatementHandlerContext(t *testing.T, userId int, role int, url string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, url, nil)
	c.Set("id", userId)
	c.Set("role", role)
	return c, w
}

type statementTestResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		Month     string                 `json:"month"`
		UserId    int                    `json:"user_id"`
		ByModel   []model.StatementGroup `json:"by_model"`
		ByChannel []model.StatementGroup `json:"by_channel"`
		Total     model.StatementGroup   `json:"total"`
	} `json:"data"`
}

func decodeStatementResponse(t *testing.T, w *httptest.ResponseRecorder) statementTestResponse {
	t.Helper()
	var resp statementTestResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

func groupByModelName(t *testing.T, groups []model.StatementGroup) map[string]model.StatementGroup {
	t.Helper()
	out := make(map[string]model.StatementGroup, len(groups))
	for _, group := range groups {
		out[group.ModelName] = group
	}
	return out
}

func TestUserLogStatementOnlyOwnData(t *testing.T) {
	userAId, userBId, _, _ := setupStatementTestData(t)

	c, w := newStatementHandlerContext(t, userAId, common.RoleCommonUser, "/api/log/self/statement?month=2026-11&user_id="+strconv.Itoa(userBId))
	GetUserLogStatement(c)
	resp := decodeStatementResponse(t, w)
	require.True(t, resp.Success, "response: %s", w.Body.String())
	assert.Equal(t, userAId, resp.Data.UserId, "a user_id parameter must never widen a regular user's statement")
	assert.Equal(t, "2026-11", resp.Data.Month)

	byModel := groupByModelName(t, resp.Data.ByModel)
	require.Len(t, byModel, 2, "only user A's models may appear")
	assert.Equal(t, model.StatementGroup{ModelName: "gpt-4o", Requests: 2, PromptTokens: 300, CompletionTokens: 150, TokenUsed: 450, Quota: 3000}, byModel["gpt-4o"])
	assert.Equal(t, model.StatementGroup{ModelName: "claude-sonnet-4-5", Requests: 1, PromptTokens: 10, CompletionTokens: 10, TokenUsed: 20, Quota: 500}, byModel["claude-sonnet-4-5"])

	require.Len(t, resp.Data.ByChannel, 2)
	assert.Equal(t, int64(2), resp.Data.ByChannel[0].Requests, "the highest-quota channel comes first")
	assert.Equal(t, "chan-one", resp.Data.ByChannel[0].ChannelName)
	assert.Equal(t, int64(3000), resp.Data.ByChannel[0].Quota)
	assert.Equal(t, "chan-two", resp.Data.ByChannel[1].ChannelName)
	assert.Equal(t, int64(500), resp.Data.ByChannel[1].Quota)

	assert.Equal(t, model.StatementGroup{Requests: 3, PromptTokens: 310, CompletionTokens: 160, TokenUsed: 470, Quota: 3500}, resp.Data.Total)
}

func TestLogStatementAdminCanScopeToUser(t *testing.T) {
	userAId, userBId, _, _ := setupStatementTestData(t)

	c, w := newStatementHandlerContext(t, userAId, common.RoleAdminUser, "/api/log/statement?month=2026-11&user_id="+strconv.Itoa(userBId))
	GetLogStatement(c)
	resp := decodeStatementResponse(t, w)
	require.True(t, resp.Success, "response: %s", w.Body.String())
	assert.Equal(t, userBId, resp.Data.UserId)
	assert.Equal(t, model.StatementGroup{Requests: 1, PromptTokens: 1000, CompletionTokens: 1000, TokenUsed: 2000, Quota: 9999}, resp.Data.Total)

	c, w = newStatementHandlerContext(t, userAId, common.RoleAdminUser, "/api/log/statement?month=2026-11")
	GetLogStatement(c)
	resp = decodeStatementResponse(t, w)
	require.True(t, resp.Success, "response: %s", w.Body.String())
	assert.Equal(t, 0, resp.Data.UserId, "no user_id means all users")
	assert.Equal(t, model.StatementGroup{Requests: 4, PromptTokens: 1310, CompletionTokens: 1160, TokenUsed: 2470, Quota: 13499}, resp.Data.Total)
}

func TestLogStatementWindowValidation(t *testing.T) {
	userAId, _, _, _ := setupStatementTestData(t)

	c, w := newStatementHandlerContext(t, userAId, common.RoleCommonUser, "/api/log/self/statement?month=2026-13")
	GetUserLogStatement(c)
	assert.False(t, decodeStatementResponse(t, w).Success, "month 13 must be rejected")

	c, w = newStatementHandlerContext(t, userAId, common.RoleCommonUser, "/api/log/self/statement?month=november")
	GetUserLogStatement(c)
	assert.False(t, decodeStatementResponse(t, w).Success, "a non YYYY-MM month must be rejected")

	c, w = newStatementHandlerContext(t, userAId, common.RoleCommonUser, "/api/log/self/statement?start_timestamp=2000000000&end_timestamp=1000000000")
	GetUserLogStatement(c)
	assert.False(t, decodeStatementResponse(t, w).Success, "an inverted range must be rejected")
}

func TestUserLogStatementExplicitRange(t *testing.T) {
	userAId, _, _, _ := setupStatementTestData(t)
	dayStart := time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local).Unix()
	dayEnd := time.Date(2026, 11, 10, 23, 59, 59, 0, time.Local).Unix()
	url := "/api/log/self/statement?start_timestamp=" + strconv.FormatInt(dayStart, 10) + "&end_timestamp=" + strconv.FormatInt(dayEnd, 10)

	c, w := newStatementHandlerContext(t, userAId, common.RoleCommonUser, url)
	GetUserLogStatement(c)
	resp := decodeStatementResponse(t, w)
	require.True(t, resp.Success, "response: %s", w.Body.String())
	assert.Equal(t, "", resp.Data.Month, "an explicit range echoes no month")
	assert.Equal(t, model.StatementGroup{Requests: 2, PromptTokens: 110, CompletionTokens: 60, TokenUsed: 170, Quota: 1500}, resp.Data.Total)
}

func TestUserLogStatementCSVExport(t *testing.T) {
	userAId, _, _, _ := setupStatementTestData(t)

	c, w := newStatementHandlerContext(t, userAId, common.RoleCommonUser, "/api/log/self/statement/export?month=2026-11")
	ExportUserLogStatement(c)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/csv")
	body := w.Body.String()
	assert.True(t, strings.HasPrefix(body, "\xef\xbb\xbf"), "CSV must start with a UTF-8 BOM so Excel opens it")
	assert.Contains(t, body, "group_type,model_name,channel_id,channel_name,requests,prompt_tokens,completion_tokens,token_used,quota")
	assert.Contains(t, body, "model,gpt-4o,")
	assert.Contains(t, body, "chan-one")
	assert.Contains(t, body, "total,,0,,3,310,160,470,3500")
	assert.NotContains(t, body, "9999", "another customer's quota and the out-of-month row must not leak into the export")
}
