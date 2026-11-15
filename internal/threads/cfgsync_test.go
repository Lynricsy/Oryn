package threads

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store"
)

// ---- M12.2：omfg 应答回写中央配置（契约 C5、C6）----

// guard 是等待异步结果的失败保护上限（不是等待时长：结果到达即返回）。
const guard = 10 * time.Second

// fakeSync 是带闸门的 ConfigSync 替身：SaveRule 把参数送进 calls，等 gate 关闭后返回 res（gate 为 nil 时立即返回）。
// block 为真时改为等到 ctx 结束，返回 Reason=orb_read_failed（模拟超时）。
type fakeSync struct {
	calls chan ConfigSaveParams
	gate  chan struct{}
	block bool
	res   ConfigSaveResult

	mu        sync.Mutex
	deadlines []time.Duration // 调用时 ctx 剩余的时间（无截止时间记 -1）
}

func newFakeSync(gated bool) *fakeSync {
	f := &fakeSync{calls: make(chan ConfigSaveParams, 16), res: ConfigSaveResult{Synced: true, Scope: "global", Path: "rules/x.md"}}
	if gated {
		f.gate = make(chan struct{})
	}
	return f
}

func (f *fakeSync) SaveRule(ctx context.Context, p ConfigSaveParams) ConfigSaveResult {
	left := time.Duration(-1)
	if dl, ok := ctx.Deadline(); ok {
		left = time.Until(dl)
	}
	f.mu.Lock()
	f.deadlines = append(f.deadlines, left)
	f.mu.Unlock()
	f.calls <- p
	if f.block {
		<-ctx.Done()
		return ConfigSaveResult{Scope: "global", Path: "rules/x.md", Reason: "orb_read_failed"}
	}
	if f.gate != nil {
		<-f.gate
	}
	return f.res
}

func (f *fakeSync) noCall(t *testing.T) {
	t.Helper()
	select {
	case p := <-f.calls:
		t.Fatalf("不应回写：%+v", p)
	default:
	}
}

func (f *fakeSync) call(t *testing.T) ConfigSaveParams {
	t.Helper()
	select {
	case p := <-f.calls:
		return p
	case <-time.After(guard):
		t.Fatal("SaveRule 没有被调用")
		return ConfigSaveParams{}
	}
}

// kindOrbs 让 Features 报告指定的 orb 种类（runner 线程的用例）。
type kindOrbs struct {
	*stubOrbs
	kind string
}

func (o kindOrbs) Features(string) (string, []string) { return o.kind, nil }

// newKindFixture 同 newFixture，但 orbs.Features 报告 kind。
func newKindFixture(t *testing.T, kind string) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	o := &stubOrbs{st: st, wakes: make(chan string, 16)}
	s := New(Config{DataDir: dir}, st, kindOrbs{stubOrbs: o, kind: kind})
	t.Cleanup(s.Close)
	sink := &recSink{}
	s.SetSink(sink)
	return &fixture{t: t, ctx: ctx, st: st, orbs: o, sink: sink, s: s}
}

// replies 收集一条命令的应答。
type replies chan json.RawMessage

func (r replies) fn() func(json.RawMessage) { return func(b json.RawMessage) { r <- b } }

func (r replies) none(t *testing.T) {
	t.Helper()
	select {
	case b := <-r:
		t.Fatalf("应答不应已交付：%s", b)
	default:
	}
}

func (r replies) next(t *testing.T) json.RawMessage {
	t.Helper()
	select {
	case b := <-r:
		return b
	case <-time.After(guard):
		t.Fatal("应答没有交付")
		return nil
	}
}

