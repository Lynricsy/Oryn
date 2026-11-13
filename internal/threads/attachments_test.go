package threads

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func TestTakeAttachments(t *testing.T) {
	ids, rest, err := takeAttachments("prompt", json.RawMessage(`{"type":"prompt","message":"hi","orynAttachments":["att_A","att_B"]}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"att_A", "att_B"}, ids)
	assert.JSONEq(t, `{"type":"prompt","message":"hi"}`, string(rest), "omp 看不到 orynAttachments")

	// 空数组：照样删除字段，走普通路径
	ids, rest, err = takeAttachments("steer", json.RawMessage(`{"type":"steer","message":"x","orynAttachments":[]}`))
	require.NoError(t, err)
	assert.Empty(t, ids)
	assert.JSONEq(t, `{"type":"steer","message":"x"}`, string(rest))

	frame := json.RawMessage(`{"type":"prompt","message":"x"}`)
	ids, rest, err = takeAttachments("prompt", frame)
	require.NoError(t, err)
	assert.Nil(t, ids)
	assert.Equal(t, string(frame), string(rest))

	for _, bad := range []string{
		`{"type":"get_state","orynAttachments":["att_A"]}`,
		`{"type":"prompt","message":"x","orynAttachments":"att_A"}`,
		`{"type":"prompt","message":"x","orynAttachments":["att_A","att_A"]}`,
	} {
		var typ struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(bad), &typ)
		_, _, err := takeAttachments(typ.Type, json.RawMessage(bad))
		assert.Error(t, err, bad)
	}
}

func TestAppendAttachmentBlock(t *testing.T) {
	block := attachmentBlock([]DeliveredAttachment{{ID: "att_A", Name: "a b.pdf", Mime: "application/pdf", Size: 12, Path: "/home/user/.oryn/attachments/T-1/att_A/a b.pdf"}})
	out, err := appendMessage([]byte(`{"type":"prompt","message":"look","images":[]}`), block)
	require.NoError(t, err)
	var f struct {
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(out, &f))
	assert.Equal(t, "look\n\n<oryn-attachments>\n"+
		"The user attached these files. They are stored outside the repository; read them when relevant and do not copy them into the repository unless asked.\n"+
		"- /home/user/.oryn/attachments/T-1/att_A/a b.pdf (application/pdf, 12 bytes)\n"+
		"</oryn-attachments>", f.Message)

	// 只有附件、没有文字：不留前导空行
	out, err = appendMessage([]byte(`{"type":"prompt"}`), block)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(out, &f))
	assert.Equal(t, "<oryn-attachments>\n", f.Message[:len("<oryn-attachments>\n")])
}

// stubAttachments 的 Deliver 交给 deliver（按附件 id 决定行为）；其余操作恒成功。
type stubAttachments struct {
	deliver func(ctx context.Context, ids []string) ([]DeliveredAttachment, error)
}

func (*stubAttachments) Check(context.Context, []string) error                       { return nil }
func (*stubAttachments) BindTx(context.Context, *db.Queries, string, []string) error { return nil }
func (*stubAttachments) DeleteThread(context.Context, string) error                  { return nil }
func (a *stubAttachments) Deliver(ctx context.Context, _, _ string, ids []string) ([]DeliveredAttachment, error) {
	return a.deliver(ctx, ids)
}

// recvFrame 等 agent 流上的下一帧。
func recvFrame(t *testing.T, a *agentConn) string {
	t.Helper()
	select {
	case b := <-a.out:
		return string(b)
	case <-time.After(5 * time.Second):
		t.Fatal("agent 流上没有等到命令")
		return ""
	}
}

// 投递失败的附件消息不得让空闲线程永远不能静止（冻结 / 停机）。
func TestFailedAttachmentDeliveryAllowsQuiesce(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	f.s.SetAttachments(&stubAttachments{deliver: func(context.Context, []string) ([]DeliveredAttachment, error) {
		return nil, errors.New("attachment_not_found")
	}})
	reply := make(chan json.RawMessage, 1)
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"hi","orynAttachments":["att_missing"]}`), func(b json.RawMessage) { reply <- b }))
	select {
	case b := <-reply:
		assert.Contains(t, string(b), `"success":false`)
	case <-time.After(5 * time.Second):
		t.Fatal("投递失败后没有合成失败应答")
	}
	assert.Empty(t, sent(a))
	assert.True(t, f.s.TryQuiesce(orb, func() bool { return true }), "投递失败后空闲线程必须可以静止")
}

