// Package portalhost 是 portal 主机名的纯函数（M4）：线程短码、主机标签 `t-<code>-p<port>`、
// 由 public_url 与 portal_domain 推出的 portal URL / 模板 / CSP 源。orbd 与 orynd 共用，不依赖其它内部包。
package portalhost

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	threadIDRe = regexp.MustCompile(`^T-[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)
	labelRe    = regexp.MustCompile(`^t-([0-9a-f]{12})-p([1-9][0-9]{0,4})$`)
	domainRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
)

// Code 返回线程短码：线程 id 形如 `T-<uuid>`（小写十六进制）时取末 12 位，否则 ok=false。
func Code(threadID string) (string, bool) {
	if !threadIDRe.MatchString(threadID) {
		return "", false
	}
	return threadID[len(threadID)-12:], true
}

// Label 返回 portal 主机的首个标签 `t-<code>-p<port>`。
func Label(code string, port int) string {
	return "t-" + code + "-p" + strconv.Itoa(port)
}

// ParseLabel 解析 `t-<12 位十六进制>-p<端口>`；端口须在 1–65535 且无前导零。
func ParseLabel(label string) (code string, port int, ok bool) {
	m := labelRe.FindStringSubmatch(label)
	if m == nil {
		return "", 0, false
	}
	p, err := strconv.Atoi(m[2])
	if err != nil || p > 65535 {
		return "", 0, false
	}
	return m[1], p, true
}

// Expand 把模板中的 `{code}` 换成短码；port > 0 时再把 `{port}` 换成端口，port = 0 时保留 `{port}`。
func Expand(template, code string, port int) string {
	out := strings.ReplaceAll(template, "{code}", code)
	if port > 0 {
		out = strings.ReplaceAll(out, "{port}", strconv.Itoa(port))
	}
	return out
}

// Base 是由 public_url 与 portal_domain 推出的 portal 地址基础。
// portal 与主域名同协议、同端口；Port 为空表示协议默认端口。
type Base struct {
	Scheme string // http / https，取自 public_url
	Domain string // portal_domain（小写、无尾点）；空 = 未启用 portal
	Port   string // public_url 的非默认端口
	Origin string // 主域名 origin：`<Scheme>://<主机>[:Port]`
}

// NewBase 由 public_url 与 portal_domain 构造 Base。portalDomain 为空时 Base 未启用，但 Origin 仍有效。
func NewBase(publicURL, portalDomain string) (Base, error) {
	u, err := url.Parse(publicURL)
	if err != nil {
		return Base{}, fmt.Errorf("portalhost: public_url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return Base{}, fmt.Errorf("portalhost: public_url scheme must be http or https, got %q", u.Scheme)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return Base{}, errors.New("portalhost: public_url has no host")
	}
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	b := Base{Scheme: scheme, Port: port}
	hostport := host
	if strings.Contains(host, ":") { // IPv6 字面量
		hostport = "[" + host + "]"
	}
	if port != "" {
		hostport += ":" + port
	}
	b.Origin = scheme + "://" + hostport
	if portalDomain != "" {
		d := strings.TrimSuffix(strings.ToLower(portalDomain), ".")
		if !domainRe.MatchString(d) {
			return Base{}, fmt.Errorf("portalhost: invalid portal_domain %q", portalDomain)
		}
		b.Domain = d
	}
	return b, nil
}

// Enabled 报告是否配置了 portal_domain。
func (b Base) Enabled() bool { return b.Domain != "" }

// Match 判断 Host 头是否是 portal 主机：去端口、转小写、去尾点后恰为 `<单标签>.<Domain>`。
// 不比较端口；返回首个标签（是否为合法 portal 标签由 ParseLabel 判定）。
func (b Base) Match(host string) (label string, ok bool) {
	if !b.Enabled() {
		return "", false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	label, ok = strings.CutSuffix(host, "."+b.Domain)
	if !ok || label == "" || strings.Contains(label, ".") {
		return "", false
	}
	return label, true
}

// hostSuffix 返回主机名中标签之后的部分 `.<Domain>[:Port]`。
func (b Base) hostSuffix() string {
	s := "." + b.Domain
	if b.Port != "" {
		s += ":" + b.Port
	}
	return s
}

// URL 返回 portal 根地址 `<Scheme>://t-<code>-p<port>.<Domain>[:Port]/`；未启用时为空串。
func (b Base) URL(code string, port int) string {
	if !b.Enabled() {
		return ""
	}
	return b.Scheme + "://" + Label(code, port) + b.hostSuffix() + "/"
}

// Template 返回地址模板 `<Scheme>://t-{code}-p{port}.<Domain>[:Port]/`；未启用时为空串。
func (b Base) Template() string {
	if !b.Enabled() {
		return ""
	}
	return b.Scheme + "://t-{code}-p{port}" + b.hostSuffix() + "/"
}

// FrameSrc 返回 CSP frame-src 源 `<Scheme>://*.<Domain>[:Port]`；未启用时为空串。
func (b Base) FrameSrc() string {
	if !b.Enabled() {
		return ""
	}
	return b.Scheme + "://*" + b.hostSuffix()
}
