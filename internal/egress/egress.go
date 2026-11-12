// Package egress 是控制面侧的出站代理：orb 内 agent 的模型 / git 请求经 link 的 egress 流到达这里，
// 在此把占位令牌替换为真实密钥后转发到上游。真实密钥从不进入 orb。
package egress

import (
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
)

// Resolver 提供路由与密钥查询。
type Resolver interface {
	// LLMRoute 返回模型路由键对应的上游前缀（scheme://host）。
	LLMRoute(key string) (origin string, ok bool)
	// SecretByID 按密钥 id 返回明文与允许的上游主机列表（空 = 不限制）。
	SecretByID(id string) (value string, hosts []string, ok bool)
	// GitToken 返回 git 主机的访问令牌（密钥 git/<host>/token）。
	GitToken(host string) (token string, ok bool)
}

var placeholderRe = regexp.MustCompile(`oryn-ph-([A-Za-z0-9_]+)`)

// Handler 为一台 orb 的 egress 流提供代理服务。
type Handler struct {
	OrbID string
	R     Resolver
	proxy *httputil.ReverseProxy
}

// NewHandler 构造代理。
func NewHandler(orbID string, r Resolver) *Handler {
	h := &Handler{OrbID: orbID, R: r}
	h.proxy = &httputil.ReverseProxy{
		Rewrite:       func(*httputil.ProxyRequest) {},
		FlushInterval: -1, // SSE 逐块立即 flush
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Warn("egress 上游错误", "orb", orbID, "url", r.URL.String(), "err", err)
			http.Error(w, "oryn egress: "+err.Error(), http.StatusBadGateway)
		},
	}
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target, extraAuth, ok := h.resolve(r.URL.Path)
	if !ok {
		http.Error(w, "oryn egress: 未知路由", http.StatusNotFound)
		return
	}
	if !h.substitute(r, target.Host) {
		http.Error(w, "oryn egress: 占位符无效或不允许用于该主机", http.StatusForbidden)
		return
	}
	p := *h.proxy
	p.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.Out.URL.Scheme = target.Scheme
		pr.Out.URL.Host = target.Host
		pr.Out.URL.Path = target.Path
		pr.Out.URL.RawPath = ""
		pr.Out.URL.RawQuery = pr.In.URL.RawQuery
		pr.Out.Host = target.Host
		pr.Out.Header.Del("X-Forwarded-For")
		if extraAuth != "" {
			pr.Out.Header.Set("Authorization", extraAuth)
		}
	}
	p.ServeHTTP(w, r)
}

// resolve 把 /llm/<key>/… 或 /git/<host>/… 映射为上游 URL。
func (h *Handler) resolve(p string) (target *url.URL, auth string, ok bool) {
	parts := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 3)
	if len(parts) < 2 {
		return nil, "", false
	}
	rest := "/"
	if len(parts) == 3 {
		rest += parts[2]
	}
	switch parts[0] {
	case "llm":
		origin, ok := h.R.LLMRoute(parts[1])
		if !ok {
			return nil, "", false
		}
		u, err := url.Parse(origin)
		if err != nil {
			return nil, "", false
		}
		u.Path = rest
		return u, "", true
	case "git":
		host := parts[1]
		tok, ok := h.R.GitToken(host)
		if !ok {
			return nil, "", false
		}
		auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok))
		return &url.URL{Scheme: "https", Host: host, Path: rest}, auth, true
	}
	return nil, "", false
}

// substitute 把请求头与 query 中的占位符替换为真实值；任一占位符无效或主机不被允许则返回 false。
func (h *Handler) substitute(r *http.Request, host string) bool {
	valid := true
	repl := func(s string) string {
		if !strings.Contains(s, "oryn-ph-") {
			return s
		}
		return placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
			id := placeholderRe.FindStringSubmatch(m)[1]
			v, hosts, ok := h.R.SecretByID(id)
			if !ok || !hostAllowed(hosts, host) {
				valid = false
				return m
			}
			return v
		})
	}
	for k, vs := range r.Header {
		for i, v := range vs {
			r.Header[k][i] = repl(v)
		}
	}
	if q := r.URL.RawQuery; strings.Contains(q, "oryn-ph-") {
		vals := r.URL.Query()
		for k, vs := range vals {
			for i, v := range vs {
				vals[k][i] = repl(v)
			}
		}
		r.URL.RawQuery = vals.Encode()
	}
	return valid
}

func hostAllowed(hosts []string, host string) bool {
	if len(hosts) == 0 {
		return true
	}
	h := strings.ToLower(host)
	for _, a := range hosts {
		a = strings.ToLower(a)
		if a == h || (strings.HasPrefix(a, "*.") && strings.HasSuffix(h, a[1:])) {
			return true
		}
	}
	return false
}
