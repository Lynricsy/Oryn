package threads

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// M8 §3.4：建线程时解析浏览器 Profile；EventDesktop 只推送 orb。

// stubProfiles 记录 ResolveForNewOrb 的入参，返回预设结果。
type stubProfiles struct {
	explicit *string
	project  string
	mode     apigen.BrowserProfileMode
	calls    int
	id       string
	err      error
}

func (p *stubProfiles) ResolveForNewOrb(_ context.Context, explicit *string, projectID string, mode apigen.BrowserProfileMode) (string, error) {
	p.calls++
	p.explicit, p.project, p.mode = explicit, projectID, mode
	return p.id, p.err
}

// conflictErr 模拟 desktop.Error：带错误码与专用响应体。
type conflictErr struct{ body any }

func (conflictErr) Error() string { return "profile_in_use" }
func (conflictErr) Coded() (string, string) {
	return "profile_in_use", "Profile「w」正在线程「T」里使用"
}
func (c conflictErr) ErrorBody() any { return c.body }

func (o *stubOrbs) createCalls() []OrbCreate {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]OrbCreate(nil), o.creates...)
}

func TestCreateThreadWithBrowserProfile(t *testing.T) {
	f := newFixture(t, Config{})
	prof := &stubProfiles{id: "bpf_A"}
	f.s.SetProfiles(prof)
	pid := "bpf_A"

	_, err := f.s.Create(f.ctx, CreateParams{Prompt: "hi", BrowserProfileID: &pid, ProfileMode: apigen.BrowserProfileModeTakeover})
	require.NoError(t, err)
	require.Len(t, f.orbs.createCalls(), 1)
	assert.Equal(t, "bpf_A", f.orbs.createCalls()[0].BrowserProfileID)
	assert.Equal(t, "takeover", f.orbs.createCalls()[0].ProfileMode)
	assert.Equal(t, &pid, prof.explicit)
	assert.Equal(t, apigen.BrowserProfileModeTakeover, prof.mode)

	// 省略：交给 Profiles 按项目默认解析（nil）
	prof.id = ""
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "hi"})
	require.NoError(t, err)
	assert.Nil(t, prof.explicit)
	assert.Empty(t, f.orbs.createCalls()[1].BrowserProfileID)

	// 挂到已有 orb 时给 Profile → invalid，不解析
	orb := f.orb("ready")
	calls := prof.calls
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "hi", OrbID: orb, BrowserProfileID: &pid})
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "invalid", te.Code)
	assert.Equal(t, calls, prof.calls)
	none := ""
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "hi", OrbID: orb, BrowserProfileID: &none})
	require.NoError(t, err, "空串（不用）可以")

	// profile_in_use：不建 orb，错误带响应体
	body := map[string]string{"code": "profile_in_use"}
	prof.err = conflictErr{body: body}
	n := len(f.orbs.createCalls())
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "hi", BrowserProfileID: &pid})
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "profile_in_use", te.Code)
	assert.Equal(t, body, te.Body)
	assert.Len(t, f.orbs.createCalls(), n, "冲突时不建 orb")
}

func TestCreateThreadProfileWithoutService(t *testing.T) {
	f := newFixture(t, Config{})
	pid := "bpf_A"
	_, err := f.s.Create(f.ctx, CreateParams{Prompt: "hi", BrowserProfileID: &pid})
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "invalid", te.Code)
	assert.Empty(t, f.orbs.createCalls())
}

func TestDesktopEventPushesOrbOnly(t *testing.T) {
	f := newFixture(t, Config{})
	sink := &orbRecSink{}
	f.s.SetSink(sink)
	orb := f.orb("ready")
	th := f.thread(orb, StateIdle)

	f.s.handleOrbEvent(f.ctx, OrbEvent{OrbID: orb, Kind: OrbEventDesktop, From: "ready", To: "ready", Seq: 1})

	sink.mu.Lock()
	defer sink.mu.Unlock()
	require.Len(t, sink.orbs, 1)
	assert.Equal(t, orb, sink.orbs[0].ID)
	assert.Empty(t, sink.index)
	row, err := f.st.Q.GetThread(f.ctx, th.id)
	require.NoError(t, err)
	assert.Equal(t, StateIdle, row.State)
	f.s.mu.Lock()
	_, cached := f.s.orbStates[orb]
	f.s.mu.Unlock()
	assert.False(t, cached, "桌面事件不写 orb 状态缓存")
}
