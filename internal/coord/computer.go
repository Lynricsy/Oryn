package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/library"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 第二层：助手的电脑（M19 步骤 4.3）。全库至多一台 computer = 1 的普通 orb，主线与所有助手对话共用；第一次用到时创建，
// 冻结或停机时在调用前唤醒。助手进程仍在协调宿主的沙箱里，经宿主工具遥控 orb 里不调模型的 omp 工具执行器
// （orbd 的 computer.* ctl）：computer_<name> 原样转发参数，结果（含 image 块）原样透传，流式输出经 host_tool_update
// 转给 omp，取消传到 orbd。操作只落在助手自己的电脑上，不写审计（同 artifact 工具）。

// 工具名与错误码。
const (
	computerToolPrefix   = "computer_"
	ToolComputerStatus   = "computer_status"
	ToolComputerUpload   = "computer_upload"
	ToolComputerDownload = "computer_download"
	// CodeComputerStarting 是助手的电脑在等待时长内没有就绪（HTTP 409）。
	CodeComputerStarting = "computer_starting"
)

// 时长与特性。
const (
	// computerReadyWait 是等待电脑就绪（hello 含 computer）的上限。
	computerReadyWait = 90 * time.Second
	// computerPoll 是等待就绪时的复查间隔。
	computerPoll = 500 * time.Millisecond
	// computerCallTimeout 是 computer_* 单次调用的上限（同时作为 computer.call 的 timeoutSec 下发）。
	computerCallTimeout = 30 * time.Minute
	// computerCancelWait 是调用被取消后发 computer.cancel 的应答上限。
	computerCancelWait = 10 * time.Second
	// computerTouchEvery 是长调用期间记 agent 活动的间隔（电脑没有线程，靠它推迟空闲冻结）。
	computerTouchEvery = time.Minute
	// featureComputer 是 orbd 在 hello 里上报的工具执行器特性。
	featureComputer = "computer"
	// computerDescPrefix 是电脑里的工具描述的前缀。
	computerDescPrefix = "[On your computer] "
)

// computerTools 是向执行器要定义的内置工具（computer.start 的 names）。
var computerTools = []string{"bash", "read", "write", "edit", "grep", "glob", "find", "ast_grep", "ast_edit", "eval", "lsp", "wait"}

// computerState 是助手的电脑的进程内状态；零值可用。
type computerState struct {
	once sync.Once
	// sem 让创建、唤醒与工具定义刷新单飞（容量 1 的信号量，等待可随 ctx 放弃）。
	sem chan struct{}
	mu  sync.Mutex
	// calls 是在途的 computer.call：callId → 所在 orb 与流式输出的去处。
	calls map[string]computerCall
	// wait / poll（仅测试）覆盖 computerReadyWait / computerPoll。
	wait, poll time.Duration
}

type computerCall struct {
	orbID  string
	update func(json.RawMessage)
}

func (c *computerState) lock(ctx context.Context) error {
	c.once.Do(func() { c.sem = make(chan struct{}, 1) })
	select {
	case c.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *computerState) unlock() { <-c.sem }

func (c *computerState) register(callID string, cc computerCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == nil {
		c.calls = map[string]computerCall{}
	}
	c.calls[callID] = cc
}

func (c *computerState) unregister(callID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.calls, callID)
}

// onComputerUpdate 把 orbd 的 computer.update 转给对应调用的 host_tool_update；不认识的调用或来自别的 orb 的丢弃。
func (s *Service) onComputerUpdate(orbID, callID string, partial json.RawMessage) {
	s.comp.mu.Lock()
	cc, ok := s.comp.calls[callID]
	s.comp.mu.Unlock()
	if !ok || cc.orbID != orbID || cc.update == nil {
		return
	}
	cc.update(partial)
}

