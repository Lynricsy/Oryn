package notify

// 官方推送网关（M28，见 docs/protocol.md「官方推送网关」）：iOS 包与 Android `gms` 包经网关登记授权后，以
// `kind: gateway` 登记 `{gateway, grant, keys}`。orynd 把与 Web Push 相同的 RFC 8291 aes128gcm 密文（base64url）
// 以主机签名 `POST /v1/push` 交给网关，网关转给 APNs / FCM，只见密文。主机密钥是 <data_dir>/push-gateway.key
// （Ed25519），启用网关时在启动期生成。订阅的 endpoint 是 `<origin>/v1/grants/<grant>`：origin 变了（改配置）
// 的订阅在下一次投递时删除，绝不改发别的网关。

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/pushproto"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/webpush"
)

// GatewayConfig 是官方推送网关参数。
type GatewayConfig struct {
	// Origin 是网关 origin（`push.gateway.url`，不带路径、无尾斜杠）；空 = 关闭网关（不登记、不投递 gateway 订阅）。
	Origin string
}

const (
	// kindGateway 是网关订阅的 kind（也是其投递的 target）。
	kindGateway = "gateway"
	// gatewayInfoVersion 是 GET /api/v1/push/gateway 的 v。
	gatewayInfoVersion = 1
	// maxGatewayOrigin 是登记请求里 gateway 字段的长度上限（与 openapi 一致）。
	maxGatewayOrigin = 255
	// maxGatewayReply 是读取网关应答体的上限（错误体只有 {code, message}）。
	maxGatewayReply = 4 << 10
)

func (s *Service) gatewayEnabled() bool { return s.cfg.Gateway.Origin != "" }

// errGatewayDisabled 是网关关闭时的错误：GET 网关信息为 404，登记订阅为 409。
func errGatewayDisabled(status int) error {
	return &Error{Status: status, Code: "gateway_disabled", Message: "the push gateway is disabled on this server (push.gateway.url is empty)"}
}

// GatewayInfo 返回 GET /api/v1/push/gateway 的应答：网关 origin 与本机主机公钥。
func (s *Service) GatewayInfo() (apigen.PushGateway, error) {
	if !s.gatewayEnabled() {
		return apigen.PushGateway{}, errGatewayDisabled(http.StatusNotFound)
	}
	return apigen.PushGateway{V: gatewayInfoVersion, Origin: s.cfg.Gateway.Origin, HostKey: pushproto.HostKeyString(s.hostKey.Public().(ed25519.PublicKey))}, nil
}

// gatewayEndpoint 是授权 grant 在当前网关上的订阅 endpoint。
func (s *Service) gatewayEndpoint(grant string) string {
	return s.cfg.Gateway.Origin + pushproto.GrantPath(grant)
}

// createGatewaySubscription 校验 `{gateway, grant, keys}`，以主机签名向网关核实授权后按 endpoint upsert 一条 gateway 订阅
// （p256dh / auth 存接收端密钥：没有它无法加密，而网关路径绝不发明文）。
func (s *Service) createGatewaySubscription(ctx context.Context, in apigen.PushSubscriptionInput, owner Owner, userAgent string) (apigen.PushSubscription, error) {
	if in.Endpoint != nil {
		return apigen.PushSubscription{}, errPushInvalid("kind gateway takes gateway, grant and keys, not endpoint")
	}
	if in.Gateway == nil || *in.Gateway == "" || len(*in.Gateway) > maxGatewayOrigin {
		return apigen.PushSubscription{}, errPushInvalid("gateway must be the push gateway origin")
	}
	if in.Grant == nil || !pushproto.IDPattern.MatchString(*in.Grant) {
		return apigen.PushSubscription{}, errPushInvalid("grant must match ^[A-Za-z0-9_-]{22}$")
	}
	if in.Keys == nil {
		return apigen.PushSubscription{}, errPushInvalid("kind gateway requires keys (p256dh, auth) for payload encryption")
	}
	p256dh, auth, err := parsePushKeys(in.Keys)
	if err != nil {
		return apigen.PushSubscription{}, err
	}
	if !s.gatewayEnabled() {
		return apigen.PushSubscription{}, errGatewayDisabled(http.StatusConflict)
	}
	if *in.Gateway != s.cfg.Gateway.Origin {
		return apigen.PushSubscription{}, &Error{Status: http.StatusConflict, Code: "gateway_changed",
			Message: fmt.Sprintf("this server uses the push gateway %s; authorize again", s.cfg.Gateway.Origin)}
	}
	if err := s.verifyGrant(ctx, *in.Grant); err != nil {
		return apigen.PushSubscription{}, err
	}
	endpoint := s.gatewayEndpoint(*in.Grant)
	if err := s.checkEndpointKind(ctx, endpoint, kindGateway); err != nil {
		return apigen.PushSubscription{}, err
	}
	row, err := s.st.Q.UpsertPushSubscription(ctx, db.UpsertPushSubscriptionParams{
		ID: ids.New("psub"), Kind: kindGateway, Endpoint: endpoint, P256dh: p256dh, Auth: auth,
		WebSessionHash: owner.WebSessionHash, DeviceID: owner.DeviceID, UserAgent: truncate(userAgent, 300), CreatedAt: s.now(),
	})
	if err != nil {
		return apigen.PushSubscription{}, err
	}
	return subscriptionAPI(row), nil
}

// checkEndpointKind 拒绝与另一种 kind 的已有订阅同 endpoint 的登记：upsert 只覆盖密钥与归属、不改 kind，
// 否则一条 Web Push 登记就能换掉网关订阅的接收端密钥（反之亦然）。
func (s *Service) checkEndpointKind(ctx context.Context, endpoint, kind string) error {
	row, err := s.st.Q.GetPushSubscriptionByEndpoint(ctx, endpoint)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	case row.Kind != kind:
		return errPushInvalid("endpoint collides with an existing subscription of another kind")
	}
	return nil
}

