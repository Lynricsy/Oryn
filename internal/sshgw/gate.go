package sshgw

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/headtail"
)

// GateOutputHalf 是运行前闸门每路输出（stdout / stderr）保留的头尾字节数：输出会附进提示词，比工具结果宽一些。
// 本机沙箱的闸门用同一上限。
const GateOutputHalf = 16 << 10

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
// （来源 owner，detail 为 label 与命令）。整个过程（含拨号）受 timeout 约束。err 非空表示命令没有正常结束（主机不可用、
// 连不上、超时、执行失败），此时 exitCode 无意义；命令以非 0 退出不算 err。
// 连接与网关会话一样登记为活动连接：拨号期间链上主机被改（停用、��回全自动权限、改配置）则不执行；执行中被改则断开，按执行失败返回。
func (s *Service) RunGate(ctx context.Context, projectID string, automation bool, host, command, label string, timeout time.Duration) (stdout, stderr string, exitCode int, err error) {
	since := s.snapshot() // 先取快照再读主机：之后的任何修改都会让登记失败
	h, deny, err := s.resolve(ctx, gateViewer(projectID, automation), host)
	if err == nil && deny != "" {
		s.audit(ctx, auditRow{source: srcOwner, host: h, hostName: host, kind: "deny", detail: label + ": " + deny})
		return "", "", 0, errors.New(deny)
	}
	if err != nil {
		return "", "", 0, err
	}
	start := time.Now()
	row := auditRow{source: srcOwner, host: h, kind: "exec", detail: label + ": " + command}
	defer func() {
		d := time.Since(start).Milliseconds()
		row.durationMs, row.ok = &d, err == nil
		if err == nil {
			code := int64(exitCode)
			row.exitCode = &code
		}
		s.audit(ctx, row)
	}()

	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	up, err := s.dial(dctx, h)
	if err != nil {
		if dctx.Err() != nil && ctx.Err() == nil {
			return "", "", 0, fmt.Errorf("timed out after %s while connecting", timeout)
		}
		return "", "", 0, err
	}
	defer up.Close()
	// 拨通即登记：拨号期间链上主机被改则作废、不执行；执行中被改由 invalidate 关掉上游连接
	untrack, ok := s.track(up.hostIDs, since, up.Close)
	if !ok {
		return "", "", 0, errStale
	}
	defer untrack()
	out, errOut := headtail.New(GateOutputHalf), headtail.New(GateOutputHalf)
	expired, runErr := runBounded(dctx, up.client, up.Close, func(sess *ssh.Session) { sess.Stdout, sess.Stderr = out, errOut }, command)
	stdout, stderr = out.String(), errOut.String()
	var ee *ssh.ExitError
	switch {
	case expired && ctx.Err() != nil:
		err = errors.New("canceled")
	case expired:
		err = fmt.Errorf("timed out after %s", timeout)
	case runErr == nil:
	case errors.As(runErr, &ee):
		exitCode = ee.ExitStatus()
	default:
		err = fmt.Errorf("exec failed: %w", runErr)
	}
	return stdout, stderr, exitCode, err
}
