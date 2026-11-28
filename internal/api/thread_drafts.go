package api

import (
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 写作块改稿：用户就地修改 agent 起草的写作块后由前端自动保存，下一条用户消息发出时随之交给 agent。

// ListThreadDrafts 对应 `GET /api/v1/threads/{id}/drafts`。
func (s *Server) ListThreadDrafts(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	list, err := svc.ListDrafts(r.Context(), id)
	if err != nil {
		writeThreadErr(w, err)
		return
	}
	out := apigen.ThreadDraftList{Drafts: make([]apigen.ThreadDraft, len(list))}
	for i, d := range list {
		out.Drafts[i] = threadDraftAPI(d)
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutThreadDraft 对应 `PUT /api/v1/threads/{id}/drafts/{entryId}/{block}`：写入改稿最新全文。
func (s *Server) PutThreadDraft(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath, entryID apigen.DraftEntryIDPath, block apigen.DraftBlockPath) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	var in apigen.ThreadDraftInput
	if !decodeJSON(w, r, &in) {
		return
	}
	d, err := svc.PutDraft(r.Context(), id, entryID, block, threads.DraftInput{Kind: string(in.Kind), Title: in.Title, Text: in.Text})
	if err != nil {
		writeThreadErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, threadDraftAPI(d))
}

// DeleteThreadDraft 对应 `DELETE /api/v1/threads/{id}/drafts/{entryId}/{block}`：删除改稿（恢复原稿）。
func (s *Server) DeleteThreadDraft(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath, entryID apigen.DraftEntryIDPath, block apigen.DraftBlockPath) {
	svc, ok := s.threadsSvc(w)
	if !ok {
		return
	}
	if err := svc.DeleteDraft(r.Context(), id, entryID, block); err != nil {
		writeThreadErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func threadDraftAPI(d threads.Draft) apigen.ThreadDraft {
	return apigen.ThreadDraft{
		EntryID: d.EntryID, Block: d.Block, Kind: apigen.DraftKind(d.Kind), Title: d.Title, Text: d.Text,
		UpdatedAt: d.UpdatedAt, DeliveredAt: d.DeliveredAt,
	}
}
