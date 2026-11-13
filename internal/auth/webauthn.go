package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	ceremonyTTL     = 5 * time.Minute
	defaultOwner    = "Owner"
	recoveryCodeNum = 10

	ceremonySetup = "setup"
	ceremonyLogin = "login"
)

// Service 持有 WebAuthn 依赖方配置、限流表与吊销广播。
//
// 仪式状态是无状态的：begin 把 SessionData 等用进程内随机密钥以 AES-GCM 封装成 ceremonyId 交给客户端，
// finish 时解封校验。服务端不为进行中的仪式保存任何东西，匿名请求无法把仪式表塞满挤掉所有者的登录；
// 只有成功完成的仪式记入 used（5 分钟），防止同一 ceremonyId 被重放。重启后未完成的仪式失效。
type Service struct {
	st      *store.Store
	dataDir string
	wa      *webauthn.WebAuthn
	aead    cipher.AEAD

	limiter     *limiter
	revocations revocations
	// appCodes 是 App 会话一次性码（`oac_…`，见 appsession.go）。
	appCodes *Tickets

	mu   sync.Mutex
	used map[string]time.Time
}

// ceremony 是封装进 ceremonyId 的仪式状态。
type ceremony struct {
	Kind    string               `json:"k"`
	Session webauthn.SessionData `json:"s"`
	Expires int64                `json:"e"`
	// 初始化：新用户句柄与所用初始化令牌的哈希；恢复：恢复会话 id 哈希。
	Handle          []byte `json:"h,omitempty"`
	TokenHash       string `json:"t,omitempty"`
	RecoverySession string `json:"r,omitempty"`
	PasskeyName     string `json:"n,omitempty"`
}

// ceremonyAD 是封装仪式状态时的附加数据。
var ceremonyAD = []byte("oryn-webauthn-ceremony-v1")

// NewService 以 public_url 构造 WebAuthn 依赖方：RP ID = 主机名，origin = public_url 的 scheme://host[:port]。
func NewService(st *store.Store, dataDir, publicURL string) (*Service, error) {
	u, err := url.Parse(publicURL)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("public_url %q 无效", publicURL)
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          u.Hostname(),
		RPDisplayName: "Oryn",
		RPOrigins:     []string{u.Scheme + "://" + u.Host},
	})
	if err != nil {
		return nil, fmt.Errorf("初始化 WebAuthn: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Service{
		st: st, dataDir: dataDir, wa: wa, aead: aead, limiter: newLimiter(), used: map[string]time.Time{},
		appCodes: NewCodes("oac"),
	}, nil
}

// Store 返回底层存储。
func (s *Service) Store() *store.Store { return s.st }

// waUser 适配 webauthn.User。
type waUser struct {
	handle  []byte
	display string
	creds   []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return u.handle }
func (u *waUser) WebAuthnName() string                       { return "owner" }
func (u *waUser) WebAuthnDisplayName() string                { return u.display }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// loadOwner 读取所有者与其全部 Passkey；尚无所有者时 ok=false。
func loadOwner(ctx context.Context, q *db.Queries) (*waUser, db.Owner, bool, error) {
	o, err := q.GetOwner(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, o, false, nil
	}
	if err != nil {
		return nil, o, false, err
	}
	pks, err := q.ListPasskeys(ctx)
	if err != nil {
		return nil, o, false, err
	}
	u := &waUser{handle: o.UserHandle, display: o.DisplayName}
	for _, pk := range pks {
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(pk.Credential), &c); err != nil {
			return nil, o, false, fmt.Errorf("解析 Passkey 记录: %w", err)
		}
		u.creds = append(u.creds, c)
	}
	return u, o, true, nil
}

// putCeremony 封装仪式状态，返回 ceremonyId（wac_<base64url(nonce||密文)>）。
func (s *Service) putCeremony(c *ceremony) (string, error) {
	c.Expires = time.Now().Add(ceremonyTTL).UnixMilli()
	plain, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return "wac_" + base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, ceremonyAD)), nil
}

