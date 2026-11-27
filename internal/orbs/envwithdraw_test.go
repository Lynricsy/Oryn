package orbs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	vault  *secrets.Vault
	orb    db.Orb
	o      *fakeOrbd
	legacy *atomic.Bool
}

// newEnvFixture 建好夹具。legacy 为真时同意判定（setup 与控制面）一律放行，模拟 a024f3fc 之前不看同意就注入的旧版本；
// consented 为真时主人已同意 model:groq。
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
		grantGroq(t, h)
	}
	f := &envFixture{h: h, drv: drv, vault: vault, legacy: &atomic.Bool{}}
	f.legacy.Store(legacy)
	gate := aiconsent.NewGate(h.st.Q)
	d.Store, d.Home, d.Vault = h.st, home, vault
	d.AIConsent = aiconsent.CheckerFunc(func(ctx context.Context, recipient string) error {
		if f.legacy.Load() {
			return nil
		}
		return gate.Check(ctx, recipient)
	})
	h.svc.SetAIConsent(d.AIConsent)

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

func grantGroq(t *testing.T, h *harness) {
	t.Helper()
	at := time.Now().UnixMilli()
	_, err := h.st.Q.SetOwnerAiConsent(context.Background(), db.SetOwnerAiConsentParams{ConsentedAt: &at, Recipients: `["model:groq"]`})
	require.NoError(t, err)
}

func (f *envFixture) revoke(t *testing.T) {
	t.Helper()
	_, err := f.h.st.Q.ClearOwnerAiConsent(context.Background())
	require.NoError(t, err)
}

// held 返回 orbs.env_fallback（nil = NULL，不知道）。
func (f *envFixture) held(t *testing.T) *string {
	t.Helper()
	orb, err := f.h.st.Q.GetOrb(context.Background(), f.orb.ID)
	require.NoError(t, err)
	return orb.EnvFallback
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

// waitResumedReady 等到 orb 恰好 n 次 resuming → ready。
func (f *envFixture) waitResumedReady(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return countEvent(f.h.eventSeq(f.orb.ID), "resuming>ready") == n },
		10*time.Second, 20*time.Millisecond, "events: %v", f.h.eventSeq(f.orb.ID))
}

// assertWithdrawn 断言 orb 已重走 resume：最后一次 resume.run 的环境与 models.yml 都不含降级密钥，agent.stop 停了线程，
// 且排在这次 resume.run 之后（新环境生效之后才重启 agent）；env_fallback 收窄为空。
func (f *envFixture) assertWithdrawn(t *testing.T) {
	t.Helper()
	resumes := f.setupParams(t, "resume.run")
	require.NotEmpty(t, resumes)
	last := resumes[len(resumes)-1]
	assert.NotContains(t, last.Env, groqEnv, "重新下发的环境里没有不被同意的密钥")
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
	lastOf := func(m string) int {
		for i := len(methods) - 1; i >= 0; i-- {
			if methods[i] == m {
				return i
			}
		}
		return -1
	}
	assert.Greater(t, lastOf("agent.stop"), lastOf("resume.run"), "agent 在新环境生效之后才停：%v", methods)
	require.NotNil(t, f.held(t))
	assert.Equal(t, "[]", *f.held(t))
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

// TestWithdrawEnvKeysAtStartup：升级前的旧版本不看同意就把降级密钥注入了 orb，迁移把 env_fallback 置为 NULL（不知道）。
// 升级后 orynd 启动时 link 都还没连上：不动状态；orbd 重连 hello 时 ready 的 orb 退回 connecting 重走 resume，新环境
// 里没有密钥，随后停掉其上的 agent，env_fallback 变为已知的空；之后再调用不会再动它。
func TestWithdrawEnvKeysAtStartup(t *testing.T) {
	ctx := context.Background()
	f := newEnvFixture(t, true, false)
	setups := f.setupParams(t, "setup.run")
	require.Len(t, setups, 1)
	require.Equal(t, "gsk-literal-0123456789abcdef", setups[0].Env[groqEnv], "旧版本注入了降级的密钥")

	// 升级：控制面重启（link 断开），迁移 0075 把已有 orb 记为不知道，新版本按同意判定
	f.legacy.Store(false)
	require.NoError(t, f.h.st.Q.SetOrbEnvFallback(ctx, db.SetOrbEnvFallbackParams{ID: f.orb.ID}))
	f.o.disconnect()
	require.Eventually(t, func() bool { return !f.h.svc.Connected(f.orb.ID) }, 5*time.Second, 20*time.Millisecond)
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go f.h.svc.Run(runCtx)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, StateReady, f.h.waitState(f.orb.ID, StateReady, time.Second).State, "link 不在时不改状态（不触发 connect 超时）")
	assert.Empty(t, f.stoppedThreads(t))

	// orbd 重连
	require.NoError(t, f.o.connect())
	f.waitResumedReady(t, 1)
	assert.Equal(t, 1, countEvent(f.h.eventSeq(f.orb.ID), "ready>connecting"))
	f.assertWithdrawn(t)

	// 已知不持有任何降级密钥：再次收回（如之后的同意变化）不再动它
	f.h.svc.WithdrawEnvKeys(ctx, "consent")
	assert.Equal(t, 1, countEvent(f.h.eventSeq(f.orb.ID), "ready>connecting"))
}

