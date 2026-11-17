package ownermem_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ownermem"
)

// fakeDecide 是假 Decisions 服务：按候选文本给分，记录请求。
type fakeDecide struct {
	srv *httptest.Server

	mu     sync.Mutex
	status int
	delay  time.Duration
	scores map[string]float64 // 候选文本 -> 期望分（缺省 0）
	keys   []string
	bodies []map[string]any
}

func newFakeDecide(t *testing.T) *fakeDecide {
	t.Helper()
	f := &fakeDecide{status: http.StatusOK, scores: map[string]float64{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		status, delay := f.status, f.delay
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.keys = append(f.keys, r.Header.Get("x-goog-api-key"))
		f.bodies = append(f.bodies, body)
		answers := map[string]any{}
		if r.URL.Path == "/v1beta/decisions" {
			for id, q := range body["questions"].(map[string]any) {
				instr := q.(map[string]any)["instructions"].(string)
				fact := strings.TrimPrefix(strings.SplitN(instr, "\n", 2)[0], "候选事实：")
				answers[id] = map[string]any{"type": "score", "score": f.scores[fact]}
			}
		} else {
			status = http.StatusNotFound
		}
		f.mu.Unlock()
		time.Sleep(delay)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func decideEnv(t *testing.T) (*env, *fakeDecide) {
	t.Helper()
	e := newEnv(t, true, func(c *ownermem.Config) { c.HTTP = &http.Client{Timeout: 300 * time.Millisecond} })
	d := newFakeDecide(t)
	key := "AQ.decide"
	_, err := e.svc.PutDecideEndpoint(context.Background(), apigen.OwnerMemoryDecideUpdate{BaseURL: d.srv.URL, Model: "gemini-decision-1", APIKey: &key})
	require.NoError(t, err)
	return e, d
}

// TestDecideRequestAndTiers:一次请求、每候选一个 score 题；按分数降序分档：≥2.0 正常、1.5–2.0 标注、<1.5 丢弃。
func TestDecideRequestAndTiers(t *testing.T) {
	e, d := decideEnv(t)
	d.scores = map[string]float64{"甲": 2.9, "乙": 1.6, "丙": 0.4, "丁": 2.1}
	res := e.svc.RerankTexts(context.Background(), "主人住在哪", []string{"甲", "乙", "丙", "丁"})
	require.NoError(t, res.Err)
	assert.True(t, res.Reranked)
	assert.False(t, res.NoDirect)
	var got []string
	for _, it := range res.Items {
		got = append(got, it.Fact.Text)
	}
	assert.Equal(t, []string{"甲", "丁", "乙"}, got)
	assert.False(t, res.Items[1].Partial)
	assert.True(t, res.Items[2].Partial)

	require.Len(t, d.bodies, 1, "一次请求打分全部候选")
	assert.Equal(t, "AQ.decide", d.keys[0])
	body := d.bodies[0]
	assert.Equal(t, "models/gemini-decision-1", body["model"])
	assert.Contains(t, body["input"], "主人的问题：主人住在哪")
	assert.Contains(t, body["input"], "3. 直接可用")
	qs := body["questions"].(map[string]any)
	require.Len(t, qs, 4)
	q0 := qs["f0"].(map[string]any)
	assert.Equal(t, "score", q0["type"])
	assert.Equal(t, []any{"无关", "不能用", "部分有用", "直接可用"}, q0["criteria"])
	assert.True(t, strings.HasPrefix(q0["instructions"].(string), "候选事实：甲\n"))
}

// TestDecideNoDirect:没有一条 ≥2.0 时标出「没有能直接回答的条目」，最多给 20 条。
func TestDecideNoDirect(t *testing.T) {
	e, d := decideEnv(t)
	var texts []string
	for i := range 60 {
		s := strings.Repeat("x", i+1)
		texts = append(texts, s)
		d.scores[s] = 1.7
	}
	res := e.svc.RerankTexts(context.Background(), "q", texts)
	require.NoError(t, res.Err)
	assert.True(t, res.NoDirect)
	assert.Len(t, res.Items, 20)
	assert.Len(t, d.bodies[0]["questions"], 50, "只送前 50 条打分")
	assert.Equal(t, "x", res.Items[0].Fact.Text, "同分保持 RRF 原序")
}

// TestDecideFallback:429、超时、未配置时按 RRF 顺序给前 20 条，标为未经重排。
func TestDecideFallback(t *testing.T) {
	ctx := context.Background()
	var texts []string
	for i := range 25 {
		texts = append(texts, strings.Repeat("y", i+1))
	}
	check := func(svc *ownermem.Service) {
		t.Helper()
		res := svc.RerankTexts(ctx, "q", texts)
		assert.False(t, res.Reranked)
		require.Error(t, res.Err)
		require.Len(t, res.Items, 20)
		assert.Equal(t, "y", res.Items[0].Fact.Text)
		assert.Equal(t, "yy", res.Items[1].Fact.Text)
	}

	e, d := decideEnv(t)
	d.mu.Lock()
	d.status = http.StatusTooManyRequests
	d.mu.Unlock()
	check(e.svc)

	d.mu.Lock()
	d.status, d.delay = http.StatusOK, time.Second
	d.mu.Unlock()
	check(e.svc)

	check(newEnv(t, true).svc)
}
