package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/omprpc"
)

// 助手的电脑里的工具执行器（M19 步骤 4.2）：控制面经 computer.* 遥控一个不调模型的 omp
// （`--mode rpc --no-session`），用 Oryn Core 的 oryn_tool_defs / oryn_tool_call / oryn_tool_cancel 直接执行内置工具。
//
// 进程惰性拉起（第一条 computer.start / computer.call），初始化是等 `ready` 再协商协议 v2；之后命令按 id 配对应答，
// 工具调用可以并发。`oryn_tool_update` 帧转成 computer.update 通知；需要回答的 extension_ui_request 一律回 cancelled。
// 进程退出时在途调用以 agent_exited 失败，下一条调用重新拉起；连续 ComputerIdleExit 没有调用则停掉进程。
// 只有普通 orb 有它（hello 特性 `computer`）；runner 与协调宿主对 computer.* 回 not_supported。

const (
	// DefaultComputerIdleExit 是执行器没有任何调用后停掉进程的时长。
	DefaultComputerIdleExit = 30 * time.Minute
	// computerDefaultTimeoutSec / computerMaxTimeoutSec 是 computer.call 的缺省与最大时限（秒）。
	computerDefaultTimeoutSec = 120
	computerMaxTimeoutSec     = 1800
	// computerCancelWait 是等 oryn_tool_cancel 应答的上限。
	computerCancelWait = 10 * time.Second
	// computerStopGrace 是 computer.stop / 空闲退出时关闭 stdin 后等进程自行退出的时长，超时杀进程组。
	computerStopGrace = 5 * time.Second
	// computerIDPrefix 是 orbd 发给执行器的命令 id 前缀。
	computerIDPrefix = "cmp-"
)

// computerOverlay 是执行器的设置覆盖层（--config）：打开默认会话里关着的 ast_grep 与 find
// （find 用 judge 模型角色判断，orb 的 omp 配置里没有可用模型时调用会以 isError 失败）。
const computerOverlay = `# 由 orbd 生成：助手的电脑里的工具执行器打开 ast_grep 与 find。
astGrep: { enabled: true }
find: { enabled: "on" }
`

// computerOverlayPath 是 computerOverlay 的位置（writeAgentAssets 写出）。
func (r *Runtime) computerOverlayPath() string {
	return filepath.Join(r.home, ".oryn", "core", "computer.yml")
}

// computer.* 的错误码。
const (
	// CodeTimeout：调用超过 timeoutSec，已向执行器发出取消。
	CodeTimeout = "timeout"
	// CodeAgentExited：执行器在调用结束前退出（崩溃或被停止）；下一条调用会重新拉起。
	CodeAgentExited = "agent_exited"
	// CodeAgentStartFailed：执行器拉起或初始化失败。
	CodeAgentStartFailed = "agent_start_failed"
	// CodeToolRejected：执行器拒绝了命令（未知工具、参数校验失败、callId 重复、被拒的工具）。
	CodeToolRejected = "tool_rejected"
)

// computerMethods 是工具执行器的 ctl 方法（控制面 → orbd）。
var computerMethods = map[string]bool{
	string(apigen.MethodComputerStart):  true,
	string(apigen.MethodComputerCall):   true,
	string(apigen.MethodComputerCancel): true,
	string(apigen.MethodComputerStop):   true,
}

// errComputerExited 表示执行器在应答之前退出。
var errComputerExited = errors.New("the tool executor exited")

// computerDialogs 是需要回答的 extension_ui_request（同 artifacts/sample）。
var computerDialogs = map[string]bool{
	"select": true, "confirm": true, "askDialog": true, "input": true, "editor": true, "ask": true, "form": true,
}

// computer 管理唯一的执行器进程。
type computer struct {
	r    *Runtime
	idle time.Duration
	seq  atomic.Uint64

	mu   sync.Mutex
	proc *computerProc // 在跑或正在初始化的进程；nil = 没有
	// inflight 是进行中的 computer.start / computer.call 数；为 0 时空闲计时。
	inflight int
	idleTm   *time.Timer
}

