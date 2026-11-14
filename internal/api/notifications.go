package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/notify"
)

// NotifyDeps 是通知、通知渠道与推送订阅接口的依赖（M5 步骤 3.6）。
type NotifyDeps struct {
	Service *notify.Service
}

func (s *Server) notifySvc(w http.ResponseWriter) (*notify.Service, bool) {
	if s.Notify == nil || s.Notify.Service == nil {
		WriteError(w, http.StatusNotImplemented, "not_implemented", "notification service not configured")
		return nil, false
	}
	return s.Notify.Service, true
}

// writeNotifyErr 写出通知服务的错误：*notify.Error 按其状态码，其余按通用映射。
func writeNotifyErr(w http.ResponseWriter, r *http.Request, err error) {
	var ne *notify.Error
	if errors.As(err, &ne) {
		WriteError(w, ne.Status, ne.Code, ne.Message)
		return
	}
	writeErr(w, r, err)
}

// ListNotifications 对应 `GET notifications`。
func (s *Server) ListNotifications(w http.ResponseWriter, r *http.Request, params apigen.ListNotificationsParams) {
	svc, ok := s.notifySvc(w)
	if !ok {
		return
	}
	limit, before := 0, int64(0)
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 100 {
			WriteError(w, http.StatusBadRequest, "invalid_request", "limit 须在 1..100")
			return
		}
		limit = *params.Limit
	}
	if params.Before != nil {
		before = *params.Before
	}
	list, err := svc.List(r.Context(), limit, before, params.Unread != nil && *params.Unread)
	if err != nil {
		writeNotifyErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, list)
}

// MarkNotificationsRead 对应 `POST notifications/read`：省略 threadId = 全部。
func (s *Server) MarkNotificationsRead(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.notifySvc(w)
	if !ok {
		return
	}
	var req apigen.NotificationsReadRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		WriteError(w, http.StatusBadRequest, "invalid_request", "请求体不是有效 JSON: "+err.Error())
		return
	}
	threadID := ""
	if req.ThreadID != nil {
		threadID = *req.ThreadID
	}
	if err := svc.MarkRead(r.Context(), threadID); err != nil {
		writeNotifyErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListNotificationChannels 对应 `GET notification-channels`。
func (s *Server) ListNotificationChannels(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.notifySvc(w)
	if !ok {
		return
	}
	chs, err := svc.ListChannels(r.Context())
	if err != nil {
		writeNotifyErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.NotificationChannelList{Channels: chs})
}

// CreateNotificationChannel 对应 `POST notification-channels`。
func (s *Server) CreateNotificationChannel(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.notifySvc(w)
	if !ok {
		return
	}
	var in apigen.NotificationChannelInput
	if !decodeJSON(w, r, &in) {
		return
	}
	ch, err := svc.CreateChannel(r.Context(), in)
	if err != nil {
		writeNotifyErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, ch)
}

// UpdateNotificationChannel 对应 `PATCH notification-channels/{id}`。
func (s *Server) UpdateNotificationChannel(w http.ResponseWriter, r *http.Request, id apigen.NotificationChannelIDPath) {
	svc, ok := s.notifySvc(w)
	if !ok {
		return
	}
	var p apigen.NotificationChannelPatch
	if !decodeJSON(w, r, &p) {
		return
	}
	ch, err := svc.UpdateChannel(r.Context(), id, p)
	if err != nil {
		writeNotifyErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, ch)
}

// DeleteNotificationChannel 对应 `DELETE notification-channels/{id}`。
func (s *Server) DeleteNotificationChannel(w http.ResponseWriter, r *http.Request, id apigen.NotificationChannelIDPath) {
	svc, ok := s.notifySvc(w)
	if !ok {
		return
	}
	if err := svc.DeleteChannel(r.Context(), id); err != nil {
		writeNotifyErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TestNotificationChannel 对应 `POST notification-channels/{id}/test`。
func (s *Server) TestNotificationChannel(w http.ResponseWriter, r *http.Request, id apigen.NotificationChannelIDPath) {
	svc, ok := s.notifySvc(w)
	if !ok {
		return
	}
	res, err := svc.TestChannel(r.Context(), id)
	if err != nil {
		writeNotifyErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, res)
}

// LinkTelegramChannel 对应 `POST notification-channels/{id}/telegram/link`。
func (s *Server) LinkTelegramChannel(w http.ResponseWriter, r *http.Request, id apigen.NotificationChannelIDPath) {
	svc, ok := s.notifySvc(w)
	if !ok {
		return
	}
	link, err := svc.LinkTelegram(r.Context(), id)
	if err != nil {
		writeNotifyErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, link)
}

// GetVapidKey 对应 `GET push/vapid-key`。
func (s *Server) GetVapidKey(w http.ResponseWriter, _ *http.Request) {
	svc, ok := s.notifySvc(w)
	if !ok {
		return
	}
	WriteJSON(w, http.StatusOK, apigen.VapidKey{PublicKey: svc.VapidKey()})
}

// ListPushSubscriptions 对应 `GET push/subscriptions`。
func (s *Server) ListPushSubscriptions(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.notifySvc(w)
	if !ok {
		return
	}
	subs, err := svc.ListPushSubscriptions(r.Context())
	if err != nil {
		writeNotifyErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, apigen.PushSubscriptionList{Subscriptions: subs})
}

// CreatePushSubscription 对应 `POST push/subscriptions`：按 endpoint upsert，记下当前主体（网页会话 / 设备）。
func (s *Server) CreatePushSubscription(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.notifySvc(w)
	if !ok {
		return
	}
	var in apigen.PushSubscriptionInput
	if !decodeJSON(w, r, &in) {
		return
	}
	var owner notify.Owner
	if p, ok := auth.FromContext(r.Context()); ok {
		switch p.Kind {
		case auth.KindWeb:
			owner.WebSessionHash = new(p.ID)
		case auth.KindDevice:
			owner.DeviceID = new(p.ID)
		}
	}
	sub, err := svc.CreatePushSubscription(r.Context(), in, owner, r.UserAgent())
	if err != nil {
		writeNotifyErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusCreated, sub)
}

// DeletePushSubscription 对应 `DELETE push/subscriptions/{id}`。
func (s *Server) DeletePushSubscription(w http.ResponseWriter, r *http.Request, id apigen.PushSubscriptionIDPath) {
	svc, ok := s.notifySvc(w)
	if !ok {
		return
	}
	if err := svc.DeletePushSubscription(r.Context(), id); err != nil {
		writeNotifyErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
