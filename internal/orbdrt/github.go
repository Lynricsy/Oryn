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

// orb 内 GitHub 工具（M15）在 orbd 的落点：github.call 原样转成 ctl github.call；github.prepare 先经 ctl github.call
// （github_gates）现查项目当前的门禁配置，在调用方线程的工作目录里执行 RunPrepare，通过后经 ctl github.prepare.report
// 带着这份配置的哈希上报，控制面据此在出站代理放行该 SHA（项目门禁之后再改，旧哈希的记录即不再放行）。
// 两个方法只在 orbd.sock 上提供（runner.sock 只认 artifact.call，runner 的 link 也拒绝这两个 ctl 方法）。

const (
	// githubCallTimeout 是 github.call 的转发上限（控制面在这次请求里可能要调 GitHub API）。
	githubCallTimeout = 2 * time.Minute
	// githubReportTimeout 是 github.prepare.report 的上限。
	githubReportTimeout = 30 * time.Second
	// githubReleaseQueryTimeout 是 github.prepare 查询线程发布类别的上限（brief 查询不读 GitHub）。
	githubReleaseQueryTimeout = 30 * time.Second
	// githubGatesQueryTimeout 是 github.prepare 现查项目门禁的上限（控制面只读库）。
	githubGatesQueryTimeout = 10 * time.Second
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

// githubPrepare 处理 orbd.sock 的 github.prepare：先向控制面现查项目当前的门禁配置（失败即本次失败，什么都不执行），
// 再查询线程是否为发布任务（决定能否在默认分支上），然后在调用方线程的工作目录里按这份门禁执行 RunPrepare；通过时
// 带上门禁哈希上报控制面（上报失败即整体失败：未登记的 SHA 推送时会被出站代理拦下），再把结果回给调用方。
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
	gates, gatesHash, err := r.prepareGates(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	dir := r.threadWorkdir(p.ThreadID)
	// 与 agent 命令相同的环境：基础环境 + setup 下发的环境，经登录 shell 求值（~/.profile 里的工具链生效）。
	env := r.baseEnv()
	maps.Copy(env, r.setupEnvSnapshot())
	env = loginEnv(ctx, dir, env)
	res, err := RunPrepare(ctx, PrepareRequest{
		Dir: dir, Level: p.Level, SkipChecks: p.SkipChecks, Reason: p.Reason, Release: r.releaseThread(ctx, p.ThreadID),
		Gates: gates, Identity: r.currentGitIdentity(), Env: envList(env),
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
		GatesHash: gatesHash,
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

// prepareGates 向控制面现查项目当前的门禁配置与其哈希（M15）：ctl github.call 的 github_gates（同一线程 id，不带参数），
// 答复 details 里的 `gates`（未配置为 null）与 `gatesHash`。setup / resume 时下发的那份不再作为命令来源：项目门禁经
// PATCH 改动后，它要到下次 setup 才更新，拿它回落会在查询短暂失败时照旧跑旧命令，又按新门禁登记通过。所以查询超时、
// 出错或答复无法解析时本次 github_prepare 直接失败（不执行任何命令），错误说明原因，agent 重试即可；skipChecks 同样
// 需要这次查询（通过记录要绑定门禁哈希）。
func (r *Runtime) prepareGates(ctx context.Context, threadID string) (apigen.GitHubGates, string, error) {
	fail := func(code, msg string) (apigen.GitHubGates, string, error) {
		return apigen.GitHubGates{}, "", &orbsock.Error{Code: code,
			Message: "github_prepare could not read the project's current gate configuration from the control plane (" + msg +
				"); nothing was run. Retry github_prepare."}
	}
	raw, err := r.ctlForwardFor(ctx, apigen.MethodGithubCall, apigen.LinkCtlGithubCallParams{
		ThreadID: threadID, Tool: "github_gates", Params: map[string]any{},
	}, githubGatesQueryTimeout)
	if err != nil {
		if ctx.Err() != nil {
			return apigen.GitHubGates{}, "", ctx.Err()
		}
		var se *orbsock.Error
		if errors.As(err, &se) {
			return fail(se.Code, se.Message)
		}
		return fail(orbsock.CodeInternal, err.Error())
	}
	var res struct {
		Details map[string]json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fail(orbsock.CodeInternal, "malformed reply: "+err.Error())
	}
	rawGates, ok := res.Details["gates"]
	var hash string
	if !ok || json.Unmarshal(res.Details["gatesHash"], &hash) != nil || !validGatesHash(hash) {
		return fail(orbsock.CodeInternal, "malformed reply: missing gates or gatesHash")
	}
	var g *apigen.GitHubGates
	if err := json.Unmarshal(rawGates, &g); err != nil {
		return fail(orbsock.CodeInternal, "malformed gates: "+err.Error())
	}
	if g == nil {
		return apigen.GitHubGates{}, hash, nil
	}
	return *g, hash, nil
}

// validGatesHash 判断 s 是否是门禁配置哈希的形状（sha256，64 位小写十六进制）。
func validGatesHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// releaseThread 向控制面查询线程是否为进行中的发布任务（M15 v4）：ctl github.call 的 github_release_status（brief，同一线程 id）
// 成功且状态为 awaiting_ci / fixing 时为真。「不是发布线程」、其它错误、超时或结果无法解析一律按非发布处理（只会多拒绝）。
// 类别只取控制面的任务状态，不接受 agent 传入的任何参数。
func (r *Runtime) releaseThread(ctx context.Context, threadID string) bool {
	raw, err := r.ctlForwardFor(ctx, apigen.MethodGithubCall, apigen.LinkCtlGithubCallParams{
		ThreadID: threadID, Tool: "github_release_status", Params: map[string]any{"brief": true},
	}, githubReleaseQueryTimeout)
	if err != nil {
		return false
	}
	var res apigen.LinkCtlGithubCallResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return false
	}
	state, _ := res.Details["state"].(string)
	return state == "awaiting_ci" || state == "fixing"
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
