// Package egress 是控制面侧的出站代理：orb 内 agent 的模型 / git / GitHub API 请求经 link 的 egress 流到达这里，
// 在此把占位令牌替换为真实密钥后转发到上游。真实密钥从不进入 orb。
//
// 安全约束（orb 内代码可任意构造请求，并能看到全部占位符）：
//   - 占位符只在固定的凭据位置（若干请求头与 query 参数 key）被替换，出现在其它任何位置一律拒绝；
//   - 每个模型路由只接受绑定到它的密钥 id：路由自己的 apiKey 密钥（hosts 为空时只能用在它自己的路由上），
//     以及 hosts 非空且与路由上游主机匹配的其它密钥；git 路由不接受任何占位符；
//   - git 路由（/git/<host>/…，或 orb 内 git 经 `http.<url>.proxy` 发来的代理形式 `http://<host>/…`）只放行 smart-HTTP 端点
//     与 Git LFS 接口（<仓库>.git/info/lfs/…，M24，见 lfs.go）；有项目仓库时只放行该仓库（读写），否则任意仓库只读；
//     路径含 `.`、`..`、空段或不是规范形式的一律 400；
//   - GitHub API 路由（/gh/api.github.com/…、/gh/uploads.github.com/…）只在策略开放时可用，注入 OAuth 用户令牌；
//     GitHub 写操作（REST 非 GET、GraphQL mutation、github.com 的 git-receive-pack）转发前交给 Resolver 预登记（可拒绝），
//     响应后回报状态码与补充键（M15 防回流与审计）；请求体原样转发，不改写；
//   - 响应头与响应体中出现的真实值会被换回占位符，防止上游回显泄露（101 升级后的字节流只脱敏响应头，
//     改写升级后的协议帧会破坏其分帧）；
//   - 记忆路由（/memory/t/<threadId>/…，M16）不走占位符替换，线程归属经 Resolver.Caller 校验后交给记忆网关
//     （internal/memory/gateway.go），由网关强制 bank、标签与放行清单并注入 Hindsight key；
//   - 正向代理（M19，web.go）只给协调宿主（NoGit）：策略带 Web 时放行 CONNECT 与 http 的 absolute-form 请求，
//     在连接建立时按实际连接的 IP 只放行公网地址（AllowHosts 例外）；没有 Web 时一律 403；
//   - MCP 路由（/mcp/<key>/…，M19）转发到主人配置的 MCP 服务器：丢掉入站的 Authorization 后注入路由的请求头，
//     凭据类请求头的值从响应里抹掉；持后台研究凭据（M26）的请求只能到达开放给研究的路由，且 JSON-RPC 请求逐个
//     经工具白名单过滤（web.go 的 mcpResearchFilter）。
package egress

import (
	"bytes"
	"cmp"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/lfs"
	"github.com/Lynricsy/Oryn/internal/memory"
)

// Route 是一条模型路由：上游前缀与允许在该路由上使用的密钥 id。
type Route struct {
	Origin string `json:"origin"`
	// SecretIDs 是路由所属 provider 自己的 apiKey 密钥：hosts 为空时不限主机（只绑定在它自己的路由上），
	// 非空时须与上游主机匹配。
	SecretIDs []string `json:"secretIds"`
	// HostBoundIDs 是 provider 子树里引用的其它密钥：只有 hosts 非空且与上游主机匹配时才可注入。
	HostBoundIDs []string `json:"hostBoundIds,omitempty"`
}

// Policy 是一台 orb 的出站策略。
type Policy struct {
	// Routes 是模型路由键 → 路由。
	Routes map[string]Route
	// GitRepos 是 git 主机 → 允许读写的仓库路径（如 "owner/repo"）。非空时只放行其中列出的仓库
	// （其它主机、同主机其它仓库一律拒绝）；为空时（无项目的 orb）任意有令牌主机上的任意仓库只读，
	// 拒绝 git-receive-pack。
	GitRepos map[string][]string
	// NoGit 为真时 /git/… 一律 403（协调宿主没有任何 git 访问）。
	NoGit bool
	// GitHub 非 nil 时放行 /gh/api.github.com/… 与 /gh/uploads.github.com/…（M14/M15），并对 github.com 的 git 不按仓库限制（M15）。
	GitHub *GitHubAccess
	// Web 非 nil 时（只用于协调宿主）正向代理放行公网访问（M19，web.go）；nil 时 CONNECT 与转发一律 403。
	Web *WebPolicy
	// MCP 是 MCP 路由键 → 路由（`/mcp/<key>/…`，M19）。
	MCP map[string]MCPRoute
	// MCPToken 是 /mcp/ 路由要求的凭据（请求头 MCPTokenHeader，M19 D10）：只有全局助手的 agent 持有，Space 协调者
	// 没有；为空时 /mcp/ 一律 403。转发前去掉该头。
	MCPToken string
	// MCPResearchToken 是后台研究（M26）的 /mcp/ 凭据（同一个请求头）：只经 agent.start 交给 MCP 运行里的私有研究员；
	// 只能访问 ResearchTools 非 nil 的路由，tools/call 只放行其中列出的工具。为空时研究凭据不存在。
	MCPResearchToken string
	// HostedGit 非 nil 时是 orb 所属项目的 Oryn 托管仓库（M24 阶段 ③）：该主机上的 git 请求只放行这个仓库，
	// 交给 Resolver.HostedGit 在本地服务，不转发、不注入令牌。
	HostedGit *HostedGitRoute
}

// GitHubAccess 是 orb 的 GitHub 访问：非 nil 时放行 `/gh/api.github.com/…`、`/gh/uploads.github.com/…`；
// GitOrigin 是 `github.com` 的 git 上游，默认 https://github.com；UploadsOrigin 是上传主机的上游，空 = https://uploads.github.com。
// 两者只有测试会指向别处。
//
// Gates 是出站代理门禁（M15 步骤 5.4，见 gate.go）里 setup 时固定的部分：项目的 github_gates。线程限制（只读评审、只发
// COMMENT、实现需授权）与其它会在线程期间变化的状态都经 GateLookup 每次写请求现查。
type GitHubAccess struct {
	APIOrigin, GitOrigin, UploadsOrigin string
	// Gates 是项目的推送 / PR 门禁；nil = 项目没有门禁（含无项目的 orb）。
	Gates *GateConfig
}

