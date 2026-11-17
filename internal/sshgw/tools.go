package sshgw

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ssh_exec_many 的限额（docs/plan-m18.md 步骤 8.1）。
const (
	toolExecMany       = "ssh_exec_many"
	execManyMaxHosts   = 32
	execManyMaxCommand = 8000 // 字符
	execManyParallel   = 16
	execManyDefaultSec = 60
	execManyMaxSec     = 600
	execOutputHalf     = 2048 // 每台输出超过 2×它时保留头尾各这么多字节
)

// threadKindWork 是工作线程的种类（同 threads.KindWork；sshgw 不导入 threads）。
const threadKindWork = "work"

// execManyArgs 是 ssh_exec_many 的参数。timeout_s 先按浮点解，再校验是整数。
type execManyArgs struct {
	Hosts    []string `json:"hosts"`
	Command  string   `json:"command"`
	TimeoutS *float64 `json:"timeout_s"`
}

// execResult 是一台主机的执行结果（details.results 的一项）。
type execResult struct {
	Host       string `json:"host"`
	OK         bool   `json:"ok"`
	ExitCode   *int   `json:"exitCode,omitempty"`
	DurationMs int64  `json:"durationMs"`
	TimedOut   bool   `json:"timedOut,omitempty"`
	Error      string `json:"error,omitempty"`
	output     string
}

// OnCall 处理 orbd 转来的 ssh.call（orb 内 agent 的 SSH 工具）；返回 apigen.LinkCtlSSHCallResult。
func (s *Service) OnCall(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlSSHCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &link.CtlError{Code: "invalid_params", Message: err.Error()}
	}
	orb, err := s.caller(ctx, orbID, p.ThreadID)
	if err != nil {
		return nil, err
	}
	if p.Tool != toolExecMany {
		return nil, &link.CtlError{Code: "invalid_params", Message: fmt.Sprintf("未知的 SSH 工具 %q", p.Tool)}
	}
	args, timeout, err := parseExecMany(p.Params)
	if err != nil {
		return nil, err
	}
	return s.execMany(ctx, orb, p.ThreadID, args, timeout), nil
}

// caller 校验调用方线程：存在、是工作线程、未归档、属于发起 link 的 orb（同 github.call）；返回该 orb。
func (s *Service) caller(ctx context.Context, orbID, threadID string) (db.Orb, error) {
	forbidden := &link.CtlError{Code: "forbidden", Message: "调用方线程不存在、已归档，或不属于这台 orb。"}
	if threadID == "" {
		return db.Orb{}, forbidden
	}
	t, err := s.st.Q.GetThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Orb{}, forbidden
	}
	if err != nil {
		return db.Orb{}, err
	}
	if t.Kind != threadKindWork || t.ArchivedAt != nil || t.OrbID == nil || *t.OrbID != orbID {
		return db.Orb{}, forbidden
	}
	orb, err := s.orbs.Get(ctx, orbID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Orb{}, forbidden
	}
	return orb, err
}

func parseExecMany(raw map[string]any) (execManyArgs, time.Duration, error) {
	bad := func(format string, a ...any) error {
		return &link.CtlError{Code: "invalid_params", Message: fmt.Sprintf(format, a...)}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return execManyArgs{}, 0, bad("参数无效：%v", err)
	}
	var a execManyArgs
	if err := json.Unmarshal(b, &a); err != nil {
		return execManyArgs{}, 0, bad("参数无效：%v", err)
	}
	if len(a.Hosts) < 1 || len(a.Hosts) > execManyMaxHosts {
		return execManyArgs{}, 0, bad("hosts 须为 1–%d 个主机名", execManyMaxHosts)
	}
	for _, h := range a.Hosts {
		if strings.TrimSpace(h) == "" {
			return execManyArgs{}, 0, bad("hosts 里有空的主机名")
		}
	}
	if n := utf8.RuneCountInString(a.Command); n < 1 || n > execManyMaxCommand {
		return execManyArgs{}, 0, bad("command 须为 1–%d 个字符", execManyMaxCommand)
	}
	sec := float64(execManyDefaultSec)
	if a.TimeoutS != nil {
		sec = *a.TimeoutS
		if sec != math.Trunc(sec) || sec < 1 || sec > execManyMaxSec {
			return execManyArgs{}, 0, bad("timeout_s 须为 1–%d 的整数", execManyMaxSec)
		}
	}
	return a, time.Duration(sec) * time.Second, nil
}

// execMany 在各主机上并发（至多 execManyParallel 台）执行同一条命令，结果按请求顺序拼成给 agent 的文本。
func (s *Service) execMany(ctx context.Context, orb db.Orb, threadID string, a execManyArgs, timeout time.Duration) apigen.LinkCtlSSHCallResult {
	results := make([]execResult, len(a.Hosts))
	sem := make(chan struct{}, execManyParallel)
	var wg sync.WaitGroup
	for i, name := range a.Hosts {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = s.execOne(ctx, orb, threadID, name, a.Command, timeout)
		})
	}
	wg.Wait()

	var b strings.Builder
	details := make([]any, 0, len(results))
	for i, r := range results {
		if i > 0 {
			b.WriteString("\n")
		}
		switch {
		case r.TimedOut:
			fmt.Fprintf(&b, "## %s — 超时（%ds）\n", r.Host, int(timeout/time.Second))
		case r.ExitCode != nil:
			fmt.Fprintf(&b, "## %s — exit %d（%.1fs）\n", r.Host, *r.ExitCode, float64(r.DurationMs)/1000)
		default:
			fmt.Fprintf(&b, "## %s — 失败：%s\n", r.Host, r.Error)
		}
		if r.output != "" {
			b.WriteString(r.output)
			if !strings.HasSuffix(r.output, "\n") {
				b.WriteString("\n")
			}
		}
		details = append(details, r)
	}
	return apigen.LinkCtlSSHCallResult{Text: b.String(), Details: map[string]any{"results": details}}
}

