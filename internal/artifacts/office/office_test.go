package office

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const discoveryXML = `<?xml version="1.0" encoding="utf-8"?>
<wopi-discovery><net-zone name="external-http">
<app name="writer"><action default="true" ext="docx" name="edit" urlsrc="http://coolwsd:9980/browser/abc/cool.html?"/>
<action ext="docx" name="view" urlsrc="http://coolwsd:9980/browser/abc/cool.html?&lt;ui=UI_LLCC&amp;&gt;"/></app>
<app name="calc"><action ext="xlsx" name="edit" urlsrc="http://coolwsd:9980/browser/abc/cool.html?"/></app>
</net-zone>%s</wopi-discovery>`

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func proofXML(cur, old *rsa.PrivateKey) string {
	attrs := fmt.Sprintf(`modulus="%s" exponent="%s"`, b64(cur.N.Bytes()), b64(big.NewInt(int64(cur.E)).Bytes()))
	if old != nil {
		attrs += fmt.Sprintf(` oldmodulus="%s" oldexponent="%s"`, b64(old.N.Bytes()), b64(big.NewInt(int64(old.E)).Bytes()))
	}
	return `<proof-key value="x" ` + attrs + `/>`
}

func TestParseDiscovery(t *testing.T) {
	d, err := ParseDiscovery(strings.NewReader(fmt.Sprintf(discoveryXML, "")))
	require.NoError(t, err)
	u, ok := d.Action("docx", "edit")
	require.True(t, ok)
	assert.Equal(t, "http://coolwsd:9980/browser/abc/cool.html?", u)
	u, ok = d.Action("docx", "view")
	require.True(t, ok)
	assert.Equal(t, "http://coolwsd:9980/browser/abc/cool.html?", u, "<…> 占位被去掉")
	_, ok = d.Action("pptx", "edit")
	assert.False(t, ok)
	assert.Nil(t, d.Proof)

	_, err = ParseDiscovery(strings.NewReader(`<wopi-discovery/>`))
	assert.Error(t, err)
	_, err = ParseDiscovery(strings.NewReader(`not xml`))
	assert.Error(t, err)
}

func TestDiscoverCache(t *testing.T) {
	var hits atomic.Int32
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hosting/discovery":
			hits.Add(1)
			if fail.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintf(w, discoveryXML, "")
		case "/hosting/capabilities":
			_, _ = io.WriteString(w, `{"convert-to":{"available":true},"productName":"Collabora Online Development Edition","productVersion":"26.04"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	now := time.Unix(1_700_000_000, 0)
	e := New(Config{URL: "https://office.example.com/", InternalURL: srv.URL, Now: func() time.Time { return now }})
	assert.Equal(t, "https://office.example.com", e.URL())
	ctx := context.Background()
	for range 3 {
		_, err := e.Discover(ctx)
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, hits.Load(), "DiscoveryTTL 内只取一次")
	now = now.Add(DiscoveryTTL)
	fail.Store(true)
	_, err := e.Discover(ctx)
	require.Error(t, err)
	_, err = e.Discover(ctx)
	require.Error(t, err)
	assert.EqualValues(t, 2, hits.Load(), "失败也缓存 FailTTL")
	now = now.Add(FailTTL)
	fail.Store(false)
	_, err = e.Discover(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 3, hits.Load())

	c, err := e.Capabilities(ctx)
	require.NoError(t, err)
	assert.True(t, c.Convert)
	assert.Equal(t, "Collabora Online Development Edition 26.04", c.Product)
}

func TestConvert(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/cool/convert-to/pdf" {
			http.Error(w, "bad format", http.StatusBadRequest)
			return
		}
		f, h, err := r.FormFile("data")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(f)
		fmt.Fprintf(w, "%%PDF-1.7 %s %s", h.Filename, b)
	}))
	defer srv.Close()
	e := New(Config{URL: srv.URL})
	rc, err := e.Convert(context.Background(), strings.NewReader("hello"), "报告.docx", "pdf")
	require.NoError(t, err)
	b, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	assert.Equal(t, "%PDF-1.7 报告.docx hello", string(b))

	_, err = e.Convert(context.Background(), strings.NewReader("x"), "a.docx", "odt")
	var ce *ConvertError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, http.StatusBadRequest, ce.Status)
	assert.Equal(t, "bad format", ce.Reason)
	_, err = e.Convert(context.Background(), strings.NewReader("x"), "a.docx", "../x")
	assert.Error(t, err)
}

// sign 按 WOPI 约定给一次请求签名。
func sign(t *testing.T, k *rsa.PrivateKey, token, url string, ticks int64) string {
	t.Helper()
	sum := sha256.Sum256(ProofData(token, url, ticks))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, sum[:])
	require.NoError(t, err)
	return b64(sig)
}

func ticksOf(t time.Time) int64 { return t.UnixNano()/100 + 621355968000000000 }

func TestProofVerify(t *testing.T) {
	cur, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	old, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	d, err := ParseDiscovery(strings.NewReader(fmt.Sprintf(discoveryXML, proofXML(cur, old))))
	require.NoError(t, err)
	require.NotNil(t, d.Proof)
	now := time.Now()
	ticks := ticksOf(now)
	assert.WithinDuration(t, now, TicksTime(ticks), time.Microsecond)
	const token, url = "tok", "https://oryn.example.com/wopi/files/art_1?access_token=tok"
	assert.NoError(t, d.Proof.Verify(token, url, ticks, sign(t, cur, token, url, ticks), "", now))
	assert.NoError(t, d.Proof.Verify(token, url, ticks, "bm9wZQ==", sign(t, cur, token, url, ticks), now), "当前公钥验 ProofOld")
	assert.NoError(t, d.Proof.Verify(token, url, ticks, sign(t, old, token, url, ticks), "", now), "旧公钥验 Proof")
	assert.NoError(t, d.Proof.Verify(token, strings.ToLower(url), ticks, sign(t, cur, token, url, ticks), "", now), "地址按大写比较")
	assert.Error(t, d.Proof.Verify("other", url, ticks, sign(t, cur, token, url, ticks), "", now))
	assert.Error(t, d.Proof.Verify(token, url, ticks, "", "", now))
	stale := ticksOf(now.Add(-21 * time.Minute))
	assert.Error(t, d.Proof.Verify(token, url, stale, sign(t, cur, token, url, stale), "", now), "时间戳超过 20 分钟")
}