// defaultUploadsOrigin 是 GitHub 上传 API（release 附件）的上游。
const defaultUploadsOrigin = "https://uploads.github.com"

// GitHub 写请求的读取上限（docs/plan-m15.md「全局约束」）：REST / GraphQL 请求体与 git-receive-pack 的命令段。
const (
	ghWriteBodyMax     = 1 << 20
	receivePackHeadMax = 64 << 10
)

// MethodPush 是 git-receive-pack 推送在 GitHubWrite.Method 与审计里的方法名。
const MethodPush = "PUSH"

// GitHubWrite 是出站代理观察到的一次 GitHub 写操作（M15）：一个非 GET 的 REST 请求、GraphQL mutation 文档里的
// 一个根字段，或一次 github.com 的 git-receive-pack。GraphQL query 不算写操作。
type GitHubWrite struct {
	Method     string // HTTP 方法（大写）；git 推送为 MethodPush
	Category   string // Cat*；只有路径级信息且路径不认识时为空
	Target     string // 防回流预登记目标（GitHubOp.Target）：REST 为规范化路径，GraphQL 为 input 里的 node id
	Op         string // REST 路径模板、mutation 字段名，或 "git-receive-pack"
	Repo       string // owner/repo：取自路径，REST 路径里没有时取自响应；都取不到为空
	BodySHA256 string // BodyHash(正文)；没有正文为空
	Refs       []RefUpdate
	// Unparsed 为真表示请求体超过上限、带压缩编码或无法解析，只有路径级信息。门禁关闭时只审计，
	// 门禁开启时出站代理拒绝（R19「失败即拒绝」，gate.go）。
	Unparsed bool
	// Gate 是门禁结论：""（未经门禁）、GatePass、GateSkipped，或拒绝原因（出站代理门禁或 GitHubWriteBegin 的返回值），随 GitHubWriteEnd 记进审计。
	Gate string
	// Rejected 为真表示出站代理的门禁拒绝了这个请求：没有调用 GitHubWriteBegin，GitHubWriteEnd 只记审计。
	Rejected bool
}

// Resolver 提供策略与密钥查询。
type Resolver interface {
	// Policy 返回该 orb 当前的出站策略。
	Policy() Policy
	// SecretByID 按密钥 id 返回明文与允许的上游主机列表（空 = 未绑定主机）。
	SecretByID(id string) (value string, hosts []string, ok bool)
	// GitToken 返回 git 主机的访问令牌（密钥 git/<host>/token）。
	GitToken(host string) (token string, ok bool)
	// GitHubToken 返回这台 orb 的 GitHub API 令牌（M15：线程身份槽位的 OAuth 令牌，账号能做什么就能做什么，不按仓库限制）；
	// 这台 orb 不由 GitHub 集成下发凭据或拿不到时 ok 为假。
	GitHubToken() (token string, ok bool)
	// GitHubTokenRejected 在出站代理注入的 GitHub 令牌被上游回 401 时调用（R2，GET 与写请求都调）：/gh/ 路由的 GitHubToken，
	// 以及 github.com git 路由的 GitToken("github.com")。令牌原样传回，由身份层按令牌找槽位；不是槽位令牌的（如 PAT）忽略。
	GitHubTokenRejected(token string)
	// GitHubWriteBegin 在转发 GitHub 写操作之前调用（预登记防回流记录），出站代理门禁放行之后才调用。返回非空表示拒绝，
	// 内容是给 agent 看的原因（代理回 `oryn: <原因>`；推送按 receive-pack 的 ng 行回）。一个请求含多个写操作时逐个调用，遇到拒绝即停。
	GitHubWriteBegin(w GitHubWrite) (gate string)
	// GitHubWriteEnd 对每个写操作恰好调用一次：status 是回给 orb 的状态码（上游不可达为 502），
	// respKeys 是 REST 写操作 JSON 响应里的补充键（RespKeys；GraphQL 与推送为空）。
	GitHubWriteEnd(w GitHubWrite, status int, respKeys []string)
	// GateLookup 是门禁现查的控制面状态（M15 步骤 5.4）。
	GateLookup
	// MemoryLookup 是记忆网关（M16 步骤 2.2）。
	MemoryLookup
	// HostedGitLookup 是托管仓库通道（M24 阶段 ③）。
	HostedGitLookup
}

// MemoryLookup 提供 `/memory/t/<threadId>/…` 的处理器与可信调用方身份（M16）。
type MemoryLookup interface {
	// Memory 返回记忆网关；nil 表示这个 Resolver 没有记忆通道（/memory/ 一律 403）。
	Memory() http.Handler
	// Caller 按路径里的线程号解析调用方身份：线程必须属于这台 orb（runner 同理），否则返回错误（代理回 403，R14）。
	Caller(threadID string) (memory.Caller, error)
}

// NoMemory 是没有记忆通道的 Resolver 用的 MemoryLookup。
type NoMemory struct{}

func (NoMemory) Memory() http.Handler { return nil }
func (NoMemory) Caller(string) (memory.Caller, error) {
	return memory.Caller{}, errors.New("egress: 没有记忆通道")
}

var placeholderRe = regexp.MustCompile(`oryn-ph-([A-Za-z0-9_]+)`)

// credentialHeaders 是允许出现占位符的请求头（规范化名）。
var credentialHeaders = map[string]bool{
	"Authorization":  true,
	"X-Api-Key":      true,
	"X-Goog-Api-Key": true,
	"Api-Key":        true,
}

// credentialQuery 是允许出现占位符的 query 参数。
const credentialQuery = "key"

// Handler 为一台 orb 的 egress 流提供代理服务。
type Handler struct {
	OrbID string
	R     Resolver
	// branches 缓存门禁查过的仓库默认分支（gate.go）。
	branches branchCache
	// lfsTickets 是 batch 响应里改写过的同主机传输地址（lfs.go）：键 `<方法> http://<gitHost><路径>[?查询]`。
	lfsMu      sync.Mutex
	lfsTickets map[string]lfsTicket
	// now 为 nil 时用 time.Now（只有测试赋值）。
	now func() time.Time
}

// NewHandler 构造代理。
func NewHandler(orbID string, r Resolver) *Handler { return &Handler{OrbID: orbID, R: r} }

