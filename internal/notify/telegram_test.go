package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/notify/notifytest"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

const linkedChat = int64(777)

func newTestBot(t *testing.T) *notifytest.Bot {
	b := notifytest.NewBot("123456:ABC-secret-token", "oryn_test_bot")
	t.Cleanup(b.Close)
	return b
}

func (f *fixture) telegramChannel(b *notifytest.Bot) apigen.NotificationChannel {
	f.t.Helper()
	ch, err := f.s.CreateChannel(f.ctx, apigen.NotificationChannelInput{Kind: apigen.NotificationChannelKindTelegram, Name: "tg",
		Telegram: &apigen.TelegramChannelConfig{BotToken: b.Token, APIBase: new(b.URL())}})
	require.NoError(f.t, err)
	return ch
}

// push 塞一条更新并同步处理一次 getUpdates。
func (f *fixture) push(b *notifytest.Bot, chID string, u map[string]any) {
	f.t.Helper()
	b.Push(u)
	require.NoError(f.t, f.s.pollOnce(f.ctx, chID))
}

func next(t *testing.T, b *notifytest.Bot, method string) notifytest.Call {
	t.Helper()
	c, ok := b.Next(5 * time.Second)
	require.True(t, ok, "等待 %s 调用超时", method)
	require.Equal(t, method, c.Method, "%+v", c.Body)
	return c
}

// linked 创建并绑定一个 Telegram 渠道。
func (f *fixture) linked(b *notifytest.Bot) apigen.NotificationChannel {
	f.t.Helper()
	ch := f.telegramChannel(b)
	link, err := f.s.LinkTelegram(f.ctx, ch.ID)
	require.NoError(f.t, err)
	f.push(b, ch.ID, notifytest.PrivateMessage(linkedChat, "/start "+link.Code))
	next(f.t, b, "sendMessage")
	return ch
}

func TestTelegramCreateAndLink(t *testing.T) {
	f := newFixture(t)
	b := newTestBot(t)

	_, err := f.s.CreateChannel(f.ctx, apigen.NotificationChannelInput{Kind: apigen.NotificationChannelKindTelegram, Name: "tg",
		Telegram: &apigen.TelegramChannelConfig{BotToken: "wrong", APIBase: new(b.URL())}})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "invalid_channel", e.Code)
	assert.Contains(t, e.Message, "Unauthorized", "带 Telegram 的 description")

	ch := f.telegramChannel(b)
	assert.Equal(t, "@oryn_test_bot", ch.Summary)
	require.NotNil(t, ch.Telegram)
	assert.False(t, ch.Telegram.Linked)
	_, err = f.s.CreateChannel(f.ctx, apigen.NotificationChannelInput{Kind: apigen.NotificationChannelKindTelegram, Name: "tg2",
		Telegram: &apigen.TelegramChannelConfig{BotToken: b.Token, APIBase: new(b.URL())}})
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "channel_exists", e.Code)
	assert.Equal(t, http.StatusConflict, e.Status)

	// 未绑定：测试 → 409；真实投递 → 不可重试的失败
	_, err = f.s.TestChannel(f.ctx, ch.ID)
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "channel_not_linked", e.Code)
	d := f.onlyDelivery(f.thread("t"))
	f.s.DeliverDue(f.ctx)
	d = f.delivery(d.ID)
	assert.Equal(t, statusFailed, d.Status)
	assert.Contains(t, *d.LastError, "not linked")

	link, err := f.s.LinkTelegram(f.ctx, ch.ID)
	require.NoError(t, err)
	assert.Regexp(t, `^[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}$`, link.Code)
	assert.Equal(t, "oryn_test_bot", link.BotUsername)
	assert.Equal(t, f.clock.Now().Add(10*time.Minute).UnixMilli(), link.ExpiresAt)

	// 错码：不绑定，回 notLinked
	f.push(b, ch.ID, notifytest.PrivateMessage(linkedChat, "/start ZZZZ-ZZZZ"))
	c := next(t, b, "sendMessage")
	assert.Equal(t, text("zh-CN", "notLinked"), c.Str("text"))
	// 群聊里的正确码：不绑定
	grp := notifytest.PrivateMessage(-100, "/start "+link.Code)
	grp["message"].(map[string]any)["chat"].(map[string]any)["type"] = "group"
	f.push(b, ch.ID, grp)
	// 过期码：不绑定
	f.clock.Advance(11 * time.Minute)
	f.push(b, ch.ID, notifytest.PrivateMessage(linkedChat, "/start "+link.Code))
	next(t, b, "sendMessage")
	got, err := f.s.GetChannel(f.ctx, ch.ID)
	require.NoError(t, err)
	assert.False(t, got.Telegram.Linked)

	// 新码，小写、去掉连字符也认
	link, err = f.s.LinkTelegram(f.ctx, ch.ID)
	require.NoError(t, err)
	f.push(b, ch.ID, notifytest.PrivateMessage(linkedChat, "/start "+strings.ToLower(strings.ReplaceAll(link.Code, "-", ""))))
	c = next(t, b, "sendMessage")
	assert.EqualValues(t, linkedChat, c.Int("chat_id"))
	assert.Equal(t, text("zh-CN", "linked"), c.Str("text"))
	got, err = f.s.GetChannel(f.ctx, ch.ID)
	require.NoError(t, err)
	assert.True(t, got.Telegram.Linked)
	assert.Equal(t, "Tester", *got.Telegram.ChatTitle)
	assert.Empty(t, b.Drain(), "群聊的 /start 被静默丢弃")

	// 绑定后：别的私聊发来的 /start 与消息静默丢弃
	f.push(b, ch.ID, notifytest.PrivateMessage(999, "/start "+link.Code))
	f.push(b, ch.ID, notifytest.PrivateMessage(999, "hello"))
	assert.Empty(t, b.Drain())
	// 已绑定的聊天：非回复消息 → replyHint
	f.push(b, ch.ID, notifytest.PrivateMessage(linkedChat, "hello"))
	c = next(t, b, "sendMessage")
	assert.Equal(t, text("zh-CN", "replyHint"), c.Str("text"))

	res, err := f.s.TestChannel(f.ctx, ch.ID)
	require.NoError(t, err)
	assert.True(t, res.Ok)
	c = next(t, b, "sendMessage")
	assert.Contains(t, c.Str("text"), text("zh-CN", "testTitle"))
}

