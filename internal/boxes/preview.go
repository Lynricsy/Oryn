package boxes

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 悬停预览探测（M23 4.2）：app 根页面 `<head>` 里有 `<meta name="oryn-preview" content="v1; enabled; iframe">`
// 才算有预览。探测经 portal 拨号直连 app 端口，不带 Cookie，与匿名访客看到的 HTML 一致。

const (
	previewTimeout   = 5 * time.Second
	previewMaxBody   = 256 << 10
	previewRedirects = 3
	// previewEvery 是对账里对全部 live app 重新探测的间隔。
	previewEvery = 5 * time.Minute
)

// parsePreviewMeta 判定 content：按 `;` 切分去空白，第一段须是 `v1`，其余是标志，同时有 enabled 与 iframe 才算。
func parsePreviewMeta(content string) bool {
	parts := strings.Split(content, ";")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) != "v1" {
		return false
	}
	var enabled, iframe bool
	for _, p := range parts[1:] {
		switch strings.TrimSpace(p) {
		case "enabled":
			enabled = true
		case "iframe":
			iframe = true
		}
	}
	return enabled && iframe
}

// hasPreviewMeta 在 `</head>`（或 `<body>`）之前找 oryn-preview 标签。
func hasPreviewMeta(r io.Reader) bool {
	z := html.NewTokenizer(r)
	for {
		switch z.Next() {
		case html.ErrorToken:
			return false
		case html.EndTagToken:
			if name, _ := z.TagName(); atom.Lookup(name) == atom.Head {
				return false
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			switch atom.Lookup(name) {
			case atom.Body:
				return false
			case atom.Meta:
				var isPreview bool
				var content string
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					switch string(k) {
					case "name":
						isPreview = strings.EqualFold(string(v), "oryn-preview")
					case "content":
						content = string(v)
					}
				}
				if isPreview {
					return parsePreviewMeta(content)
				}
			}
		}
	}
}

// probePreview 探测一个 app 是否声明了预览，结果写回 app 行；变化时通知前端。任何失败都记成没有预览。
func (s *Service) probePreview(ctx context.Context, app string) {
	a, err := s.st.Q.GetBoxAppByName(ctx, app)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
			slog.Warn("boxes: 预览探测读取 app 失败", "app", app, "err", err)
		}
		return
	}
	b, err := s.st.Q.GetBox(ctx, a.BoxID)
	if err != nil {
		return
	}
	ok := a.LiveReleaseID != nil && s.fetchPreview(ctx, b.OrbID, a)
	if ctx.Err() != nil {
		return
	}
	if _, err := s.st.Q.SetBoxAppPreview(ctx, db.SetBoxAppPreviewParams{
		Preview: ok, PreviewCheckedAt: s.now().UnixMilli(), ID: a.ID,
	}); err != nil {
		slog.Warn("boxes: 写入预览探测结果失败", "app", app, "err", err)
		return
	}
	if ok != a.Preview {
		s.changed()
	}
}

// fetchPreview 以公开地址（AppURL）发一个不带 Cookie 的 GET /，至多跟随 3 次同源（公开 scheme + 主机）重定向，
// 读响应体前 256KiB。请求经 DialPortal 走明文 HTTP，像 portal 代理一样带公开的 X-Forwarded-Proto / Host。
func (s *Service) fetchPreview(ctx context.Context, orbID string, a db.BoxApp) bool {
	pub, err := url.Parse(s.cfg.Base.AppURL(a.Name))
	if err != nil || pub.Host == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, previewTimeout)
	defer cancel()
	port := int(a.Port)
	client := &http.Client{
		Transport: previewTransport{
			proto: pub.Scheme,
			next: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return s.orbs.DialPortal(ctx, orbID, port)
				},
				DisableKeepAlives: true,
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > previewRedirects || req.URL.Scheme != pub.Scheme || req.URL.Host != pub.Host {
				return http.ErrUseLastResponse
			}
			req.Header.Del("Cookie")
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pub.Scheme+"://"+pub.Host+"/", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("X-Oryn-Authenticated", "owner=no")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return hasPreviewMeta(io.LimitReader(resp.Body, previewMaxBody))
}

// previewTransport 把公开地址（可能是 https）上的请求改成明文 HTTP 发到 portal 流上，Host 不变，
// 并带上公开的 X-Forwarded-Proto / X-Forwarded-Host（与 portal/proxy.go 一致）。
type previewTransport struct {
	proto string
	next  http.RoundTripper
}

func (t previewTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.URL.Scheme = "http"
	out.Header.Set("X-Forwarded-Proto", t.proto)
	out.Header.Set("X-Forwarded-Host", req.URL.Host)
	return t.next.RoundTrip(out)
}

// probePreviews 对 running 的 box 上已有 live 的 app 各探测一次（对账每 previewEvery 调一次，后台运行）。
func (s *Service) probePreviews(ctx context.Context) {
	apps, err := s.st.Q.ListAllBoxApps(ctx)
	if err != nil {
		return
	}
	running := map[string]bool{}
	for _, a := range apps {
		if a.LiveReleaseID == nil || ctx.Err() != nil {
			continue
		}
		up, seen := running[a.BoxID]
		if !seen {
			if b, err := s.st.Q.GetBox(ctx, a.BoxID); err == nil {
				if o, err := s.orbs.Get(ctx, b.OrbID); err == nil {
					up = b.Desired == "running" && o.State == orbs.StateReady && s.orbs.Connected(b.OrbID)
				}
			}
			running[a.BoxID] = up
		}
		if up {
			s.probePreview(ctx, a.Name)
		}
	}
}
