package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts"
)

// ArtifactsDeps 是 M14 Artifacts 的依赖（计划步骤 1.3）。
type ArtifactsDeps struct {
	Service *artifacts.Service
}

func (s *Server) artifactsSvc(w http.ResponseWriter) (*artifacts.Service, bool) {
	if s.Artifacts == nil || s.Artifacts.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "artifacts not configured")
		return nil, false
	}
	return s.Artifacts.Service, true
}

// writeArtifactErr 把 artifact 的操作错误按错误码映射为 HTTP 状态；conflict 的响应体另带 state。
func writeArtifactErr(w http.ResponseWriter, r *http.Request, err error) {
	oe, ok := artifacts.AsOpError(err)
	if !ok {
		writeErr(w, r, err)
		return
	}
	if oe.Code == artifacts.CodeConflict && oe.State != nil {
		WriteJSON(w, http.StatusConflict, apigen.ArtifactConflict{
			Code: oe.Code, Message: oe.Message,
			State: apigen.ArtifactState{Version: oe.State.Version, Deleted: oe.State.Deleted, Changed: oe.State.Changed},
		})
		return
	}
	WriteError(w, oe.Status(), oe.Code, oe.Message)
}

// artifactVersionParam 校验可选的 version 查询参数（缺省 = 0，即最新版本）。
func artifactVersionParam(w http.ResponseWriter, v *int) (int, bool) {
	if v == nil {
		return 0, true
	}
	if *v < 1 {
		WriteError(w, http.StatusBadRequest, artifacts.CodeInvalid, "version 必须从 1 开始")
		return 0, false
	}
	return *v, true
}

// ListArtifacts 对应 `GET artifacts?deleted=&threadId=&projectId=`：最近更新的在前。
func (s *Server) ListArtifacts(w http.ResponseWriter, r *http.Request, params apigen.ListArtifactsParams) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	var f artifacts.Filter
	if params.Deleted != nil {
		f.Deleted = *params.Deleted
	}
	if params.ThreadID != nil {
		f.ThreadID = *params.ThreadID
	}
	if params.ProjectID != nil {
		f.ProjectID = *params.ProjectID
	}
	list, err := svc.List(r.Context(), f)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	out := apigen.ArtifactList{Artifacts: make([]apigen.Artifact, 0, len(list))}
	for _, a := range list {
		out.Artifacts = append(out.Artifacts, a.API())
	}
	WriteJSON(w, http.StatusOK, out)
}

// GetArtifact 对应 `GET artifacts/{id}?version=`：某个版本的文件清单、能力声明（画布另带解析后的 canvas.json），
// 以及创建线程与写过版本的线程。回收站里的也能取到（查看器据此显示「已删除」）。
func (s *Server) GetArtifact(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath, params apigen.GetArtifactParams) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	version, ok := artifactVersionParam(w, params.Version)
	if !ok {
		return
	}
	ctx := r.Context()
	a, err := svc.Get(ctx, id)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	v, err := svc.Version(ctx, id, version)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	files, err := svc.Files(ctx, id, v.Version)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	refs, err := s.artifactThreads(ctx, svc, a)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	out := apigen.ArtifactDetail{
		Artifact: a.API(), Version: v.Version, Head: a.Version, Threads: refs,
		Files:        make([]apigen.ArtifactFile, 0, len(files)),
		Capabilities: make([]apigen.ArtifactCapability, 0, len(v.Capabilities)),
	}
	for _, f := range files {
		out.Files = append(out.Files, f.API())
	}
	for _, c := range v.Capabilities {
		out.Capabilities = append(out.Capabilities, apigen.ArtifactCapability(c))
	}
	if a.Kind == artifacts.KindCanvas {
		// canvas.json 解析失败（或读不到）时省略，查看器按空画布处理
		if b, _, err := svc.ReadFile(ctx, id, v.Version, artifacts.EntryCanvas); err == nil {
			var doc map[string]any
			if json.Unmarshal(b, &doc) == nil {
				out.Canvas = doc
			}
		}
	}
	WriteJSON(w, http.StatusOK, out)
}