type target struct {
	url    *url.URL
	auth   string   // 非空时覆盖 Authorization（git、gh）
	redact []string // 要从响应里抹掉的真实值（换成 oryn-redacted）
	// ghToken 是注入的 GitHub 令牌（/gh/ 路由与 github.com 的 git 路由）；上游回 401 时交给 Resolver.GitHubTokenRejected。
	ghToken string
	// stripEncoding 为真时去掉客户端的 Accept-Encoding，让 Transport 自己协商并解压（M15：上游 gzip 时响应脱敏与写操作识别读到的是明文）。
	stripEncoding bool
	own           []string // 路由自己的 apiKey 密钥 id
	hostBound     []string // 须 hosts 匹配的其它密钥 id
	// GitHub 写操作识别（M15）：gh 为真时是 /gh/ 路由，ghPath / ghRawPath 是上游路径（不含 /gh/<host> 与 APIOrigin 的路径前缀），
	// ghUpload 为真时是上传主机（请求体是二进制附件，不读）；receivePack 为真时是 github.com 的推送，repo 是 owner/repo。
	// ghAccess 是解析请求时的 GitHub 策略（门禁用）。
	gh                bool
	ghUpload          bool
	ghPath, ghRawPath string
	receivePack       bool
	repo              string
	ghAccess          *GitHubAccess
	// headers 非 nil 时（MCP 路由）先删掉入站的 Authorization，再逐个设置这些请求头；query 是上游地址自带的查询串，
	// 拼在入站查询串之前。
	headers map[string]string
	query   string
	// mcpKey / mcpBase 非空时是 MCP 路由：上游的重定向经 mcpRedirect 处理（同源且在路由路径下的改写回 /mcp/<key>/…，
	// 其余拦成 502），客户端不会带着 MCP 凭据头直接跳到别的服务器。
	mcpKey  string
	mcpBase *url.URL
	// mcpAllow 非 nil 时请求持的是后台研究凭据（M26）：转发前经 mcpResearchFilter 过滤，tools/call 只放行其中的工具
	// （空切片 = 一个也不放行）。
	mcpAllow []string
	// deny 非空时是 resolve 拒绝（非 200）的原因，代替按状态码给出的通用文案。
	deny string
	// push 为真时是 git-receive-pack 端点（任意主机），gitHost 是 git 主机（小写）；上游以 200 应答推送后交给 PushRecorder（M24）。
	push    bool
	gitHost string
	// lfs 为真时是 Git LFS 端点（或门票）：拒绝回 LFS JSON 错误体；lfsBatch 为真时是 objects/batch，200 响应经 lfsRewrite 重写（M24）。
	lfs, lfsBatch bool
	// hostedProject 非空时是托管仓库请求（M24 阶段 ③）：不转发，交给 Resolver.HostedGit 在本地服务；hostedEndpoint 是
	// 相对 <repo>.git/ 的端点。
	hostedProject, hostedEndpoint string
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 按请求形式分开路由：absolute-form（代理形式，r.URL.Host 非空）在普通 orb 上只走 git 路由（resolve），在协调宿主（NoGit）上
	// 是正向代理（serveForward）；记忆、模型、/gh/ 与 /mcp/ 都只认 origin-form。同一个路径不会按两种语义解析，owner 叫 memory 的
	// 仓库也能经代理形式访问。CONNECT 的 r.URL.Host 也非空，先于 absolute-form 分流。
	if r.URL.Host == "" && strings.HasPrefix(r.URL.Path, "/memory/") {
		h.serveMemory(w, r)
		return
	}
	if r.Method == http.MethodConnect {
		h.serveConnect(w, r)
		return
	}
	if r.URL.Host != "" {
		if pol := h.R.Policy(); pol.NoGit {
			h.serveForward(w, r, pol.Web)
			return
		}
	}
	t, status := h.resolve(r)
	if status != http.StatusOK {
		msg := "oryn egress: 未知或不允许的路由"
		switch {
		case t.deny != "":
			msg = "oryn: " + t.deny
		case status == http.StatusBadRequest:
			msg = "oryn egress: 路径不是规范形式"
		case status == http.StatusBadGateway:
			msg = "oryn egress: GitHub 令牌不可用（身份未连接或签发失败）"
		}
		if t.lfs {
			lfs.WriteError(w, status, msg)
			return
		}
		http.Error(w, msg, status)
		return
	}
	if t.hostedProject != "" {
		h.serveHosted(w, r, t)
		return
	}
	if t.mcpAllow != nil && mcpResearchFilter(w, r, t.mcpAllow) {
		return
	}
	used, ok := h.substitute(r, t)
	if !ok {
		http.Error(w, "oryn egress: 占位符位置无效或不允许用于该路由", http.StatusForbidden)
		return
	}
	aud, responded := h.beginGitHubWrites(w, r, t)
	if responded {
		return
	}
	pushed := h.pushRefs(r, t, aud)
	secretsUsed := make([][2]string, 0, len(used)+len(t.redact)) // 真实值 → 占位符，用于响应脱敏
	for id, v := range used {
		secretsUsed = append(secretsUsed, [2]string{v, "oryn-ph-" + id})
	}
	for _, v := range t.redact {
		secretsUsed = append(secretsUsed, [2]string{v, "oryn-redacted"})
	}
	p := &httputil.ReverseProxy{
		FlushInterval: -1, // SSE 逐块立即 flush
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = t.url.Scheme
			pr.Out.URL.Host = t.url.Host
			pr.Out.URL.Path = t.url.Path
			pr.Out.URL.RawPath = t.url.RawPath
			pr.Out.Host = t.url.Host
			pr.Out.Header.Del("X-Forwarded-For")
			pr.Out.Header.Del(MCPTokenHeader)
			if t.auth != "" {
				pr.Out.Header.Set("Authorization", t.auth)
			}
			if t.headers != nil {
				pr.Out.Header.Del("Authorization")
				for k, v := range t.headers {
					pr.Out.Header.Set(k, v)
				}
			}
			if t.query != "" {
				q := t.query
				if pr.Out.URL.RawQuery != "" {
					q += "&" + pr.Out.URL.RawQuery
				}
				pr.Out.URL.RawQuery = q
			}
			if t.stripEncoding {
				pr.Out.Header.Del("Accept-Encoding")
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode == http.StatusOK && len(pushed) > 0 {
				if pr, ok := h.R.(PushRecorder); ok {
					pr.GitPushed(GitPush{Host: t.gitHost, Repo: t.repo, Refs: pushed})
				}
			}
			aud.response(resp)
			if resp.StatusCode == http.StatusUnauthorized && t.ghToken != "" {
				h.R.GitHubTokenRejected(t.ghToken)
			}
			if t.mcpKey != "" {
				mcpRedirect(resp, t.mcpKey, t.mcpBase)
			}
			if t.lfsBatch {
				switch {
				case resp.StatusCode == http.StatusOK:
					h.lfsRewrite(resp, t)
				case resp.StatusCode/100 == 2:
					lfsBadGateway(resp, lfsRespNot200)
				}
			}
			if len(secretsUsed) == 0 {
				return nil
			}
			for k, vs := range resp.Header {
				for i, v := range vs {
					resp.Header[k][i] = redact(v, secretsUsed)
				}
			}
			// 101：ReverseProxy 需要原样的双向 body（io.ReadWriteCloser）来桥接升级后的连接；
			// 替换成只读的脱敏读取器会让升级失败（502）。升级后的协议有自己的分帧，不做按字节替换。
			if resp.StatusCode == http.StatusSwitchingProtocols {
				return nil
			}
			resp.Body = newRedactReader(resp.Body, secretsUsed)
			resp.ContentLength = -1
			resp.Header.Del("Content-Length")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Warn("egress 上游错误", "orb", h.OrbID, "host", t.url.Host, "err", err)
			aud.end(http.StatusBadGateway, nil)
			http.Error(w, "oryn egress: 上游不可达", http.StatusBadGateway)
		},
	}
	p.ServeHTTP(w, r)
}