// hasComputerTools 报告缓存的工具定义是否非空。
func hasComputerTools(tools string) bool { return tools != "" && tools != "[]" }

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// useComputer 返回可用的助手的电脑（单飞）：没有则按助手设置的 provider 与类型创建；非 ready 则唤醒并等到 ready 且
// hello 含 computer，至多 computerReadyWait（超时 computer_starting）。随后记一次 agent 活动，并在需要时刷新工具定义缓存。
func (s *Service) useComputer(ctx context.Context) (string, error) {
	m, err := s.machinesOr()
	if err != nil {
		return "", err
	}
	if err := s.comp.lock(ctx); err != nil {
		return "", err
	}
	defer s.comp.unlock()
	o, ok, err := m.Computer(ctx)
	if err != nil {
		return "", err
	}
	if !ok || o.State == apigen.OrbState(orbs.StateDeleting) || o.State == apigen.OrbState(orbs.StateDeleted) {
		a, err := s.loadSettings(ctx)
		if err != nil {
			return "", err
		}
		if o, err = m.CreateComputer(ctx, derefStr(a.provider), derefStr(a.orbTyp)); err != nil {
			if errors.Is(err, orbs.ErrNoProvider) || errors.Is(err, orbs.ErrBadType) {
				return "", errf(CodeInvalid, "cannot create your computer: %s", err.Error())
			}
			return "", err
		}
		slog.Info("coord: 已创建助手的电脑", "orb", o.ID)
	}
	wait, poll := computerReadyWait, computerPoll
	if s.comp.wait > 0 {
		wait = s.comp.wait
	}
	if s.comp.poll > 0 {
		poll = s.comp.poll
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	woke := false
	for {
		ready, err := computerReady(o)
		if err != nil {
			return "", err
		}
		if ready {
			break
		}
		switch string(o.State) {
		case orbs.StateFrozen, orbs.StateStopped, orbs.StateFailed, orbs.StateFreezing, orbs.StateStopping:
			if !woke {
				if err := m.Wake(ctx, o.ID); err != nil {
					return "", err
				}
				woke = true
			}
		case orbs.StateDeleting, orbs.StateDeleted:
			return "", orbs.ErrOrbGone
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", errf(CodeComputerStarting, "your computer is still starting; try again in a minute")
		case <-tick.C:
		}
		if o, ok, err = m.Computer(ctx); err != nil {
			return "", err
		}
		if !ok {
			return "", orbs.ErrOrbGone
		}
	}
	m.TouchAgent(ctx, o.ID)
	if err := s.refreshComputerTools(ctx, m, o); err != nil {
		// 工具定义取不到不影响文件传输；computer_* 调用会各自报错
		slog.Warn("coord: 取助手的电脑的工具定义失败", "orb", o.ID, "err", err)
	}
	return o.ID, nil
}

// computerReady 报告电脑是否可用：ready 且已 hello 的 link 上报了 computer。link 在但没有该特性 → orbd_outdated。
func computerReady(o apigen.Orb) (bool, error) {
	if string(o.State) != orbs.StateReady || o.Features == nil {
		return false, nil
	}
	if !slices.Contains(o.Features, featureComputer) {
		return false, orbs.ErrOrbdOutdated
	}
	return true, nil
}

// refreshComputerTools 在缓存为空或缓存的版本与电脑的 omp 版本不同时，向执行器要一次工具定义并存库；内容变了就让
// 全部未归档的助手对话在静止后重启 agent（下一轮带上新的 computer_* 工具）。调用方持有单飞锁。
func (s *Service) refreshComputerTools(ctx context.Context, m Machines, o apigen.Orb) error {
	tools, version, err := s.cachedComputerTools(ctx)
	if err != nil {
		return err
	}
	if hasComputerTools(tools) && (o.AgentVersion == nil || *o.AgentVersion == version) {
		return nil
	}
	var res apigen.LinkCtlComputerStartResult
	if err := m.ComputerCall(ctx, o.ID, string(apigen.MethodComputerStart), apigen.LinkCtlComputerStartParams{Names: computerTools}, &res); err != nil {
		return err
	}
	if res.Tools == nil {
		res.Tools = []apigen.OmpFrame{}
	}
	b, err := json.Marshal(res.Tools)
	if err != nil {
		return err
	}
	fresh := string(b)
	if err := s.st.Q.SetAssistantComputerTools(ctx, db.SetAssistantComputerToolsParams{
		ComputerTools: &fresh, ComputerToolsVersion: &res.AgentVersion, UpdatedAt: s.nowMs(),
	}); err != nil {
		return err
	}
	if fresh != tools {
		slog.Info("coord: 助手的电脑的工具定义已更新", "orb", o.ID, "version", res.AgentVersion, "tools", len(res.Tools))
		s.refreshConversations(ctx)
	}
	return nil
}

// cachedComputerTools 返回缓存的工具定义（JSON 数组；空串 = 没有缓存）与其 omp 版本。
func (s *Service) cachedComputerTools(ctx context.Context) (tools, version string, err error) {
	row, err := s.st.Q.GetAssistantSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return derefStr(row.ComputerTools), derefStr(row.ComputerToolsVersion), nil
}

// computerToolList 解析缓存的工具定义，只保留 computerTools 里的名字。
func (s *Service) computerToolList(ctx context.Context) []apigen.OmpFrame {
	raw, _, err := s.cachedComputerTools(ctx)
	if err != nil {
		slog.Warn("coord: 读取助手的电脑的工具定义失败", "err", err)
		return nil
	}
	if raw == "" {
		return nil
	}
	var defs []apigen.OmpFrame
	if err := json.Unmarshal([]byte(raw), &defs); err != nil {
		slog.Warn("coord: 助手的电脑的工具定义无法解析", "err", err)
		return nil
	}
	out := defs[:0]
	for _, d := range defs {
		if name, _ := d["name"].(string); slices.Contains(computerTools, name) {
			out = append(out, d)
		}
	}
	return out
}

// computerDefs 返回追加到助手工具表的电脑工具：缓存的定义改名为 computer_<name>，描述加前缀。
func (s *Service) computerDefs(ctx context.Context) []apigen.OmpFrame {
	defs := s.computerToolList(ctx)
	out := make([]apigen.OmpFrame, 0, len(defs))
	for _, d := range defs {
		c := make(apigen.OmpFrame, len(d))
		for k, v := range d {
			c[k] = v
		}
		desc, _ := d["description"].(string)
		c["name"] = computerToolPrefix + d["name"].(string)
		c["description"] = computerDescPrefix + desc
		out = append(out, c)
	}
	return out
}

// computerToolDefs 是始终给全局助手的三个电脑工具（其余 computer_* 来自缓存，见 computerDefs）。
func computerToolDefs() []apigen.OmpFrame {
	return []apigen.OmpFrame{
		tool(ToolComputerStatus, "Start or wake your computer (a persistent Linux machine that is yours alone) and report its state. The first time, the other computer_* tools become available from your next turn.", obj{}),
		tool(ToolComputerUpload, "Copy a file onto your computer: a library file (file) or a file under ~/library/out/ or ~/tmp/ (source).", obj{
			"file":      str("Library file id (afl_…)."),
			"source":    str("Path under ~/library/out/ or ~/tmp/."),
			"path":      str("Destination path on your computer (absolute, or relative to its home directory)."),
			"overwrite": boolean("Replace an existing file (default false)."),
		}, "path"),
		tool(ToolComputerDownload, "Save a file from your computer into the owner's library (at most 100 MiB).", obj{
			"path":   str("Path of the file on your computer."),
			"name":   str("File name in the library (default: the file's name)."),
			"shared": boolean("Share it with every conversation (default: only this conversation)."),
		}, "path"),
	}
}

// computerTool 分派电脑工具：只给全局助手。上传与下载按文件类工具限速。
func (s *Service) computerTool(ctx context.Context, c toolCtx) threads.HostToolResult {
	if c.Kind != threads.KindAssistant {
		return failResult(errf("unknown_tool", "no tool named %q", c.Name))
	}
	var (
		v   any
		err error
	)
	switch c.Name {
	case ToolComputerStatus:
		v, err = s.toolComputerStatus(ctx)
	case ToolComputerUpload, ToolComputerDownload:
		if !s.lim.allow(c.ThreadID+"|file", s.cfg.Now(), fileRules) {
			return failResult(errf(CodeRateLimited, "file tool limit reached (120 per 10 minutes per conversation)"))
		}
		if c.Name == ToolComputerUpload {
			v, err = s.toolComputerUpload(ctx, c)
		} else {
			v, err = s.toolComputerDownload(ctx, c)
		}
	default:
		return s.computerProxy(ctx, c)
	}
	if err != nil {
		return failResult(err)
	}
	return threads.HostToolResult{Text: jsonText(v), Details: v}
}

// computerCallParams 是 computer.call 的参数（args 原样转发，不经 map 往返）。
type computerCallParams struct {
	CallID     string          `json:"callId"`
	Tool       string          `json:"tool"`
	Args       json.RawMessage `json:"args"`
	TimeoutSec int             `json:"timeoutSec"`
}

// computerProxy 把 computer_<name> 转给电脑里的执行器：结果原样透传，流式输出转成 host_tool_update，调用被取消
// （omp 取消或超时）时向 orbd 发 computer.cancel。
func (s *Service) computerProxy(ctx context.Context, c toolCtx) threads.HostToolResult {
	name := strings.TrimPrefix(c.Name, computerToolPrefix)
	if !slices.ContainsFunc(s.computerToolList(ctx), func(d apigen.OmpFrame) bool { return d["name"] == name }) {
		return failResult(errf("unknown_tool", "no tool named %q (call computer_status first)", c.Name))
	}
	orbID, err := s.useComputer(ctx)
	if err != nil {
		return failResult(err)
	}
	m := s.machines
	// callId 由控制面生成：omp 的调用 id 只在一个对话内唯一
	callID := ids.New("ccl")
	s.comp.register(callID, computerCall{orbID: orbID, update: c.Update})
	defer s.comp.unregister(callID)
	args := c.Args
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	stop := s.touchWhileRunning(ctx, m, orbID)
	var res struct {
		Result json.RawMessage `json:"result"`
	}
	err = m.ComputerCall(ctx, orbID, string(apigen.MethodComputerCall), computerCallParams{
		CallID: callID, Tool: name, Args: args, TimeoutSec: int(computerCallTimeout / time.Second),
	}, &res)
	stop()
	if err != nil {
		if ctx.Err() != nil {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), computerCancelWait)
			if cerr := m.ComputerCall(cctx, orbID, string(apigen.MethodComputerCancel), apigen.LinkCtlComputerCancelParams{CallID: callID}, nil); cerr != nil {
				slog.Warn("coord: 取消助手的电脑里的工具失败", "orb", orbID, "call", callID, "err", cerr)
			}
			cancel()
		}
		return failResult(err)
	}
	if len(res.Result) == 0 || string(res.Result) == "null" {
		return failResult(errf("orbd_error", "your computer answered without a result"))
	}
	return threads.HostToolResult{Raw: res.Result}
}

