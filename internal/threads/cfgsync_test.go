package threads

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
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

// choose 模拟 Core 的保存位置对话框与用户的选择（value 为空 = 取消）。旧 Core 的文案配旧 Core 的选项。
func choose(t *testing.T, f *fixture, th *thread, a *agentConn, uiID, value string) {
	t.Helper()
	opts := []string{projectLabel, globalLabel, amendLabel}
	if value == oldGlobalLabel || value == oldProjectLbl {
		opts = []string{oldProjectLbl, oldGlobalLabel, amendLabel}
	}
	dialog(t, f, th, a, uiID, omfgSaveTitle, opts, value)
}

// dialog 模拟 Core 发一个 select 对话框、用户作答（value 为空 = 取消）。
func dialog(t *testing.T, f *fixture, th *thread, a *agentConn, uiID, title string, opts []string, value string) {
	t.Helper()
	req, err := json.Marshal(map[string]any{"type": "extension_ui_request", "id": uiID, "method": "select", "title": title, "options": opts})
	require.NoError(t, err)
	f.frame(th, string(req))
	answer(t, f, th, a, uiID, value)
}

// answer 以用户身份回答 UI 请求 uiID（value 为空 = 取消）。
func answer(t *testing.T, f *fixture, th *thread, a *agentConn, uiID, value string) {
	t.Helper()
	resp := fmt.Sprintf(`{"type":"extension_ui_response","id":%q,"cancelled":true}`, uiID)
	if value != "" {
		resp = fmt.Sprintf(`{"type":"extension_ui_response","id":%q,"value":%q}`, uiID, value)
	}
	require.NoError(t, f.s.UIResponse(f.ctx, th.id, json.RawMessage(resp)))
	_ = sent(a)
}

// preview 模拟 Core 的 omfg 候选规则预览（setWidget key omfg，见 rpc-omfg.ts 的 publishRule）；rule 为空 = 清除。
func preview(t *testing.T, f *fixture, th *thread, rule string) {
	t.Helper()
	w := map[string]any{"type": "extension_ui_request", "id": "w-omfg", "method": "setWidget", "widgetKey": "omfg"}
	if rule != "" {
		w["widgetLines"] = append([]string{"TTSR rule — matches this conversation", ""}, strings.Split(rule, "\n")...)
	}
	b, err := json.Marshal(w)
	require.NoError(t, err)
	f.frame(th, string(b))
}

// ruleText 是一条 Core 生成格式的 /omfg 规则。
const ruleText = "---\nname: x\ndescription: \"Never print the marker\"\ncondition: \"M122_MARKER\"\nscope: [\"text\"]\n---\n\nDo not output M122_MARKER."

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
	preview(t, f, th, ruleText)
	choose(t, f, th, a, "u1", globalLabel)
	f.respond(th, cmd, true, savedGlobal)

	p := fs.call(t)
	assert.Equal(t, ConfigSaveParams{OrbID: th.orbID, ThreadID: th.id, SavedPath: "~/.omp/agent/rules/x.md", GlobalChosen: true, Preview: ruleText}, p)
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

