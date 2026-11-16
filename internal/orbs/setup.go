package orbs

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// SetupPlan 是一次 setup.run / resume.run 的参数与随之生效的 egress 路由。
type SetupPlan struct {
	Params apigen.LinkCtlSetupRunParams
	// Egress 是随之生效的出站策略（模型路由来自 agentcfg.Rendered.Routes，git 仓库限定来自项目 repo_url）。
	Egress egress.Policy
	// ConfigHash 是与 Params 出自同一份输入（同一次读到的项目行、与密钥值一致的修改时间、这台 orb 建立时的基础镜像
	// 身份）的项目配置哈希（CP-1）；无项目或协调 orb 为空。快照发布与「已应用的配置」都只用它，不再另读当前配置。
	ConfigHash string
}

// SetupSource 为 orb 生成 setup / resume 参数。
type SetupSource interface {
	Plan(ctx context.Context, orb db.Orb) (SetupPlan, error)
}

// Vault 是 DefaultSetup 需要的密钥库能力（*secrets.Vault 实现）。
type Vault interface {
	agentcfg.Resolver
	SecretByID(id string) (value string, hosts []string, ok bool)
	GitToken(host string) (string, bool)
	// EnvSecrets 返回 mode=env 的密钥（变量名 = 密钥名）。
	EnvSecrets() (map[string]string, error)
	// GitHosts 返回存在 git/<host>/token 密钥的全部主机。
	GitHosts() ([]string, error)
}

// Home 是 omp 文本配置存储（*agentcfg.Home 实现）。
type Home interface {
	LoadAll() (map[string][]byte, error)
	Modes() (map[string]os.FileMode, error)
}

// ProjectConfigs 是项目级 omp 配置存储（*agentcfg.ProjectHomes 实现，M12.2）。
type ProjectConfigs interface {
	Load(projectID string) (map[string][]byte, map[string]os.FileMode, error)
}

// DefaultSetup 是默认的 SetupSource：项目（仓库、分支、浅克隆、脚本）+ omp-home 下发变换 + 密钥。
type DefaultSetup struct {
	Store *store.Store
	Home  Home  // 可为 nil（不下发 omp 配置）
	Vault Vault // 可为 nil（无密钥）
	// CoordinatorEgressAddr 是协调宿主内模型代理的监听地址（127.0.0.1:<coordinator.egress_port>）；
	// 空 = agentcfg.EgressAddr。
	CoordinatorEgressAddr string
	// Projects 是项目级 omp 配置；可为 nil（不下发项目级配置）。
	Projects ProjectConfigs
	// GitHub 是 GitHub 集成（M14）；可为 nil（不下发 gh 配置，github.com 只走 PAT）。
	GitHub GitHub
}

// coordinatorFiles 是下发给协调宿主的 omp-home 文件：只有模型与基本设置，其余（mcp.json、extensions/、skills/、
// agents/、commands/、AGENTS.md…）一律不下发。
var coordinatorFiles = map[string]bool{agentcfg.ModelsFile: true, "config.yml": true}

