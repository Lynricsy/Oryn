package pushgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apns"
	"github.com/Lynricsy/Oryn/internal/fcm"
	"github.com/Lynricsy/Oryn/internal/pushproto"
)

const (
	// sendTimeout 是单次上游发送的时限。
	sendTimeout = 15 * time.Second
	// challengeFCMTTL 是 challenge 推送在 FCM 的离线保留时长（与 challenge 有效期相同）。
	challengeFCMTTL = 5 * time.Minute
)

// APNs 的提示文案：明文只有固定占位，真实内容在 e 里，由 Notification Service Extension 解密后替换。
const (
	apnsTitle          = "Oryn"
	apnsChallengeBody  = "Verifying push notifications"
	apnsNotifyBody     = "New notification"
	apnsPushTypeAlert  = "alert"
	apnsPriorityAlways = 10
)

// outcome 是一次上游发送的归类。
type outcome int

const (
	sentOK outcome = iota
	// sentUnregistered：令牌已失效（FCM UNREGISTERED；APNs 410 / BadDeviceToken）。
	sentUnregistered
	// sentMismatch：令牌不属于本 App（FCM SENDER_ID_MISMATCH；APNs DeviceTokenNotForTopic）。
	sentMismatch
	// sentUnavailable：该通道当前未配置（运维关掉了它），不归咎于令牌。
	sentUnavailable
	// sentFailed：其它失败（拒绝或不可达）。
	sentFailed
)

// sendResult 是上游发送的结果；code 是可以写进日志的上游错误码（不含令牌）。
type sendResult struct {
	outcome    outcome
	code       string
	retryAfter time.Duration
}

// upstream 持有 FCM 与 APNs 的出站客户端；未配置的通道为 nil。
type upstream struct {
	fcm     *fcm.Client
	account *fcm.Account
	apns    *apns.Client
	topic   string
	now     func() time.Time
}

