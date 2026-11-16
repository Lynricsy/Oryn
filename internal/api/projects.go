package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/github"
	"github.com/Lynricsy/Oryn/internal/glyph"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// projectJSON 把项目行转成 API 视图；hook 是项目所绑仓库的 github_hooks 行（没有为 nil）。
func projectJSON(p db.Project, hook *db.GithubHook) apigen.Project {
	out := apigen.Project{
		ID: p.ID, Name: p.Name, RepoURL: p.RepoUrl, BaseBranch: p.BaseBranch,
		DefaultProvider: p.DefaultProvider, DefaultOrbType: p.DefaultOrbType,
		SetupScript: p.SetupScript, ResumeScript: p.ResumeScript, ShallowClone: p.ShallowClone,
		Color: p.Color, Icon: p.Icon, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, ArchivedAt: p.ArchivedAt,
		DefaultBrowserProfileID: p.DefaultBrowserProfileID, Snapshots: p.Snapshots, WarmOrbs: int(p.WarmOrbs),
	}
	if id := gitIdentityJSON(p.GitAuthorName, p.GitAuthorEmail); id != nil {
		out.GitAuthorName, out.GitAuthorEmail = &id.Name, &id.Email
	}
	out.Github = github.ProjectGitHubAPI(p, hook)
	return out
}

// writeProject 以 status 写出单个项目的 API 视图：已绑定时至多查一次所绑仓库的 hook 行。
func (s *Server) writeProject(w http.ResponseWriter, r *http.Request, status int, p db.Project) {
	var hook *db.GithubHook
	if p.GithubRepoID != nil {
		h, err := s.Store.Q.GetGithubHook(r.Context(), *p.GithubRepoID)
		if err == nil {
			hook = &h
		} else if !errors.Is(err, sql.ErrNoRows) {
			writeErr(w, r, err)
			return
		}
	}
	WriteJSON(w, status, projectJSON(p, hook))
}

func sameOptStr(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func validProjectName(name string) bool {
	n := utf8.RuneCountInString(name)
	return n >= 1 && n <= 100
}

// projectIcon 校验项目图标：nil 或空串 → nil（未设置 / 清除）；否则须是单个字形且不超过 16 字节。
func projectIcon(v *string) (*string, error) {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil, nil
	}
	icon := strings.TrimSpace(*v)
	if !glyph.Single(icon, glyph.MaxIconBytes) {
		return nil, errInvalid("icon 须是单个字形（≤16 字节）")
	}
	return &icon, nil
}

// checkBrowserProfile 校验 defaultBrowserProfileId：nil 或空串 → nil（不设 / 清除）；否则须是已存在的 Profile
// （404 profile_not_found）。
func (s *Server) checkBrowserProfile(ctx context.Context, id *string) (*string, error) {
	v := optStr(id)
	if v == nil {
		return nil, nil
	}
	if _, err := s.Store.Q.GetBrowserProfile(ctx, *v); errors.Is(err, sql.ErrNoRows) {
		return nil, &statusError{http.StatusNotFound, "profile_not_found", "浏览器 Profile 不存在"}
	} else if err != nil {
		return nil, err
	}
	return v, nil
}

// checkProviderName 校验 defaultProvider 指向已存在的 provider。
func (s *Server) checkProviderName(ctx context.Context, name *string) error {
	if name == nil {
		return nil
	}
	if *name == orbs.HostProviderName {
		return errInvalid("provider " + *name + " 是协调宿主保留的内置 provider")
	}
	// runner 的 provider 行（M11b）不能新建 orb，不能作项目默认
	if p, err := s.Store.Q.GetProviderByName(ctx, *name); errors.Is(err, sql.ErrNoRows) || (err == nil && p.Driver == orbs.DriverRunner) {
		return errInvalid("provider " + *name + " 不存在")
	} else if err != nil {
		return err
	}
	return nil
}

