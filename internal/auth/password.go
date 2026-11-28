package auth

// 不用 Passkey 的登录方式：密码 + 强制 TOTP。
//
// 两者总是一起启用、一起替换、一起关闭。登录与重新验证时密码和验证码都算完再统一判定，
// 统一报「密码或验证码错误」：若分开报错，攻击者可以先单独试出当前的验证码，把两步验证退化成只有密码。
// 被接受的 TOTP 时间步记在 owner.totp_last_step，同一个验证码（及更早的）不能再用。
// 这些端点不做限流（主人的决定）。

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	passwordMinRunes = 8
	passwordMaxRunes = 256

	// Argon2id 参数取 OWASP 推荐的 m=46 MiB、t=1、p=1；参数随哈希一起存（PHC 串），以后调整不影响旧哈希。
	argonTime    = 1
	argonMemory  = 46 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16

	// TOTP（RFC 6238）：SHA1、6 位、30 秒，前后各容忍一个时间步的时钟偏差。
	totpDigits      = 6
	totpPeriod      = 30
	totpSkew        = 1
	totpSecretBytes = 20
	totpAAD         = "auth/owner/totp"

	ceremonyPasswordSetup = "pw_setup"
	ceremonyPasswordSet   = "pw_set"
)

var (
	errBadPassword = newErr(http.StatusUnauthorized, "unauthorized", "密码或验证码错误")
	errEnrollCode  = newErr(http.StatusBadRequest, "invalid_request", "验证码不对：请确认验证器里添加的是这次显示的密钥，并且设备时间准确")
	totpEncoding   = base32.StdEncoding.WithPadding(base32.NoPadding)
)

// TOTPEnrollment 是待登记的 TOTP 密钥：ID 在 finish 时回传，Secret 与 URI 交给验证器 App。
type TOTPEnrollment struct {
	ID     string
	Secret string
	URI    string
}

// validatePassword 检查密码长度（按字符计，8–256）。不去首尾空白：密码原样参与哈希。
func validatePassword(pw string) error {
	if n := utf8.RuneCountInString(pw); n < passwordMinRunes || n > passwordMaxRunes {
		return newErr(http.StatusBadRequest, "invalid_request", fmt.Sprintf("密码须为 %d–%d 个字符", passwordMinRunes, passwordMaxRunes))
	}
	return nil
}

// hashPassword 返回 Argon2id 的 PHC 串：$argon2id$v=19$m=…,t=…,p=…$<盐>$<哈希>。
func hashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifyPassword 按 PHC 串里记录的参数重算并做常数时间比较；格式不对按不匹配处理。
func verifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// totpCode 计算某个时间步的验证码（RFC 4226 动态截断）。
func totpCode(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1_000_000)
}

// matchTOTP 在当前时间步前后 totpSkew 步内查找匹配的验证码，返回命中的时间步。每一步都比较，不提前返回。
func matchTOTP(key []byte, code string, now time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	cur := now.Unix() / totpPeriod
	var hit int64
	ok := false
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		if subtle.ConstantTimeCompare([]byte(totpCode(key, cur+d)), []byte(code)) == 1 {
			hit, ok = cur+d, true
		}
	}
	return hit, ok
}

// newTOTPEnrollment 生成新的 TOTP 密钥并封装进登记仪式。
func (s *Service) newTOTPEnrollment(c *ceremony) (TOTPEnrollment, error) {
	key := make([]byte, totpSecretBytes)
	if _, err := rand.Read(key); err != nil {
		return TOTPEnrollment{}, err
	}
	c.TOTPSecret = totpEncoding.EncodeToString(key)
	id, err := s.putCeremony(c)
	if err != nil {
		return TOTPEnrollment{}, err
	}
	host := s.wa.Config.RPID
	q := url.Values{}
	q.Set("secret", c.TOTPSecret)
	q.Set("issuer", "Oryn")
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(totpPeriod))
	uri := "otpauth://totp/" + url.PathEscape("Oryn:"+host) + "?" + q.Encode()
	return TOTPEnrollment{ID: id, Secret: c.TOTPSecret, URI: uri}, nil
}

// passwordCredential 是校验通过、待落库的密码与 TOTP：密码哈希、加密后的密钥与登记时用掉的时间步。
type passwordCredential struct {
	hash      string
	secretEnc []byte
	step      int64
}

