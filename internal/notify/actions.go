package notify

// 推送通知上的一触作答：可点一个选项作答的 input（单选 select / confirm / 单题单选 ask，2–3 个短选项）在
// Web Push 与 FCM 载荷里带 `actions`，设备点按钮后调 `POST /api/v1/notifications/{id}/answer`（Answer）。
// 请求在别处作答或取消后，向已收到带按钮推送的订阅发撤回消息（withdrawPayload），设备据此撤掉过期按钮。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/uiprompt"
)

const (
	minPushActions = 2
	maxPushActions = 3
	// maxActionLabel 是按钮文字的字符上限：更长的选项在通知按钮上会被系统截断，宁可不给按钮。
	maxActionLabel = 24
)

// pushAction 是推送载荷里的一个操作按钮：I 是选项序号（作答时原样回传），Role 只用于 confirm（yes / no），
// 客户端据此按界面语言显示「是 / 否」，其余按钮显示 Label 原文。
type pushAction struct {
	I     int    `json:"i"`
	Label string `json:"label"`
	Role  string `json:"role,omitempty"`
}

var (
	// errNotAnswerable 是 422 not_answerable：通知不是能用按钮作答的待答请求。
	errNotAnswerable = &Error{Status: http.StatusUnprocessableEntity, Code: "not_answerable", Message: "notification is not a question answerable with one tap"}
	// errInputResolved 是 409 input_resolved：请求已在别处作答或已取消。
	errInputResolved = &Error{Status: http.StatusConflict, Code: "input_resolved", Message: "request already answered elsewhere"}
	// errInvalidOption 是 400 invalid_option：选项序号越界。
	errInvalidOption = &Error{Status: http.StatusBadRequest, Code: "invalid_option", Message: "option out of range"}
)

// quickChoice 判断请求能否用通知按钮作答：uiprompt.SingleChoice 认可、2–3 个选项、每个选项是不超过
// maxActionLabel 个字符的单行文字。
func quickChoice(frame json.RawMessage) (uiprompt.Choice, bool) {
	c, ok := uiprompt.SingleChoice(frame, uiprompt.Labels{})
	if !ok || c.Text == "" || len(c.Options) < minPushActions || len(c.Options) > maxPushActions {
		return uiprompt.Choice{}, false
	}
	for _, o := range c.Options {
		if strings.TrimSpace(o) == "" || utf8Len(o) > maxActionLabel || strings.ContainsAny(o, "\r\n") {
			return uiprompt.Choice{}, false
		}
	}
	return c, true
}

// pushActions 返回推送应带的按钮与随按钮一起显示的完整问题。只有未解决、属于线程、可一触作答，且完整问题
// 不超过推送正文上限（maxPushBody）时才给按钮：通知正文必须完整说明要批准的内容（如工具审批的命令），
// 会被截断时只发普通通知、点按打开线程。
func pushActions(m Message) ([]pushAction, string, bool) {
	if m.Kind != KindInput || m.Resolved || m.ThreadID == "" || len(m.Request) == 0 {
		return nil, "", false
	}
	c, ok := quickChoice(m.Request)
	if !ok || utf8Len(c.Text) > maxPushBody {
		return nil, "", false
	}
	acts := make([]pushAction, len(c.Options))
	for i, o := range c.Options {
		acts[i] = pushAction{I: i, Label: o}
		if c.Confirm {
			acts[i].Role = "yes"
			if i == 1 {
				acts[i].Role = "no"
			}
		}
	}
	return acts, c.Text, true
}

// withdrawPayload 是撤回消息 `{"v":1,"kind":"withdraw","id","tag"}`：设备只在 tag 上仍显示着同一 id 的通知时撤掉它，
// 同一线程后来的通知不受影响。
func withdrawPayload(notificationID, tag string) ([]byte, error) {
	return json.Marshal(map[string]any{"v": 1, "kind": "withdraw", "id": notificationID, "tag": tag})
}

