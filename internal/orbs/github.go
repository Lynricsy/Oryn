package orbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// GitHubTokenState 是一台 orb 取 GitHub 令牌的结果（M14）。
type GitHubTokenState int

const (
	GitHubTokenUnmanaged GitHubTokenState = iota // 这台 orb 不归 GitHub App 管（项目未绑定，或 user 槽位未连接）：调用方可回落到 PAT
	GitHubTokenOK
	GitHubTokenUnavailable // 归 App 管但拿不到令牌（agent 槽位不可用、签发失败）：调用方不得回落
)

// GitHubSetup 是一台 orb 的 GitHub 下发参数。
type GitHubSetup struct {
	Login                string
	GitIdentity          apigen.GitIdentity
	Agent                bool // 身份为 agent：GitIdentity 覆盖项目与所有者设置
	APIOrigin, GitOrigin string
}

// GitHub 是 GitHub 集成给 setup 计划、出站代理与 orb 内 GitHub 工具提供的能力（M14 / M15）；nil = 未启用。
type GitHub interface {
	OrbSetup(ctx context.Context, orb db.Orb, prj db.Project) (GitHubSetup, bool)
	OrbToken(ctx context.Context, orbID string) (string, GitHubTokenState)
	// Call 执行 orb 内 agent 的 GitHub 工具（ctl github.call，M15）；调用方线程已由 orbs 校验。
	Call(ctx context.Context, orbID, threadID string, p apigen.LinkCtlGithubCallParams) (apigen.LinkCtlGithubCallResult, error)
	// PrepareReport 登记 github_prepare 的通过记录（ctl github.prepare.report，M15）；调用方线程已由 orbs 校验。
	PrepareReport(ctx context.Context, orbID string, p apigen.LinkCtlGithubPrepareReportParams) error
}

// CtlCoded 由带错误码的领域错误实现（如 github.Error）：ctl 请求按其错误码与文案回给 orbd。
type CtlCoded interface {
	CtlError() *link.CtlError
}

// threadKindWork 是工作线程的种类（同 threads.KindWork；orbs 不导入 threads）。
const threadKindWork = "work"

// CodeGitHubDisabled 是 GitHub 集成未启用时 github.* ctl 请求回的错误码。
const CodeGitHubDisabled = "github_disabled"

// onGithubCall 处理 orbd 转来的 github.call（M15）。
func (s *Service) onGithubCall(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlGithubCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &link.CtlError{Code: "invalid_params", Message: err.Error()}
	}
	if err := s.githubCaller(ctx, orbID, p.ThreadID); err != nil {
		return nil, err
	}
	res, err := s.github.Call(ctx, orbID, p.ThreadID, p)
	if err != nil {
		return nil, githubCtlErr(err)
	}
	return res, nil
}

// onGithubPrepareReport 处理 orbd 上报的 github.prepare.report（M15）。
func (s *Service) onGithubPrepareReport(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlGithubPrepareReportParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &link.CtlError{Code: "invalid_params", Message: err.Error()}
	}
	if err := s.githubCaller(ctx, orbID, p.ThreadID); err != nil {
		return nil, err
	}
	if err := s.github.PrepareReport(ctx, orbID, p); err != nil {
		return nil, githubCtlErr(err)
	}
	return apigen.LinkCtlOkResult{Ok: true}, nil
}

// githubCaller 校验 github.* 的调用方线程：集成已启用；线程存在、是工作线程、未归档、属于发起 link 的 orb
// （同 artifact.call；否则 forbidden）。
func (s *Service) githubCaller(ctx context.Context, orbID, threadID string) error {
	if s.github == nil {
		return &link.CtlError{Code: CodeGitHubDisabled, Message: "GitHub 集成未启用。"}
	}
	forbidden := &link.CtlError{Code: "forbidden", Message: "调用方线程不存在、已归档，或不属于这台 orb。"}
	if threadID == "" {
		return forbidden
	}
	t, err := s.st.Q.GetThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return forbidden
	}
	if err != nil {
		return err
	}
	if t.Kind != threadKindWork || t.ArchivedAt != nil || t.OrbID == nil || *t.OrbID != orbID {
		return forbidden
	}
	return nil
}

// githubCtlErr 把 GitHub 集成返回的错误转成 ctl 错误：带错误码的原样透传，其余按内部错误处理。
func githubCtlErr(err error) error {
	var ce *link.CtlError
	if errors.As(err, &ce) {
		return ce
	}
	var cc CtlCoded
	if errors.As(err, &cc) {
		return cc.CtlError()
	}
	return err
}
