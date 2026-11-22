package api

import (
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/research"
)

// ResearchDeps 是 M26 后台研究（设置、跟进话题、运行记录）的依赖。
type ResearchDeps struct {
	Service *research.Service
}

func (s *Server) researchSvc(w http.ResponseWriter) (*research.Service, bool) {
	if s.Research == nil || s.Research.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "research service not configured")
		return nil, false
	}
	return s.Research.Service, true
}

// writeResearchErr 写出研究服务的错误：invalid_request → 400、not_found → 404，其余按通用映射。
func writeResearchErr(w http.ResponseWriter, r *http.Request, err error) {
	var re *research.Error
	if errors.As(err, &re) {
		status := http.StatusBadRequest
		if re.Code == research.CodeNotFound {
			status = http.StatusNotFound
		}
		WriteError(w, status, re.Code, re.Message)
		return
	}
	writeErr(w, r, err)
}

// GetResearchSettings 对应 `GET assistant/research/settings`。
func (s *Server) GetResearchSettings(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.researchSvc(w)
	if !ok {
		return
	}
	out, err := svc.Settings(r.Context())
	if err != nil {
		writeResearchErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutResearchSettings 对应 `PUT assistant/research/settings`：整体替换。
func (s *Server) PutResearchSettings(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.researchSvc(w)
	if !ok {
		return
	}
	var in apigen.ResearchSettings
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.PutSettings(r.Context(), in)
	if err != nil {
		writeResearchErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// ListResearchTopics 对应 `GET assistant/research/topics`。
func (s *Server) ListResearchTopics(w http.ResponseWriter, r *http.Request, params apigen.ListResearchTopicsParams) {
	svc, ok := s.researchSvc(w)
	if !ok {
		return
	}
	state := ""
	if params.State != nil {
		if !params.State.Valid() {
			WriteError(w, http.StatusBadRequest, "invalid_request", "unknown state")
			return
		}
		state = string(*params.State)
	}
	topics, err := svc.ListTopics(r.Context(), state)
	if err != nil {
		writeResearchErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.ResearchTopicList{Topics: topics})
}

// CreateResearchTopic 对应 `POST assistant/research/topics`。
func (s *Server) CreateResearchTopic(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.researchSvc(w)
	if !ok {
		return
	}
	var in apigen.ResearchTopicInput
	if !decodeJSON(w, r, &in) {
		return
	}
	ti := research.TopicInput{Kind: string(in.Kind), Title: in.Title, Question: in.Question}
	if in.Baseline != nil {
		ti.Baseline = *in.Baseline
	}
	if in.SourceThreadID != nil {
		ti.SourceThreadID = *in.SourceThreadID
	}
	out, err := svc.CreateTopic(r.Context(), ti)
	if err != nil {
		writeResearchErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, out)
}

// GetResearchTopic 对应 `GET assistant/research/topics/{id}`。
func (s *Server) GetResearchTopic(w http.ResponseWriter, r *http.Request, id string) {
	svc, ok := s.researchSvc(w)
	if !ok {
		return
	}
	out, err := svc.GetTopic(r.Context(), id)
	if err != nil {
		writeResearchErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// UpdateResearchTopic 对应 `PATCH assistant/research/topics/{id}`。
func (s *Server) UpdateResearchTopic(w http.ResponseWriter, r *http.Request, id string) {
	svc, ok := s.researchSvc(w)
	if !ok {
		return
	}
	var in apigen.ResearchTopicPatch
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := svc.UpdateTopic(r.Context(), id, in)
	if err != nil {
		writeResearchErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// DeleteResearchTopic 对应 `DELETE assistant/research/topics/{id}`：置为 dismissed。
func (s *Server) DeleteResearchTopic(w http.ResponseWriter, r *http.Request, id string) {
	svc, ok := s.researchSvc(w)
	if !ok {
		return
	}
	if err := svc.DismissTopic(r.Context(), id); err != nil {
		writeResearchErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunResearchTopic 对应 `POST assistant/research/topics/{id}/run`。
func (s *Server) RunResearchTopic(w http.ResponseWriter, r *http.Request, id string) {
	svc, ok := s.researchSvc(w)
	if !ok {
		return
	}
	out, err := svc.RunTopicNow(r.Context(), id)
	if err != nil {
		writeResearchErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// ListResearchRuns 对应 `GET assistant/research/runs`。
func (s *Server) ListResearchRuns(w http.ResponseWriter, r *http.Request, params apigen.ListResearchRunsParams) {
	svc, ok := s.researchSvc(w)
	if !ok {
		return
	}
	limit := 50
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 200 {
			WriteError(w, http.StatusBadRequest, "invalid_request", "limit must be 1-200")
			return
		}
		limit = *params.Limit
	}
	runs, err := svc.ListRuns(r.Context(), limit)
	if err != nil {
		writeResearchErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.ResearchRunList{Runs: runs})
}
