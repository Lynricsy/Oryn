package orbdrt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// frameWith 读取帧直到 pred 成立（含），返回期间的全部帧。
func (fr *frameReader) frameWith(t *testing.T, what string, pred func(json.RawMessage) bool) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	tm := time.NewTimer(30 * time.Second)
	defer tm.Stop()
	for {
		select {
		case f, ok := <-fr.ch:
			if !ok {
				t.Fatalf("agent stream ended before %s; got %v", what, types(out))
			}
			out = append(out, f)
			if pred(f) {
				return out
			}
		case <-tm.C:
			t.Fatalf("timed out waiting for %s; got %v", what, types(out))
		}
	}
}

func responseID(id string) func(json.RawMessage) bool {
	return func(f json.RawMessage) bool {
		var e struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		_ = json.Unmarshal(f, &e)
		return e.Type == "response" && e.ID == id
	}
}

func hasStatus(cp *fakeCP, thread, status string) bool {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	pred := statusIs(thread, status)
	for _, n := range cp.notifs {
		if n.method == "agent.status" && pred(n.params) {
			return true
		}
	}
	return false
}

// OD1：扩展的 session_start 在等一个对话框时，omp 只即时处理侧通道应答（ready 尚未发出）。
// orbd 必须把 extension_ui_response 立即写入 omp，普通命令则排到初始化完成之后。
func TestStartupDialogAnsweredBeforeReady(t *testing.T) {
	cp := newFakeCP(t)
	startRuntime(t, cp, func(c *Config) { c.InitTimeout = 5 * time.Second })
	cp.ctlNow(t)
	const thread = "T-startup"
	startAgent(t, cp, thread, map[string]string{"FAKEOMP_STARTUP_DIALOG": "Trust this folder?"})
	st := waitAgentStream(t, cp)
	fr := readFrames(st)
	pre := fr.until(t, "extension_ui_request")
	var req struct {
		ID     string `json:"id"`
		Method string `json:"method"`
	}
	require.NoError(t, json.Unmarshal(pre[len(pre)-1], &req))
	assert.Equal(t, "select", req.Method)
	assert.False(t, hasStatus(cp, thread, "ready"), "对话框待答时 agent 尚未就绪")

	// 普通命令先到：不能挡住其后的对话框应答。
	sendCmd(t, st, `{"type":"get_state","id":"c1"}`)
	sendCmd(t, st, fmt.Sprintf(`{"type":"extension_ui_response","id":%q,"value":"continue"}`, req.ID))
	frames := fr.frameWith(t, "get_state response", responseID("c1"))
	var notice bool
	for _, f := range frames {
		if frameType(f) == "notice" && strings.Contains(string(f), "continue") {
			notice = true
		}
	}
	assert.True(t, notice, "启动对话框收到了应答")
	cp.waitNotif(t, 0, "agent.status", statusIs(thread, "ready"))
	assert.False(t, hasStatus(cp, thread, "exited"), "初始化没有超时重启")
}

// OD2：替换进程完成初始化时旧进程代的帧还没写完：ready 只能在旧流写完、当前进程代的流打开之后上报，
// 控制面据此下发的命令落在新流上并得到应答。
func TestReadyOnlyOnCurrentEpochStream(t *testing.T) {
	inited := make(chan *thread, 4)
	testHookAgentInitialized = func(th *thread) { inited <- th }
	t.Cleanup(func() { testHookAgentInitialized = nil })

	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.ctlNow(t)
	const thread = "T-epoch"
	th := rt.thread(thread, filepath.Join(rt.home, ".oryn", "threads", thread))
	// 上一进程代（epoch 0）遗留 2MiB 帧；控制面先不读，转发器卡在旧流上（yamux 窗口 256KiB）。
	pad := strings.Repeat("x", 64<<10)
	const old = 32
	for i := range old {
		th.out.push(outItem{epoch: 0, data: fmt.Appendf(nil, `{"type":"notice","level":"info","message":"old-%d %s"}`, i, pad)})
	}
	th.wake()
	st0 := waitAgentStream(t, cp)

	startAgent(t, cp, thread, nil)
	select {
	case got := <-inited:
		require.Same(t, th, got)
	case <-time.After(20 * time.Second):
		t.Fatal("agent did not initialize")
	}
	s := th.lastStatus()
	require.NotNil(t, s)
	assert.Equal(t, apigen.LinkCtlAgentStatusParamsStatusStarting, s.Status, "旧流未写完时不得上报 ready")

	fr0 := readFrames(st0)
	for i := range old {
		f := <-fr0.ch
		require.Contains(t, string(f), fmt.Sprintf("old-%d ", i))
	}
	st1 := waitAgentStream(t, cp)
	cp.waitNotif(t, 0, "agent.status", statusIs(thread, "ready"))
	_, ok := <-fr0.ch
	assert.False(t, ok, "旧进程代的流已关闭")
	fr1 := readFrames(st1)
	sendCmd(t, st1, `{"type":"get_state","id":"s1"}`)
	resp := fr1.frameWith(t, "get_state response", responseID("s1"))
	assert.Contains(t, string(resp[len(resp)-1]), `"success":true`)
}

