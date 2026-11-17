package coord

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/library"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// compMachines 在 fsMachines（orbd 本机 API）之上扮演助手的电脑：一台 orb 的状态、创建与唤醒，以及 computer.* ctl。
type compMachines struct {
	*fsMachines
	cmu     sync.Mutex
	orb     *apigen.Orb
	creates []string // provider/type
	events  []string // wake / computer.start / computer.call / computer.cancel
	// wakeReady 为真时 Wake 让电脑立即就绪。
	wakeReady bool
	// tools / version 是 computer.start 的应答。
	tools   []apigen.OmpFrame
	version string
	calls   []computerCallParams
	cancels []string
	// callFn 决定 computer.call 的结果；nil = 回显参数。
	callFn  func(ctx context.Context, p computerCallParams) (json.RawMessage, error)
	update  func(orbID, callID string, partial json.RawMessage)
	touches int
}

func newCompMachines() *compMachines {
	return &compMachines{fsMachines: newFSMachines(), wakeReady: true, version: "v1",
		tools: []apigen.OmpFrame{
			{"name": "bash", "description": "Run a command.", "parameters": map[string]any{"type": "object"}},
			{"name": "read", "description": "Read a file.", "parameters": map[string]any{"type": "object"}},
			{"name": "bogus", "description": "Not asked for."},
		}}
}

func readyOrb(id, version string) *apigen.Orb {
	return &apigen.Orb{ID: id, State: apigen.OrbState(orbs.StateReady), Type: "small", ProviderName: new("docker"),
		Features: []string{"fs", "computer"}, AgentVersion: &version}
}

func (m *compMachines) CreateComputer(_ context.Context, provider, orbType string) (apigen.Orb, error) {
	m.cmu.Lock()
	defer m.cmu.Unlock()
	time.Sleep(20 * time.Millisecond) // 并发调用若没有单飞，会在这里交错
	m.creates = append(m.creates, provider+"/"+orbType)
	m.orb = readyOrb("orb_comp", m.version)
	return *m.orb, nil
}

func (m *compMachines) Computer(context.Context) (apigen.Orb, bool, error) {
	m.cmu.Lock()
	defer m.cmu.Unlock()
	if m.orb == nil {
		return apigen.Orb{}, false, nil
	}
	return *m.orb, true, nil
}

func (m *compMachines) Wake(_ context.Context, orbID string) error {
	m.cmu.Lock()
	defer m.cmu.Unlock()
	m.events = append(m.events, "wake")
	if m.wakeReady {
		m.orb = readyOrb(orbID, m.version)
	}
	return nil
}

func (m *compMachines) Pause(_ context.Context, orbID, mode string) error {
	m.cmu.Lock()
	defer m.cmu.Unlock()
	m.events = append(m.events, "pause:"+mode)
	m.orb.State, m.orb.Features = apigen.OrbState(orbs.StateFrozen), nil
	return nil
}

func (m *compMachines) Delete(_ context.Context, orbID string) error {
	m.cmu.Lock()
	defer m.cmu.Unlock()
	m.events = append(m.events, "delete:"+orbID)
	m.orb = nil
	return nil
}

func (m *compMachines) TouchAgent(context.Context, string) {
	m.cmu.Lock()
	defer m.cmu.Unlock()
	m.touches++
}

func (m *compMachines) OnComputerUpdate(fn func(orbID, callID string, partial json.RawMessage)) {
	m.update = fn
}