func newComputer(r *Runtime) *computer {
	idle := r.cfg.ComputerIdleExit
	if idle <= 0 {
		idle = DefaultComputerIdleExit
	}
	return &computer{r: r, idle: idle}
}

// computerProc 是一代执行器进程。
type computerProc struct {
	pid    int
	stdin  *os.File
	w      *omprpc.Writer
	stderr *lineRing

	sawReady chan struct{} // 读到 ready 帧
	init     chan struct{} // 初始化结束（成功或失败）后关闭；initErr 随之可读
	initErr  error
	exited   chan struct{}

	mu      sync.Mutex
	waiters map[string]chan omprpc.Envelope
}

// toolUpdate 是 oryn_tool_update 帧；partialResult 原样转发，不经 Go 解码。
type toolUpdate struct {
	CallID        string          `json:"callId"`
	PartialResult json.RawMessage `json:"partialResult"`
}

// computerCallResult 与 apigen.LinkCtlComputerCallResult 同形，result 原样转发（可能含大图片块）。
type computerCallResult struct {
	Result json.RawMessage `json:"result"`
}

// computerStartResult 与 apigen.LinkCtlComputerStartResult 同形，tools 原样转发。
type computerStartResult struct {
	Tools        json.RawMessage `json:"tools"`
	AgentVersion string          `json:"agentVersion"`
}

// begin / end 包住每个 computer.start / computer.call：在途期间不计空闲，最后一个结束时重新开始计时。
func (c *computer) begin() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflight++
	if c.idleTm != nil {
		c.idleTm.Stop()
		c.idleTm = nil
	}
}

func (c *computer) end() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflight--
	if c.inflight > 0 || c.proc == nil {
		return
	}
	var tm *time.Timer
	tm = time.AfterFunc(c.idle, func() {
		c.mu.Lock()
		if c.idleTm != tm || c.inflight > 0 {
			c.mu.Unlock()
			return
		}
		c.idleTm = nil
		p := c.proc
		c.proc = nil
		c.mu.Unlock()
		if p != nil {
			c.r.log.Info("tool executor idle, stopping", "pid", p.pid)
			p.stop(computerStopGrace)
		}
	})
	c.idleTm = tm
}

// ensure 返回已初始化的执行器，没有（或已退出）时拉起一个；并发调用共用同一次拉起。
func (c *computer) ensure(ctx context.Context) (*computerProc, error) {
	c.mu.Lock()
	p := c.proc
	if p == nil || p.isExited() {
		var err error
		if p, err = c.spawn(); err != nil {
			c.mu.Unlock()
			return nil, ctlErr(CodeAgentStartFailed, "start the tool executor: %v", err)
		}
		c.proc = p
		go c.initialize(p)
		go func() {
			<-p.exited
			c.mu.Lock()
			if c.proc == p {
				c.proc = nil
			}
			c.mu.Unlock()
		}()
	}
	c.mu.Unlock()
	select {
	case <-p.init:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.initErr != nil {
		return nil, ctlErr(CodeAgentStartFailed, "start the tool executor: %v", p.initErr)
	}
	return p, nil
}

// spawn 启动执行器进程。环境与普通线程的 agent 相同（基础环境 + setup 环境 + 登录 shell）；
// 设置覆盖层之后，桌面启用时再加桌面覆盖层，让 eval 的浏览器 prelude 连到本机 CDP 前门。调用方持 c.mu。
func (c *computer) spawn() (*computerProc, error) {
	r := c.r
	env := r.baseEnv()
	for k, v := range r.setupEnvSnapshot() {
		env[k] = v
	}
	env = loginEnv(r.ctx, r.home, env)
	env["PI_NOTIFICATIONS"] = "off"
	args := []string{
		"--mode", "rpc", "--no-session", "--no-skills", "--no-extensions", "--no-rules", "--no-context-files",
		"--cwd", r.home, "--config", r.computerOverlayPath(),
	}
	if r.desk != nil {
		args = append(args, "--config", r.desktopOverlayPath())
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, err
	}
	// r.ctx 结束（orbd 退出）时杀掉整个进程组。
	cmd := r.bins.command(r.ctx, r.cfg.AgentBin, args...)
	cmd.Dir = r.home
	cmd.Env = envList(env)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	err = cmd.Start()
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		return nil, err
	}
	p := &computerProc{
		pid:      cmd.Process.Pid,
		stdin:    inW,
		w:        omprpc.NewWriter(inW, 0),
		stderr:   &lineRing{max: stderrTailLines},
		sawReady: make(chan struct{}),
		init:     make(chan struct{}),
		exited:   make(chan struct{}),
		waiters:  map[string]chan omprpc.Envelope{},
	}
	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		c.readStdout(p, outR)
	}()
	go func() {
		defer close(stderrDone)
		p.stderr.consume(errR)
	}()
	go func() {
		_ = cmd.Wait()
		// 工具起的子进程可能继承了管道：主进程退出后给 2s 读完，再强行关闭。
		waitOrClose(stdoutDone, outR)
		waitOrClose(stderrDone, errR)
		_ = syscall.Kill(-p.pid, syscall.SIGKILL)
		_ = inW.Close()
		if c.r.ctx.Err() == nil {
			c.r.log.Info("tool executor exited", "pid", p.pid, "code", exitCodeOf(cmd.ProcessState), "stderr", p.stderr.String())
		}
		close(p.exited)
	}()
	return p, nil
}

