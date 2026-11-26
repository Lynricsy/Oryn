package sandboxes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 参数范围（C5）。
const (
	maxRequestLine   = 1 << 20
	minIdleMinutes   = 5
	maxIdleMinutes   = 240
	maxTTLMinutes    = 720
	maxCommandChars  = 32000
	defaultExecSec   = 600
	maxSyncExecSec   = 1800
	maxDetachExecSec = 86400
	maxEnv           = 64
	maxEnvBytes      = 64 << 10
	maxExclude       = 64
	maxExcludeBytes  = 256
	// MaxTransferBytes 是单次 cp 的上限（2 GiB）。
	MaxTransferBytes = 2 << 30
)

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// request 是流上的请求行。
type request struct {
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params"`
}

// lineWriter 串行地往流上写 JSON 行。
type lineWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lineWriter) line(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err = l.w.Write(append(b, '\n'))
	return err
}

// writeRaw 原样写一行（已是 JSON，带不带结尾换行都行）。
func (l *lineWriter) writeRaw(b []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(b) == 0 || b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	_, err := l.w.Write(b)
	return err
}

// OnStream 处理 owner orb 发起的 `sandbox` 流（orbs.StreamFunc）：读一行请求，按 op 执行，零或多行输出之后恰好一行
// result 或 error，然后返回（流随之关闭）。orbd 在最后一行之前关流 = 放弃：本次操作的 ctx 取消（create 除外）。
func (s *Service) OnStream(ctx context.Context, orbID string, h link.StreamHeader, st *link.Stream) {
	out := &lineWriter{w: st}
	fail := func(err error) {
		e := streamErr(err)
		_ = out.line(map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
	}
	raw, err := st.ReadLine(maxRequestLine)
	if err != nil {
		if errors.Is(err, link.ErrLineTooLong) {
			fail(errf(CodeInvalid, "request line exceeds 1 MiB"))
		}
		return
	}
	var req request
	if err := json.Unmarshal(raw, &req); err != nil || req.Op == "" {
		fail(errf(CodeInvalid, "malformed request"))
		return
	}
	opCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// orbd 不再写别的；读到 EOF 或出错 = 调用方关流放弃
	go func() {
		_, _ = io.Copy(io.Discard, st)
		cancel()
	}()
	res, err := s.dispatch(opCtx, orbID, h.ThreadID, req, out)
	if err != nil {
		fail(err)
		return
	}
	_ = out.line(map[string]any{"result": res})
}

// dispatch 按 op 执行；除 status 外先校验调用方。
func (s *Service) dispatch(ctx context.Context, orbID, threadID string, req request, out *lineWriter) (any, error) {
	if req.Op == "status" {
		return s.status(ctx)
	}
	owner, err := s.caller(ctx, orbID, threadID)
	if err != nil {
		return nil, err
	}
	decode := func(v any) error {
		if len(req.Params) == 0 {
			return nil
		}
		dec := json.NewDecoder(bytes.NewReader(req.Params))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			return errf(CodeInvalid, "invalid params for %s: %v", req.Op, err)
		}
		return nil
	}
	switch req.Op {
	case "create":
		var p createParams
		if err := decode(&p); err != nil {
			return nil, err
		}
		sb, err := s.create(ctx, owner, threadID, p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sandbox": sb}, nil
	case "list":
		since := s.now().Add(-recentWindow).UnixMilli()
		rows, err := s.st.Q.ListSandboxesByOwnerOrb(ctx, db.ListSandboxesByOwnerOrbParams{OwnerOrbID: orbID, Since: &since})
		if err != nil {
			return nil, err
		}
		return map[string]any{"sandboxes": views(rows)}, nil
	case "exec":
		var p execParams
		if err := decode(&p); err != nil {
			return nil, err
		}
		return s.exec(ctx, orbID, p, out)
	case "cp":
		var p cpParams
		if err := decode(&p); err != nil {
			return nil, err
		}
		return s.cp(ctx, orbID, p)
	case "network":
		var p networkParams
		if err := decode(&p); err != nil {
			return nil, err
		}
		sb, err := s.network(ctx, orbID, p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sandbox": sb}, nil
	case "extend":
		var p extendParams
		if err := decode(&p); err != nil {
			return nil, err
		}
		sb, err := s.extend(ctx, orbID, p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sandbox": sb}, nil
	case "jobs":
		var p jobsParams
		if err := decode(&p); err != nil {
			return nil, err
		}
		return s.jobs(ctx, orbID, p)
	case "destroy":
		var p struct {
			Sandbox string `json:"sandbox"`
		}
		if err := decode(&p); err != nil {
			return nil, err
		}
		sb, err := s.own(ctx, orbID, p.Sandbox, false)
		if err != nil {
			return nil, err
		}
		if err := s.destroy(ctx, sb, ReasonAgent); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	}
	return nil, errf(CodeInvalid, "unknown op %q", req.Op)
}

// ---- create ----

type createParams struct {
	Network     string `json:"network"`
	Backend     string `json:"backend"`
	Type        string `json:"type"`
	IdleMinutes *int   `json:"idleMinutes"`
	TTLMinutes  *int   `json:"ttlMinutes"`
}

// create 核对配额、选 provider、建沙箱 orb 与行，然后等它就绪。建好之后的等待不随调用方放弃而中止：
// 沙箱照常就绪并出现在 list 里。
func (s *Service) create(ctx context.Context, owner db.Orb, threadID string, p createParams) (apigen.Sandbox, error) {
	if !s.cfg.Sandbox.Enabled {
		return apigen.Sandbox{}, errf(CodeDisabled, "sandboxes are disabled on this Oryn server")
	}
	if p.Network != orbs.SandboxNetNone && p.Network != orbs.SandboxNetEgress {
		return apigen.Sandbox{}, errf(CodeInvalid, "network must be none or egress")
	}
	if p.Backend != "" && p.Backend != backendDocker && p.Backend != backendKVM {
		return apigen.Sandbox{}, errf(CodeInvalid, "backend must be docker or kvm")
	}
	idle := s.cfg.Sandbox.Idle.D()
	if p.IdleMinutes != nil {
		if *p.IdleMinutes < minIdleMinutes || *p.IdleMinutes > maxIdleMinutes {
			return apigen.Sandbox{}, errf(CodeInvalid, "idleMinutes must be %d–%d", minIdleMinutes, maxIdleMinutes)
		}
		idle = time.Duration(*p.IdleMinutes) * time.Minute
	}
	if p.TTLMinutes != nil && (*p.TTLMinutes < 1 || *p.TTLMinutes > maxTTLMinutes) {
		return apigen.Sandbox{}, errf(CodeInvalid, "ttlMinutes must be 1–%d", maxTTLMinutes)
	}

	sb, err := s.start(ctx, owner, threadID, p, idle)
	if err != nil {
		return apigen.Sandbox{}, err
	}
	return s.awaitReady(context.WithoutCancel(ctx), sb)
}

// start 在 createMu 下核对配额、建 orb 并写行。
func (s *Service) start(ctx context.Context, owner db.Orb, threadID string, p createParams, idle time.Duration) (db.Sandbox, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	n, err := s.st.Q.CountLiveSandboxesByOwnerOrb(ctx, owner.ID)
	if err != nil {
		return db.Sandbox{}, err
	}
	if n >= int64(s.cfg.Sandbox.MaxPerOrb) {
		return db.Sandbox{}, errf(CodeQuotaPerOrb, "this machine already has %d sandboxes (limit %d); destroy one first", n, s.cfg.Sandbox.MaxPerOrb)
	}
	total, err := s.st.Q.CountLiveSandboxes(ctx)
	if err != nil {
		return db.Sandbox{}, err
	}
	if total >= int64(s.cfg.Sandbox.MaxTotal) {
		return db.Sandbox{}, errf(CodeQuotaTotal, "the server already runs %d sandboxes (limit %d)", total, s.cfg.Sandbox.MaxTotal)
	}
	prov, typ, backend, err := s.pick(ctx, owner, p.Backend, p.Type)
	if err != nil {
		return db.Sandbox{}, err
	}
	o, err := s.orbs.Create(ctx, orbs.CreateParams{Provider: prov.Name, Type: typ, SandboxNetwork: p.Network})
	if err != nil {
		return db.Sandbox{}, errf(CodeCreateFailed, "%v", err)
	}
	now := s.now()
	hard := now.Add(s.cfg.Sandbox.MaxLifetime.D()).UnixMilli()
	var ttl *int64
	if p.TTLMinutes != nil {
		ttl = new(min(now.Add(time.Duration(*p.TTLMinutes)*time.Minute).UnixMilli(), hard))
	}
	row := db.CreateSandboxParams{
		ID: ids.New("sbx"), OrbID: o.ID, OwnerOrbID: owner.ID, OwnerThreadID: threadID, Backend: backend,
		ProviderName: prov.Name, Type: typ, CreatedNetwork: p.Network, Network: p.Network,
		IdleSec: int64(idle / time.Second), LastActiveAt: now.UnixMilli(), TtlDeadline: ttl, HardDeadline: hard,
		CreatedAt: now.UnixMilli(),
	}
	if err := s.st.Q.CreateSandbox(ctx, row); err != nil {
		if derr := s.orbs.Delete(context.WithoutCancel(ctx), o.ID); derr != nil {
			slog.Warn("sandboxes: 写行失败后回收 orb 失败，孤儿清理兜底", "orb", o.ID, "err", derr)
		}
		return db.Sandbox{}, err
	}
	slog.Info("sandboxes: 创建沙箱", "sandbox", row.ID, "orb", o.ID, "owner", owner.ID, "provider", prov.Name, "type", typ, "network", p.Network)
	s.changed(threadID)
	return s.st.Q.GetSandbox(ctx, row.ID)
}

// awaitReady 轮询到沙箱 orb ready、link 在线且声明了 sandbox 特性；orb 失败或超时则销毁并报错。
func (s *Service) awaitReady(ctx context.Context, sb db.Sandbox) (apigen.Sandbox, error) {
	deadline := time.Now().Add(s.cfg.CreateTimeout)
	t := time.NewTicker(createPoll)
	defer t.Stop()
	for {
		cur, err := s.st.Q.GetSandbox(ctx, sb.ID)
		if err != nil {
			return apigen.Sandbox{}, err
		}
		if cur.State != stateCreating {
			// 等待期间被级联或主人销毁
			return apigen.Sandbox{}, errf(CodeCreateFailed, "sandbox %s was destroyed while starting", sb.ID)
		}
		o, err := s.orbs.Get(ctx, sb.OrbID)
		if err != nil {
			return apigen.Sandbox{}, err
		}
		switch {
		case o.State == orbs.StateReady && s.orbs.Connected(o.ID) && s.orbs.HasFeature(o.ID, orbs.FeatureSandbox):
			now := s.now().UnixMilli()
			if _, err := s.st.Q.MarkSandboxReady(ctx, db.MarkSandboxReadyParams{Now: &now, ID: sb.ID}); err != nil {
				return apigen.Sandbox{}, err
			}
			s.changed(sb.OwnerThreadID)
			ready, err := s.st.Q.GetSandbox(ctx, sb.ID)
			if err != nil {
				return apigen.Sandbox{}, err
			}
			if ready.State != stateReady {
				return apigen.Sandbox{}, errf(CodeCreateFailed, "sandbox %s was destroyed while starting", sb.ID)
			}
			return view(ready), nil
		case o.State == orbs.StateFailed:
			reason, err := orbs.FailureReason(ctx, s.st.Q, o.ID)
			if err != nil || reason == "" {
				reason = "the sandbox machine failed to start"
			}
			now := s.now().UnixMilli()
			why := ReasonCreateFailed
			if _, err := s.st.Q.MarkSandboxFailed(ctx, db.MarkSandboxFailedParams{Error: &reason, Reason: &why, Now: &now, ID: sb.ID}); err != nil {
				return apigen.Sandbox{}, err
			}
			if err := s.orbs.Delete(ctx, o.ID); err != nil && !errors.Is(err, orbs.ErrNotFound) {
				slog.Warn("sandboxes: 删除失败的沙箱 orb 失败，孤儿清理兜底", "orb", o.ID, "err", err)
			}
			s.changed(sb.OwnerThreadID)
			if strings.HasPrefix(reason, "capacity:") {
				return apigen.Sandbox{}, errf(CodeCapacity, "%s", reason)
			}
			return apigen.Sandbox{}, errf(CodeCreateFailed, "%s", reason)
		case time.Now().After(deadline):
			if err := s.destroy(ctx, cur, ReasonCreateTimeout); err != nil {
				return apigen.Sandbox{}, err
			}
			return apigen.Sandbox{}, errf(CodeCreateTimeout, "sandbox %s did not become ready within %s", sb.ID, s.cfg.CreateTimeout)
		}
		<-t.C
	}
}

// ---- exec ----

type execParams struct {
	Sandbox    string            `json:"sandbox"`
	Command    string            `json:"command"`
	Cwd        string            `json:"cwd"`
	Env        map[string]string `json:"env"`
	TimeoutSec *int              `json:"timeoutSec"`
	Detach     bool              `json:"detach"`
}

func (p *execParams) validate() (int, error) {
	if n := utf8.RuneCountInString(p.Command); n == 0 || n > maxCommandChars {
		return 0, errf(CodeInvalid, "command must be 1–%d characters", maxCommandChars)
	}
	if len(p.Env) > maxEnv {
		return 0, errf(CodeInvalid, "at most %d env entries", maxEnv)
	}
	size := 0
	for k, v := range p.Env {
		if !envKeyRe.MatchString(k) {
			return 0, errf(CodeInvalid, "invalid env name %q", k)
		}
		size += len(k) + len(v)
	}
	if size > maxEnvBytes {
		return 0, errf(CodeInvalid, "env exceeds 64 KiB")
	}
	if p.Detach {
		t := 0
		if p.TimeoutSec != nil {
			t = *p.TimeoutSec
		}
		if t < 0 || t > maxDetachExecSec {
			return 0, errf(CodeInvalid, "timeoutSec for a detached job must be 0 (no limit) or 1–%d", maxDetachExecSec)
		}
		return t, nil
	}
	t := defaultExecSec
	if p.TimeoutSec != nil {
		t = *p.TimeoutSec
	}
	if t < 1 || t > maxSyncExecSec {
		return 0, errf(CodeInvalid, "timeoutSec must be 1–%d (use detach for longer runs)", maxSyncExecSec)
	}
	return t, nil
}

// exec 在沙箱里跑命令：同步时把 orbd 的 NDJSON 输出行原样写回流，`{"exit":N}` 改写成结果；detach 时回作业 id。
// ctx 取消（调用方关流）时对沙箱的请求随之取消，沙箱 orbd 杀掉进程组。
func (s *Service) exec(ctx context.Context, linkOrbID string, p execParams, out *lineWriter) (map[string]any, error) {
	timeout, err := p.validate()
	if err != nil {
		return nil, err
	}
	sb, err := s.own(ctx, linkOrbID, p.Sandbox, true)
	if err != nil {
		return nil, err
	}
	defer s.hold(ctx, sb.ID)()
	body, err := json.Marshal(apigen.OrbSandboxExecRequest{
		Command: p.Command, Cwd: p.Cwd, Env: p.Env, TimeoutSec: timeout, Detach: &p.Detach,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/sandbox/exec", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := s.orbs.Do(ctx, sb.OrbID, req)
	if err != nil {
		return nil, errf(CodeLinkLost, "sandbox unreachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, orbError(resp, CodeInternal)
	}
	if p.Detach {
		var r apigen.OrbSandboxJobStart
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&r); err != nil {
			return nil, errf(CodeLinkLost, "sandbox did not confirm the job: %v", err)
		}
		return map[string]any{"jobId": r.JobID}, nil
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var line struct {
			Exit *int `json:"exit"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil {
			continue
		}
		if line.Exit != nil {
			return map[string]any{"exit": *line.Exit, "durationMs": time.Since(start).Milliseconds()}, nil
		}
		if err := out.writeRaw(sc.Bytes()); err != nil {
			return nil, err
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, errf(CodeLinkLost, "the sandbox closed the command's output before it finished")
}

// ---- cp ----

type cpParams struct {
	Sandbox   string   `json:"sandbox"`
	Direction string   `json:"direction"`
	Source    string   `json:"source"`
	Dest      string   `json:"dest"`
	Exclude   []string `json:"exclude"`
	Gitignore bool     `json:"gitignore"`
}

// errTooLarge 是计数读取器超过上限时的错误。
var errTooLarge = errors.New("transfer exceeds 2 GiB")

// capReader 计数并在超过 limit 时报错。
type capReader struct {
	r     io.Reader
	n     int64
	limit int64
	over  bool
}

func (c *capReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > c.limit {
		c.over = true
		return n, errTooLarge
	}
	return n, err
}

// cp 在 owner orb 与沙箱之间流式传 tar（经控制面中转，不落盘）：源端 GET /fs/tar，目标端 PUT /fs/tar。
func (s *Service) cp(ctx context.Context, linkOrbID string, p cpParams) (map[string]any, error) {
	if p.Direction != "to_sandbox" && p.Direction != "from_sandbox" {
		return nil, errf(CodeInvalid, "direction must be to_sandbox or from_sandbox")
	}
	if p.Source == "" || p.Dest == "" {
		return nil, errf(CodeInvalid, "source and dest are required")
	}
	if len(p.Exclude) > maxExclude {
		return nil, errf(CodeInvalid, "at most %d exclude patterns", maxExclude)
	}
	for _, e := range p.Exclude {
		if e == "" || len(e) > maxExcludeBytes {
			return nil, errf(CodeInvalid, "exclude patterns must be 1–%d bytes", maxExcludeBytes)
		}
	}
	sb, err := s.own(ctx, linkOrbID, p.Sandbox, true)
	if err != nil {
		return nil, err
	}
	if !s.orbs.HasFeature(linkOrbID, orbs.FeatureFSTar) || !s.orbs.HasFeature(sb.OrbID, orbs.FeatureFSTar) {
		return nil, errf(CodeOrbdOutdated, "orbd on this machine or in the sandbox is too old for directory transfer; recreate the machine")
	}
	defer s.hold(ctx, sb.ID)()
	from, to := linkOrbID, sb.OrbID
	if p.Direction == "from_sandbox" {
		from, to = sb.OrbID, linkOrbID
	}
	q := url.Values{"path": {p.Source}}
	for _, e := range p.Exclude {
		q.Add("exclude", e)
	}
	if p.Gitignore {
		q.Set("gitignore", "1")
	}
	get, err := http.NewRequestWithContext(ctx, http.MethodGet, "/fs/tar?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	src, err := s.orbs.Do(ctx, from, get)
	if err != nil {
		return nil, errf(CodeLinkLost, "source unreachable: %v", err)
	}
	defer src.Body.Close()
	if src.StatusCode < 200 || src.StatusCode > 299 {
		return nil, orbError(src, CodeInternal)
	}
	kind := src.Header.Get("X-Oryn-Tar-Kind")
	if kind != "file" && kind != "dir" {
		return nil, errf(CodeInternal, "source sent an unknown tar kind %q", kind)
	}
	cr := &capReader{r: src.Body, limit: MaxTransferBytes}
	put, err := http.NewRequestWithContext(ctx, http.MethodPut,
		"/fs/tar?"+url.Values{"path": {p.Dest}, "kind": {kind}}.Encode(), cr)
	if err != nil {
		return nil, err
	}
	put.ContentLength = -1
	put.Header.Set("Content-Type", "application/x-tar")
	dst, err := s.orbs.Do(ctx, to, put)
	if cr.over {
		if dst != nil {
			_ = dst.Body.Close()
		}
		return nil, errf(CodeTooLarge, "transfer exceeds 2 GiB")
	}
	if err != nil {
		return nil, errf(CodeLinkLost, "destination unreachable: %v", err)
	}
	defer dst.Body.Close()
	if dst.StatusCode < 200 || dst.StatusCode > 299 {
		return nil, orbError(dst, CodeInternal)
	}
	var r apigen.OrbFsTarResult
	if err := json.NewDecoder(io.LimitReader(dst.Body, 64<<10)).Decode(&r); err != nil {
		return nil, errf(CodeInternal, "destination returned a malformed result: %v", err)
	}
	return map[string]any{"files": r.Files, "bytes": r.Bytes, "skipped": r.Skipped}, nil
}

// ---- network / extend ----

type networkParams struct {
	Sandbox string `json:"sandbox"`
	Network string `json:"network"`
}

func (s *Service) network(ctx context.Context, linkOrbID string, p networkParams) (apigen.Sandbox, error) {
	if p.Network != orbs.SandboxNetNone && p.Network != orbs.SandboxNetEgress {
		return apigen.Sandbox{}, errf(CodeInvalid, "network must be none or egress")
	}
	sb, err := s.own(ctx, linkOrbID, p.Sandbox, true)
	if err != nil {
		return apigen.Sandbox{}, err
	}
	if sb.CreatedNetwork == orbs.SandboxNetNone && p.Network == orbs.SandboxNetEgress {
		return apigen.Sandbox{}, errf(CodeNetworkUnavailable, "sandbox %s was created without a network interface and cannot be given one; create a new sandbox with network egress", sb.ID)
	}
	if sb.Network == p.Network {
		return view(sb), nil
	}
	release := s.hold(ctx, sb.ID)
	err = s.orbs.SetSandboxNetwork(ctx, sb.OrbID, p.Network)
	release()
	if err != nil {
		if msg := err.Error(); strings.Contains(msg, CodeNetworkUnavailable+":") {
			_, rest, _ := strings.Cut(msg, CodeNetworkUnavailable+":")
			return apigen.Sandbox{}, errf(CodeNetworkUnavailable, "%s", strings.TrimSpace(rest))
		}
		return apigen.Sandbox{}, errf(CodeInternal, "switching the network failed: %v", err)
	}
	if err := s.st.Q.SetSandboxNetwork(ctx, db.SetSandboxNetworkParams{Network: p.Network, ID: sb.ID}); err != nil {
		return apigen.Sandbox{}, err
	}
	s.touch(ctx, sb.ID)
	s.changed(sb.OwnerThreadID)
	sb, err = s.st.Q.GetSandbox(ctx, sb.ID)
	return view(sb), err
}

type extendParams struct {
	Sandbox    string `json:"sandbox"`
	TTLMinutes int    `json:"ttlMinutes"`
}

func (s *Service) extend(ctx context.Context, linkOrbID string, p extendParams) (apigen.Sandbox, error) {
	if p.TTLMinutes < 1 || p.TTLMinutes > maxTTLMinutes {
		return apigen.Sandbox{}, errf(CodeInvalid, "ttlMinutes must be 1–%d", maxTTLMinutes)
	}
	sb, err := s.own(ctx, linkOrbID, p.Sandbox, false)
	if err != nil {
		return apigen.Sandbox{}, err
	}
	if sb.State != stateCreating && sb.State != stateReady {
		return apigen.Sandbox{}, errf(CodeNotReady, "sandbox %s is %s", sb.ID, sb.State)
	}
	ttl := min(s.now().Add(time.Duration(p.TTLMinutes)*time.Minute).UnixMilli(), sb.HardDeadline)
	if err := s.st.Q.SetSandboxTTL(ctx, db.SetSandboxTTLParams{TtlDeadline: &ttl, ID: sb.ID}); err != nil {
		return apigen.Sandbox{}, err
	}
	s.touch(ctx, sb.ID)
	s.changed(sb.OwnerThreadID)
	sb, err = s.st.Q.GetSandbox(ctx, sb.ID)
	return view(sb), err
}

// ---- jobs ----

type jobsParams struct {
	Sandbox string `json:"sandbox"`
	Action  string `json:"action"`
	Job     string `json:"job"`
	Offset  *int64 `json:"offset"`
}

// jobs 转发到沙箱 orbd 的 /sandbox/jobs 路由；logs 与 kill 记一次活动。
func (s *Service) jobs(ctx context.Context, linkOrbID string, p jobsParams) (any, error) {
	switch p.Action {
	case "list", "logs", "kill":
	default:
		return nil, errf(CodeInvalid, "action must be list, logs or kill")
	}
	if p.Action != "list" && p.Job == "" {
		return nil, errf(CodeInvalid, "job is required for %s", p.Action)
	}
	sb, err := s.own(ctx, linkOrbID, p.Sandbox, true)
	if err != nil {
		return nil, err
	}
	switch p.Action {
	case "list":
		var r apigen.OrbSandboxJobList
		err := s.orbJSON(ctx, sb.OrbID, http.MethodGet, "/sandbox/jobs", &r)
		return r, err
	case "logs":
		target := "/sandbox/jobs/" + url.PathEscape(p.Job) + "/log"
		if p.Offset != nil {
			target += "?offset=" + strconv.FormatInt(*p.Offset, 10)
		}
		var r apigen.OrbSandboxJobLog
		if err := s.orbJSON(ctx, sb.OrbID, http.MethodGet, target, &r); err != nil {
			return nil, err
		}
		s.touch(ctx, sb.ID)
		return r, nil
	default:
		if err := s.orbJSON(ctx, sb.OrbID, http.MethodPost, "/sandbox/jobs/"+url.PathEscape(p.Job)+"/kill", nil); err != nil {
			return nil, err
		}
		s.touch(ctx, sb.ID)
		return map[string]any{"ok": true}, nil
	}
}

// orbJSON 向沙箱 orbd 发不带体的请求，把 2xx 的 JSON 应答解到 out（nil 时丢弃）。
func (s *Service) orbJSON(ctx context.Context, orbID, method, target string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return err
	}
	resp, err := s.orbs.Do(ctx, orbID, req)
	if err != nil {
		return errf(CodeLinkLost, "sandbox unreachable: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return orbError(resp, CodeInternal)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return errf(CodeInternal, "sandbox returned a malformed response: %v", err)
	}
	return nil
}
