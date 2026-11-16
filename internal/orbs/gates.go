package orbs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
)

// 项目门禁（projects.github_gates，M15）的读写：API 写入经 EncodeGitHubGates 校验、规整后存库，setup 与 API 视图都经
// ParseGitHubGates 读出，两端用同一个 apigen.GitHubGates 形状，不会各自漂移。

// GitHubGates 契约（openapi GitHubGates）的长度上限。
const (
	gateCmdMax      = 2000
	gateHeadingsMax = 20
	gateHeadingMax  = 100
)

// ParseGitHubGates 解析项目的 github_gates（JSON），供 orbd 的 github_prepare 读取修复 / 检查 / 测试命令、出站代理的门禁
// 与项目的 API 视图；NULL、空串或解析失败时返回 nil（视为未配置）。
func ParseGitHubGates(raw *string) *apigen.GitHubGates {
	if raw == nil || *raw == "" {
		return nil
	}
	var g apigen.GitHubGates
	if json.Unmarshal([]byte(*raw), &g) != nil {
		return nil
	}
	return &g
}

// EncodeGitHubGates 按 GitHubGates 契约校验 API 写入的门禁（body 是该字段的原始 JSON），返回要存入 github_gates 的 JSON：
// push、pr 必填；命令去掉首尾空白、空命令省略，长度 ≤2000；prHeadings 至多 20 条，每条去掉首尾空白后须为 1–100 字符的单行。
// 什么都没配置（两个门禁都关、没有命令与标题、不要求关闭引用）时返回 nil，即存 NULL = 未配置。
// 返回的错误是给调用方的校验说明。
func EncodeGitHubGates(body []byte) (*string, error) {
	// push / pr 必填：生成类型里的布尔零值分不出「未给出」，先单独看是否出现
	var req struct {
		Push *bool `json:"push"`
		Pr   *bool `json:"pr"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("githubGates 格式错误: %w", err)
	}
	if req.Push == nil || req.Pr == nil {
		return nil, errors.New("githubGates 须同时给出 push 与 pr")
	}
	var g apigen.GitHubGates
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("githubGates 格式错误: %w", err)
	}
	for _, c := range []struct {
		name string
		v    **string
	}{{"fixCmd", &g.FixCmd}, {"checkCmd", &g.CheckCmd}, {"testCmd", &g.TestCmd}} {
		if *c.v == nil {
			continue
		}
		cmd := strings.TrimSpace(**c.v)
		if utf8.RuneCountInString(cmd) > gateCmdMax {
			return nil, fmt.Errorf("githubGates.%s 长度须 ≤%d", c.name, gateCmdMax)
		}
		*c.v = nil
		if cmd != "" {
			*c.v = &cmd
		}
	}
	if len(g.PrHeadings) > gateHeadingsMax {
		return nil, fmt.Errorf("githubGates.prHeadings 至多 %d 条", gateHeadingsMax)
	}
	var headings []string
	for _, h := range g.PrHeadings {
		h = strings.TrimSpace(h)
		if n := utf8.RuneCountInString(h); n < 1 || n > gateHeadingMax {
			return nil, fmt.Errorf("githubGates.prHeadings 每条须为 1–%d 字符", gateHeadingMax)
		}
		// 出站代理逐行匹配 `## <标题>`：含换行等控制字符的标题永远匹配不上，会让所有 PR 都被拒
		if strings.ContainsFunc(h, unicode.IsControl) {
			return nil, errors.New("githubGates.prHeadings 每条须是单行文本")
		}
		headings = append(headings, h)
	}
	g.PrHeadings = headings
	if g.RequireClosingRef != nil && !*g.RequireClosingRef {
		g.RequireClosingRef = nil
	}
	if !g.Push && !g.Pr && g.FixCmd == nil && g.CheckCmd == nil && g.TestCmd == nil && g.PrHeadings == nil && g.RequireClosingRef == nil {
		return nil, nil
	}
	b, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	s := string(b)
	return &s, nil
}

// gateConfig 把项目的门禁转成出站代理的门禁配置；推送、PR 门禁都关闭时为 nil（等于没有门禁）。
func gateConfig(g *apigen.GitHubGates) *egress.GateConfig {
	if g == nil || (!g.Push && !g.Pr) {
		return nil
	}
	return &egress.GateConfig{
		Push: g.Push, PR: g.Pr, PRHeadings: g.PrHeadings,
		RequireClosingRef: g.RequireClosingRef != nil && *g.RequireClosingRef,
	}
}

// InvalidateProjectPolicies 在项目门禁改动后作废该项目各 orb 缓存的出站策略：门禁是出站代理强制的安全边界，
// 不能等到下次 setup / resume 才生效。作废后下一个出站请求由 policyFor 按当前配置重建（与 orynd 重启后相同）；
// orbd 侧 github_prepare 的命令仍随下次 setup / resume 下发。读 orb 列表失败时作废全部缓存（宁可多重建，不留旧门禁）。
func (s *Service) InvalidateProjectPolicies(ctx context.Context, projectID string) {
	rows, err := s.st.Q.ListOrbs(ctx, nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		slog.Warn("读取 orb 列表失败，作废全部出站策略缓存", "project", projectID, "err", err)
		clear(s.policies)
		return
	}
	for _, o := range rows {
		if o.ProjectID != nil && *o.ProjectID == projectID {
			delete(s.policies, o.ID)
		}
	}
}