// touchWhileRunning 在调用期间每 computerTouchEvery 记一次 agent 活动（电脑没有线程，空闲冻结只看活动时间）；返回停止函数。
func (s *Service) touchWhileRunning(ctx context.Context, m Machines, orbID string) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(computerTouchEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				m.TouchAgent(context.WithoutCancel(ctx), orbID)
			}
		}
	}()
	return func() { close(done) }
}

// toolComputerStatus 执行 computer_status：确保电脑可用并返回其状态。
func (s *Service) toolComputerStatus(ctx context.Context) (any, error) {
	if _, err := s.useComputer(ctx); err != nil {
		return nil, err
	}
	st, err := s.ComputerStatus(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"orbId": st.OrbID, "state": st.State, "type": st.Type, "providerName": st.ProviderName, "toolsReady": st.ToolsReady}
	if st.ToolsReady {
		out["note"] = "Your computer is ready. If the computer_* tools are not in your tool list yet, they become available from your next turn."
	}
	return out, nil
}

// computerFileArgs 是 computer_upload / computer_download 的参数。
type computerFileArgs struct {
	Path      string `json:"path"`
	File      string `json:"file"`
	Source    string `json:"source"`
	Overwrite bool   `json:"overwrite"`
	Name      string `json:"name"`
	Shared    bool   `json:"shared"`
}