var errCeremony = newErr(http.StatusBadRequest, "invalid_request", "认证仪式不存在或已过期，请重新开始")

// takeCeremony 解封并校验仪式（类型、过期、未被成功使用过）。
func (s *Service) takeCeremony(id, kind string) (*ceremony, error) {
	raw, ok := strings.CutPrefix(id, "wac_")
	if !ok || len(raw) > 16<<10 {
		return nil, errCeremony
	}
	sealed, err := base64.RawURLEncoding.DecodeString(raw)
	n := s.aead.NonceSize()
	if err != nil || len(sealed) < n {
		return nil, errCeremony
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], ceremonyAD)
	if err != nil {
		return nil, errCeremony
	}
	var c ceremony
	if json.Unmarshal(plain, &c) != nil || c.Kind != kind || time.Now().UnixMilli() > c.Expires {
		return nil, errCeremony
	}
	s.mu.Lock()
	_, used := s.used[ids.Hash(id)]
	s.mu.Unlock()
	if used {
		return nil, errCeremony
	}
	return &c, nil
}

// markUsed 在仪式校验成功后记录它，防止重放；已被使用返回错误。
func (s *Service) markUsed(id string, c *ceremony) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := ids.Hash(id)
	if _, ok := s.used[h]; ok {
		return errCeremony
	}
	s.used[h] = time.UnixMilli(c.Expires)
	return nil
}

func (s *Service) pruneUsedCeremonies(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, exp := range s.used {
		if now.After(exp) {
			delete(s.used, h)
		}
	}
}

// optionsJSON 把 publicKey 选项转成通用对象（WebAuthn L3 JSON 形态，二进制字段为 base64url）。
func optionsJSON(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}

// Ceremony 是 begin 的返回：仪式句柄 + 传给 navigator.credentials.create/get 的 publicKey 选项。
type Ceremony struct {
	ID      string
	Options map[string]any
}

// BeginSetup 开始注册 Passkey。token 非空 = 首次初始化；为空 = 恢复流程（要求恢复会话 Cookie + CSRF 头）。
func (s *Service) BeginSetup(ctx context.Context, r *http.Request, token, name string) (Ceremony, error) {
	c := &ceremony{Kind: ceremonySetup, PasskeyName: strings.TrimSpace(name)}
	var user *waUser
	var opts []webauthn.RegistrationOption
	if token != "" {
		_, _, exists, err := loadOwner(ctx, s.st.Q)
		if err != nil {
			return Ceremony{}, err
		}
		if exists {
			return Ceremony{}, newErr(http.StatusConflict, "conflict", ErrAlreadyInitialized.Error())
		}
		if c.TokenHash, err = checkSetupToken(s.dataDir, token); err != nil {
			return Ceremony{}, err
		}
		c.Handle = make([]byte, 32)
		_, _ = rand.Read(c.Handle)
		user = &waUser{handle: c.Handle, display: defaultOwner}
	} else {
		ws, err := s.recoverySession(ctx, r)
		if err != nil {
			return Ceremony{}, err
		}
		u, _, exists, err := loadOwner(ctx, s.st.Q)
		if err != nil {
			return Ceremony{}, err
		}
		if !exists {
			return Ceremony{}, newErr(http.StatusConflict, "conflict", "尚未初始化")
		}
		user, c.RecoverySession = u, ws.IDHash
		excl := make([]protocol.CredentialDescriptor, 0, len(u.creds))
		for i := range u.creds {
			excl = append(excl, u.creds[i].Descriptor())
		}
		opts = append(opts, webauthn.WithExclusions(excl))
	}
	opts = append(opts,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationPreferred,
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
	)
	creation, session, err := s.wa.BeginRegistration(user, opts...)
	if err != nil {
		return Ceremony{}, fmt.Errorf("BeginRegistration: %w", err)
	}
	c.Session = *session
	options, err := optionsJSON(creation.Response)
	if err != nil {
		return Ceremony{}, err
	}
	id, err := s.putCeremony(c)
	return Ceremony{ID: id, Options: options}, err
}

