package artifacts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestViewPolicy(t *testing.T) {
	assert.Equal(t,
		"sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline' https:; style-src 'unsafe-inline' https:; "+
			"img-src data: blob: https:; font-src data: https:; media-src data: blob: https:; connect-src 'none'; "+
			"form-action 'none'; base-uri 'none'; webrtc 'block'; frame-ancestors https://oryn.example.com",
		viewPolicy("https://oryn.example.com"))
}

// fakeViewAsset 模拟嵌入的视图运行时与内置库；运行时带一个 </script 字样检查转义。
func fakeViewAsset(name string) ([]byte, bool) {
	switch name {
	case viewRuntimeAsset:
		return []byte(`var s="</script>";`), true
	case viewChartAsset:
		return []byte(`window.Chart={};`), true
	case viewTailwindAsset:
		return []byte(`/*tw*/`), true
	}
	return nil, false
}

func TestViewDocument(t *testing.T) {
	a := loadViewAssets(fakeViewAsset)
	head := string(viewHead(ViewDoc{}, a))
	assert.Equal(t, `<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><script>var s="<\/script>";</script>`, head,
		"内联运行时里的 </script 被转义")

	cases := []struct{ name, src, want string }{
		{"有 head", "<!DOCTYPE html>\n<!-- x -->\n<html lang=\"zh\">\n<head data-x>\n<title>t</title></head><body>b</body></html>",
			"<!DOCTYPE html>\n<!-- x -->\n<html lang=\"zh\">\n<head data-x>" + head + "\n<title>t</title></head><body>b</body></html>"},
		{"只有 head", "<head><title>t</title></head><p>x</p>", "<head>" + head + "<title>t</title></head><p>x</p>"},
		{"只有 html", "<html><body><header>h</header></body></html>", "<html><head>" + head + "</head><body><header>h</header></body></html>"},
		{"片段", "<header>h</header><script>document.write('<head>')</script>",
			"<!doctype html><html><head>" + head + "</head><body><header>h</header><script>document.write('<head>')</script></body></html>"},
		{"BOM 与空白后的片段", "\uFEFF  <div>x</div>", "<!doctype html><html><head>" + head + "</head><body>\uFEFF  <div>x</div></body></html>"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, string(viewDocument(ViewDoc{HTML: c.src}, a)), c.name)
	}

	both := string(viewDocument(ViewDoc{HTML: "<p>x</p>", Libs: []string{ViewLibTailwind, ViewLibChart}}, a))
	assert.Contains(t, both, `<style type="text/tailwindcss">`+viewTailwindTheme+`</style><script>/*tw*/</script>`)
	assert.Contains(t, both, `<script>window.Chart={};</script><script>`+viewChartInit+`</script>`)
	assert.Less(t, strings.Index(both, "var s="), strings.Index(both, "/*tw*/"), "运行时先于内置库")

	none := string(viewDocument(ViewDoc{HTML: "<p>x</p>", Libs: []string{ViewLibChart}}, loadViewAssets(noAsset)))
	assert.Equal(t, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"></head><body><p>x</p></body></html>`, none,
		"没构建 Web 时只注入 meta")
}

func TestViewDocFromArgs(t *testing.T) {
	doc, ok := ViewDocFromArgs(json.RawMessage(`{"html":"<p>x</p>","libs":["chartjs","bogus","chartjs","tailwind"],"height":300}`))
	require.True(t, ok)
	assert.Equal(t, ViewDoc{HTML: "<p>x</p>", Libs: []string{ViewLibChart, ViewLibTailwind}}, doc)

	doc, ok = ViewDocFromArgs(json.RawMessage(`{"html":"<p>x</p>","libs":"tailwind"}`))
	require.True(t, ok)
	assert.Equal(t, []string{ViewLibTailwind}, doc.Libs, "单个字符串的 libs")

	for _, bad := range []string{`{}`, `{"html":""}`, `{"html":3}`, `"<p>x</p>"`, `not json`} {
		_, ok := ViewDocFromArgs(json.RawMessage(bad))
		assert.False(t, ok, bad)
	}
}