// enrollPassword 校验登记仪式里的验证码与新密码，算出待落库的凭据，并把仪式记为已用。
func (s *Service) enrollPassword(id string, c *ceremony, password, code string) (passwordCredential, error) {
	if err := validatePassword(password); err != nil {
		return passwordCredential{}, err
	}
	key, err := totpEncoding.DecodeString(c.TOTPSecret)
	if err != nil {
		return passwordCredential{}, errCeremony
	}
	step, ok := matchTOTP(key, code, time.Now())
	if !ok {
		return passwordCredential{}, errEnrollCode
	}
	if err := s.markUsed(id, c); err != nil {
		return passwordCredential{}, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return passwordCredential{}, err
	}
	enc, err := s.box.Seal(totpAAD, key)
	if err != nil {
		return passwordCredential{}, err
	}
	return passwordCredential{hash: hash, secretEnc: enc, step: step}, nil
}

// storePassword 在事务里替换密码与 TOTP，用掉登记时的时间步（登记用的验证码不能再拿去登录），
// 并删除其它用密码登录的会话（keep 除外），返回被删会话的 id 哈希。
func storePassword(ctx context.Context, q *db.Queries, cred passwordCredential, keep string) ([]string, error) {
	if err := q.SetOwnerPassword(ctx, db.SetOwnerPasswordParams{PasswordHash: &cred.hash, TotpSecretEnc: cred.secretEnc}); err != nil {
		return nil, err
	}
	if _, err := q.UseTotpStep(ctx, cred.step); err != nil {
		return nil, err
	}
	return q.DeleteWebSessionsByPassword(ctx, keep)
}

// BeginPasswordSetup 开始用密码 + TOTP 初始化。入口同 BeginSetup：token 非空 = 首次初始化；为空 = 恢复流程。
func (s *Service) BeginPasswordSetup(ctx context.Context, r *http.Request, token string) (TOTPEnrollment, error) {
	c := &ceremony{Kind: ceremonyPasswordSetup}
	if _, err := s.beginSetupEntry(ctx, r, token, c); err != nil {
		return TOTPEnrollment{}, err
	}
	return s.newTOTPEnrollment(c)
}

// FinishPasswordSetup 校验验证码与密码，写入所有者（首次）与密码 + TOTP，签发完整网页会话。
// 首次初始化返回 10 个恢复码；恢复流程替换原有密码与 TOTP，删除恢复会话并吊销其它用密码登录的会话。
func (s *Service) FinishPasswordSetup(ctx context.Context, r *http.Request, enrollmentID, password, code string) (SetupResult, error) {
	c, err := s.takeCeremony(enrollmentID, ceremonyPasswordSetup)
	if err != nil {
		return SetupResult{}, err
	}
	if _, err := s.finishSetupEntry(ctx, r, c); err != nil {
		return SetupResult{}, err
	}
	cred, err := s.enrollPassword(enrollmentID, c, password, code)
	if err != nil {
		return SetupResult{}, err
	}
	return s.completeSetup(ctx, r, c, sessionAuth{password: true}, func(q *db.Queries) ([]string, error) {
		return storePassword(ctx, q, cred, "")
	})
}

// checkPassword 校验密码与验证码，并在事务里用掉命中的时间步；任何一项不对、未启用或验证码已用过都返回 errBadPassword。
// then 在同一事务里执行（签发会话或记下验证时间）。
func (s *Service) checkPassword(ctx context.Context, password, code string, then func(q *db.Queries) error) error {
	o, err := s.st.Q.GetOwner(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return errBadPassword
	}
	if err != nil {
		return err
	}
	if o.PasswordHash == nil || len(o.TotpSecretEnc) == 0 || utf8.RuneCountInString(password) > passwordMaxRunes {
		return errBadPassword
	}
	key, err := s.box.Open(totpAAD, o.TotpSecretEnc)
	if err != nil {
		return fmt.Errorf("解密 TOTP 密钥: %w", err)
	}
	// 两项都算完再判定，不让响应区分是哪一项错
	pwOK := verifyPassword(*o.PasswordHash, password)
	step, codeOK := matchTOTP(key, code, time.Now())
	if !pwOK || !codeOK {
		return errBadPassword
	}
	return s.st.Tx(ctx, func(q *db.Queries) error {
		n, err := q.UseTotpStep(ctx, step)
		if err != nil {
			return err
		}
		if n == 0 {
			return errBadPassword
		}
		return then(q)
	})
}

