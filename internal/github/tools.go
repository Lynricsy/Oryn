package github

import (
	"context"
	"encoding/hex"
	"log/slog"
	"slices"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// orb 内 GitHub 工具（M15 R17）的控制面入口：ctl github.call 与 github.prepare.report。
// 调用方线程（存在、工作线程、未归档、属于发起 link 的 orb）已由 orbs 校验。

// 门禁级别（同 orbdrt.PrepareLevelPush / PrepareLevelPR）。
const (
	levelPush = "push"
	levelPR   = "pr"
)

// 工具错误码。
const (
	codeNotImplemented = "not_implemented"
	codeInvalidParams  = "invalid_params"
)

// callTools 是经 ctl github.call 执行的工具（github_prepare 由 orbd 执行，不在其中）。
var callTools = []string{
	"github_classify_issue",
	"github_task_state",
	"github_autoclose",
	"github_release_status",
	"github_abort_task",
}

// CtlError 让 orbs 把领域错误按原错误码回给 orbd（实现 orbs.CtlCoded）。
func (e *Error) CtlError() *link.CtlError {
	msg := e.Message
	if msg == "" {
		msg = e.Code
	}
	return &link.CtlError{Code: e.Code, Message: msg}
}

// Call 执行一次 orb 内 GitHub 工具调用（ctl github.call）。未知工具名回 invalid_params；github_release_status 在
// 步骤 6.1 实现之前回 not_implemented。
func (s *Service) Call(ctx context.Context, orbID, threadID string, p apigen.LinkCtlGithubCallParams) (apigen.LinkCtlGithubCallResult, error) {
	switch p.Tool {
	case "github_classify_issue":
		return s.toolClassify(ctx, threadID, p.Params)
	case "github_task_state":
		return s.toolTaskState(ctx, threadID, p.Params)
	case "github_autoclose":
		return s.toolAutoclose(ctx, threadID, p.Params)
	case "github_abort_task":
		return s.toolAbortTask(ctx, orbID, threadID, p.Params)
	}
	if !slices.Contains(callTools, p.Tool) {
		return apigen.LinkCtlGithubCallResult{}, errf(codeInvalidParams,
			"未知的 GitHub 工具 "+p.Tool+"；可用："+strings.Join(callTools, "、"))
	}
	return apigen.LinkCtlGithubCallResult{}, errf(codeNotImplemented, p.Tool+" 尚未实现")
}

// PrepareReport 登记 github_prepare 的通过记录（ctl github.prepare.report）：同一 (sha, orb, level) 再次上报时覆盖
// skipped / reason / at。出站代理经 PrepareOK 判断是否放行该 SHA。
func (s *Service) PrepareReport(ctx context.Context, orbID string, p apigen.LinkCtlGithubPrepareReportParams) error {
	level := string(p.Level)
	if level != levelPush && level != levelPR {
		return errf(codeInvalidParams, "level 必须是 push 或 pr")
	}
	sha := strings.ToLower(p.Sha)
	if !validSHA(sha) {
		return errf(codeInvalidParams, "sha 必须是完整的提交哈希")
	}
	var reason *string
	if p.Reason != nil && strings.TrimSpace(*p.Reason) != "" {
		reason = p.Reason
	}
	if p.Skipped && reason == nil {
		return errf(codeInvalidParams, "跳过检查时必须给出 reason")
	}
	if err := s.st.Q.UpsertGithubPrepare(ctx, db.UpsertGithubPrepareParams{
		Sha: sha, OrbID: orbID, Level: level, Skipped: p.Skipped, Reason: reason, At: s.nowMs(),
	}); err != nil {
		return err
	}
	attrs := []any{"orb", orbID, "thread", p.ThreadID, "sha", sha, "level", level, "skipped", p.Skipped}
	if reason != nil {
		attrs = append(attrs, "reason", *reason)
	}
	slog.Info("github: prepare 通过", attrs...)
	return nil
}

// PrepareOK 报告 orb 是否登记过 sha 在 level 上的通过记录；pr 级的记录同时满足 push。
// 有多条时未跳过检查的优先（skipped 反映选中的那条）。查询失败按未通过处理。
func (s *Service) PrepareOK(ctx context.Context, orbID, sha, level string) (ok, skipped bool) {
	var levels []string
	switch level {
	case levelPush:
		levels = []string{levelPush, levelPR}
	case levelPR:
		levels = []string{levelPR}
	default:
		return false, false
	}
	rec, err := s.st.Q.FindGithubPrepare(ctx, db.FindGithubPrepareParams{Sha: strings.ToLower(sha), OrbID: orbID, Levels: levels})
	if err != nil {
		if !isNoRows(err) {
			slog.Warn("github: 读取 prepare 记录失败", "orb", orbID, "sha", sha, "err", err)
		}
		return false, false
	}
	return true, rec.Skipped
}

// validSHA 判断 s 是否是完整的十六进制提交哈希（SHA-1 40 位或 SHA-256 64 位，小写）。
func validSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