// toolComputerUpload 把文件库文件或协调宿主沙箱里的文件放到电脑上（orbd 的 PUT /fs/file）。
func (s *Service) toolComputerUpload(ctx context.Context, c toolCtx) (any, error) {
	var a computerFileArgs
	if err := decodeArgs(c.Args, &a); err != nil {
		return nil, err
	}
	p := strings.TrimSpace(a.Path)
	file, source := strings.TrimSpace(a.File), strings.TrimSpace(a.Source)
	switch {
	case p == "":
		return nil, errf(CodeInvalid, "path is required")
	case (file == "") == (source == ""):
		return nil, errf(CodeInvalid, "pass exactly one of file and source")
	}
	if err := s.libraryReady(); err != nil {
		return nil, err
	}
	orbID, err := s.useComputer(ctx)
	if err != nil {
		return nil, err
	}
	var (
		body io.ReadCloser
		size int64
	)
	if file != "" {
		f, err := s.visibleFile(ctx, c.ThreadID, file)
		if err != nil {
			return nil, err
		}
		rc, _, err := s.lib.Open(ctx, f.ID)
		if err != nil {
			return nil, libErr(err)
		}
		body, size = rc, f.Size
	} else if body, size, err = s.fetchSource(ctx, c.ThreadID, source); err != nil {
		return nil, err
	}
	defer body.Close()
	if size > s.transferMax() {
		return nil, errf(CodeTooLarge, "the file exceeds %d MiB", s.transferMax()>>20)
	}
	res, err := s.orbDoAt(ctx, s.machines, orbID, nil, orbRequest{method: http.MethodPut, path: "/fs/file",
		query: writeQuery(p, a.Overwrite), body: body, size: size})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var e apigen.FsEntry
	if err := json.NewDecoder(io.LimitReader(res.Body, orbJSONMax)).Decode(&e); err != nil {
		return nil, errf("orbd_error", "your computer answered an invalid response: %s", err.Error())
	}
	return map[string]any{"path": e.Path, "size": e.Size}, nil
}