func (m *compMachines) ComputerCall(ctx context.Context, orbID, method string, params, result any) error {
	raw, _ := json.Marshal(params)
	m.cmu.Lock()
	m.events = append(m.events, method)
	m.cmu.Unlock()
	var out any
	switch apigen.LinkCtlMethod(method) {
	case apigen.MethodComputerStart:
		m.cmu.Lock()
		out = apigen.LinkCtlComputerStartResult{Tools: m.tools, AgentVersion: m.version}
		m.cmu.Unlock()
	case apigen.MethodComputerCancel:
		var p apigen.LinkCtlComputerCancelParams
		_ = json.Unmarshal(raw, &p)
		m.cmu.Lock()
		m.cancels = append(m.cancels, p.CallID)
		m.cmu.Unlock()
		out = map[string]any{"ok": true}
	case apigen.MethodComputerCall:
		var p computerCallParams
		_ = json.Unmarshal(raw, &p)
		m.cmu.Lock()
		m.calls = append(m.calls, p)
		fn := m.callFn
		m.cmu.Unlock()
		res := json.RawMessage(`{"content":[{"type":"text","text":"echo"}]}`)
		if fn != nil {
			var err error
			if res, err = fn(ctx, p); err != nil {
				return err
			}
		}
		out = map[string]any{"result": res}
	}
	b, _ := json.Marshal(out)
	if result != nil {
		return json.Unmarshal(b, result)
	}
	return nil
}

func (m *compMachines) eventsCopy() []string {
	m.cmu.Lock()
	defer m.cmu.Unlock()
	return append([]string(nil), m.events...)
}

func (m *compMachines) count(ev string) int {
	n := 0
	for _, e := range m.eventsCopy() {
		if e == ev {
			n++
		}
	}
	return n
}

func compToolNames(defs []apigen.OmpFrame) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d["name"].(string))
	}
	return out
}

// M19 4.3：并发的第一次使用只建一台电脑（按助手设置的 provider 与类型）；之后复用。
func TestComputerCreatedOnce(t *testing.T) {
	e := newEnv(t, true)
	m := newCompMachines()
	e.c.SetMachines(m)
	prov, typ := "docker", "small"
	_, err := e.c.PutSettings(e.ctx, apigen.AssistantSettings{WebAccess: true, DisabledMcpServers: []string{},
		Computer: apigen.AssistantComputerSettings{Provider: &prov, OrbType: &typ}})
	require.NoError(t, err)

	var wg sync.WaitGroup
	got := make([]string, 5)
	for i := range got {
		wg.Go(func() {
			id, err := e.c.useComputer(e.ctx)
			assert.NoError(t, err)
			got[i] = id
		})
	}
	wg.Wait()
	assert.Equal(t, []string{"docker/small"}, m.creates, "只建一台")
	for _, id := range got {
		assert.Equal(t, "orb_comp", id)
	}
	assert.Equal(t, 1, m.count(string(apigen.MethodComputerStart)), "工具定义只取一次")
	assert.GreaterOrEqual(t, m.touches, 5, "每次使用记 agent 活动")

	st, err := e.c.ComputerStatus(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, "orb_comp", deref(st.OrbID))
	assert.Equal(t, apigen.OrbState(orbs.StateReady), *st.State)
	assert.True(t, st.ToolsReady)

	// 重置：删除；下一次使用重新创建
	st, err = e.c.ResetComputer(e.ctx)
	require.NoError(t, err)
	assert.Nil(t, st.OrbID)
	assert.Contains(t, m.eventsCopy(), "delete:orb_comp")
	st, err = e.c.ResetComputer(e.ctx)
	require.NoError(t, err, "没有电脑时幂等")
	_, err = e.c.PauseComputer(e.ctx)
	assert.Equal(t, CodeNotFound, errCode(err))
	_, err = e.c.WakeComputer(e.ctx)
	require.NoError(t, err)
	assert.Len(t, m.creates, 2)
	_, err = e.c.PauseComputer(e.ctx)
	require.NoError(t, err)
	assert.Contains(t, m.eventsCopy(), "pause:freeze")
}

