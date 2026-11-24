package pushproto

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 重新生成向量：go test ./internal/pushproto -run TestVectors -update-vectors
var updateVectors = flag.Bool("update-vectors", false, "重新生成 schema/pushgw/sign-vectors.json")

const vectorFile = "../../schema/pushgw/sign-vectors.json"

// vectors 是 Go 与 Rust 两侧共用的签名测试向量。
type vectors struct {
	// HostSeed 是 32 字节 Ed25519 种子（hex）；HostKey 是对应公钥的线上形态。
	HostSeed string `json:"hostSeed"`
	HostKey  string `json:"hostKey"`
	// DeviceScalar 是 32 字节 P-256 私钥标量（hex）；DeviceKey 是对应公钥的线上形态。
	DeviceScalar string       `json:"deviceScalar"`
	DeviceKey    string       `json:"deviceKey"`
	Cases        []vectorCase `json:"cases"`
	Collapse     []struct {
		Grant string `json:"grant"`
		Tag   string `json:"tag"`
		Out   string `json:"out"`
	} `json:"collapse"`
}

type vectorCase struct {
	Method    string `json:"method"`
	Origin    string `json:"origin"`
	Path      string `json:"path"`
	Timestamp int64  `json:"timestamp"`
	Body      string `json:"body"`
	// Input 是签名输入原文；HostSignature 确定（Ed25519），DeviceSignature 只要求能验过（ECDSA 带随机数）。
	Input           string `json:"input"`
	HostSignature   string `json:"hostSignature"`
	DeviceSignature string `json:"deviceSignature"`
}

func deviceKeyFromScalar(t *testing.T, scalarHex string) *ecdsa.PrivateKey {
	t.Helper()
	raw, err := hex.DecodeString(scalarHex)
	require.NoError(t, err)
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	require.NoError(t, err)
	return k
}

func deviceKeyString(t *testing.T, k *ecdsa.PrivateKey) string {
	t.Helper()
	pub, err := k.PublicKey.Bytes()
	require.NoError(t, err)
	return b64.EncodeToString(pub)
}

func generateVectors(t *testing.T) vectors {
	t.Helper()
	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
	host := ed25519.NewKeyFromSeed(seed)
	scalar := "c9afa9d845ba75166b5c215767b1d6934e50c3db36e89b127b8a622b120f6721"
	dev := deviceKeyFromScalar(t, scalar)
	v := vectors{
		HostSeed: hex.EncodeToString(seed), HostKey: HostKeyString(host.Public().(ed25519.PublicKey)),
		DeviceScalar: scalar, DeviceKey: deviceKeyString(t, dev),
	}
	in := []struct{ method, origin, path, body string }{
		{"GET", "https://push.oryn.best", PathInfo, ""},
		{"POST", "https://push.oryn.best", PathChallenges, `{"channel":"apns","appId":"com.lynricsy.oryn","environment":"production","token":"` + string(bytes.Repeat([]byte("ab"), 32)) + `"}`},
		{"POST", "https://push.oryn.best", PathPush, `{"grant":"AAAAAAAAAAAAAAAAAAAAAA","id":"ntf_01JX","collapse":"q1w2e3r4t5y6u7i8o9p0aa","e":"ZXhhbXBsZQ"}`},
		{"DELETE", "http://localhost:7700", GrantPath("AAAAAAAAAAAAAAAAAAAAAA"), ""},
		{"POST", "https://push.oryn.best", PathGrants, `{"challengeId":"BBBBBBBBBBBBBBBBBBBBBB","nonce":"CCCCCCCCCCCCCCCCCCCCCC","hostKey":"` + v.HostKey + `","note":"中文与 \u2028 保持原样"}`},
	}
	for i, c := range in {
		ts := int64(1_790_000_000_000 + i)
		input := SigningInput(c.method, c.origin, c.path, ts, []byte(c.body))
		sum := sha256.Sum256(input)
		dsig, err := ecdsa.SignASN1(rand.Reader, dev, sum[:])
		require.NoError(t, err)
		v.Cases = append(v.Cases, vectorCase{
			Method: c.method, Origin: c.origin, Path: c.path, Timestamp: ts, Body: c.body,
			Input: string(input), HostSignature: b64.EncodeToString(ed25519.Sign(host, input)),
			DeviceSignature: b64.EncodeToString(dsig),
		})
	}
	for _, c := range [][2]string{{"AAAAAAAAAAAAAAAAAAAAAA", "oryn-0123456789ab"}, {"Zz_-Zz_-Zz_-Zz_-Zz_-Zz", "oryn-budget"}} {
		v.Collapse = append(v.Collapse, struct {
			Grant string `json:"grant"`
			Tag   string `json:"tag"`
			Out   string `json:"out"`
		}{c[0], c[1], Collapse(c[0], c[1])})
	}
	return v
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile(vectorFile)
	require.NoError(t, err)
	var v vectors
	require.NoError(t, json.Unmarshal(raw, &v))
	return v
}

