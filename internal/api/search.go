package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/search"
)

// SearchDeps 是对话内容全文搜索的依赖。
type SearchDeps struct {
	Service *search.Service
}

// SearchMessages 对应 `GET search?q=&limit=&cursor=`。
func (s *Server) SearchMessages(w http.ResponseWriter, r *http.Request, params apigen.SearchMessagesParams) {
	if s.Search == nil || s.Search.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "search service not configured")
		return
	}
	p := search.Params{Q: params.Q}
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > search.MaxLimit {
			WriteError(w, http.StatusBadRequest, "invalid_request", "limit 须在 1–50 之间")
			return
		}
		p.Limit = *params.Limit
	}
	if params.Cursor != nil {
		p.Cursor = *params.Cursor
	}
	res, err := s.Search.Service.Search(r.Context(), p)
	if err != nil {
		if errors.Is(err, search.ErrInvalid) {
			WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		slog.Error("api: 搜索失败", "err", err)
		WriteError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	WriteJSON(w, http.StatusOK, res)
}