// initialize 等 ready 并协商协议 v2；失败时杀掉进程。
func (c *computer) initialize(p *computerProc) {
	defer close(p.init)
	timeout := c.r.cfg.InitTimeout
	tm := time.NewTimer(timeout)
	defer tm.Stop()
	select {
	case <-p.sawReady:
	case <-p.exited:
		p.initErr = fmt.Errorf("exited before ready: %s", p.stderr.String())
		return
	case <-tm.C:
		p.initErr = errors.New("timed out waiting for ready")
		p.kill()
		return
	}
	ctx, cancel := context.WithTimeout(c.r.ctx, timeout)
	defer cancel()
	env, err := c.request(ctx, p, map[string]any{"type": "negotiate_protocol", "protocolVersion": 2})
	if err == nil && !env.OK() {
		err = fmt.Errorf("negotiate_protocol failed: %s", env.ErrorText())
	}
	if err != nil {
		p.initErr = err
		p.kill()
	}
}

// readStdout 解码执行器的输出：ready、命令应答、工具的流式输出与对话框请求，其余帧丢弃。
func (c *computer) readStdout(p *computerProc, rd io.Reader) {
	dec := omprpc.NewDecoder(rd)
	readySeen := false
	for {
		frame, err := dec.Next()
		if err != nil {
			if omprpc.IsRecoverable(err) {
				c.r.log.Warn("bad tool executor frame", "err", err)
				continue
			}
			return
		}
		env, err := omprpc.ParseEnvelope(frame)
		if err != nil {
			continue
		}
		switch env.Type {
		case omprpc.TypeReady:
			if !readySeen {
				readySeen = true
				close(p.sawReady)
			}
		case omprpc.TypeResponse:
			p.mu.Lock()
			ch := p.waiters[env.ID]
			delete(p.waiters, env.ID)
			p.mu.Unlock()
			if ch != nil {
				ch <- env
			}
		case "oryn_tool_update":
			var u toolUpdate
			if json.Unmarshal(frame, &u) == nil && u.CallID != "" {
				c.r.notify(string(apigen.MethodComputerUpdate), u)
			}
		case "extension_ui_request":
			var req struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(frame, &req) == nil && computerDialogs[req.Method] && env.ID != "" {
				_ = p.w.WriteFrame(map[string]any{"type": "extension_ui_response", "id": env.ID, "cancelled": true})
			}
		}
	}
}

