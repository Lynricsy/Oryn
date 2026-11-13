package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Message 是交给发送方的一条消息。
type Message struct {
	// Kind 为 done / input / error，渠道测试为 test。
	NotificationID, DeliveryID, Kind, ThreadID string
	// URL = PublicURL + "/t/" + ThreadID（测试消息为 PublicURL）。
	Title, Body, URL string
	// Request 是 input 时的请求帧（extension_ui_request 或 plan_proposal）。
	Request json.RawMessage
	// Locale 是渠道语言（推送订阅为空）。
	Locale string
	// CreatedAt 是通知创建时间（毫秒）。
	CreatedAt int64
}

// Result 是一次发送的结果。
type Result struct {
	// Ref 是渠道侧引用（telegram 的 "<chat>:<message>"）。
	Ref string
	// Retry 表示可重试的失败。
	Retry      bool
	RetryAfter time.Duration
	// Gone 表示目标已失效（webpush 404 / 410）：删除订阅。
	Gone bool
	Err  error
}

// Sender 发送一条消息。
type Sender interface {
	Send(ctx context.Context, m Message) Result
}

const (
	statusPending = "pending"
	statusSent    = "sent"
	statusFailed  = "failed"

	maxAttempts  = 4
	expireAfter  = time.Hour
	maxErrorLen  = 500
	fallbackWake = 30 * time.Second
	batchSize    = 50
)

// backoff 是第 1 / 2 / 3 次失败后的重试间隔。
var backoff = []time.Duration{10 * time.Second, 60 * time.Second, 5 * time.Minute}

func (s *Service) registerSenders() {
	s.senders = map[string]func(db.NotificationChannel, []byte) (Sender, error){
		"webhook":  s.webhookSender,
		"ntfy":     s.ntfySender,
		"bark":     s.barkSender,
		"telegram": s.telegramSender,
	}
	s.pushSenders = map[string]func(db.PushSubscription) Sender{
		"webpush": s.webpushSender,
	}
}

func (s *Service) deliverLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		n := s.DeliverDue(ctx)
		if ctx.Err() != nil {
			return
		}
		if n >= batchSize {
			continue
		}
		wait := fallbackWake
		if next, err := s.st.Q.GetNextNotificationDeliveryAt(ctx); err == nil && next > 0 {
			wait = min(wait, max(time.Duration(next-s.now())*time.Millisecond, 0))
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-s.deliverWake:
		case <-timer.C:
		}
	}
}

// DeliverDue 处理一批到期的待投递（≤50 条），返回处理条数。
func (s *Service) DeliverDue(ctx context.Context) int {
	now := s.now()
	due, err := s.st.Q.ListDueNotificationDeliveries(ctx, now)
	if err != nil {
		slog.Warn("notify: 读取待投递失败", "err", err)
		return 0
	}
	var subs map[string]db.PushSubscription
	for _, d := range due {
		if ctx.Err() != nil {
			return 0
		}
		n, err := s.st.Q.GetNotification(ctx, d.NotificationID)
		if err != nil {
			continue // 通知已被删除（投递行随之级联删除）
		}
		if now-n.CreatedAt > expireAfter.Milliseconds() {
			s.finish(ctx, d, statusFailed, d.Attempts, "expired", "")
			continue
		}
		m := s.message(n, d)
		var sender Sender
		switch {
		case d.ChannelID != nil:
			ch, err := s.st.Q.GetNotificationChannel(ctx, *d.ChannelID)
			if err != nil {
				continue
			}
			if !ch.Enabled {
				s.finish(ctx, d, statusFailed, d.Attempts, "channel disabled", "")
				continue
			}
			m.Locale = ch.Locale
			sender, err = s.channelSender(ch)
			if err != nil {
				s.applyResult(ctx, d, Result{Err: err})
				continue
			}
		case d.SubscriptionID != nil:
			if subs == nil {
				subs = map[string]db.PushSubscription{}
				all, err := s.st.Q.ListPushSubscriptions(ctx)
				if err != nil {
					continue
				}
				for _, sub := range all {
					subs[sub.ID] = sub
				}
			}
			sub, ok := subs[*d.SubscriptionID]
			mk := s.pushSenders[sub.Kind]
			if !ok || mk == nil {
				s.finish(ctx, d, statusFailed, d.Attempts, "unsupported subscription", "")
				continue
			}
			sender = mk(sub)
		default:
			continue
		}
		s.applyResult(ctx, d, sender.Send(ctx, m))
	}
	return len(due)
}