// verifyGrant 以主机签名调 `POST /v1/grants/verify`：204 通过；410 / 403 → 409 gateway_grant_invalid；
// 网络错误、5xx 及其它应答 → 502 gateway_unreachable（401 另告警检查时钟）。
func (s *Service) verifyGrant(ctx context.Context, grant string) error {
	resp, err := s.gatewayPost(ctx, pushproto.PathVerify, pushproto.VerifyRequest{Grant: grant})
	if err != nil {
		return &Error{Status: http.StatusBadGateway, Code: "gateway_unreachable", Message: errorText(err)}
	}
	switch {
	case resp.status >= 200 && resp.status < 300:
		return nil
	case resp.status == http.StatusGone || resp.status == http.StatusForbidden:
		return &Error{Status: http.StatusConflict, Code: "gateway_grant_invalid", Message: "the push gateway rejected the grant: " + resp.String()}
	case resp.status == http.StatusUnauthorized:
		warnGatewaySignature(resp)
	}
	return &Error{Status: http.StatusBadGateway, Code: "gateway_unreachable", Message: "the push gateway failed: " + resp.String()}
}

// gatewayReply 是网关应答的状态、错误体与 Retry-After。
type gatewayReply struct {
	status int
	code   string
	header http.Header
}

func (r gatewayReply) String() string {
	if r.code == "" {
		return fmt.Sprintf("HTTP %d", r.status)
	}
	return fmt.Sprintf("%s (HTTP %d)", r.code, r.status)
}

// gatewayPost 以主机签名向网关 POST 一个 JSON 请求体；err 只表示没有得到应答（URL 已脱敏）。
func (s *Service) gatewayPost(ctx context.Context, path string, v any) (gatewayReply, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return gatewayReply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Gateway.Origin+path, bytes.NewReader(body))
	if err != nil {
		return gatewayReply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	pushproto.SignHost(req, s.cfg.Gateway.Origin, s.hostKey, s.cfg.Now(), body)
	resp, err := s.gatewayHTTP.Do(req)
	if err != nil {
		return gatewayReply{}, redactURLError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out := gatewayReply{status: resp.StatusCode, header: resp.Header}
	if resp.StatusCode >= 300 {
		var e pushproto.ErrorBody
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxGatewayReply))
		if json.Unmarshal(raw, &e) == nil {
			out.code = truncate(e.Code, 64)
		}
	}
	return out, nil
}

// warnGatewaySignature 告警网关拒绝了主机签名：多半是本机时钟偏差（clock_skew）。
func warnGatewaySignature(r gatewayReply) {
	slog.Warn("notify: 推送网关拒绝了主机签名，请检查本机时钟（NTP）", "code", r.code, "status", r.status)
}

type gatewaySender struct {
	s   *Service
	sub db.PushSubscription
}

func (s *Service) gatewaySender(sub db.PushSubscription) Sender { return gatewaySender{s, sub} }

// errGatewayChanged 是 origin 已变的订阅的投递错误（写入投递的 last_error）。
var errGatewayChanged = errors.New("push gateway changed; subscription dropped")

// Send 加密载荷（明文上限 pushproto.MaxPlaintext，超出时去掉正文）后以主机签名 `POST /v1/push`。
// 非撤回消息带 collapse（按线程，网关看不出线程）；撤回带 withdraw、不带 collapse，免得顶掉同线程尚未送达的通知。
func (g gatewaySender) Send(ctx context.Context, m Message) Result {
	grant, ok := strings.CutPrefix(g.sub.Endpoint, g.s.cfg.Gateway.Origin+pushproto.PathGrants+"/")
	if !ok || !pushproto.IDPattern.MatchString(grant) {
		// 登记于另一个网关（push.gateway.url 改过）：删除订阅，App 下次同步时向新网关重新授权
		slog.Info("notify: 删除不属于当前推送网关的订阅", "subscription", g.sub.ID, "endpoint", schemeHost(g.sub.Endpoint))
		return Result{Gone: true, Err: errGatewayChanged}
	}
	payload, err := pushPayloadLimit(m, pushproto.MaxPlaintext)
	if err != nil {
		return Result{Err: err}
	}
	ct, err := webpush.Encrypt(payload, webpush.Subscription{P256dh: g.sub.P256dh, Auth: g.sub.Auth}, rand.Reader)
	if err != nil {
		return Result{Err: err}
	}
	req := pushproto.PushRequest{Grant: grant, ID: m.NotificationID, E: base64.RawURLEncoding.EncodeToString(ct)}
	if m.Withdraw {
		req.Withdraw = true
	} else {
		tag, _ := pushTag(m)
		req.Collapse = pushproto.Collapse(grant, tag)
	}
	resp, err := g.s.gatewayPost(ctx, pushproto.PathPush, req)
	if err != nil {
		return netResult(err)
	}
	return gatewayResult(resp)
}

// gatewayResult 按网关应答归类：2xx 成功；410 / 403 → 删除订阅；429 / 502 / 503 → 可重试并采信 Retry-After；
// 401 → 不重试并告警检查时钟；其余失败不重试。
func gatewayResult(r gatewayReply) Result {
	if r.status >= 200 && r.status < 300 {
		return Result{}
	}
	err := fmt.Errorf("push gateway %s", r)
	switch r.status {
	case http.StatusGone, http.StatusForbidden:
		return Result{Gone: true, Err: err}
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable:
		return Result{Retry: true, RetryAfter: retryAfter(r.header), Err: err}
	case http.StatusUnauthorized:
		warnGatewaySignature(r)
	}
	return Result{Err: err}
}
