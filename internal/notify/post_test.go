package notify

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/webpush/webpushtest"
)

func TestPostForThread(t *testing.T) {
	f := newFixture(t)
	h := newHook(t)
	f.webhookChannel(h.srv.URL+"/in", "", apigen.NotificationKindError)
	id := f.thread("每日巡检")

	require.NoError(t, f.s.Post(f.ctx, id, "ignored", `Scheduled run "daily" could not be delivered: queue_full`))
	ns := f.notifications()
	require.Len(t, ns, 1)
	n := ns[0]
	assert.Equal(t, KindError, n.Kind)
	assert.Equal(t, "每日巡检", n.Title, "有线程时标题取线程标题")
	assert.Equal(t, id, *n.ThreadID)
	assert.Equal(t, `Scheduled run "daily" could not be delivered: queue_full`, n.Body)
	assert.True(t, f.s.unread[id], "登记未读")
	require.Len(t, f.out.Notes(), 1)
	assert.Equal(t, "/t/"+id, f.out.Notes()[0].URL)
	require.Len(t, f.deliveries(n.ID), 1)

	assert.Equal(t, 1, f.s.DeliverDue(f.ctx))
	var body struct {
		Event  string
		Thread struct{ ID, URL string }
	}
	require.NoError(t, json.Unmarshal(h.requests()[0].Body, &body))
	assert.Equal(t, "thread.error", body.Event)
	assert.Equal(t, "https://oryn.test/t/"+id, body.Thread.URL)

	// 已归档或不存在的线程不发
	_, err := f.st.DB.ExecContext(f.ctx, `UPDATE threads SET archived_at = 1 WHERE id = ?`, id)
	require.NoError(t, err)
	require.NoError(t, f.s.Post(f.ctx, id, "", "x"))
	require.NoError(t, f.s.Post(f.ctx, "T-missing", "", "x"))
	assert.Len(t, f.notifications(), 1)
}

func TestPostWithoutThread(t *testing.T) {
	f := newFixture(t)
	h := newHook(t)
	f.webhookChannel(h.srv.URL+"/in", "")

	require.NoError(t, f.s.Post(f.ctx, "", `Schedule "bad"`, "Scheduled run could not start a thread: provider not found"))
	ns := f.notifications()
	require.Len(t, ns, 1)
	assert.Nil(t, ns[0].ThreadID)
	assert.Equal(t, `Schedule "bad"`, ns[0].Title)
	assert.Equal(t, KindError, ns[0].Kind)
	require.NotNil(t, ns[0].Link, "落点写进 link 列")
	assert.Equal(t, AutomationsPath, *ns[0].Link)
	notes := f.out.Notes()
	require.Len(t, notes, 1)
	assert.Equal(t, AutomationsPath, notes[0].URL)
	assert.Nil(t, notes[0].ThreadID)
	assert.Empty(t, f.s.unread, "没有线程就没有线程未读")

	list, err := f.s.List(f.ctx, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, AutomationsPath, list.Notifications[0].URL)

	assert.Equal(t, 1, f.s.DeliverDue(f.ctx))
	var body map[string]any
	require.NoError(t, json.Unmarshal(h.requests()[0].Body, &body))
	assert.Equal(t, "thread.error", body["event"])
	assert.Equal(t, `Schedule "bad"`, body["title"])
	assert.Equal(t, "https://oryn.test"+AutomationsPath, body["url"])
	assert.NotContains(t, body, "thread")

	payload, err := pushPayload(Message{NotificationID: "ntf_1", Kind: KindError, Title: "t", Link: AutomationsPath})
	require.NoError(t, err)
	var push map[string]any
	require.NoError(t, json.Unmarshal(payload, &push))
	assert.Equal(t, AutomationsPath, push["url"])
	assert.Equal(t, "oryn-automations", push["tag"])
}

// subscribe 登记一条（不会被真正投递的）Web Push 订阅。
func (f *fixture) subscribe(endpoint string) {
	f.t.Helper()
	c, err := webpushtest.NewClient(endpoint)
	require.NoError(f.t, err)
	_, err = f.s.CreatePushSubscription(f.ctx, apigen.PushSubscriptionInput{Endpoint: new(c.Sub.Endpoint),
		Keys: &apigen.PushSubscriptionKeys{P256Dh: c.Sub.P256dh, Auth: c.Sub.Auth}}, Owner{}, "UA")
	require.NoError(f.t, err)
}

