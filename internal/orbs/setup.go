package orbs

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
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
	// Withheld 是 runner 下发前因含形似密钥的内容被扣下、没有下发的全局 omp-home 文件（M21，见 planRunner）；orb 恒为空。
	Withheld []agentcfg.Withheld
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
	// CoordinatorWebAllow 是全局助手联网时放行的非公网目标（M19，coordinator.web_allow_hosts，已转小写）。
	CoordinatorWebAllow []string
	// CoordinatorMCPToken 是协调宿主 /mcp/ 路由的凭据（M19 D10）：只经 agent.start 交给全局助手的 agent，出站代理逐个
	// 请求核对；空 = 协调宿主不放行 MCP。
	CoordinatorMCPToken string
	// Projects 是项目级 omp 配置；可为 nil（不下发项目级配置）。
	Projects ProjectConfigs
	// GitHub 是 GitHub 集成（M14）；可为 nil（不下发 gh 配置，github.com 只走 PAT）。
	GitHub GitHub
	// SSH 是 SSH 主机目录（M18）；可为 nil（不下发 orb 内的 SSH 配置）。
	SSH SSH
	// KnownSecrets 是 runner 无密钥投递校验用的已知密钥（M21）；nil = 只查形似密钥的串。
	KnownSecrets KnownSecrets
}

// SSH 给出一台 orb 的 SSH 配置（M18）；协调 orb 返回 nil（不动 orb 内的 SSH 配置），runner 只有全局主机（M21）。
type SSH interface {
	OrbConfig(ctx context.Context, orb db.Orb) (*apigen.LinkCtlSSHConfig, error)
}

// coordinatorFiles 是下发给协调宿主的 omp-home 文件：只有模型与基本设置，其余（extensions/、skills/、agents/、
// commands/、AGENTS.md…）一律不下发。mcp.json 不原样下发：M19 起由 planCoordinator 按全局配置生成只含 orynd 代理
// 地址的版本（鉴权头由出站代理注入，不进沙箱）。
var coordinatorFiles = map[string]bool{agentcfg.ModelsFile: true, "config.yml": true}

// planCoordinator 是协调 orb 的 setup 参数：不取项目、不下发 git 身份 / GitHosts / env 模式密钥，omp-home 只有
// models.yml 与 config.yml（模型地址改写到协调宿主的代理端口），以及助手可用的 HTTP MCP（M19，有才下发）；
// 环境变量降级的模型密钥丢弃，git 出站一律拒绝；助手联网开着时放行公网正向代理（M19）。
func (d *DefaultSetup) planCoordinator(ctx context.Context, orb db.Orb) (SetupPlan, error) {
	plan := SetupPlan{
		Params: apigen.LinkCtlSetupRunParams{
			GitHosts: []string{}, OmpHome: []apigen.LinkCtlOmpHomeFile{}, Env: map[string]string{},
		},
		Egress: egress.Policy{Routes: map[string]egress.Route{}, GitRepos: map[string][]string{}, NoGit: true, MCPToken: d.CoordinatorMCPToken},
	}
	web, disabled, err := d.assistantSettings(ctx)
	if err != nil {
		return plan, err
	}
	if web {
		plan.Egress.Web = &egress.WebPolicy{AllowHosts: d.CoordinatorWebAllow}
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
	if err := d.coordinatorMCP(&plan, files[agentcfg.MCPFile], disabled); err != nil {
		return plan, err
	}
	return plan, nil
}

// assistantSettings 读全局助手的联网开关与关掉的 MCP 服务器（M19）；没有设置行时按默认（联网开、无禁用项）。
func (d *DefaultSetup) assistantSettings(ctx context.Context) (web bool, disabled map[string]bool, err error) {
	disabled = map[string]bool{}
	if d.Store == nil {
		return true, disabled, nil
	}
	row, err := d.Store.Q.GetAssistantSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return true, disabled, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("读取全局助手设置: %w", err)
	}
	var names []string
	if err := json.Unmarshal([]byte(row.DisabledMcp), &names); err != nil {
		slog.Warn("orbs: 全局助手设置里的 disabled_mcp 无法解析，按空处理", "err", err)
	}
	for _, n := range names {
		disabled[n] = true
	}
	return row.WebAccess == 1, disabled, nil
}