// serveMemory 处理 `/memory/t/<threadId>/<Hindsight 路径>`（M16）：线程号只从路径取，经 Resolver.Caller 校验归属
// 并解析出项目后，把剩余路径与调用方身份交给记忆网关。不做占位符替换：网关自己注入 Hindsight key，
// 客户端的 Authorization 不会被转发。
func (h *Handler) serveMemory(w http.ResponseWriter, r *http.Request) {
	gw := h.R.Memory()
	if gw == nil {
		memory.WriteError(w, http.StatusForbidden, "memory_forbidden", "oryn egress: 未知或不允许的路由")
		return
	}
	segs := strings.SplitN(strings.TrimPrefix(r.URL.EscapedPath(), "/memory/"), "/", 3)
	if len(segs) < 3 || segs[0] != "t" {
		memory.WriteError(w, http.StatusForbidden, "memory_forbidden", "记忆请求路径必须是 /memory/t/<threadId>/…")
		return
	}
	tid, err := url.PathUnescape(segs[1])
	if err != nil || tid == "" || strings.ContainsAny(tid, "/\\") {
		memory.WriteError(w, http.StatusForbidden, "memory_forbidden", "记忆请求路径里的线程号无效")
		return
	}
	c, err := h.R.Caller(tid)
	if err != nil {
		slog.Info("记忆请求被拒绝", "orb", h.OrbID, "thread", tid, "err", err)
		memory.WriteError(w, http.StatusForbidden, "memory_forbidden", "线程不属于发出请求的 orb")
		return
	}
	rawRest := "/" + segs[2]
	rest, err := url.PathUnescape(rawRest)
	if err != nil {
		memory.WriteError(w, http.StatusForbidden, "memory_forbidden", "记忆请求路径无效")
		return
	}
	r2 := r.Clone(memory.WithCaller(r.Context(), c))
	r2.URL.Path, r2.URL.RawPath = rest, rawRest
	r2.Header.Del("Authorization")
	gw.ServeHTTP(w, r2)
}

// resolve 把 /llm/<key>/…、/git/<host>/…、/gh/api.github.com/… 或 /mcp/<key>/… 映射为上游；返回 HTTP 状态（200 = 放行）。
// 代理形式的请求（`GET http://<host>/<owner>/<repo>/… HTTP/1.1`，orb 内 git 经 `http.<url>.proxy` 发来）只走 git 路由，
// 等同于 /git/<host>/<owner>/<repo>/…：不是 http 绝对地址或路径不规范回 400，不是 git 端点（smart-HTTP、Git LFS）又没有
// 有效 LFS 门票的回 403。
func (h *Handler) resolve(r *http.Request) (target, int) {
	pol := h.R.Policy()
	if r.URL.Host != "" {
		host, ok := proxyGitHost(r)
		if !ok || !canonicalPath(r.URL.Path) {
			return target{}, http.StatusBadRequest
		}
		return h.resolveGit(r, pol, host, r.URL.Path)
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) < 2 {
		return target{}, http.StatusNotFound
	}
	rest := "/"
	if len(parts) == 3 {
		rest += parts[2]
	}
	switch parts[0] {
	case "llm":
		rt, ok := pol.Routes[parts[1]]
		if !ok {
			return target{}, http.StatusNotFound
		}
		u, err := url.Parse(rt.Origin)
		if err != nil || u.Host == "" {
			return target{}, http.StatusNotFound
		}
		u.Path = rest
		return target{url: u, own: rt.SecretIDs, hostBound: rt.HostBoundIDs}, http.StatusOK
	case "git":
		return h.resolveGit(r, pol, strings.ToLower(parts[1]), rest)
	case "gh":
		// GitHub API（M14/M15）：令牌由出站代理注入，这里不做路径白名单；保留原始转义（如 contents 路径里的 %2F）。
		if pol.GitHub == nil {
			return target{}, http.StatusForbidden
		}
		origin, upload := pol.GitHub.APIOrigin, false
		switch strings.ToLower(parts[1]) {
		case "api.github.com":
		case "uploads.github.com":
			origin, upload = cmp.Or(pol.GitHub.UploadsOrigin, defaultUploadsOrigin), true
		default:
			return target{}, http.StatusNotFound
		}
		o, err := url.Parse(origin)
		if err != nil || o.Host == "" {
			return target{}, http.StatusNotFound
		}
		tok, ok := h.R.GitHubToken()
		if !ok {
			return target{}, http.StatusBadGateway
		}
		rawRest := strings.TrimPrefix(r.URL.EscapedPath(), "/gh/"+parts[1])
		up := &url.URL{
			Scheme:  o.Scheme,
			Host:    o.Host,
			Path:    o.Path + rest,
			RawPath: o.Path + rawRest,
		}
		return target{
			url: up, auth: "Bearer " + tok, ghToken: tok, redact: []string{tok}, stripEncoding: true,
			gh: true, ghUpload: upload, ghPath: rest, ghRawPath: rawRest, ghAccess: pol.GitHub,
		}, http.StatusOK
	case "mcp":
		// MCP（M19）：主人配置的地址可以是内网，按普通方式拨号；请求路径在 /mcp/<key> 之后的部分追加到上游路径之后。
		// 先核对凭据（D10）：任何方法、任何子路径（含 SSE 的 GET）都要带对，否则 403，不暴露路由是否存在。
		// 助手凭据不受限制；后台研究凭据（M26）只能访问开放给研究的路由，请求经工具白名单过滤。
		got := []byte(r.Header.Get(MCPTokenHeader))
		research := false
		switch {
		case pol.MCPToken != "" && subtle.ConstantTimeCompare(got, []byte(pol.MCPToken)) == 1:
		case pol.MCPResearchToken != "" && subtle.ConstantTimeCompare(got, []byte(pol.MCPResearchToken)) == 1:
			research = true
		default:
			return target{deny: "MCP 只对全局助手开放"}, http.StatusForbidden
		}
		rt, ok := pol.MCP[parts[1]]
		if !ok {
			return target{}, http.StatusNotFound
		}
		var allow []string
		if research {
			if rt.ResearchTools == nil {
				return target{deny: "这个 MCP 服务器没有开放给后台研究"}, http.StatusForbidden
			}
			allow = rt.ResearchTools
		}
		u, err := url.Parse(rt.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return target{}, http.StatusNotFound
		}
		base := *u
		if len(parts) == 3 {
			u.Path = strings.TrimSuffix(u.Path, "/") + rest
			u.RawPath = ""
		}
		hdr := rt.Headers
		if hdr == nil {
			hdr = map[string]string{}
		}
		return target{
			url: u, headers: hdr, query: u.RawQuery, redact: mcpRedact(rt.Headers), mcpKey: parts[1], mcpBase: &base, mcpAllow: allow,
		}, http.StatusOK
	}
	return target{}, http.StatusNotFound
}