// OD2 兜底：命令落到已退出进程代的流上时，得到一条合成的失败应答，而不是被静默丢弃。
func TestCommandOnDeadEpochGetsFailure(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.ctlNow(t)
	const thread = "T-dead"
	th := rt.thread(thread, filepath.Join(rt.home, ".oryn", "threads", thread))
	// epoch 0 没有对应的进程；遗留帧足够多，转发器卡在写这条流上，流在命令到达时仍然打开。
	pad := strings.Repeat("x", 64<<10)
	for i := range 32 {
		th.out.push(outItem{epoch: 0, data: fmt.Appendf(nil, `{"type":"notice","level":"info","message":"left-%d %s"}`, i, pad)})
	}
	th.wake()
	st := waitAgentStream(t, cp)
	sendCmd(t, st, `{"type":"prompt","id":"p1","message":"hi"}`)
	require.Eventually(t, func() bool {
		th.out.mu.Lock()
		defer th.out.mu.Unlock()
		for _, it := range th.out.items {
			if strings.Contains(string(it.data), `"id":"p1"`) {
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "synthetic response not queued")
	fr := readFrames(st)
	resp := fr.frameWith(t, "synthetic response", responseID("p1"))
	last := string(resp[len(resp)-1])
	assert.Contains(t, last, `"success":false`)
	assert.Contains(t, last, `"command":"prompt"`)
}

// OD9：gap 上报途中 link 断开，标记必须保留到重连后重报，且重报先于 gap 之后的帧。
func TestGapRetainedWhenReportFails(t *testing.T) {
	var once sync.Once
	testHookBeforeGapReport = func(c *conn) { once.Do(func() { _ = c.sess.Close() }) }
	t.Cleanup(func() { testHookBeforeGapReport = nil })

	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.ctlNow(t)
	const thread = "T-gap"
	th := rt.thread(thread, filepath.Join(rt.home, ".oryn", "threads", thread))
	th.out.push(outItem{epoch: 0, data: make([]byte, maxBufferedFrames+1)}) // 溢出 → gap
	th.out.push(outItem{epoch: 0, data: []byte(`{"type":"notice","level":"info","message":"after-gap"}`)})
	th.wake()

	// 第一次上报时 link 被断开；重连（第二次 hello）之后必须再报一次 gap。
	helloIdx, _ := cp.waitNotif(t, 0, "hello", nil)
	second, _ := cp.waitNotif(t, helloIdx+1, "hello", nil)
	gapIdx, _ := cp.waitNotif(t, second, "agent.gap", func(raw json.RawMessage) bool {
		return strings.Contains(string(raw), thread)
	})
	var fr *frameReader
	for fr == nil {
		st := waitAgentStream(t, cp)
		r := readFrames(st)
		select {
		case f, ok := <-r.ch:
			if ok {
				assert.Contains(t, string(f), "after-gap")
				fr = r
			}
		case <-time.After(20 * time.Second):
			t.Fatal("frame after gap not delivered")
		}
	}
	assert.Greater(t, gapIdx, second)
}

// H2：每次拨号优先读 $HOME/.oryn/orb-token；SIGHUP（Redial）后以轮换后的令牌重连。
func TestTokenFileUsedOnRedial(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.ctlNow(t)
	first, _ := cp.waitNotif(t, 0, "hello", nil)

	cp.token.Store("oot_rotated")
	require.NoError(t, os.MkdirAll(filepath.Dir(TokenFile(rt.home)), 0o700))
	require.NoError(t, os.WriteFile(TokenFile(rt.home), []byte("  oot_rotated\n"), 0o600))
	rt.Redial()
	cp.waitNotif(t, first+1, "hello", nil)
}

func TestConfigFromEnvTokenFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ORYN_URL", "http://cp")
	t.Setenv("ORYN_ORB_ID", "orb-x")
	t.Setenv("ORYN_ORB_TOKEN", "")
	_, err := ConfigFromEnv("v")
	require.Error(t, err, "既无环境变量也无令牌文件")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".oryn"), 0o700))
	require.NoError(t, os.WriteFile(TokenFile(home), []byte("oot_file\n"), 0o600))
	_, err = ConfigFromEnv("v")
	require.NoError(t, err)
}

