package egress

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicProxy(t *testing.T) {
	p, err := ListenPublicProxy()
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	assert.Regexp(t, `^http://127\.0\.0\.1:\d+$`, p.URL())

	for _, tc := range []struct {
		name, head string
		code       int
		body       string
	}{
		{"loopback", "CONNECT 127.0.0.1:443 HTTP/1.1\r\nHost: 127.0.0.1:443\r\n\r\n", http.StatusForbidden, "destination is not a public address\n"},
		{"ipv6 loopback", "CONNECT [::1]:443 HTTP/1.1\r\nHost: [::1]:443\r\n\r\n", http.StatusForbidden, "destination is not a public address\n"},
		{"private", "CONNECT 10.0.0.1:443 HTTP/1.1\r\nHost: 10.0.0.1:443\r\n\r\n", http.StatusForbidden, "destination is not a public address\n"},
		{"port 22", "CONNECT example.com:22 HTTP/1.1\r\nHost: example.com:22\r\n\r\n", http.StatusForbidden, "oryn repos: only port 443\n"},
		{"no port", "CONNECT example.com HTTP/1.1\r\nHost: example.com\r\n\r\n", http.StatusBadRequest, ""},
		{"GET", "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", http.StatusMethodNotAllowed, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, br, _ := rawProxy(t, p.URL(), tc.head)
			assert.Equal(t, tc.code, code)
			if tc.body != "" {
				b := make([]byte, len(tc.body))
				_, err := io.ReadFull(br, b)
				require.NoError(t, err)
				assert.Equal(t, tc.body, string(b))
			}
		})
	}
}
