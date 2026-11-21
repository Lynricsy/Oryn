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
	"unicode/utf8"

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

// 推送后 Webhook（M24 阶段 ③）：只有主人能改（不在 ProjectPatch 里，助手的项目工具碰不到这个端点）。

const (
	webhookURLMax    = 2048
	webhookSecretMax = 256
)

// webhookJSON 是 Webhook 行的 API 视图：从不返回签名密钥，只报告有没有。
func webhookJSON(h db.ProjectWebhook) *apigen.ProjectWebhook {
	out := &apigen.ProjectWebhook{URL: h.Url, HasSecret: len(h.SecretEnc) > 0, LastDeliveryAt: h.LastDeliveryAt, LastError: h.LastError}
	if h.LastStatus != nil {
		out.LastStatus = new(int(*h.LastStatus))
	}
	return out
}

// webhookURL 校验 Webhook 地址：http(s)、有主机、不带用户名密码（地址会原样显示在设置里）、≤2048 字符。
// 内网地址照常允许：只有主人能设，等同主人自己发请求。
func webhookURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || utf8.RuneCountInString(raw) > webhookURLMax {
		return "", errInvalid("Webhook 地址不能为空，且至多 2048 个字符")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errInvalid("Webhook 地址须是 http:// 或 https:// 地址")
	}
	if u.User != nil {
		return "", errInvalid("Webhook 地址不能带用户名或密码")
	}
	return raw, nil
}

// PutProjectWebhook 设置托管项目的推送后 Webhook：secret 省略 = 保留原密钥，空串 = 清除。
func (s *Server) PutProjectWebhook(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	var req apigen.ProjectWebhookInput
	if !decodeJSON(w, r, &req) {
		return
	}
	p, err := s.putProjectWebhook(r.Context(), id, req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.writeProject(w, r, http.StatusOK, p)
}

// putProjectWebhook 校验并写 Webhook 行，返回项目行。
func (s *Server) putProjectWebhook(ctx context.Context, id string, req apigen.ProjectWebhookInput) (db.Project, error) {
	p, err := s.Store.Q.GetProject(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return p, errNotFound("项目不存在")
	} else if err != nil {
		return p, err
	}
	if !p.Hosted {
		return p, &statusError{http.StatusConflict, "repo_not_hosted", "只有 Oryn 托管仓库的项目能设置推送后 Webhook"}
	}
	target, err := webhookURL(req.URL)
	if err != nil {
		return p, err
	}
	var sealed []byte
	switch {
	case req.Secret == nil:
		old, err := s.Store.Q.GetProjectWebhook(ctx, id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return p, err
		}
		sealed = old.SecretEnc
	case *req.Secret == "":
	default:
		if utf8.RuneCountInString(*req.Secret) > webhookSecretMax {
			return p, errInvalid("签名密钥至多 256 个字符")
		}
		if s.Repos == nil || s.Repos.Service == nil {
			return p, errRepoUnavailable
		}
		if sealed, err = s.Repos.Service.SealWebhookSecret(id, *req.Secret); err != nil {
			return p, err
		}
	}
	return p, s.Store.Q.UpsertProjectWebhook(ctx, db.UpsertProjectWebhookParams{
		ProjectID: id, Url: target, SecretEnc: sealed, UpdatedAt: time.Now().UnixMilli(),
	})
}

// DeleteProjectWebhook 移除项目的推送后 Webhook（幂等）。
func (s *Server) DeleteProjectWebhook(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	if _, err := s.Store.Q.GetProject(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		writeErr(w, r, errNotFound("项目不存在"))
		return
	} else if err != nil {
		writeErr(w, r, err)
		return
	}
	if _, err := s.Store.Q.DeleteProjectWebhook(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