// 向量文件与实现一致：签名输入与主机签名逐字节相等，设备签名能验过，折叠键相等。
func TestVectors(t *testing.T) {
	if *updateVectors {
		b, err := json.MarshalIndent(generateVectors(t), "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(vectorFile), 0o755))
		require.NoError(t, os.WriteFile(vectorFile, append(b, '\n'), 0o644))
	}
	v := loadVectors(t)
	seed, err := hex.DecodeString(v.HostSeed)
	require.NoError(t, err)
	host := ed25519.NewKeyFromSeed(seed)
	assert.Equal(t, v.HostKey, HostKeyString(host.Public().(ed25519.PublicKey)))
	dev := deviceKeyFromScalar(t, v.DeviceScalar)
	assert.Equal(t, v.DeviceKey, deviceKeyString(t, dev))
	require.NotEmpty(t, v.Cases)
	for _, c := range v.Cases {
		input := SigningInput(c.Method, c.Origin, c.Path, c.Timestamp, []byte(c.Body))
		assert.Equal(t, c.Input, string(input), c.Path)
		assert.Equal(t, c.HostSignature, b64.EncodeToString(ed25519.Sign(host, input)), c.Path)

		req := httptest.NewRequest(c.Method, c.Origin+c.Path, bytes.NewReader([]byte(c.Body)))
		req.Header.Set(HeaderDeviceKey, v.DeviceKey)
		req.Header.Set(HeaderTimestamp, strconv.FormatInt(c.Timestamp, 10))
		req.Header.Set(HeaderSignature, c.DeviceSignature)
		s, err := Verify(req, c.Origin, []byte(c.Body), time.UnixMilli(c.Timestamp))
		require.NoError(t, err, c.Path)
		assert.Equal(t, Signer{Kind: SignerDevice, Key: v.DeviceKey}, s)
	}
	for _, c := range v.Collapse {
		assert.Equal(t, c.Out, Collapse(c.Grant, c.Tag))
		assert.Len(t, c.Out, 22)
	}
}

func signedHostRequest(t *testing.T, key ed25519.PrivateKey, now time.Time, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest("POST", "https://push.oryn.best"+PathPush, bytes.NewReader([]byte(body)))
	SignHost(req, "https://push.oryn.best", key, now, []byte(body))
	return req
}

func TestVerifyHostRoundTripAndRejects(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.UnixMilli(1_790_000_000_000)
	body := `{"grant":"x"}`
	origin := "https://push.oryn.best"

	s, err := Verify(signedHostRequest(t, key, now, body), origin, []byte(body), now)
	require.NoError(t, err)
	assert.Equal(t, SignerHost, s.Kind)
	assert.Equal(t, HostKeyString(key.Public().(ed25519.PublicKey)), s.Key)

	// 请求体被改
	_, err = Verify(signedHostRequest(t, key, now, body), origin, []byte(`{"grant":"y"}`), now)
	assert.ErrorIs(t, err, ErrBadSignature)
	// 别的网关 origin
	_, err = Verify(signedHostRequest(t, key, now, body), "https://evil.example", []byte(body), now)
	assert.ErrorIs(t, err, ErrBadSignature)
	// 时钟差超过 5 分钟（两个方向）
	_, err = Verify(signedHostRequest(t, key, now, body), origin, []byte(body), now.Add(MaxClockSkew+time.Second))
	assert.ErrorIs(t, err, ErrClockSkew)
	_, err = Verify(signedHostRequest(t, key, now, body), origin, []byte(body), now.Add(-MaxClockSkew-time.Second))
	assert.ErrorIs(t, err, ErrClockSkew)
	// 两种身份头并存
	req := signedHostRequest(t, key, now, body)
	req.Header.Set(HeaderDeviceKey, "x")
	_, err = Verify(req, origin, []byte(body), now)
	assert.ErrorIs(t, err, ErrMalformed)
	// 带查询串
	req = signedHostRequest(t, key, now, body)
	req.URL.RawQuery = "a=1"
	_, err = Verify(req, origin, []byte(body), now)
	assert.ErrorIs(t, err, ErrMalformed)
	// 没有身份头 / 公钥长度不对
	req = signedHostRequest(t, key, now, body)
	req.Header.Del(HeaderHostKey)
	_, err = Verify(req, origin, []byte(body), now)
	assert.ErrorIs(t, err, ErrMalformed)
	req = signedHostRequest(t, key, now, body)
	req.Header.Set(HeaderHostKey, "AAAA")
	_, err = Verify(req, origin, []byte(body), now)
	assert.ErrorIs(t, err, ErrMalformed)
}

func TestHostKeyStable(t *testing.T) {
	dir := t.TempDir()
	k1, err := LoadOrCreateHostKey(dir)
	require.NoError(t, err)
	k2, err := LoadOrCreateHostKey(dir)
	require.NoError(t, err)
	assert.Equal(t, k1, k2)
	st, err := os.Stat(filepath.Join(dir, HostKeyFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	require.NoError(t, os.WriteFile(filepath.Join(dir, HostKeyFile), []byte("short"), 0o600))
	_, err = LoadOrCreateHostKey(dir)
	assert.Error(t, err)
}

func TestReadBodyLimit(t *testing.T) {
	ok := httptest.NewRequest("POST", "/", bytes.NewReader(make([]byte, MaxBody)))
	b, err := ReadBody(ok)
	require.NoError(t, err)
	assert.Len(t, b, MaxBody)
	big := httptest.NewRequest("POST", "/", bytes.NewReader(make([]byte, MaxBody+1)))
	_, err = ReadBody(big)
	assert.ErrorIs(t, err, ErrTooBig)
}