// TestWithdrawEnvKeysConsented：主人已同意 model:groq、orb 已知持有它时收回什么也不做：不改状态、不停 agent。
func TestWithdrawEnvKeysConsented(t *testing.T) {
	ctx := context.Background()
	f := newEnvFixture(t, false, true)
	setups := f.setupParams(t, "setup.run")
	require.Len(t, setups, 1)
	require.Equal(t, "gsk-literal-0123456789abcdef", setups[0].Env[groqEnv])
	require.NotNil(t, f.held(t))
	assert.Equal(t, `["groq"]`, *f.held(t))

	f.h.svc.recover(ctx)
	f.h.svc.WithdrawEnvKeys(ctx, "startup")
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

	f.revoke(t)
	f.h.svc.WithdrawEnvKeys(ctx, "consent")
	f.waitResumedReady(t, 1)
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

// TestWithdrawEnvKeysAfterVaultKeyDeleted：已同意 groq、orb 带着它的密钥在跑；主人先在设置里删掉这个密钥（只删记录，
// 不停进程），再撤回同意。收回按 orb 记下的 provider 直接查同意，不从当前密钥库或 models.yml 反推：orb 重走 resume，
// 参数因密钥已删而生成失败、进 failed 之前也要停掉还带着旧密钥的 agent。
func TestWithdrawEnvKeysAfterVaultKeyDeleted(t *testing.T) {
	ctx := context.Background()
	f := newEnvFixture(t, false, true)
	require.Equal(t, "gsk-literal-0123456789abcdef", f.setupParams(t, "setup.run")[0].Env[groqEnv])

	ok, err := f.vault.Delete(ctx, agentcfg.ModelKeySecretName("groq"))
	require.NoError(t, err)
	require.True(t, ok)
	f.revoke(t)
	f.h.svc.WithdrawEnvKeys(ctx, "consent")

	require.Eventually(t, func() bool { return countEvent(f.h.eventSeq(f.orb.ID), "resuming>failed") == 1 },
		10*time.Second, 20*time.Millisecond, "events: %v", f.h.eventSeq(f.orb.ID))
	assert.Equal(t, []string{"T-env"}, f.stoppedThreads(t), "进 failed 之前停掉带着旧密钥的 agent")
	assert.Empty(t, f.setupParams(t, "resume.run"))
	require.NotNil(t, f.held(t))
	assert.Equal(t, `["groq"]`, *f.held(t), "orbd 环境没有换掉：记录不收窄")
}

// TestWithdrawEnvKeysStopFailureHoldsReady：撤回后重走 resume，但 link 一直在线、agent.stop 返回错误（agent 可能还带着
// 旧密钥在跑）：orb 不进 ready、env_fallback 不收窄，按退避重试 agent.stop；停下之后才进入 ready。
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

	f.revoke(t)
	f.h.svc.WithdrawEnvKeys(ctx, "consent")
	require.Eventually(t, func() bool { return len(f.stoppedThreads(t)) >= 3 }, 10*time.Second, 10*time.Millisecond, "agent.stop 失败后重试")
	assert.True(t, f.h.svc.Connected(f.orb.ID), "link 一直在线")
	seq := f.h.eventSeq(f.orb.ID)
	assert.Equal(t, 1, countEvent(seq, "ready>connecting"))
	assert.Zero(t, countEvent(seq, "resuming>ready"), "agent 没停下之前不进 ready：%v", seq)
	got, err := f.h.svc.Get(ctx, f.orb.ID)
	require.NoError(t, err)
	assert.Equal(t, StateResuming, got.State)
	require.NotNil(t, f.held(t))
	assert.Equal(t, `["groq"]`, *f.held(t))

	stopFails.Store(false)
	f.waitResumedReady(t, 1)
	assert.Len(t, f.setupParams(t, "resume.run"), 1, "只重试停止，不重走 resume")
	assert.NotContains(t, f.setupParams(t, "resume.run")[0].Env, groqEnv)
	assert.Equal(t, "[]", *f.held(t))
}

// TestWithdrawEnvKeysRevokedDuringResume：orb 已知不持有降级密钥；同意之后一次 resume 生成了带密钥的参数，在 orbd 应答
// 之前撤回（WithdrawEnvKeys 那时看到的不是 ready，不动它）。应答之后、进 ready 之前按最新同意复查：不进 ready，退回
// connecting 重走 resume，新参数里没有密钥、停掉 agent，随后才 ready。
func TestWithdrawEnvKeysRevokedDuringResume(t *testing.T) {
	ctx := context.Background()
	f := newEnvFixture(t, false, false)
	require.NotContains(t, f.setupParams(t, "setup.run")[0].Env, groqEnv)
	require.NotNil(t, f.held(t))
	require.Equal(t, "[]", *f.held(t))

	grantGroq(t, f.h)
	entered := make(chan struct{})
	release := make(chan struct{})
	var first atomic.Bool
	first.Store(true)
	f.o.mu.Lock()
	f.o.fail = func(method string) error {
		if method == "resume.run" && first.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return nil
	}
	f.o.mu.Unlock()

	require.True(t, f.h.svc.reprepareForEnv(ctx, f.orb.ID, nil), "触发一次 resume")
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("resume.run 没有发出")
	}
	require.Equal(t, "gsk-literal-0123456789abcdef", f.setupParams(t, "resume.run")[0].Env[groqEnv], "同意之后的参数带着密钥")
	assert.Equal(t, `["groq"]`, *f.held(t), "发出之前已记下并集")

	f.revoke(t)
	f.h.svc.WithdrawEnvKeys(ctx, "consent")
	close(release)

	require.Eventually(t, func() bool {
		return countEvent(f.h.eventSeq(f.orb.ID), "resuming>ready") == 1 && len(f.setupParams(t, "resume.run")) == 2
	}, 10*time.Second, 20*time.Millisecond, "events: %v", f.h.eventSeq(f.orb.ID))
	seq := f.h.eventSeq(f.orb.ID)
	assert.Equal(t, 1, countEvent(seq, "resuming>connecting"), "带着已撤回密钥的那次 resume 不进 ready：%v", seq)
	f.assertWithdrawn(t)
}
