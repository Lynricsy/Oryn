// Package relayroute 为记忆模型中转查出中央模型目录里一个模型的协议类型与上游：按控制面的 models.yml 渲染出
// egress 路由（agentcfg.RenderAt），再经 egress.Handler 转发。真实密钥的注入、绑定检查与响应脱敏全部沿用 egress 的规则，
// 这里不接触密钥明文。
//
// 它单独成包是因为 egress 依赖 memory（网关的调用方身份），memory 不能反过来依赖 egress；由 orynd 装配进 memory.Config。
package relayroute

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/secrets"
)

// egressID 是中转在 egress 日志里的标识（代替 orb id）。
const egressID = "memory-relay"

// renderAddr 是渲染 models.yml 时代替 orb 出站地址的主机名；中转只用渲染结果里 `/llm/<路由键>/…` 的路径部分。
const renderAddr = "memory-relay.invalid"

// ErrUnsupported 表示模型的协议不在中转支持的三种之内，或 provider 不经代理（没有 baseUrl 的环境变量降级）。
var ErrUnsupported = errors.New("模型协议不受记忆中转支持")

// Router 按控制面的 omp-home（中央模型目录同一份 models.yml）解析模型路由。每次调用都重新渲染，
// 管理页换模型、主人改 models.yml 或轮换密钥都在下一条请求生效。
type Router struct {
	home  *agentcfg.Home
	vault *secrets.Vault
}

// New 构造路由解析器。
func New(home *agentcfg.Home, vault *secrets.Vault) *Router {
	return &Router{home: home, vault: vault}
}

// route 是一个模型解析出的上游。
type route struct {
	family apigen.MemoryFamily
	// base 是渲染后的本地前缀 `/llm/<路由键>/<provider baseUrl 的路径>`。
	base string
	// apiKey 是 provider apiKey 渲染出的占位符（`oryn-ph-<id>`）；provider 不带密钥时为空。
	apiKey string
	// bearer 为真时 anthropic 协议也用 `Authorization: Bearer`（models.yml 的 `authHeader: true`）。
	bearer  bool
	headers map[string]string
	routes  map[string]egress.Route
}

// Family 实现 memory.Config.Family：按 models.yml 查出模型的协议类型。
func (r *Router) Family(_ context.Context, providerKey, modelID string) (apigen.MemoryFamily, error) {
	rt, err := r.resolve(providerKey, modelID)
	if err != nil {
		return "", err
	}
	return rt.family, nil
}

// Upstream 实现 memory.Config.Upstream：返回模型的协议类型与转发器。
func (r *Router) Upstream(_ context.Context, providerKey, modelID string) (memory.ModelUpstream, error) {
	rt, err := r.resolve(providerKey, modelID)
	if err != nil {
		return memory.ModelUpstream{}, err
	}
	h := egress.NewHandler(egressID, policy{vault: r.vault, routes: rt.routes})
	return memory.ModelUpstream{Family: rt.family, Forward: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		forward(h, rt, w, req)
	})}, nil
}

// forward 把原生协议请求改写到 `/llm/<路由键>/…`，在该协议的凭据头里放 provider 的占位符，交给 egress 注入真实密钥并脱敏响应。
func forward(h *egress.Handler, rt route, w http.ResponseWriter, req *http.Request) {
	out := req.Clone(req.Context())
	out.URL.Path = joinNative(rt.base, req.URL.Path)
	out.URL.RawPath = ""
	out.RequestURI = ""
	for k, v := range rt.headers {
		out.Header.Set(k, v)
	}
	if rt.apiKey != "" {
		switch rt.family {
		case apigen.MemoryFamilyGemini:
			out.Header.Set("X-Goog-Api-Key", rt.apiKey)
		case apigen.MemoryFamilyAnthropic:
			out.Header.Set("X-Api-Key", rt.apiKey)
			if rt.bearer {
				out.Header.Set("Authorization", "Bearer "+rt.apiKey)
			}
		default:
			out.Header.Set("Authorization", "Bearer "+rt.apiKey)
		}
	}
	h.ServeHTTP(w, out)
}

// versionSeg 匹配 API 版本段（v1、v1beta、v1alpha2 …）。
var versionSeg = regexp.MustCompile(`^v\d+([a-z]+\d*)?$`)

// joinNative 把原生路径接到 provider 的 baseUrl 路径之后。原生路径以版本段开头（anthropic 的 `/v1/messages`、
// gemini 的 `/v1beta/models/…`）而 baseUrl 已经以版本段结尾时（如 `…/v1beta`），去掉原生路径的版本段，
// 与 SDK 拼 baseUrl 的方式一致；openai 的原生路径不带版本段（版本在 baseUrl 里），原样拼接。
func joinNative(base, native string) string {
	base = strings.TrimRight(base, "/")
	first, rest, ok := strings.Cut(strings.TrimPrefix(native, "/"), "/")
	if ok && versionSeg.MatchString(first) && versionSeg.MatchString(base[strings.LastIndex(base, "/")+1:]) {
		return base + "/" + rest
	}
	return base + native
}

// modelsDoc 是渲染后 models.yml 里中转用到的字段。
type modelsDoc struct {
	Providers map[string]providerDoc `yaml:"providers"`
}