// inputDelivery 生成一条 select 待答通知并投递到 Telegram，返回投递 id 与消息 id。
func (f *fixture) inputDelivery(b *notifytest.Bot, threadID, req string) (string, int64) {
	f.t.Helper()
	f.s.InputRequested(threads.InputEvent{ThreadID: threadID, RequestID: req, Frame: uiFrame(req)})
	f.flush()
	ns := f.notifications()
	ds := f.deliveries(ns[0].ID)
	require.Len(f.t, ds, 1)
	require.Equal(f.t, 1, f.s.DeliverDue(f.ctx))
	d := f.delivery(ds[0].ID)
	require.Equal(f.t, statusSent, d.Status, "%v", d.LastError)
	require.NotNil(f.t, d.ExternalRef)
	_, msg, ok := parseRef(*d.ExternalRef)
	require.True(f.t, ok)
	return d.ID, msg
}

func TestTelegramInputButtonsAndCallback(t *testing.T) {
	f := newFixture(t)
	b := newTestBot(t)
	ch := f.linked(b)
	id := f.thread("修复登录")

	del, msg := f.inputDelivery(b, id, "r1")
	c := next(t, b, "sendMessage")
	txt := c.Str("text")
	assert.True(t, strings.HasPrefix(txt, "❓ 修复登录\n选 A 还是 B？"), txt)
	assert.Contains(t, txt, "1. A\n2. B")
	assert.Contains(t, txt, text("zh-CN", "answerHint"))
	assert.Contains(t, txt, "https://oryn.test/t/"+id)
	btns := c.Buttons()
	require.Len(t, btns, 2)
	assert.Equal(t, "B", btns[1]["text"])
	assert.Equal(t, fmt.Sprintf("o:%s:1", del), btns[1]["callback_data"])
	assert.Equal(t, fmt.Sprintf("%d:%d", linkedChat, msg), *f.delivery(del).ExternalRef)

	// 点第 2 个按钮 → UIResponse 收到 value=B；回答回调、去掉按钮
	f.push(b, ch.ID, notifytest.Callback(linkedChat, msg, fmt.Sprintf("o:%s:1", del)))
	ui := f.th.UICalls()
	require.Len(t, ui, 1)
	assert.Equal(t, id, ui[0].ThreadID)
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"r1","value":"B"}`, string(ui[0].Frame))
	c = next(t, b, "answerCallbackQuery")
	assert.Equal(t, text("zh-CN", "answered"), c.Str("text"))
	c = next(t, b, "editMessageReplyMarkup")
	assert.EqualValues(t, msg, c.Int("message_id"))
	assert.Empty(t, c.Buttons())

	// 已在别处作答（先到先得）
	f.th.mu.Lock()
	f.th.uiErr = threads.ErrUIResolved
	f.th.mu.Unlock()
	f.push(b, ch.ID, notifytest.Callback(linkedChat, msg, fmt.Sprintf("o:%s:0", del)))
	c = next(t, b, "answerCallbackQuery")
	assert.Equal(t, text("zh-CN", "answeredElsewhere"), c.Str("text"))
	next(t, b, "editMessageReplyMarkup")

	// 非绑定聊天的回调被忽略
	f.push(b, ch.ID, notifytest.Callback(999, msg, fmt.Sprintf("o:%s:0", del)))
	assert.Empty(t, b.Drain())
}

func TestTelegramTextAnswerAndSteer(t *testing.T) {
	f := newFixture(t)
	b := newTestBot(t)
	ch := f.linked(b)
	id := f.thread("t")

	_, msg := f.inputDelivery(b, id, "r2")
	next(t, b, "sendMessage")
	// 选项文字不匹配 → 提示用选项作答（select 不接受自由文本）
	f.push(b, ch.ID, notifytest.Reply(linkedChat, msg, "C"))
	c := next(t, b, "sendMessage")
	assert.Equal(t, text("zh-CN", "answerHint"), c.Str("text"))
	f.push(b, ch.ID, notifytest.Reply(linkedChat, msg, "2"))
	ui := f.th.UICalls()
	require.Len(t, ui, 1)
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"r2","value":"B"}`, string(ui[0].Frame))
	c = next(t, b, "sendMessage")
	assert.Equal(t, text("zh-CN", "answered"), c.Str("text"))

	// 回复 done 通知：线程已空闲 → prompt（开启新回合；带 streamingBehavior 的消息在回合结束后不会开启回合）
	f.done(id)
	require.Equal(t, 1, f.s.DeliverDue(f.ctx))
	c = next(t, b, "sendMessage")
	assert.True(t, strings.HasPrefix(c.Str("text"), "✅ t\n"), c.Str("text"))
	assert.Contains(t, c.Str("text"), text("zh-CN", "replyHint"))
	assert.Nil(t, c.Buttons())
	d := f.deliveries(f.notifications()[0].ID)[0]
	_, doneMsg, _ := parseRef(*d.ExternalRef)
	f.push(b, ch.ID, notifytest.Reply(linkedChat, doneMsg, "再输出一行 TG-STEER-OK"))
	cmds := f.th.Commands()
	require.Len(t, cmds, 1)
	assert.Equal(t, id, cmds[0].ThreadID)
	assert.JSONEq(t, `{"type":"prompt","message":"再输出一行 TG-STEER-OK"}`, string(cmds[0].Frame))
	c = next(t, b, "sendMessage")
	assert.Equal(t, text("zh-CN", "sent"), c.Str("text"))

	// 线程正在跑回合 → steer（与 Web 输入区「忙时 Enter = steer」一致）
	require.NoError(t, f.st.Q.SetThreadState(f.ctx, db.SetThreadStateParams{State: threads.StateWorking, StateChangedAt: 2, ID: id}))
	f.push(b, ch.ID, notifytest.Reply(linkedChat, doneMsg, "顺便看看日志"))
	cmds = f.th.Commands()
	require.Len(t, cmds, 2)
	assert.JSONEq(t, `{"type":"steer","message":"顺便看看日志"}`, string(cmds[1].Frame))
	c = next(t, b, "sendMessage")
	assert.Equal(t, text("zh-CN", "sent"), c.Str("text"))
	require.NoError(t, f.st.Q.SetThreadState(f.ctx, db.SetThreadStateParams{State: threads.StateIdle, StateChangedAt: 3, ID: id}))

	// 应答失败 → failed: <error>
	f.th.mu.Lock()
	f.th.cmdReply = json.RawMessage(`{"type":"response","success":false,"error":"no orb"}`)
	f.th.mu.Unlock()
	f.push(b, ch.ID, notifytest.Reply(linkedChat, doneMsg, "x"))
	c = next(t, b, "sendMessage")
	assert.Equal(t, text("zh-CN", "failed")+": no orb", c.Str("text"))

	// 另一个聊天回复同一条通知：忽略
	f.push(b, ch.ID, notifytest.Reply(999, doneMsg, "evil"))
	assert.Len(t, f.th.Commands(), 3)
	assert.Empty(t, b.Drain())
}