// newUpstream 读取凭据文件并构造客户端。
func newUpstream(cfg Config, o Options, now func() time.Time) (*upstream, error) {
	u := &upstream{now: now}
	if cfg.FCM.Enabled() {
		raw, err := os.ReadFile(cfg.FCM.ServiceAccountFile)
		if err != nil {
			return nil, fmt.Errorf("读取 FCM 服务账号: %w", err)
		}
		if u.account, err = fcm.ParseAccount(raw); err != nil {
			return nil, fmt.Errorf("%s: %w", cfg.FCM.ServiceAccountFile, err)
		}
		hc := o.FCMHTTP
		if hc == nil {
			hc = &http.Client{Timeout: sendTimeout, CheckRedirect: noRedirect}
		}
		u.fcm = &fcm.Client{HTTP: hc, TokenURL: o.FCMTokenURL, APIBase: o.FCMAPIBase, Now: now}
	}
	if cfg.APNs.Enabled() {
		raw, err := os.ReadFile(cfg.APNs.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("读取 APNs key: %w", err)
		}
		key, err := apns.ParseKey(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", cfg.APNs.KeyFile, err)
		}
		hc := o.APNsHTTP
		if hc == nil {
			// APNs 只接受 HTTP/2
			tr := http.DefaultTransport.(*http.Transport).Clone()
			tr.Protocols = new(http.Protocols)
			tr.Protocols.SetHTTP2(true)
			hc = &http.Client{Transport: tr, Timeout: sendTimeout, CheckRedirect: noRedirect}
		}
		u.apns = &apns.Client{
			HTTP: hc, Key: key, KeyID: cfg.APNs.KeyID, TeamID: cfg.APNs.TeamID,
			ProductionURL: o.APNsProductionURL, SandboxURL: o.APNsSandboxURL, Now: now,
		}
		u.topic = cfg.APNs.Topic
	}
	return u, nil
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// sendChallenge 把 challenge 推给它的令牌。
func (u *upstream) sendChallenge(ctx context.Context, ch *challenge) sendResult {
	switch ch.channel {
	case pushproto.ChannelFCM:
		return u.sendFCM(ctx, fcm.Message{
			Token: ch.token, High: true, TTL: challengeFCMTTL,
			Data: map[string]string{"kind": pushproto.ChallengeKind, "challengeId": ch.id, "nonce": ch.nonce},
		})
	default:
		payload, err := json.Marshal(map[string]any{
			"aps":  map[string]any{"alert": map[string]string{"title": apnsTitle, "body": apnsChallengeBody}},
			"kind": pushproto.ChallengeKind, "challengeId": ch.id, "nonce": ch.nonce,
		})
		if err != nil {
			return sendResult{outcome: sentFailed, code: "encode"}
		}
		// expiration 为零：只尝试一次、不离线存储
		return u.sendAPNs(ctx, apns.Notification{
			Token: ch.token, Environment: ch.env, Topic: u.topic,
			PushType: apnsPushTypeAlert, Priority: apnsPriorityAlways, Payload: payload,
		})
	}
}

// sendNotification 把一条加密通知推给授权的令牌。APNs 的 withdraw 由调用方直接回 202，不到这里。
func (u *upstream) sendNotification(ctx context.Context, g *Grant, req pushproto.PushRequest) sendResult {
	switch g.Channel {
	case pushproto.ChannelFCM:
		return u.sendFCM(ctx, fcm.Message{
			Token: g.Token, High: !req.Withdraw, TTL: pushproto.TTL,
			Data: map[string]string{"v": pushproto.DataVersion, "id": req.ID, "e": req.E},
		})
	default:
		payload, err := json.Marshal(map[string]any{
			"aps": map[string]any{
				"alert":           map[string]string{"title": apnsTitle, "body": apnsNotifyBody},
				"mutable-content": 1,
				"sound":           "default",
			},
			"v": pushproto.DataVersion, "id": req.ID, "e": req.E,
		})
		if err != nil {
			return sendResult{outcome: sentFailed, code: "encode"}
		}
		return u.sendAPNs(ctx, apns.Notification{
			Token: g.Token, Environment: g.Environment, Topic: u.topic,
			PushType: apnsPushTypeAlert, Priority: apnsPriorityAlways,
			Expiration: u.now().Add(pushproto.TTL), CollapseID: req.Collapse, Payload: payload,
		})
	}
}

func (u *upstream) sendFCM(ctx context.Context, m fcm.Message) sendResult {
	if u.fcm == nil {
		return sendResult{outcome: sentUnavailable, code: "fcm_disabled"}
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	r := u.fcm.Send(ctx, u.account, m)
	if r.Err != nil && strings.HasPrefix(r.Err.Error(), fcmRejectedAccess) {
		// access token 被拒：Send 已作废缓存，重新换取后重试一次
		r = u.fcm.Send(ctx, u.account, m)
	}
	switch {
	case r.Err == nil:
		return sendResult{outcome: sentOK}
	case r.Unregistered:
		return sendResult{outcome: sentUnregistered, code: "UNREGISTERED"}
	}
	code := fcmCode(r.Err)
	if code == "SENDER_ID_MISMATCH" {
		return sendResult{outcome: sentMismatch, code: code}
	}
	return sendResult{outcome: sentFailed, code: code, retryAfter: r.RetryAfter}
}

// fcm.Result 只单列 UNREGISTERED，其它错误码从错误文本里取（internal/fcm 的固定格式）。
const (
	fcmRejectedAccess = "fcm rejected access token"
	fcmOAuth          = "fcm oauth token"
)

// fcmCode 是 FCM 错误里可以写进日志的部分：FCM 错误码（"fcm <errorCode> (HTTP <status>)"），
// 或令牌端点 / HTTP 状态 / 网络错误的概述。错误体原文不进日志。
func fcmCode(err error) string {
	s := err.Error()
	switch {
	case strings.HasPrefix(s, fcmOAuth):
		return "oauth_token"
	case strings.HasPrefix(s, fcmRejectedAccess):
		return "UNAUTHENTICATED"
	case strings.HasPrefix(s, "fcm "):
		if code, _, ok := strings.Cut(strings.TrimPrefix(s, "fcm "), " (HTTP "); ok {
			return code
		}
	case strings.HasPrefix(s, "HTTP "):
		code, _, _ := strings.Cut(s, ":")
		return code
	}
	var te interface{ Timeout() bool }
	if errors.As(err, &te) && te.Timeout() {
		return "timeout"
	}
	return "network"
}

func (u *upstream) sendAPNs(ctx context.Context, n apns.Notification) sendResult {
	if u.apns == nil {
		return sendResult{outcome: sentUnavailable, code: "apns_disabled"}
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	r := u.apns.Send(ctx, n)
	code := r.Reason
	if code == "" && r.Status != 0 {
		code = fmt.Sprintf("HTTP %d", r.Status)
	}
	switch {
	case r.Err == nil:
		return sendResult{outcome: sentOK}
	case r.Unregistered:
		return sendResult{outcome: sentUnregistered, code: code}
	case r.TopicMismatch:
		return sendResult{outcome: sentMismatch, code: code}
	case r.Status == 0:
		code = "network"
	}
	return sendResult{outcome: sentFailed, code: code, retryAfter: r.RetryAfter}
}
