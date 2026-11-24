package pushgw

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/Lynricsy/Oryn/internal/apns"
	"github.com/Lynricsy/Oryn/internal/fcm"
	"github.com/Lynricsy/Oryn/internal/pushproto"
	"github.com/Lynricsy/Oryn/internal/version"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, pushproto.ErrorBody{Code: code, Message: msg})
}

// writeRetry 回 429 / 502 / 503，wait > 0 时带 Retry-After（整秒，向上取整）。
func writeRetry(w http.ResponseWriter, status int, code, msg string, wait time.Duration) {
	if wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	}
	writeError(w, status, code, msg)
}

func (s *Server) unavailable(w http.ResponseWriter, err error) {
	s.log.Error("推送网关内部错误", "err", err)
	writeError(w, http.StatusServiceUnavailable, pushproto.CodeUnavailable, "gateway temporarily unavailable")
}

// signed 读取请求体并验签；want 非空时要求该种签名者。签名者公钥规范化为 base64url 无填充，
// 以便与授权表里的值逐字比较。失败时已写好应答。
func (s *Server) signed(w http.ResponseWriter, r *http.Request, want string) ([]byte, pushproto.Signer, bool) {
	body, err := pushproto.ReadBody(r)
	if errors.Is(err, pushproto.ErrTooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, pushproto.CodeTooBig, "request body exceeds 8 KiB")
		return nil, pushproto.Signer{}, false
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, pushproto.CodeMalformed, "cannot read request body")
		return nil, pushproto.Signer{}, false
	}
	signer, err := pushproto.Verify(r, s.cfg.PublicOrigin, body, s.now())
	switch {
	case errors.Is(err, pushproto.ErrClockSkew):
		writeError(w, http.StatusUnauthorized, pushproto.CodeClockSkew, "timestamp differs from gateway clock by more than 5 minutes")
		return nil, signer, false
	case errors.Is(err, pushproto.ErrBadSignature):
		writeError(w, http.StatusUnauthorized, pushproto.CodeBadSignature, "signature does not verify")
		return nil, signer, false
	case err != nil:
		writeError(w, http.StatusBadRequest, pushproto.CodeMalformed, "request needs exactly one of host or device signature headers and no query string")
		return nil, signer, false
	}
	if want != "" && signer.Kind != want {
		writeError(w, http.StatusBadRequest, pushproto.CodeMalformed, "this endpoint requires a "+want+" signature")
		return nil, signer, false
	}
	signer.Key = canonicalKey(signer)
	if signer.Kind == pushproto.SignerHost {
		logOf(r).host = hostHash(signer.Key)
	}
	return body, signer, true
}