func TestTelegramOffsetPersistsAcrossRestart(t *testing.T) {
	f := newFixture(t)
	b := newTestBot(t)
	ch := f.linked(b)
	id1 := b.Push(notifytest.PrivateMessage(linkedChat, "a"))
	id2 := b.Push(notifytest.PrivateMessage(linkedChat, "b"))
	require.NoError(t, f.s.pollOnce(f.ctx, ch.ID))
	next(t, b, "sendMessage")
	next(t, b, "sendMessage")
	row, err := f.st.Q.GetNotificationChannel(f.ctx, ch.ID)
	require.NoError(t, err)
	assert.Equal(t, id2+1, parseTgState(row.State).Offset)
	assert.Greater(t, id2, id1)

	// 重建服务（控制面重启）后从库里的 offset 继续：旧更新不会重放
	f.s = f.newService()
	id3 := b.Push(notifytest.PrivateMessage(linkedChat, "c"))
	require.NoError(t, f.s.pollOnce(f.ctx, ch.ID))
	offs := b.Offsets()
	assert.Equal(t, id2+1, offs[len(offs)-1])
	next(t, b, "sendMessage")
	assert.Empty(t, b.Drain(), "a / b 没有被再处理一次")
	row, _ = f.st.Q.GetNotificationChannel(f.ctx, ch.ID)
	assert.Equal(t, id3+1, parseTgState(row.State).Offset)

	// getUpdates 失败（409：别处在轮询）→ 返回错误，offset 不变
	b.FailPolls(1)
	require.Error(t, f.s.pollOnce(f.ctx, ch.ID))
}

