package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	// PairTTL 是配对码有效期。
	PairTTL = 10 * time.Minute
	// PairInterval 是建议的轮询间隔（秒）。
	PairInterval = 2
	// pairAlphabet 去掉易混字符 0 O 1 I。
	pairAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// ValidDeviceKind 校验设备类型。
func ValidDeviceKind(kind string) bool {
	switch kind {
	case "cli", "desktop", "mobile":
		return true
	}
	return false
}

func validateDeviceFields(name, kind, platform string) error {
	n := utf8.RuneCountInString(strings.TrimSpace(name))
	if n < 1 || n > 100 {
		return newErr(http.StatusBadRequest, "invalid_request", "设备名长度须为 1–100")
	}
	if !ValidDeviceKind(kind) {
		return newErr(http.StatusBadRequest, "invalid_request", "kind 必须是 cli / desktop / mobile")
	}
	if len(platform) > 64 {
		return newErr(http.StatusBadRequest, "invalid_request", "platform 过长")
	}
	return nil
}

// CreateDevice 直接创建设备并返回明文令牌（`orynd admin create-device` 使用）。
func CreateDevice(ctx context.Context, st *store.Store, name, kind, platform string) (db.Device, string, error) {
	if err := validateDeviceFields(name, kind, platform); err != nil {
		return db.Device{}, "", err
	}
	tok := ids.Token("odt")
	dev := db.Device{
		ID: ids.New("dev"), Name: strings.TrimSpace(name), Kind: kind, Platform: platform,
		TokenHash: ids.Hash(tok), CreatedAt: time.Now().UnixMilli(),
	}
	err := st.Q.CreateDevice(ctx, db.CreateDeviceParams{
		ID: dev.ID, Name: dev.Name, Kind: dev.Kind, Platform: dev.Platform, TokenHash: dev.TokenHash, CreatedAt: dev.CreatedAt,
	})
	return dev, tok, err
}

// NormalizePairCode 把用户输入规范成 ABCD-EFGH。
func NormalizePairCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if strings.ContainsRune(pairAlphabet, r) {
			b.WriteRune(r)
		}
	}
	c := b.String()
	if len(c) != 8 {
		return c
	}
	return c[:4] + "-" + c[4:]
}

func newPairCode() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	out := make([]byte, 0, 9)
	for i, v := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, pairAlphabet[int(v)%len(pairAlphabet)])
	}
	return string(out)
}

// PairStart 是发起配对的结果。
type PairStart struct {
	Code      string
	Verifier  string
	ExpiresAt time.Time
}

// 配对设计：verifier 是 32 字节随机串，批准后设备令牌即为 `odt_<verifier>`。
// device_pairings.verifier_hash 保存的是该令牌的哈希，于是批准时就能直接以它建设备行，
// 轮询时校验 verifier 并把令牌交给发起方 —— 明文令牌从不落库，重启也不丢批准结果。
func tokenForVerifier(v string) string { return "odt_" + v }

// Requester 是配对发起方的网络信息，展示在批准页上供所有者核对。
type Requester struct {
	IP        string
	UserAgent string
}

// StartPairing 创建配对请求（免鉴权；调用方负责限流）。
func StartPairing(ctx context.Context, st *store.Store, name, kind, platform string, req Requester) (PairStart, error) {
	if err := validateDeviceFields(name, kind, platform); err != nil {
		return PairStart{}, err
	}
	now := time.Now()
	_, _ = st.Q.DeleteExpiredDevicePairings(ctx, now.UnixMilli())
	verifier := strings.TrimPrefix(ids.Token("odt"), "odt_")
	exp := now.Add(PairTTL)
	for range 5 {
		code := newPairCode()
		err := st.Q.CreateDevicePairingWithMeta(ctx, db.CreateDevicePairingWithMetaParams{
			Code: code, VerifierHash: ids.Hash(tokenForVerifier(verifier)), Name: strings.TrimSpace(name),
			Kind: kind, Platform: platform, CreatedAt: now.UnixMilli(), ExpiresAt: exp.UnixMilli(),
			RequesterIp: truncate(req.IP, 64), UserAgent: truncate(req.UserAgent, 512),
		})
		if err == nil {
			return PairStart{Code: code, Verifier: verifier, ExpiresAt: exp}, nil
		}
		if !strings.Contains(err.Error(), "UNIQUE") && !strings.Contains(err.Error(), "PRIMARY KEY") {
			return PairStart{}, err
		}
	}
	return PairStart{}, errors.New("生成配对码失败")
}