// canonicalKey 把验签通过的公钥重新编码：base64url 解码不检查末尾多余的位，同一把钥匙可能有多种写法。
func canonicalKey(sg pushproto.Signer) string {
	if sg.Kind == pushproto.SignerHost {
		pub, err := pushproto.ParseHostKey(sg.Key)
		if err != nil {
			return sg.Key
		}
		return pushproto.HostKeyString(pub)
	}
	pub, err := pushproto.ParseDeviceKey(sg.Key)
	if err != nil {
		return sg.Key
	}
	raw, err := pub.Bytes()
	if err != nil {
		return sg.Key
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decode 解析 JSON 请求体；失败时回 400 malformed。
func decode(w http.ResponseWriter, body []byte, v any) bool {
	if err := json.Unmarshal(body, v); err != nil {
		writeError(w, http.StatusBadRequest, pushproto.CodeMalformed, "request body is not valid JSON for this endpoint")
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": version.Version})
}

func (s *Server) info(w http.ResponseWriter, _ *http.Request) {
	in := pushproto.Info{V: 1}
	if s.cfg.APNs.Enabled() {
		in.APNs = &pushproto.InfoAPNs{Topic: s.cfg.APNs.Topic, Environments: s.cfg.APNs.Environments}
	}
	if s.up.account != nil {
		in.FCM = &pushproto.InfoFCM{ProjectID: s.up.account.ProjectID}
	}
	writeJSON(w, http.StatusOK, in)
}

// createChallenge：POST /v1/challenges（设备签名）。核对通道与 App 标识后把 challengeId + nonce 推给令牌。
func (s *Server) createChallenge(w http.ResponseWriter, r *http.Request) {
	if ok, wait := s.limiter.allow(s.now(), check{s.limits.challengeIP, clientIP(r)}); !ok {
		writeRetry(w, http.StatusTooManyRequests, pushproto.CodeRateLimited, "too many challenges from this address", wait)
		return
	}
	body, signer, ok := s.signed(w, r, pushproto.SignerDevice)
	if !ok {
		return
	}
	var req pushproto.ChallengeRequest
	if !decode(w, body, &req) {
		return
	}
	ch := challenge{deviceKey: signer.Key, channel: req.Channel, appID: req.AppID, token: req.Token}
	switch req.Channel {
	case pushproto.ChannelFCM:
		if !fcm.ValidToken(req.Token) {
			writeError(w, http.StatusBadRequest, pushproto.CodeMalformed, "invalid FCM registration token")
			return
		}
		if !s.cfg.FCM.Enabled() || req.AppID != s.cfg.FCM.AppID {
			writeError(w, http.StatusConflict, pushproto.CodeAppMismatch, "this gateway does not serve FCM for this app")
			return
		}
	case pushproto.ChannelAPNs:
		if !pushproto.APNsTokenPattern.MatchString(req.Token) {
			writeError(w, http.StatusBadRequest, pushproto.CodeMalformed, "invalid APNs device token")
			return
		}
		ch.env = req.Environment
		if ch.env == "" {
			ch.env = apns.EnvProduction
		}
		if !s.cfg.APNs.Enabled() || req.AppID != s.cfg.APNs.Topic || !slices.Contains(s.cfg.APNs.Environments, ch.env) {
			writeError(w, http.StatusConflict, pushproto.CodeAppMismatch, "this gateway does not serve APNs for this app or environment")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, pushproto.CodeMalformed, `channel must be "apns" or "fcm"`)
		return
	}
	added, err := s.challenges.add(ch, s.now())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, pushproto.CodeUnavailable, "too many pending challenges")
		return
	}
	res := s.up.sendChallenge(r.Context(), added)
	logOf(r).upstream = res.code
	if res.outcome != sentOK {
		s.challenges.remove(added.id)
	}
	switch res.outcome {
	case sentOK:
		writeJSON(w, http.StatusAccepted, pushproto.ChallengeResponse{ChallengeID: added.id})
	case sentUnregistered:
		writeError(w, http.StatusGone, pushproto.CodeDeviceUnregistered, "push service reports the token is no longer valid")
	case sentMismatch:
		writeError(w, http.StatusConflict, pushproto.CodeAppMismatch, "push service reports the token does not belong to this app")
	case sentUnavailable:
		writeError(w, http.StatusServiceUnavailable, pushproto.CodeUnavailable, "channel is not configured")
	default:
		writeRetry(w, http.StatusBadGateway, pushproto.CodeUpstream, "push service rejected or did not answer the challenge", res.retryAfter)
	}
}

// createGrant：POST /v1/grants（设备签名）。凭 challenge 证明过的令牌登记授权。
func (s *Server) createGrant(w http.ResponseWriter, r *http.Request) {
	body, signer, ok := s.signed(w, r, pushproto.SignerDevice)
	if !ok {
		return
	}
	var req pushproto.GrantRequest
	if !decode(w, body, &req) {
		return
	}
	hostPub, err := pushproto.ParseHostKey(req.HostKey)
	if !pushproto.IDPattern.MatchString(req.ChallengeID) || !pushproto.IDPattern.MatchString(req.Nonce) || err != nil {
		writeError(w, http.StatusBadRequest, pushproto.CodeMalformed, "challengeId, nonce or hostKey is malformed")
		return
	}
	hostKey := pushproto.HostKeyString(hostPub)
	logOf(r).host = hostHash(hostKey)
	ch, ok := s.challenges.take(req.ChallengeID, req.Nonce, signer.Key, s.now())
	if !ok {
		writeError(w, http.StatusConflict, pushproto.CodeChallengeInvalid, "challenge does not exist, has expired, was used or belongs to another device")
		return
	}
	g, err := s.store.Put(r.Context(), Grant{
		DeviceKey: signer.Key, HostKey: hostKey, Channel: ch.channel, AppID: ch.appID, Environment: ch.env, Token: ch.token,
	}, s.now())
	if err != nil {
		s.unavailable(w, err)
		return
	}
	logOf(r).grant = g.ID
	writeJSON(w, http.StatusCreated, pushproto.GrantResponse{Grant: g.ID, CreatedAt: g.CreatedAt})
}

// deleteGrant：DELETE /v1/grants/{id}（设备或主机签名）。
func (s *Server) deleteGrant(w http.ResponseWriter, r *http.Request) {
	_, signer, ok := s.signed(w, r, "")
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !pushproto.IDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, pushproto.CodeMalformed, "grant id is malformed")
		return
	}
	logOf(r).grant = id
	g, err := s.store.Get(r.Context(), id)
	if err != nil {
		s.unavailable(w, err)
		return
	}
	if g == nil || g.RevokedAt != 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !(signer.Kind == pushproto.SignerDevice && signer.Key == g.DeviceKey) && !(signer.Kind == pushproto.SignerHost && signer.Key == g.HostKey) {
		writeError(w, http.StatusForbidden, pushproto.CodeForbidden, "signer is neither the grant's device nor its host")
		return
	}
	if err := s.store.Revoke(r.Context(), id, s.now()); err != nil {
		s.unavailable(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// activeGrant 读取属于主机 hostKey 的有效授权；失败时已写好应答（410 / 403 / 503）。
func (s *Server) activeGrant(w http.ResponseWriter, r *http.Request, id, hostKey string) (*Grant, bool) {
	g, err := s.store.Get(r.Context(), id)
	if err != nil {
		s.unavailable(w, err)
		return nil, false
	}
	if g == nil || g.RevokedAt != 0 {
		writeError(w, http.StatusGone, pushproto.CodeGrantRevoked, "grant does not exist or was revoked")
		return nil, false
	}
	if g.HostKey != hostKey {
		writeError(w, http.StatusForbidden, pushproto.CodeHostMismatch, "grant belongs to another host key")
		return nil, false
	}
	return g, true
}

// verifyGrant：POST /v1/grants/verify（主机签名）。
func (s *Server) verifyGrant(w http.ResponseWriter, r *http.Request) {
	body, signer, ok := s.signed(w, r, pushproto.SignerHost)
	if !ok {
		return
	}
	var req pushproto.VerifyRequest
	if !decode(w, body, &req) {
		return
	}
	if !pushproto.IDPattern.MatchString(req.Grant) {
		writeError(w, http.StatusBadRequest, pushproto.CodeMalformed, "grant is malformed")
		return
	}
	logOf(r).grant = req.Grant
	if _, ok := s.activeGrant(w, r, req.Grant, signer.Key); !ok {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// push：POST /v1/push（主机签名）。把密文转给授权的令牌；上游报令牌失效则吊销授权。
func (s *Server) push(w http.ResponseWriter, r *http.Request) {
	body, signer, ok := s.signed(w, r, pushproto.SignerHost)
	if !ok {
		return
	}
	var req pushproto.PushRequest
	if !decode(w, body, &req) {
		return
	}
	if len(req.E) > pushproto.MaxCipher {
		writeError(w, http.StatusRequestEntityTooLarge, pushproto.CodeTooBig, "e exceeds 3872 characters")
		return
	}
	if !pushproto.IDPattern.MatchString(req.Grant) || !pushproto.NotificationIDPattern.MatchString(req.ID) ||
		(req.Collapse != "" && !pushproto.CollapsePattern.MatchString(req.Collapse)) || !pushproto.CipherPattern.MatchString(req.E) {
		writeError(w, http.StatusBadRequest, pushproto.CodeMalformed, "grant, id, collapse or e is malformed")
		return
	}
	logOf(r).grant = req.Grant
	now := s.now()
	if ok, wait := s.limiter.allow(now, check{s.limits.host, signer.Key}); !ok {
		writeRetry(w, http.StatusTooManyRequests, pushproto.CodeRateLimited, "too many pushes from this host key", wait)
		return
	}
	g, ok := s.activeGrant(w, r, req.Grant, signer.Key)
	if !ok {
		return
	}
	if ok, wait := s.limiter.allow(now, check{s.limits.grantMinute, g.ID}, check{s.limits.grantDay, g.ID}); !ok {
		writeRetry(w, http.StatusTooManyRequests, pushproto.CodeRateLimited, "too many pushes for this grant", wait)
		return
	}
	if g.Channel == pushproto.ChannelAPNs && req.Withdraw {
		// iOS 没有可撤回的按钮：不联系 APNs
		s.touch(r, g)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	res := s.up.sendNotification(r.Context(), g, req)
	logOf(r).upstream = res.code
	switch res.outcome {
	case sentOK:
		s.touch(r, g)
		w.WriteHeader(http.StatusAccepted)
	case sentUnregistered, sentMismatch:
		if err := s.store.Revoke(r.Context(), g.ID, s.now()); err != nil {
			s.unavailable(w, err)
			return
		}
		writeError(w, http.StatusGone, pushproto.CodeDeviceUnregistered, "push service reports the device token is no longer valid; grant revoked")
	case sentUnavailable:
		writeError(w, http.StatusServiceUnavailable, pushproto.CodeUnavailable, "channel is not configured")
	default:
		writeRetry(w, http.StatusBadGateway, pushproto.CodeUpstream, "push service rejected or did not answer", res.retryAfter)
	}
}

// touch 记录授权使用时间（至多每小时写一次）；失败只记日志。
func (s *Server) touch(r *http.Request, g *Grant) {
	if err := s.store.Touch(r.Context(), g, s.now()); err != nil {
		s.log.Warn("记录授权使用时间失败", "grant", g.ID, "err", err)
	}
}