// denyProxyNotGit 是代理形式请求不是 git 端点、也没有有效门票时的拒绝原因。
const denyProxyNotGit = "代理形式的请求只接受 git smart-HTTP（info/refs、git-upload-pack、git-receive-pack）、" +
	"Git LFS 接口（<仓库>.git/info/lfs/…）与 LFS 批量接口给出的传输地址"

// git 仓库限制的拒绝原因（PAT 主机）。
const (
	denyGitOtherRepo = "这个 orb 只能访问项目仓库"
	denyGitReadOnly  = "没有项目的 orb 对 git 仓库只读（不能推送，也不能上传 Git LFS 对象或改动文件锁）"
)

// proxyGitHost 返回代理形式 git 请求的 git 主机键：请求行须是 `http://<host>[:<port>]/…` 绝对地址，键是小写的
// `host[:port]`，与 /git/<host>/ 路径形式里的主机段同义（带端口的 PAT 主机如 `git.corp.example:8443`）。
// orb 内 git 的 insteadOf 只把 `https://<host>/<owner>/<repo>` 换成 `http://<host>/<owner>/<repo>`，再用
// `http.http://<host>/.proxy` 把这些请求送到本机 egress——`git remote -v` 显示的仍是真实主机与 owner/repo，
// gh 据此推断仓库。其余路由（模型、gh.sock、记忆）都是 origin-form 请求，不受影响。
func proxyGitHost(r *http.Request) (string, bool) {
	if r.URL.Scheme != "http" || r.URL.Host == "" {
		return "", false
	}
	return strings.ToLower(r.URL.Host), true
}

// resolveGit 处理 git 路由（/git/<host>/<rest> 与代理形式）：只放行 smart-HTTP 端点与 Git LFS 接口，按策略限定仓库与读写，
// 注入主机令牌；代理形式下认不出的路径再查 LFS 门票（lfs.go）。
func (h *Handler) resolveGit(r *http.Request, pol Policy, host, rest string) (target, int) {
	if pol.NoGit {
		return target{}, http.StatusForbidden
	}
	// 解码后的路径（%2e%2e 已还原为 ..）原样转发，因此在这里拒绝一切非规范形式。
	if !canonicalPath(rest) {
		return target{}, http.StatusBadRequest
	}
	hg := pol.HostedGit
	hosted := hg != nil && host == hg.Host
	req, ok := classifyGit(r, rest, hosted)
	if !ok {
		if r.URL.Host == "" {
			return target{}, http.StatusNotFound
		}
		// 只有 classifyGit 不认的路径才查门票：门票不能遮蔽已识别的 git / LFS 端点。
		if t, st, ok := h.lfsTicketTarget(r, pol, host); ok {
			return t, st
		}
		return target{deny: denyProxyNotGit}, http.StatusForbidden
	}
	if req.reject != 0 {
		return target{deny: req.rejectMsg, lfs: true}, req.reject
	}
	// 托管仓库（M24 阶段 ③）：public_url 主机上只放行本项目的仓库，在本地服务，不查令牌、不经 github.com 规则。
	// 写操作（推送、LFS 上传与对象 PUT、锁的写操作）在只读评审线程里拒绝。
	if hosted {
		if req.repo != hg.Repo {
			return target{deny: denyHostedOther, lfs: req.lfs}, http.StatusForbidden
		}
		if req.write && h.threadGates().ReviewOnly {
			return target{deny: denyReviewOnly, lfs: req.lfs}, http.StatusForbidden
		}
		return target{hostedProject: hg.ProjectID, hostedEndpoint: req.endpoint, lfs: req.lfs}, http.StatusOK
	}
	receivePack := req.endpoint == "git-receive-pack"
	if deny, st := h.gitAllowed(pol, host, req.repo, req.write, receivePack); st != 0 {
		return target{deny: deny, lfs: req.lfs}, st
	}
	tok, ok := h.R.GitToken(host)
	if !ok {
		return target{lfs: req.lfs}, http.StatusNotFound
	}
	auth := gitBasicAuth(tok)
	up := &url.URL{Scheme: "https", Host: host, Path: rest}
	if host == "github.com" && pol.GitHub != nil && pol.GitHub.GitOrigin != "" {
		o, err := url.Parse(pol.GitHub.GitOrigin)
		if err != nil || o.Host == "" {
			return target{lfs: req.lfs}, http.StatusNotFound
		}
		up.Scheme, up.Host = o.Scheme, o.Host
	}
	githubOpen := host == "github.com" && pol.GitHub != nil
	t := target{
		url: up, auth: auth, redact: []string{strings.TrimPrefix(auth, "Basic "), tok},
		receivePack: githubOpen && receivePack, repo: req.repo,
		push: receivePack, gitHost: strings.ToLower(host),
		lfs: req.lfs, lfsBatch: req.lfsBatch, stripEncoding: req.lfsBatch,
	}
	if githubOpen {
		t.ghAccess = pol.GitHub
		t.ghToken = tok
	}
	return t, http.StatusOK
}