// planCoordinator 是协调 orb 的 setup 参数：不取项目、不下发 git 身份 / GitHosts / env 模式密钥，omp-home 只有
// models.yml 与 config.yml（模型地址改写到协调宿主的代理端口），环境变量降级的模型密钥丢弃，git 出站一律拒绝。
func (d *DefaultSetup) planCoordinator(orb db.Orb) (SetupPlan, error) {
	plan := SetupPlan{
		Params: apigen.LinkCtlSetupRunParams{
			GitHosts: []string{}, OmpHome: []apigen.LinkCtlOmpHomeFile{}, Env: map[string]string{},
		},
		Egress: egress.Policy{Routes: map[string]egress.Route{}, GitRepos: map[string][]string{}, NoGit: true},
	}
	if d.Home == nil {
		return plan, nil
	}
	files, err := d.Home.LoadAll()
	if err != nil {
		return plan, fmt.Errorf("读取 omp-home: %w", err)
	}
	modes, err := d.Home.Modes()
	if err != nil {
		return plan, fmt.Errorf("读取 omp-home 权限: %w", err)
	}
	kept := map[string][]byte{}
	for p, b := range files {
		if coordinatorFiles[p] {
			kept[p] = b
		}
	}
	var resolver agentcfg.Resolver = emptyResolver{}
	if d.Vault != nil {
		resolver = d.Vault
	}
	addr := d.CoordinatorEgressAddr
	if addr == "" {
		addr = agentcfg.EgressAddr
	}
	rendered, err := agentcfg.RenderAt(kept, resolver, addr)
	if err != nil {
		return plan, fmt.Errorf("变换 omp-home: %w", err)
	}
	if len(rendered.Env) > 0 {
		names := make([]string, 0, len(rendered.Env))
		for k := range rendered.Env {
			names = append(names, k)
		}
		sort.Strings(names)
		slog.Warn("orbs: 以环境变量降级的模型 provider 对协调 agent 不可用", "orb", orb.ID, "env", names)
	}
	paths := make([]string, 0, len(rendered.Files))
	for path := range rendered.Files {
		if coordinatorFiles[path] {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		mode := os.FileMode(0o644)
		if m, ok := modes[path]; ok {
			mode = m.Perm()
		}
		plan.Params.OmpHome = append(plan.Params.OmpHome, apigen.LinkCtlOmpHomeFile{
			Path: path, Mode: int(mode), ContentB64: base64.StdEncoding.EncodeToString(rendered.Files[path]),
		})
	}
	for k, v := range rendered.Routes {
		plan.Egress.Routes[k] = v
	}
	return plan, nil
}

// planStampRetries 是 Plan 读密钥值前后修改时间不一致时的重读次数上限。
const planStampRetries = 3

// Plan 实现 SetupSource。workdir 留空，由 orbd 按有无仓库取 $HOME/workspace[/repo]。
func (d *DefaultSetup) Plan(ctx context.Context, orb db.Orb) (SetupPlan, error) {
	if orb.Coordinator {
		return d.planCoordinator(orb)
	}
	var prj *db.Project
	if orb.ProjectID != nil {
		v, err := d.Store.Q.GetProject(ctx, *orb.ProjectID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return SetupPlan{}, fmt.Errorf("读取项目: %w", err)
		default:
			prj = &v
		}
	}
	// 密钥值与配置哈希里的修改时间必须出自同一份快照：读值前后各读一次修改时间，不一致（期间有人改了密钥）就重来。
	for range planStampRetries {
		before, err := d.Store.Q.ListConfigSecretStamps(ctx)
		if err != nil {
			return SetupPlan{}, fmt.Errorf("读取密钥修改时间: %w", err)
		}
		plan, err := d.plan(ctx, prj, orb)
		if err != nil {
			return plan, err
		}
		after, err := d.Store.Q.ListConfigSecretStamps(ctx)
		if err != nil {
			return SetupPlan{}, fmt.Errorf("读取密钥修改时间: %w", err)
		}
		if !sameStamps(before, after) {
			continue
		}
		if prj != nil {
			plan.ConfigHash = projectConfigHash(*prj, orb.BaseImageID, orb.Type, before)
		}
		return plan, nil
	}
	return SetupPlan{}, errors.New("生成 setup 参数期间密钥反复变化")
}

// plan 由给定的项目行（可为 nil）、orb 与当前密钥生成参数。
func (d *DefaultSetup) plan(ctx context.Context, prj *db.Project, orb db.Orb) (SetupPlan, error) {
	plan := SetupPlan{
		Params: apigen.LinkCtlSetupRunParams{
			GitHosts: []string{}, OmpHome: []apigen.LinkCtlOmpHomeFile{}, Env: map[string]string{},
		},
		Egress: egress.Policy{Routes: map[string]egress.Route{}, GitRepos: map[string][]string{}},
	}
	p := &plan.Params
	var projectIdentity *apigen.GitIdentity
	var projectRepo *gitRepo
	if prj != nil {
		if prj.RepoUrl != nil && *prj.RepoUrl != "" {
			repo := &apigen.LinkCtlSetupRepo{URL: *prj.RepoUrl, Shallow: prj.ShallowClone}
			if prj.BaseBranch != nil && *prj.BaseBranch != "" {
				repo.Branch = prj.BaseBranch
			}
			p.Repo = repo
			if prev := prevURLs(prj.GithubPrevUrls); len(prev) > 0 {
				repo.PreviousUrls = prev
			}
			if host, path, ok := HTTPSRepo(*prj.RepoUrl); ok {
				projectRepo = &gitRepo{host: host, path: path}
			}
		}
		p.Scripts.Setup = nonEmpty(prj.SetupScript)
		p.Scripts.Resume = nonEmpty(prj.ResumeScript)
		projectIdentity = gitIdentity(prj.GitAuthorName, prj.GitAuthorEmail)
		p.GithubGates = githubGates(prj.GithubGates)
	}
	// git 提交身份：项目覆盖（两项齐全）> 所有者默认 > 不下发（orb 现有配置不动）。
	p.GitIdentity = projectIdentity
	if p.GitIdentity == nil {
		row, err := d.Store.Q.GetOwnerGitIdentity(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return plan, fmt.Errorf("读取 git 身份: %w", err)
		}
		p.GitIdentity = gitIdentity(row.GitName, row.GitEmail)
	}
	var resolver agentcfg.Resolver = emptyResolver{}
	var hosts []string
	if d.Vault != nil {
		resolver = d.Vault
		env, err := d.Vault.EnvSecrets()
		if err != nil {
			return plan, fmt.Errorf("读取 env 密钥: %w", err)
		}
		for k, v := range env {
			p.Env[k] = v
		}
		hosts, err = d.Vault.GitHosts()
		if err != nil {
			return plan, fmt.Errorf("读取 git 主机: %w", err)
		}
	}
	// GitHub（M15）：工作 orb 只要身份槽位可用就下发 gh 登录名（含 uploads.github.com）、/gh/ 路由与整个 github.com 主机的
	// insteadOf，有没有项目、项目在哪个主机都一样；git 与 gh 都经出站代理注入 OAuth 令牌，不需要 PAT。runner 与协调宿主
	// 不下发。agent 身份的提交作者固定为 Agent 账号，user 身份只在项目与所有者都没设时用 GitHub 账号推出的身份。
	if d.GitHub != nil && !IsRunner(orb) {
		if gs, ok := d.GitHub.OrbSetup(ctx, orb); ok {
			uploads := true
			p.Gh = &apigen.LinkCtlSetupGh{Login: gs.Login, Uploads: &uploads}
			plan.Egress.GitHub = &egress.GitHubAccess{APIOrigin: gs.APIOrigin, GitOrigin: gs.GitOrigin, Gates: gateConfig(p.GithubGates)}
			if err := d.threadGates(ctx, orb, plan.Egress.GitHub); err != nil {
				return plan, err
			}
			if gs.Agent || p.GitIdentity == nil {
				p.GitIdentity = &gs.GitIdentity
			}
		}
	}
	p.GitHosts = gitInsteadOf(projectRepo, hosts)
	if p.Gh != nil {
		p.GitHosts = withGitHubHost(p.GitHosts)
	}
	// git 出站策略（契约 §5）：有 https 项目仓库的 orb 只放行该仓库（读写）；否则任意仓库只读（GitRepos 为空）。
	// 下发了 GitHub 凭据时 github.com 不受此限（egress 按 Policy.GitHub 放行），其它 PAT 主机照旧。
	if projectRepo != nil {
		plan.Egress.GitRepos[projectRepo.host] = []string{egress.NormalizeRepo(projectRepo.path)}
	}
	if d.Home != nil {
		files, err := d.Home.LoadAll()
		if err != nil {
			return plan, fmt.Errorf("读取 omp-home: %w", err)
		}
		modes, err := d.Home.Modes()
		if err != nil {
			return plan, fmt.Errorf("读取 omp-home 权限: %w", err)
		}
		rendered, err := agentcfg.Render(files, resolver)
		if err != nil {
			return plan, fmt.Errorf("变换 omp-home: %w", err)
		}
		p.OmpHome = ompFiles(rendered.Files, modes)
		// 环境变量降级的模型密钥优先于同名 env 密钥。
		for k, v := range rendered.Env {
			p.Env[k] = v
		}
		for k, v := range rendered.Routes {
			plan.Egress.Routes[k] = v
		}
	}
	// 项目级 omp 配置（M12.2）：属于项目即下发（没有文件也给空清单，orbd 据此清空镜像）；原样下发，不经
	// agentcfg.Render（项目层没有 models.yml，也不解析 ${oryn:secret:…}）。
	if prj != nil && d.Projects != nil {
		files, modes, err := d.Projects.Load(prj.ID)
		if err != nil {
			return plan, fmt.Errorf("读取项目 omp 配置: %w", err)
		}
		p.ProjectConfig = &apigen.LinkCtlProjectConfig{Files: ompFiles(files, modes)}
	}
	if err := checkDeliverable(p); err != nil {
		return plan, err
	}
	return plan, nil
}

// ctlEnvelope 是 ctl 请求在 params 之外（id、method、换行）的开销上界。
const ctlEnvelope = 4 << 10

// checkDeliverable 在参数编码后放不进单行 ctl 消息（link.MaxCtlLineBytes）时返回说明两层大小的错误，代替 ctl 写入时
// 才报的「line too long」。写入接口已按 agentcfg.DeliveryBudget 限住两层合计，这里拦的是此前存下的超限配置；
// 两层按 EntrySize 口径粗算不超预算即放行，超了才真正编码一次参数量准确大小。
func checkDeliverable(p *apigen.LinkCtlSetupRunParams) error {
	layer := func(files []apigen.LinkCtlOmpHomeFile) int64 {
		var n int64
		for _, f := range files {
			n += int64(len(f.ContentB64)) + agentcfg.EntrySize(f.Path, 0)
		}
		return n
	}
	global := layer(p.OmpHome)
	var project int64
	if p.ProjectConfig != nil {
		project = layer(p.ProjectConfig.Files)
	}
	if global+project <= agentcfg.DeliveryBudget {
		return nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("编码 setup 参数: %w", err)
	}
	if len(b)+ctlEnvelope <= link.MaxCtlLineBytes {
		return nil
	}
	const mib = 1 << 20
	return fmt.Errorf("agent 配置编码后 %.1f MiB（全局 %.1f MiB、项目 %.1f MiB），超过单次下发上限 %d MiB：请在设置或项目的 agent 配置里删减文件",
		float64(len(b))/mib, float64(global)/mib, float64(project)/mib, link.MaxCtlLineBytes/mib)
}

// ompFiles 把配置文件按路径排序生成下发清单（权限缺省 0644，内容 base64）；没有文件时返回空切片而不是 nil。
func ompFiles(files map[string][]byte, modes map[string]os.FileMode) []apigen.LinkCtlOmpHomeFile {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	out := make([]apigen.LinkCtlOmpHomeFile, 0, len(paths))
	for _, path := range paths {
		mode := os.FileMode(0o644)
		if m, ok := modes[path]; ok {
			mode = m.Perm()
		}
		out = append(out, apigen.LinkCtlOmpHomeFile{
			Path: path, Mode: int(mode), ContentB64: base64.StdEncoding.EncodeToString(files[path]),
		})
	}
	return out
}

// gitRepo 是项目的 https 仓库：主机（小写）与仓库路径（保留原大小写，去掉 .git 后缀）。
type gitRepo struct{ host, path string }

// gitInsteadOf 生成 setup 的 GitHosts 条目：有项目仓库时只覆盖该仓库地址（`host/owner/repo`，且仅当该主机
// 配置了令牌），同主机其它地址照常直连公网；没有项目仓库时覆盖每个有令牌的主机（egress 只读放行）。
func gitInsteadOf(repo *gitRepo, tokenHosts []string) []string {
	out := []string{}
	if repo != nil {
		for _, h := range tokenHosts {
			if strings.EqualFold(h, repo.host) {
				out = append(out, repo.host+"/"+repo.path)
				break
			}
		}
		return out
	}
	out = append(out, tokenHosts...)
	sort.Strings(out)
	return out
}

// withGitHubHost 把 GitHosts 中 github.com 的条目（整主机或只覆盖项目仓库）换成一条整主机条目（M15：OAuth 令牌不按仓库
// 收窄，orb 内任意 github.com 仓库都经出站代理），结果按字典序排列。
func withGitHubHost(entries []string) []string {
	out := slices.DeleteFunc(entries, func(e string) bool {
		host, _, _ := strings.Cut(e, "/")
		return strings.EqualFold(host, "github.com")
	})
	out = append(out, "github.com")
	sort.Strings(out)
	return out
}

// HTTPSRepo 解析 https://host/owner/repo(.git) 形式的仓库地址（GitLab 子组等多段路径整体作为仓库路径）：
// 返回小写主机与保留原大小写、去掉 .git 的仓库路径。
// 路径段含 `.`、`..`、空段或 git 配置键不能容纳的字符时不视为可放行的仓库。
func HTTPSRepo(raw string) (host, path string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", "", false
	}
	p := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	segs := strings.Split(p, "/")
	if len(segs) < 2 {
		return "", "", false
	}
	for _, s := range segs {
		if !repoSegRe.MatchString(s) || s == "." || s == ".." {
			return "", "", false
		}
	}
	return strings.ToLower(u.Hostname()), p, true
}