func TestHostViewFrame(t *testing.T) {
	e := newEnv(t)
	h := newHost(t, e, "localhost", fakeViewAsset)
	mw := h.Middleware(http.NotFoundHandler())
	gone := false
	load := func(context.Context) (ViewDoc, error) {
		if gone {
			return ViewDoc{}, ErrViewNotFound
		}
		return ViewDoc{HTML: "<p>hi</p>"}, nil
	}
	token, url, err := h.OpenViewFrame(load, "https://app.example.com", false)
	require.NoError(t, err)
	assert.Equal(t, "http://a-"+token+".localhost:7600/", url)
	host := "a-" + token + ".localhost:7600"

	rec := doRaw(mw, hostReq{host: host, path: "/"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, viewPolicy("https://app.example.com"), rec.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), "<body><p>hi</p></body>")

	rec = doRaw(mw, hostReq{method: http.MethodHead, host: host, path: "/"})
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Body.String())

	for _, q := range []hostReq{
		{host: host, path: "/x.js"},
		{host: host, path: "/_oryn/v/1/"},
		{host: host, path: "/_oryn/v1"},
		{method: http.MethodPost, host: host, path: "/"},
	} {
		rec := doRaw(mw, q)
		assert.Equal(t, http.StatusNotFound, rec.Code, q.method+" "+q.path)
		assert.Equal(t, viewPolicy("https://app.example.com"), rec.Header().Get("Content-Security-Policy"), "错误响应同样带策略")
	}

	gone = true
	assert.Equal(t, http.StatusNotFound, doRaw(mw, hostReq{host: host, path: "/"}).Code, "调用不在了")

	failing, _, err := h.OpenViewFrame(func(context.Context) (ViewDoc, error) { return ViewDoc{}, errors.New("db down") }, "", false)
	require.NoError(t, err)
	rec = doRaw(mw, hostReq{host: "a-" + failing + ".localhost:7600", path: "/"})
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.True(t, strings.HasSuffix(rec.Header().Get("Content-Security-Policy"), "frame-ancestors http://localhost:7600"),
		"缺省 Origin 时 frame-ancestors 用 public_url 的源")

	h.CloseFrame(token)
	gone = false
	assert.Equal(t, http.StatusNotFound, doRaw(mw, hostReq{host: host, path: "/"}).Code, "关帧后 404")
}

// 匿名访客的帧只在公开帧之间淘汰：挤不掉主人的帧。
func TestPublicViewFrameQuota(t *testing.T) {
	e := newEnv(t)
	h := newHost(t, e, "localhost", noAsset)
	load := func(context.Context) (ViewDoc, error) { return ViewDoc{HTML: "x"}, nil }
	open := func(public bool) (string, error) {
		e.clk.add(time.Millisecond)
		tok, _, err := h.OpenViewFrame(load, "", public)
		return tok, err
	}
	var owner []string
	for range 10 {
		tok, err := open(false)
		require.NoError(t, err)
		owner = append(owner, tok)
	}
	var public []string
	for range maxPublicFrames + 6 {
		tok, err := open(true)
		require.NoError(t, err)
		public = append(public, tok)
	}
	assert.Len(t, h.frames, 10+maxPublicFrames)
	for _, tok := range owner {
		_, ok := h.use(tok)
		assert.True(t, ok, "主人的帧不被匿名帧淘汰")
	}
	for i, tok := range public {
		_, ok := h.use(tok)
		assert.Equal(t, i >= 6, ok, "只淘汰最旧的公开帧（第 %d 个）", i)
	}

	// 总表满时主人的申请先淘汰公开帧：把公开帧刷新得比主人的旧帧更新，全局 LRU 会淘汰主人的帧，这里不能
	e.clk.add(time.Millisecond)
	for _, tok := range public[6:] {
		h.use(tok)
	}
	for range maxFrames - len(h.frames) {
		_, err := open(false)
		require.NoError(t, err)
	}
	require.Len(t, h.frames, maxFrames)
	_, err := open(false)
	require.NoError(t, err)
	assert.Len(t, h.frames, maxFrames)
	for _, tok := range owner {
		_, ok := h.use(tok)
		assert.True(t, ok, "总表满时先淘汰公开帧")
	}
}

// 256 个主人帧占满总表时：公开申请被拒且不淘汰主人的帧；公开申请永远不让总表超过上限。
func TestPublicViewFrameRejectedWhenOwnerFull(t *testing.T) {
	e := newEnv(t)
	h := newHost(t, e, "localhost", noAsset)
	load := func(context.Context) (ViewDoc, error) { return ViewDoc{HTML: "x"}, nil }
	var owner []string
	for range maxFrames {
		e.clk.add(time.Millisecond)
		tok, _, err := h.OpenViewFrame(load, "", false)
		require.NoError(t, err)
		owner = append(owner, tok)
	}
	e.clk.add(time.Millisecond)
	_, _, err := h.OpenViewFrame(load, "", true)
	require.ErrorIs(t, err, ErrFramesFull)
	assert.Len(t, h.frames, maxFrames)
	for _, tok := range owner {
		_, ok := h.use(tok)
		assert.True(t, ok, "公开申请不淘汰主人的帧")
	}

	// 主人关掉一个帧后，公开申请可以用这个空位
	h.CloseFrame(owner[0])
	_, _, err = h.OpenViewFrame(load, "", true)
	require.NoError(t, err)
	assert.Len(t, h.frames, maxFrames)
}