// toolComputerDownload 把电脑上的文件存进文件库（上限 transferMax），并同步到本对话所在的协调宿主。
func (s *Service) toolComputerDownload(ctx context.Context, c toolCtx) (any, error) {
	if err := s.libraryReady(); err != nil {
		return nil, err
	}
	var a computerFileArgs
	if err := decodeArgs(c.Args, &a); err != nil {
		return nil, err
	}
	p := strings.TrimSpace(a.Path)
	if p == "" {
		return nil, errf(CodeInvalid, "path is required")
	}
	convOrb, err := s.conversationOrb(ctx, c.ThreadID)
	if err != nil {
		return nil, err
	}
	orbID, err := s.useComputer(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = path.Base(p)
	}
	scope := library.ScopeConversation
	if a.Shared {
		scope = library.ScopeShared
	}
	tooLarge := errf(CodeTooLarge, "the file exceeds %d MiB", s.transferMax()>>20)
	res, err := s.orbDoAt(ctx, s.machines, orbID, nil, orbRequest{method: http.MethodGet, path: "/fs/file", query: url.Values{"path": {p}}})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.ContentLength > s.transferMax() {
		return nil, tooLarge
	}
	f, err := s.lib.Put(ctx, library.PutParams{
		Name: name, ThreadID: c.ThreadID, Scope: scope, Source: library.SourceAssistant,
		R: &capReader{r: res.Body, max: s.transferMax()},
	})
	if errors.Is(err, errTransferTooLarge) || errors.Is(err, library.ErrTooLarge) {
		return nil, tooLarge
	}
	if err != nil {
		return nil, libErr(err)
	}
	s.syncLibrary(ctx, convOrb)
	return toFileItem(f), nil
}