// applePush 判断 Web Push 端点是否属于 Apple（Safari）：Safari 不显示操作按钮，且每条推送都必须弹出通知。
func applePush(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "push.apple.com" || strings.HasSuffix(h, ".push.apple.com")
}

// Answer 以第 option 个选项（从 0 起）作答一条 input 通知对应的待答请求（推送通知上的操作按钮）。
// 通知不存在 → 404 not_found；不是可一触作答的 input → 422 not_answerable；序号越界 → 400 invalid_option；
// 已在别处作答或取消 → 409 input_resolved；线程服务的其它错误原样返回。成功时返回请求所在的线程 id。
func (s *Service) Answer(ctx context.Context, id string, option int) (string, error) {
	n, err := s.st.Q.GetNotification(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if n.Kind != KindInput || n.ThreadID == nil || n.Request == nil {
		return "", errNotAnswerable
	}
	frame := json.RawMessage(*n.Request)
	c, ok := quickChoice(frame)
	if !ok {
		return "", errNotAnswerable
	}
	if n.ResolvedAt != nil {
		return "", errInputResolved
	}
	if option < 0 || option >= len(c.Options) {
		return "", errInvalidOption
	}
	resp, err := uiprompt.AnswerOption(frame, option)
	if err != nil {
		return "", errNotAnswerable
	}
	switch err := s.th.UIResponse(ctx, *n.ThreadID, resp); {
	case err == nil:
		return *n.ThreadID, nil
	case errors.Is(err, threads.ErrUIResolved):
		return "", errInputResolved
	default:
		return "", err
	}
}

// withdraw 向已收到某条带按钮推送的订阅发撤回消息（请求已作答 / 取消）。尽力而为、不重试：撤回没送到时，
// 设备上过期的按钮作答会得到 409，提示「已在别处作答」。没带按钮的推送不撤回（Web Push 的静默推送有配额）。
func (s *Service) withdraw(ctx context.Context, notificationID string) {
	n, err := s.st.Q.GetNotification(ctx, notificationID)
	if err != nil {
		return
	}
	m := s.message(n, db.NotificationDelivery{})
	m.Resolved = false // 按发送时的样子判断那条推送是否带了按钮
	if _, _, ok := pushActions(m); !ok {
		return
	}
	dels, err := s.st.Q.ListDeliveriesByNotifications(ctx, []string{notificationID})
	if err != nil {
		slog.Warn("notify: 读取投递失败", "notification", notificationID, "err", err)
		return
	}
	m.Withdraw = true
	for _, d := range dels {
		if d.SubscriptionID == nil || d.Status != statusSent {
			continue
		}
		sub, err := s.st.Q.GetDeliverablePushSubscription(ctx, *d.SubscriptionID)
		if err != nil {
			continue
		}
		mk := s.pushSenders[sub.Kind]
		if mk == nil {
			continue
		}
		m.DeliveryID = d.ID
		s.sendWithdraw(ctx, mk(sub), m, sub.ID)
	}
}

// withdrawIfResolved 在一条带按钮的推送刚送出后复查请求：发送期间已作答 / 取消（resolve 那时投递还没记成 sent，
// 不会撤回它）则补发撤回。
func (s *Service) withdrawIfResolved(ctx context.Context, sender Sender, m Message) {
	if _, _, ok := pushActions(m); !ok {
		return
	}
	n, err := s.st.Q.GetNotification(ctx, m.NotificationID)
	if err != nil || n.ResolvedAt == nil {
		return
	}
	m.Withdraw = true
	s.sendWithdraw(ctx, sender, m, "")
}

func (s *Service) sendWithdraw(ctx context.Context, sender Sender, m Message, subID string) {
	r := sender.Send(ctx, m)
	if r.Err == nil {
		return
	}
	if r.Gone && subID != "" {
		if _, err := s.st.Q.DeletePushSubscription(ctx, subID); err != nil {
			slog.Warn("notify: 删除失效推送订阅失败", "subscription", subID, "err", err)
		}
		return
	}
	slog.Info("notify: 撤回推送失败", "notification", m.NotificationID, "err", errorText(r.Err))
}
