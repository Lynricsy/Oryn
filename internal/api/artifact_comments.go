package api

import (
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts"
)

// artifact 的评论（M14 计划步骤 3.2）。作者一律是用户；agent 经 read / reply / resolve_artifact_comment 参与。

// ListArtifactComments 对应 `GET artifacts/{id}/comments`：评论串（旧的在前），锚点已在最新版本上重新定位。
func (s *Server) ListArtifactComments(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	head, ts, err := svc.Threads(r.Context(), id)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	out := apigen.ArtifactCommentThreadList{Version: head, Threads: make([]apigen.ArtifactCommentThread, 0, len(ts))}
	for _, t := range ts {
		out.Threads = append(out.Threads, t.API())
	}
	WriteJSON(w, http.StatusOK, out)
}

// CreateArtifactComment 对应 `POST artifacts/{id}/comments`：用户新建一个评论串。
func (s *Server) CreateArtifactComment(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	var req apigen.ArtifactCommentCreate
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := svc.Comment(r.Context(), id, artifacts.AnchorFromAPI(req.Anchor), req.Body)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, t.API())
}

// ReplyArtifactComment 对应 `POST artifacts/{id}/comments/{threadId}/replies`：用户回复（清掉该串的已发送标记）。
func (s *Server) ReplyArtifactComment(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath, threadID apigen.ArtifactCommentThreadIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	var req apigen.ArtifactCommentReply
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := svc.Reply(r.Context(), id, threadID, req.Body, artifacts.Author{})
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, t.API())
}

// PatchArtifactComment 对应 `PATCH artifacts/{id}/comments/{threadId}`：标为已解决 / 重新打开。
func (s *Server) PatchArtifactComment(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath, threadID apigen.ArtifactCommentThreadIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	var req apigen.ArtifactCommentPatch
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := svc.Resolve(r.Context(), id, threadID, req.Resolved)
	if err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, t.API())
}

// MarkArtifactCommentsSent 对应 `POST artifacts/{id}/comments/sent`：记下这些评论串已发给某个线程。
func (s *Server) MarkArtifactCommentsSent(w http.ResponseWriter, r *http.Request, id apigen.ArtifactIDPath) {
	svc, ok := s.artifactsSvc(w)
	if !ok {
		return
	}
	var req apigen.ArtifactCommentsSent
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := svc.MarkSent(r.Context(), id, req.ThreadIds, req.SentTo); err != nil {
		writeArtifactErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