// ---- REST ----

// ComputerStatus 返回助手的电脑的状态（还没有时 orbId 省略）。
func (s *Service) ComputerStatus(ctx context.Context) (apigen.AssistantComputer, error) {
	m, err := s.machinesOr()
	if err != nil {
		return apigen.AssistantComputer{}, err
	}
	tools, _, err := s.cachedComputerTools(ctx)
	if err != nil {
		return apigen.AssistantComputer{}, err
	}
	out := apigen.AssistantComputer{ToolsReady: hasComputerTools(tools)}
	o, ok, err := m.Computer(ctx)
	if err != nil {
		return apigen.AssistantComputer{}, err
	}
	if ok {
		out.OrbID, out.State, out.ProviderName, out.Type = &o.ID, &o.State, o.ProviderName, &o.Type
	}
	return out, nil
}

// WakeComputer 唤醒助手的电脑（没有则创建），等到就绪。
func (s *Service) WakeComputer(ctx context.Context) (apigen.AssistantComputer, error) {
	if _, err := s.useComputer(ctx); err != nil {
		return apigen.AssistantComputer{}, codedOrbErr(err)
	}
	return s.ComputerStatus(ctx)
}

// PauseComputer 冻结助手的电脑；没有 → not_found。
func (s *Service) PauseComputer(ctx context.Context) (apigen.AssistantComputer, error) {
	m, err := s.machinesOr()
	if err != nil {
		return apigen.AssistantComputer{}, err
	}
	o, ok, err := m.Computer(ctx)
	if err != nil {
		return apigen.AssistantComputer{}, err
	}
	if !ok {
		return apigen.AssistantComputer{}, errf(CodeNotFound, "the assistant has no computer yet")
	}
	if err := m.Pause(ctx, o.ID, orbs.PauseFreeze); err != nil {
		return apigen.AssistantComputer{}, codedOrbErr(err)
	}
	return s.ComputerStatus(ctx)
}

// ResetComputer 删除助手的电脑（没有时无操作）；下次用到时重新创建。
func (s *Service) ResetComputer(ctx context.Context) (apigen.AssistantComputer, error) {
	m, err := s.machinesOr()
	if err != nil {
		return apigen.AssistantComputer{}, err
	}
	if err := s.comp.lock(ctx); err != nil {
		return apigen.AssistantComputer{}, err
	}
	defer s.comp.unlock()
	o, ok, err := m.Computer(ctx)
	if err != nil {
		return apigen.AssistantComputer{}, err
	}
	if ok && string(o.State) != orbs.StateDeleting && string(o.State) != orbs.StateDeleted {
		if err := m.Delete(ctx, o.ID); err != nil {
			return apigen.AssistantComputer{}, codedOrbErr(err)
		}
		slog.Info("coord: 已重置助手的电脑", "orb", o.ID)
	}
	tools, _, err := s.cachedComputerTools(ctx)
	if err != nil {
		return apigen.AssistantComputer{}, err
	}
	return apigen.AssistantComputer{ToolsReady: hasComputerTools(tools)}, nil
}

// codedOrbErr 把 orbs 的哨兵错误转成带码的协调层错误（REST 按码映射状态）；其余原样返回。
func codedOrbErr(err error) error {
	if code := orbErrCode(err); code != "" {
		return errf(code, "%s", err.Error())
	}
	return err
}
