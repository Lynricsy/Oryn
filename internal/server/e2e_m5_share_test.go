package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
)

// M5 步骤 2 验证：公开分享全链路——process 驱动 + 真实 orbd + fakeomp，匿名请求不带任何凭据。

type publicEntry struct {
	Seq     int64           `json:"seq"`
	File    string          `json:"file"`
	EntryID string          `json:"entryId"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// anonGet 不带凭据地 GET，返回状态码、头与响应体。
func (s *stack) anonGet(path string) (int, http.Header, []byte) {
	s.t.Helper()
	resp, err := http.Get(s.http.URL + path)
	require.NoError(s.t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(s.t, err)
	return resp.StatusCode, resp.Header, b
}

// publicEntries 以匿名分页读完分享范围内的全部条目。
func (s *stack) publicEntries(slug string) []publicEntry {
	s.t.Helper()
	var out []publicEntry
	after := int64(0)
	for {
		code, hdr, body := s.anonGet("/api/v1/public/shares/" + slug + "/entries?limit=50&after=" + strconv.FormatInt(after, 10))
		require.Equal(s.t, http.StatusOK, code, string(body))
		assert.Equal(s.t, "noindex, nofollow", hdr.Get("X-Robots-Tag"))
		assert.Equal(s.t, "no-store", hdr.Get("Cache-Control"))
		var page struct {
			Entries []publicEntry `json:"entries"`
			HasMore bool          `json:"hasMore"`
			LastSeq int64         `json:"lastSeq"`
		}
		require.NoError(s.t, json.Unmarshal(body, &page))
		for _, e := range page.Entries {
			require.LessOrEqual(s.t, e.Seq, page.LastSeq)
			after = e.Seq
		}
		out = append(out, page.Entries...)
		if !page.HasMore {
			return out
		}
	}
}

func roleCounts(es []publicEntry) map[string]int {
	out := map[string]int{}
	for _, e := range es {
		if e.Type != "message" {
			continue
		}
		var p struct {
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		_ = json.Unmarshal(e.Payload, &p)
		out[p.Message.Role]++
	}
	return out
}

type shareView struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Mode     string `json:"mode"`
	URL      string `json:"url"`
	UntilSeq *int64 `json:"untilSeq"`
}

func TestE2EM5Share(t *testing.T) {
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})
	a := s.dial()
	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{
		"prompt":   "hello from m5",
		"executor": map[string]any{"kind": "new_orb", "provider": "local", "type": "small"},
	}, &created, 201)
	th := created.Thread
	a.send(map[string]any{"type": "sub", "threadId": th.ID, "afterSeq": 0})
	final := s.waitThread(th.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" })
	require.NotEmpty(t, final.OrbID)
	// 等最后几行入库（agent_end 之后的 assistant 条目）
	require.Eventually(t, func() bool {
		var page struct {
			Entries []publicEntry `json:"entries"`
		}
		s.rest("GET", "/api/v1/threads/"+th.ID+"/entries?limit=500", nil, &page, 200)
		r := roleCounts(page.Entries)
		return r["user"] >= 1 && r["assistant"] >= 1
	}, 20*time.Second, 50*time.Millisecond)

	// 密钥扫描：密钥库里的值出现在转录里 → 409 secrets_found（预览已脱敏）；确认后创建成功
	s.rest("PUT", "/api/v1/secrets/E2E_PHRASE", map[string]any{"value": "hello from m5", "mode": "env"}, nil, 200)
	var conflict struct {
		Code string `json:"code"`
		Scan struct {
			Findings []struct {
				Rule       string `json:"rule"`
				SecretName string `json:"secretName"`
				Role       string `json:"role"`
				Preview    string `json:"preview"`
			} `json:"findings"`
		} `json:"scan"`
	}
	s.rest("POST", "/api/v1/threads/"+th.ID+"/shares", map[string]any{"mode": "snapshot"}, &conflict, 409)
	assert.Equal(t, "secrets_found", conflict.Code)
	require.NotEmpty(t, conflict.Scan.Findings)
	assert.Equal(t, "vault_secret", conflict.Scan.Findings[0].Rule)
	assert.Equal(t, "E2E_PHRASE", conflict.Scan.Findings[0].SecretName)
	assert.Equal(t, "user", conflict.Scan.Findings[0].Role)
	assert.Equal(t, "hell…m5", conflict.Scan.Findings[0].Preview)

	var snap, live shareView
	s.rest("POST", "/api/v1/threads/"+th.ID+"/shares", map[string]any{"mode": "snapshot", "acknowledgeFindings": true}, &snap, 201)
	s.rest("POST", "/api/v1/threads/"+th.ID+"/shares", map[string]any{"mode": "live", "acknowledgeFindings": true}, &live, 201)
	require.NotNil(t, snap.UntilSeq)
	assert.Equal(t, "http://localhost/s/"+snap.Slug, snap.URL)
	var list struct {
		Shares []shareView `json:"shares"`
	}
	s.rest("GET", "/api/v1/threads/"+th.ID+"/shares", nil, &list, 200)
	assert.Len(t, list.Shares, 2)
	var tv struct {
		PublicShares int `json:"publicShares"`
	}
	s.rest("GET", "/api/v1/threads/"+th.ID, nil, &tv, 200)
	assert.Equal(t, 2, tv.PublicShares)

	// 匿名读取：元数据与条目，不含线程 id 与 orb id
	code, hdr, body := s.anonGet("/api/v1/public/shares/" + snap.Slug)
	require.Equal(t, http.StatusOK, code, string(body))
	assert.Equal(t, "noindex, nofollow", hdr.Get("X-Robots-Tag"))
	assert.NotContains(t, string(body), th.ID)
	assert.NotContains(t, string(body), final.OrbID)
	snapEntries := s.publicEntries(snap.Slug)
	r := roleCounts(snapEntries)
	assert.GreaterOrEqual(t, r["user"], 1)
	assert.GreaterOrEqual(t, r["assistant"], 1)
	for _, e := range snapEntries {
		assert.NotContains(t, e.File, "/")
		assert.LessOrEqual(t, e.Seq, *snap.UntilSeq)
	}
	code, hdr, _ = s.anonGet("/s/" + snap.Slug)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "noindex, nofollow", hdr.Get("X-Robots-Tag"))
	code, hdr, body = s.anonGet("/s/" + snap.Slug + ".md")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "text/markdown; charset=utf-8", hdr.Get("Content-Type"))
	assert.Contains(t, string(body), "## User")
	assert.Contains(t, string(body), "hello from m5")

	// 快照分享没有 WS
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsBase := strings.Replace(s.http.URL, "http", "ws", 1) + "/api/v1/public/shares/"
	_, resp, err := websocket.Dial(ctx, wsBase+snap.Slug+"/ws", nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// 实时 WS：匿名连接，补发到快照时刻后转实时
	pw, _, err := websocket.Dial(ctx, wsBase+live.Slug+"/ws?after=0", nil)
	require.NoError(t, err)
	pw.SetReadLimit(64 << 20)
	t.Cleanup(func() { _ = pw.CloseNow() })
	type pubMsg struct {
		Type    string          `json:"type"`
		LastSeq int64           `json:"lastSeq"`
		Frame   json.RawMessage `json:"frame"`
		Entries []publicEntry   `json:"entries"`
		raw     string
	}
	pch := make(chan pubMsg, 4096)
	pclosed := make(chan error, 1)
	go func() {
		defer close(pch)
		for {
			_, b, err := pw.Read(context.Background())
			if err != nil {
				pclosed <- err
				return
			}
			var m pubMsg
			if json.Unmarshal(b, &m) == nil {
				m.raw = string(b)
				pch <- m
			}
		}
	}()
	var seen []pubMsg
	until := func(timeout time.Duration, fn func(pubMsg) bool) {
		t.Helper()
		deadline := time.After(timeout)
		for {
			select {
			case m, ok := <-pch:
				require.True(t, ok, "匿名 WS 被关闭")
				seen = append(seen, m)
				if fn(m) {
					return
				}
			case <-deadline:
				t.Fatal("等待匿名 WS 消息超时")
			}
		}
	}
	var pubSnapSeq int64
	until(10*time.Second, func(m pubMsg) bool {
		if m.Type == "share.snapshot" {
			pubSnapSeq = m.LastSeq
			return true
		}
		return false
	})
	until(10*time.Second, func(m pubMsg) bool { return m.Type == "entries" && m.LastSeq == pubSnapSeq })

	// 再发一条消息：快照不变，实时收到新条目与流式帧
	a.send(map[string]any{"type": "cmd", "threadId": th.ID, "id": "c2", "frame": map[string]any{"type": "prompt", "message": "second round PUB-MARK"}})
	var sawUpdate, sawNew bool
	until(60*time.Second, func(m pubMsg) bool {
		if m.Type == "frame" && strings.Contains(string(m.Frame), `"message_update"`) {
			sawUpdate = true
		}
		if m.Type == "entries" {
			for _, e := range m.Entries {
				if strings.Contains(string(e.Payload), "PUB-MARK") {
					sawNew = true
				}
			}
		}
		return sawUpdate && sawNew && m.Type == "frame" && strings.Contains(string(m.Frame), `"agent_end"`)
	})
	s.waitThread(th.ID, 30*time.Second, func(v threadView) bool { return v.State == "idle" })
	after := s.publicEntries(snap.Slug)
	assert.Equal(t, len(snapEntries), len(after), "快照页条目数不变")
	for _, e := range after {
		assert.NotContains(t, string(e.Payload), "PUB-MARK")
	}
	assert.Contains(t, func() string {
		var b strings.Builder
		for _, e := range s.publicEntries(live.Slug) {
			b.Write(e.Payload)
		}
		return b.String()
	}(), "PUB-MARK")

	// 撤销：REST / 页面 / .md 立即 404，匿名 WS 收到 share.revoked 后断开
	s.rest("DELETE", "/api/v1/shares/"+live.ID, nil, nil, 204)
	s.rest("DELETE", "/api/v1/shares/"+snap.ID, nil, nil, 204)
	select {
	case err := <-pclosed:
		assert.Equal(t, websocket.StatusCode(4001), websocket.CloseStatus(err))
	case <-time.After(10 * time.Second):
		t.Fatal("撤销后匿名 WS 未断开")
	}
	for m := range pch {
		seen = append(seen, m)
	}
	require.NotEmpty(t, seen)
	assert.Equal(t, "share.revoked", seen[len(seen)-1].Type)
	allowed := map[string]bool{"share.snapshot": true, "entries": true, "frame": true, "live.reset": true, "transcript.reset": true, "share.state": true, "share.revoked": true}
	for _, m := range seen {
		assert.True(t, allowed[m.Type], "匿名连接收到了 %s", m.Type)
		assert.NotContains(t, m.raw, `"threadId"`)
		assert.NotContains(t, m.raw, "extension_ui_request")
	}
	for _, p := range []string{
		"/api/v1/public/shares/" + live.Slug, "/api/v1/public/shares/" + snap.Slug + "/entries",
		"/s/" + snap.Slug, "/s/" + snap.Slug + ".md",
	} {
		code, _, _ := s.anonGet(p)
		assert.Equal(t, http.StatusNotFound, code, p)
	}
	tv.PublicShares = 0 // 为 0 时字段省略：解码前先清零
	s.rest("GET", "/api/v1/threads/"+th.ID, nil, &tv, 200)
	assert.Zero(t, tv.PublicShares)

	// 免鉴权前缀只覆盖 /api/v1/public/：其它接口仍要凭据
	code, _, body = s.anonGet("/api/v1/public/shares/x")
	assert.Equal(t, http.StatusNotFound, code, string(body))
	code, _, _ = s.anonGet("/api/v1/threads")
	assert.Equal(t, http.StatusUnauthorized, code)
	code, _, _ = s.anonGet("/api/v1/shares")
	assert.Equal(t, http.StatusUnauthorized, code)
}