// M19 4.3：冻结的电脑在调用前先唤醒；等不到就绪回 computer_starting；link 在但没有 computer 特性回 orbd_outdated。
func TestComputerWakeAndReadyTimeout(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	m := newCompMachines()
	e.c.SetMachines(m)
	e.c.comp.wait, e.c.comp.poll = 300*time.Millisecond, 5*time.Millisecond
	_, err := e.c.useComputer(e.ctx)
	require.NoError(t, err)

	m.cmu.Lock()
	m.orb.State, m.orb.Features = apigen.OrbState(orbs.StateFrozen), nil
	m.events = nil
	m.cmu.Unlock()
	r := e.c.HostToolCall(e.ctx, threads.HostToolCall{ThreadID: asst.ID, Kind: threads.KindAssistant, CallID: "h-1",
		Name: "computer_bash", Args: json.RawMessage(`{"command":"ls"}`)})
	require.False(t, r.IsError, r.Text)
	assert.Equal(t, []string{"wake", string(apigen.MethodComputerCall)}, m.eventsCopy(), "先唤醒再调用")

	// 唤醒后迟迟不就绪
	m.cmu.Lock()
	m.orb.State, m.orb.Features = apigen.OrbState(orbs.StateFrozen), nil
	m.wakeReady = false
	m.cmu.Unlock()
	r = e.call(asst, ToolComputerStatus, `{}`, true)
	assert.Equal(t, CodeComputerStarting, errCodeOf(t, r))
	assert.Contains(t, r.Text, "try again in a minute")
	_, err = e.c.WakeComputer(e.ctx)
	assert.Equal(t, CodeComputerStarting, errCode(err))

	m.cmu.Lock()
	m.orb.State, m.orb.Features = apigen.OrbState(orbs.StateReady), []string{"fs"}
	m.cmu.Unlock()
	assert.Equal(t, "orbd_outdated", errCodeOf(t, e.call(asst, ToolComputerStatus, `{}`, true)))
}

// M19 4.3：工具定义缓存在库里，改名为 computer_<name>、描述加前缀后进助手的工具表；电脑的 omp 版本变化时重取，
// 内容变了让静止的助手对话停掉 agent（下一轮带上新工具）。Space 协调者没有电脑工具。
func TestComputerToolCache(t *testing.T) {
	e := newEnv(t, false)
	m := newCompMachines()
	e.c.SetMachines(m)
	conv, err := e.c.CreateConversation(e.ctx, threads.CoordinatorParams{Prompt: "hi"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		th, err := e.th.Get(e.ctx, conv.ID)
		return err == nil && th.State == apigen.ThreadStateIdle
	}, 10*time.Second, 20*time.Millisecond)
	stops := func() int {
		n := 0
		for _, c := range e.orbs.Calls() {
			if c == "agent.stop" {
				n++
			}
		}
		return n
	}

	prof, err := e.c.AgentProfile(e.ctx, conv.ID)
	require.NoError(t, err)
	names := compToolNames(prof.Tools)
	assert.Contains(t, names, ToolComputerStatus)
	assert.Contains(t, names, ToolComputerUpload)
	assert.Contains(t, names, ToolComputerDownload)
	assert.NotContains(t, names, "computer_bash", "还没有缓存")
	assert.Contains(t, prof.SystemPrompt, "then your own computer")

	before := stops()
	st, err := e.c.toolComputerStatus(e.ctx)
	require.NoError(t, err)
	assert.Contains(t, st.(map[string]any)["note"], "next turn")
	require.Eventually(t, func() bool { return stops() > before }, 10*time.Second, 20*time.Millisecond, "缓存变化后助手对话重启")

	prof, err = e.c.AgentProfile(e.ctx, conv.ID)
	require.NoError(t, err)
	var bash apigen.OmpFrame
	for _, d := range prof.Tools {
		if d["name"] == "computer_bash" {
			bash = d
		}
	}
	require.NotNil(t, bash)
	assert.Equal(t, "[On your computer] Run a command.", bash["description"])
	assert.Equal(t, map[string]any{"type": "object"}, bash["parameters"])
	names = compToolNames(prof.Tools)
	assert.Contains(t, names, "computer_read")
	assert.NotContains(t, names, "computer_bogus", "只取所列的工具")
	assert.Equal(t, "unknown_tool", errCodeOf(t, e.call(conv, "computer_bogus", `{}`, true)))

	// 同版本不重取
	_, err = e.c.useComputer(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, m.count(string(apigen.MethodComputerStart)))

	// 版本变化：重取；内容变了 → 再次让（重新拉起的）助手对话停掉 agent
	starts := func() int {
		n := 0
		for _, c := range e.orbs.Calls() {
			if c == "agent.start" {
				n++
			}
		}
		return n
	}
	started := starts()
	_, err = e.th.Deliver(e.ctx, conv.ID, threads.AutoMessage{Text: "again"}, nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		th, err := e.th.Get(e.ctx, conv.ID)
		return err == nil && th.State == apigen.ThreadStateIdle && starts() > started
	}, 10*time.Second, 20*time.Millisecond)
	before = stops()
	m.cmu.Lock()
	m.version = "v2"
	m.tools = m.tools[:1]
	m.orb.AgentVersion = new("v2")
	m.cmu.Unlock()
	_, err = e.c.useComputer(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, m.count(string(apigen.MethodComputerStart)))
	tools, version, err := e.c.cachedComputerTools(e.ctx)
	require.NoError(t, err)
	assert.Equal(t, "v2", version)
	assert.NotContains(t, tools, `"read"`)
	require.Eventually(t, func() bool { return stops() > before }, 10*time.Second, 20*time.Millisecond, "版本变化后定义变了，助手对话重启")

	chat := e.spaceChat(e.space("Alpha", ""))
	prof, err = e.c.AgentProfile(e.ctx, chat.ID)
	require.NoError(t, err)
	for _, n := range compToolNames(prof.Tools) {
		assert.False(t, strings.HasPrefix(n, computerToolPrefix), n)
	}
	assert.Equal(t, "unknown_tool", errCodeOf(t, e.call(chat, "computer_bash", `{"command":"id"}`, true)))
	assert.Equal(t, "unknown_tool", errCodeOf(t, e.call(chat, ToolComputerStatus, `{}`, true)))
}

