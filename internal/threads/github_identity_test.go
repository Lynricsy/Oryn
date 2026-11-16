package threads

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// stubAutomation 是 GitHub 桩：ResolveAutomationSlot 恒返回 slot 与 note。
type stubAutomation struct{ slot, note string }

func (a stubAutomation) ResolveAutomationSlot(context.Context, bool) (string, string, error) {
	return a.slot, a.note, nil
}

// M14 3.1：新 orb 的 GitHub 身份——自动化入口按自动化身份，其余为 user。
func TestCreateGithubIdentityAutomation(t *testing.T) {
	f := newFixture(t, Config{})
	f.s.SetGitHub(stubAutomation{slot: "agent"})
	_, err := f.s.Create(f.ctx, CreateParams{Prompt: "auto", Automation: true})
	require.NoError(t, err)
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "manual"})
	require.NoError(t, err)
	f.orbs.mu.Lock()
	defer f.orbs.mu.Unlock()
	require.Len(t, f.orbs.creates, 2)
	assert.Equal(t, "agent", f.orbs.creates[0].GithubIdentity)
	assert.Equal(t, "user", f.orbs.creates[1].GithubIdentity)
}

// M14 3.1：orb 内派生的子线程沿用父线程 orb 的身份，即使不带 Automation。
func TestCreateGithubIdentityInheritsParent(t *testing.T) {
	f := newFixture(t, Config{})
	f.s.SetGitHub(stubAutomation{slot: "user"})
	parentOrb := f.orb("ready")
	ident := "agent"
	prov, err := f.st.Q.GetOrb(f.ctx, parentOrb)
	require.NoError(t, err)
	agentOrb := ids.Orb()
	require.NoError(t, f.st.Q.CreateOrb(f.ctx, db.CreateOrbParams{ID: agentOrb, ProviderID: prov.ProviderID, Type: "medium", Image: "test",
		State: "ready", StateChangedAt: 1, TokenHash: ids.Hash("oot_" + agentOrb), CreatedAt: 1, GithubIdentity: &ident}))
	parent := f.thread(agentOrb, StateIdle)

	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "child", ParentThreadID: parent.id})
	require.NoError(t, err)
	f.orbs.mu.Lock()
	defer f.orbs.mu.Unlock()
	require.Len(t, f.orbs.creates, 1)
	assert.Equal(t, "agent", f.orbs.creates[0].GithubIdentity)
}

// M15 1.6：Agent 账号不可用、全自动线程回退到我的账号时，回退提示追加在首条提示词末尾（首行不变）。
func TestCreateGithubIdentityFallbackNote(t *testing.T) {
	f := newFixture(t, Config{})
	f.s.SetGitHub(stubAutomation{slot: "user", note: "[fallback]"})
	th, err := f.s.Create(f.ctx, CreateParams{Prompt: "[first line]\nauto", Automation: true})
	require.NoError(t, err)
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "manual"})
	require.NoError(t, err)
	f.orbs.mu.Lock()
	require.Len(t, f.orbs.creates, 2)
	assert.Equal(t, "user", f.orbs.creates[0].GithubIdentity)
	f.orbs.mu.Unlock()
	row, err := f.st.Q.GetThread(f.ctx, th.ID)
	require.NoError(t, err)
	assert.Contains(t, string(row.StartupFrames), `"message":"[first line]\nauto\n\n[fallback]"`)
}

// recordingAutomation 记下 ResolveAutomationSlot 收到的 untrusted；err 非 nil 时返回它。
type recordingAutomation struct {
	untrusted []bool
	err       error
}

func (a *recordingAutomation) ResolveAutomationSlot(_ context.Context, untrusted bool) (string, string, error) {
	a.untrusted = append(a.untrusted, untrusted)
	if a.err != nil {
		return "", "", a.err
	}
	return "agent", "", nil
}

// M15 Q6：Untrusted 原样传给 ResolveAutomationSlot；它报错（Agent 账号不可用）时不建 orb、不建线程，错误原样返回。
func TestCreateGithubIdentityUntrusted(t *testing.T) {
	f := newFixture(t, Config{})
	gh := &recordingAutomation{}
	f.s.SetGitHub(gh)
	_, err := f.s.Create(f.ctx, CreateParams{Prompt: "auto", Automation: true, Untrusted: true})
	require.NoError(t, err)
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "auto", Automation: true})
	require.NoError(t, err)
	assert.Equal(t, []bool{true, false}, gh.untrusted)

	agentRequired := errors.New("agent required")
	gh.err = agentRequired
	_, err = f.s.Create(f.ctx, CreateParams{Prompt: "auto", Automation: true, Untrusted: true})
	require.ErrorIs(t, err, agentRequired)
	f.orbs.mu.Lock()
	defer f.orbs.mu.Unlock()
	assert.Len(t, f.orbs.creates, 2)
}

// M15 R14：SetModel 以 set_model + set_thinking_level 下发；零值模型与空级别各自跳过。
func TestSetModelFrames(t *testing.T) {
	f := newFixture(t, Config{})
	th := f.thread(f.orb("ready"), StateIdle)
	a := fakeAgent(8)
	attach(th, a, apigen.AgentStatusReady)

	require.NoError(t, f.s.SetModel(f.ctx, th.id, apigen.ModelRef{Provider: "anthropic", ID: "claude-opus"}, "high"))
	frames := sent(a)
	require.Len(t, frames, 2)
	assert.Contains(t, frames[0], `"type":"set_model"`)
	assert.Contains(t, frames[0], `"provider":"anthropic"`)
	assert.Contains(t, frames[0], `"modelId":"claude-opus"`)
	assert.Contains(t, frames[1], `"type":"set_thinking_level"`)
	assert.Contains(t, frames[1], `"level":"high"`)

	require.NoError(t, f.s.SetModel(f.ctx, th.id, apigen.ModelRef{}, "low"))
	assert.Equal(t, []string{"set_thinking_level"}, typesOf(sent(a)))
	require.NoError(t, f.s.SetModel(f.ctx, th.id, apigen.ModelRef{}, ""))
	assert.Empty(t, sent(a))
}

func typesOf(frames []string) []string {
	var out []string
	for _, f := range frames {
		if typ, _, ok := frameHeader(json.RawMessage(f)); ok {
			out = append(out, typ)
		}
	}
	return out
}