// LoginWithPassword 用密码 + 验证码登录，签发完整网页会话（此刻记为一次验证），返回 Cookie 值与过期时间。
func (s *Service) LoginWithPassword(ctx context.Context, r *http.Request, password, code string) (string, time.Time, error) {
	var tok string
	var exp time.Time
	err := s.checkPassword(ctx, password, code, func(q *db.Queries) error {
		var err error
		tok, exp, err = createSession(ctx, q, sessionKindFull, r.UserAgent(), SessionTTL, sessionAuth{password: true})
		return err
	})
	return tok, exp, err
}

// ReauthWithPassword 用密码 + 验证码重新验证当前会话，把验证时间记为此刻并返回它。
func (s *Service) ReauthWithPassword(ctx context.Context, p Principal, password, code string) (time.Time, error) {
	if err := requireOwnerSession(p); err != nil {
		return time.Time{}, err
	}
	ms := time.Now().UnixMilli()
	err := s.checkPassword(ctx, password, code, func(q *db.Queries) error {
		return q.TouchWebSessionVerified(ctx, db.TouchWebSessionVerifiedParams{VerifiedAt: &ms, IDHash: p.ID})
	})
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms), nil
}

// BeginSetPassword 开始在设置页设置或重设密码登录。要求所有者会话且 5 分钟内验证过。
func (s *Service) BeginSetPassword(ctx context.Context, p Principal) (TOTPEnrollment, error) {
	if err := requireOwnerSession(p); err != nil {
		return TOTPEnrollment{}, err
	}
	if err := RequireRecentAuth(p, time.Now()); err != nil {
		return TOTPEnrollment{}, err
	}
	if _, err := s.st.Q.GetOwner(ctx); errors.Is(err, sql.ErrNoRows) {
		return TOTPEnrollment{}, newErr(http.StatusConflict, "conflict", "尚未初始化")
	} else if err != nil {
		return TOTPEnrollment{}, err
	}
	return s.newTOTPEnrollment(&ceremony{Kind: ceremonyPasswordSet, WebSession: p.ID})
}

// FinishSetPassword 校验验证码与新密码并替换密码 + TOTP；吊销其它用密码登录的会话，当前会话保留。
// 不再检查验证窗口：begin 已检查过，仪式自身 5 分钟过期；用户已在验证器里加好密钥后再拒绝只会让他白加一条。
func (s *Service) FinishSetPassword(ctx context.Context, p Principal, enrollmentID, password, code string) error {
	if err := requireOwnerSession(p); err != nil {
		return err
	}
	c, err := s.takeCeremony(enrollmentID, ceremonyPasswordSet)
	if err != nil {
		return err
	}
	if c.WebSession != p.ID {
		return errUnauthorized
	}
	cred, err := s.enrollPassword(enrollmentID, c, password, code)
	if err != nil {
		return err
	}
	var revoked []string
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		var err error
		revoked, err = storePassword(ctx, q, cred, p.ID)
		return err
	})
	if err != nil {
		return err
	}
	for _, h := range revoked {
		s.revocations.publish(KindWeb, h)
	}
	return nil
}

// DeletePassword 关闭密码登录并吊销其它用密码登录的会话（当前会话保留）。
// 要求 5 分钟内验证过；没有 Passkey 时不能关闭（409 last_login_method），至少保留一种登录方式。
func (s *Service) DeletePassword(ctx context.Context, p Principal) error {
	if err := requireOwnerSession(p); err != nil {
		return err
	}
	if err := RequireRecentAuth(p, time.Now()); err != nil {
		return err
	}
	var revoked []string
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		o, err := q.GetOwner(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return newErr(http.StatusConflict, "conflict", "尚未初始化")
		}
		if err != nil {
			return err
		}
		if o.PasswordHash == nil {
			return newErr(http.StatusNotFound, "not_found", "未启用密码登录")
		}
		n, err := q.CountPasskeys(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			return newErr(http.StatusConflict, "last_login_method", "没有 Passkey 时不能关闭密码登录，请先添加一把 Passkey")
		}
		if err := q.ClearOwnerPassword(ctx); err != nil {
			return err
		}
		revoked, err = q.DeleteWebSessionsByPassword(ctx, p.ID)
		return err
	})
	if err != nil {
		return err
	}
	for _, h := range revoked {
		s.revocations.publish(KindWeb, h)
	}
	return nil
}