var repoSegRe = regexp.MustCompile(`^[A-Za-z0-9_~][A-Za-z0-9._~-]*$`)

type emptyResolver struct{}

func (emptyResolver) Lookup(string) (string, string, bool)       { return "", "", false }
func (emptyResolver) SecretByID(string) (string, []string, bool) { return "", nil, false }

// gitIdentity 在名字与邮箱都非空时返回身份，否则 nil（半套覆盖不生效）。
func gitIdentity(name, email *string) *apigen.GitIdentity {
	if nonEmpty(name) == nil || nonEmpty(email) == nil {
		return nil
	}
	return &apigen.GitIdentity{Name: *name, Email: *email}
}

func nonEmpty(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}

// prevURLs 解析项目的 github_prev_urls（JSON 字符串数组，M14）；解析失败或为空时返回 nil。
func prevURLs(raw string) []string {
	var out []string
	if json.Unmarshal([]byte(raw), &out) != nil || len(out) == 0 {
		return nil
	}
	return out
}

// githubGates 解析项目的 github_gates（JSON，M15），供 orbd 的 github_prepare 读取修复 / 检查 / 测试命令；
// NULL、空串或解析失败时返回 nil（视为未配置）。
func githubGates(raw *string) *apigen.GitHubGates {
	if raw == nil || *raw == "" {
		return nil
	}
	var g apigen.GitHubGates
	if json.Unmarshal([]byte(*raw), &g) != nil {
		return nil
	}
	return &g
}