func TestPostSystemBudget(t *testing.T) {
	f := newFixture(t)
	h := newHook(t)
	f.webhookChannel(h.srv.URL+"/in", "", apigen.NotificationKindBudget)
	f.webhookChannel(h.srv.URL+"/err", "", apigen.NotificationKindError)
	f.subscribe(h.srv.URL + "/push")

	require.NoError(t, f.s.PostSystem(f.ctx, KindBudget, "Daily budget exceeded", "Spent $12.00 of $10.00 today", "/stats/usage"))
	ns := f.notifications()
	require.Len(t, ns, 1)
	n := ns[0]
	assert.Nil(t, n.ThreadID)
	assert.Equal(t, KindBudget, n.Kind)
	require.NotNil(t, n.Link)
	assert.Equal(t, "/stats/usage", *n.Link)
	f.s.mu.Lock()
	assert.Empty(t, f.s.unread, "系统通知不登记线程未读")
	f.s.mu.Unlock()

	notes := f.out.Notes()
	require.Len(t, notes, 1)
	assert.Equal(t, "/stats/usage", notes[0].URL, "广播的 url 取 link")
	assert.Nil(t, notes[0].ThreadID)
	assert.Equal(t, apigen.NotificationKindBudget, notes[0].Kind)
	list, err := f.s.List(f.ctx, 10, 0, false)
	require.NoError(t, err)
	require.Len(t, list.Notifications, 1)
	assert.Equal(t, notes[0].URL, list.Notifications[0].URL)
	assert.Nil(t, list.Notifications[0].ThreadID)

	// 渠道过滤：只订阅 error 的渠道不投；推送订阅一律投
	ds := f.deliveries(n.ID)
	targets := map[string]int{}
	for _, d := range ds {
		targets[d.Target]++
	}
	assert.Equal(t, map[string]int{"webhook": 1, "webpush": 1}, targets)

	// Webhook：budget.alert，无 thread 键，顶层 url 为绝对地址
	assert.Equal(t, 2, f.s.DeliverDue(f.ctx))
	var hooks []hookReq
	for _, r := range h.requests() {
		if r.Path != "/push" {
			hooks = append(hooks, r)
		}
	}
	require.Len(t, hooks, 1)
	reqs := hooks
	assert.Equal(t, "/in", reqs[0].Path)
	assert.Equal(t, "budget.alert", reqs[0].Header.Get("X-Oryn-Event"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(reqs[0].Body, &body))
	assert.Equal(t, "budget.alert", body["event"])
	assert.NotContains(t, body, "thread")
	assert.Equal(t, "https://oryn.test/stats/usage", body["url"])
	assert.Equal(t, "Daily budget exceeded", body["title"])

	// 推送载荷：url 为站内路径、tag 按种类合并
	payload, err := pushPayload(f.s.message(n, db.NotificationDelivery{}))
	require.NoError(t, err)
	var push map[string]any
	require.NoError(t, json.Unmarshal(payload, &push))
	assert.Equal(t, "/stats/usage", push["url"])
	assert.Equal(t, "oryn-budget", push["tag"])
	assert.Equal(t, "budget", push["kind"])
}

func TestPostSystemRejectsInvalid(t *testing.T) {
	f := newFixture(t)
	h := newHook(t)
	f.webhookChannel(h.srv.URL, "")
	f.subscribe("https://push.example/sub/x")
	for name, c := range map[string][4]string{
		"kind done":     {KindDone, "t", "", "/stats/usage"},
		"kind input":    {KindInput, "t", "", "/stats/usage"},
		"empty title":   {KindBudget, "", "", "/stats/usage"},
		"relative link": {KindBudget, "t", "", "stats"},
		"empty link":    {KindBudget, "t", "", ""},
		"protocol-rel":  {KindBudget, "t", "", "//evil"},
		// 与桌面壳 valid_app_path 同规则（sec F7）：URL 标准把 "\" 当 "/"，"/\evil" 会被 openWindow 解析成外站
		"backslash":    {KindBudget, "t", "", `/\evil.example`},
		"newline":      {KindBudget, "t", "", "/a\nb"},
		"tab":          {KindBudget, "t", "", "/\t/evil.example"},
		"c1 control":   {KindBudget, "t", "", "/a\u0085b"},
		"invalid utf8": {KindBudget, "t", "", "/a\xffb"},
		"too long":     {KindBudget, "t", "", "/" + strings.Repeat("a", 2048)},
	} {
		assert.Error(t, f.s.PostSystem(f.ctx, c[0], c[1], c[2], c[3]), name)
	}
	assert.Empty(t, f.notifications(), "参数不合法不落库")
	n, err := f.st.DB.QueryContext(f.ctx, `SELECT 1 FROM notification_deliveries`)
	require.NoError(t, err)
	assert.False(t, n.Next(), "也不排投递")
	require.NoError(t, n.Close())
	assert.Empty(t, f.out.Notes())
	require.NoError(t, f.s.PostSystem(f.ctx, KindBudget, "t", "", "/"+strings.Repeat("a", 2047)), "2048 字节恰好放行")
}

func TestMarkReadByID(t *testing.T) {
	f := newFixture(t)
	th := f.thread("t")
	f.done(th)
	require.NoError(t, f.s.PostSystem(f.ctx, KindBudget, "b1", "", "/stats/usage"))
	require.NoError(t, f.s.PostSystem(f.ctx, KindError, "e1", "", AutomationsPath))
	var target string
	for _, n := range f.notifications() {
		if n.Title == "b1" {
			target = n.ID
		}
	}
	require.NotEmpty(t, target)

	require.NoError(t, f.s.MarkRead(f.ctx, "", target))
	for _, n := range f.notifications() {
		if n.ID == target {
			assert.NotNil(t, n.ReadAt, "按 id 已读：该条已读")
		} else {
			assert.Nil(t, n.ReadAt, "按 id 已读不影响其它通知：%s", n.Title)
		}
	}
	assert.Equal(t, [][2]string{{"", target}}, f.out.ReadEvents())
	f.s.mu.Lock()
	assert.True(t, f.s.unread[th], "按 id 已读不动线程未读登记")
	f.s.mu.Unlock()
	list, err := f.s.List(f.ctx, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, 2, list.Unread)

	// 不存在 / 已读的 id：同样成功
	require.NoError(t, f.s.MarkRead(f.ctx, "", "ntf_missing"))
	require.NoError(t, f.s.MarkRead(f.ctx, "", target))

	// 按线程：只标该线程，系统通知不受影响
	require.NoError(t, f.s.MarkRead(f.ctx, th, ""))
	list, err = f.s.List(f.ctx, 10, 0, false)
	require.NoError(t, err)
	assert.Equal(t, 1, list.Unread)
	f.s.mu.Lock()
	assert.False(t, f.s.unread[th])
	f.s.mu.Unlock()
	evs := f.out.ReadEvents()
	assert.Equal(t, [2]string{th, ""}, evs[len(evs)-1])
}