var errPairNotFound = newErr(http.StatusNotFound, "not_found", "配对码不存在或已过期")

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// PendingPairing 返回待批准的配对请求（批准页展示设备名、发起方 IP、User-Agent 与时间）。
// 已过期或不存在返回 404，已批准返回 409。
func PendingPairing(ctx context.Context, st *store.Store, code string) (db.DevicePairing, error) {
	p, err := st.Q.GetDevicePairing(ctx, NormalizePairCode(code))
	if errors.Is(err, sql.ErrNoRows) {
		return p, errPairNotFound
	}
	if err != nil {
		return p, err
	}
	if p.ExpiresAt <= time.Now().UnixMilli() {
		return p, errPairNotFound
	}
	if p.ApprovedDeviceID != nil {
		return p, newErr(http.StatusConflict, "conflict", "该配对码已被批准")
	}
	return p, nil
}

// ApprovePairing 批准配对并创建设备（需网页会话，由调用方校验）。
//
// 防钓鱼：verifyUrl 不预填配对码，所有者须手动输入设备上显示的码，批准页展示发起方 IP /
// User-Agent / 时间（PendingPairing）。已知局限：批准时不要求重新做一次 WebAuthn 用户验证，
// 已登录网页会话被劫持（如 XSS）时攻击者可批准自己的配对请求。
func ApprovePairing(ctx context.Context, st *store.Store, code string) (db.Device, error) {
	code = NormalizePairCode(code)
	var dev db.Device
	err := st.Tx(ctx, func(q *db.Queries) error {
		p, err := q.GetDevicePairing(ctx, code)
		if errors.Is(err, sql.ErrNoRows) {
			return errPairNotFound
		}
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		if p.ExpiresAt <= now {
			return errPairNotFound
		}
		if p.ApprovedDeviceID != nil {
			return newErr(http.StatusConflict, "conflict", "该配对码已被批准")
		}
		dev = db.Device{ID: ids.New("dev"), Name: p.Name, Kind: p.Kind, Platform: p.Platform, TokenHash: p.VerifierHash, CreatedAt: now}
		if err := q.CreateDevice(ctx, db.CreateDeviceParams{
			ID: dev.ID, Name: dev.Name, Kind: dev.Kind, Platform: dev.Platform, TokenHash: dev.TokenHash, CreatedAt: now,
		}); err != nil {
			return err
		}
		_, err = q.ApproveDevicePairing(ctx, db.ApproveDevicePairingParams{ApprovedDeviceID: &dev.ID, Code: code, Now: now})
		return err
	})
	return dev, err
}

// PollPairing 由发起方轮询。未批准返回 approved=false；批准后返回设备与令牌并删除配对记录（令牌只交付一次）。
func PollPairing(ctx context.Context, st *store.Store, code, verifier string) (db.Device, string, bool, error) {
	code = NormalizePairCode(code)
	var dev db.Device
	var approved bool
	tok := tokenForVerifier(verifier)
	err := st.Tx(ctx, func(q *db.Queries) error {
		p, err := q.GetDevicePairing(ctx, code)
		if errors.Is(err, sql.ErrNoRows) {
			return errPairNotFound
		}
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(p.VerifierHash), []byte(ids.Hash(tok))) != 1 {
			return newErr(http.StatusForbidden, "forbidden", "verifier 不匹配")
		}
		if p.ApprovedDeviceID == nil {
			if p.ExpiresAt <= time.Now().UnixMilli() {
				_ = q.DeleteDevicePairing(ctx, code)
				return errPairNotFound
			}
			return nil
		}
		dev, err = q.GetDevice(ctx, *p.ApprovedDeviceID)
		if err != nil {
			return err
		}
		if err := q.DeleteDevicePairing(ctx, code); err != nil {
			return err
		}
		if dev.RevokedAt != nil {
			return newErr(http.StatusForbidden, "forbidden", "设备已被吊销")
		}
		approved = true
		return nil
	})
	if err != nil || !approved {
		return db.Device{}, "", false, err
	}
	return dev, tok, true, nil
}
