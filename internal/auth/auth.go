// Package auth 实现控制面鉴权：Passkey（WebAuthn）与密码 + TOTP 的初始化与登录、恢复码、网页会话、
// 设备配对与设备令牌、WebSocket 票据，以及保护 /api/v1 的中间件。
//
// 凭据形态：
//   - 网页会话：Cookie `__Host-oryn_session`（Path=/、无 Domain、HttpOnly、Secure、SameSite=Lax），30 天有效、滑动续期；
//     `__Host-` 前缀让浏览器拒绝任何带 Domain 的同名 Cookie，portal 子域无法从父域写入覆盖它；
//     变更类请求必须带头 `X-Oryn-CSRF: 1`。kind=recovery 的会话只能调用 `auth/setup/*`、`auth/password/setup/*`、`me`、`auth/logout`。
//   - 设备令牌：`Authorization: Bearer odt_…`，可逐台吊销。
//
// 库中只保存令牌与会话 id 的 SHA-256。
package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 常量。
const (
	// CookieName 是网页会话 Cookie；`__Host-` 前缀要求 Secure、Path=/ 且不得带 Domain。
	CookieName = "__Host-oryn_session"
	CSRFHeader = "X-Oryn-CSRF"

	// SessionTTL 是网页会话有效期（滑动续期）。
	SessionTTL = 30 * 24 * time.Hour
	// RecoveryTTL 是恢复码换来的一次性会话有效期。
	RecoveryTTL = 15 * time.Minute
	// sessionTouchEvery 控制滑动续期写库的频率，避免每个请求都写。
	sessionTouchEvery = time.Hour
	// deviceTouchEvery 控制设备 last_seen_at 的更新频率。
	deviceTouchEvery = time.Minute
	// reauthTTL 是一次验证（Passkey 或密码 + TOTP 的登录、注册或 reauth）对敏感操作的有效期，从验证时刻起固定，不随操作延长。
	reauthTTL = 5 * time.Minute

	sessionKindFull     = "full"
	sessionKindRecovery = "recovery"
)

// 主体类型。
const (
	KindWeb    = "web"
	KindDevice = "device"
	// KindAssistant 是全局助手经 oryn 工具在进程内调用 REST 处理器（ID 为助手对话的线程 id）；鉴权中间件永远不产生它。
	KindAssistant = "assistant"
)

// Principal 是通过鉴权的请求主体。
type Principal struct {
	// Kind 为 "web"、"device" 或 "assistant"。
	Kind string
	// ID：网页会话为会话 id 的哈希；设备为 dev_…；助手为对话的线程 id。
	ID string
	// Recovery 为真表示恢复码换来的一次性会话（只能重新注册 Passkey）。
	Recovery bool
	// DeviceID 在 Kind == KindWeb 且会话由设备换取（App 会话，M6）时为 dev_…，其余为空。
	DeviceID string
	// VerifiedAt 是网页会话最近一次 Passkey 验证的毫秒时间；0 = 从未（恢复会话、admin 签发、App 会话）。
	VerifiedAt int64
	// PasskeyID 是登录所用 Passkey 的 credential id（无填充 base64url）；空 = 不是 Passkey 登录。
	// 用 string 而不是 []byte，保持 Principal 可比较。
	PasskeyID string
}

type ctxKey struct{}

// WithPrincipal 返回携带主体的 context。
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext 返回中间件写入的主体。
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Error 是带 HTTP 状态与错误码的鉴权错误，API 层原样转成 {code, message}。
type Error struct {
	Status  int
	Code    string
	Message string
	// RetryAfter 非零时写出 Retry-After 头（秒）。
	RetryAfter int
}

func (e *Error) Error() string { return e.Message }

