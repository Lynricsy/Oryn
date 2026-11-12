// Package auth 实现控制面鉴权：Passkey（WebAuthn）初始化与登录、恢复码、网页会话、
// 设备配对与设备令牌、WebSocket 票据，以及保护 /api/v1 的中间件。
//
// 凭据形态：
//   - 网页会话：Cookie `oryn_session`（HttpOnly、Secure、SameSite=Lax），30 天有效、滑动续期；
//     变更类请求必须带头 `X-Oryn-CSRF: 1`。kind=recovery 的会话只能调用 `auth/setup/*`、`me`、`auth/logout`。
//   - 设备令牌：`Authorization: Bearer odt_…`，可逐台吊销。
//
// 库中只保存令牌与会话 id 的 SHA-256。
package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 常量。
const (
	CookieName = "oryn_session"
	CSRFHeader = "X-Oryn-CSRF"

	// SessionTTL 是网页会话有效期（滑动续期）。
	SessionTTL = 30 * 24 * time.Hour
	// RecoveryTTL 是恢复码换来的一次性会话有效期。
	RecoveryTTL = 15 * time.Minute
	// sessionTouchEvery 控制滑动续期写库的频率，避免每个请求都写。
	sessionTouchEvery = time.Hour
	// deviceTouchEvery 控制设备 last_seen_at 的更新频率。
	deviceTouchEvery = time.Minute

	sessionKindFull     = "full"
	sessionKindRecovery = "recovery"
)

// 主体类型。
const (
	KindWeb    = "web"
	KindDevice = "device"
)

// Principal 是通过鉴权的请求主体。
type Principal struct {
	// Kind 为 "web" 或 "device"。
	Kind string
	// ID：网页会话为会话 id 的哈希；设备为 dev_…。
	ID string
	// Recovery 为真表示恢复码换来的一次性会话（只能重新注册 Passkey）。
	Recovery bool
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
}

func (e *Error) Error() string { return e.Message }

func newErr(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

var (
	errUnauthorized = newErr(http.StatusUnauthorized, "unauthorized", "未登录或凭据无效")
	errCSRF         = newErr(http.StatusForbidden, "forbidden", "缺少 X-Oryn-CSRF 头")
	errRecoveryOnly = newErr(http.StatusForbidden, "forbidden", "恢复会话只能重新注册 Passkey")
)

// recoveryAllowed 是恢复会话可经中间件访问的路径（auth/setup/* 免中间件，由处理器自行校验）。
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
		if !ok || ids.TokenPrefix(tok) != "odt" {
			return Principal{}, errUnauthorized
		}
		dev, err := st.Q.GetDeviceByTokenHash(ctx, ids.Hash(tok))
		if err != nil || dev.RevokedAt != nil {
			return Principal{}, errUnauthorized
		}
		now := time.Now()
		if dev.LastSeenAt == nil || now.Sub(time.UnixMilli(*dev.LastSeenAt)) >= deviceTouchEvery {
			ms := now.UnixMilli()
			_ = st.Q.TouchDevice(ctx, db.TouchDeviceParams{LastSeenAt: &ms, ID: dev.ID})
		}
		return Principal{Kind: KindDevice, ID: dev.ID}, nil
	}
	ws, ok, err := sessionFromCookie(ctx, st.Q, r)
	if err != nil {
		return Principal{}, err
	}
	if !ok {
		return Principal{}, errUnauthorized
	}
	if isMutation(r.Method) && r.Header.Get(CSRFHeader) != "1" {
		return Principal{}, errCSRF
	}
	if ws.Kind == sessionKindFull {
		now := time.Now()
		if now.Sub(time.UnixMilli(ws.LastSeenAt)) >= sessionTouchEvery {
			exp := now.Add(SessionTTL)
			if err := st.Q.TouchWebSession(ctx, db.TouchWebSessionParams{
				LastSeenAt: now.UnixMilli(), ExpiresAt: exp.UnixMilli(), IDHash: ws.IDHash,
			}); err == nil {
				if c, err := r.Cookie(CookieName); err == nil {
					SetSessionCookie(w, c.Value, exp)
				}
			}
		}
	}
	return Principal{Kind: KindWeb, ID: ws.IDHash, Recovery: ws.Kind == sessionKindRecovery}, nil
}

// sessionFromCookie 读取并校验 Cookie 会话；会话不存在或过期返回 ok=false。
func sessionFromCookie(ctx context.Context, q *db.Queries, r *http.Request) (db.WebSession, bool, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return db.WebSession{}, false, nil
	}
	ws, err := q.GetWebSession(ctx, ids.Hash(c.Value))
	if errors.Is(err, sql.ErrNoRows) {
		return db.WebSession{}, false, nil
	}
	if err != nil {
		return db.WebSession{}, false, err
	}
	if ws.ExpiresAt <= time.Now().UnixMilli() {
		_ = q.DeleteWebSession(ctx, ws.IDHash)
		return db.WebSession{}, false, nil
	}
	return ws, true, nil
}

func isMutation(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// createSession 写入一条网页会话并返回明文 Cookie 值与过期时间。
func createSession(ctx context.Context, q *db.Queries, kind, userAgent string, ttl time.Duration) (string, time.Time, error) {
	tok := ids.Token("ows")
	now := time.Now()
	exp := now.Add(ttl)
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	err := q.CreateWebSession(ctx, db.CreateWebSessionParams{
		IDHash: ids.Hash(tok), Kind: kind, CreatedAt: now.UnixMilli(),
		ExpiresAt: exp.UnixMilli(), LastSeenAt: now.UnixMilli(), UserAgent: userAgent,
	})
	return tok, exp, err
}

// CreateWebSession 直接签发一条完整网页会话（测试与本机工具使用），返回 Cookie 值。
func CreateWebSession(ctx context.Context, st *store.Store, userAgent string) (string, error) {
	tok, _, err := createSession(ctx, st.Q, sessionKindFull, userAgent, SessionTTL)
	return tok, err
}

// SetSessionCookie 写出会话 Cookie。
func SetSessionCookie(w http.ResponseWriter, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: value, Path: "/",
		Expires: expires, MaxAge: int(time.Until(expires).Seconds()),
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

// Logout 删除当前网页会话。
func Logout(ctx context.Context, st *store.Store, p Principal) error {
	if p.Kind != KindWeb {
		return newErr(http.StatusBadRequest, "invalid_request", "只有网页会话可以注销；设备请吊销")
	}
	return st.Q.DeleteWebSession(ctx, p.ID)
}

func writeErr(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = newErr(http.StatusInternalServerError, "internal", err.Error())
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": e.Code, "message": e.Message})
}
