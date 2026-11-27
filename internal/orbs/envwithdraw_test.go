package orbs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/aiconsent"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const groqEnv = "ORYN_MODEL_KEY_GROQ"

// envFixture 是收回环境变量降级密钥测试的夹具：真实的库、密钥库、omp-home 与 DefaultSetup，models.yml 里有一个以
// 环境变量降级的 provider（groq：有密钥、没有 baseUrl、不在内置表里），一台已 ready 的工作 orb 与归属它的一个线程。
type envFixture struct {
	h      *harness
	drv    *fakeDriver
	orb    db.Orb
	o      *fakeOrbd
	legacy *atomic.Bool
}

// newEnvFixture 建好夹具。legacy 为真时同意判定一律放行，模拟 a024f3fc 之前不看同意就注入的旧版本；consented 为真时
// 主人已同意 model:groq。
func newEnvFixture(t *testing.T, legacy, consented bool) *envFixture {
	t.Helper()
	ctx := context.Background()
	drv := newFakeDriver()
	d := &DefaultSetup{}
	h := newHarnessRun(t, drv, d, nil, false)
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	vault := secrets.NewVault(h.st, box)
	home, err := agentcfg.NewHome(t.TempDir())
	require.NoError(t, err)
	_, err = home.Put(ctx, agentcfg.ModelsFile, []byte("providers:\n  groq:\n    apiKey: gsk-literal-0123456789abcdef\n"), 0, vault)
	require.NoError(t, err)
	require.NoError(t, h.st.Q.CreateOwner(ctx, db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
	if consented {
		at := int64(1)
		_, err = h.st.Q.SetOwnerAiConsent(ctx, db.SetOwnerAiConsentParams{ConsentedAt: &at, Recipients: `["model:groq"]`})
		require.NoError(t, err)
	}
	f := &envFixture{h: h, drv: drv, legacy: &atomic.Bool{}}
	f.legacy.Store(legacy)
	gate := aiconsent.NewGate(h.st.Q)
	d.Store, d.Home, d.Vault = h.st, home, vault
	d.AIConsent = aiconsent.CheckerFunc(func(ctx context.Context, recipient string) error {
		if f.legacy.Load() {
			return nil
		}
		return gate.Check(ctx, recipient)
	})

	f.orb, err = h.svc.Create(ctx, CreateParams{})
	require.NoError(t, err)
	h.waitState(f.orb.ID, StateReady, 10*time.Second)
	f.o = drv.orbd(f.orb.ID)
	require.NotNil(t, f.o)
	require.NoError(t, h.st.Q.CreateThread(ctx, db.CreateThreadParams{
		Kind: "work", ID: "T-env", OrbID: &f.orb.ID, TitleSource: "auto", State: "idle", SessionDir: "/s/T-env", CreatedAt: 1, UpdatedAt: 1,
	}))
	return f
}

// setupParams 返回假 orbd 收到的全部 method（setup.run / resume.run）参数。
func (f *envFixture) setupParams(t *testing.T, method string) []apigen.LinkCtlSetupRunParams {
	t.Helper()
	f.o.mu.Lock()
	defer f.o.mu.Unlock()
	var out []apigen.LinkCtlSetupRunParams
	for _, c := range f.o.calls {
		if c.method == method {
			var p apigen.LinkCtlSetupRunParams
			require.NoError(t, json.Unmarshal(c.params, &p))
			out = append(out, p)
		}
	}
	return out
}

// stoppedThreads 返回假 orbd 收到的 agent.stop 的线程。
func (f *envFixture) stoppedThreads(t *testing.T) []string {
	t.Helper()
	f.o.mu.Lock()
	defer f.o.mu.Unlock()
	var out []string
	for _, c := range f.o.calls {
		if c.method == "agent.stop" {
			var p apigen.LinkCtlAgentStopParams
			require.NoError(t, json.Unmarshal(c.params, &p))
			out = append(out, p.ThreadID)
		}
	}
	return out
}

// forgetHeld 清掉本进程记下的「已下发的降级密钥」，模拟 orynd 重启：新进程不知道旧进程往 orbd 环境里放过什么。
func (f *envFixture) forgetHeld() {
	f.h.svc.mu.Lock()
	clear(f.h.svc.envHeld)
	f.h.svc.mu.Unlock()
}

// assertWithdrawn 断言 orb 已重走 resume：最后一次 resume.run 的环境与 models.yml 都不含降级密钥，agent.stop 停了线程，
// 且排在这次 resume.run 之后（新环境生效之后才重启 agent）。
func (f *envFixture) assertWithdrawn(t *testing.T) {
	t.Helper()
	resumes := f.setupParams(t, "resume.run")
	require.NotEmpty(t, resumes)
	last := resumes[len(resumes)-1]
	assert.NotContains(t, last.Env, groqEnv, "重新下发的环境里没有被扣下的密钥")
	for _, file := range last.OmpHome {
		if file.Path == agentcfg.ModelsFile {
			b, err := base64.StdEncoding.DecodeString(file.ContentB64)
			require.NoError(t, err)
			assert.NotContains(t, string(b), groqEnv, "models.yml 不再引用降级的环境变量")
			assert.NotContains(t, string(b), "gsk-literal", "models.yml 不含明文密钥")
		}
	}
	assert.Equal(t, []string{"T-env"}, f.stoppedThreads(t))
	methods := f.o.methods()
	assert.Greater(t, slices.Index(methods, "agent.stop"), slices.Index(methods, "resume.run"), "agent 在新环境生效之后才停：%v", methods)
}

func countEvent(seq []string, ev string) int {
	n := 0
	for _, e := range seq {
		if e == ev {
			n++
		}
	}
	return n
}

// TestWithdrawEnvKeysAtStartup：升级前的旧版本不看同意就把降级密钥注入了 orb。升级后 orynd 启动时 link 都还没连上：
// 启动对账之后记下待收回，orbd 重连 hello 时 ready 的 orb 退回 connecting 重走 resume，新环境里没有密钥，随后停掉其上的
// agent；之后再调用不会再动它（本进程已按新同意下发过）。
func TestWithdrawEnvKeysAtStartup(t *testing.T) {
	ctx := context.Background()
	f := newEnvFixture(t, true, false)
	setups := f.setupParams(t, "setup.run")
	require.Len(t, setups, 1)
	require.Equal(t, "gsk-literal-0123456789abcdef", setups[0].Env[groqEnv], "旧版本注入了降级的密钥")

	// 升级：控制面重启（link 断开、内存状态清空），新版本按同意判定
	f.legacy.Store(false)
	f.forgetHeld()
	f.o.disconnect()
	require.Eventually(t, func() bool { return !f.h.svc.Connected(f.orb.ID) }, 5*time.Second, 20*time.Millisecond)
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go f.h.svc.Run(runCtx)
	require.Eventually(t, func() bool { return len(f.h.svc.pendingEnvWithdraw(f.orb.ID)) > 0 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, []string{"groq"}, f.h.svc.pendingEnvWithdraw(f.orb.ID))
	assert.Equal(t, StateReady, f.h.waitState(f.orb.ID, StateReady, time.Second).State, "link 不在时不改状态（不触发 connect 超时）")
	assert.Empty(t, f.stoppedThreads(t))

	// orbd 重连
	require.NoError(t, f.o.connect())
	require.Eventually(t, func() bool {
		seq := f.h.eventSeq(f.orb.ID)
		return countEvent(seq, "ready>connecting") == 1 && countEvent(seq, "resuming>ready") == 1
	}, 10*time.Second, 20*time.Millisecond, "events: %v", f.h.eventSeq(f.orb.ID))
	f.assertWithdrawn(t)
	assert.Empty(t, f.h.svc.pendingEnvWithdraw(f.orb.ID))

	// 已按新同意下发过：再次收回（如之后的同意变化）不再动它
	f.h.svc.WithdrawEnvKeys(ctx, "consent")
	assert.Equal(t, 1, countEvent(f.h.eventSeq(f.orb.ID), "ready>connecting"))
	assert.Empty(t, f.h.svc.pendingEnvWithdraw(f.orb.ID))
}

// TestWithdrawEnvKeysConsented：主人已同意 model:groq 时启动收回什么也不做：不改状态、不停 agent。
func TestWithdrawEnvKeysConsented(t *testing.T) {
	ctx := context.Background()
	f := newEnvFixture(t, false, true)
	setups := f.setupParams(t, "setup.run")
	require.Len(t, setups, 1)
	require.Equal(t, "gsk-literal-0123456789abcdef", setups[0].Env[groqEnv])

	f.forgetHeld()
	f.h.svc.recover(ctx)
	f.h.svc.WithdrawEnvKeys(ctx, "startup")
	assert.Empty(t, f.h.svc.pendingEnvWithdraw(f.orb.ID))
	assert.Equal(t, 0, countEvent(f.h.eventSeq(f.orb.ID), "ready>connecting"))
	assert.Empty(t, f.setupParams(t, "resume.run"))
	assert.Empty(t, f.stoppedThreads(t))
	assert.Equal(t, StateReady, f.h.waitState(f.orb.ID, StateReady, time.Second).State)
}

// TestWithdrawEnvKeysOnRevoke：已同意、orb 带着降级密钥在跑；撤回（DELETE ai-data-consent 调用 WithdrawEnvKeys）后 orb
// 立即经现有 link 重走 resume，新环境里没有密钥，其上的 agent 被停掉。撤回之前（同意不变）调用不动它。
func TestWithdrawEnvKeysOnRevoke(t *testing.T) {
	ctx := context.Background()
	f := newEnvFixture(t, false, true)
	require.Equal(t, "gsk-literal-0123456789abcdef", f.setupParams(t, "setup.run")[0].Env[groqEnv])

	f.h.svc.WithdrawEnvKeys(ctx, "consent")
	assert.Equal(t, 0, countEvent(f.h.eventSeq(f.orb.ID), "ready>connecting"), "同意仍在：不动")

	_, err := f.h.st.Q.ClearOwnerAiConsent(ctx)
	require.NoError(t, err)
	f.h.svc.WithdrawEnvKeys(ctx, "consent")
	require.Eventually(t, func() bool {
		seq := f.h.eventSeq(f.orb.ID)
		return countEvent(seq, "ready>connecting") == 1 && countEvent(seq, "resuming>ready") == 1
	}, 10*time.Second, 20*time.Millisecond, "events: %v", f.h.eventSeq(f.orb.ID))
	f.assertWithdrawn(t)
	evs, err := f.h.st.Q.ListOrbEvents(ctx, f.orb.ID)
	require.NoError(t, err)
	var reason string
	for _, e := range evs {
		if e.FromState == StateReady && e.ToState == StateConnecting {
			reason = e.Detail
		}
	}
	assert.Contains(t, reason, envWithdrawReason)
	assert.Contains(t, reason, `"groq"`)
}

// TestWithdrawEnvKeysStopFailureHoldsReady：撤回后重走 resume，但 link 一直在线、agent.stop 返回错误（agent 可能还带着
// 旧密钥在跑）：orb 不进 ready、待收回状态不消费，按退避重试 agent.stop；停下之后才进入 ready。
func TestWithdrawEnvKeysStopFailureHoldsReady(t *testing.T) {
	retry, retryMax := envAgentStopRetry, envAgentStopRetryMax
	envAgentStopRetry, envAgentStopRetryMax = 20*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { envAgentStopRetry, envAgentStopRetryMax = retry, retryMax })

	ctx := context.Background()
	f := newEnvFixture(t, false, true)
	var stopFails atomic.Bool
	stopFails.Store(true)
	f.o.mu.Lock()
	f.o.fail = func(method string) error {
		if method == "agent.stop" && stopFails.Load() {
			return errors.New("agent did not exit")
		}
		return nil
	}
	f.o.mu.Unlock()

	_, err := f.h.st.Q.ClearOwnerAiConsent(ctx)
	require.NoError(t, err)
	f.h.svc.WithdrawEnvKeys(ctx, "consent")
	require.Eventually(t, func() bool { return len(f.stoppedThreads(t)) >= 3 }, 10*time.Second, 10*time.Millisecond, "agent.stop 失败后重试")
	assert.True(t, f.h.svc.Connected(f.orb.ID), "link 一直在线")
	seq := f.h.eventSeq(f.orb.ID)
	assert.Equal(t, 1, countEvent(seq, "ready>connecting"))
	assert.Zero(t, countEvent(seq, "resuming>ready"), "agent 没停下之前不进 ready：%v", seq)
	got, err := f.h.svc.Get(ctx, f.orb.ID)
	require.NoError(t, err)
	assert.Equal(t, StateResuming, got.State)

	stopFails.Store(false)
	require.Eventually(t, func() bool { return countEvent(f.h.eventSeq(f.orb.ID), "resuming>ready") == 1 }, 10*time.Second, 10*time.Millisecond,
		"events: %v", f.h.eventSeq(f.orb.ID))
	assert.Empty(t, f.h.svc.pendingEnvWithdraw(f.orb.ID))
	assert.Len(t, f.setupParams(t, "resume.run"), 1, "只重试停止，不重走 resume")
	assert.NotContains(t, f.setupParams(t, "resume.run")[0].Env, groqEnv)
}
