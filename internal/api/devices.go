package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

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
	var req apigen.DevicePairStartRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ps, err := auth.StartPairing(r.Context(), s.Store, req.Name, string(req.Kind), req.Platform)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.DevicePairStartResponse{
		Code: ps.Code, Verifier: ps.Verifier, ExpiresAt: ps.ExpiresAt.UnixMilli(), Interval: auth.PairInterval,
		VerifyURL: strings.TrimSuffix(s.Config.PublicURL, "/") + "/pair?code=" + ps.Code,
	})
}

// DevicePairApprove 批准配对（只接受完整网页会话）。
func (s *Server) DevicePairApprove(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != auth.KindWeb || p.Recovery {
		WriteError(w, http.StatusForbidden, "forbidden", "只能在已登录的网页上批准配对")
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
	cur := ""
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

// RevokeDevice 吊销设备（幂等）。
func (s *Server) RevokeDevice(w http.ResponseWriter, r *http.Request, id apigen.DeviceIDPath) {
	ctx := r.Context()
	if _, err := s.Store.Q.GetDevice(ctx, id); errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "设备不存在")
		return
	} else if err != nil {
		writeErr(w, r, err)
		return
	}
	if _, err := s.Store.Q.RevokeDevice(ctx, db.RevokeDeviceParams{RevokedAt: new(time.Now().UnixMilli()), ID: id}); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
