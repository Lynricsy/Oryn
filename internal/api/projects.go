package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func projectJSON(p db.Project) apigen.Project {
	return apigen.Project{
		ID: p.ID, Name: p.Name, RepoURL: p.RepoUrl, BaseBranch: p.BaseBranch,
		DefaultProvider: p.DefaultProvider, DefaultOrbType: p.DefaultOrbType,
		SetupScript: p.SetupScript, ResumeScript: p.ResumeScript, ShallowClone: p.ShallowClone,
		Color: p.Color, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, ArchivedAt: p.ArchivedAt,
	}
}

func validProjectName(name string) bool {
	n := utf8.RuneCountInString(name)
	return n >= 1 && n <= 100
}

// checkProviderName 校验 defaultProvider 指向已存在的 provider。
func (s *Server) checkProviderName(ctx context.Context, name *string) error {
	if name == nil {
		return nil
	}
	if _, err := s.Store.Q.GetProviderByName(ctx, *name); errors.Is(err, sql.ErrNoRows) {
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
	out := apigen.ProjectList{Projects: make([]apigen.Project, 0, len(rows))}
	for _, p := range rows {
		out.Projects = append(out.Projects, projectJSON(p))
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
	ctx := r.Context()
	if err := s.checkProviderName(ctx, optStr(req.DefaultProvider)); err != nil {
		writeErr(w, r, err)
		return
	}
	now := time.Now().UnixMilli()
	p := db.CreateProjectParams{
		ID: ids.New("prj"), Name: name, RepoUrl: optStr(req.RepoURL), BaseBranch: optStr(req.BaseBranch),
		DefaultProvider: optStr(req.DefaultProvider), DefaultOrbType: optStr(req.DefaultOrbType),
		SetupScript: optStr(req.SetupScript), ResumeScript: optStr(req.ResumeScript),
		ShallowClone: req.ShallowClone != nil && *req.ShallowClone, Color: optStr(req.Color),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.Store.Q.CreateProject(ctx, p); isUniqueViolation(err) {
		WriteError(w, http.StatusConflict, "conflict", "已有同名项目")
		return
	} else if err != nil {
		writeErr(w, r, err)
		return
	}
	row, err := s.Store.Q.GetProject(ctx, p.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, projectJSON(row))
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
	WriteJSON(w, http.StatusOK, projectJSON(p))
}

// UpdateProject 只修改出现的字段；可选字符串传空串清除。
func (s *Server) UpdateProject(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	var req apigen.ProjectPatch
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	if req.DefaultProvider != nil {
		if err := s.checkProviderName(ctx, optStr(req.DefaultProvider)); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	var out db.Project
	err := s.Store.Tx(ctx, func(q *db.Queries) error {
		p, err := q.GetProject(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return errNotFound("项目不存在")
		} else if err != nil {
			return err
		}
		u := db.UpdateProjectParams{
			Name: p.Name, RepoUrl: p.RepoUrl, BaseBranch: p.BaseBranch, DefaultProvider: p.DefaultProvider,
			DefaultOrbType: p.DefaultOrbType, SetupScript: p.SetupScript, ResumeScript: p.ResumeScript,
			ShallowClone: p.ShallowClone, Color: p.Color, ArchivedAt: p.ArchivedAt,
			UpdatedAt: time.Now().UnixMilli(), ID: id,
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
		out, err = q.GetProject(ctx, id)
		return err
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, projectJSON(out))
}

// DeleteProject 删除项目（线程保留，project_id 置空）。
func (s *Server) DeleteProject(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	n, err := s.Store.Q.DeleteProject(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if n == 0 {
		WriteError(w, http.StatusNotFound, "not_found", "项目不存在")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