// M19 4.3：computer_<name> 原样转发参数（callId 由控制面生成、timeoutSec 1800），结果（含 image 块）原样透传；
// 运行中的 computer.update 转给本次调用的 Update，别的 orb 发来的丢弃；取消（ctx 结束）时向 orbd 发 computer.cancel。
func TestComputerProxy(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	m := newCompMachines()
	e.c.SetMachines(m)
	_, err := e.c.useComputer(e.ctx)
	require.NoError(t, err)

	result := `{"content":[{"type":"text","text":"Linux"},{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}],"details":{"exitCode":0}}`
	m.callFn = func(_ context.Context, p computerCallParams) (json.RawMessage, error) {
		m.update("orb_other", p.CallID, json.RawMessage(`{"content":[{"type":"text","text":"forged"}]}`))
		m.update("orb_comp", p.CallID, json.RawMessage(`{"content":[{"type":"text","text":"Lin"}]}`))
		return json.RawMessage(result), nil
	}
	var partials []string
	r := e.c.HostToolCall(e.ctx, threads.HostToolCall{ThreadID: asst.ID, Kind: threads.KindAssistant, CallID: "h-1",
		Name: "computer_bash", Args: json.RawMessage(`{"command":"uname -a","timeout":5}`),
		Update: func(p json.RawMessage) { partials = append(partials, string(p)) }})
	require.False(t, r.IsError, r.Text)
	assert.JSONEq(t, result, string(r.Raw), "结果原样透传")
	assert.Equal(t, []string{`{"content":[{"type":"text","text":"Lin"}]}`}, partials)
	require.Len(t, m.calls, 1)
	c := m.calls[0]
	assert.Equal(t, "bash", c.Tool)
	assert.JSONEq(t, `{"command":"uname -a","timeout":5}`, string(c.Args))
	assert.Equal(t, 1800, c.TimeoutSec)
	assert.True(t, strings.HasPrefix(c.CallID, "ccl"), c.CallID)
	assert.Empty(t, e.c.comp.calls, "调用结束后注销")
	assert.Empty(t, e.actions(), "电脑工具不写审计")

	// isError 的结果同样透传
	m.callFn = func(context.Context, computerCallParams) (json.RawMessage, error) {
		return json.RawMessage(`{"content":[{"type":"text","text":"exit 1"}],"isError":true}`), nil
	}
	r = e.call(asst, "computer_bash", `{"command":"false"}`, true)
	assert.JSONEq(t, `{"content":[{"type":"text","text":"exit 1"}],"isError":true}`, string(r.Raw))

	// 取消
	started := make(chan struct{})
	m.callFn = func(ctx context.Context, _ computerCallParams) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan threads.HostToolResult, 1)
	go func() {
		done <- e.c.HostToolCall(ctx, threads.HostToolCall{ThreadID: asst.ID, Kind: threads.KindAssistant, CallID: "h-3",
			Name: "computer_read", Args: json.RawMessage(`{"path":"/etc/hostname"}`)})
	}()
	<-started
	cancel()
	r = <-done
	assert.True(t, r.IsError)
	require.Len(t, m.calls, 3)
	assert.Equal(t, []string{m.calls[2].CallID}, m.cancels, "取消传到 orbd")
}