// gitAllowed 按策略判定一次 git / LFS 访问（status 为 0 表示放行）：github.com 开放时（M15）不按仓库限制，令牌是 OAuth
// 用户令牌，账号能做什么取决于账号自身；其它主机（PAT）维持 M2 规则：有项目时只放行项目仓库，无项目时任意仓库只读。
// 只读评审线程（R21，线程限制现查）在 github.com 上拒绝推送以外的写操作：推送协商（info/refs?service=git-receive-pack）、
// LFS 上传、建锁、解锁与 locks/verify；推送本身（receivePack）由门禁回 ng。
func (h *Handler) gitAllowed(pol Policy, host, repo string, write, receivePack bool) (deny string, status int) {
	githubOpen := host == "github.com" && pol.GitHub != nil
	if !githubOpen {
		if len(pol.GitRepos) > 0 {
			if !slices.ContainsFunc(pol.GitRepos[host], func(a string) bool { return NormalizeRepo(a) == repo }) {
				return denyGitOtherRepo, http.StatusForbidden
			}
		} else if write {
			return denyGitReadOnly, http.StatusForbidden
		}
	}
	if githubOpen && write && !receivePack && h.threadGates().ReviewOnly {
		return denyReviewOnly, http.StatusForbidden
	}
	return "", 0
}

// canonicalPath 报告 p（以 / 开头）不含 `.`、`..` 与空段，且与 path.Clean 的结果一致。
func canonicalPath(p string) bool {
	if p == "/" || path.Clean(p) != p {
		return false
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// gitEndpoint 只认 git smart-HTTP 端点，返回规范化的仓库路径与是否为写操作（receive-pack）。
func gitEndpoint(p, service string) (repo string, write, ok bool) {
	var base string
	switch {
	case strings.HasSuffix(p, "/info/refs"):
		if service != "git-upload-pack" && service != "git-receive-pack" {
			return "", false, false
		}
		base, write = strings.TrimSuffix(p, "/info/refs"), service == "git-receive-pack"
	case strings.HasSuffix(p, "/git-upload-pack"):
		base = strings.TrimSuffix(p, "/git-upload-pack")
	case strings.HasSuffix(p, "/git-receive-pack"):
		base, write = strings.TrimSuffix(p, "/git-receive-pack"), true
	default:
		return "", false, false
	}
	repo = NormalizeRepo(base)
	if strings.Count(repo, "/") < 1 {
		return "", false, false
	}
	return repo, write, true
}

// NormalizeRepo 把 "Owner/Repo.git" 规范为 "owner/repo"。
func NormalizeRepo(r string) string {
	return strings.ToLower(strings.TrimSuffix(strings.Trim(r, "/"), ".git"))
}

// substitute 把凭据位置上的占位符替换为真实值；返回用到的 id → 值。
// 任一占位符出现在非凭据位置、不属于该路由或主机不被允许，返回 false。
func (h *Handler) substitute(r *http.Request, t target) (map[string]string, bool) {
	used := map[string]string{}
	valid := true
	host := t.url.Hostname()
	repl := func(s string) string {
		return placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
			id := placeholderRe.FindStringSubmatch(m)[1]
			own, bound := slices.Contains(t.own, id), slices.Contains(t.hostBound, id)
			if !own && !bound {
				valid = false
				return m
			}
			v, hosts, ok := h.R.SecretByID(id)
			// hosts 为空的密钥只能用作它自己 provider 路由的 apiKey；其它引用必须绑定了匹配的主机。
			if !ok || (len(hosts) == 0 && !own) || (len(hosts) > 0 && !hostAllowed(hosts, host)) {
				valid = false
				return m
			}
			used[id] = v
			return v
		})
	}
	if strings.Contains(r.URL.Path, "oryn-ph-") || strings.Contains(r.URL.RawPath, "oryn-ph-") {
		return nil, false
	}
	for k, vs := range r.Header {
		if strings.Contains(k, "oryn-ph-") {
			return nil, false
		}
		for i, v := range vs {
			if !strings.Contains(v, "oryn-ph-") {
				continue
			}
			if !credentialHeaders[http.CanonicalHeaderKey(k)] {
				return nil, false
			}
			r.Header[k][i] = repl(v)
		}
	}
	if strings.Contains(r.URL.RawQuery, "oryn-ph-") {
		vals, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			return nil, false
		}
		for k, vs := range vals {
			for i, v := range vs {
				if !strings.Contains(v, "oryn-ph-") && !strings.Contains(k, "oryn-ph-") {
					continue
				}
				if k != credentialQuery {
					return nil, false
				}
				vals[k][i] = repl(v)
			}
		}
		r.URL.RawQuery = vals.Encode()
	}
	return used, valid
}

// HostAllowed 报告 host 是否在 hosts 之列（支持 `*.example.com` 通配）；hosts 为空时为 false。
func HostAllowed(hosts []string, host string) bool { return hostAllowed(hosts, host) }

func hostAllowed(hosts []string, host string) bool {
	h := strings.ToLower(host)
	for _, a := range hosts {
		a = strings.ToLower(a)
		if a == h || (strings.HasPrefix(a, "*.") && strings.HasSuffix(h, a[1:])) {
			return true
		}
	}
	return false
}

func redact(s string, pairs [][2]string) string {
	for _, p := range pairs {
		if p[0] != "" {
			s = strings.ReplaceAll(s, p[0], p[1])
		}
	}
	return s
}