// request 发一条命令并等它的应答；进程退出时返回 errComputerExited。
func (c *computer) request(ctx context.Context, p *computerProc, frame map[string]any) (omprpc.Envelope, error) {
	id := computerIDPrefix + strconv.FormatUint(c.seq.Add(1), 10)
	frame["id"] = id
	ch := make(chan omprpc.Envelope, 1)
	p.mu.Lock()
	p.waiters[id] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiters, id)
		p.mu.Unlock()
	}()
	if err := p.w.WriteFrame(frame); err != nil {
		select {
		case <-p.exited:
			return omprpc.Envelope{}, errComputerExited
		default:
			return omprpc.Envelope{}, fmt.Errorf("write %v: %w", frame["type"], err)
		}
	}
	select {
	case env := <-ch:
		return env, nil
	case <-p.exited:
		return omprpc.Envelope{}, errComputerExited
	case <-ctx.Done():
		return omprpc.Envelope{}, ctx.Err()
	}
}

// exitedErr 把进程退出转成 agent_exited。
func (p *computerProc) exitedErr() error {
	return ctlErr(CodeAgentExited, "the tool executor exited: %s", lastLine(p.stderr.String()))
}

func (p *computerProc) isExited() bool {
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}

func (p *computerProc) kill() { _ = syscall.Kill(-p.pid, syscall.SIGKILL) }

// stop 关闭 stdin 让 omp 自行退出，grace 内没退出就杀进程组；返回时进程已退出。
func (p *computerProc) stop(grace time.Duration) {
	_ = p.stdin.Close()
	tm := time.NewTimer(grace)
	defer tm.Stop()
	select {
	case <-p.exited:
		return
	case <-tm.C:
	}
	p.kill()
	<-p.exited
}

// lastLine 返回多行文本的最后一个非空行（stderr 尾部的摘要）。
func lastLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			return s[i+1:]
		}
	}
	return s
}

// ---- ctl 方法 ----

// start 是 computer.start：确保执行器在跑，返回所列工具的定义与 omp 版本。
func (c *computer) start(ctx context.Context, p apigen.LinkCtlComputerStartParams) (computerStartResult, error) {
	c.begin()
	defer c.end()
	proc, err := c.ensure(ctx)
	if err != nil {
		return computerStartResult{}, err
	}
	frame := map[string]any{"type": "oryn_tool_defs"}
	if p.Names != nil {
		frame["names"] = p.Names
	}
	env, err := c.request(ctx, proc, frame)
	if err != nil {
		if errors.Is(err, errComputerExited) {
			return computerStartResult{}, proc.exitedErr()
		}
		return computerStartResult{}, ctlErr("failed", "oryn_tool_defs: %v", err)
	}
	if !env.OK() {
		return computerStartResult{}, ctlErr(CodeToolRejected, "oryn_tool_defs: %s", env.ErrorText())
	}
	var data struct {
		Tools json.RawMessage `json:"tools"`
	}
	if err := responseData(env, &data); err != nil || len(data.Tools) == 0 || string(data.Tools) == "null" {
		return computerStartResult{}, ctlErr("failed", "oryn_tool_defs: malformed response")
	}
	return computerStartResult{Tools: data.Tools, AgentVersion: c.r.agentVersion}, nil
}