// GlobalChosen 只在用户于这条 omfg 的保存位置对话框里点了全局文案（新旧两套）才为真；记录随命令结束。
// 对保存位置对话框的任何作答（含取消、修改反馈、不在选项里的值）都先作废此前的记录；其它对话框不算数。
func TestOmfgGlobalChoice(t *testing.T) {
	f := newFixture(t, Config{})
	fs := newFakeSync(false)
	f.s.SetConfigSync(fs)
	th, a, _ := readyThread(f)

	run := func(name string, steps func(), want bool) {
		t.Helper()
		cmd, r := omfg(t, f, th, a)
		if steps != nil {
			steps()
		}
		f.respond(th, cmd, true, savedGlobal)
		assert.Equal(t, want, fs.call(t).GlobalChosen, name)
		r.next(t)
	}
	pick := func(name string, choices ...string) func() {
		return func() {
			for i, c := range choices {
				choose(t, f, th, a, fmt.Sprintf("%s-%d", name, i), c)
			}
		}
	}
	run("new-global", pick("ng", globalLabel), true)
	run("none", nil, false) // 上一条命令的选择不留到这一条
	run("old-global", pick("og", oldGlobalLabel), true)
	run("new-project", pick("np", projectLabel), false)
	run("old-project", pick("op", oldProjectLbl), false)
	run("global-then-amend", pick("ga", globalLabel, amendLabel), false)
	run("global-then-cancel", pick("gc", globalLabel, ""), false)
	run("amend-then-global", pick("ag", amendLabel, globalLabel), true)
	saveOpts := []string{projectLabel, globalLabel, amendLabel}
	run("global-then-other-dialog", func() {
		choose(t, f, th, a, "go-0", globalLabel)
		// 别的对话框（标题不同）里同名的选项不影响保存位置的记录
		dialog(t, f, th, a, "go-1", "Pick one", []string{projectLabel}, projectLabel)
	}, true)
	run("other-dialog-global", func() {
		dialog(t, f, th, a, "od", "Pick one", []string{globalLabel}, globalLabel)
	}, false)
	run("global-not-an-option", func() {
		dialog(t, f, th, a, "no", omfgSaveTitle, []string{projectLabel, amendLabel}, globalLabel)
	}, false)
	run("not-select", func() {
		req, _ := json.Marshal(map[string]any{"type": "extension_ui_request", "id": "ns", "method": "input", "title": omfgSaveTitle})
		f.frame(th, string(req))
		answer(t, f, th, a, "ns", globalLabel)
	}, false)
	// 命令受理之前就已待答的对话框不属于这条 omfg
	early, _ := json.Marshal(map[string]any{"type": "extension_ui_request", "id": "early", "method": "select", "title": omfgSaveTitle, "options": saveOpts})
	f.frame(th, string(early))
	run("dialog-before-command", func() { answer(t, f, th, a, "early", globalLabel) }, false)

	// 不成功的 omfg 应答：记录随命令结束
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

// framesOfType 从写到 agent 流上的帧里挑出 typ 类型的。
func framesOfType(t *testing.T, frames []string, typ string) []string {
	t.Helper()
	var out []string
	for _, fr := range frames {
		if ft, _, ok := frameHeader(json.RawMessage(fr)); ok && ft == typ {
			out = append(out, fr)
		}
	}
	return out
}

// 点选保存位置那一刻的规则预览随回写请求带上；修改反馈后重新生成的候选以最后一次点选时为准；没有预览或形状不对时为空。
func TestOmfgPreviewCaptured(t *testing.T) {
	f := newFixture(t, Config{})
	fs := newFakeSync(false)
	f.s.SetConfigSync(fs)
	th, a, _ := readyThread(f)
	ruleB := strings.Replace(ruleText, "Do not output", "Never output", 1)

	// 修改反馈：候选 A → 选修改 → 预览清空、生成候选 B → 点本项目
	cmd, r := omfg(t, f, th, a)
	preview(t, f, th, ruleText)
	choose(t, f, th, a, "p1", amendLabel)
	preview(t, f, th, "")
	preview(t, f, th, ruleB)
	choose(t, f, th, a, "p2", projectLabel)
	f.respond(th, cmd, true, `{"state":"saved","savedPath":"~/.oryn/project-config/.omp/rules/x.md"}`)
	p := fs.call(t)
	assert.Equal(t, ruleB, p.Preview)
	assert.False(t, p.GlobalChosen)
	r.next(t)

	// 点选之后预览再变（Core 的 clear 在应答之前）不影响已记下的预览
	cmd, r = omfg(t, f, th, a)
	preview(t, f, th, ruleText)
	choose(t, f, th, a, "p3", globalLabel)
	preview(t, f, th, "")
	f.respond(th, cmd, true, savedGlobal)
	p = fs.call(t)
	assert.Equal(t, ruleText, p.Preview)
	assert.True(t, p.GlobalChosen)
	r.next(t)

	// 没有预览、或预览不是「标题 + 空行 + 规则」的形状：空
	for name, lines := range map[string][]string{
		"none":      nil,
		"no-head":   {"something", "", "rule"},
		"no-blank":  {"TTSR rule — matches this conversation", "rule"},
		"head-only": {"TTSR rule — matches this conversation", ""},
	} {
		cmd, r := omfg(t, f, th, a)
		preview(t, f, th, "")
		if lines != nil {
			b, _ := json.Marshal(map[string]any{"type": "extension_ui_request", "id": "w", "method": "setWidget", "widgetKey": "omfg", "widgetLines": lines})
			f.frame(th, string(b))
		}
		choose(t, f, th, a, "p-"+name, globalLabel)
		f.respond(th, cmd, true, savedGlobal)
		assert.Empty(t, fs.call(t).Preview, name)
		r.next(t)
	}
}

// 同一线程同时只受理一条 omfg：保存位置对话框与预览都不带命令 id，第二条无法归属。前一条应答之后可以再发。
func TestOmfgSingleInFlight(t *testing.T) {
	f := newFixture(t, Config{})
	fs := newFakeSync(false)
	f.s.SetConfigSync(fs)
	th, a, _ := readyThread(f)

	cmd, r := omfg(t, f, th, a)
	err := f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"omfg","text":"again"}`), func(json.RawMessage) { t.Fatal("被拒的命令不应有应答") })
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeOmfgInProgress, te.Code)
	assert.Empty(t, sent(a), "被拒的 omfg 不写进 agent 流")
	// 自动化投递同样受限
	_, err = f.s.command(f.ctx, th.id, json.RawMessage(`{"type":"omfg","text":"auto"}`), noReply, originAuto, "")
	require.ErrorAs(t, err, &te)
	assert.Equal(t, CodeOmfgInProgress, te.Code)

	f.respond(th, cmd, true, `{"state":"aborted"}`)
	r.next(t)
	cmd, r = omfg(t, f, th, a)
	f.respond(th, cmd, true, `{"state":"rejected"}`)
	r.next(t)
}

// 授权随命令结束：用户点了全局之后 omfg 因 agent 退出 / 流断开以合成失败结束，下一条 omfg 收到不经对话框的伪造
// saved 应答时 GlobalChosen 为假、没有预览（Security P2-1）。
func TestOmfgChoiceEndsWithCommand(t *testing.T) {
	for _, how := range []string{"exit", "gap"} {
		t.Run(how, func(t *testing.T) {
			f := newFixture(t, Config{})
			fs := newFakeSync(false)
			f.s.SetConfigSync(fs)
			th, a, _ := readyThread(f)

			_, r := omfg(t, f, th, a)
			preview(t, f, th, ruleText)
			choose(t, f, th, a, "u1", globalLabel)
			switch how {
			case "exit":
				raw, _ := json.Marshal(apigen.LinkCtlAgentStatusParams{ThreadID: th.id, Status: "exited"})
				f.s.onAgentStatus(f.ctx, th.orbID, raw)
			case "gap":
				raw, _ := json.Marshal(apigen.LinkCtlAgentGapParams{ThreadID: th.id})
				f.s.onAgentGap(f.ctx, th.orbID, raw)
			}
			assert.False(t, mustRaw(t, r.next(t), "success")[0] == 't', "命令以合成失败结束")

			r2 := make(replies, 1)
			require.NoError(t, f.s.Command(f.ctx, th.id, json.RawMessage(`{"type":"omfg","text":"x"}`), r2.fn()))
			if how == "exit" {
				attach(th, a, apigen.AgentStatusReady)
				th.mu.Lock()
				f.s.onReadyLocked(f.ctx, th)
				th.mu.Unlock()
			}
			cmds := framesOfType(t, sent(a), "omfg")
			require.Len(t, cmds, 1)
			f.respond(th, cmds[0], true, savedGlobal)
			p := fs.call(t)
			assert.False(t, p.GlobalChosen, "上一条命令的授权不留给伪造的 saved 应答")
			assert.Empty(t, p.Preview)
			r2.next(t)
		})
	}
}

// 回写进行中（应答已离开 pending）冻结闸门不放行；回写完成、应答交付后才放行（RuntimeWriteback #2）。
func TestOmfgSyncHoldsQuiesce(t *testing.T) {
	f := newFixture(t, Config{})
	fs := newFakeSync(true)
	f.s.SetConfigSync(fs)
	th, a, _ := readyThread(f)
	cmd, r := omfg(t, f, th, a)
	preview(t, f, th, ruleText)
	choose(t, f, th, a, "u1", globalLabel)
	preview(t, f, th, "")
	f.respond(th, cmd, true, savedGlobal)
	fs.call(t) // 回写已开始，卡在闸门上

	committed := false
	assert.False(t, f.s.TryQuiesce(th.orbID, func() bool { committed = true; return true }), "回写进行中不得冻结")
	assert.False(t, committed)

	close(fs.gate)
	r.next(t)
	th.mu.Lock()
	assert.Zero(t, th.cfgSyncs)
	th.mu.Unlock()
	assert.True(t, f.s.TryQuiesce(th.orbID, func() bool { committed = true; return true }), "回写结束后可以冻结")
	assert.True(t, committed)
}

// 验收 Z7：用户发起的 omfg 在保存位置对话框被取消 → Core 应答 aborted → 应答原样、同步送达客户端，pending 清空，
// 不登记回写、不留授权，线程可以静止（冻结闸门放行）。
func TestOmfgCancelledSaveLocation(t *testing.T) {
	f := newFixture(t, Config{})
	fs := newFakeSync(false)
	f.s.SetConfigSync(fs)
	th, a, _ := readyThread(f)

	cmd, r := omfg(t, f, th, a)
	// Core 的 RPC 镜像：进度行、候选预览、保存位置对话框
	f.frame(th, `{"type":"extension_ui_request","id":"s1","method":"setStatus","statusKey":"omfg","statusText":"/omfg · Choose where to save or amend the TTSR rule…"}`)
	preview(t, f, th, ruleText)
	f.frame(th, fmt.Sprintf(`{"type":"extension_ui_request","id":"u1","method":"select","title":%q,"options":[%q,%q,%q]}`, omfgSaveTitle, projectLabel, globalLabel, amendLabel))
	th.mu.Lock()
	assert.False(t, th.quiescentLocked(), "对话框待答、omfg 在途：不静止")
	th.mu.Unlock()

	// 用户取消：取消帧原样写进 agent 流
	cancel := `{"type":"extension_ui_response","id":"u1","cancelled":true}`
	require.NoError(t, f.s.UIResponse(f.ctx, th.id, json.RawMessage(cancel)))
	assert.Equal(t, []string{cancel}, sent(a))
	th.mu.Lock()
	require.Len(t, th.pending, 1)
	for _, p := range th.pending {
		require.NotNil(t, p.omfg)
		assert.False(t, p.omfg.global)
		assert.Empty(t, p.omfg.preview)
	}
	th.mu.Unlock()

	// Core：markAborted → runRpcOmfg 的 finally 清掉镜像 → 成功应答 {state: aborted}
	f.frame(th, `{"type":"extension_ui_request","id":"s2","method":"setStatus","statusKey":"omfg"}`)
	preview(t, f, th, "")
	f.respond(th, cmd, true, `{"state":"aborted"}`)
	assert.JSONEq(t, respFrame(t, cmd, true, `{"state":"aborted"}`), string(r.next(t)))
	fs.noCall(t)

	th.mu.Lock()
	assert.Empty(t, th.pending)
	assert.Zero(t, th.cfgSyncs)
	assert.True(t, th.quiescentLocked())
	th.mu.Unlock()
	committed := false
	assert.True(t, f.s.TryQuiesce(th.orbID, func() bool { committed = true; return true }))
	assert.True(t, committed)
}
