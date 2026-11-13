package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
)

type hookEvent struct {
	Event  string `json:"event"`
	ID     string `json:"id"`
	Thread struct {
		ID    string `json:"id"`
		State string `json:"state"`
		URL   string `json:"url"`
	} `json:"thread"`
	sigOK bool
}

// nextHook 等待下一条 Webhook 请求。
func nextHook(t *testing.T, ch <-chan hookEvent, timeout time.Duration) hookEvent {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(timeout):
		t.Fatal("等待 Webhook 超时")
		return hookEvent{}
	}
}

// M5 步骤 3 端到端：process 驱动 + 真 orbd + fakeomp，Webhook 指向测试内的 httptest。
func TestE2EM5Notify(t *testing.T) {
	prevDone, prevInput := notifyDoneDelay, notifyInputDelay
	notifyDoneDelay, notifyInputDelay = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { notifyDoneDelay, notifyInputDelay = prevDone, prevInput })

	const secret = "e2e-hook-secret"
	hooks := make(chan hookEvent, 64)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var e hookEvent
		_ = json.Unmarshal(b, &e)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(b)
		e.sigOK = r.Header.Get("X-Oryn-Signature") == "sha256="+hex.EncodeToString(mac.Sum(nil))
		hooks <- e
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()

	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})
	s.rest("POST", "/api/v1/notification-channels", map[string]any{
		"kind": "webhook", "name": "e2e", "webhook": map[string]any{"url": hook.URL + "/in", "secret": secret},
	}, nil, 201)

	// 1. 没有任何 WS 连接时建线程 → 回合结束后收到 thread.done
	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "hello", "executor": map[string]any{"kind": "new_orb", "provider": "local", "type": "small"}}, &created, 201)
	t1 := created.Thread.ID
	e := nextHook(t, hooks, 90*time.Second)
	assert.Equal(t, "thread.done", e.Event)
	assert.Equal(t, t1, e.Thread.ID)
	assert.Equal(t, "http://localhost/t/"+t1, e.Thread.URL)
	assert.True(t, e.sigOK, "X-Oryn-Signature 用 secret 重算一致")

	// 2. 提示词含 ask → thread.input；经 WS ui.response 作答后线程完成
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "please ask me", "executor": map[string]any{"kind": "orb", "orbId": created.Thread.OrbID}}, &created, 201)
	t2 := created.Thread.ID
	e = nextHook(t, hooks, 60*time.Second)
	assert.Equal(t, "thread.input", e.Event)
	assert.Equal(t, t2, e.Thread.ID)
	assert.Equal(t, "awaiting_input", e.Thread.State)

	a := s.dial()
	a.send(map[string]any{"type": "sub", "threadId": t2, "afterSeq": 0})
	var reqID string
	a.until(30*time.Second, func(m wsMsg) bool {
		frames := m.Agent.PendingUI
		if m.Type == "frame" && m.ThreadID == t2 && m.frameType() == "extension_ui_request" {
			frames = append(frames, m.Frame)
		}
		for _, f := range frames {
			var r struct{ ID, Method string }
			if json.Unmarshal(f, &r) == nil && r.Method == "select" {
				reqID = r.ID
				return true
			}
		}
		return false
	})
	a.send(map[string]any{"type": "ui.response", "threadId": t2, "frame": map[string]any{"type": "extension_ui_response", "id": reqID, "value": "yes"}})
	s.waitThread(t2, 60*time.Second, func(v threadView) bool { return v.State == "idle" })
	e = nextHook(t, hooks, 30*time.Second)
	assert.Equal(t, "thread.done", e.Event, "作答后回合完成（无人 focus）")
	assert.Equal(t, t2, e.Thread.ID)

	// 3. WS 连接 focus 该线程期间再跑一轮 → 不通知；取消 focus 后再跑一轮 → 通知
	round := func(msg string) {
		a.send(map[string]any{"type": "cmd", "threadId": t1, "id": msg, "frame": map[string]any{"type": "prompt", "message": msg}})
		sawWorking := false
		a.until(60*time.Second, func(m wsMsg) bool {
			v, ok := m.upserted(t1)
			if !ok {
				return false
			}
			sawWorking = sawWorking || v.State == "working"
			return sawWorking && v.State == "idle"
		})
	}
	a.send(map[string]any{"type": "focus", "threadId": t1})
	round("focused round")
	a.send(map[string]any{"type": "focus", "threadId": nil})
	round("unfocused round")
	e = nextHook(t, hooks, 30*time.Second)
	assert.Equal(t, "thread.done", e.Event)
	assert.Equal(t, t1, e.Thread.ID)
	select {
	case extra := <-hooks:
		t.Fatalf("focus 期间的回合不应通知，多收到 %+v", extra)
	default:
	}

	// 4. GET notifications 列出它们，全部投递 sent
	var list struct {
		Notifications []struct {
			Kind       string `json:"kind"`
			ThreadID   string `json:"threadId"`
			Deliveries []struct {
				Target string `json:"target"`
				Status string `json:"status"`
			} `json:"deliveries"`
		} `json:"notifications"`
	}
	require.Eventually(t, func() bool {
		s.rest("GET", "/api/v1/notifications", nil, &list, 200)
		for _, n := range list.Notifications {
			if len(n.Deliveries) != 1 || n.Deliveries[0].Status != "sent" {
				return false
			}
		}
		return len(list.Notifications) == 4
	}, 10*time.Second, 50*time.Millisecond)
	var kinds []string
	for _, n := range list.Notifications {
		kinds = append(kinds, n.Kind+":"+map[string]string{t1: "t1", t2: "t2"}[n.ThreadID])
		assert.Equal(t, "webhook", n.Deliveries[0].Target)
	}
	assert.Equal(t, []string{"done:t1", "done:t2", "input:t2", "done:t1"}, kinds)
}
