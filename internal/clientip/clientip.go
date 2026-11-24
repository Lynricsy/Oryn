// Package clientip 在请求来自本机反代时还原真实客户端地址（控制面与推送网关共用）。
package clientip

import (
	"net"
	"net/http"
	"strings"
)

// Trusted 只在请求来自本机反代（回环地址）时采信 X-Real-IP / X-Forwarded-For，并把它写进 r.RemoteAddr（端口为 0）。
// 其它来源（如经 docker 网桥直连控制面的 orb）自报的转发头不可信。
func Trusted(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err == nil {
			if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
				if real := forwarded(r); real != "" {
					r.RemoteAddr = net.JoinHostPort(real, "0")
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// forwarded 优先取反代设置的 X-Real-IP，否则取 X-Forwarded-For 的最后一跳（由反代追加，客户端无法伪造）。
func forwarded(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(v) != nil {
		return v
	}
	xff := r.Header.Values("X-Forwarded-For")
	if len(xff) == 0 {
		return ""
	}
	parts := strings.Split(xff[len(xff)-1], ",")
	if v := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(v) != nil {
		return v
	}
	return ""
}