// coordinatorMCP 把全局 mcp.json 里助手可用、未被关掉的 HTTP / SSE 服务器写进出站路由（/mcp/m<i>，带鉴权头），
// 并生成协调宿主的 mcp.json（只有类型与 orynd 代理地址，不含任何头）。没有可用服务器时不下发该文件；解析失败只记日志。
func (d *DefaultSetup) coordinatorMCP(plan *SetupPlan, content []byte, disabled map[string]bool) error {
	var env map[string]string
	if d.Vault != nil {
		var err error
		if env, err = d.Vault.EnvSecrets(); err != nil {
			return fmt.Errorf("读取 env 密钥: %w", err)
		}
	}
	servers, err := agentcfg.ParseMCP(content, env)
	if err != nil {
		slog.Warn("orbs: 全局 mcp.json 无法解析，全局助手不使用 MCP", "err", err)
		return nil
	}
	out := map[string]map[string]any{}
	for _, srv := range servers {
		if !srv.Usable || disabled[srv.Name] {
			continue
		}
		key := fmt.Sprintf("m%d", len(out))
		if plan.Egress.MCP == nil {
			plan.Egress.MCP = map[string]egress.MCPRoute{}
		}
		plan.Egress.MCP[key] = egress.MCPRoute{URL: srv.URL, Headers: srv.Headers}
		// 地址前缀与凭据都由环境变量给出（omp 展开 url / headers 里的 ${VAR}）：协调宿主只给全局助手的 agent 设置它们；
		// Space 协调者既不会自动连上，直接请求 /mcp/ 也因缺凭据被出站代理拒绝（D10）。
		out[srv.Name] = map[string]any{
			"type":    srv.Transport,
			"url":     "${" + agentcfg.MCPBaseEnv + "}/mcp/" + key,
			"headers": map[string]string{egress.MCPTokenHeader: "${" + agentcfg.MCPTokenEnv + "}"},
		}
	}
	if len(out) == 0 {
		return nil
	}
	b, err := json.MarshalIndent(map[string]any{"mcpServers": out}, "", "  ")
	if err != nil {
		return err
	}
	plan.Params.OmpHome = append(plan.Params.OmpHome, apigen.LinkCtlOmpHomeFile{
		Path: agentcfg.MCPFile, Mode: 0o600, ContentB64: base64.StdEncoding.EncodeToString(b),
	})
	return nil
}

// planStampRetries 是 Plan 读密钥值前后修改时间不一致时的重读次数上限。
const planStampRetries = 3