type providerDoc struct {
	BaseURL    string            `yaml:"baseUrl"`
	API        string            `yaml:"api"`
	APIKey     string            `yaml:"apiKey"`
	AuthHeader bool              `yaml:"authHeader"`
	Headers    map[string]string `yaml:"headers"`
	Models     []modelDoc        `yaml:"models"`
}

type modelDoc struct {
	ID      string            `yaml:"id"`
	API     string            `yaml:"api"`
	BaseURL string            `yaml:"baseUrl"`
	Headers map[string]string `yaml:"headers"`
}

// builtinFamilies 是 models.yml 没写 `api` 时内置 provider 的协议；其它 provider 按 OpenAI 兼容处理（omp 的默认）。
var builtinFamilies = map[string]apigen.MemoryFamily{
	"anthropic": apigen.MemoryFamilyAnthropic,
	"google":    apigen.MemoryFamilyGemini,
}

// familyOf 把 omp 的 `api` 映射为 Hindsight 的协议类型；不支持的返回 ErrUnsupported。
func familyOf(api, providerKey string) (apigen.MemoryFamily, error) {
	switch api {
	case "":
		if f, ok := builtinFamilies[providerKey]; ok {
			return f, nil
		}
		return apigen.MemoryFamilyOpenai, nil
	case "openai-completions", "openai-responses":
		return apigen.MemoryFamilyOpenai, nil
	case "anthropic-messages":
		return apigen.MemoryFamilyAnthropic, nil
	case "google-generative-ai":
		return apigen.MemoryFamilyGemini, nil
	}
	return "", fmt.Errorf("%w：provider %s 的 api %q", ErrUnsupported, providerKey, api)
}

// resolve 渲染 models.yml（与下发到 orb 的规则相同）并找出模型的路由。
func (r *Router) resolve(providerKey, modelID string) (route, error) {
	b, _, err := r.home.Get(agentcfg.ModelsFile)
	if errors.Is(err, fs.ErrNotExist) {
		return route{}, fmt.Errorf("控制面没有 %s，找不到 provider %s", agentcfg.ModelsFile, providerKey)
	}
	if err != nil {
		return route{}, err
	}
	rendered, err := agentcfg.RenderAt(map[string][]byte{agentcfg.ModelsFile: b}, r.vault, renderAddr)
	if err != nil {
		return route{}, err
	}
	var doc modelsDoc
	if err := yaml.Unmarshal(rendered.Files[agentcfg.ModelsFile], &doc); err != nil {
		return route{}, fmt.Errorf("解析 %s：%w", agentcfg.ModelsFile, err)
	}
	prov, ok := doc.Providers[providerKey]
	if !ok {
		return route{}, fmt.Errorf("%s 里没有 provider %s", agentcfg.ModelsFile, providerKey)
	}
	base, api := prov.BaseURL, prov.API
	headers := map[string]string{}
	for k, v := range prov.Headers {
		headers[k] = v
	}
	for _, m := range prov.Models {
		if m.ID != modelID {
			continue
		}
		if m.BaseURL != "" {
			base = m.BaseURL
		}
		if m.API != "" {
			api = m.API
		}
		for k, v := range m.Headers {
			headers[k] = v
		}
		break
	}
	fam, err := familyOf(api, providerKey)
	if err != nil {
		return route{}, err
	}
	u, err := url.Parse(base)
	if err != nil || u.Host != renderAddr || !strings.HasPrefix(u.Path, "/llm/") {
		// 没有 baseUrl 也不在内置表里的 provider 以环境变量降级，不经代理，中转无法注入密钥
		return route{}, fmt.Errorf("%w：provider %s 不经出站代理（没有 baseUrl）", ErrUnsupported, providerKey)
	}
	rt := route{family: fam, base: u.Path, bearer: prov.AuthHeader, headers: headers, routes: rendered.Routes}
	if strings.HasPrefix(prov.APIKey, "oryn-ph-") {
		rt.apiKey = prov.APIKey
	}
	return rt, nil
}

// policy 是中转的 egress.Resolver：只放行渲染出的模型路由，没有任何 git / GitHub 访问。
type policy struct {
	egress.NoGateLookup // 没有 GitHub 路由，门禁查询不会被调用
	egress.NoMemory     // 中转不经 /memory/
	egress.NoHostedGit  // 没有托管仓库通道
	vault               *secrets.Vault
	routes              map[string]egress.Route
}

func (p policy) Policy() egress.Policy { return egress.Policy{Routes: p.routes, NoGit: true} }

func (p policy) SecretByID(id string) (string, []string, bool) { return p.vault.SecretByID(id) }

func (policy) GitToken(string) (string, bool) { return "", false }

func (policy) GitHubToken() (string, bool) { return "", false }

// 中转不注入 GitHub 令牌，不会有令牌被上游拒绝，所以是空实现。
func (policy) GitHubTokenRejected(string) {}

// 中转没有 GitHub 路由，不会出现 GitHub 写操作。
func (policy) GitHubWriteBegin(egress.GitHubWrite) string       { return "" }
func (policy) GitHubWriteEnd(egress.GitHubWrite, int, []string) {}