// recoverySession 要求请求带未过期的恢复会话 Cookie 与 CSRF 头。
func (s *Service) recoverySession(ctx context.Context, r *http.Request) (db.WebSession, error) {
	ws, _, ok, err := sessionFromCookie(ctx, s.st.Q, r)
	if err != nil {
		return ws, err
	}
	if !ok || ws.Kind != sessionKindRecovery {
		return ws, newErr(http.StatusUnauthorized, "unauthorized", "需要初始化令牌或恢复会话")
	}
	if r.Header.Get(CSRFHeader) != "1" {
		return ws, errCSRF
	}
	return ws, nil
}

// SetupResult 是注册完成后的结果。
type SetupResult struct {
	Cookie        string
	Expires       time.Time
	RecoveryCodes []string
}

// FinishSetup 校验注册响应，写入所有者（首次）与 Passkey，签发完整网页会话。
// 首次初始化时生成 10 个恢复码（明文只返回这一次）并作废初始化令牌；恢复流程删除恢复会话。
func (s *Service) FinishSetup(ctx context.Context, r *http.Request, ceremonyID string, credential []byte) (SetupResult, error) {
	c, err := s.takeCeremony(ceremonyID, ceremonySetup)
	if err != nil {
		return SetupResult{}, err
	}
	var user *waUser
	if c.RecoverySession != "" {
		ws, err := s.recoverySession(ctx, r)
		if err != nil {
			return SetupResult{}, err
		}
		if ws.IDHash != c.RecoverySession {
			return SetupResult{}, errUnauthorized
		}
		u, _, exists, err := loadOwner(ctx, s.st.Q)
		if err != nil {
			return SetupResult{}, err
		}
		if !exists {
			return SetupResult{}, newErr(http.StatusConflict, "conflict", "尚未初始化")
		}
		user = u
	} else {
		if cur, err := currentSetupTokenHash(s.dataDir); err != nil {
			return SetupResult{}, err
		} else if cur != c.TokenHash {
			return SetupResult{}, newErr(http.StatusUnauthorized, "unauthorized", "初始化令牌已被重新生成")
		}
		user = &waUser{handle: c.Handle, display: defaultOwner}
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(credential)
	if err != nil {
		return SetupResult{}, newErr(http.StatusBadRequest, "invalid_request", webauthnMsg("注册响应无法解析", err))
	}
	cred, err := s.wa.CreateCredential(user, c.Session, parsed)
	if err != nil {
		return SetupResult{}, newErr(http.StatusBadRequest, "invalid_request", webauthnMsg("注册校验失败", err))
	}
	if err := s.markUsed(ceremonyID, c); err != nil {
		return SetupResult{}, err
	}
	credJSON, err := json.Marshal(cred)
	if err != nil {
		return SetupResult{}, err
	}
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	transportsJSON, _ := json.Marshal(transports)
	name := c.PasskeyName
	if name == "" {
		name = "Passkey"
	}
	var res SetupResult
	initial := c.RecoverySession == ""
	if initial {
		res.RecoveryCodes = newRecoveryCodes()
	}
	now := time.Now().UnixMilli()
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if initial {
			if _, err := q.GetOwner(ctx); err == nil {
				return newErr(http.StatusConflict, "conflict", ErrAlreadyInitialized.Error())
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err := q.CreateOwner(ctx, db.CreateOwnerParams{DisplayName: defaultOwner, UserHandle: c.Handle, CreatedAt: now}); err != nil {
				return err
			}
			if err := q.DeleteAllRecoveryCodes(ctx); err != nil {
				return err
			}
			for _, code := range res.RecoveryCodes {
				if err := q.InsertRecoveryCode(ctx, hashRecoveryCode(code)); err != nil {
					return err
				}
			}
		} else if err := q.DeleteWebSession(ctx, c.RecoverySession); err != nil {
			return err
		}
		if err := q.CreatePasskey(ctx, db.CreatePasskeyParams{
			ID: cred.ID, PublicKey: cred.PublicKey, SignCount: int64(cred.Authenticator.SignCount),
			Transports: string(transportsJSON), Credential: string(credJSON), Name: name, CreatedAt: now,
		}); err != nil {
			return err
		}
		res.Cookie, res.Expires, err = createSession(ctx, q, sessionKindFull, r.UserAgent(), SessionTTL)
		return err
	})
	if err != nil {
		return SetupResult{}, err
	}
	if initial {
		consumeSetupToken(s.dataDir, c.TokenHash)
	} else {
		s.revocations.publish(KindWeb, c.RecoverySession)
	}
	return res, nil
}