// call 是 computer.call：执行一个工具直到结束。超过时限时发 oryn_tool_cancel 并回 timeout；
// 控制面的 link 断开（reqCtx 结束）时同样取消，免得工具一直跑到时限。
func (c *computer) call(reqCtx context.Context, p apigen.LinkCtlComputerCallParams) (computerCallResult, error) {
	if p.CallID == "" || p.Tool == "" {
		return computerCallResult{}, ctlErr(link.CodeInvalidParams, "callId and tool are required")
	}
	timeoutSec := p.TimeoutSec
	if timeoutSec == 0 {
		timeoutSec = computerDefaultTimeoutSec
	}
	if timeoutSec < 1 || timeoutSec > computerMaxTimeoutSec {
		return computerCallResult{}, ctlErr(link.CodeInvalidParams, "timeoutSec must be between 1 and %d", computerMaxTimeoutSec)
	}
	c.begin()
	defer c.end()
	ctx, cancel := context.WithTimeout(reqCtx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	proc, err := c.ensure(ctx)
	if err != nil {
		if ctx.Err() != nil && reqCtx.Err() == nil {
			return computerCallResult{}, ctlErr(CodeTimeout, "the tool executor did not start within %ds", timeoutSec)
		}
		return computerCallResult{}, err
	}
	args := p.Args
	if args == nil {
		args = map[string]any{}
	}
	env, err := c.request(ctx, proc, map[string]any{
		"type": "oryn_tool_call", "callId": p.CallID, "toolName": p.Tool, "arguments": args,
	})
	switch {
	case errors.Is(err, errComputerExited):
		return computerCallResult{}, proc.exitedErr()
	case err != nil && ctx.Err() != nil:
		c.sendCancel(proc, p.CallID)
		if reqCtx.Err() != nil {
			return computerCallResult{}, reqCtx.Err()
		}
		return computerCallResult{}, ctlErr(CodeTimeout, "%s did not finish within %ds and was cancelled", p.Tool, timeoutSec)
	case err != nil:
		return computerCallResult{}, ctlErr("failed", "oryn_tool_call: %v", err)
	case !env.OK():
		return computerCallResult{}, ctlErr(CodeToolRejected, "%s", env.ErrorText())
	}
	var data computerCallResult
	if err := responseData(env, &data); err != nil || len(data.Result) == 0 || string(data.Result) == "null" {
		return computerCallResult{}, ctlErr("failed", "oryn_tool_call: malformed response")
	}
	return data, nil
}

// sendCancel 在后台发 oryn_tool_cancel（不等应答，进程已退出时什么都不做）。
func (c *computer) sendCancel(p *computerProc, callID string) {
	go func() {
		ctx, cancel := context.WithTimeout(c.r.ctx, computerCancelWait)
		defer cancel()
		if _, err := c.request(ctx, p, map[string]any{"type": "oryn_tool_cancel", "callId": callID}); err != nil && !errors.Is(err, errComputerExited) {
			c.r.log.Warn("cancel tool call failed", "callId", callID, "err", err)
		}
	}()
}

// cancel 是 computer.cancel：中止一个在途调用（该调用随后以工具在中止时给出的结果返回）。
// 执行器没在跑或调用已结束时同样成功。
func (c *computer) cancel(p apigen.LinkCtlComputerCancelParams) (apigen.LinkCtlOkResult, error) {
	if p.CallID == "" {
		return apigen.LinkCtlOkResult{}, ctlErr(link.CodeInvalidParams, "callId is required")
	}
	c.mu.Lock()
	proc := c.proc
	c.mu.Unlock()
	if proc == nil || proc.isExited() {
		return apigen.LinkCtlOkResult{Ok: true}, nil
	}
	select {
	case <-proc.init:
	default:
		// 还在初始化：不可能有在途调用
		return apigen.LinkCtlOkResult{Ok: true}, nil
	}
	ctx, cancel := context.WithTimeout(c.r.ctx, computerCancelWait)
	defer cancel()
	env, err := c.request(ctx, proc, map[string]any{"type": "oryn_tool_cancel", "callId": p.CallID})
	switch {
	case errors.Is(err, errComputerExited):
		return apigen.LinkCtlOkResult{Ok: true}, nil
	case err != nil:
		return apigen.LinkCtlOkResult{}, ctlErr("failed", "oryn_tool_cancel: %v", err)
	case !env.OK():
		return apigen.LinkCtlOkResult{}, ctlErr(CodeToolRejected, "%s", env.ErrorText())
	}
	return apigen.LinkCtlOkResult{Ok: true}, nil
}

// stop 是 computer.stop（也用于 orbd 优雅退出）：停掉执行器，在途调用以 agent_exited 失败。
func (c *computer) stop(grace time.Duration) apigen.LinkCtlOkResult {
	c.mu.Lock()
	p := c.proc
	c.proc = nil
	if c.idleTm != nil {
		c.idleTm.Stop()
		c.idleTm = nil
	}
	c.mu.Unlock()
	if p != nil {
		p.stop(grace)
	}
	return apigen.LinkCtlOkResult{Ok: true}
}

// running 报告执行器进程是否在跑（测试用）。
func (c *computer) running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.proc != nil && !c.proc.isExited()
}

// responseData 解出应答的 data 字段。
func responseData(env omprpc.Envelope, v any) error {
	var r struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(env.Raw, &r); err != nil {
		return err
	}
	return json.Unmarshal(r.Data, v)
}
