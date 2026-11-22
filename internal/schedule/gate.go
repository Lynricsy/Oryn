package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// reasonGate 是闸门判定本次不必运行时运行记录的 error。
const reasonGate = "gate"

// SSHGate 是经 SSH 网关执行闸门的能力（*sshgw.Service 实现）：projectID 为空 = 只看全局主机；automation 为真时主机须
// 允许全自动线程使用。err 非空表示命令没有正常结束（此时 exitCode 无意义）；非 0 退出不算 err。
type SSHGate interface {
	CheckGateHost(ctx context.Context, projectID string, automation bool, host string) error
	RunGate(ctx context.Context, projectID string, automation bool, host, command, label string, timeout time.Duration) (stdout, stderr string, exitCode int, err error)
}

// LocalGate 是在控制面本机沙箱里执行闸门的能力（*localgate.Runner 实现）；返回值同 SSHGate.RunGate。
type LocalGate interface {
	Available() error
	Run(ctx context.Context, command, label string, timeout time.Duration) (stdout, stderr string, exitCode int, err error)
}

// gateDone 是一次运行前闸门的结果（经 evGate 回到事件循环）。
type gateDone struct {
	fp             fireParams
	stdout, stderr string
	exitCode       int
	err            error
}

// runGate 在后台执行运行前闸门（没有 host 在本机沙箱，否则经 SSH 网关）；运行保持 pending（占着重叠判定），
// 结果入队为 evGate。调用方持有 loopMu。闸门的 ctx 不随调用方取消（RunNow 的请求结束不应打断它），
// 上限由闸门自己的执行上限保证。
func (s *Service) runGate(ctx context.Context, fp fireParams) {
	sc := fp.sc
	g := gateOf(sc)
	done := func(stdout, stderr string, code int, err error) {
		s.enqueue(event{kind: evGate, runID: fp.runID, gate: &gateDone{fp: fp, stdout: stdout, stderr: stderr, exitCode: code, err: err}})
	}
	gctx := context.WithoutCancel(ctx)
	timeout := time.Duration(*g.TimeoutSeconds) * time.Second
	label := fmt.Sprintf("schedule %s run #%d gate", sc.ID, fp.run)
	if g.Host == nil {
		local := s.cfg.LocalGate
		if local == nil {
			done("", "", 0, errors.New("local pre-run gates are unavailable"))
			return
		}
		s.cfg.Go(func() { done(local.Run(gctx, g.Command, label, timeout)) })
		return
	}
	pid, auto, err := gateScope(ctx, s.st.Q, sc.Mode, deref(sc.ThreadID), deref(sc.ProjectID))
	switch {
	case err != nil:
		done("", "", 0, fmt.Errorf("resolve gate scope: %w", err))
		return
	case s.cfg.SSHGate == nil:
		done("", "", 0, errors.New("SSH pre-run gates are unavailable (no SSH gateway)"))
		return
	}
	ssh := s.cfg.SSHGate
	s.cfg.Go(func() { done(ssh.RunGate(gctx, pid, auto, *g.Host, g.Command, label, timeout)) })
}

// onGate 按闸门结果跳过或继续一次运行。运行已不是 pending（定时被删、运行被清理）时丢弃结果。调用方持有 loopMu。
func (s *Service) onGate(ctx context.Context, runID string, g *gateDone) {
	run, err := s.st.Q.GetScheduleRun(ctx, runID)
	if err != nil || run.Status != runPending {
		return
	}
	if g.err == nil && g.exitCode == 0 && !wakeAgent(g.stdout) {
		s.finishRun(ctx, run.ScheduleID, runID, runSkipped, reasonGate)
		return
	}
	if g.err != nil {
		slog.Info("schedule: 闸门未能正常执行，照常运行", "schedule", run.ScheduleID, "run", runID, "err", g.err)
	}
	s.launch(ctx, g.fp, gateBlock(*gateOf(g.fp.sc), g))
}

// wakeAgent 判定闸门输出是否要唤醒 agent（同 Hermes 的约定）：只有最后一个非空行是 JSON 对象且其 wakeAgent 为 false 时不唤醒；
// 其它输出（含空输出、非 JSON、没有该键）一律唤醒。
func wakeAgent(stdout string) bool {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	var v map[string]any
	if json.Unmarshal([]byte(last), &v) != nil {
		return true
	}
	wake, ok := v["wakeAgent"].(bool)
	return !ok || wake
}

// gateBlock 是附在提示词前的闸门结果：一行说明（执行位置、退出码或失败原因），再接命令与 stdout / stderr 的代码块（空的省略）。
func gateBlock(g apigen.ScheduleGate, r *gateDone) string {
	where := "locally"
	if g.Host != nil {
		where = "on " + *g.Host
	}
	var b strings.Builder
	if r.err != nil {
		fmt.Fprintf(&b, "[Pre-run check %s failed: %v]", where, r.err)
	} else {
		fmt.Fprintf(&b, "[Pre-run check %s exited with code %d]", where, r.exitCode)
	}
	fmt.Fprintf(&b, "\nCommand:\n%s", fenced(g.Command))
	if strings.TrimSpace(r.stdout) != "" {
		fmt.Fprintf(&b, "\nStdout:\n%s", fenced(r.stdout))
	}
	if strings.TrimSpace(r.stderr) != "" {
		fmt.Fprintf(&b, "\nStderr:\n%s", fenced(r.stderr))
	}
	return b.String()
}

// fenced 用比内容里最长的连续反引号多一个（至少 3 个）的围栏包住 text。
func fenced(text string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + "\n" + strings.TrimRight(text, "\n") + "\n" + fence
}