// artifactThreads 返回创建线程（若还在）与写过版本的线程（还在的，最近写入的在前），去重。
func (s *Server) artifactThreads(ctx context.Context, svc *artifacts.Service, a artifacts.Artifact) ([]apigen.ArtifactThreadRef, error) {
	authors, err := svc.AuthorThreads(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	order := make([]string, 0, len(authors)+1)
	if a.ThreadID != "" {
		order = append(order, a.ThreadID)
	}
	order = append(order, authors...)
	seen := make(map[string]bool, len(order))
	out := make([]apigen.ArtifactThreadRef, 0, len(order))
	for _, tid := range order {
		if seen[tid] {
			continue
		}
		seen[tid] = true
		t, err := s.Store.Q.GetThread(ctx, tid)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, apigen.ArtifactThreadRef{ThreadID: t.ID, Title: t.Title, Archived: t.ArchivedAt != nil})
	}
	return out, nil
}

// PatchArtifact 对应 `PATCH artifacts/{id}`：重命名（不产生版本；回收站里的回 410）。
func (s *Server) PatchArtifact(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	var req apigen.ArtifactPatch
	if !decodeJSON(w, r, &req) {
		return
	}
	a, err := svc.Rename(r.Context(), id, req.Title)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, a.API())
}

// DeleteArtifact 对应 `DELETE artifacts/{id}?permanent=`：缺省移到「最近删除」；permanent=true 永久删除
// 回收站里的 artifact（不在回收站里的回 400）。
func (s *Server) DeleteArtifact(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath, params apigen.DeleteArtifactParams) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	var err error
	if params.Permanent != nil && *params.Permanent {
		err = svc.Erase(r.Context(), id)
	} else {
		_, err = svc.Delete(r.Context(), id)
	}
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UndeleteArtifact 对应 `POST artifacts/{id}/restore`：从「最近删除」恢复。
func (s *Server) UndeleteArtifact(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	a, err := svc.Undelete(r.Context(), id)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, a.API())
}

// MarkArtifactOpened 对应 `POST artifacts/{id}/opened`。
func (s *Server) MarkArtifactOpened(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	if err := svc.MarkOpened(r.Context(), id); err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListArtifactVersions 对应 `GET artifacts/{id}/versions`：新的在前。
func (s *Server) ListArtifactVersions(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	list, err := svc.Versions(r.Context(), id)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	out := apigen.ArtifactVersionList{Versions: make([]apigen.ArtifactVersion, 0, len(list))}
	for _, v := range list {
		out.Versions = append(out.Versions, v.API())
	}
	WriteJSON(w, http.StatusOK, out)
}

// RestoreArtifactVersion 对应 `POST artifacts/{id}/versions/{n}/restore`：把版本 n 复制成新的最新版本，
// 作者是用户；baseVersion 之后两版间不同的文件被改过时回 409 并带 state。
func (s *Server) RestoreArtifactVersion(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath, n apigen.ArtifactVersionPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	var req apigen.ArtifactRestoreVersion
	if !decodeJSON(w, r, &req) {
		return
	}
	if n < 1 {
		WriteError(w, http.StatusBadRequest, artifacts.CodeInvalid, "版本号必须从 1 开始")
		return
	}
	a, err := svc.RestoreVersion(r.Context(), id, n, req.BaseVersion, artifacts.Author{})
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, a.API())
}

// GetArtifactFile 对应 `GET artifacts/{id}/file?version=&path=`：内容策略同 serveStoredFile——栅格图 inline，
// 其余一律 application/octet-stream + attachment，带 `Content-Security-Policy: sandbox` 与 nosniff，支持 Range。
// 主域上永远不把 artifact 的 HTML 当页面渲染（渲染只在独立源的帧里）。
func (s *Server) GetArtifactFile(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath, params apigen.GetArtifactFileParams) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	version, ok := artifactVersionParam(w, params.Version)
	if !ok {
		return
	}
	ctx := r.Context()
	v, err := svc.Version(ctx, id, version)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	content, f, err := svc.Open(ctx, id, v.Version, params.Path)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	defer content.Close()
	serveStoredFile(w, r, content, path.Base(f.Path), f.Mime, v.CreatedAt, true)
}
