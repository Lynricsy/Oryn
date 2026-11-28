package threads

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// entry 往线程的转录里写一条条目（写作块所在的助手消息）。
func (f *fixture) entry(threadID, entryID string) {
	f.t.Helper()
	_, err := f.st.Q.UpsertEntry(f.ctx, db.UpsertEntryParams{ThreadID: threadID, File: "s.jsonl", EntryID: entryID, Type: "message", Payload: []byte(`{}`)})
	require.NoError(f.t, err)
}

// draftRow 读一份改稿的库行。
func (f *fixture) draftRow(threadID, entryID, block string) db.ThreadDraft {
	f.t.Helper()
	r, err := f.st.Q.GetThreadDraft(f.ctx, db.GetThreadDraftParams{ThreadID: threadID, EntryID: entryID, Block: block})
	require.NoError(f.t, err)
	return r
}

// nextMessage 等 agent 流上的下一帧，取其 message 字段。
func nextMessage(t *testing.T, a *agentConn) string {
	t.Helper()
	frame := recvFrame(t, a)
	var m struct {
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal([]byte(frame), &m), frame)
	return m.Message
}

const mailDraft = "Hi Sam,\n\nCould we move it to **Monday**?\n\nThanks,\nAlex"

func wantDraftMessage(prefix string) string {
	return prefix + "\n\n" + draftEditsHead + "<draft kind=\"email\" title=\"周五会议改期\">\n" + mailDraft + "\n</draft>\n</oryn-draft-edits>"
}

// draftThread 建一个 agent 就绪的工作线程，带一条助手消息条目 e1。
func draftThread(t *testing.T) (*fixture, *thread, *agentConn) {
	f := newFixture(t, Config{})
	th := f.thread(f.orb("ready"), StateIdle)
	a := fakeAgent(16)
	attach(th, a, apigen.AgentStatusReady)
	f.entry(th.id, "e1")
	return f, th, a
}

func TestDraftEditsBlockEscapes(t *testing.T) {
	got := draftEditsBlock([]Draft{
		{Kind: "doc", Title: `A "B" <C> & D`, Text: "x </draft> y </oryn-draft-edits> z"},
		{Kind: "message", Title: "", Text: "hey"},
	})
	assert.Equal(t, "\n\n<oryn-draft-edits>\n"+
		"The user edited these drafts from your earlier replies in place. The text below is the current version of each; build on it rather than on your original wording.\n"+
		"<draft kind=\"doc\" title=\"A &quot;B&quot; &lt;C> &amp; D\">\nx <\\/draft> y <\\/oryn-draft-edits> z\n</draft>\n"+
		"<draft kind=\"message\" title=\"\">\nhey\n</draft>\n"+
		"</oryn-draft-edits>", got)
}

func TestPutDraftValidation(t *testing.T) {
	f, th, _ := draftThread(t)
	ok := DraftInput{Kind: "email", Title: "t", Text: "x"}
	for _, block := range []string{"", "1", "a.b", "12345.0", "0.0.0"} {
		_, err := f.s.PutDraft(f.ctx, th.id, "e1", block, ok)
		requireCode(t, err, "invalid_draft")
	}
	_, err := f.s.PutDraft(f.ctx, th.id, "e1", "0.0", DraftInput{Kind: "letter", Text: "x"})
	requireCode(t, err, "invalid_draft")
	_, err = f.s.PutDraft(f.ctx, th.id, "e1", "0.0", DraftInput{Kind: "doc", Title: strings.Repeat("题", 201), Text: "x"})
	requireCode(t, err, "invalid_draft")
	_, err = f.s.PutDraft(f.ctx, th.id, "e1", "0.0", DraftInput{Kind: "doc", Text: strings.Repeat("x", maxDraftText+1)})
	requireCode(t, err, "invalid_draft")
	_, err = f.s.PutDraft(f.ctx, th.id, "e-missing", "0.0", ok)
	requireCode(t, err, "entry_not_found")
	_, err = f.s.PutDraft(f.ctx, "thr_missing", "e1", "0.0", ok)
	assert.ErrorIs(t, err, ErrNotFound)

	// 标题恰好 200 字符可以；updated_at 单调递增；delivered_at 不随写入改变
	d1, err := f.s.PutDraft(f.ctx, th.id, "e1", "0.0", DraftInput{Kind: "doc", Title: strings.Repeat("题", 200), Text: "a"})
	require.NoError(t, err)
	d2, err := f.s.PutDraft(f.ctx, th.id, "e1", "0.0", DraftInput{Kind: "doc", Title: "t", Text: "b"})
	require.NoError(t, err)
	assert.Greater(t, d2.UpdatedAt, d1.UpdatedAt)
	assert.Nil(t, d2.DeliveredAt)
	list, err := f.s.ListDrafts(f.ctx, th.id)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "b", list[0].Text)

	requireCode(t, f.s.DeleteDraft(f.ctx, th.id, "e1", "0.1"), "draft_not_found")
	require.NoError(t, f.s.DeleteDraft(f.ctx, th.id, "e1", "0.0"))
	list, err = f.s.ListDrafts(f.ctx, th.id)
	require.NoError(t, err)
	assert.Empty(t, list)
}