func TestTelegramRevokeButtonsWhenResolvedElsewhere(t *testing.T) {
	f := newFixture(t)
	b := newTestBot(t)
	f.linked(b)
	id := f.thread("t")
	_, msg := f.inputDelivery(b, id, "r9")
	next(t, b, "sendMessage")

	f.s.InputResolved(id, "r9")
	f.s.processEvents(f.ctx)
	c := next(t, b, "editMessageReplyMarkup")
	assert.EqualValues(t, msg, c.Int("message_id"))
	assert.EqualValues(t, linkedChat, c.Int("chat_id"))
	assert.Empty(t, c.Buttons())
}

func TestTelegramPatchTokenResetsLink(t *testing.T) {
	f := newFixture(t)
	b := newTestBot(t)
	ch := f.linked(b)
	got, err := f.s.UpdateChannel(f.ctx, ch.ID, apigen.NotificationChannelPatch{Name: new("renamed")})
	require.NoError(t, err)
	assert.True(t, got.Telegram.Linked, "只改名不影响绑定")

	b2 := newTestBot(t)
	b2.Username = "other_bot"
	got, err = f.s.UpdateChannel(f.ctx, ch.ID, apigen.NotificationChannelPatch{Telegram: &apigen.TelegramChannelConfig{BotToken: b2.Token, APIBase: new(b2.URL())}})
	require.NoError(t, err)
	assert.False(t, got.Telegram.Linked, "换 bot 清掉绑定")
	assert.Equal(t, "@other_bot", got.Summary)

	_, err = f.s.UpdateChannel(f.ctx, ch.ID, apigen.NotificationChannelPatch{Webhook: &apigen.WebhookChannelConfig{URL: "https://x.example"}})
	var e *Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "invalid_channel", e.Code)
}

// Run 期间的轮询 goroutine：启用即开始轮询，停用即停止。
func TestTelegramPollerLifecycle(t *testing.T) {
	f := newFixture(t)
	b := newTestBot(t)
	ch := f.linked(b)
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() {
		f.s.Run(ctx)
		close(done)
	}()
	b.Push(notifytest.PrivateMessage(linkedChat, "ping"))
	c := next(t, b, "sendMessage")
	assert.Equal(t, text("zh-CN", "replyHint"), c.Str("text"))

	_, err := f.s.UpdateChannel(f.ctx, ch.ID, apigen.NotificationChannelPatch{Enabled: new(false)})
	require.NoError(t, err)
	f.s.tgMu.Lock()
	assert.Empty(t, f.s.pollers)
	f.s.tgMu.Unlock()
	cancel()
	<-done
}
