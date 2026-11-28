package api_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// h2TOTP 按 RFC 6238（SHA1、6 位、30 秒）独立计算验证码，不复用被测实现。
func h2TOTP(t *testing.T, secret string, step int64) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	require.NoError(t, err)
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

// h2TOTPStep 返回当前时间步；离下一步不足 5 秒时等到下一步开始，保证用例期间 step-1…step+1 都在容忍窗口内。
func h2TOTPStep() int64 {
	now := time.Now()
	if rem := 30 - now.Unix()%30; rem < 5 {
		time.Sleep(time.Duration(rem)*time.Second + 100*time.Millisecond)
	}
	return time.Now().Unix() / 30
}

type h2Enrollment struct {
	EnrollmentID string
	Secret       string
	OtpauthURI   string
}

// 密码 + TOTP：初始化、登录、防重放、重新验证、设置页重设与关闭、恢复流程，以及「至少保留一种登录方式」。
func TestPasswordTotpLogin(t *testing.T) {
	e := newH2Env(t)
	ctx := context.Background()
	step := h2TOTPStep()

	setupURL, _, err := auth.EnsureSetupToken(ctx, e.st, e.dataDir, h2PublicURL)
	require.NoError(t, err)
	u, err := url.Parse(setupURL)
	require.NoError(t, err)
	frag, err := url.ParseQuery(u.Fragment)
	require.NoError(t, err)

	finish := func(path string, cred h2Cred, en h2Enrollment, pw, code string) h2Resp {
		t.Helper()
		return e.do("POST", path, cred, map[string]string{"enrollmentId": en.EnrollmentID, "password": pw, "code": code})
	}
	login := func(pw, code string) h2Resp {
		t.Helper()
		return e.do("POST", "/api/v1/auth/password/login", h2Cred{}, map[string]string{"password": pw, "code": code})
	}

	// 1. 只用密码 + TOTP 初始化
	assert.Equal(t, 401, e.do("POST", "/api/v1/auth/password/setup/begin", h2Cred{}, map[string]string{}).status, "既无令牌也无恢复会话")
	r := e.do("POST", "/api/v1/auth/password/setup/begin", h2Cred{}, map[string]string{"token": frag.Get("token")})
	require.Equal(t, 200, r.status, string(r.body))
	var en h2Enrollment
	r.json(t, &en)
	otp, err := url.Parse(en.OtpauthURI)
	require.NoError(t, err)
	assert.Equal(t, "otpauth", otp.Scheme)
	assert.Equal(t, en.Secret, otp.Query().Get("secret"))
	assert.Equal(t, "Oryn", otp.Query().Get("issuer"))
	assert.Contains(t, otp.Path, h2RPID)

	const pw1 = "correct horse battery"
	assert.Equal(t, 400, finish("/api/v1/auth/password/setup/finish", h2Cred{}, en, pw1, h2TOTP(t, en.Secret, step+5)).status, "窗口外的验证码")
	assert.Equal(t, 400, finish("/api/v1/auth/password/setup/finish", h2Cred{}, en, "short", h2TOTP(t, en.Secret, step-1)).status, "密码过短")
	r = finish("/api/v1/auth/password/setup/finish", h2Cred{}, en, pw1, h2TOTP(t, en.Secret, step-1))
	require.Equal(t, 200, r.status, string(r.body))
	var fin struct {
		Me struct {
			PasskeyCount    int
			PasswordEnabled bool
		}
		RecoveryCodes []string
	}
	r.json(t, &fin)
	assert.Equal(t, 0, fin.Me.PasskeyCount)
	assert.True(t, fin.Me.PasswordEnabled)
	require.Len(t, fin.RecoveryCodes, 10)
	s0 := r.session()
	require.NotEmpty(t, s0)
	assert.Equal(t, 400, finish("/api/v1/auth/password/setup/finish", h2Cred{}, en, pw1, h2TOTP(t, en.Secret, step)).status, "登记仪式一次性")
	require.Equal(t, 204, e.do("POST", "/api/v1/auth/logout", h2Cred{cookie: s0, csrf: true}, nil).status)

	// 2. 登录：登记用过的验证码不能再用；错一项统一 401 且不消耗时间步
	assert.Equal(t, 401, login(pw1, h2TOTP(t, en.Secret, step-1)).status, "验证码重放")
	r = login("wrong password!", h2TOTP(t, en.Secret, step))
	assert.Equal(t, 401, r.status)
	wrongPw := string(r.body)
	r = login(pw1, "12345x")
	assert.Equal(t, 401, r.status)
	assert.Equal(t, wrongPw, string(r.body), "密码错与验证码错的响应一致")
	r = login(pw1, " "+h2TOTP(t, en.Secret, step)+" ")
	require.Equal(t, 200, r.status, string(r.body))
	s1 := r.session()
	r = login(pw1, h2TOTP(t, en.Secret, step+1))
	require.Equal(t, 200, r.status, string(r.body))
	s2 := r.session()
	s1c := h2Cred{cookie: s1, csrf: true}
	assert.Equal(t, 200, e.do("POST", "/api/v1/auth/recovery-codes", s1c, nil).status, "登录即算一次验证")

	// 3. 没有 Passkey 时不能关闭密码登录
	r = e.do("DELETE", "/api/v1/auth/password", s1c, nil)
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "last_login_method", r.code(t))

	// 4. 设置页重设：新密钥、新密码；其它用密码登录的会话被吊销，当前会话保留
	var revoked []string
	e.svc.Subscribe(func(kind, id string) { revoked = append(revoked, kind+":"+id) })
	r = e.do("POST", "/api/v1/auth/password/begin", s1c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var en2 h2Enrollment
	r.json(t, &en2)
	assert.NotEqual(t, en.Secret, en2.Secret)
	const pw2 = "a different passphrase"
	assert.Equal(t, 401, finish("/api/v1/auth/password/finish", h2Cred{cookie: s2, csrf: true}, en2, pw2, h2TOTP(t, en2.Secret, step-1)).status, "仪式绑定发起的会话")
	require.Equal(t, 204, finish("/api/v1/auth/password/finish", s1c, en2, pw2, h2TOTP(t, en2.Secret, step-1)).status)
	assert.Equal(t, 401, e.do("GET", "/api/v1/me", h2Cred{cookie: s2}, nil).status)
	assert.Equal(t, 200, e.do("GET", "/api/v1/me", h2Cred{cookie: s1}, nil).status)
	assert.Contains(t, revoked, "web:"+ids.Hash(s2))
	assert.Equal(t, 401, login(pw1, h2TOTP(t, en.Secret, step)).status, "旧密码与旧密钥作废")

	// 5. 重新验证
	r = e.do("POST", "/api/v1/auth/password/reauth", s1c, map[string]string{"password": pw1, "code": h2TOTP(t, en2.Secret, step)})
	assert.Equal(t, 403, r.status, "复核失败不是会话失效，不能用 401")
	assert.Equal(t, "reauth_failed", r.code(t))
	assert.Equal(t, 200, e.do("GET", "/api/v1/me", h2Cred{cookie: s1}, nil).status)
	r = e.do("POST", "/api/v1/auth/password/reauth", s1c, map[string]string{"password": pw2, "code": h2TOTP(t, en2.Secret, step)})
	require.Equal(t, 200, r.status, string(r.body))
	var re struct{ VerifiedAt, ValidUntil int64 }
	r.json(t, &re)
	assert.Equal(t, int64(5*time.Minute/time.Millisecond), re.ValidUntil-re.VerifiedAt)

	// 6. 有密码时可以删掉最后一把 Passkey；有 Passkey 后可以关闭密码，之后最后一把又不能删
	addPasskey := func() string {
		t.Helper()
		r := e.do("POST", "/api/v1/auth/passkeys/begin", s1c, map[string]string{"name": "钥匙"})
		require.Equal(t, 200, r.status, string(r.body))
		var cer h2Ceremony
		r.json(t, &cer)
		r = e.do("POST", "/api/v1/auth/passkeys/finish", s1c, map[string]any{"ceremonyId": cer.CeremonyID, "credential": newH2Authenticator(t).create(cer.Options)})
		require.Equal(t, 200, r.status, string(r.body))
		var pk struct{ ID string }
		r.json(t, &pk)
		return pk.ID
	}
	require.Equal(t, 204, e.do("DELETE", "/api/v1/auth/passkeys/"+addPasskey(), s1c, nil).status)
	last := addPasskey()
	require.Equal(t, 204, e.do("DELETE", "/api/v1/auth/password", s1c, nil).status)
	r = e.do("GET", "/api/v1/me", h2Cred{cookie: s1}, nil)
	assert.Contains(t, string(r.body), `"passwordEnabled":false`)
	assert.Equal(t, 401, login(pw2, h2TOTP(t, en2.Secret, step+1)).status)
	r = e.do("DELETE", "/api/v1/auth/passkeys/"+last, s1c, nil)
	assert.Equal(t, 409, r.status)
	assert.Equal(t, "last_passkey", r.code(t))

	// 7. 恢复流程：恢复会话可以改用密码 + TOTP，完成后恢复会话删除（初始化时的恢复码在第 2 步已重新生成作废，这里再生成一次）
	r = e.do("POST", "/api/v1/auth/recovery-codes", s1c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	var codes struct{ RecoveryCodes []string }
	r.json(t, &codes)
	r = e.do("POST", "/api/v1/auth/recovery", h2Cred{}, map[string]string{"code": codes.RecoveryCodes[0]})
	require.Equal(t, 200, r.status, string(r.body))
	rec := h2Cred{cookie: r.session(), csrf: true}
	assert.Equal(t, 403, e.do("POST", "/api/v1/auth/password/begin", rec, nil).status, "恢复会话不能走设置页")
	r = e.do("POST", "/api/v1/auth/password/setup/begin", rec, map[string]string{})
	require.Equal(t, 200, r.status, string(r.body))
	var en3 h2Enrollment
	r.json(t, &en3)
	r = finish("/api/v1/auth/password/setup/finish", rec, en3, pw1, h2TOTP(t, en3.Secret, step))
	require.Equal(t, 200, r.status, string(r.body))
	fin.RecoveryCodes = nil
	r.json(t, &fin)
	assert.True(t, fin.Me.PasswordEnabled)
	assert.Empty(t, fin.RecoveryCodes)
	assert.Equal(t, 401, e.do("GET", "/api/v1/me", rec, nil).status, "恢复会话已删除")
	assert.Equal(t, 200, login(pw1, h2TOTP(t, en3.Secret, step+1)).status)
}

// 会话通过中间件后、写入新 Passkey 前被吊销（如另一端重设或关闭密码登录）：进行中的登记不能留下永久凭据。
func TestAddPasskeyRejectsRevokedSession(t *testing.T) {
	e := newH2Env(t)
	ctx := context.Background()
	require.NoError(t, e.st.Q.CreateOwner(ctx, db.CreateOwnerParams{DisplayName: "Owner", UserHandle: []byte("h"), CreatedAt: 1}))
	sess, err := auth.CreateWebSession(ctx, e.st, "")
	require.NoError(t, err)
	now := time.Now().UnixMilli()
	require.NoError(t, e.st.Q.TouchWebSessionVerified(ctx, db.TouchWebSessionVerifiedParams{VerifiedAt: &now, IDHash: ids.Hash(sess)}))

	r := e.do("POST", "/api/v1/auth/passkeys/begin", h2Cred{cookie: sess, csrf: true}, map[string]string{"name": "钥匙"})
	require.Equal(t, 200, r.status, string(r.body))
	var cer h2Ceremony
	r.json(t, &cer)
	cred, err := json.Marshal(newH2Authenticator(t).create(cer.Options))
	require.NoError(t, err)

	// 中间件已放行的旧 principal；随后会话被删
	p := auth.Principal{Kind: auth.KindWeb, ID: ids.Hash(sess), VerifiedAt: now}
	require.NoError(t, e.st.Q.DeleteWebSession(ctx, p.ID))
	_, err = e.svc.FinishAddPasskey(ctx, p, cer.CeremonyID, cred)
	var ae *auth.Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, 401, ae.Status)
	n, err := e.st.Q.CountPasskeys(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "吊销后的请求没有写入 Passkey")
}
