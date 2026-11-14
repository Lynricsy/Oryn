package api_test

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// sec F1：两行 input = 5e18、cost = 1.7e308 落在同一分组时，统计接口回 200 且响应体是合法 JSON（原来 SUM 抛
// integer overflow 回 500，或成本 +Inf 让编码失败、200 加空体）。
func TestStatsOverflowRowsAPI(t *testing.T) {
	e := newStatsEnv(t, true)
	for i := range 2 {
		e.seq++
		_, err := e.st.Q.InsertUsageRecord(e.ctx, db.InsertUsageRecordParams{ThreadID: new("T-1"), EntrySeq: e.seq,
			Ts: statsD + dayMillis + int64(i), Provider: "p", Model: "m", InputTokens: 5e18, OutputTokens: 5e18, CostUsd: 1.7e308})
		require.NoError(t, err)
	}
	for _, path := range []string{
		"/api/v1/stats/usage?groupBy=model&" + win(""),
		"/api/v1/stats/usage?groupBy=day&" + win(""),
		"/api/v1/stats/usage?groupBy=thread&series=hour&" + win(""),
		"/api/v1/stats/tools?" + win(""),
		"/api/v1/stats/budget",
	} {
		code, _, raw := e.get(path)
		require.Equal(t, http.StatusOK, code, "%s: %s", path, raw)
		require.NotEmpty(t, raw, path)
		require.True(t, json.Valid(raw), path)
	}
	var u apigen.UsageStats
	e.json("/api/v1/stats/usage?groupBy=model&"+win(""), &u)
	assert.Positive(t, u.Total.InputTokens)
	var b apigen.BudgetStatus
	e.json("/api/v1/stats/budget", &b)
	assert.False(t, math.IsInf(b.Day.SpentUsd, 0))
}

// WriteJSON 先编码再写头：值不可编码时回 500 与 JSON 错误体，而不是 200 加空体。
func TestWriteJSONEncodeFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	api.WriteJSON(rec, http.StatusOK, map[string]float64{"cost": math.Inf(1)})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var body struct{ Code, Message string }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	assert.Equal(t, "internal", body.Code)

	rec = httptest.NewRecorder()
	api.WriteJSON(rec, http.StatusCreated, map[string]int{"n": 1})
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.JSONEq(t, `{"n":1}`, rec.Body.String())
}
