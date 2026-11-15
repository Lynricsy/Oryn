package threads

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// fakeLibrary 是测试用的 LibraryFiles：按对话目录给出沙箱路径，记录每次绑定；missing 中的 id 返回 not_found。
type fakeLibrary struct {
	binds   []fakeBind
	missing map[string]bool
}

type fakeBind struct {
	threadID string
	ids      []string
	inTx     bool
}

func (l *fakeLibrary) BindTx(_ context.Context, q *db.Queries, threadID string, ids []string) ([]DeliveredAttachment, error) {
	l.binds = append(l.binds, fakeBind{threadID: threadID, ids: ids, inTx: q != nil})
	out := make([]DeliveredAttachment, 0, len(ids))
	for _, id := range ids {
		if l.missing[id] {
			return nil, errf("not_found", "library: file not found: "+id)
		}
		out = append(out, DeliveredAttachment{ID: id, Name: "a.txt", Mime: "text/plain", Size: 3,
			Path: "~/library/threads/" + threadID + "/" + id + "/a.txt"})
	}
	return out, nil
}

func frameMessage(t *testing.T, frame string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(frame), &m))
	return m
}

// 助手对话的 prompt 带 orynFiles：绑定到本对话，下发的消息末尾是沙箱路径的附件块，帧上不再有 orynFiles。
func TestAssistantPromptWithLibraryFiles(t *testing.T) {
	f := newFixture(t, Config{})
	lib := &fakeLibrary{}
	f.s.SetLibrary(lib)
	th := f.coordThread(f.orb("ready"), KindAssistant, StateIdle)
	a := fakeAgent(16)
	attach(th, a, apigen.AgentStatusReady)

	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"看看这个","orynFiles":["afl_1","afl_2"]}`), noReply))
	m := frameMessage(t, recvFrame(t, a))
	assert.Equal(t, "prompt", m["type"])
	assert.NotContains(t, m, orynFilesKey)
	assert.Equal(t, "看看这个\n\n"+attachmentBlockHead+
		"- ~/library/threads/"+th.id+"/afl_1/a.txt (text/plain, 3 bytes)\n"+
		"- ~/library/threads/"+th.id+"/afl_2/a.txt (text/plain, 3 bytes)\n"+
		"</oryn-attachments>", m["message"])
	require.Len(t, lib.binds, 1)
	assert.Equal(t, fakeBind{threadID: th.id, ids: []string{"afl_1", "afl_2"}, inTx: true}, lib.binds[0])

	// steer / follow_up 同样接受；文件不存在 → not_found，不下发
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"follow_up","message":"再看","orynFiles":["afl_3"]}`), noReply))
	assert.Contains(t, frameMessage(t, recvFrame(t, a))["message"], "~/library/threads/"+th.id+"/afl_3/a.txt")
	lib.missing = map[string]bool{"afl_x": true}
	err := f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"x","orynFiles":["afl_x"]}`), noReply)
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "not_found", te.Code)
	assert.Empty(t, sent(a))
}

// orynAttachments 在助手对话上仍然拒绝；Space 聊天不接受 orynFiles；工作线程不接受 orynFiles；
// 非消息命令、重复 id 都是非法帧。
func TestLibraryFilesRestrictions(t *testing.T) {
	f := newFixture(t, Config{})
	f.s.SetLibrary(&fakeLibrary{})
	orb := f.orb("ready")
	asst := f.coordThread(orb, KindAssistant, StateIdle)
	attach(asst, fakeAgent(16), apigen.AgentStatusReady)
	space := f.coordThread(orb, KindSpace, StateIdle)
	attach(space, fakeAgent(16), apigen.AgentStatusReady)
	work := f.thread(orb, StateIdle)
	attach(work, fakeAgent(16), apigen.AgentStatusReady)

	for _, c := range []struct {
		thread *thread
		frame  string
		code   string
	}{
		{asst, `{"type":"prompt","message":"x","orynAttachments":["att_1"]}`, CodeCoordinatorRestricted},
		{asst, `{"type":"prompt","message":"x","orynAttachments":["att_1"],"orynFiles":["afl_1"]}`, CodeCoordinatorRestricted},
		{space, `{"type":"prompt","message":"x","orynFiles":["afl_1"]}`, CodeCoordinatorRestricted},
		{work, `{"type":"prompt","message":"x","orynFiles":["afl_1"]}`, "invalid"},
		{asst, `{"type":"set_session_name","name":"x","orynFiles":["afl_1"]}`, RouteInvalidFrame},
		{asst, `{"type":"compact","orynFiles":["afl_1"]}`, RouteInvalidFrame},
		{asst, `{"type":"prompt","message":"x","orynFiles":["afl_1","afl_1"]}`, "invalid"},
		{asst, `{"type":"prompt","message":"x","orynFiles":"afl_1"}`, RouteInvalidFrame},
	} {
		err := f.s.Command(f.ctx, c.thread.id, json.RawMessage(c.frame), noReply)
		var te *Error
		require.ErrorAs(t, err, &te, c.frame)
		assert.Equal(t, c.code, te.Code, c.frame)
	}
}

// 新建助手对话带首条消息：文件在建线程的事务里绑定到新对话，首条 prompt（提示词 + 沙箱路径块）随线程行落库，
// agent 就绪后最先下发。
func TestCreateCoordinatorWithFirstMessage(t *testing.T) {
	f := newFixture(t, Config{})
	lib := &fakeLibrary{}
	f.s.SetLibrary(lib)
	orb := f.orb("ready")
	out, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{
		Kind: KindAssistant, OrbID: orb, Model: "p/m", ThinkingLevel: "high", Prompt: "帮我总结", Files: []string{"afl_1"},
		Images: []apigen.ImageContent{{Type: "image", Data: "aGk=", MimeType: "image/png"}},
	})
	require.NoError(t, err)
	require.Len(t, lib.binds, 1)
	assert.Equal(t, fakeBind{threadID: out.ID, ids: []string{"afl_1"}, inTx: true}, lib.binds[0])
	row, err := f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	require.NotNil(t, row.ThinkingLevel)
	assert.Equal(t, "high", *row.ThinkingLevel)
	require.NotEmpty(t, row.StartupFrames)

	th, err := f.s.get(f.ctx, out.ID)
	require.NoError(t, err)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	th.mu.Lock()
	f.s.onReadyLocked(f.ctx, th)
	th.mu.Unlock()
	frames := afterMark(t, sent(a))
	require.NotEmpty(t, frames)
	m := frameMessage(t, frames[0])
	assert.Equal(t, "prompt", m["type"])
	assert.Equal(t, "帮我总结\n\n"+attachmentBlockHead+"- ~/library/threads/"+out.ID+"/afl_1/a.txt (text/plain, 3 bytes)\n</oryn-attachments>", m["message"])
	assert.Len(t, m["images"], 1)
}

func TestCreateCoordinatorFirstMessageRules(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")

	// 不带首条消息：与以前一样没有首批命令
	out, err := f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb})
	require.NoError(t, err)
	row, err := f.st.Q.GetThread(f.ctx, out.ID)
	require.NoError(t, err)
	assert.Empty(t, row.StartupFrames)

	// 文件库未装配时不接受 files；Space 聊天不接受首条消息
	var te *Error
	_, err = f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Files: []string{"afl_1"}})
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "invalid", te.Code)
	_, err = f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindSpace, OrbID: orb, Prompt: "x"})
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "invalid", te.Code)

	// 绑定失败：整个创建回滚，不留下线程
	lib := &fakeLibrary{missing: map[string]bool{"afl_x": true}}
	f.s.SetLibrary(lib)
	before, err := f.st.Q.ListThreadsByOrb(f.ctx, &orb)
	require.NoError(t, err)
	_, err = f.s.CreateCoordinator(f.ctx, CoordinatorParams{Kind: KindAssistant, OrbID: orb, Prompt: "x", Files: []string{"afl_x"}})
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "not_found", te.Code)
	after, err := f.st.Q.ListThreadsByOrb(f.ctx, &orb)
	require.NoError(t, err)
	assert.Len(t, after, len(before))
}