// OD10：超过复制上限的条目不能被跳过并确认。复制停在它之前（游标不越过它），线程收到一条 notice；
// 文件被重写后恢复。
func TestOversizedEntryNotAcknowledged(t *testing.T) {
	old := entryLineLimit
	entryLineLimit = 1 << 10
	t.Cleanup(func() { entryLineLimit = old })

	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.ctlNow(t)
	const thread = "T-big"
	dir := filepath.Join(rt.home, ".oryn", "threads", thread)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	l1 := `{"type":"session","id":"__session__"}` + "\n"
	big := `{"type":"message","id":"bbbbbbbb","pad":"` + strings.Repeat("x", 2<<10) + `"}` + "\n"
	l3 := `{"type":"message","id":"cccccccc"}` + "\n"
	file := filepath.Join(dir, "2026-01-01_s.jsonl")
	require.NoError(t, os.WriteFile(file, []byte(l1+big+l3), 0o644))
	th := rt.thread(thread, dir)

	st := waitAgentStream(t, cp)
	fr := readFrames(st)
	notice := fr.until(t, "notice")
	assert.Contains(t, string(notice[len(notice)-1]), "replication limit")

	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	th.tail.flush(ctx)
	th.tail.flush(ctx)
	assert.Equal(t, []string{strings.TrimSuffix(l1, "\n")}, cp.mainLines(thread), "超长行及其后的行都不复制")
	cp.mu.Lock()
	var offset int64
	for name, f := range cp.files[thread] {
		if !strings.Contains(name, "/") {
			offset = f.offset
		}
	}
	cp.mu.Unlock()
	assert.EqualValues(t, len(l1), offset, "游标停在超长行之前")
	select {
	case f, ok := <-fr.ch:
		if ok {
			t.Fatalf("notice must be sent once, got another frame %s", f)
		}
	default:
	}

	// 重写文件（去掉超长行）：reset 后全部复制。
	tmp := file + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte(l1+l3), 0o644))
	require.NoError(t, os.Rename(tmp, file))
	require.Eventually(t, func() bool { return len(cp.mainLines(thread)) == 2 }, 20*time.Second, 20*time.Millisecond)
}

func contextWithTimeout(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 20*time.Second)
}

// OD7：setup 进行中 link 重连，控制面重发同样的 setup.run：重试等原运行的请求结果，
// 不重跑 setup，也不排在转入后台的 resume 之后。
func TestSetupRetryJoinsInflightRun(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, func(c *Config) { c.ResumeWait = 200 * time.Millisecond })
	home := rt.home
	setup := `echo run >> "$HOME/setup-count"; while [ ! -f "$HOME/go-setup" ]; do sleep 0.05; done`
	resume := `while [ ! -f "$HOME/go-resume" ]; do sleep 0.05; done`
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(home, "go-resume"), nil, 0o644) })
	params := apigen.LinkCtlSetupRunParams{
		GitHosts: []string{}, OmpHome: []apigen.LinkCtlOmpHomeFile{}, Env: map[string]string{},
		Scripts: apigen.LinkCtlSetupScripts{Setup: &setup, Resume: &resume},
	}
	first := make(chan error, 1)
	go func() { first <- cp.call(t, "setup.run", params, nil) }()
	require.Eventually(t, func() bool {
		b, _ := os.ReadFile(filepath.Join(home, "setup-count"))
		return len(b) > 0
	}, 20*time.Second, 20*time.Millisecond, "setup script did not start")

	hello, _ := cp.waitNotif(t, 0, "hello", nil)
	cp.killLink()
	require.Error(t, <-first, "原请求所在的 link 已断开")
	cp.waitNotif(t, hello+1, "hello", nil)

	second := make(chan apigen.LinkCtlSetupRunResult, 1)
	secondErr := make(chan error, 1)
	go func() {
		var res apigen.LinkCtlSetupRunResult
		err := cp.call(t, "setup.run", params, &res)
		second <- res
		secondErr <- err
	}()
	require.NoError(t, os.WriteFile(filepath.Join(home, "go-setup"), nil, 0o644))
	select {
	case res := <-second:
		require.NoError(t, <-secondErr)
		assert.True(t, res.Ok)
		assert.True(t, res.SetupRan, "得到的是原运行的结果")
	case <-time.After(30 * time.Second):
		t.Fatal("retried setup.run waited behind the background resume")
	}
	b, err := os.ReadFile(filepath.Join(home, "setup-count"))
	require.NoError(t, err)
	assert.Equal(t, "run\n", string(b), "setup 只运行一次")
	// 放行后台 resume 并等它上报结束，避免它在 TempDir 清理时仍在写 $HOME/.oryn。
	require.NoError(t, os.WriteFile(filepath.Join(home, "go-resume"), nil, 0o644))
	waitResult(t, cp, 0, apigen.SetupPhaseResume)
}

