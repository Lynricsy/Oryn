package sshgw

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
)

// gateOutputHalf 是运行前闸门每路输出（stdout / stderr）保留的头尾字节数：输出会附进提示词，比工具结果宽一些。
const gateOutputHalf = 16 << 10

// GateResult 是一次运行前闸门的执行结果。Err 非空表示命令没有正常结束（主机不可用、连不上、超时、执行失败），
// 此时 ExitCode 无意义；命令以非 0 退出不算 Err。
type GateResult struct {
	Stdout, Stderr string
	ExitCode       int
	Err            error
}

// gateViewer 是定时闸门的可见性主体：不经 orb，按定时的项目与全自动标记解析主机（与该定时建出的线程看到的一致）。
func gateViewer(projectID string, automation bool) viewer {
	v := viewer{automation: automation}
	if projectID != "" {
		v.projectID = &projectID
	}
	return v
}

// CheckGateHost 检查主机对该范围可用（保存定时时的预检）；不可用时返回给人看的原因。projectID 为空 = 只看全局主机。
func (s *Service) CheckGateHost(ctx context.Context, projectID string, automation bool, host string) error {
	_, deny, err := s.resolve(ctx, gateViewer(projectID, automation), host)
	if err != nil {
		return err
	}
	if deny != "" {
		return errors.New(deny)
	}
	return nil
}

// RunGate 代主人在主机上执行定时的运行前闸门（控制面直接拨号，不经 orb），分开收集 stdout 与 stderr，并记一行审计
// （来源 owner，detail 为 label 与命令）。整个过程（含拨号）受 timeout 约束。
func (s *Service) RunGate(ctx context.Context, projectID string, automation bool, host, command, label string, timeout time.Duration) (res GateResult) {
	h, deny, err := s.resolve(ctx, gateViewer(projectID, automation), host)
	if err == nil && deny != "" {
		s.audit(ctx, auditRow{source: srcOwner, host: h, hostName: host, kind: "deny", detail: label + ": " + deny})
		return GateResult{Err: errors.New(deny)}
	}
	if err != nil {
		return GateResult{Err: err}
	}
	start := time.Now()
	row := auditRow{source: srcOwner, host: h, kind: "exec", detail: label + ": " + command}
	defer func() {
		d := time.Since(start).Milliseconds()
		row.durationMs, row.ok = &d, res.Err == nil
		if res.Err == nil {
			code := int64(res.ExitCode)
			row.exitCode = &code
		}
		s.audit(ctx, row)
	}()

	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	up, err := s.dial(dctx, h)
	if err != nil {
		if dctx.Err() != nil && ctx.Err() == nil {
			return GateResult{Err: fmt.Errorf("timed out after %s while connecting", timeout)}
		}
		return GateResult{Err: err}
	}
	defer up.Close()
	stdout, stderr := &headTail{half: gateOutputHalf}, &headTail{half: gateOutputHalf}
	expired, err := runBounded(dctx, up.client, up.Close, func(sess *ssh.Session) { sess.Stdout, sess.Stderr = stdout, stderr }, command)
	res = GateResult{Stdout: stdout.String(), Stderr: stderr.String()}
	var ee *ssh.ExitError
	switch {
	case expired && ctx.Err() != nil:
		res.Err = errors.New("canceled")
	case expired:
		res.Err = fmt.Errorf("timed out after %s", timeout)
	case err == nil:
	case errors.As(err, &ee):
		res.ExitCode = ee.ExitStatus()
	default:
		res.Err = fmt.Errorf("exec failed: %w", err)
	}
	return res
}
