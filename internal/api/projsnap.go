package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// projectSnapshotJSON 把 project_snapshots 行转换成 API 形态。
func projectSnapshotJSON(r db.ProjectSnapshot, providerName string) apigen.ProjectSnapshot {
	out := apigen.ProjectSnapshot{
		ID: r.ID, ProviderID: r.ProviderID, Type: r.Type, State: apigen.ProjectSnapshotState(r.State),
		ConfigHash: r.ConfigHash, Depth: int(r.Depth), LineageAt: r.LineageAt, CreatedAt: r.CreatedAt,
		ReadyAt: r.ReadyAt, LastUsedAt: r.LastUsedAt, UseCount: r.UseCount, SizeBytes: r.SizeBytes, RetireReason: r.RetireReason,
	}
	if providerName != "" {
		out.ProviderName = &providerName
	}
	if r.BaseCommit != "" {
		out.BaseCommit = &r.BaseCommit
	}
	return out
}

// projectExists 校验项目存在；不存在时写出 404 并返回 false。
func (s *Server) projectExists(w http.ResponseWriter, r *http.Request, id string) bool {
	if _, err := s.Store.Q.GetProject(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "项目不存在")
		return false
	} else if err != nil {
		writeErr(w, r, err)
		return false
	}
	return true
}

// ListProjectSnapshots 实现 GET /api/v1/projects/{id}/snapshots：按 createdAt 倒序，含 building / ready / retired。
func (s *Server) ListProjectSnapshots(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath) {
	if !s.projectExists(w, r, id) {
		return
	}
	rows, err := s.Store.Q.ListProjectSnapshotsByProject(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := apigen.ProjectSnapshotList{Snapshots: make([]apigen.ProjectSnapshot, 0, len(rows))}
	for _, row := range rows {
		out.Snapshots = append(out.Snapshots, projectSnapshotJSON(row.ProjectSnapshot, row.ProviderName))
	}
	WriteJSON(w, http.StatusOK, out)
}

// DeleteProjectSnapshots 实现 DELETE /api/v1/projects/{id}/snapshots[?type=]：把匹配的 ready 快照退役（manual）；
// 镜像由回收器在不再被 orb 引用后异步删除。
func (s *Server) DeleteProjectSnapshots(w http.ResponseWriter, r *http.Request, id apigen.ProjectIDPath, params apigen.DeleteProjectSnapshotsParams) {
	if !s.projectExists(w, r, id) {
		return
	}
	typ := params.Type
	if typ != nil && *typ == "" {
		typ = nil
	}
	if err := s.Store.Q.RetireProjectSnapshotsByProject(r.Context(), db.RetireProjectSnapshotsByProjectParams{
		RetiredAt: time.Now().UnixMilli(), RetireReason: orbs.RetireManual, ProjectID: id, Type: typ,
	}); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