// omfg 以客户端身份发一条 omfg 命令，返回写到 agent 流上的命令帧与应答收集器。
func omfg(t *testing.T, f *fixture, th *thread, a *agentConn) (string, replies) {
	t.Helper()
	r := make(replies, 4)
	require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"omfg","text":"do not print the marker"}`), r.fn()))
	out := sent(a)
	require.Len(t, out, 1)
	return out[0], r
}

// choose 模拟 Core 的保存位置对话框与用户的选择（value 为空 = 取消）。
func choose(t *testing.T, f *fixture, th *thread, a *agentConn, uiID, value string) {
	t.Helper()
	f.frame(th, fmt.Sprintf(`{"type":"extension_ui_request","id":%q,"method":"select","title":"Save TTSR rule where?","options":["This project (Oryn project config)","Global — all projects (Oryn central config)","Amend with feedback…"]}`, uiID))
	resp := fmt.Sprintf(`{"type":"extension_ui_response","id":%q,"cancelled":true}`, uiID)
	if value != "" {
		resp = fmt.Sprintf(`{"type":"extension_ui_response","id":%q,"value":%q}`, uiID, value)
	}
	require.NoError(t, f.s.UIResponse(f.ctx, th.id, json.RawMessage(resp)))
	_ = sent(a)
}

func respFrame(t *testing.T, cmd string, success bool, data string) string {
	t.Helper()
	return fmt.Sprintf(`{"type":"response","id":%q,"command":"omfg","success":%t,"data":%s}`, frameID(t, cmd), success, data)
}

const (
	globalLabel    = "Global — all projects (Oryn central config)"
	oldGlobalLabel = "Global — all projects (~/.omp/agent/rules)"
	projectLabel   = "This project (Oryn project config)"
	oldProjectLbl  = "This project (.omp/rules)"
	amendLabel     = "Amend with feedback…"
	savedGlobal    = `{"state":"saved","savedPath":"~/.omp/agent/rules/x.md"}`
)

// saved 应答在 SaveRule 返回之前不交付；之后 data.oryn 并入，原有字段不变；回写有 cfgSyncTimeout 截止时间。
func TestOmfgSyncDelaysResponse(t *testing.T) {
	f := newFixture(t, Config{})
	fs := newFakeSync(true)
	f.s.SetConfigSync(fs)
	th, a, _ := readyThread(f)

	cmd, r := omfg(t, f, th, a)
	choose(t, f, th, a, "u1", globalLabel)
	f.respond(th, cmd, true, savedGlobal)

	p := fs.call(t)
	assert.Equal(t, ConfigSaveParams{OrbID: th.orbID, ThreadID: th.id, SavedPath: "~/.omp/agent/rules/x.md", GlobalChosen: true}, p)
	r.none(t)
	close(fs.gate)
	got := r.next(t)

	var resp struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Command string `json:"command"`
		Success bool   `json:"success"`
		Data    struct {
			State     string           `json:"state"`
			SavedPath string           `json:"savedPath"`
			Oryn      ConfigSaveResult `json:"oryn"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(got, &resp))
	assert.Equal(t, "response", resp.Type)
	assert.Equal(t, frameID(t, cmd), resp.ID)
	assert.Equal(t, "omfg", resp.Command)
	assert.True(t, resp.Success)
	assert.Equal(t, "saved", resp.Data.State)
	assert.Equal(t, "~/.omp/agent/rules/x.md", resp.Data.SavedPath)
	assert.Equal(t, ConfigSaveResult{Synced: true, Scope: "global", Path: "rules/x.md"}, resp.Data.Oryn)
	assert.JSONEq(t, `{"synced":true,"scope":"global","path":"rules/x.md"}`, string(mustRaw(t, got, "data", "oryn")))

	fs.mu.Lock()
	dl := fs.deadlines[0]
	fs.mu.Unlock()
	assert.Positive(t, dl, "回写必须带截止时间")
	assert.LessOrEqual(t, dl, cfgSyncTimeout)
	th.mu.Lock()
	assert.Empty(t, th.pending)
	th.mu.Unlock()
}

