package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTrusted(t *testing.T) {
	var got string
	h := Trusted(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.RemoteAddr }))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "172.18.0.5:1234"
	req.Header.Set("X-Real-IP", "1.2.3.4")
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "172.18.0.5:1234", got, "非回环来源的转发头不可信")
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Del("X-Real-IP")
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 9.9.9.9")
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "9.9.9.9:0", got, "取反代追加的最后一跳")
}
