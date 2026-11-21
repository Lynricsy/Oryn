package api

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/repos"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 托管仓库（M24 阶段 ③）：projects.hosted 是唯一权威，repo_url 恒为 NULL；正本在 <data_dir>/repos/<id>.git。
// 建仓与改基线分支只动 git 与文件系统，一律在数据库事务之外执行（库只有一条连接，事务回调里再读库会卡死）。

var errRepoUnavailable = &statusError{http.StatusServiceUnavailable, "repo_unavailable", "控制面没有可用的 git"}

// hostedRepos 返回可用的仓库服务；没有装配或控制面没有可用的 git → 503 repo_unavailable。
func (s *Server) hostedRepos() (*repos.Service, error) {
	if s.Repos == nil || s.Repos.Service == nil {
		return nil, errRepoUnavailable
	}
	if ok, _ := s.Repos.Service.Available(); !ok {
		return nil, errRepoUnavailable
	}
	return s.Repos.Service, nil
}

// createRepoSource 校验新建项目的仓库来源，返回要写入的基线分支与 hosted 标记：hosted 与 repoUrl 互斥，
// emptyRepo 只能与 hosted 同用，托管项目的基线分支缺省 main。
func (s *Server) createRepoSource(req apigen.ProjectCreate) (base *string, hosted bool, err error) {
	base = optStr(req.BaseBranch)
	if req.Hosted == nil || !*req.Hosted {
		if req.EmptyRepo != nil && *req.EmptyRepo {
			return nil, false, errInvalid("emptyRepo 只能与 hosted 同用")
		}
		return base, false, nil
	}
	if optStr(req.RepoURL) != nil {
		return nil, false, errInvalid("hosted 与 repoUrl 只能二选一")
	}
	b := cmp.Or(deref(base), "main")
	if !repos.ValidRef(b) {
		return nil, false, errInvalid("baseBranch 不是合法的分支名")
	}
	if _, err := s.hostedRepos(); err != nil {
		return nil, false, err
	}
	return &b, true, nil
}

// checkHostedPatch 校验托管项目的修改：不能改用外部仓库地址（空串照常放行，网页每次都发 repoUrl:""），
// 基线分支不能清空且须是合法的分支名。
func checkHostedPatch(p db.Project, req apigen.ProjectPatch) error {
	if !p.Hosted {
		return nil
	}
	if req.RepoURL != nil && strings.TrimSpace(*req.RepoURL) != "" {
		return &statusError{http.StatusConflict, "repo_hosted", "托管仓库的项目不能改用外部仓库地址"}
	}
	if req.BaseBranch != nil {
		b := strings.TrimSpace(*req.BaseBranch)
		if b == "" {
			return errInvalid("托管仓库的基线分支不能清空")
		}
		if !repos.ValidRef(b) {
			return errInvalid("baseBranch 不是合法的分支名")
		}
	}
	return nil
}

// initHostedRepo 建（或认领）项目的托管仓库并写 project_repos 行。写行失败时删掉本次新建的目录，认领来的目录不动。
func (s *Server) initHostedRepo(ctx context.Context, id, base string, initialCommit bool) (adopted bool, err error) {
	svc, err := s.hostedRepos()
	if err != nil {
		return false, err
	}
	prj, err := s.Store.Q.GetProject(ctx, id)
	if err != nil {
		return false, err
	}
	if adopted, err = svc.InitHosted(ctx, id, base, initialCommit, s.hostedIdentity(ctx, prj)); err != nil {
		return false, err
	}
	if err := svc.RecordHosted(ctx, id, base); err != nil {
		if !adopted {
			svc.Remove(id)
		}
		return adopted, err
	}
	return adopted, nil
}

// moveHostedHead 在托管项目的基线分支变化（事务提交）之后把 HEAD 指过去并更新 project_repos 行。
// 失败只记日志：Status 与托管 git 入口发现行与项目不一致时会自愈。
func (s *Server) moveHostedHead(ctx context.Context, id, base string) {
	svc, err := s.hostedRepos()
	if err != nil {
		slog.Error("改托管仓库基线分支", "project", id, "err", err)
		return
	}
	if err := svc.SetHostedHead(ctx, id, base); err != nil {
		slog.Error("改托管仓库 HEAD", "project", id, "err", err)
		return
	}
	if err := svc.RecordHosted(ctx, id, base); err != nil {
		slog.Error("写托管仓库记录", "project", id, "err", err)
	}
}