// M19 4.3：computer_upload 把文件库文件或沙箱文件放到电脑上；computer_download 把电脑上的文件存进文件库并同步到
// 协调宿主，超过上限报 too_large。
func TestComputerUploadDownload(t *testing.T) {
	e := newEnv(t, true)
	lib, _, host := e.withLibrary()
	asst := e.assistant()
	m := newCompMachines()
	e.c.SetMachines(m)
	m.files["/home/u/out/chart.png"] = "\x89PNG-chart"

	d := details(t, e.call(asst, ToolComputerDownload, `{"path":"/home/u/out/chart.png"}`, true))
	f, err := lib.Get(e.ctx, d["id"].(string))
	require.NoError(t, err)
	assert.Equal(t, "chart.png", f.Name)
	assert.Equal(t, library.SourceAssistant, f.Source)
	assert.Equal(t, asst.ID, f.ThreadID)
	assert.Equal(t, library.ScopeConversation, f.Scope)
	assert.Equal(t, []string{deref(e.row(asst.ID).OrbID)}, host.syncs, "存库后同步到协调宿主")
	assert.Equal(t, fsReq{orb: "orb_comp", method: "GET", path: "/fs/file", query: map[string]string{"path": "/home/u/out/chart.png"}}, m.last())
	assert.Len(t, m.creates, 1, "第一次用到时创建")
	e.c.xferMax = 4
	assert.Equal(t, CodeTooLarge, errCodeOf(t, e.call(asst, ToolComputerDownload, `{"path":"/home/u/out/chart.png"}`, true)))
	e.c.xferMax = 0

	details(t, e.call(asst, ToolComputerUpload, `{"path":"/home/u/in/chart.png","file":"`+f.ID+`"}`, true))
	assert.Equal(t, "\x89PNG-chart", m.files["/home/u/in/chart.png"])
	assert.Equal(t, "false", m.last().query["overwrite"])
	assert.Empty(t, m.last().workdir, "电脑不带工作目录头")
	host.files["~/tmp/img.png"] = []byte("img")
	details(t, e.call(asst, ToolComputerUpload, `{"path":"/home/u/in/chart.png","source":"~/tmp/img.png","overwrite":true}`, true))
	assert.Equal(t, "img", m.files["/home/u/in/chart.png"])
	for _, args := range []string{
		`{"path":"/x"}`,
		`{"file":"` + f.ID + `"}`,
		`{"path":"/x","file":"` + f.ID + `","source":"~/tmp/img.png"}`,
		`{"path":"/x","source":"~/.omp/agent/models.yml"}`,
	} {
		assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolComputerUpload, args, true)), args)
	}
	assert.Empty(t, e.actions(), "电脑工具不写审计")
}

// M19 4.3：电脑工具的调用上限：computer_* 30 分钟，上传与下载 5 分钟。
func TestComputerHostToolTimeout(t *testing.T) {
	var s Service
	assert.Equal(t, 30*time.Minute, s.HostToolTimeout("computer_bash"))
	assert.Equal(t, 30*time.Minute, s.HostToolTimeout(ToolComputerStatus))
	assert.Equal(t, 5*time.Minute, s.HostToolTimeout(ToolComputerUpload))
	assert.Equal(t, 5*time.Minute, s.HostToolTimeout(ToolComputerDownload))
}
