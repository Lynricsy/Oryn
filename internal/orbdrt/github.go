package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// orb 内 GitHub 工具（M15）在 orbd 的落点：github.call 原样转成 ctl github.call；github.prepare 在调用方线程的
// 工作目录里执行 RunPrepare，通过后经 ctl github.prepare.report 上报，控制面据此在出站代理放行该 SHA。
// 两个方法只在 orbd.sock 上提供（runner.sock 只认 artifact.call，runner 的 link 也拒绝这两个 ctl 方法）。

const (
	// githubCallTimeout 是 github.call 的转发上限（控制面在这次请求里可能要调 GitHub API）。
	githubCallTimeout = 2 * time.Minute
	// githubReportTimeout 是 github.prepare.report 的上限。
	githubReportTimeout = 30 * time.Second
)

// githubCall 处理 orbd.sock 的 github.call：校验最基本的形状后原样转成 ctl github.call。
func (r *Runtime) githubCall(ctx context.Context, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlGithubCallParams
	if err := decodeSock(params, &p); err != nil {
		return nil, err
	}
	if !validThreadID(p.ThreadID) {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "invalid threadId %q", p.ThreadID)
	}
	if strings.TrimSpace(p.Tool) == "" {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "missing tool name")
	}
	if p.Params == nil {
		p.Params = map[string]any{}
	}
	return r.ctlForwardFor(ctx, apigen.MethodGithubCall, p, githubCallTimeout)
}

// githubPrepare 处理 orbd.sock 的 github.prepare：在调用方线程的工作目录里按项目门禁执行 RunPrepare；
// 通过时先上报控制面（上报失败即整体失败：未登记的 SHA 推送时会被出站代理拦下），再把结果回给调用方。
func (r *Runtime) githubPrepare(ctx context.Context, params json.RawMessage) (any, error) {
	var p orbsock.GithubPrepareParams
	if err := decodeSock(params, &p); err != nil {
		return nil, err
	}
	if !validThreadID(p.ThreadID) {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "invalid threadId %q", p.ThreadID)
	}
	if p.Level != PrepareLevelPush && p.Level != PrepareLevelPR {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "invalid level %q: use push or pr", p.Level)
	}
	if p.SkipChecks && strings.TrimSpace(p.Reason) == "" {
		return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "skipChecks requires a non-empty reason")
	}
	// 先确认连得上控制面：离线时跑完也无法登记，白白改写提交。
	if r.current() == nil {
		return nil, orbsock.Errorf(orbsock.CodeOffline, "orbd is not connected to the control plane")
	}
	dir := r.threadWorkdir(p.ThreadID)
	// 与 agent 命令相同的环境：基础环境 + setup 下发的环境，经登录 shell 求值（~/.profile 里的工具链生效）。
	env := r.baseEnv()
	maps.Copy(env, r.setupEnvSnapshot())
	env = loginEnv(ctx, dir, env)
	res, err := RunPrepare(ctx, PrepareRequest{
		Dir: dir, Level: p.Level, SkipChecks: p.SkipChecks, Reason: p.Reason,
		Gates: r.currentGitHubGates(), Identity: r.currentGitIdentity(), Env: envList(env),
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, orbsock.Errorf(orbsock.CodeInternal, "github_prepare: %v", err)
	}
	if res.Steps == nil {
		res.Steps = []PrepareStep{}
	}
	if !res.OK {
		return res, nil
	}
	rep := apigen.LinkCtlGithubPrepareReportParams{
		ThreadID: p.ThreadID, Sha: res.SHA, Level: apigen.LinkCtlGithubPrepareReportParamsLevel(p.Level), Skipped: res.Skipped,
	}
	if p.SkipChecks {
		rep.Reason = new(p.Reason)
	}
	if _, err := r.ctlForwardFor(ctx, apigen.MethodGithubPrepareReport, rep, githubReportTimeout); err != nil {
		var se *orbsock.Error
		if errors.As(err, &se) {
			return nil, &orbsock.Error{Code: se.Code, Message: "github_prepare passed but reporting it failed: " + se.Message}
		}
		return nil, err
	}
	return res, nil
}

// threadWorkdir 返回线程 agent 的工作目录（agent.start 的 cwd，含 worktree）；线程不在本 orbd 上运行过时
// 回落到 orb 工作目录。
func (r *Runtime) threadWorkdir(id string) string {
	if t := r.lookupThread(id); t != nil {
		t.mu.Lock()
		s := t.sup
		t.mu.Unlock()
		if s != nil && s.spec.cwd != "" {
			return s.spec.cwd
		}
	}
	return r.currentWorkdir()
}
