package orbs

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// SetupPlan 是一次 setup.run / resume.run 的参数与随之生效的 egress 路由。
type SetupPlan struct {
	Params apigen.LinkCtlSetupRunParams
	// Egress 是随之生效的出站策略（模型路由来自 agentcfg.Rendered.Routes，git 仓库限定来自项目 repo_url）。
	Egress egress.Policy
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

// DefaultSetup 是默认的 SetupSource：项目（仓库、分支、浅克隆、脚本）+ omp-home 下发变换 + 密钥。
type DefaultSetup struct {
	Store *store.Store
	Home  Home  // 可为 nil（不下发 omp 配置）
	Vault Vault // 可为 nil（无密钥）
}

// Plan 实现 SetupSource。workdir 留空，由 orbd 按有无仓库取 $HOME/workspace[/repo]。
func (d *DefaultSetup) Plan(ctx context.Context, orb db.Orb) (SetupPlan, error) {
	plan := SetupPlan{
		Params: apigen.LinkCtlSetupRunParams{
			GitHosts: []string{}, OmpHome: []apigen.LinkCtlOmpHomeFile{}, Env: map[string]string{},
		},
		Egress: egress.Policy{Routes: map[string]egress.Route{}, GitRepos: map[string][]string{}},
	}
	p := &plan.Params
	var projectIdentity *apigen.GitIdentity
	var projectRepo *gitRepo
	if orb.ProjectID != nil {
		prj, err := d.Store.Q.GetProject(ctx, *orb.ProjectID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return plan, fmt.Errorf("读取项目: %w", err)
		default:
			if prj.RepoUrl != nil && *prj.RepoUrl != "" {
				repo := &apigen.LinkCtlSetupRepo{URL: *prj.RepoUrl, Shallow: prj.ShallowClone}
				if prj.BaseBranch != nil && *prj.BaseBranch != "" {
					repo.Branch = prj.BaseBranch
				}
				p.Repo = repo
				if host, path, ok := httpsRepo(*prj.RepoUrl); ok {
					projectRepo = &gitRepo{host: host, path: path}
				}
			}
			p.Scripts.Setup = nonEmpty(prj.SetupScript)
			p.Scripts.Resume = nonEmpty(prj.ResumeScript)
			projectIdentity = gitIdentity(prj.GitAuthorName, prj.GitAuthorEmail)
		}
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
	if d.Vault != nil {
		resolver = d.Vault
		env, err := d.Vault.EnvSecrets()
		if err != nil {
			return plan, fmt.Errorf("读取 env 密钥: %w", err)
		}
		for k, v := range env {
			p.Env[k] = v
		}
		hosts, err := d.Vault.GitHosts()
		if err != nil {
			return plan, fmt.Errorf("读取 git 主机: %w", err)
		}
		p.GitHosts = gitInsteadOf(projectRepo, hosts)
	}
	// git 出站策略（契约 §5）：有 https 项目仓库的 orb 只放行该仓库（读写）；否则任意仓库只读（GitRepos 为空）。
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
		paths := make([]string, 0, len(rendered.Files))
		for path := range rendered.Files {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			mode := os.FileMode(0o644)
			if m, ok := modes[path]; ok {
				mode = m.Perm()
			}
			p.OmpHome = append(p.OmpHome, apigen.LinkCtlOmpHomeFile{
				Path: path, Mode: int(mode), ContentB64: base64.StdEncoding.EncodeToString(rendered.Files[path]),
			})
		}
		// 环境变量降级的模型密钥优先于同名 env 密钥。
		for k, v := range rendered.Env {
			p.Env[k] = v
		}
		for k, v := range rendered.Routes {
			plan.Egress.Routes[k] = v
		}
	}
	return plan, nil
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

// httpsRepo 解析 https://host/owner/repo(.git) 形式的仓库地址（GitLab 子组等多段路径整体作为仓库路径）。
// 路径段含 `.`、`..`、空段或 git 配置键不能容纳的字符时不视为可放行的仓库。
func httpsRepo(raw string) (host, path string, ok bool) {
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