// gateConfig 把项目的 github_gates 转成出站代理的门禁配置；推送、PR 门禁都关闭时为 nil（等于没有门禁）。
func gateConfig(g *apigen.GitHubGates) *egress.GateConfig {
	if g == nil || (!g.Push && !g.Pr) {
		return nil
	}
	return &egress.GateConfig{
		Push: g.Push, PR: g.Pr, PRHeadings: g.PrHeadings,
		RequireClosingRef: g.RequireClosingRef != nil && *g.RequireClosingRef,
	}
}

// threadGates 按 orb 线程的任务填写门禁的线程限制（M15 步骤 5.4）：thread_issues.review_only，以及建立任务的触发规则的
// impl_auth / comment_only_reviews。orb 没有唯一的未归档工作线程、线程没有任务时不加限制。实现授权、是否发布任务
// 会在线程期间变化，不在这里固定，由出站代理每次写请求经 GateLookup 现查。
func (d *DefaultSetup) threadGates(ctx context.Context, orb db.Orb, ga *egress.GitHubAccess) error {
	ths, err := d.Store.Q.ListThreadsByOrb(ctx, &orb.ID)
	if err != nil {
		return fmt.Errorf("读取 orb 的线程: %w", err)
	}
	var tid string
	for _, th := range ths {
		if th.Kind != threadKindWork || th.ArchivedAt != nil {
			continue
		}
		if tid != "" {
			return nil
		}
		tid = th.ID
	}
	if tid == "" {
		return nil
	}
	ti, err := d.Store.Q.GetThreadIssueByThread(ctx, tid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取线程任务: %w", err)
	}
	ga.ReviewOnly = ti.ReviewOnly
	rule := ti.RuleID
	if rule == nil {
		rule = ti.TriggerID
	}
	if rule == nil {
		return nil
	}
	trig, err := d.Store.Q.GetGithubTrigger(ctx, *rule)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取触发规则: %w", err)
	}
	ga.ImplAuth, ga.CommentOnlyReviews = trig.ImplAuth, trig.CommentOnlyReviews
	return nil
}
