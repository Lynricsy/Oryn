package webui

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestSPAHeaders(t *testing.T) {
	h := handler(fstest.MapFS{
		"index.html":    {Data: []byte(`<!doctype html><script type="module" src="/assets/app.js"></script>`)},
		"assets/app.js": {Data: []byte(`console.log(1)`)},
		"favicon.svg":   {Data: []byte(`<svg/>`)},
	})
	for _, p := range []string{"/", "/t/T-1", "/pair", "/assets/app.js", "/favicon.svg"} {
		rec := get(t, h, p)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", p, rec.Code)
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != ContentSecurityPolicy {
			t.Fatalf("%s: CSP = %q", p, got)
		}
		if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
			t.Fatalf("%s: X-Frame-Options = %q", p, got)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s: nosniff = %q", p, got)
		}
	}
	for _, d := range []string{"frame-ancestors 'none'", "script-src 'self';", "object-src 'none'", "base-uri 'none'"} {
		if !strings.Contains(ContentSecurityPolicy, d) {
			t.Fatalf("CSP 缺少 %q", d)
		}
	}
	if strings.Contains(ContentSecurityPolicy, "unsafe-eval") || strings.Contains(strings.SplitN(strings.SplitN(ContentSecurityPolicy, "script-src", 2)[1], ";", 2)[0], "unsafe-inline") {
		t.Fatal("script-src 不得放行内联或 eval")
	}
}

func TestPlaceholderScriptHashAllowed(t *testing.T) {
	h := handler(fstest.MapFS{})
	rec := get(t, h, "/")
	csp := rec.Header().Get("Content-Security-Policy")
	if rec.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("占位页缺少防嵌入头：%q", csp)
	}
	body := rec.Body.String()
	start := strings.Index(body, "<script>") + len("<script>")
	end := strings.Index(body, "</script>")
	sum := sha256.Sum256([]byte(body[start:end]))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if !strings.Contains(csp, "script-src 'self' "+want) {
		t.Fatalf("占位页内联脚本哈希 %s 未放行：%q", want, csp)
	}
}