// execOne 在一台主机上执行命令（合并 stdout 与 stderr），并记一行审计。
// 返回值是命名的：defer 里补的耗时与审计字段要落到返回给调用方的结果上。
func (s *Service) execOne(ctx context.Context, orb db.Orb, threadID, name, command string, timeout time.Duration) (res execResult) {
	res = execResult{Host: name}
	h, deny, err := s.resolve(ctx, orb, name)
	if err == nil && deny != "" {
		res.Error = deny
		s.audit(ctx, auditRow{source: srcTool, orbID: orb.ID, threadID: threadID, host: h, hostName: name, kind: "deny", detail: deny})
		return res
	}
	if err != nil {
		res.Error = err.Error()
		return res
	}
	start := time.Now()
	row := auditRow{source: srcTool, orbID: orb.ID, threadID: threadID, host: h, kind: "exec", detail: command}
	defer func() {
		res.DurationMs = time.Since(start).Milliseconds()
		row.durationMs = &res.DurationMs
		row.ok = res.OK
		if res.ExitCode != nil {
			code := int64(*res.ExitCode)
			row.exitCode = &code
		}
		s.audit(ctx, row)
	}()

	up, err := s.dial(ctx, h)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer up.Close()
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out := &headTail{}
	expired, err := runBounded(dctx, up.client, up.Close, func(sess *ssh.Session) { sess.Stdout, sess.Stderr = out, out }, command)
	res.output = out.String()
	if expired {
		if ctx.Err() != nil {
			res.Error = "调用已取消"
		} else {
			res.TimedOut = true
		}
		return res
	}
	var ee *ssh.ExitError
	switch {
	case err == nil:
		res.ExitCode = new(0)
		res.OK = true
	case errors.As(err, &ee):
		res.ExitCode = new(ee.ExitStatus())
		res.OK = true
	default:
		res.Error = "执行失败：" + err.Error()
	}
	return res
}

// killGrace 是截止后发了 SIGKILL 与会话 close 帧、仍等对端回应的时长；过了就直接关掉上游连接。
const killGrace = 2 * time.Second

// runBounded 在 c 上开会话、用 setup 接好输出、执行 cmd 并等它结束，整个过程受 dctx 约束（含开会话与 Start）。
// dctx 结束时先尽力 SIGKILL 并关会话；对端不回话时这两步都换不来返回（SSH 的 close 需要对端确认），
// 所以 killGrace 后调用 closeConn 关掉整条上游 TCP 连接，开会话、Start、Wait 才保证返回。
// expired 为真表示结束原因是 dctx（超时或取消），不是命令自己退出。
func runBounded(dctx context.Context, c *ssh.Client, closeConn func(), setup func(*ssh.Session), cmd string) (expired bool, err error) {
	var mu sync.Mutex
	var sess *ssh.Session
	stop := context.AfterFunc(dctx, func() {
		time.AfterFunc(killGrace, closeConn) // 先排上兜底：下面的 Signal 写传输层也可能卡住
		mu.Lock()
		s := sess
		mu.Unlock()
		if s != nil {
			_ = s.Signal(ssh.SIGKILL)
			_ = s.Close()
		}
	})
	s, err := c.NewSession()
	if err != nil {
		return !stop(), err
	}
	defer s.Close()
	mu.Lock()
	sess = s
	mu.Unlock()
	setup(s)
	if err := s.Start(cmd); err != nil {
		return !stop(), err
	}
	err = s.Wait()
	return !stop(), err
}

// headTail 并发安全地收集输出：只留头 execOutputHalf 字节与尾 execOutputHalf 字节，记下总长。
type headTail struct {
	mu    sync.Mutex
	head  []byte
	tail  []byte // 滑动窗口，至多 execOutputHalf 字节
	total int
}

func (w *headTail) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += len(p)
	rest := p
	if room := execOutputHalf - len(w.head); room > 0 {
		n := min(room, len(rest))
		w.head = append(w.head, rest[:n]...)
		rest = rest[n:]
	}
	if len(rest) > 0 {
		w.tail = append(w.tail, rest...)
		if over := len(w.tail) - execOutputHalf; over > 0 {
			w.tail = append(w.tail[:0], w.tail[over:]...)
		}
	}
	return len(p), nil
}

// String 返回输出；超过 2×execOutputHalf 时在头尾之间插省略说明。
func (w *headTail) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if omitted := w.total - len(w.head) - len(w.tail); omitted > 0 {
		return strings.ToValidUTF8(string(w.head), "") + fmt.Sprintf("\n…（省略 %d 字节）…\n", omitted) + strings.ToValidUTF8(string(w.tail), "")
	}
	return strings.ToValidUTF8(string(w.head)+string(w.tail), "")
}