// redactReader 流式地把真实值替换为占位符。只扣留「可能是某个真实值前缀」的尾部字节，
// 因此普通流式文本（SSE 事件以 \n\n 结尾）不会被延迟。
type redactReader struct {
	src   io.ReadCloser
	pairs [][2]string
	buf   []byte // 已读未输出
	out   []byte // 待交付
	chunk []byte
	eof   bool
}

func newRedactReader(src io.ReadCloser, pairs [][2]string) *redactReader {
	ps := make([][2]string, 0, len(pairs))
	for _, p := range pairs {
		if p[0] != "" {
			ps = append(ps, p)
		}
	}
	return &redactReader{src: src, pairs: ps, chunk: make([]byte, 32<<10)}
}

func (r *redactReader) Read(p []byte) (int, error) {
	for len(r.out) == 0 {
		if r.eof {
			if len(r.buf) == 0 {
				return 0, io.EOF
			}
			r.out, r.buf = []byte(redact(string(r.buf), r.pairs)), nil
			break
		}
		n, err := r.src.Read(r.chunk)
		r.buf = append(r.buf, r.chunk[:n]...)
		if err == io.EOF {
			r.eof = true
		} else if err != nil {
			return 0, err
		}
		for _, pr := range r.pairs {
			r.buf = bytes.ReplaceAll(r.buf, []byte(pr[0]), []byte(pr[1]))
		}
		hold := r.holdback()
		r.out = append(r.out, r.buf[:len(r.buf)-hold]...)
		r.buf = append(r.buf[:0:0], r.buf[len(r.buf)-hold:]...)
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	return n, nil
}

// holdback 返回 buf 末尾中是某个真实值真前缀的最长后缀长度。
func (r *redactReader) holdback() int {
	best := 0
	for _, pr := range r.pairs {
		s := pr[0]
		for l := min(len(s)-1, len(r.buf)); l > best; l-- {
			if bytes.HasSuffix(r.buf, []byte(s[:l])) {
				best = l
				break
			}
		}
	}
	return best
}

func (r *redactReader) Close() error { return r.src.Close() }

// ---- GitHub 写操作：识别、预登记与审计（M15 步骤 2.3） ----

// ghAudit 跟踪一个请求里已 Begin 的 GitHub 写操作，保证每个恰好 End 一次。
type ghAudit struct {
	r      Resolver
	writes []GitHubWrite
	// readResp 为真时（REST 写操作）从 2xx JSON 响应里取补充键与仓库；GraphQL 的响应不读。
	readResp bool
	done     bool
}

// end 以 status 结束全部写操作；nil 或已结束时什么都不做。
func (a *ghAudit) end(status int, keys []string) {
	if a == nil || a.done {
		return
	}
	a.done = true
	for _, w := range a.writes {
		a.r.GitHubWriteEnd(w, status, keys)
	}
}

// response 在响应交回 orb 之前结束写操作：REST 的 2xx JSON 响应读入（上限 ghWriteBodyMax）取补充键与仓库，读后复原响应体。
func (a *ghAudit) response(resp *http.Response) {
	if a == nil || a.done {
		return
	}
	var keys []string
	if a.readResp && resp.StatusCode/100 == 2 && strings.Contains(resp.Header.Get("Content-Type"), "json") {
		buf, err := io.ReadAll(io.LimitReader(resp.Body, ghWriteBodyMax+1))
		resp.Body = readCloser{io.MultiReader(bytes.NewReader(buf), resp.Body), resp.Body}
		if err == nil && len(buf) <= ghWriteBodyMax {
			keys = RespKeys(buf)
			if repo := respRepo(buf); repo != "" {
				for i := range a.writes {
					if a.writes[i].Repo == "" {
						a.writes[i].Repo = repo
					}
				}
			}
		}
	}
	a.end(resp.StatusCode, keys)
}

type readCloser struct {
	io.Reader
	io.Closer
}

// beginGitHubWrites 识别请求里的 GitHub 写操作，先过出站代理门禁（gate.go），再逐个调用 GitHubWriteBegin。
// 被拒绝时已写出响应并结束审计，responded 为真；不是写操作时 aud 为 nil。
//
// 门禁对请求里的全部写操作求值之后才调用 Begin：任一被拒绝则整个请求不转发，全部写操作都不 Begin（不预登记防回流），
// 以 Rejected 与拒绝原因记审计。
func (h *Handler) beginGitHubWrites(rw http.ResponseWriter, r *http.Request, t target) (aud *ghAudit, responded bool) {
	sideband := false
	var ops []GitHubOp
	switch {
	case t.receivePack && r.Method == http.MethodPost:
		var w GitHubWrite
		w, sideband = receivePackWrite(r, t.repo)
		aud = &ghAudit{r: h.R, writes: []GitHubWrite{w}}
	case t.gh && r.Method != http.MethodGet && r.Method != http.MethodHead:
		var writes []GitHubWrite
		var rest bool
		writes, ops, rest = ghRequestWrites(r, t)
		if len(writes) == 0 {
			return nil, false
		}
		aud = &ghAudit{r: h.R, writes: writes, readResp: rest}
	default:
		return nil, false
	}
	g := gateRun{h: h, gh: t.ghAccess}
	for i := range aud.writes {
		var op *GitHubOp
		if i < len(ops) {
			op = &ops[i]
		}
		verdict, denied := g.check(aud.writes[i], op)
		if !denied {
			aud.writes[i].Gate = verdict
			continue
		}
		for j := range aud.writes {
			aud.writes[j].Gate, aud.writes[j].Rejected = verdict, true
		}
		aud.end(denyGitHubWrite(rw, r, aud.writes, "oryn: "+verdict, sideband), nil)
		return nil, true
	}
	for i := range aud.writes {
		gate := h.R.GitHubWriteBegin(aud.writes[i])
		if gate == "" {
			continue
		}
		aud.writes = aud.writes[:i+1]
		aud.writes[i].Gate = gate
		aud.end(denyGitHubWrite(rw, r, aud.writes, "oryn: "+gate, sideband), nil)
		return nil, true
	}
	return aud, false
}

// denyGitHubWrite 写出拒绝响应并返回状态码：解析出命令的推送读完并丢弃请求体后回 200 + 每个 ref 的 `ng`
// （附录 B-7：git 显示 `! [remote rejected] … (oryn: <原因>)`），其余回 403。
func denyGitHubWrite(rw http.ResponseWriter, r *http.Request, writes []GitHubWrite, msg string, sideband bool) int {
	if w := writes[0]; w.Method == MethodPush && !w.Unparsed && len(w.Refs) > 0 {
		_, _ = io.Copy(io.Discard, r.Body)
		refs := make([]string, len(w.Refs))
		for i, u := range w.Refs {
			refs[i] = u.Ref
		}
		rw.Header().Set("Content-Type", "application/x-git-receive-pack-result")
		rw.Header().Set("Cache-Control", "no-cache")
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write(ReportStatusNG(refs, msg, sideband))
		return http.StatusOK
	}
	http.Error(rw, msg, http.StatusForbidden)
	return http.StatusForbidden
}

// receivePackWrite 读取推送请求开头的命令段（上限 receivePackHeadMax）并还原请求体；sideband 报告客户端是否协商了 side-band-64k。
// 带压缩编码、超限或格式不认识时 Unparsed 为真。
func receivePackWrite(r *http.Request, repo string) (w GitHubWrite, sideband bool) {
	w = GitHubWrite{Method: MethodPush, Op: "git-receive-pack", Repo: repo, Unparsed: true}
	cmds, caps, ok := readPushCommands(r)
	if !ok {
		return w, false
	}
	w.Refs, w.Unparsed = cmds, false
	return w, slices.Contains(caps, "side-band-64k")
}

// readPushCommands 读取推送请求开头的命令段（上限 receivePackHeadMax）并还原请求体。带压缩编码、超限或格式不认识时 ok 为假。
func readPushCommands(r *http.Request) (cmds []RefUpdate, caps []string, ok bool) {
	if !identityEncoding(r.Header) {
		return nil, nil, false
	}
	cmds, caps, prefix, err := ParseReceivePack(r.Body, receivePackHeadMax)
	r.Body = readCloser{io.MultiReader(bytes.NewReader(prefix), r.Body), r.Body}
	if err != nil {
		return nil, nil, false
	}
	return cmds, caps, true
}

// GitPush 是上游以 200 应答的一次 git-receive-pack（M24）：Host 是小写的 git 主机，Repo 是 NormalizeRepo 形式。
// 只看 HTTP 状态，不解析 report-status：被 ng 拒绝的 ref 也在其中，由接收方按镜像里是否出现新提交判断。
type GitPush struct {
	Host, Repo string
	Refs       []RefUpdate
}

// PushRecorder 是 Resolver 的可选能力：上游以 200 应答推送后调用（不实现即不记录）。在响应转发之前同步调用。
type PushRecorder interface{ GitPushed(p GitPush) }

// pushRefs 返回要记录的推送命令：只对 git-receive-pack 的 POST；门禁已解析过的推送复用它的结果，否则在这里读命令段。
// Resolver 不实现 PushRecorder 时不读请求体。
func (h *Handler) pushRefs(r *http.Request, t target, aud *ghAudit) []RefUpdate {
	if !t.push || r.Method != http.MethodPost {
		return nil
	}
	if _, ok := h.R.(PushRecorder); !ok {
		return nil
	}
	if aud != nil {
		for _, w := range aud.writes {
			if w.Method == MethodPush {
				return w.Refs
			}
		}
		return nil
	}
	cmds, _, _ := readPushCommands(r)
	return cmds
}

// ghRequestWrites 识别 /gh/ 上的一个非 GET 请求：REST 得到一个写操作，GraphQL mutation 每个根字段一个，GraphQL query 为空。
// ops 与 writes 一一对应，是门禁用的识别结果（只有路径级信息时取路径解析的结果，路径也解析不了时为零值）。
// 请求体读入后用 bytes.Reader 复原；上传主机的二进制附件不读，只按路径识别。rest 报告是否为 REST（GraphQL 的响应不读）。
func ghRequestWrites(r *http.Request, t target) (writes []GitHubWrite, ops []GitHubOp, rest bool) {
	method := strings.ToUpper(r.Method)
	p := strings.ToLower(strings.Trim(t.ghPath, "/"))
	rest = p != "graphql" && p != "graphql.json"
	var body []byte
	parsed := true
	if !t.ghUpload {
		if !identityEncoding(r.Header) {
			parsed = false
		} else {
			buf, err := io.ReadAll(io.LimitReader(r.Body, ghWriteBodyMax+1))
			if err == nil && len(buf) <= ghWriteBodyMax {
				r.Body = readCloser{bytes.NewReader(buf), r.Body}
				body = buf
			} else {
				r.Body = readCloser{io.MultiReader(bytes.NewReader(buf), r.Body), r.Body}
				parsed = false
			}
		}
	}
	if parsed {
		if req, err := ParseGitHubRequest(method, t.ghPath, t.ghRawPath, body); err == nil {
			for _, op := range req.Ops {
				writes = append(writes, GitHubWrite{
					Method: method, Category: op.Category, Target: op.Target, Op: op.Op, Repo: op.Repo, BodySHA256: BodyHash(op.Body),
				})
			}
			return writes, req.Ops, rest
		}
	}
	// 只有路径级信息：门禁关闭时只审计，门禁开启时由门禁拒绝（R19「失败即拒绝」）
	w := GitHubWrite{Method: method, Op: method + " " + t.ghPath, Unparsed: true}
	var op GitHubOp
	if req, err := ParseGitHubRequest(method, t.ghPath, t.ghRawPath, nil); err == nil && len(req.Ops) == 1 {
		op = req.Ops[0]
		w.Category, w.Op, w.Repo = op.Category, op.Op, op.Repo
	}
	return []GitHubWrite{w}, []GitHubOp{op}, rest
}

// identityEncoding 报告请求体没有压缩编码。
func identityEncoding(h http.Header) bool {
	e := strings.TrimSpace(h.Get("Content-Encoding"))
	return e == "" || strings.EqualFold(e, "identity")
}

// respRepo 从 REST 响应顶层的 repository_url / url 里取 owner/repo（R23：路径里取不到仓库时用，如 /repositories/{id}/…）。
func respRepo(body []byte) string {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return ""
	}
	for _, k := range []string{"repository_url", "url"} {
		s, _ := m[k].(string)
		_, after, ok := strings.Cut(s, "/repos/")
		if !ok {
			continue
		}
		if seg := strings.SplitN(after, "/", 3); len(seg) >= 2 && seg[0] != "" && seg[1] != "" {
			return strings.ToLower(seg[0] + "/" + seg[1])
		}
	}
	return ""
}
