package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"math"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts"
	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// 视图帧（M33）：为转录里的一次 render_html 调用申请帧。帧的内容在每次取帧根时现从条目读取，
// 申请时只确认这次调用存在；关帧沿用 `DELETE artifact-frames/{token}`。

// maxToolCallID 是 ViewFrameRequest.toolCallId 的长度上限（与规格一致）。
const maxToolCallID = 256

// viewFrameRequest 解出并校验请求体；不合法时已写出 400。
func viewFrameRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req apigen.ViewFrameRequest
	if !decodeJSON(w, r, &req) {
		return "", false
	}
	if req.ToolCallID == "" || len(req.ToolCallID) > maxToolCallID {
		WriteError(w, http.StatusBadRequest, "invalid_request", "toolCallId must be 1..256 characters")
		return "", false
	}
	return req.ToolCallID, true
}

// OpenViewFrame 对应 `POST threads/{id}/view-frames`：在该线程主会话的条目里找这次 render_html 调用，找不到 404。
// load 闭包按 (线程, seq) 直接取那一行，转录被改写、条目不在了帧根即 404。
func (s *Server) OpenViewFrame(w http.ResponseWriter, r *http.Request, id apigen.ThreadIDPath) {
	host, ok := s.artifactsHost(w)
	if !ok {
		return
	}
	callID, ok := viewFrameRequest(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if _, err := s.Store.Q.GetThread(ctx, id); err != nil {
		writeViewFrameErr(w, err)
		return
	}
	seqs, err := s.Store.Q.ListEntrySeqsContaining(ctx, db.ListEntrySeqsContainingParams{ThreadID: id, UntilSeq: math.MaxInt64, Needle: []byte(callID)})
	if err != nil {
		writeViewFrameErr(w, err)
		return
	}
	q := s.Store.Q
	load := func(seq int64) func(context.Context) (artifacts.ViewDoc, error) {
		return func(ctx context.Context) (artifacts.ViewDoc, error) {
			return threadViewDoc(ctx, q, id, seq, callID)
		}
	}
	for _, seq := range seqs {
		if _, err := threadViewDoc(ctx, q, id, seq, callID); err != nil {
			if errors.Is(err, artifacts.ErrViewNotFound) {
				continue
			}
			writeViewFrameErr(w, err)
			return
		}
		token, url := host.OpenViewFrame(load(seq), r.Header.Get("Origin"), false)
		WriteJSON(w, http.StatusCreated, apigen.ViewFrame{Token: token, URL: url})
		return
	}
	WriteError(w, http.StatusNotFound, "not_found", "view not found")
}

// threadViewDoc 读线程主会话的第 seq 条条目，取出其中 id 为 callID 的 render_html 调用的视图内容。
func threadViewDoc(ctx context.Context, q *db.Queries, threadID string, seq int64, callID string) (artifacts.ViewDoc, error) {
	rows, err := q.ListEntriesBySeqRange(ctx, db.ListEntriesBySeqRangeParams{ThreadID: threadID, AfterSeq: seq - 1, UntilSeq: seq, Limit: 1})
	if err != nil {
		return artifacts.ViewDoc{}, err
	}
	if len(rows) == 0 {
		return artifacts.ViewDoc{}, artifacts.ErrViewNotFound
	}
	e := rows[0]
	return viewDocOf(transcript.Entry{Seq: e.Seq, File: e.File, EntryID: e.EntryID, ParentID: e.ParentID, Type: e.Type, TS: e.Ts, Payload: e.Payload}, callID)
}

func viewDocOf(e transcript.Entry, callID string) (artifacts.ViewDoc, error) {
	args, ok := transcript.ToolCallArgs(e, callID, artifacts.ViewTool)
	if !ok {
		return artifacts.ViewDoc{}, artifacts.ErrViewNotFound
	}
	doc, ok := artifacts.ViewDocFromArgs(args)
	if !ok {
		return artifacts.ViewDoc{}, artifacts.ErrViewNotFound
	}
	return doc, nil
}

// OpenPublicShareViewFrame 对应 `POST public/shares/{slug}/view-frames`（匿名）：内容一律从服务端条目取，
// 只在分享范围内、用清洗后的公开条目查找，且只服务已有成功结果的调用。帧绑定这个分享：load 每次重新走
// 分享校验，撤销后帧根即 404。帧计入匿名配额。
func (s *Server) OpenPublicShareViewFrame(w http.ResponseWriter, r *http.Request, slug apigen.ShareSlugPath) {
	setPublicNoIndex(w)
	svc, ok := s.shareSvc(w)
	if !ok {
		return
	}
	host, ok := s.artifactsHost(w)
	if !ok {
		return
	}
	callID, ok := viewFrameRequest(w, r)
	if !ok {
		return
	}
	load := func(ctx context.Context) (artifacts.ViewDoc, error) {
		args, err := svc.PublicToolCall(ctx, slug, callID, artifacts.ViewTool)
		if err != nil {
			if errors.Is(err, share.ErrNotFound) {
				return artifacts.ViewDoc{}, artifacts.ErrViewNotFound
			}
			return artifacts.ViewDoc{}, err
		}
		doc, ok := artifacts.ViewDocFromArgs(args)
		if !ok {
			return artifacts.ViewDoc{}, artifacts.ErrViewNotFound
		}
		return doc, nil
	}
	if _, err := load(r.Context()); err != nil {
		writeViewFrameErr(w, err)
		return
	}
	token, url := host.OpenViewFrame(load, r.Header.Get("Origin"), true)
	WriteJSON(w, http.StatusCreated, apigen.ViewFrame{Token: token, URL: url})
}

// writeViewFrameErr：线程不存在、调用不在了一律 404，其余 500。
func writeViewFrameErr(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, artifacts.ErrViewNotFound) {
		WriteError(w, http.StatusNotFound, "not_found", "view not found")
		return
	}
	slog.Error("api: 申请视图帧失败", "err", err)
	WriteError(w, http.StatusInternalServerError, "internal", "internal error")
}
