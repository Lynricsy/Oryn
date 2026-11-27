// Package aiconsent 是「把主人的个人数据发给第三方 AI 服务」的同意（App Store 审核指南 5.1.2(i)）在服务端的唯一口径：
// 接收方 id 的推导规则（设置页的同意框与各出站卡点共用），以及按主人当前的同意记录放行或拒绝的 Gate。
//
// 接收方 id：
//   - 模型上游：`model:<provider>@<host>`（主机未知时为 `model:<provider>`），见 ModelRecipient；
//   - 记忆的嵌入 / 重排接口与助手记忆的判断模型：`memory-embed:<host>` / `memory-rerank:<host>` / `memory-decide:<host>`，
//     见 EndpointRecipient；
//   - 全局助手联网：`web:public`。
//
// 上游在本机、内网、链路本地或单标签主机名上的不算第三方（IsLocalHost），不需要同意；没写 baseUrl 的 ollama / lmstudio 同样。
package aiconsent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Code 是拒绝响应里稳定的错误码。
const Code = "ai_consent_required"

// Hint 是拒绝时给主人与 agent 看的去处。
const Hint = "请在「设置 → 隐私与数据」（Settings → Privacy & data）中同意后重试"

// WebRecipientID 是全局助手联网的接收方：搜索词与打开的网址发往公网网站。
const WebRecipientID = "web:public"

// 记忆接口接收方 id 的前缀。
const (
	PrefixMemoryEmbed  = "memory-embed:"
	PrefixMemoryRerank = "memory-rerank:"
	PrefixMemoryDecide = "memory-decide:"
)

// LocalProviders 是没写 baseUrl 时默认连本机的 provider，不是第三方。
var LocalProviders = map[string]bool{"ollama": true, "lmstudio": true}

// decisionsModelPrefix / decisionsHost：Gemini Decisions 的模型名前缀与它的上游主机。随项目构建的 gemini-rerank 适配器
// （cmd/gemini-rerank）只监听回环，却把重排请求原样转给 Google，没有别的上游；接口地址在本机、模型是 gemini-decision-*
// 时，真正的接收方是 Google。
const (
	decisionsModelPrefix = "gemini-decision"
	decisionsHost        = "generativelanguage.googleapis.com"
)

// URLHost 返回地址的小写主机名；解析不了时为空。
func URLHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// cgnat 是 CGNAT 共享地址（100.64.0.0/10，Tailscale 等）。
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// IsLocalHost 判断主机是否在主人自己的机器或内网：localhost、*.localhost / *.local / *.internal、单标签主机名
// （容器网络里的服务名），以及回环、私有、链路本地、未指定与 CGNAT 地址。空主机返回 false。
func IsLocalHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || cgnat.Contains(ip)
	}
	if net.ParseIP(host) != nil {
		return false
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return true
	}
	for _, suf := range []string{".localhost", ".local", ".internal"} {
		if strings.HasSuffix(host, suf) {
			return true
		}
	}
	return false
}

// ModelRecipient 返回 provider 经主机 host（小写，可为空）上游时的接收方 id；thirdParty 为假表示上游不是第三方
// （本机 / 内网，或没有主机的本机 provider），不需要同意。
func ModelRecipient(provider, host string) (id string, thirdParty bool) {
	host = strings.ToLower(host)
	if (host == "" && LocalProviders[provider]) || IsLocalHost(host) {
		return "", false
	}
	id = "model:" + provider
	if host != "" {
		id += "@" + host
	}
	return id, true
}

// EndpointRecipient 返回记忆接口（嵌入、重排、判断模型）的接收方 id：prefix 是 Prefix* 之一，baseURL 是接口地址，
// model 是配置的模型名。地址解析不出主机时 thirdParty 为假且 id 为空（请求本身发不出去）。
func EndpointRecipient(prefix, baseURL, model string) (id string, thirdParty bool) {
	host := URLHost(baseURL)
	if host == "" {
		return "", false
	}
	if IsLocalHost(host) {
		if !strings.HasPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "models/"), decisionsModelPrefix) {
			return "", false
		}
		host = decisionsHost
	}
	return prefix + host, true
}

// Checker 判断主人是否已同意把数据发给接收方；nil 表示放行，否则是拒绝原因（*DeniedError）。
type Checker interface {
	Check(ctx context.Context, recipient string) error
}

// CheckerFunc 把函数适配为 Checker。
type CheckerFunc func(ctx context.Context, recipient string) error

// Check 实现 Checker。
func (f CheckerFunc) Check(ctx context.Context, recipient string) error { return f(ctx, recipient) }

// Check 用 c 判定；c 为 nil 时一律拒绝（没有接上同意来源的出站卡点不放行）。
func Check(ctx context.Context, c Checker, recipient string) error {
	if c == nil {
		return &DeniedError{Recipient: recipient}
	}
	return c.Check(ctx, recipient)
}

// DeniedError 是主人尚未同意发往 Recipient 时的拒绝。
type DeniedError struct {
	Recipient string
}

func (e *DeniedError) Error() string {
	return Code + "：主人尚未同意把数据发给这个 AI 服务（" + e.Recipient + "），请求没有发出。" + Hint
}

// IsDenied 报告 err 是否是同意拒绝。
func IsDenied(err error) bool {
	var d *DeniedError
	return errors.As(err, &d)
}

// Store 是 Gate 读同意记录的来源（*db.Queries 实现）。
type Store interface {
	GetOwnerAiConsent(ctx context.Context) (db.GetOwnerAiConsentRow, error)
}

// readTimeout 是一次读同意记录的上限（调用方的 context 没有期限时也不会被卡住）。
const readTimeout = 5 * time.Second

// Gate 按主人当前的同意记录放行：每次现读 owner 行（单行主键查询），设置页的同意与撤回对下一个请求立即生效，
// 不需要缓存失效。没有同意记录、读库失败、记录无法解析时一律拒绝。拒绝日志每个接收方只记一次，放行后重新计。
type Gate struct {
	q      Store
	mu     sync.Mutex
	logged map[string]bool
}

// NewGate 构造 Gate。
func NewGate(q Store) *Gate { return &Gate{q: q, logged: map[string]bool{}} }

// Check 实现 Checker。
func (g *Gate) Check(ctx context.Context, recipient string) error {
	ok, err := g.consented(ctx, recipient)
	g.mu.Lock()
	if ok {
		delete(g.logged, recipient)
		g.mu.Unlock()
		return nil
	}
	first := !g.logged[recipient]
	g.logged[recipient] = true
	g.mu.Unlock()
	if first {
		if err != nil {
			slog.Warn("AI 数据同意：读同意记录失败，按未同意拒绝", "recipient", recipient, "err", err)
		} else {
			slog.Warn("AI 数据同意：主人尚未同意，拒绝发往该接收方（同一接收方只记一次）", "recipient", recipient)
		}
	}
	return &DeniedError{Recipient: recipient}
}

func (g *Gate) consented(ctx context.Context, recipient string) (bool, error) {
	if g == nil || g.q == nil {
		return false, errors.New("aiconsent: 没有同意来源")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
	defer cancel()
	row, err := g.q.GetOwnerAiConsent(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.AiConsentAt == nil {
		return false, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(row.AiConsentRecipients), &ids); err != nil {
		return false, err
	}
	return slices.Contains(ids, recipient), nil
}
