package notify

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/webpush/webpushtest"
)

func frame(v map[string]any) json.RawMessage {
	v["type"], v["id"] = "extension_ui_request", "r1"
	b, _ := json.Marshal(v)
	return b
}

// 推送载荷只给能一触作答、且正文完整说明了问题的 input 带按钮。
func TestPushPayloadActions(t *testing.T) {
	approval := "Allow tool: bash\nReason: writes outside cwd\nCommand: rm -rf /tmp/x"
	long := "Allow tool: bash\nCommand: " + strings.Repeat("x", 300)
	type act = map[string]any
	cases := []struct {
		name string
		m    Message
		acts []act
		body string
	}{
		{"两项 select", Message{Request: frame(map[string]any{"method": "select", "title": "选 A 还是 B？", "options": []string{"A", "B"}})},
			[]act{{"i": 0.0, "label": "A"}, {"i": 1.0, "label": "B"}}, "选 A 还是 B？"},
		{"工具审批：正文是完整多行标题（含命令）", Message{Request: frame(map[string]any{"method": "select", "title": approval, "options": []string{"Approve", "Deny"}})},
			[]act{{"i": 0.0, "label": "Approve"}, {"i": 1.0, "label": "Deny"}}, approval},
		{"confirm：带说明，按钮标 yes / no 供客户端本地化", Message{Request: frame(map[string]any{"method": "confirm", "title": "Overwrite?", "message": "a.md exists"})},
			[]act{{"i": 0.0, "label": "Yes", "role": "yes"}, {"i": 1.0, "label": "No", "role": "no"}}, "Overwrite?\na.md exists"},
		{"单题单选 ask", Message{Request: frame(map[string]any{"method": "ask", "questions": []any{map[string]any{"id": "q", "question": "继续？", "options": []any{map[string]any{"label": "是"}, map[string]any{"label": "否"}, map[string]any{"label": "稍后"}}}}})},
			[]act{{"i": 0.0, "label": "是"}, {"i": 1.0, "label": "否"}, {"i": 2.0, "label": "稍后"}}, "继续？"},
		{"四个选项", Message{Body: "摘要", Request: frame(map[string]any{"method": "select", "title": "选", "options": []string{"a", "b", "c", "d"}})}, nil, "摘要"},
		{"一个选项", Message{Body: "摘要", Request: frame(map[string]any{"method": "select", "title": "选", "options": []string{"a"}})}, nil, "摘要"},
		{"选项过长", Message{Body: "摘要", Request: frame(map[string]any{"method": "select", "title": "选", "options": []string{"a", strings.Repeat("长", 25)}})}, nil, "摘要"},
		{"问题会被截断：不给按钮", Message{Body: long, Request: frame(map[string]any{"method": "select", "title": long, "options": []string{"Approve", "Deny"}})}, nil, truncate(long, maxPushBody)},
		{"文字输入", Message{Body: "名字？", Request: frame(map[string]any{"method": "input", "title": "名字？"})}, nil, "名字？"},
		{"密钥输入", Message{Body: "Token", Request: frame(map[string]any{"method": "input", "title": "Token", "secret": true})}, nil, "Token"},
		{"多选 ask", Message{Body: "选", Request: frame(map[string]any{"method": "ask", "questions": []any{map[string]any{"id": "q", "question": "选", "multi": true, "options": []any{map[string]any{"label": "a"}, map[string]any{"label": "b"}}}}})}, nil, "选"},
		{"单字段单选表单", Message{Body: "表单", Request: frame(map[string]any{"method": "form", "sections": []any{map[string]any{"blocks": []any{map[string]any{"kind": "field", "field": map[string]any{"kind": "choice", "name": "x", "options": []any{map[string]any{"value": "a", "label": []any{map[string]any{"text": "A"}}}, map[string]any{"value": "b", "label": []any{map[string]any{"text": "B"}}}}}}}}}})}, nil, "表单"},
		{"计划审批", Message{Body: "", Request: json.RawMessage(`{"type":"plan_proposal","title":"计划"}`)}, nil, ""},
		{"已解决", Message{Body: "选 A 还是 B？", Resolved: true, Request: uiFrame("r1")}, nil, "选 A 还是 B？"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.m.NotificationID, c.m.Kind, c.m.ThreadID, c.m.Title = "ntf_1", KindInput, "T-1", "线程"
			b, err := pushPayload(c.m)
			require.NoError(t, err)
			var p map[string]any
			require.NoError(t, json.Unmarshal(b, &p))
			assert.Equal(t, c.body, p["body"])
			if c.acts == nil {
				assert.NotContains(t, p, "actions")
				return
			}
			got := []act{}
			for _, a := range p["actions"].([]any) {
				got = append(got, a.(map[string]any))
			}
			assert.Equal(t, c.acts, got)
		})
	}
	// 不是 input：从不带按钮
	b, err := pushPayload(Message{NotificationID: "ntf_1", Kind: KindDone, ThreadID: "T-1", Title: "t", Request: uiFrame("r1")})
	require.NoError(t, err)
	assert.NotContains(t, string(b), "actions")
	// 撤回消息只有 v / kind / id / tag
	b, err = pushPayload(Message{NotificationID: "ntf_1", Kind: KindInput, ThreadID: "T-0123456789abcdef", Title: "t", Body: "b", Withdraw: true})
	require.NoError(t, err)
	assert.JSONEq(t, `{"v":1,"kind":"withdraw","id":"ntf_1","tag":"oryn-456789abcdef"}`, string(b))
}

