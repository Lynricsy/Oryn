package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/coord"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// CoordinatorHost 报告协调宿主的可用性与沙箱报告（*coordhost.Host 实现）。
type CoordinatorHost interface {
	Status(ctx context.Context) apigen.CoordinatorStatus
}

// CoordDeps 是 M9 协调领域（Space、全局助手对话、协调宿主状态、操作记录）的依赖。
type CoordDeps struct {
	Service *coord.Service
	// Host 为 nil 时 GET coordinator 回 available=false。
	Host CoordinatorHost
}

func (s *Server) coordSvc(w http.ResponseWriter) (*coord.Service, bool) {
	if s.Coord == nil || s.Coord.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "coordinator service not configured")
		return nil, false
	}
	return s.Coord.Service, true
}

// coordErrStatus 把协调层的错误码映射为 HTTP 状态。
var coordErrStatus = map[string]int{
	coord.CodeCoordinatorUnavailable: http.StatusConflict,
	coord.CodeSpaceNotFound:          http.StatusNotFound,
	coord.CodeSpaceNameTaken:         http.StatusConflict,
	coord.CodeInvalid:                http.StatusBadRequest,
	coord.CodeNotFound:               http.StatusNotFound,
}

// writeCoordErr 写出协调层的错误：*coord.Error 按码映射，线程服务的错误按线程映射，其余按通用映射。
func writeCoordErr(w http.ResponseWriter, r *http.Request, err error) {
	var ce *coord.Error
	if errors.As(err, &ce) {
		status, ok := coordErrStatus[ce.Code]
		if !ok {
			status = http.StatusBadRequest
		}
		WriteError(w, status, ce.Code, ce.Message)
		return
	}
	var te *threads.Error
	if errors.As(err, &te) {
		writeThreadErr(w, err)
		return
	}
	writeErr(w, r, err)
}

// GetCoordinator 对应 `GET coordinator`：协调宿主的可用性与沙箱报告。
func (s *Server) GetCoordinator(w http.ResponseWriter, r *http.Request) {
	if s.Coord == nil || s.Coord.Host == nil {
		reason := "coordinator host is not configured"
		WriteJSON(w, http.StatusOK, apigen.CoordinatorStatus{Available: false, Reason: &reason})
		return
	}
	WriteJSON(w, http.StatusOK, s.Coord.Host.Status(r.Context()))
}

// ListAutomationActions 对应 `GET automation/actions?thread=&limit=`。
func (s *Server) ListAutomationActions(w http.ResponseWriter, r *http.Request, params apigen.ListAutomationActionsParams) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	limit := 50
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 200 {
			WriteError(w, http.StatusBadRequest, "invalid_request", "limit 须在 1–200 之间")
			return
		}
		limit = *params.Limit
	}
	thread := ""
	if params.Thread != nil {
		thread = *params.Thread
	}
	actions, err := svc.Actions(r.Context(), thread, limit)
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.AutomationActionList{Actions: actions})
}

// CreateAssistantConversation 对应 `POST assistant/conversations`：新建一个全局助手对话。
func (s *Server) CreateAssistantConversation(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	t, err := svc.CreateConversation(r.Context())
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, apigen.ThreadEnvelope{Thread: t})
}