// hostedIdentity 是托管仓库初始提交的作者：项目覆盖 > 所有者默认 > Oryn <oryn@<public_url 的主机>>（同 orb 的 git 身份规则）。
func (s *Server) hostedIdentity(ctx context.Context, p db.Project) repos.Identity {
	if id := gitIdentityJSON(p.GitAuthorName, p.GitAuthorEmail); id != nil {
		return repos.Identity{Name: id.Name, Email: id.Email}
	}
	row, err := s.Store.Q.GetOwnerGitIdentity(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("读取 git 身份", "err", err)
	}
	if id := gitIdentityJSON(row.GitName, row.GitEmail); id != nil {
		return repos.Identity{Name: id.Name, Email: id.Email}
	}
	host := "localhost"
	if u, err := url.Parse(s.Config.PublicURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	return repos.Identity{Name: "Oryn", Email: "oryn@" + host}
}

// CreateProjectHostedRepo 为没有仓库的项目创建托管仓库（带初始提交，基线分支缺省 main）。
func (s *Server) CreateProjectHostedRepo(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	out, err := s.createProjectHostedRepo(r.Context(), id)
	if err != nil {
		writeRepoErr(w, r, err)
		return
	}
	s.writeProject(w, r, http.StatusCreated, out)
}

func (s *Server) createProjectHostedRepo(ctx context.Context, id string) (db.Project, error) {
	errExists := &statusError{http.StatusConflict, "repo_exists", "项目已有仓库"}
	p, err := s.Store.Q.GetProject(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Project{}, errNotFound("项目不存在")
	} else if err != nil {
		return db.Project{}, err
	}
	if p.ArchivedAt != nil {
		return db.Project{}, &statusError{http.StatusConflict, "project_archived", "已归档的项目不能创建仓库，先取消归档"}
	}
	if p.RepoUrl != nil && *p.RepoUrl != "" {
		return db.Project{}, errExists
	}
	base := cmp.Or(deref(p.BaseBranch), "main")
	if !repos.ValidRef(base) {
		return db.Project{}, errInvalid("baseBranch 不是合法的分支名")
	}
	svc, err := s.hostedRepos()
	if err != nil {
		return db.Project{}, err
	}
	if p.Hosted {
		// 已标为托管：仓库在就是重复创建；不在（上次崩在建仓之前）就补建
		st, err := svc.Status(ctx, id)
		if err != nil {
			return db.Project{}, err
		}
		if st.State == repos.StateReady {
			return db.Project{}, errExists
		}
	} else {
		// 单条语句做条件更新，并发的两次创建只有一次能把项目标为托管
		n, err := s.Store.Q.SetProjectHosted(ctx, db.SetProjectHostedParams{BaseBranch: &base, UpdatedAt: time.Now().UnixMilli(), ID: id})
		if err != nil {
			return db.Project{}, err
		}
		if n == 0 {
			return db.Project{}, errExists
		}
	}
	if _, err := s.initHostedRepo(ctx, id, base, true); err != nil {
		if !p.Hosted {
			if _, uerr := s.Store.Q.UnsetProjectHosted(context.WithoutCancel(ctx), db.UnsetProjectHostedParams{UpdatedAt: time.Now().UnixMilli(), ID: id}); uerr != nil {
				slog.Error("撤销托管标记", "project", id, "err", uerr)
			}
		}
		return db.Project{}, err
	}
	// 出站策略按项目缓存（托管仓库的 git 路由），新 orb 的配置哈希也变了；正在运行的 orb 不受影响
	if s.Orbs != nil && s.Orbs.Service != nil {
		s.Orbs.Service.InvalidateProjectPolicies(ctx, id)
	}
	s.poolKick(id)
	return s.Store.Q.GetProject(ctx, id)
}
