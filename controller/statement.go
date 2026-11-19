package controller

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// parseStatementWindow resolves the statement window from the query: a
// month ("YYYY-MM") or an explicit start/end timestamp pair. With neither,
// the current month is used. A month always spans whole days in server
// local time; end_timestamp is inclusive, matching the log search.
func parseStatementWindow(c *gin.Context) (month string, startTimestamp int64, endTimestamp int64, ok bool) {
	month = c.Query("month")
	if month != "" {
		parsed, err := time.Parse("2006-01", month)
		if err != nil {
			common.ApiErrorMsg(c, "invalid month, expected YYYY-MM")
			return "", 0, 0, false
		}
		start := time.Date(parsed.Year(), parsed.Month(), 1, 0, 0, 0, 0, time.Local)
		startTimestamp = start.Unix()
		endTimestamp = start.AddDate(0, 1, 0).Add(-time.Second).Unix()
		return month, startTimestamp, endTimestamp, true
	}
	startTimestamp, _ = strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ = strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	if startTimestamp == 0 && endTimestamp == 0 {
		now := time.Now()
		month = now.Format("2006-01")
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
		startTimestamp = start.Unix()
		endTimestamp = start.AddDate(0, 1, 0).Add(-time.Second).Unix()
		return month, startTimestamp, endTimestamp, true
	}
	if startTimestamp <= 0 || endTimestamp <= 0 || endTimestamp < startTimestamp {
		common.ApiErrorMsg(c, "invalid time range")
		return "", 0, 0, false
	}
	return "", startTimestamp, endTimestamp, true
}

// statementResponse is the JSON shape shared by the statement endpoints:
// the window actually queried plus the model/channel breakdown and total.
func statementResponse(c *gin.Context, month string, startTimestamp int64, endTimestamp int64, userId int, statement model.Statement) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"month":           month,
			"start_timestamp": startTimestamp,
			"end_timestamp":   endTimestamp,
			"user_id":         userId,
			"by_model":        statement.ByModel,
			"by_channel":      statement.ByChannel,
			"total":           statement.Total,
		},
	})
}

// GetLogStatement returns the billing statement (对账单) for any user.
// Administrators pass user_id to scope to one customer; without it the
// statement covers all users. The numbers come only from the consume logs.
func GetLogStatement(c *gin.Context) {
	month, startTimestamp, endTimestamp, ok := parseStatementWindow(c)
	if !ok {
		return
	}
	userId, _ := strconv.Atoi(c.Query("user_id"))
	statement, err := model.GetStatement(userId, startTimestamp, endTimestamp)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	statementResponse(c, month, startTimestamp, endTimestamp, userId, statement)
}

// GetUserLogStatement is the same statement scoped to the caller's own
// account. A user_id query parameter, if given, is ignored: a regular user
// can never see another customer's numbers.
func GetUserLogStatement(c *gin.Context) {
	month, startTimestamp, endTimestamp, ok := parseStatementWindow(c)
	if !ok {
		return
	}
	userId := c.GetInt("id")
	if c.GetInt("role") < common.RoleAdminUser {
		var err error
		if startTimestamp, err = limitLogTimeRange(startTimestamp, endTimestamp); err != nil {
			common.ApiError(c, err)
			return
		}
	}
	statement, err := model.GetStatement(userId, startTimestamp, endTimestamp)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	statementResponse(c, month, startTimestamp, endTimestamp, userId, statement)
}

// writeLogStatementCSV streams the statement as CSV with a UTF-8 BOM (so
// Excel opens it directly): one row per model, one per channel, then a
// TOTAL row. Amounts are not included: quota is the platform billing unit
// and the console already converts it; the CSV stays a usage record.
func writeLogStatementCSV(c *gin.Context, statement model.Statement) {
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"statement-%s.csv\"", time.Now().Format("20060102-150405")))
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(c.Writer)
	_ = writer.Write([]string{"group_type", "model_name", "channel_id", "channel_name", "requests", "prompt_tokens", "completion_tokens", "token_used", "quota"})
	writeRow := func(groupType string, row model.StatementGroup) {
		_ = writer.Write([]string{
			groupType,
			row.ModelName,
			strconv.FormatInt(int64(row.ChannelId), 10),
			row.ChannelName,
			strconv.FormatInt(row.Requests, 10),
			strconv.FormatInt(row.PromptTokens, 10),
			strconv.FormatInt(row.CompletionTokens, 10),
			strconv.FormatInt(row.TokenUsed, 10),
			strconv.FormatInt(row.Quota, 10),
		})
	}
	for _, row := range statement.ByModel {
		writeRow("model", row)
	}
	for _, row := range statement.ByChannel {
		writeRow("channel", row)
	}
	writeRow("total", statement.Total)
	writer.Flush()
}

// ExportLogStatement downloads the admin statement as CSV.
func ExportLogStatement(c *gin.Context) {
	_, startTimestamp, endTimestamp, ok := parseStatementWindow(c)
	if !ok {
		return
	}
	userId, _ := strconv.Atoi(c.Query("user_id"))
	statement, err := model.GetStatement(userId, startTimestamp, endTimestamp)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	writeLogStatementCSV(c, statement)
}

// ExportUserLogStatement downloads the caller's own statement as CSV.
func ExportUserLogStatement(c *gin.Context) {
	_, startTimestamp, endTimestamp, ok := parseStatementWindow(c)
	if !ok {
		return
	}
	userId := c.GetInt("id")
	if c.GetInt("role") < common.RoleAdminUser {
		var err error
		if startTimestamp, err = limitLogTimeRange(startTimestamp, endTimestamp); err != nil {
			common.ApiError(c, err)
			return
		}
	}
	statement, err := model.GetStatement(userId, startTimestamp, endTimestamp)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	writeLogStatementCSV(c, statement)
}
