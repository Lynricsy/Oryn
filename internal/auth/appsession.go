package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// App 会话（M6）：桌面 / 移动 App 的壳用设备令牌换一条「设备名下的网页会话」Cookie，
// 让加载服务器 Web UI 的 WebView 按同源方式工作，令牌本身不进 JS。
//
// 流程照搬 portal 握手的「一次性码 + 顶层跳转」：`POST /api/v1/auth/app-session`（Bearer `odt_…`）
// 签发 30 秒单次码 → 壳把主窗口导航到 `GET /auth/app?code=…&next=…` → 写会话 Cookie 并 302。
// 会话是普通的 web_sessions(kind='full') 行加 device_id，所以 WS 票据、CSRF、portal 握手不需改动；
// 吊销设备时一并删除其会话（RevokeDevice）。

// errAppSessionUnsupported 是非 desktop / mobile 设备令牌主体请求 App 会话时的错误。
var errAppSessionUnsupported = newErr(http.StatusForbidden, "app_session_unsupported", "只有桌面或移动 App 的设备令牌能换取 App 会话")

// appSessionKind 判断设备类型能否换取 App 会话。
func appSessionKind(kind string) bool { return kind == "desktop" || kind == "mobile" }

// IssueAppCode 为设备主体签发一次性码（有效期 TicketTTL）。p 必须是 Bearer 设备令牌主体（Kind == KindDevice），
// 且设备 kind ∈ {desktop, mobile}；否则 403 app_session_unsupported。
func (s *Service) IssueAppCode(ctx context.Context, p Principal) (code string, expiresIn int, err error) {
	if p.Kind != KindDevice {
		return "", 0, errAppSessionUnsupported
	}
	dev, err := s.st.Q.GetDevice(ctx, p.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, errUnauthorized
	}
	if err != nil {
		return "", 0, err
	}
	if dev.RevokedAt != nil {
		return "", 0, errUnauthorized
	}
	if !appSessionKind(dev.Kind) {
		return "", 0, errAppSessionUnsupported
	}
	code, expiresIn = s.appCodes.Issue(Principal{Kind: KindDevice, ID: dev.ID})
	return code, expiresIn, nil
}

// RedeemAppCode 兑换一次性码：码无效 / 已用 / 过期 / 设备已吊销 → ok=false。
// 成功时在一个事务里删除该设备已有的网页会话（事务后逐个发布 (web, 哈希) 吊销事件）并新建一条
// kind=full、device_id=该设备、有效期 SessionTTL 的会话，再更新设备的 last_seen_at。
func (s *Service) RedeemAppCode(ctx context.Context, code, userAgent string) (cookie string, expires time.Time, ok bool, err error) {
	if ids.TokenPrefix(code) != "oac" {
		return "", time.Time{}, false, nil
	}
	p, ok := s.appCodes.Redeem(code)
	if !ok || p.Kind != KindDevice {
		return "", time.Time{}, false, nil
	}
	tok := ids.Token("ows")
	now := time.Now()
	exp := now.Add(SessionTTL)
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	var old []string
	valid := false
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		// 设备状态在写事务内复核（BEGIN IMMEDIATE）：与 RevokeDevice 的「标记吊销 → 删除会话」串行，
		// 不会留下吊销之后才建成的会话。
		dev, err := q.GetDevice(ctx, p.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if dev.RevokedAt != nil || !appSessionKind(dev.Kind) {
			return nil
		}
		if old, err = q.DeleteWebSessionsByDevice(ctx, &dev.ID); err != nil {
			return err
		}
		if err := q.CreateDeviceWebSession(ctx, db.CreateDeviceWebSessionParams{
			IDHash: ids.Hash(tok), CreatedAt: now.UnixMilli(), ExpiresAt: exp.UnixMilli(),
			LastSeenAt: now.UnixMilli(), UserAgent: userAgent, DeviceID: &dev.ID,
		}); err != nil {
			return err
		}
		ms := now.UnixMilli()
		if err := q.TouchDevice(ctx, db.TouchDeviceParams{LastSeenAt: &ms, ID: dev.ID}); err != nil {
			return err
		}
		valid = true
		return nil
	})
	if err != nil {
		return "", time.Time{}, false, err
	}
	for _, h := range old {
		s.revocations.publish(KindWeb, h)
	}
	if !valid {
		return "", time.Time{}, false, nil
	}
	return tok, exp, true, nil
}
