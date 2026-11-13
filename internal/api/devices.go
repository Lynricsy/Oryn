package api

import (
	"net/http"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func deviceJSON(d db.Device, current string) apigen.Device {
	return apigen.Device{
		ID: d.ID, Name: d.Name, Kind: apigen.DeviceKind(d.Kind), Platform: d.Platform,
		CreatedAt: d.CreatedAt, LastSeenAt: d.LastSeenAt, RevokedAt: d.RevokedAt,
		Current: new(current != "" && d.ID == current),
	}
}

// DevicePairStart 发起设备配对（免鉴权）。
func (s *Server) DevicePairStart(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimit(w, r, auth.LimitPairStart) {
		return
	}
	var req apigen.DevicePairStartRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ps, err := auth.StartPairing(r.Context(), s.Store, req.Name, string(req.Kind), req.Platform,
		auth.Requester{IP: auth.ClientIP(r), UserAgent: r.UserAgent()})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.DevicePairStartResponse{
		Code: ps.Code, Verifier: ps.Verifier, ExpiresAt: ps.ExpiresAt.UnixMilli(), Interval: auth.PairInterval,
		// 不预填配对码：所有者须在批准页手动输入设备上显示的码（防一键钓鱼）
		VerifyURL: strings.TrimSuffix(s.Config.PublicURL, "/") + "/pair",
	})
}

// requireWeb 要求完整网页会话（不含设备换来的 App 会话：设备令牌不能再签发设备）；否则写出 401/403。
func requireWeb(w http.ResponseWriter, r *http.Request) bool {
	p, ok := principal(w, r)
	if !ok {
		return false
	}
	if p.Kind != auth.KindWeb || p.Recovery || p.DeviceID != "" {
		WriteError(w, http.StatusForbidden, "forbidden", "只能在已登录的网页上查看或批准配对")
		return false
	}
	return true
}

// GetDevicePairing 返回待批准配对请求的发起方信息（只接受完整网页会话）。
func (s *Server) GetDevicePairing(w http.ResponseWriter, r *http.Request, code string) {
	if !requireWeb(w, r) {
		return
	}
	p, err := auth.PendingPairing(r.Context(), s.Store, code)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.DevicePairing{
		Code: p.Code, Name: p.Name, Kind: apigen.DeviceKind(p.Kind), Platform: p.Platform,
		IP: p.RequesterIp, UserAgent: p.UserAgent, CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt,
	})
}

// DevicePairApprove 批准配对（只接受完整网页会话）。
func (s *Server) DevicePairApprove(w http.ResponseWriter, r *http.Request) {
	if !requireWeb(w, r) {
		return
	}
	var req apigen.DevicePairApproveRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	dev, err := auth.ApprovePairing(r.Context(), s.Store, req.Code)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, deviceJSON(dev, ""))
}

// DevicePairPoll 发起方轮询：未批准 202，批准后 200 并交付令牌（仅一次）。
func (s *Server) DevicePairPoll(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimit(w, r, auth.LimitPairPoll) {
		return
	}
	var req apigen.DevicePairPollRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	dev, tok, approved, err := auth.PollPairing(r.Context(), s.Store, req.Code, req.Verifier)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !approved {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.DevicePairPollResponse{DeviceToken: tok, Device: deviceJSON(dev, dev.ID)})
}

// ListDevices 列出全部设备（含已吊销）。
func (s *Server) ListDevices(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	cur := p.DeviceID
	if p.Kind == auth.KindDevice {
		cur = p.ID
	}
	rows, err := s.Store.Q.ListDevices(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := apigen.DeviceList{Devices: make([]apigen.Device, 0, len(rows))}
	for _, d := range rows {
		out.Devices = append(out.Devices, deviceJSON(d, cur))
	}
	WriteJSON(w, http.StatusOK, out)
}

// RevokeDevice 吊销设备（幂等），并通知实时通道关闭其连接。
func (s *Server) RevokeDevice(w http.ResponseWriter, r *http.Request, id apigen.DeviceIDPath) {
	if err := s.Auth.Service.RevokeDevice(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
