package api

import (
	"encoding/json"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 实时画布（M14 计划步骤 6.2）。用户的操作先在内存里实时广播给观看者，静默 30 秒或最后一个观看者离开时
// 才落成一个用户版本；见 artifacts/live.go。

// GetArtifactCanvas 对应 `GET artifacts/{id}/canvas`：底稿版本、实时序号与当前文档（含未落版本的用户操作）。
func (s *Server) GetArtifactCanvas(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	st, err := svc.CanvasState(r.Context(), id)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, st.API())
}

// ApplyArtifactCanvasOps 对应 `POST artifacts/{id}/canvas/ops`：应用一批用户操作（整批原子），回新的实时序号。
func (s *Server) ApplyArtifactCanvasOps(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	var req apigen.ArtifactCanvasOps
	if !decodeJSON(w, r, &req) {
		return
	}
	raw, err := json.Marshal(req.Ops)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	seq, err := svc.ApplyCanvas(r.Context(), id, raw, req.Client, req.ClientSeq)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.ArtifactCanvasSeq{Seq: seq})
}
