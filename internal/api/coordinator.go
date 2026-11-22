package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	// M19：助手的电脑在等待时长内没有就绪；电脑的 orb 状态不允许该操作、已删除、orbd 过旧或未就绪
	coord.CodeComputerStarting: http.StatusConflict,
	"invalid_state":            http.StatusConflict,
	"orb_gone":                 http.StatusConflict,
	"orbd_outdated":            http.StatusConflict,
	"orb_not_ready":            http.StatusConflict,
	// M26：列出 MCP 服务器的工具：SSE 服务器不支持；连接上游失败
	coord.CodeUnsupportedTransport: http.StatusBadRequest,
	coord.CodeUpstreamFailed:       http.StatusBadGateway,
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

// CreateAssistantConversation 对应 `POST assistant/conversations`：新建一个全局助手对话。请求体可省略；
// 给了 prompt / files / images 时它们是新对话的第一条用户消息（files 绑定到新对话）。
func (s *Server) CreateAssistantConversation(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.coordSvc(w)
	if !ok {
		return
	}
	var req apigen.CreateAssistantConversationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxThreadBody)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		WriteError(w, http.StatusBadRequest, "invalid_request", "请求体不是有效 JSON: "+err.Error())
		return
	}
	p := threads.CoordinatorParams{Images: req.Images, Files: req.Files}
	if req.Prompt != nil {
		p.Prompt = *req.Prompt
	}
	if req.Model != nil && req.Model.Provider != "" && req.Model.ID != "" {
		p.Model = req.Model.Provider + "/" + req.Model.ID
	}
	if req.ThinkingLevel != nil {
		p.ThinkingLevel = *req.ThinkingLevel
	}
	t, err := svc.CreateConversation(r.Context(), p)
	if err != nil {
		writeCoordErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, apigen.ThreadEnvelope{Thread: t})
}