// ListProjects 列出项目；缺省只返回未归档。
func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request, params apigen.ListProjectsParams) {
	archived := false
	if params.Archived != nil {
		archived = *params.Archived
	}
	rows, err := s.Store.Q.ListProjects(r.Context(), &archived)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// hook 行一次读全再按仓库 id 查，不逐项目查询
	var hooks map[int64]db.GithubHook
	if slices.ContainsFunc(rows, func(p db.Project) bool { return p.GithubRepoID != nil }) {
		all, err := s.Store.Q.ListGithubHooks(r.Context())
		if err != nil {
			writeErr(w, r, err)
			return
		}
		hooks = make(map[int64]db.GithubHook, len(all))
		for _, h := range all {
			hooks[h.RepoID] = h
		}
	}
	out := apigen.ProjectList{Projects: make([]apigen.Project, 0, len(rows))}
	for _, p := range rows {
		var hook *db.GithubHook
		if p.GithubRepoID != nil {
			if h, ok := hooks[*p.GithubRepoID]; ok {
				hook = &h
			}
		}
		out.Projects = append(out.Projects, projectJSON(p, hook))
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateProject 新建项目。
func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request) {
	var req apigen.ProjectCreate
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if !validProjectName(name) {
		WriteError(w, http.StatusBadRequest, "invalid_request", "项目名长度须为 1–100")
		return
	}
	warm, err := checkWarmOrbs(req.WarmOrbs)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	gitName, gitEmail, _, err := projectGitIdentity(req.GitAuthorName, req.GitAuthorEmail)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	icon, err := projectIcon(req.Icon)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	if err := s.checkProviderName(ctx, optStr(req.DefaultProvider)); err != nil {
		writeErr(w, r, err)
		return
	}
	profileID, err := s.checkBrowserProfile(ctx, req.DefaultBrowserProfileID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	now := time.Now().UnixMilli()
	p := db.CreateProjectParams{
		ID: ids.New("prj"), Name: name, RepoUrl: optStr(req.RepoURL), BaseBranch: optStr(req.BaseBranch),
		DefaultProvider: optStr(req.DefaultProvider), DefaultOrbType: optStr(req.DefaultOrbType),
		SetupScript: optStr(req.SetupScript), ResumeScript: optStr(req.ResumeScript),
		ShallowClone: req.ShallowClone != nil && *req.ShallowClone, Color: optStr(req.Color), Icon: icon,
		GitAuthorName: gitName, GitAuthorEmail: gitEmail, CreatedAt: now, UpdatedAt: now, DefaultBrowserProfileID: profileID,
		Snapshots: req.Snapshots, WarmOrbs: warm,
	}
	if err := s.Store.Q.CreateProject(ctx, p); isUniqueViolation(err) {
		WriteError(w, http.StatusConflict, "conflict", "已有同名项目")
		return
	} else if err != nil {
		writeErr(w, r, err)
		return
	}
	if s.GitHub != nil && s.GitHub.Service != nil {
		if err := s.GitHub.Service.BindProject(ctx, p.ID); err != nil {
			slog.Warn("绑定项目的 GitHub 仓库失败", "project", p.ID, "err", err)
		}
	}
	row, err := s.Store.Q.GetProject(ctx, p.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if row.WarmOrbs > 0 {
		s.poolKick(row.ID)
	}
	s.writeProject(w, r, http.StatusCreated, row)
}

// GetProject 返回项目详情。
func (s *Server) GetProject(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	p, err := s.Store.Q.GetProject(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "项目不存在")
		return
	} else if err != nil {
		writeErr(w, r, err)
		return
	}
	s.writeProject(w, r, http.StatusOK, p)
}

// UpdateProject 只修改出现的字段；可选字符串传空串清除。
func (s *Server) UpdateProject(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	var req apigen.ProjectPatch
	if !decodeJSON(w, r, &req) {
		return
	}
	gitName, gitEmail, gitSet, err := projectGitIdentity(req.GitAuthorName, req.GitAuthorEmail)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	icon, err := projectIcon(req.Icon)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	if req.DefaultProvider != nil {
		if err := s.checkProviderName(ctx, optStr(req.DefaultProvider)); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	profileID, err := s.checkBrowserProfile(ctx, req.DefaultBrowserProfileID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	warm, err := checkWarmOrbs(req.WarmOrbs)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var out db.Project
	var oldRepoID *int64 // 改仓库、归档后可能不再有项目绑定的 GitHub 仓库
	repoChanged := false
	err = s.Store.Tx(ctx, func(q *db.Queries) error {
		p, err := q.GetProject(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return errNotFound("项目不存在")
		} else if err != nil {
			return err
		}
		u := db.UpdateProjectParams{
			Name: p.Name, RepoUrl: p.RepoUrl, BaseBranch: p.BaseBranch, DefaultProvider: p.DefaultProvider,
			DefaultOrbType: p.DefaultOrbType, SetupScript: p.SetupScript, ResumeScript: p.ResumeScript,
			ShallowClone: p.ShallowClone, Color: p.Color, Icon: p.Icon, GitAuthorName: p.GitAuthorName,
			GitAuthorEmail: p.GitAuthorEmail, ArchivedAt: p.ArchivedAt, UpdatedAt: time.Now().UnixMilli(), ID: id,
			DefaultBrowserProfileID: p.DefaultBrowserProfileID,
		}
		if req.DefaultBrowserProfileID != nil {
			// 未给出该字段时保留旧值；空串清除
			u.DefaultBrowserProfileID = profileID
		}
		if gitSet {
			u.GitAuthorName, u.GitAuthorEmail = gitName, gitEmail
		}
		if req.Icon != nil {
			u.Icon = icon
		}
		if req.Name != nil {
			u.Name = strings.TrimSpace(*req.Name)
			if !validProjectName(u.Name) {
				return errInvalid("项目名长度须为 1–100")
			}
		}
		patchStr := func(dst **string, v *string) {
			if v != nil {
				*dst = optStr(v)
			}
		}
		patchStr(&u.RepoUrl, req.RepoURL)
		patchStr(&u.BaseBranch, req.BaseBranch)
		patchStr(&u.DefaultProvider, req.DefaultProvider)
		patchStr(&u.DefaultOrbType, req.DefaultOrbType)
		patchStr(&u.SetupScript, req.SetupScript)
		patchStr(&u.ResumeScript, req.ResumeScript)
		patchStr(&u.Color, req.Color)
		if req.ShallowClone != nil {
			u.ShallowClone = *req.ShallowClone
		}
		u.Snapshots, u.WarmOrbs = req.Snapshots, warm
		if req.Archived != nil {
			switch {
			case *req.Archived && p.ArchivedAt == nil:
				u.ArchivedAt = new(u.UpdatedAt)
			case !*req.Archived:
				u.ArchivedAt = nil
			}
		}
		if _, err := q.UpdateProject(ctx, u); isUniqueViolation(err) {
			return errConflict("已有同名项目")
		} else if err != nil {
			return err
		}
		repoChanged = !sameOptStr(p.RepoUrl, u.RepoUrl)
		if repoChanged || (u.ArchivedAt != nil && p.ArchivedAt == nil) {
			oldRepoID = p.GithubRepoID
		}
		if repoChanged && s.GitHub != nil {
			// 旧仓库的事件不得再命中这个项目；新仓库在事务之后重新绑定
			if err := q.ClearProjectGithub(ctx, id); err != nil {
				return err
			}
		}
		if req.GithubListen != nil {
			if err := q.SetProjectGithubListen(ctx, db.SetProjectGithubListenParams{
				GithubListenReviews: req.GithubListen.Reviews, GithubListenComments: req.GithubListen.Comments,
				GithubListenCi: req.GithubListen.Ci, UpdatedAt: u.UpdatedAt, ID: id,
			}); err != nil {
				return err
			}
		}
		if req.Snapshots != nil && !*req.Snapshots && p.Snapshots {
			// 关闭项目快照（S-P2-2）：就绪的退役，在建的同时作废（构建者发布时看到作废会自行清理镜像）
			if err := q.RetireProjectSnapshotsByProject(ctx, db.RetireProjectSnapshotsByProjectParams{
				RetiredAt: u.UpdatedAt, RetireReason: orbs.RetireManual, ProjectID: id,
			}); err != nil {
				return err
			}
		}
		out, err = q.GetProject(ctx, id)
		return err
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if repoChanged && s.GitHub != nil && s.GitHub.Service != nil {
		if err := s.GitHub.Service.BindProject(ctx, id); err != nil {
			slog.Warn("绑定项目的 GitHub 仓库失败", "project", id, "err", err)
		}
		if out, err = s.Store.Q.GetProject(ctx, id); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	if oldRepoID != nil && s.GitHub != nil && s.GitHub.Service != nil {
		if err := s.GitHub.Service.ReleaseHook(ctx, *oldRepoID); err != nil {
			slog.Warn("删除 GitHub 仓库 hook 失败", "project", id, "err", err)
		}
	}
	// 预热台数、快照开关、setup 脚本、仓库、分支、默认 provider / 类型或归档变化都会改变预热池的目标或配置哈希
	if req.WarmOrbs != nil || req.Snapshots != nil || req.SetupScript != nil || req.RepoURL != nil || req.BaseBranch != nil ||
		req.ShallowClone != nil || req.DefaultProvider != nil || req.DefaultOrbType != nil || req.Archived != nil {
		s.poolKick(id)
	}
	s.writeProject(w, r, http.StatusOK, out)
}

// DeleteProject 删除项目（线程保留，project_id 置空）。
func (s *Server) DeleteProject(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	ctx := r.Context()
	var n int64
	var repoID *int64
	err := s.Store.Tx(ctx, func(q *db.Queries) error {
		if p, err := q.GetProject(ctx, id); err == nil {
			repoID = p.GithubRepoID
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// 先退役项目快照（行随项目删除只把 project_id 置空，镜像交给回收器在无引用后删除）
		if err := q.RetireProjectSnapshotsByProject(ctx, db.RetireProjectSnapshotsByProjectParams{
			RetiredAt: time.Now().UnixMilli(), RetireReason: orbs.RetireProjectDeleted, ProjectID: id,
		}); err != nil {
			return err
		}
		var err error
		n, err = q.DeleteProject(ctx, id)
		return err
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if n == 0 {
		WriteError(w, http.StatusNotFound, "not_found", "项目不存在")
		return
	}
	// 最后一个绑定该仓库的项目删除后，删除 Oryn 的仓库 hook
	if repoID != nil && s.GitHub != nil && s.GitHub.Service != nil {
		if err := s.GitHub.Service.ReleaseHook(ctx, *repoID); err != nil {
			slog.Warn("删除 GitHub 仓库 hook 失败", "project", id, "err", err)
		}
	}
	// 项目级 omp 配置随项目删除（归档不删）；失败只记日志，目录留着无害
	if s.Auth != nil && s.Auth.ProjectHomes != nil {
		if err := s.Auth.ProjectHomes.Remove(id); err != nil {
			slog.Warn("删除项目 omp 配置失败", "project", id, "err", err)
		}
	}
	// M16：级联删除项目在 Hindsight 里的记忆（R23）。失败的进入重试队列；记忆未配置或 Hindsight 不可用都不阻塞删除
	if s.Memory != nil && s.Memory.Service != nil {
		if err := s.Memory.Service.PurgeProject(context.WithoutCancel(ctx), id); err != nil {
			slog.Warn("登记项目记忆的级联删除失败", "project", id, "err", err)
		}
	}
	// 该项目的预热池 orb 由对账回收
	s.poolKick(id)
	w.WriteHeader(http.StatusNoContent)
}

// poolKick 在影响预热池的项目设置变化后立即触发一次对账（未注入 orb 服务时无操作）。
func (s *Server) poolKick(projectID string) {
	if s.Orbs != nil && s.Orbs.Service != nil {
		s.Orbs.Service.PoolKick(projectID)
	}
}

// checkWarmOrbs 校验 warmOrbs 在 0–3 之间（nil = 未给出）。
func checkWarmOrbs(v *int) (*int64, error) {
	if v == nil {
		return nil, nil
	}
	if *v < 0 || *v > 3 {
		return nil, errInvalid("warmOrbs 须为 0–3")
	}
	return new(int64(*v)), nil
}
