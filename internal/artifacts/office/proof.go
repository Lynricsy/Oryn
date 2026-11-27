package office

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// ProofMaxAge 是 X-WOPI-TimeStamp 与当前时刻的最大偏差。
const ProofMaxAge = 20 * time.Minute

// ProofKeys 是 discovery 里的验签公钥（当前与上一把；Old 可为 nil）。
type ProofKeys struct {
	Current, Old *rsa.PublicKey
}

func parseProofKeys(mod, exp, oldMod, oldExp string) (*ProofKeys, error) {
	cur, err := rsaKey(mod, exp)
	if err != nil {
		return nil, fmt.Errorf("discovery 的 proof-key 无效：%w", err)
	}
	keys := &ProofKeys{Current: cur}
	if oldMod != "" {
		if keys.Old, err = rsaKey(oldMod, oldExp); err != nil {
			return nil, fmt.Errorf("discovery 的旧 proof-key 无效：%w", err)
		}
	}
	return keys, nil
}

// rsaKey 用 base64 的大端模数与指数构造 RSA 公钥。
func rsaKey(mod, exp string) (*rsa.PublicKey, error) {
	m, err := base64.StdEncoding.DecodeString(mod)
	if err != nil {
		return nil, err
	}
	e, err := base64.StdEncoding.DecodeString(exp)
	if err != nil {
		return nil, err
	}
	if len(m) == 0 || len(e) == 0 || len(e) > 4 {
		return nil, errors.New("模数或指数为空")
	}
	var ev int
	for _, b := range e {
		ev = ev<<8 | int(b)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(m), E: ev}, nil
}

// TicksTime 把 X-WOPI-TimeStamp（.NET ticks：0001-01-01 起的 100 纳秒数）换成时刻。
func TicksTime(ticks int64) time.Time {
	const unixEpochTicks = 621355968000000000
	d := ticks - unixEpochTicks
	return time.Unix(d/10_000_000, d%10_000_000*100).UTC()
}

// ProofData 返回 WOPI 验签的原文：依次是 4 字节大端长度 + 访问令牌、4 字节长度 + 大写的完整请求地址、
// 4 字节长度（8）+ 8 字节大端时间戳。
func ProofData(token, url string, ticks int64) []byte {
	u := strings.ToUpper(url)
	b := make([]byte, 0, 4+len(token)+4+len(u)+4+8)
	b = binary.BigEndian.AppendUint32(b, uint32(len(token)))
	b = append(b, token...)
	b = binary.BigEndian.AppendUint32(b, uint32(len(u)))
	b = append(b, u...)
	b = binary.BigEndian.AppendUint32(b, 8)
	return binary.BigEndian.AppendUint64(b, uint64(ticks))
}

// Verify 校验一次 WOPI 请求的签名：proof / proofOld 是 X-WOPI-Proof / X-WOPI-ProofOld 的 base64 值；
// 按 WOPI 约定依次接受「当前公钥验 Proof」「当前公钥验 ProofOld」「旧公钥验 Proof」。时间戳超出 ProofMaxAge 也算失败。
func (k *ProofKeys) Verify(token, url string, ticks int64, proof, proofOld string, now time.Time) error {
	if d := now.Sub(TicksTime(ticks)); d > ProofMaxAge || d < -ProofMaxAge {
		return errors.New("X-WOPI-TimeStamp 超出 20 分钟")
	}
	sum := sha256.Sum256(ProofData(token, url, ticks))
	check := func(key *rsa.PublicKey, sig string) bool {
		if key == nil || sig == "" {
			return false
		}
		b, err := base64.StdEncoding.DecodeString(sig)
		return err == nil && rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], b) == nil
	}
	if check(k.Current, proof) || check(k.Current, proofOld) || check(k.Old, proof) {
		return nil
	}
	return errors.New("X-WOPI-Proof 验签失败")
}