// 未同步的结果（reason）同样并入 data.oryn。
func TestOmfgSyncReasonMerged(t *testing.T) {
	f := newFixture(t, Config{})
	fs := newFakeSync(false)
	fs.res = ConfigSaveResult{Reason: "unsupported_path"}
	f.s.SetConfigSync(fs)
	th, a, _ := readyThread(f)
	cmd, r := omfg(t, f, th, a)
	f.respond(th, cmd, true, `{"state":"saved","savedPath":"/tmp/x.md"}`)
	fs.call(t)
	assert.JSONEq(t, `{"synced":false,"reason":"unsupported_path"}`, string(mustRaw(t, r.next(t), "data", "oryn")))
}

// SaveRule 卡住时，cfgSyncTimeout 到期后应答照样交付（带 SaveRule 在超时后给出的结果）。
func TestOmfgSyncTimeout(t *testing.T) {
	f := newFixture(t, Config{})
	fs := newFakeSync(false)
	fs.block = true
	f.s.SetConfigSync(fs)
	f.s.cfgSyncTimeout = time.Millisecond
	th, a, _ := readyThread(f)
	cmd, r := omfg(t, f, th, a)
	choose(t, f, th, a, "u1", globalLabel)
	f.respond(th, cmd, true, savedGlobal)
	fs.call(t)
	assert.JSONEq(t, `{"synced":false,"scope":"global","path":"rules/x.md","reason":"orb_read_failed"}`, string(mustRaw(t, r.next(t), "data", "oryn")))
}

// GlobalChosen 只在用户于 omfg 在途时点了全局文案（新旧两套）才为真；每次 omfg 应答后复位。
func TestOmfgGlobalChoice(t *testing.T) {
	f := newFixture(t, Config{})
	fs := newFakeSync(false)
	f.s.SetConfigSync(fs)
	th, a, _ := readyThread(f)

	run := func(name string, choices []string, want bool) {
		t.Helper()
		cmd, r := omfg(t, f, th, a)
		for i, c := range choices {
			choose(t, f, th, a, fmt.Sprintf("%s-%d", name, i), c)
		}
		f.respond(th, cmd, true, savedGlobal)
		assert.Equal(t, want, fs.call(t).GlobalChosen, name)
		r.next(t)
	}
	run("new-global", []string{globalLabel}, true)
	run("none", nil, false) // 上一次的选择已复位
	run("old-global", []string{oldGlobalLabel}, true)
	run("new-project", []string{projectLabel}, false)
	run("old-project", []string{oldProjectLbl}, false)
	run("global-then-amend", []string{globalLabel, amendLabel}, false)
	run("global-then-other-dialog", []string{globalLabel, "something else"}, true)
	run("global-then-cancel", []string{globalLabel, ""}, true)

	// 不成功的 omfg 应答同样复位
	cmd, r := omfg(t, f, th, a)
	choose(t, f, th, a, "rej", globalLabel)
	f.respond(th, cmd, true, `{"state":"rejected"}`)
	r.next(t)
	run("after-rejected", nil, false)

	// 没有 omfg 在途时（其它对话框）点的全局文案不算数
	choose(t, f, th, a, "stray", globalLabel)
	run("stray", nil, false)
}

// 不回写：rejected / aborted / 失败应答 / 缺 savedPath / 没设置 ConfigSync：原帧同步交付，ConfigSync 没被调用。
func TestOmfgNoSync(t *testing.T) {
	f := newFixture(t, Config{})
	fs := newFakeSync(false)
	f.s.SetConfigSync(fs)
	th, a, _ := readyThread(f)

	for _, c := range []struct {
		success bool
		data    string
	}{
		{true, `{"state":"rejected"}`},
		{true, `{"state":"aborted"}`},
		{false, `null`},
		{false, savedGlobal},
		{true, `{"state":"saved"}`},
		{true, `{"state":"saved","savedPath":""}`},
		{true, `null`},
	} {
		cmd, r := omfg(t, f, th, a)
		choose(t, f, th, a, "u", globalLabel)
		f.respond(th, cmd, c.success, c.data)
		got := r.next(t) // 同步交付：respond 返回时已在通道里
		assert.JSONEq(t, respFrame(t, cmd, c.success, c.data), string(got), c.data)
	}
	fs.noCall(t)

	// 没有 SetConfigSync
	f2 := newFixture(t, Config{})
	th2, a2, _ := readyThread(f2)
	cmd, r := omfg(t, f2, th2, a2)
	choose(t, f2, th2, a2, "u", globalLabel)
	f2.respond(th2, cmd, true, savedGlobal)
	assert.JSONEq(t, respFrame(t, cmd, true, savedGlobal), string(r.next(t)))
}