// BeginLogin 开始可发现凭据登录。
func (s *Service) BeginLogin(ctx context.Context) (Ceremony, error) {
	_, _, exists, err := loadOwner(ctx, s.st.Q)
	if err != nil {
		return Ceremony{}, err
	}
	if !exists {
		return Ceremony{}, newErr(http.StatusConflict, "conflict", "尚未初始化，请使用启动日志中的 setup 链接")
	}
	assertion, session, err := s.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		return Ceremony{}, fmt.Errorf("BeginDiscoverableLogin: %w", err)
	}
	options, err := optionsJSON(assertion.Response)
	if err != nil {
		return Ceremony{}, err
	}
	id, err := s.putCeremony(&ceremony{Kind: ceremonyLogin, Session: *session})
	return Ceremony{ID: id, Options: options}, err
}

// FinishLogin 校验断言并签发网页会话，返回 Cookie 值与过期时间。
func (s *Service) FinishLogin(ctx context.Context, r *http.Request, ceremonyID string, credential []byte) (string, time.Time, error) {
	c, err := s.takeCeremony(ceremonyID, ceremonyLogin)
	if err != nil {
		return "", time.Time{}, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credential)
	if err != nil {
		return "", time.Time{}, newErr(http.StatusBadRequest, "invalid_request", webauthnMsg("登录响应无法解析", err))
	}
	owner, _, exists, err := loadOwner(ctx, s.st.Q)
	if err != nil {
		return "", time.Time{}, err
	}
	if !exists {
		return "", time.Time{}, errUnauthorized
	}
	handler := func(_, userHandle []byte) (webauthn.User, error) {
		if string(userHandle) != string(owner.handle) {
			return nil, errors.New("未知用户句柄")
		}
		return owner, nil
	}
	_, cred, err := s.wa.ValidatePasskeyLogin(handler, c.Session, parsed)
	if err != nil {
		return "", time.Time{}, newErr(http.StatusUnauthorized, "unauthorized", webauthnMsg("Passkey 校验失败", err))
	}
	if cred.Authenticator.CloneWarning {
		return "", time.Time{}, newErr(http.StatusUnauthorized, "unauthorized", "Passkey 签名计数回退，疑似被克隆")
	}
	if err := s.markUsed(ceremonyID, c); err != nil {
		return "", time.Time{}, err
	}
	credJSON, err := json.Marshal(cred)
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UnixMilli()
	var tok string
	var exp time.Time
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.UpdatePasskeyUse(ctx, db.UpdatePasskeyUseParams{
			SignCount: int64(cred.Authenticator.SignCount), Credential: string(credJSON), LastUsedAt: &now, ID: cred.ID,
		}); err != nil {
			return err
		}
		tok, exp, err = createSession(ctx, q, sessionKindFull, r.UserAgent(), SessionTTL)
		return err
	})
	return tok, exp, err
}

// webauthnMsg 拼出包含 go-webauthn 细节的错误信息。
func webauthnMsg(prefix string, err error) string {
	var pe *protocol.Error
	if errors.As(err, &pe) && pe.DevInfo != "" {
		return prefix + "：" + pe.Details + "（" + pe.DevInfo + "）"
	}
	if errors.As(err, &pe) {
		return prefix + "：" + pe.Details
	}
	return prefix + "：" + err.Error()
}
