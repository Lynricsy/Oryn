package webpush_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/webpush"
	"github.com/Lynricsy/Oryn/internal/webpush/webpushtest"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := webpush.DecodeKey(s)
	require.NoError(t, err)
	return b
}

// RFC 8291 附录 A 的测试向量。
func TestEncryptRFC8291Vector(t *testing.T) {
	rnd := io.MultiReader(
		bytes.NewReader(mustB64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw")),
		bytes.NewReader(mustB64(t, "DGv6ra1nlYgDCS1FRnbzlw")),
	)
	sub := webpush.Subscription{
		P256dh: "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		Auth:   "BTBZMqHH6r4Tts7J_aSIgg",
	}
	got, err := webpush.Encrypt([]byte("When I grow up, I want to be a watermelon"), sub, rnd)
	require.NoError(t, err)
	want := append(mustB64(t, "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"),
		mustB64(t, "8pfeW0KbunFT06SuDKoJH9Ql87S1QUrdirN6GcG7sFz1y1sqLgVi1VhjVkHsUoEsbI_0LpXMuGvnzQ")...)
	assert.Equal(t, want, got)

	// 用 ua_private 解密还原
	ua, err := ecdh.P256().NewPrivateKey(mustB64(t, "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
	require.NoError(t, err)
	plain, err := webpushtest.Decrypt(got, ua, mustB64(t, sub.Auth))
	require.NoError(t, err)
	assert.Equal(t, "When I grow up, I want to be a watermelon", string(plain))
}

func TestEncryptRoundTripRandomKeys(t *testing.T) {
	c, err := webpushtest.NewClient("https://push.example/x")
	require.NoError(t, err)
	msg := bytes.Repeat([]byte("推送"), 300)
	ct, err := webpush.Encrypt(msg, c.Sub, rand.Reader)
	require.NoError(t, err)
	plain, err := c.Decrypt(ct)
	require.NoError(t, err)
	assert.Equal(t, msg, plain)

	_, err = webpush.Encrypt(make([]byte, webpush.MaxPlaintext+1), c.Sub, rand.Reader)
	assert.ErrorIs(t, err, webpush.ErrTooLarge)
	_, err = webpush.Encrypt([]byte("x"), webpush.Subscription{P256dh: c.Sub.P256dh, Auth: "AAAA"}, rand.Reader)
	assert.Error(t, err, "auth secret 长度不对")
	_, err = webpush.Encrypt([]byte("x"), webpush.Subscription{P256dh: "AAAA", Auth: c.Sub.Auth}, rand.Reader)
	assert.Error(t, err, "p256dh 不是合法点")
}

func TestLoadOrCreateKeys(t *testing.T) {
	dir := t.TempDir()
	k1, err := webpush.LoadOrCreateKeys(dir)
	require.NoError(t, err)
	k2, err := webpush.LoadOrCreateKeys(dir)
	require.NoError(t, err)
	assert.Equal(t, k1.PublicKey(), k2.PublicKey(), "二次加载得到同一公钥")
	pub := mustB64(t, k1.PublicKey())
	assert.Len(t, pub, 65)
	assert.Equal(t, byte(0x04), pub[0])
	fi, err := os.Stat(filepath.Join(dir, "vapid.key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	assert.EqualValues(t, 32, fi.Size())
}

func TestVAPIDAuthorization(t *testing.T) {
	k, err := webpush.LoadOrCreateKeys(t.TempDir())
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0)
	h, err := k.Authorization("https://fcm.googleapis.com:443/fcm/send/abc?x=1", "mailto:admin@oryn.invalid", now)
	require.NoError(t, err)
	claims, err := webpushtest.VerifyVAPID(h, k.PublicKey())
	require.NoError(t, err)
	assert.Equal(t, "https://fcm.googleapis.com:443", claims["aud"])
	assert.EqualValues(t, now.Add(12*time.Hour).Unix(), claims["exp"])
	assert.Equal(t, "mailto:admin@oryn.invalid", claims["sub"])

	other, err := webpush.LoadOrCreateKeys(t.TempDir())
	require.NoError(t, err)
	_, err = webpushtest.VerifyVAPID(h, other.PublicKey())
	assert.Error(t, err, "别的公钥验不过")

	_, err = k.Authorization("not a url", "x", now)
	assert.Error(t, err)
}

func TestSendHeaders(t *testing.T) {
	k, err := webpush.LoadOrCreateKeys(t.TempDir())
	require.NoError(t, err)
	type got struct {
		h    http.Header
		body []byte
	}
	ch := make(chan got, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- got{r.Header.Clone(), b}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	c, err := webpushtest.NewClient(srv.URL + "/push/1")
	require.NoError(t, err)
	status, err := k.Send(context.Background(), srv.Client(), c.Sub, []byte(`{"v":1}`), webpush.Options{TTL: 86400, Urgency: "high", Topic: "abc", Subject: "https://oryn.example"})
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, status)
	g := <-ch
	assert.Equal(t, "aes128gcm", g.h.Get("Content-Encoding"))
	assert.Equal(t, "application/octet-stream", g.h.Get("Content-Type"))
	assert.Equal(t, "86400", g.h.Get("TTL"))
	assert.Equal(t, "high", g.h.Get("Urgency"))
	assert.Equal(t, "abc", g.h.Get("Topic"))
	claims, err := webpushtest.VerifyVAPID(g.h.Get("Authorization"), k.PublicKey())
	require.NoError(t, err)
	assert.Equal(t, srv.URL, claims["aud"])
	assert.Equal(t, "https://oryn.example", claims["sub"])
	plain, err := c.Decrypt(g.body)
	require.NoError(t, err)
	assert.Equal(t, `{"v":1}`, string(plain))
}
