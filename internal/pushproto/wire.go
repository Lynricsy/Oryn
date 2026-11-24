package pushproto

import (
	"regexp"
	"time"
)

// 网关路径。
const (
	PathInfo       = "/v1/info"
	PathChallenges = "/v1/challenges"
	PathGrants     = "/v1/grants"
	PathVerify     = "/v1/grants/verify"
	PathPush       = "/v1/push"
)

// GrantPath 是单个授权的路径（DELETE），也是 orynd 订阅 endpoint 的路径部分。
func GrantPath(grant string) string { return PathGrants + "/" + grant }

// 通道。
const (
	ChannelAPNs = "apns"
	ChannelFCM  = "fcm"
)

// 网关错误码（ErrorBody.Code）。
const (
	CodeMalformed          = "malformed"
	CodeBadSignature       = "bad_signature"
	CodeClockSkew          = "clock_skew"
	CodeForbidden          = "forbidden"
	CodeHostMismatch       = "host_mismatch"
	CodeGrantRevoked       = "grant_revoked"
	CodeDeviceUnregistered = "device_unregistered"
	CodeAppMismatch        = "app_mismatch"
	CodeChallengeInvalid   = "challenge_invalid"
	CodeTooBig             = "too_big"
	CodeRateLimited        = "rate_limited"
	CodeUpstream           = "upstream"
	CodeUnavailable        = "unavailable"
	CodeNotFound           = "not_found"
)

// 字段上限与格式。
const (
	// MaxCipher 是 PushRequest.E 的字符上限：明文 2800 字节 + 103 字节开销，base64url 后 3871。
	MaxCipher = 3872
	// MaxPlaintext 是 orynd 加密前的明文上限（与 FCM 直发相同）。
	MaxPlaintext = 2800
	// TTL 是通知的离线保留时长。
	TTL = 24 * time.Hour
)

var (
	// IDPattern 匹配 grant / challengeId / nonce：128 位随机数的 base64url（22 字符）。
	IDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)
	// NotificationIDPattern 匹配 PushRequest.ID。
	NotificationIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
	// CollapsePattern 匹配 PushRequest.Collapse。
	CollapsePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// APNsTokenPattern 匹配 APNs 设备令牌（小写十六进制）。
	APNsTokenPattern = regexp.MustCompile(`^[0-9a-f]{64,200}$`)
	// CipherPattern 匹配无填充 base64url。
	CipherPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// Info 是 GET /v1/info：网关自报的兼容性信息（不作为信任依据）。
type Info struct {
	V    int       `json:"v"`
	APNs *InfoAPNs `json:"apns,omitempty"`
	FCM  *InfoFCM  `json:"fcm,omitempty"`
}

// InfoAPNs 是网关服务的 APNs topic 与环境。
type InfoAPNs struct {
	Topic        string   `json:"topic"`
	Environments []string `json:"environments"`
}

// InfoFCM 是网关服务的 Firebase 项目。
type InfoFCM struct {
	ProjectID string `json:"projectId"`
}

// ChallengeRequest 是 POST /v1/challenges（设备签名）。
type ChallengeRequest struct {
	Channel     string `json:"channel"`
	AppID       string `json:"appId"`
	Environment string `json:"environment,omitempty"`
	Token       string `json:"token"`
}

// ChallengeResponse 是 202 应答。
type ChallengeResponse struct {
	ChallengeID string `json:"challengeId"`
}

// GrantRequest 是 POST /v1/grants（设备签名）。
type GrantRequest struct {
	ChallengeID string `json:"challengeId"`
	Nonce       string `json:"nonce"`
	HostKey     string `json:"hostKey"`
}

// GrantResponse 是 201 应答。
type GrantResponse struct {
	Grant     string `json:"grant"`
	CreatedAt int64  `json:"createdAt"`
}

// VerifyRequest 是 POST /v1/grants/verify（主机签名）。
type VerifyRequest struct {
	Grant string `json:"grant"`
}

// PushRequest 是 POST /v1/push（主机签名）。
type PushRequest struct {
	Grant    string `json:"grant"`
	ID       string `json:"id"`
	Collapse string `json:"collapse,omitempty"`
	Withdraw bool   `json:"withdraw,omitempty"`
	E        string `json:"e"`
}

// ErrorBody 是错误应答体。
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ChallengeKind 是 challenge 推送载荷里的 kind。
const ChallengeKind = "gateway_challenge"

// DataVersion 是通知推送载荷里的 v（与 FCM 直发相同：2 = 载荷加密）。
const DataVersion = "2"