// 服务关闭取消首条提示词的附件投递：不是投递失败，不得发出不带附件的降级提示词，持久化的首批命令留待重启续作。
func TestStartupAttachmentShutdownKeepsStartupFrames(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	begun := make(chan struct{})
	f.s.SetAttachments(&stubAttachments{deliver: func(ctx context.Context, _ []string) ([]DeliveredAttachment, error) {
		close(begun)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	frame := json.RawMessage(`{"type":"prompt","message":"read my document","orynAttachments":["att_pending"]}`)
	require.NoError(t, f.st.Q.SetThreadStartupFrames(f.ctx, db.SetThreadStartupFramesParams{ID: th.id, StartupFrames: startupFrames(frame)}))
	th.mu.Lock()
	f.s.queueStartupFrameLocked(th, frame, false)
	th.mu.Unlock()
	<-begun
	f.s.Close()
	assert.Empty(t, sent(a), "关闭时不得发出降级提示词")
	row, err := f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	assert.NotEmpty(t, row.StartupFrames, "首批命令须保留到重启后续作")
	th.mu.Lock()
	assert.Zero(t, th.deliveries)
	th.mu.Unlock()
}

// 服务关闭取消客户端附件消息的投递：发起方收到失败应答（不会悬而不决），也不发出不带附件的消息。
func TestAttachmentShutdownFailsClientCommand(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	begun := make(chan struct{})
	f.s.SetAttachments(&stubAttachments{deliver: func(ctx context.Context, _ []string) ([]DeliveredAttachment, error) {
		close(begun)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	reply := make(chan json.RawMessage, 1)
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"x","orynAttachments":["att_A"]}`), func(b json.RawMessage) { reply <- b }))
	<-begun
	f.s.Close()
	select {
	case b := <-reply:
		assert.Contains(t, string(b), `"success":false`)
	default:
		t.Fatal("关闭后发起方没有收到失败应答")
	}
	assert.Empty(t, sent(a))
}

// 带附件的消息在受理时占位：其后受理的消息不得越过它；终端输入等非消息命令不等它。
func TestAttachmentMessageKeepsAcceptanceOrder(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	begun, release := make(chan struct{}), make(chan struct{})
	f.s.SetAttachments(&stubAttachments{deliver: func(context.Context, []string) ([]DeliveredAttachment, error) {
		close(begun)
		<-release
		return []DeliveredAttachment{{ID: "att_A", Path: "/doc"}}, nil
	}})
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"first","orynAttachments":["att_A"]}`), nil))
	<-begun
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"follow_up","message":"second"}`), nil))
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"terminal_input","terminalId":"term-1","data":"ls\r"}`), nil))
	assert.Contains(t, recvFrame(t, a), `"terminal_input"`, "非消息命令不等附件投递")
	assert.Empty(t, sent(a), "附件投递完成前其后的消息不得下发")
	th.mu.Lock()
	assert.False(t, th.quiescentLocked(), "排队等待的消息让线程不静止")
	th.mu.Unlock()
	close(release)
	assert.Contains(t, recvFrame(t, a), `"message":"first`)
	assert.Contains(t, recvFrame(t, a), `"message":"second"`)
}

// 多条带附件的消息按受理顺序下发，与投递完成的先后无关。
func TestAttachmentMessagesDispatchInAcceptanceOrder(t *testing.T) {
	f := newFixture(t, Config{})
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)
	releaseA := make(chan struct{})
	doneB := make(chan struct{})
	f.s.SetAttachments(&stubAttachments{deliver: func(_ context.Context, ids []string) ([]DeliveredAttachment, error) {
		if ids[0] == "att_A" {
			<-releaseA
		} else {
			defer close(doneB)
		}
		return []DeliveredAttachment{{ID: ids[0], Path: "/" + ids[0]}}, nil
	}})
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"prompt","message":"first","orynAttachments":["att_A"]}`), nil))
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"follow_up","message":"second","orynAttachments":["att_B"]}`), nil))
	<-doneB
	// 第二条的投递已完成，但第一条仍在投递：第二条不得先发
	require.Eventually(t, func() bool {
		th.mu.Lock()
		defer th.mu.Unlock()
		return th.deliveries == 1
	}, 5*time.Second, 5*time.Millisecond)
	assert.Empty(t, sent(a))
	close(releaseA)
	assert.Contains(t, recvFrame(t, a), `"message":"first`)
	assert.Contains(t, recvFrame(t, a), `"message":"second`)
}