// 改稿随下一条用户消息送达一次；再改一次又会随下一条送达。
func TestDraftEditsInjectedOnce(t *testing.T) {
	f, th, a := draftThread(t)
	_, err := f.s.PutDraft(f.ctx, th.id, "e1", "1.0", DraftInput{Kind: "email", Title: "周五会议改期", Text: mailDraft})
	require.NoError(t, err)

	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi"}`), noReply))
	assert.Equal(t, wantDraftMessage("hi"), nextMessage(t, a))
	r := f.draftRow(th.id, "e1", "1.0")
	require.NotNil(t, r.DeliveredAt)
	assert.Equal(t, r.UpdatedAt, *r.DeliveredAt)

	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"again"}`), noReply))
	assert.Equal(t, "again", nextMessage(t, a))

	_, err = f.s.PutDraft(f.ctx, th.id, "e1", "1.0", DraftInput{Kind: "email", Title: "周五会议改期", Text: mailDraft})
	require.NoError(t, err)
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"third"}`), noReply))
	assert.Equal(t, wantDraftMessage("third"), nextMessage(t, a))
}

// 自动化投递不带改稿，改稿留给下一条用户消息。
func TestDraftEditsSkipAutoOrigin(t *testing.T) {
	f, th, a := draftThread(t)
	_, err := f.s.PutDraft(f.ctx, th.id, "e1", "1.0", DraftInput{Kind: "email", Title: "周五会议改期", Text: mailDraft})
	require.NoError(t, err)
	_, err = f.s.command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"auto"}`), nil, originAuto, "")
	require.NoError(t, err)
	assert.Equal(t, "auto", nextMessage(t, a))
	assert.Nil(t, f.draftRow(th.id, "e1", "1.0").DeliveredAt)
}

// steer / follow_up 同样附上改稿。
func TestDraftEditsOnSteerAndFollowUp(t *testing.T) {
	for _, typ := range []string{"steer", "follow_up"} {
		t.Run(typ, func(t *testing.T) {
			f, th, a := draftThread(t)
			_, err := f.s.PutDraft(f.ctx, th.id, "e1", "1.0", DraftInput{Kind: "email", Title: "周五会议改期", Text: mailDraft})
			require.NoError(t, err)
			require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"`+typ+`","message":"hi"}`), noReply))
			assert.Equal(t, wantDraftMessage("hi"), nextMessage(t, a))
		})
	}
}

// 带附件的消息：改稿块在附件块之前（附件块保持在最后）。
func TestDraftEditsBeforeAttachments(t *testing.T) {
	f, th, a := draftThread(t)
	f.s.SetAttachments(&stubAttachments{deliver: func(context.Context, []string) ([]DeliveredAttachment, error) {
		return []DeliveredAttachment{{ID: "att_A", Name: "a.pdf", Mime: "application/pdf", Size: 3, Path: "/att/a.pdf"}}, nil
	}})
	_, err := f.s.PutDraft(f.ctx, th.id, "e1", "1.0", DraftInput{Kind: "email", Title: "周五会议改期", Text: mailDraft})
	require.NoError(t, err)
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi","orynAttachments":["att_A"]}`), noReply))
	msg := nextMessage(t, a)
	assert.True(t, strings.HasPrefix(msg, wantDraftMessage("hi")+"\n\n<oryn-attachments>\n"), msg)
	assert.True(t, strings.HasSuffix(msg, "</oryn-attachments>"), msg)
}