func newErr(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

var (
	errUnauthorized = newErr(http.StatusUnauthorized, "unauthorized", "未登录或凭据无效")
	errCSRF         = newErr(http.StatusForbidden, "forbidden", "缺少 X-Oryn-CSRF 头")
	errRecoveryOnly = newErr(http.StatusForbidden, "forbidden", "恢复会话只能重新注册 Passkey 或重设密码登录")
)

// recoveryAllowed 是恢复会话可经中间件访问的路径（auth/setup/*、auth/password/setup/* 免中间件，由处理器自行校验）。
var recoveryAllowed = map[string]bool{
	"/api/v1/me":          true,
	"/api/v1/auth/logout": true,
}

// Middleware 校验 Cookie 会话或设备 Bearer 令牌；失败返回 401 {code:"unauthorized"}。
func Middleware(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := Authenticate(w, r, st)
			if err != nil {
				writeErr(w, err)
				return
			}
			if p.Recovery && !recoveryAllowed[r.URL.Path] {
				writeErr(w, errRecoveryOnly)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// Authenticate 解析请求凭据。有 Authorization 头时只认设备令牌；否则读 Cookie 会话
// （变更类方法要求 CSRF 头，必要时滑动续期并重发 Cookie）。
func Authenticate(w http.ResponseWriter, r *http.Request, st *store.Store) (Principal, error) {
	ctx := r.Context()
	if h := r.Header.Get("Authorization"); h != "" {
		tok, ok := strings.CutPrefix(h, "Bearer ")
		if !ok {
			return Principal{}, errUnauthorized
		}
		p, ok := DeviceByToken(ctx, st, tok)
		if !ok {
			return Principal{}, errUnauthorized
		}
		return p, nil
	}
	ws, cookie, ok, err := sessionFromCookie(ctx, st.Q, r)
	if err != nil {
		return Principal{}, err
	}
	if !ok {
		return Principal{}, errUnauthorized
	}
	if isMutation(r.Method) && r.Header.Get(CSRFHeader) != "1" {
		return Principal{}, errCSRF
	}
	p := Principal{Kind: KindWeb, ID: ws.IDHash, Recovery: ws.Kind == sessionKindRecovery}
	if ws.DeviceID != nil {
		p.DeviceID = *ws.DeviceID
	}
	if ws.VerifiedAt != nil {
		p.VerifiedAt = *ws.VerifiedAt
	}
	if len(ws.PasskeyID) > 0 {
		p.PasskeyID = base64.RawURLEncoding.EncodeToString(ws.PasskeyID)
	}
	if ws.Kind == sessionKindFull {
		now := time.Now()
		if now.Sub(time.UnixMilli(ws.LastSeenAt)) >= sessionTouchEvery {
			exp := now.Add(SessionTTL)
			if err := st.Q.TouchWebSession(ctx, db.TouchWebSessionParams{
				LastSeenAt: now.UnixMilli(), ExpiresAt: exp.UnixMilli(), IDHash: ws.IDHash,
			}); err == nil {
				if p.DeviceID != "" {
					// App 会话只用会话级 Cookie（壳每次启动都重新换取，不落盘）
					SetAppSessionCookie(w, cookie)
				} else {
					SetSessionCookie(w, cookie, exp)
				}
				// App 会话：让设备列表的「最近活跃」对 App 有效
				if p.DeviceID != "" {
					ms := now.UnixMilli()
					_ = st.Q.TouchDevice(ctx, db.TouchDeviceParams{LastSeenAt: &ms, ID: p.DeviceID})
				}
			}
		}
	}
	return p, nil
}

// DeviceByToken 校验设备令牌 `odt_…`（存在且未吊销），按 deviceTouchEvery 节流更新 last_seen_at。
// 供 Authenticate 与 portal 主机上的 `Authorization: Bearer odt_…` 共用。
func DeviceByToken(ctx context.Context, st *store.Store, tok string) (Principal, bool) {
	if ids.TokenPrefix(tok) != "odt" {
		return Principal{}, false
	}
	dev, err := st.Q.GetDeviceByTokenHash(ctx, ids.Hash(tok))
	if err != nil || dev.RevokedAt != nil {
		return Principal{}, false
	}
	now := time.Now()
	if dev.LastSeenAt == nil || now.Sub(time.UnixMilli(*dev.LastSeenAt)) >= deviceTouchEvery {
		ms := now.UnixMilli()
		_ = st.Q.TouchDevice(ctx, db.TouchDeviceParams{LastSeenAt: &ms, ID: dev.ID})
	}
	return Principal{Kind: KindDevice, ID: dev.ID}, true
}

// GitCredential 校验 /git/ 路径的凭据（M24 阶段 ③）：`Authorization: Basic <任意用户名>:<odt_…>` 或 `Bearer odt_…`，
// 只认设备令牌（DeviceByToken：存在且未吊销），从不读 Cookie。
func GitCredential(ctx context.Context, st *store.Store, r *http.Request) (Principal, bool) {
	tok := GitRequestToken(r)
	if tok == "" {
		return Principal{}, false
	}
	return DeviceByToken(ctx, st, tok)
}

// GitRequestToken 取 /git/ 请求带的令牌：HTTP Basic 的密码位，否则 Bearer 的值；都没有为空串。不校验令牌。
func GitRequestToken(r *http.Request) string {
	if _, pw, ok := r.BasicAuth(); ok {
		return pw
	}
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return tok
	}
	return ""
}

// sessionFromCookie 读取并校验 Cookie 会话，返回会话与命中的 Cookie 值；没有有效会话返回 ok=false。
// 同名 Cookie 可能有多条（浏览器按 Path 长度排序发送，旧的或被写坏的可能排在前面），
// 逐条校验并取第一条有效的，而不是只看第一条。App 会话（device_id 非空）在其设备已吊销时
// 由 GetWebSession 直接视为不存在，即便吊销中途失败留下了会话行也无法通过鉴权。
func sessionFromCookie(ctx context.Context, q *db.Queries, r *http.Request) (db.WebSession, string, bool, error) {
	for _, c := range r.CookiesNamed(CookieName) {
		if c.Value == "" {
			continue
		}
		ws, err := q.GetWebSession(ctx, ids.Hash(c.Value))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return db.WebSession{}, "", false, err
		}
		// 过期会话不在这里删除：由 Service.Run 的清理循环删除并发布吊销事件，
		// 让持有该会话 WebSocket 的连接能被关闭。
		if ws.ExpiresAt <= time.Now().UnixMilli() {
			continue
		}
		return ws, c.Value, true, nil
	}
	return db.WebSession{}, "", false, nil
}

func isMutation(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// sessionAuth 记录会话由哪种验证签发；零值 = 未验证（恢复会话、admin 签发）。
type sessionAuth struct {
	// passkeyID 非 nil：由 Passkey 仪式（登录 / 注册）签发，记下所用凭据。
	passkeyID []byte
	// password：由密码 + TOTP 登录或初始化签发；改密码、关闭密码登录时据此吊销。
	password bool
}

// createSession 写入一条网页会话并返回明文 Cookie 值与过期时间。经过验证（Passkey 或密码 + TOTP）签发的会话把此刻记为一次验证。
func createSession(ctx context.Context, q *db.Queries, kind, userAgent string, ttl time.Duration, via sessionAuth) (string, time.Time, error) {
	tok := ids.Token("ows")
	now := time.Now()
	exp := now.Add(ttl)
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	arg := db.CreateWebSessionParams{
		IDHash: ids.Hash(tok), Kind: kind, CreatedAt: now.UnixMilli(),
		ExpiresAt: exp.UnixMilli(), LastSeenAt: now.UnixMilli(), UserAgent: userAgent, PasskeyID: via.passkeyID,
	}
	if via.passkeyID != nil || via.password {
		verified := now.UnixMilli()
		arg.VerifiedAt = &verified
	}
	if via.password {
		arg.ViaPassword = 1
	}
	err := q.CreateWebSession(ctx, arg)
	return tok, exp, err
}

// CreateWebSession 直接签发一条完整网页会话（测试与本机工具使用），返回 Cookie 值。
func CreateWebSession(ctx context.Context, st *store.Store, userAgent string) (string, error) {
	tok, _, err := createSession(ctx, st.Q, sessionKindFull, userAgent, SessionTTL, sessionAuth{})
	return tok, err
}

// requireOwnerSession 只放行所有者的网页会话与 App 会话（恢复会话、设备令牌、助手 → 403）。
func requireOwnerSession(p Principal) error {
	if p.Kind != KindWeb || p.Recovery {
		return newErr(http.StatusForbidden, "forbidden", "需要网页登录会话")
	}
	return nil
}

// ReauthValidUntil 返回验证有效期的截止时间；从未验证或已过期时 ok 为 false。
func ReauthValidUntil(p Principal, now time.Time) (time.Time, bool) {
	if p.VerifiedAt <= 0 {
		return time.Time{}, false
	}
	until := time.UnixMilli(p.VerifiedAt).Add(reauthTTL)
	if !now.Before(until) {
		return time.Time{}, false
	}
	return until, true
}

// RequireRecentAuth 要求会话在 reauthTTL 内做过验证（Passkey 或密码 + TOTP），否则返回 403 reauth_required。
func RequireRecentAuth(p Principal, now time.Time) error {
	if _, ok := ReauthValidUntil(p, now); !ok {
		return newErr(http.StatusForbidden, "reauth_required", "这项操作需要先验证身份（Passkey 或密码 + 验证码，5 分钟内有效）")
	}
	return nil
}

// SetSessionCookie 写出会话 Cookie。
func SetSessionCookie(w http.ResponseWriter, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: value, Path: "/",
		Expires: expires, MaxAge: int(time.Until(expires).Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// SetAppSessionCookie 写出 App 会话 Cookie：不带 Expires / Max-Age，只活在 WebView 进程里，
// 不写进 WebView 的磁盘存储；服务端过期仍按 SessionTTL 滑动。
func SetAppSessionCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: value, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie 让浏览器删除会话 Cookie。
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// Logout 删除当前网页会话并发布吊销事件。
func (s *Service) Logout(ctx context.Context, p Principal) error {
	if p.Kind != KindWeb {
		return newErr(http.StatusBadRequest, "invalid_request", "只有网页会话可以注销；设备请吊销")
	}
	if err := s.st.Q.DeleteWebSession(ctx, p.ID); err != nil {
		return err
	}
	s.revocations.publish(KindWeb, p.ID)
	return nil
}

func writeErr(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = newErr(http.StatusInternalServerError, "internal", err.Error())
	}
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfter))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": e.Code, "message": e.Message})
}