func TestApplePush(t *testing.T) {
	assert.True(t, applePush("https://web.push.apple.com/QF0abc"))
	assert.True(t, applePush("https://api.push.apple.com/x"))
	assert.False(t, applePush("https://fcm.googleapis.com/fcm/send/x"))
	assert.False(t, applePush("https://evilpush.apple.com.example/x"))
}

// inputNotification 发出一条 input 通知并返回它。
func (f *fixture) inputNotification(threadID, requestID string, fr json.RawMessage) db.Notification {
	f.t.Helper()
	f.s.InputRequested(threads.InputEvent{ThreadID: threadID, RequestID: requestID, Frame: fr})
	f.flush()
	for _, n := range f.notifications() {
		if n.RequestID != nil && *n.RequestID == requestID {
			return n
		}
	}
	f.t.Fatalf("no notification for %s", requestID)
	return db.Notification{}
}

func TestAnswer(t *testing.T) {
	f := newFixture(t)
	id := f.thread("t")
	n := f.inputNotification(id, "r1", uiFrame("r1"))

	tid, err := f.s.Answer(f.ctx, n.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, id, tid)
	calls := f.th.UICalls()
	require.Len(t, calls, 1)
	assert.Equal(t, id, calls[0].ThreadID)
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"r1","value":"B"}`, string(calls[0].Frame))

	var ne *Error
	_, err = f.s.Answer(f.ctx, n.ID, 2)
	require.ErrorAs(t, err, &ne)
	assert.Equal(t, "invalid_option", ne.Code)

	// 已在别处作答：线程服务先到先得
	f.th.mu.Lock()
	f.th.uiErr = threads.ErrUIResolved
	f.th.mu.Unlock()
	_, err = f.s.Answer(f.ctx, n.ID, 0)
	require.ErrorAs(t, err, &ne)
	assert.Equal(t, 409, ne.Status)
	assert.Equal(t, "input_resolved", ne.Code)
	f.th.mu.Lock()
	f.th.uiErr = nil
	f.th.mu.Unlock()

	// 通知已标为已解决：不再调线程服务
	f.s.InputResolved(id, "r1")
	f.flush()
	_, err = f.s.Answer(f.ctx, n.ID, 0)
	require.ErrorAs(t, err, &ne)
	assert.Equal(t, "input_resolved", ne.Code)
	assert.Len(t, f.th.UICalls(), 1)

	// 不能一触作答：文字输入、done
	text := f.inputNotification(id, "r2", frame(map[string]any{"method": "input", "title": "名字？"}))
	_, err = f.s.Answer(f.ctx, text.ID, 0)
	require.ErrorAs(t, err, &ne)
	assert.Equal(t, 422, ne.Status)
	assert.Equal(t, "not_answerable", ne.Code)
	f.done(id)
	for _, x := range f.notifications() {
		if x.Kind == KindDone {
			_, err = f.s.Answer(f.ctx, x.ID, 0)
			require.ErrorAs(t, err, &ne)
			assert.Equal(t, "not_answerable", ne.Code)
		}
	}

	_, err = f.s.Answer(f.ctx, "ntf_missing", 0)
	assert.ErrorIs(t, err, ErrNotFound)
}

// API 列表上的 quickAnswer：可一触作答的未决 input 带完整问题与选项（不受推送正文上限约束）；
// 已解决、文字输入与非 input 不带；请求解决时广播 notifications.resolved（站内作答按钮据此消失）。
func TestQuickAnswerAPI(t *testing.T) {
	f := newFixture(t)
	id := f.thread("t")
	long := "Allow tool: bash\nCommand: " + strings.Repeat("x", 300)
	f.inputNotification(id, "r1", frame(map[string]any{"method": "select", "title": long, "options": []string{"Approve", "Deny"}}))
	f.inputNotification(id, "r2", frame(map[string]any{"method": "confirm", "title": "Overwrite?", "message": "a.md exists"}))
	f.inputNotification(id, "r3", frame(map[string]any{"method": "input", "title": "名字？"}))
	r4 := f.inputNotification(id, "r4", uiFrame("r4"))
	require.Empty(t, f.out.Resolved())
	f.s.InputResolved(id, "r4")
	f.flush()
	assert.Equal(t, []string{r4.ID}, f.out.Resolved())

	list, err := f.s.List(f.ctx, 50, 0, false)
	require.NoError(t, err)
	got := map[string]*apigen.NotificationQuickAnswer{}
	for _, n := range list.Notifications {
		got[n.Body] = n.QuickAnswer
	}
	require.Len(t, got, 4)
	for body, q := range got {
		switch {
		case strings.HasPrefix(body, "Allow tool"):
			require.NotNil(t, q)
			assert.Equal(t, long, q.Question)
			assert.Equal(t, []apigen.NotificationQuickOption{{I: 0, Label: "Approve"}, {I: 1, Label: "Deny"}}, q.Options)
		case body == "Overwrite?":
			require.NotNil(t, q)
			assert.Equal(t, "Overwrite?\na.md exists", q.Question)
			require.Len(t, q.Options, 2)
			assert.Equal(t, apigen.Yes, *q.Options[0].Role)
			assert.Equal(t, apigen.No, *q.Options[1].Role)
		default: // 文字输入、已解决
			assert.Nil(t, q, body)
		}
	}
}

type pushRecorder struct {
	srv  *httptest.Server
	reqs chan pushReq

	mu sync.Mutex
	// onRequest 非空时在应答前调用一次（模拟发送途中请求被作答）。
	onRequest func()
}

type pushReq struct {
	h    http.Header
	body []byte
}

func newPushRecorder(t *testing.T) *pushRecorder {
	r := &pushRecorder{reqs: make(chan pushReq, 8)}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		fn := r.onRequest
		r.onRequest = nil
		r.mu.Unlock()
		if fn != nil {
			fn()
		}
		r.reqs <- pushReq{req.Header.Clone(), b}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *pushRecorder) next(t *testing.T, c *webpushtest.Client) (http.Header, map[string]any) {
	t.Helper()
	select {
	case g := <-r.reqs:
		plain, err := c.Decrypt(g.body)
		require.NoError(t, err)
		var p map[string]any
		require.NoError(t, json.Unmarshal(plain, &p))
		return g.h, p
	default:
		t.Fatal("no push request")
		return nil, nil
	}
}

func (f *fixture) webpushClient(endpoint string) *webpushtest.Client {
	f.t.Helper()
	c, err := webpushtest.NewClient(endpoint)
	require.NoError(f.t, err)
	_, err = f.s.CreatePushSubscription(f.ctx, apigen.PushSubscriptionInput{Endpoint: new(c.Sub.Endpoint), Keys: &apigen.PushSubscriptionKeys{P256Dh: c.Sub.P256dh, Auth: c.Sub.Auth}}, Owner{}, "UA")
	require.NoError(f.t, err)
	return c
}

// 请求在别处作答后，已收到带按钮推送的订阅收到撤回消息；没带按钮的推送不撤回。
func TestWithdrawOnResolve(t *testing.T) {
	f := newFixture(t)
	rec := newPushRecorder(t)
	c := f.webpushClient(rec.srv.URL + "/push/1")
	id := f.thread("t")
	tag := "oryn-" + id[len(id)-12:]

	n := f.inputNotification(id, "r1", uiFrame("r1"))
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	_, p := rec.next(t, c)
	require.Contains(t, p, "actions")

	f.s.InputResolved(id, "r1")
	f.flush()
	h, p := rec.next(t, c)
	assert.Equal(t, map[string]any{"v": 1.0, "kind": "withdraw", "id": n.ID, "tag": tag}, p)
	assert.Empty(t, h.Get("Topic"), "撤回不设 Topic，不顶掉同线程尚未送达的通知")
	assert.Equal(t, "normal", h.Get("Urgency"))

	// 文字输入（没有按钮）：作答后不撤回
	f.inputNotification(id, "r2", frame(map[string]any{"method": "input", "title": "名字？"}))
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	_, p = rec.next(t, c)
	require.NotContains(t, p, "actions")
	f.s.InputResolved(id, "r2")
	f.flush()
	assert.Empty(t, rec.reqs)

	// 推送尚未送出就已作答：送出时不带按钮，也不撤回
	f.inputNotification(id, "r3", uiFrame("r3"))
	f.s.InputResolved(id, "r3")
	f.flush()
	assert.Empty(t, rec.reqs)
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	_, p = rec.next(t, c)
	assert.NotContains(t, p, "actions")
	assert.Empty(t, rec.reqs)
}

// 发送途中请求被作答（resolve 时投递还没记成 sent）：送出后复查并补发撤回。
func TestWithdrawAfterRacingSend(t *testing.T) {
	f := newFixture(t)
	rec := newPushRecorder(t)
	c := f.webpushClient(rec.srv.URL + "/push/1")
	id := f.thread("t")
	n := f.inputNotification(id, "r1", uiFrame("r1"))
	rec.mu.Lock()
	rec.onRequest = func() {
		_, err := f.st.Q.ResolveNotificationRequest(f.ctx, db.ResolveNotificationRequestParams{Now: f.s.now(), ThreadID: id, RequestID: "r1"})
		if err != nil {
			panic(errors.Join(errors.New("resolve"), err))
		}
	}
	rec.mu.Unlock()
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	_, p := rec.next(t, c)
	require.Contains(t, p, "actions")
	_, p = rec.next(t, c)
	assert.Equal(t, "withdraw", p["kind"])
	assert.Equal(t, n.ID, p["id"])
}