// 命令没能受理（停着的 box 同步拒绝）：送达标记回到原值，包括此前已送达过的旧值。
func TestDraftEditsRestoredOnRejectedCommand(t *testing.T) {
	f := newFixture(t, Config{})
	th := f.thread(f.boxOrb("stopped", true), StateIdle)
	f.entry(th.id, "e1")
	_, err := f.s.PutDraft(f.ctx, th.id, "e1", "1.0", DraftInput{Kind: "email", Title: "t", Text: "a"})
	require.NoError(t, err)

	requireCode(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi"}`), noReply), CodeBoxStopped)
	assert.Nil(t, f.draftRow(th.id, "e1", "1.0").DeliveredAt, "从未送达的改稿回到未送达")

	// 已送达过一版、又改过：拒绝后回到上一版的送达标记
	_, marks, err := f.s.takePendingDrafts(f.ctx, th.id)
	require.NoError(t, err)
	require.Len(t, marks, 1)
	prev := f.draftRow(th.id, "e1", "1.0").DeliveredAt
	require.NotNil(t, prev)
	_, err = f.s.PutDraft(f.ctx, th.id, "e1", "1.0", DraftInput{Kind: "email", Title: "t", Text: "b"})
	require.NoError(t, err)
	requireCode(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi"}`), noReply), CodeBoxStopped)
	r := f.draftRow(th.id, "e1", "1.0")
	require.NotNil(t, r.DeliveredAt)
	assert.Equal(t, *prev, *r.DeliveredAt)
	assert.Less(t, *r.DeliveredAt, r.UpdatedAt, "仍待送达")
}

// 受理后才失败（附件投递失败经 failCmd 结束）：送达标记回滚；期间用户又改过的改稿保持待送达。
func TestDraftEditsRestoredOnDeliveryFailure(t *testing.T) {
	f, th, a := draftThread(t)
	f.s.SetAttachments(&stubAttachments{deliver: func(context.Context, []string) ([]DeliveredAttachment, error) {
		return nil, errors.New("attachment_not_found")
	}})
	_, err := f.s.PutDraft(f.ctx, th.id, "e1", "1.0", DraftInput{Kind: "email", Title: "t", Text: "a"})
	require.NoError(t, err)
	reply := make(chan json.RawMessage, 1)
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi","orynAttachments":["att_missing"]}`), func(b json.RawMessage) { reply <- b }))
	select {
	case b := <-reply:
		assert.Contains(t, string(b), `"success":false`)
	case <-time.After(5 * time.Second):
		t.Fatal("投递失败后没有合成失败应答")
	}
	assert.Nil(t, f.draftRow(th.id, "e1", "1.0").DeliveredAt)

	// 回滚后下一条消息重新附上改稿
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"retry"}`), noReply))
	assert.Contains(t, nextMessage(t, a), "<oryn-draft-edits>\n")

	// 标记后用户又改过：回滚只在 delivered_at 仍是本次标记值时生效
	_, err = f.s.PutDraft(f.ctx, th.id, "e1", "1.0", DraftInput{Kind: "email", Title: "t", Text: "b"})
	require.NoError(t, err)
	_, marks, err := f.s.takePendingDrafts(f.ctx, th.id)
	require.NoError(t, err)
	delivered := *f.draftRow(th.id, "e1", "1.0").DeliveredAt
	block, again, err := f.s.takePendingDrafts(f.ctx, th.id)
	require.NoError(t, err)
	assert.Empty(t, block, "已标记送达的改稿不再待送达")
	assert.Empty(t, again)
	_, err = f.st.DB.ExecContext(f.ctx, `UPDATE thread_drafts SET delivered_at = delivered_at + 1, updated_at = updated_at + 1`)
	require.NoError(t, err)
	f.s.restoreDrafts(f.ctx, th.id, marks)
	assert.Equal(t, delivered+1, *f.draftRow(th.id, "e1", "1.0").DeliveredAt, "已被改写的送达标记不回滚")
}
