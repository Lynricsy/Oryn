package github

import (
	"context"
	"fmt"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// github_gates（M15）：orbd 的 github.prepare 每次执行前经 ctl github.call 现查项目当前的门禁配置（修复 / 检查 / 测试
// 命令）与它的规范化哈希，不再用 setup 时下发的那份：项目门禁经 PATCH 改动后，运行中的 orb 下一次 prepare 就跑新命令。
// 通过记录绑定这个哈希（github.prepare.report 原样带回），出站门禁只认与项目当前哈希相同的记录（见 Prepared）。
//
// 这是 orbd 内部用的工具：不在 callTools 里，orb 内 omp 扩展也不把它注册成 agent 工具。orb 内的进程仍能经 orbd.sock
// 的 github.call 直接调用它（调用方线程照常经 orbs 校验），但答复只有调用方 orb 自己项目的门禁配置与哈希，都不是秘密：
// 门禁命令与 PR 标题规则 agent 在 github_prepare 的步骤输出里本来就看得到，哈希只是这份配置的摘要，不是凭据。

// toolGatesName 是 github_gates 的工具名（同 orbdrt 的 github.prepare 发出的名字）。
const toolGatesName = "github_gates"

// toolGates 实现 github_gates：不接受参数；Details 为 `gates`（apigen.GitHubGates，未配置为 null）与 `gatesHash`
// （orbs.GitHubGatesHash）。读库失败回错误（orbd 据此让本次 prepare 失败，不回落到 setup 时的那份）。
func (s *Service) toolGates(ctx context.Context, orbID string, params map[string]any) (apigen.LinkCtlGithubCallResult, error) {
	if err := decodeParams(params, &struct{}{}); err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	raw, err := s.orbGates(ctx, orbID)
	if err != nil {
		return apigen.LinkCtlGithubCallResult{}, err
	}
	g := orbs.ParseGitHubGates(raw)
	hash := orbs.GitHubGatesHash(raw)
	text := "The project has no GitHub gates configured."
	if g != nil {
		text = fmt.Sprintf("Project GitHub gates: push=%t pr=%t.", g.Push, g.Pr)
	}
	return apigen.LinkCtlGithubCallResult{Text: text, Details: map[string]any{"gates": g, "gatesHash": hash}}, nil
}

// orbGates 读 orb 所属项目的门禁配置原文（projects.github_gates），与 setup 计划取项目的规则一致：协调宿主、没有项目
// 或项目已删除时为 nil（未配置）。读库失败返回错误。
func (s *Service) orbGates(ctx context.Context, orbID string) (*string, error) {
	orb, err := s.st.Q.GetOrb(ctx, orbID)
	if err != nil {
		return nil, err
	}
	if orb.Coordinator || orb.ProjectID == nil {
		return nil, nil
	}
	prj, err := s.st.Q.GetProject(ctx, *orb.ProjectID)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return prj.GithubGates, nil
}
