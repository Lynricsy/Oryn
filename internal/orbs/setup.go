package orbs

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// SetupPlan 是一次 setup.run / resume.run 的参数与随之生效的 egress 路由。
type SetupPlan struct {
	Params apigen.LinkCtlSetupRunParams
	// Routes 是 egress 模型路由键 → 上游前缀（agentcfg.Rendered.Routes）。
	Routes map[string]string
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
		Routes: map[string]string{},
	}
	p := &plan.Params
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
			}
			p.Scripts.Setup = nonEmpty(prj.SetupScript)
			p.Scripts.Resume = nonEmpty(prj.ResumeScript)
		}
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
		p.GitHosts = append(p.GitHosts, hosts...)
		sort.Strings(p.GitHosts)
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
			plan.Routes[k] = v
		}
	}
	return plan, nil
}

type emptyResolver struct{}

func (emptyResolver) Lookup(string) (string, string, bool) { return "", "", false }

func nonEmpty(s *string) *string {
	if s == nil || *s == "" {
		return nil
	}
	return s
}