// M2：GitHosts 条目可限定到仓库；每次 setup 先清掉以前写入的全部 egress insteadOf（含旧的整主机条目）。
func TestGitInsteadOfScopedAndReplaced(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	gitcfg := filepath.Join(rt.home, ".gitconfig")
	base := "http://" + rt.EgressAddr() + "/git/"
	run := func(hosts ...string) error {
		return cp.call(t, "resume.run", apigen.LinkCtlSetupRunParams{
			GitHosts: hosts, OmpHome: []apigen.LinkCtlOmpHomeFile{}, Env: map[string]string{},
		}, nil)
	}
	require.NoError(t, run("github.com", "gitlab.com"))
	require.NoError(t, exec.Command("git", "config", "--file", gitcfg, "user.name", "keep").Run())
	require.NoError(t, run("github.com/Owner/Repo"))

	out, err := exec.Command("git", "config", "--file", gitcfg, "--get-regexp", `^url\.`).Output()
	require.NoError(t, err)
	assert.Equal(t, "url."+base+"github.com/Owner/Repo.insteadof https://github.com/Owner/Repo\n", string(out),
		"只剩项目仓库的条目")
	name, _ := exec.Command("git", "config", "--file", gitcfg, "user.name").Output()
	assert.Equal(t, "keep\n", string(name), "其它配置不受影响")

	for _, bad := range []string{"github.com/owner", "github.com/../x/y", "github.com/a//b", "git hub.com", "x@github.com", "github.com/a/b\nc"} {
		assert.Error(t, run(bad), bad)
	}
}

// OD1 真机验证（需要真实 omp）：用户扩展的 session_start 等一个 ui.select；对话框经 orbd 作答后 agent 就绪。
// ORYN_REAL_OMP=<omp 可执行文件> 时运行，例如 build/core/x64/omp。
func TestRealOmpStartupDialog(t *testing.T) {
	bin := os.Getenv("ORYN_REAL_OMP")
	if bin == "" {
		t.Skip("set ORYN_REAL_OMP to a real omp binary")
	}
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, func(c *Config) {
		c.AgentBin = bin
		c.InitTimeout = 90 * time.Second
		c.RestartDelays = []time.Duration{}
	})
	cp.ctlNow(t)
	ext := filepath.Join(rt.home, ".omp", "agent", "extensions", "startup-probe.ts")
	require.NoError(t, os.MkdirAll(filepath.Dir(ext), 0o755))
	require.NoError(t, os.WriteFile(ext, []byte(`import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";
export default function (pi: ExtensionAPI) {
	pi.on("session_start", async (_event, ctx) => {
		const v = await ctx.ui.select("Oryn startup probe", ["continue", "abort"]);
		ctx.ui.notify("startup probe answered: " + v, "info");
	});
}
`), 0o644))
	const thread = "T-realomp"
	startAgent(t, cp, thread, nil)
	st := waitAgentStream(t, cp)
	fr := readFrames(st)
	frames := fr.frameWith(t, "startup select", func(f json.RawMessage) bool {
		return frameType(f) == "extension_ui_request" && strings.Contains(string(f), "Oryn startup probe")
	})
	var req struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(frames[len(frames)-1], &req))
	t.Logf("startup dialog request: %s", frames[len(frames)-1])
	assert.False(t, hasStatus(cp, thread, "ready"))
	sendCmd(t, st, `{"type":"get_state","id":"c1"}`)
	sendCmd(t, st, fmt.Sprintf(`{"type":"extension_ui_response","id":%q,"value":"continue"}`, req.ID))
	resp := fr.frameWith(t, "get_state response", responseID("c1"))
	for _, f := range resp {
		if strings.Contains(string(f), "startup probe answered") {
			t.Logf("notify after answer: %s", f)
		}
	}
	assert.Contains(t, string(resp[len(resp)-1]), `"success":true`)
	cp.waitNotif(t, 0, "agent.status", statusIs(thread, "ready"))
	assert.False(t, hasStatus(cp, thread, "exited"))
}
