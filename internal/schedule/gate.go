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
	"github.com/Lynricsy/Oryn/internal/sshgw"
)

// reasonGate 是闸门判定本次不必运行时运行记录的 error。
const reasonGate = "gate"

// gateDone 是一次运行前闸门的结果（经 evGate 回到事件循环）。
type gateDone struct {
	fp  fireParams
	res sshgw.GateResult
}

// runGate 在后台执行运行前闸门；运行保持 pending（占着重叠判定），结果入队为 evGate。调用方持有 loopMu。
// 闸门的 ctx 不随调用方取消（RunNow 的请求结束不应打断它），上限由闸门自己的执行上限保证。
func (s *Service) runGate(ctx context.Context, fp fireParams) {
	sc := fp.sc
	g := gateOf(sc)
	pid, auto, err := gateScope(ctx, s.st.Q, sc.Mode, deref(sc.ThreadID), deref(sc.ProjectID))
	done := func(res sshgw.GateResult) {
		s.enqueue(event{kind: evGate, runID: fp.runID, gate: &gateDone{fp: fp, res: res}})
	}
	switch {
	case err != nil:
		done(sshgw.GateResult{Err: fmt.Errorf("resolve gate scope: %w", err)})
		return
	case s.cfg.Gate == nil:
		done(sshgw.GateResult{Err: errors.New("pre-run gates are unavailable (no SSH gateway)")})
		return
	}
	gctx := context.WithoutCancel(ctx)
	timeout := time.Duration(*g.TimeoutSeconds) * time.Second
	label := fmt.Sprintf("schedule %s run #%d gate", sc.ID, fp.run)
	gate := s.cfg.Gate
	s.cfg.Go(func() {
		done(gate.RunGate(gctx, pid, auto, g.Host, g.Command, label, timeout))
	})
}

// onGate 按闸门结果跳过或继续一次运行。运行已不是 pending（定时被删、运行被清理）时丢弃结果。调用方持有 loopMu。
func (s *Service) onGate(ctx context.Context, runID string, g *gateDone) {
	run, err := s.st.Q.GetScheduleRun(ctx, runID)
	if err != nil || run.Status != runPending {
		return
	}
	if g.res.Err == nil && g.res.ExitCode == 0 && !wakeAgent(g.res.Stdout) {
		s.finishRun(ctx, run.ScheduleID, runID, runSkipped, reasonGate)
		return
	}
	if g.res.Err != nil {
		slog.Info("schedule: 闸门未能正常执行，照常运行", "schedule", run.ScheduleID, "run", runID, "err", g.res.Err)
	}
	s.launch(ctx, g.fp, gateBlock(*gateOf(g.fp.sc), g.res))
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

// gateBlock 是附在提示词前的闸门结果：一行说明（主机、退出码或失败原因），再接 stdout / stderr 的代码块（空的省略）。
func gateBlock(g apigen.ScheduleGate, r sshgw.GateResult) string {
	var b strings.Builder
	if r.Err != nil {
		fmt.Fprintf(&b, "[Pre-run check on %s failed: %v]", g.Host, r.Err)
	} else {
		fmt.Fprintf(&b, "[Pre-run check on %s exited with code %d]", g.Host, r.ExitCode)
	}
	fmt.Fprintf(&b, "\nCommand:\n%s", fenced(g.Command))
	if strings.TrimSpace(r.Stdout) != "" {
		fmt.Fprintf(&b, "\nStdout:\n%s", fenced(r.Stdout))
	}
	if strings.TrimSpace(r.Stderr) != "" {
		fmt.Fprintf(&b, "\nStderr:\n%s", fenced(r.Stderr))
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