func (s *Service) message(n db.Notification, d db.NotificationDelivery) Message {
	m := Message{NotificationID: n.ID, DeliveryID: d.ID, Kind: n.Kind, Title: n.Title, Body: n.Body, CreatedAt: n.CreatedAt}
	if n.ThreadID != nil {
		m.ThreadID = *n.ThreadID
		m.URL = s.threadURL(m.ThreadID)
	}
	if n.Request != nil {
		m.Request = json.RawMessage(*n.Request)
	}
	return m
}

func (s *Service) threadURL(threadID string) string {
	return strings.TrimRight(s.cfg.PublicURL, "/") + "/t/" + threadID
}

// channelSender 解密渠道配置并构造发送方。
func (s *Service) channelSender(ch db.NotificationChannel) (Sender, error) {
	mk := s.senders[ch.Kind]
	if mk == nil {
		return nil, fmt.Errorf("unknown channel kind %q", ch.Kind)
	}
	cfg, err := s.openConfig(ch)
	if err != nil {
		return nil, err
	}
	return mk(ch, cfg)
}

// applyResult 按发送结果推进投递：成功 → sent；可重试且未用尽 → 按退避重排；否则 → failed。
func (s *Service) applyResult(ctx context.Context, d db.NotificationDelivery, r Result) {
	attempts := d.Attempts + 1
	now := s.now()
	if r.Err == nil {
		s.finish(ctx, d, statusSent, attempts, "", r.Ref)
		if d.ChannelID != nil {
			_ = s.st.Q.SetNotificationChannelError(ctx, db.SetNotificationChannelErrorParams{ID: *d.ChannelID})
		}
		if d.SubscriptionID != nil {
			_ = s.st.Q.SetPushSubscriptionResult(ctx, db.SetPushSubscriptionResultParams{LastSuccessAt: &now, ID: *d.SubscriptionID})
		}
		return
	}
	msg := truncate(r.Err.Error(), maxErrorLen)
	if d.ChannelID != nil {
		_ = s.st.Q.SetNotificationChannelError(ctx, db.SetNotificationChannelErrorParams{LastError: &msg, ID: *d.ChannelID})
	}
	if d.SubscriptionID != nil {
		if r.Gone {
			s.finish(ctx, d, statusFailed, attempts, msg, "")
			// 订阅已失效：删除（其投递行随外键级联删除）
			if _, err := s.st.Q.DeletePushSubscription(ctx, *d.SubscriptionID); err != nil {
				slog.Warn("notify: 删除失效推送订阅失败", "subscription", *d.SubscriptionID, "err", err)
			}
			return
		}
		_ = s.st.Q.SetPushSubscriptionResult(ctx, db.SetPushSubscriptionResultParams{LastError: &msg, ID: *d.SubscriptionID})
	}
	if r.Retry && attempts < maxAttempts {
		delay := backoff[attempts-1]
		if r.RetryAfter > delay {
			delay = min(r.RetryAfter, backoff[len(backoff)-1])
		}
		if err := s.st.Q.RescheduleNotificationDelivery(ctx, db.RescheduleNotificationDeliveryParams{
			Attempts: attempts, NextAttemptAt: now + delay.Milliseconds(), LastError: &msg, ID: d.ID,
		}); err != nil {
			slog.Warn("notify: 重排投递失败", "delivery", d.ID, "err", err)
		}
		return
	}
	s.finish(ctx, d, statusFailed, attempts, msg, "")
}

func (s *Service) finish(ctx context.Context, d db.NotificationDelivery, status string, attempts int64, errMsg, ref string) {
	p := db.FinishNotificationDeliveryParams{Status: status, Attempts: attempts, FinishedAt: s.now(), ID: d.ID}
	if errMsg != "" {
		p.LastError = &errMsg
	}
	if ref != "" {
		p.ExternalRef = &ref
	}
	if err := s.st.Q.FinishNotificationDelivery(ctx, p); err != nil {
		slog.Warn("notify: 写入投递结果失败", "delivery", d.ID, "err", err)
	}
}

// httpResult 按 HTTP 状态归类：2xx 成功；408、429、5xx 可重试（429 / 503 采信 Retry-After 秒数）；其余 4xx 不重试。
func httpResult(resp *http.Response, body []byte) Result {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return Result{}
	}
	err := fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(body)), 200))
	r := Result{Err: err}
	switch {
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		r.Retry = true
		if sec, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && sec > 0 {
			r.RetryAfter = time.Duration(sec) * time.Second
		}
	}
	return r
}

// netResult 是请求未得到响应（网络错误 / 超时）的结果：可重试。
func netResult(err error) Result {
	if errors.Is(err, context.Canceled) {
		return Result{Err: err}
	}
	return Result{Err: err, Retry: true}
}

// truncate 按字符截断到 n 个字符（超出时以 "…" 结尾）。
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