// 非客户端发起的 omfg（自动化投递、服务内部命令）不回写。
func TestOmfgNotUserInitiated(t *testing.T) {
	f := newFixture(t, Config{})
	fs := newFakeSync(false)
	f.s.SetConfigSync(fs)
	th, a, _ := readyThread(f)

	// 自动化投递
	r := make(replies, 1)
	_, err := f.s.command(f.ctx, th.id, json.RawMessage(`{"type":"omfg","text":"x"}`), r.fn(), originAuto, "")
	require.NoError(t, err)
	cmd := sent(a)[0]
	choose(t, f, th, a, "u1", globalLabel)
	f.respond(th, cmd, true, savedGlobal)
	assert.JSONEq(t, respFrame(t, cmd, true, savedGlobal), string(r.next(t)))

	// 服务内部命令
	ch := make(chan json.RawMessage, 1)
	th.mu.Lock()
	require.NoError(t, f.s.enqueueLocked(th, []byte(`{"type":"omfg","id":"internal-1","text":"x"}`), &pendingCmd{cmdType: "omfg", internal: ch}))
	th.mu.Unlock()
	cmd = sent(a)[0]
	f.respond(th, cmd, true, savedGlobal)
	select {
	case got := <-ch:
		assert.JSONEq(t, respFrame(t, cmd, true, savedGlobal), string(got))
	default:
		t.Fatal("内部命令的应答应同步交付")
	}
	fs.noCall(t)
}

// runner 线程用的是用户机器上自己的 omp 配置：不回写，原帧交付。
func TestOmfgRunnerNoSync(t *testing.T) {
	f := newKindFixture(t, orbs.KindRunner)
	fs := newFakeSync(false)
	f.s.SetConfigSync(fs)
	th, a, _ := readyThread(f)
	cmd, r := omfg(t, f, th, a)
	choose(t, f, th, a, "u1", oldGlobalLabel)
	f.respond(th, cmd, true, savedGlobal)
	assert.JSONEq(t, respFrame(t, cmd, true, savedGlobal), string(r.next(t)))
	fs.noCall(t)
}

func TestWithOrynSync(t *testing.T) {
	res := ConfigSaveResult{Synced: true, Scope: "project", Path: "rules/a.md"}
	for _, bad := range []string{`not json`, `[]`, `null`, `{"type":"response"}`, `{"type":"response","data":null}`, `{"type":"response","data":"x"}`} {
		assert.Equal(t, bad, string(withOrynSync(json.RawMessage(bad), res)), bad)
	}
	out := withOrynSync(json.RawMessage(`{"type":"response","id":"1","data":{"state":"saved","savedPath":"p","oryn":"forged"}}`), res)
	assert.JSONEq(t, `{"type":"response","id":"1","data":{"state":"saved","savedPath":"p","oryn":{"synced":true,"scope":"project","path":"rules/a.md"}}}`, string(out))
}

// mustRaw 取 JSON 帧里按 keys 逐层下钻的原始值。
func mustRaw(t *testing.T, frame json.RawMessage, keys ...string) json.RawMessage {
	t.Helper()
	cur := frame
	for _, k := range keys {
		var m map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(cur, &m), string(cur))
		v, ok := m[k]
		require.True(t, ok, "缺少 %s：%s", k, cur)
		cur = v
	}
	return cur
}