// Plan 实现 SetupSource。workdir 留空，由 orbd 按有无仓库取 $HOME/workspace[/repo]。
func (d *DefaultSetup) Plan(ctx context.Context, orb db.Orb) (SetupPlan, error) {
	if orb.Coordinator {
		return d.planCoordinator(ctx, orb)
	}
	if IsRunner(orb) {
		return d.planRunner(ctx, orb)
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
	// projectGates 只用于本次生成的出站策略；不随 setup 下发（github_prepare 在执行时向控制面现查）。
	var projectGates *apigen.GitHubGates
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
		projectGates = ParseGitHubGates(prj.GithubGates)
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
	// insteadOf，有没有项目、项目在哪个主机都一样；git 与 gh 都经出站代理注入 OAuth 令牌，不需要 PAT。runner（planRunner）
	// 与协调宿主不下发。agent 身份的提交作者固定为 Agent 账号，user 身份只在项目与所有者都没设时用 GitHub 账号推出的身份。
	if d.GitHub != nil {
		if gs, ok := d.GitHub.OrbSetup(ctx, orb); ok {
			uploads := true
			p.Gh = &apigen.LinkCtlSetupGh{Login: gs.Login, Uploads: &uploads}
			plan.Egress.GitHub = &egress.GitHubAccess{APIOrigin: gs.APIOrigin, GitOrigin: gs.GitOrigin, Gates: gateConfig(projectGates)}
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
		home, rendered, _, err := d.renderHome(resolver, agentcfg.RenderOptions{}, nil)
		if err != nil {
			return plan, err
		}
		p.OmpHome = home
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
		cfg, err := ProjectConfigOf(d.Projects, prj.ID)
		if err != nil {
			return plan, err
		}
		p.ProjectConfig = cfg
	}
	if d.SSH != nil {
		cfg, err := d.SSH.OrbConfig(ctx, orb)
		if err != nil {
			return plan, fmt.Errorf("读取 SSH 主机: %w", err)
		}
		p.SSH = cfg
	}
	if err := checkDeliverable(p); err != nil {
		return plan, err
	}
	return plan, nil
}

// ProjectConfigOf 读项目层并生成下发清单（按路径排序，没有文件时是非 nil 的空清单）。orb 随 setup.run / omphome.apply
// 下发；runner 线程用 RunnerProjectConfigOf。
func ProjectConfigOf(projects ProjectConfigs, projectID string) (*apigen.LinkCtlProjectConfig, error) {
	files, modes, err := projects.Load(projectID)
	if err != nil {
		return nil, fmt.Errorf("读取项目 omp 配置: %w", err)
	}
	return &apigen.LinkCtlProjectConfig{Files: ompFiles(files, modes)}, nil
}

// RunnerProjectConfigOf 是 runner 线程随 agent.start 交付的项目层（M21）：项目层文件并入 extra（如线程项目主机合成的
// ssh.json，0600，同名覆盖），再过一遍 screen（无密钥投递校验，见 RunnerScreen），返回下发清单与扣下的文件。一个文件
// 都不剩时清单为 nil（不带项目层）。
func RunnerProjectConfigOf(projects ProjectConfigs, projectID string, extra map[string][]byte, screen func(map[string][]byte) []agentcfg.Withheld) (*apigen.LinkCtlProjectConfig, []agentcfg.Withheld, error) {
	stored, storedModes, err := projects.Load(projectID)
	if err != nil {
		return nil, nil, fmt.Errorf("读取项目 omp 配置: %w", err)
	}
	files, modes := maps.Clone(stored), maps.Clone(storedModes)
	if files == nil {
		files = map[string][]byte{}
	}
	if modes == nil {
		modes = map[string]os.FileMode{}
	}
	for p, b := range extra {
		files[p], modes[p] = b, 0o600
	}
	withheld := screen(files)
	if len(files) == 0 {
		return nil, withheld, nil
	}
	return &apigen.LinkCtlProjectConfig{Files: ompFiles(files, modes)}, withheld, nil
}

// renderHome 读全局 omp-home 并按 opts 变换（models.yml 的密钥换成占位符、baseUrl 改写到出站代理），返回下发清单与
// 变换结果（路由、降级的环境变量）。screen 非 nil 时（runner）对变换后的文件做无密钥投递校验，扣下的文件不进清单。
func (d *DefaultSetup) renderHome(resolver agentcfg.Resolver, opts agentcfg.RenderOptions, screen func(map[string][]byte) []agentcfg.Withheld) ([]apigen.LinkCtlOmpHomeFile, agentcfg.Rendered, []agentcfg.Withheld, error) {
	files, err := d.Home.LoadAll()
	if err != nil {
		return nil, agentcfg.Rendered{}, nil, fmt.Errorf("读取 omp-home: %w", err)
	}
	delete(files, "ssh.json") // M18：orb 内的 ssh.json 由 SSH 网关生成；旧部署可能在中央配置里存过原始文件
	modes, err := d.Home.Modes()
	if err != nil {
		return nil, agentcfg.Rendered{}, nil, fmt.Errorf("读取 omp-home 权限: %w", err)
	}
	rendered, err := agentcfg.RenderWith(files, resolver, opts)
	if err != nil {
		return nil, agentcfg.Rendered{}, nil, fmt.Errorf("变换 omp-home: %w", err)
	}
	var withheld []agentcfg.Withheld
	if screen != nil {
		withheld = screen(rendered.Files)
	}
	return ompFiles(rendered.Files, modes), rendered, withheld, nil
}

// KnownSecrets 返回已知密钥的明文（名 → 值），供 runner 的无密钥投递校验（agentcfg.ScreenSecrets）使用；与分享前的
// 密钥扫描同一来源（密钥库全部值 + SSH 密码登录主机的密码）。
type KnownSecrets func(ctx context.Context) (map[string]string, error)

// RunnerScreen 返回 runner 下发前的无密钥投递校验（M21）：known 为 nil 时只查形似密钥的串。读已知密钥失败时返回错误，
// 调用方不带着未经校验的文件下发。
func RunnerScreen(ctx context.Context, known KnownSecrets) (func(map[string][]byte) []agentcfg.Withheld, error) {
	var vals map[string]string
	if known != nil {
		v, err := known(ctx)
		if err != nil {
			return nil, fmt.Errorf("读取已知密钥: %w", err)
		}
		vals = v
	}
	return func(files map[string][]byte) []agentcfg.Withheld { return agentcfg.ScreenSecrets(files, vals) }, nil
}

// planRunner 是 runner 的下发参数（M21，只经 omphome.apply 使用）：全局 omp-home（模型改写到 runner 本机常驻的出站
// 通道 agentcfg.EgressAddr，关闭环境变量降级）与 SSH 主机；不带 GitHub、git 主机、git 身份、env 密钥与脚本。出站策略
// 只有模型路由，git 一律拒绝（runner 的出站通道另由 runnerEgressHandler 只放 /llm/ 与 /memory/）。runner 是用户自己的
// 机器，下发的文件里不得有密钥明文：变换后的文件再过一遍无密钥投递校验（RunnerScreen），命中的扣下不发，记在
// SetupPlan.Withheld（applyOrb 写进 orb 事件备注）。
//
// runner 的常驻 orb 不属于任何项目（project_id 恒为空），同一台 runner 上可以同时跑多个项目的线程，而一台 runner 只有
// 一份 omp-home：项目层不在这里下发，而是按线程随 agent.start 交付（threads 的 runnerProjectConfig，含该线程项目的
// SSH 主机）；这里的 SSH 只有全局主机。
func (d *DefaultSetup) planRunner(ctx context.Context, orb db.Orb) (SetupPlan, error) {
	plan := SetupPlan{
		Params: apigen.LinkCtlSetupRunParams{
			GitHosts: []string{}, OmpHome: []apigen.LinkCtlOmpHomeFile{}, Env: map[string]string{},
		},
		Egress: egress.Policy{Routes: map[string]egress.Route{}, GitRepos: map[string][]string{}, NoGit: true},
	}
	p := &plan.Params
	if d.Home != nil {
		var resolver agentcfg.Resolver = emptyResolver{}
		if d.Vault != nil {
			resolver = d.Vault
		}
		screen, err := RunnerScreen(ctx, d.KnownSecrets)
		if err != nil {
			return plan, err
		}
		home, rendered, withheld, err := d.renderHome(resolver, agentcfg.RenderOptions{NoEnvFallback: true}, screen)
		if err != nil {
			return plan, err
		}
		p.OmpHome, plan.Withheld = home, withheld
		for k, v := range rendered.Routes {
			plan.Egress.Routes[k] = v
		}
	}
	if d.SSH != nil {
		cfg, err := d.SSH.OrbConfig(ctx, orb)
		if err != nil {
			return plan, fmt.Errorf("读取 SSH 主机: %w", err)
		}
		p.SSH = cfg
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

// HostedCloneURL 是 Oryn 托管仓库（M24 阶段 ③）的克隆地址：<public_url>/git/<projectID>.git。
// 放在这里而不是 internal/repos：repos 引用 orbs（HTTPSRepo），反过来会成环。
func HostedCloneURL(publicURL, projectID string) string {
	return strings.TrimSuffix(publicURL, "/") + "/git/" + projectID + ".git"
}

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
