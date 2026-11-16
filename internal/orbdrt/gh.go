package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// M14：orb 内 `gh` 免登录。
//
// 控制面在 setup / resume 参数里带上 `gh`（项目绑定了 GitHub 仓库时）。orbd 据此写 `$HOME/.oryn/gh/`：
// `hosts.yml` 只有占位令牌 `oryn-proxied`，`config.yml` 让 gh 经 `$HOME/.oryn/gh.sock` 发请求。
// 套接字上的反向代理把发往 api.github.com 的请求转给本机 egress 的 `/gh/api.github.com/…`，
// 由 egress 换上控制面签发的按仓库收窄的令牌；真实令牌从不落到 orb 里。

// ghPlaceholderToken 是写进 hosts.yml 的占位令牌：gh 需要「已登录」才肯发请求，egress 会替换掉它。
const ghPlaceholderToken = "oryn-proxied"

// ghLoginRe 是 GitHub 登录名（含 App bot 的 `[bot]` 后缀）；写进 YAML 前校验，杜绝注入。
var ghLoginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})(?:\[bot\])?$`)

// ghSockPath 是 gh 的 http_unix_socket。
func (r *Runtime) ghSockPath() string { return filepath.Join(r.home, ".oryn", "gh.sock") }

// ghDir 是 gh 的配置目录（GH_CONFIG_DIR）。
func (r *Runtime) ghDir() string { return filepath.Join(r.home, ".oryn", "gh") }

// serveGh 在 gh.sock 上运行 gh 代理直到 ctx 结束；监听失败只记日志（gh 退回为未登录状态）。
func (r *Runtime) serveGh(ctx context.Context) {
	path := r.ghSockPath()
	ln, err := orbsock.Listen(path)
	if err != nil {
		r.log.Warn("gh proxy socket unavailable", "path", path, "err", err)
		return
	}
	srv := &http.Server{Handler: r.ghProxy(), ReadHeaderTimeout: 30 * time.Second}
	stop := context.AfterFunc(ctx, func() { _ = srv.Close() })
	defer stop()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		r.log.Warn("gh proxy stopped", "err", err)
	}
	_ = os.Remove(path)
}

// ghProxy 处理 gh 经 unix 套接字发来的明文 HTTP 请求。api.github.com 转给本机 egress 的 `/gh/` 路由（保留原始编码的路径）；
// 其它主机（如 API 返回的 302 下载地址）按 https 原样直连——这些请求不带任何凭据，orb 本来就能直连它们。
func (r *Runtime) ghProxy() http.Handler {
	return &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			host := strings.ToLower(pr.In.Host)
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if host == "api.github.com" {
				egress := r.EgressAddr()
				pr.Out.URL.Scheme = "http"
				pr.Out.URL.Host = egress
				pr.Out.URL.Path = "/gh/" + host + pr.In.URL.Path
				pr.Out.URL.RawPath = "/gh/" + host + pr.In.URL.EscapedPath()
				pr.Out.Host = egress
				return
			}
			pr.Out.URL.Scheme = "https"
			pr.Out.URL.Host = pr.In.Host
			pr.Out.Host = pr.In.Host
		},
	}
}

// applyGh 按 setup 参数写入或删除 gh 配置目录。g 为 nil（项目未连接 GitHub）时删除整个目录。
func (r *Runtime) applyGh(g *apigen.LinkCtlSetupGh) error {
	dir := r.ghDir()
	if g == nil {
		return os.RemoveAll(dir)
	}
	if !ghLoginRe.MatchString(g.Login) {
		return fmt.Errorf("invalid gh login %q", g.Login)
	}
	ident, err := json.Marshal(map[string]string{"login": g.Login})
	if err != nil {
		return err
	}
	files := []struct {
		name string
		data string
	}{
		{"config.yml", "# managed by oryn\ngit_protocol: https\nprompt: disabled\nhttp_unix_socket: " + r.ghSockPath() + "\n"},
		{"hosts.yml", "github.com:\n    oauth_token: " + ghPlaceholderToken + "\n    user: " + g.Login + "\n    git_protocol: https\n"},
		{"identity.json", string(ident)},
	}
	for _, f := range files {
		if err := writeFileAtomic(filepath.Join(dir, f.name), []byte(f.data), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// followOrigin 在仓库改名 / 转移后把检出的 origin 改到新地址：只有 origin 等于某个旧地址时才改（M14）。
// 任何一步失败只记日志，不算 setup 失败。
func (r *Runtime) followOrigin(ctx context.Context, workdir string, repo *apigen.LinkCtlSetupRepo) {
	if repo == nil || len(repo.PreviousUrls) == 0 || repo.URL == "" || !hasCheckout(workdir) {
		return
	}
	res, err := r.git(ctx, workdir, nil, 64<<10, "remote", "get-url", "origin")
	if err != nil || res.code != 0 {
		r.log.Warn("origin follow: get-url failed", "err", err, "stderr", res.stderr)
		return
	}
	cur := normRepoURL(string(res.out))
	for _, prev := range repo.PreviousUrls {
		if cur == "" || normRepoURL(prev) != cur {
			continue
		}
		res, err := r.git(ctx, workdir, nil, 64<<10, "remote", "set-url", "origin", repo.URL)
		if err != nil || res.code != 0 {
			r.log.Warn("origin follow: set-url failed", "err", err, "stderr", res.stderr)
		}
		return
	}
}

// normRepoURL 归一化仓库地址用于比较：去空白、去结尾的 `/` 与 `.git`、忽略大小写。
func normRepoURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	u = strings.TrimSuffix(u, ".git")
	return strings.ToLower(strings.TrimRight(u, "/"))
}
