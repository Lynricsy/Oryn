package egress

// Git LFS 经出站代理（M24）：orb 内 git-lfs 发来的 `<仓库>.git/info/lfs/…` 请求与 git smart-HTTP 一样按仓库与读写放行、
// 注入主机令牌。batch 的请求体解析后重新序列化再转发（读写判定与上游读到的是同一份字节）；batch 的 200 响应同样解析后
// 重写：与上游同主机的传输地址去掉凭据头、改写成经代理的 http:// 地址并登记门票，其它主机的地址交给 orb 直连，
// 无法解析的响应换成 502。令牌与上游回显或新签发的凭据都不进 orb。

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/lfs"
)

const (
	lfsBatchRespMax = 4 << 20 // batch 响应的读取上限；超出回 502（失败关闭）
	lfsTicketMax    = 4096    // 每台 orb 的门票上限
	lfsTicketTTL    = time.Hour
)

// 拒绝与改写的说明（代理回给 git-lfs 时加 `oryn: ` 前缀）。
const (
	denyLFSEncoded    = "批量请求不能压缩"
	denyLFSTooLarge   = "批量请求过大"
	denyLFSInvalid    = "批量请求无效"
	lfsRespUnparsable = "oryn: 上游的 LFS 批量响应无法解析"
	lfsRespNot200     = "oryn: 上游的 LFS 批量响应不是 200"
	lfsActionDropped  = "oryn: 上游给出的传输地址不能经代理访问"
)

// lfsLockIDRe 是 `info/lfs/locks/<id>/unlock` 里锁 id 的形式。
var lfsLockIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// lfsActionMethods 是 batch 响应里认得的 action 与 orb 用它发请求的方法；其它名字的 action 直接丢掉。
var lfsActionMethods = map[string]string{"download": http.MethodGet, "upload": http.MethodPut, "verify": http.MethodPost}

// lfsKeepHeaders 是同主机 action 的 header 里保留的键；其余（凭据）一律删掉，由代理注入。
var lfsKeepHeaders = map[string]bool{"Accept": true, "Content-Type": true, "Transfer-Encoding": true}

// gitReq 是 classifyGit 认出的 git 请求（smart-HTTP 或 Git LFS）。
type gitReq struct {
	repo      string // NormalizeRepo 形式
	write     bool
	endpoint  string // 相对 <repo>.git/ 的端点：info/refs、git-upload-pack、git-receive-pack、info/lfs/objects/batch、info/lfs/locks…
	lfs       bool   // info/lfs/ 端点：拒绝时回 LFS JSON 错误体
	lfsBatch  bool   // objects/batch：请求体已由 lfsBatchRequest 规范化，响应经 lfsRewrite
	reject    int    // 非 0：batch 请求体不可接受，直接以这个状态码（413 / 415 / 422）回 LFS 错误体
	rejectMsg string
}

// classifyGit 认出 git smart-HTTP 端点与 Git LFS 端点（rest 已是规范路径）。batch 端点在这里调用 lfsBatchRequest，
// 请求体不可接受时 ok 仍为真、reject 非 0。其它方法或路径一律不认。
func classifyGit(r *http.Request, rest string) (gitReq, bool) {
	if repo, write, ok := gitEndpoint(rest, r.URL.Query().Get("service")); ok {
		ep := rest[strings.LastIndexByte(rest, '/')+1:]
		if ep == "refs" {
			ep = "info/refs"
		}
		return gitReq{repo: repo, write: write, endpoint: ep}, true
	}
	// git-lfs 的 endpoint 一定是 <remote>.git/info/lfs（server-discovery.md），所以要求有 .git。
	i := strings.Index(rest, ".git/info/lfs/")
	if i < 0 {
		return gitReq{}, false
	}
	repo := NormalizeRepo(rest[:i+len(".git")])
	if strings.Count(repo, "/") < 1 {
		return gitReq{}, false
	}
	req := gitReq{repo: repo, endpoint: rest[i+len(".git/"):], lfs: true}
	m := r.Method
	switch ep := req.endpoint; {
	case ep == "info/lfs/objects/batch" && m == http.MethodPost:
		req.lfsBatch = true
		br, status, msg := lfsBatchRequest(r)
		if status != 0 {
			req.reject, req.rejectMsg = status, msg
			return req, true
		}
		req.write = br.Operation == "upload"
	case ep == "info/lfs/locks" && (m == http.MethodGet || m == http.MethodPost):
		req.write = m == http.MethodPost
	case ep == "info/lfs/locks/verify" && m == http.MethodPost:
		req.write = true // locking.md：要求推送权限
	case strings.HasPrefix(ep, "info/lfs/locks/") && strings.HasSuffix(ep, "/unlock") && m == http.MethodPost:
		id := strings.TrimSuffix(strings.TrimPrefix(ep, "info/lfs/locks/"), "/unlock")
		if !lfsLockIDRe.MatchString(id) {
			return gitReq{}, false
		}
		req.write = true
	default:
		return gitReq{}, false
	}
	return req, true
}

// lfsBatchRequest 解析 batch 请求体，并把 r.Body 换成对解析结果的重新序列化（同时重设 ContentLength 与 Content-Length 头）：
// 上游收到的字节就是代理据以判断读写的那一份，两边不会对同一个请求体读出不同的 operation（Go 的 encoding/json 字段名
// 不分大小写、重复键取后一个，上游的解析器未必如此）。oid 在这里校验：有的上游把请求里的 oid 直接拼进 href。
// status 为 0 表示可以转发；否则是应当回给 orb 的状态码，msg 是说明。
func lfsBatchRequest(r *http.Request) (req lfs.BatchRequest, status int, msg string) {
	if !identityEncoding(r.Header) {
		return req, http.StatusUnsupportedMediaType, denyLFSEncoded
	}
	var body []byte
	if r.Body != nil {
		b, err := io.ReadAll(io.LimitReader(r.Body, lfs.BatchBodyMax+1))
		if err != nil {
			return req, http.StatusUnprocessableEntity, denyLFSInvalid
		}
		body = b
	}
	if len(body) > lfs.BatchBodyMax {
		return req, http.StatusRequestEntityTooLarge, denyLFSTooLarge
	}
	var in lfs.BatchRequest
	if json.Unmarshal(body, &in) != nil {
		return req, http.StatusUnprocessableEntity, denyLFSInvalid
	}
	if len(in.Objects) > lfs.BatchMaxObjects {
		return req, http.StatusRequestEntityTooLarge, denyLFSTooLarge
	}
	if (in.Operation != "download" && in.Operation != "upload") || (in.HashAlgo != "" && in.HashAlgo != "sha256") {
		return req, http.StatusUnprocessableEntity, denyLFSInvalid
	}
	req = lfs.BatchRequest{Operation: in.Operation, Transfers: []string{"basic"}, Objects: make([]lfs.ObjectSpec, 0, len(in.Objects)), HashAlgo: in.HashAlgo}
	for _, o := range in.Objects {
		if !lfs.ValidOID(o.OID) || o.Size < 0 {
			return lfs.BatchRequest{}, http.StatusUnprocessableEntity, denyLFSInvalid
		}
		req.Objects = append(req.Objects, lfs.ObjectSpec{OID: o.OID, Size: o.Size})
	}
	if in.Ref != nil {
		req.Ref = &lfs.Ref{Name: in.Ref.Name}
	}
	out, err := json.Marshal(req)
	if err != nil {
		return lfs.BatchRequest{}, http.StatusUnprocessableEntity, denyLFSInvalid
	}
	r.Body = io.NopCloser(bytes.NewReader(out))
	r.ContentLength = int64(len(out))
	r.TransferEncoding = nil
	r.Header.Set("Content-Length", strconv.Itoa(len(out)))
	return req, 0, ""
}

// lfsTicket 是改写过的同主机传输地址：orb 以 `<方法> http://<gitHost><路径>[?查询]` 访问时，代理按当前策略重查读写后
// 注入主机令牌转发到 upstream。
type lfsTicket struct {
	upstream   *url.URL
	host, repo string
	write      bool
	expires    time.Time
}

func (h *Handler) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func lfsTicketKey(method, gitHost, escapedPath, rawQuery string) string {
	k := method + " http://" + gitHost + escapedPath
	if rawQuery != "" {
		k += "?" + rawQuery
	}
	return k
}

// addTickets 登记门票；超过 lfsTicketMax 时先删过期的，再删最早到期的。
func (h *Handler) addTickets(tks map[string]lfsTicket, now time.Time) {
	h.lfsMu.Lock()
	defer h.lfsMu.Unlock()
	if h.lfsTickets == nil {
		h.lfsTickets = map[string]lfsTicket{}
	}
	for k, tk := range tks {
		if _, ok := h.lfsTickets[k]; !ok && len(h.lfsTickets) >= lfsTicketMax {
			for kk, v := range h.lfsTickets {
				if !now.Before(v.expires) {
					delete(h.lfsTickets, kk)
				}
			}
			for len(h.lfsTickets) >= lfsTicketMax {
				var oldest string
				var at time.Time
				for kk, v := range h.lfsTickets {
					if oldest == "" || v.expires.Before(at) {
						oldest, at = kk, v.expires
					}
				}
				delete(h.lfsTickets, oldest)
			}
		}
		h.lfsTickets[k] = tk
	}
}

// lfsTicketTarget 只在 classifyGit 不认该路径时被调用（代理形式）：命中未过期的门票时按当前策略重查读写、重新取令牌，
// 返回转发到门票上游的 target；未命中或过期时 ok=false。
func (h *Handler) lfsTicketTarget(r *http.Request, pol Policy, host string) (target, int, bool) {
	key := lfsTicketKey(r.Method, host, r.URL.EscapedPath(), r.URL.RawQuery)
	now := h.clock()
	h.lfsMu.Lock()
	tk, ok := h.lfsTickets[key]
	if ok && !now.Before(tk.expires) {
		delete(h.lfsTickets, key)
		ok = false
	}
	h.lfsMu.Unlock()
	if !ok {
		return target{}, 0, false
	}
	if deny, st := h.gitAllowed(pol, tk.host, tk.repo, tk.write, false); st != 0 {
		return target{deny: deny, lfs: true}, st, true
	}
	tok, ok := h.R.GitToken(tk.host)
	if !ok {
		return target{lfs: true}, http.StatusNotFound, true
	}
	up := *tk.upstream
	auth := gitBasicAuth(tok)
	t := target{url: &up, auth: auth, redact: []string{strings.TrimPrefix(auth, "Basic "), tok}, gitHost: host, repo: tk.repo, lfs: true}
	if tk.host == "github.com" && pol.GitHub != nil {
		t.ghAccess, t.ghToken = pol.GitHub, tok
	}
	return t, http.StatusOK, true
}

// gitBasicAuth 是注入 git 上游的 Authorization 值。
func gitBasicAuth(tok string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok))
}

// lfsRewrite（M24）把上游对 objects/batch 的 200 响应换成代理重新序列化的 lfs.BatchResponse：
// 与上游同主机的 action 去掉凭据头并改写成经代理的地址（登记门票），不能改写的删掉；其它主机的 action 只去掉带已知令牌的头。
// 响应无法解析时把它换成 502 的 LFS 错误体。
func (h *Handler) lfsRewrite(resp *http.Response, t target) {
	if enc := strings.TrimSpace(resp.Header.Get("Content-Encoding")); enc != "" && !strings.EqualFold(enc, "identity") {
		lfsBadGateway(resp, lfsRespUnparsable)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, lfsBatchRespMax+1))
	if err != nil || len(raw) > lfsBatchRespMax {
		lfsBadGateway(resp, lfsRespUnparsable)
		return
	}
	var br lfs.BatchResponse
	if json.Unmarshal(raw, &br) != nil {
		lfsBadGateway(resp, lfsRespUnparsable)
		return
	}
	now := h.clock()
	tickets := map[string]lfsTicket{}
	for i := range br.Objects {
		o := &br.Objects[i]
		if len(o.Actions) == 0 {
			o.Actions = nil
			continue
		}
		out := map[string]*lfs.Action{}
		pending := map[string]lfsTicket{}
		bad := false
		for name, a := range o.Actions {
			method, known := lfsActionMethods[name]
			if !known {
				continue
			}
			na, key, tk, ok := h.rewriteAction(t, name, method, a, now)
			if !ok {
				bad = true
				break
			}
			out[name] = na
			if key != "" {
				pending[key] = tk
			}
		}
		if bad {
			o.Actions = nil
			o.Error = &lfs.ObjectError{Code: http.StatusUnprocessableEntity, Message: lfsActionDropped}
			continue
		}
		o.Actions = out
		if len(out) == 0 {
			o.Actions = nil
		}
		for k, v := range pending {
			tickets[k] = v
		}
	}
	if br.Objects == nil {
		br.Objects = []lfs.ObjectResult{}
	}
	body, err := json.Marshal(br)
	if err != nil {
		lfsBadGateway(resp, lfsRespUnparsable)
		return
	}
	if len(tickets) > 0 {
		h.addTickets(tickets, now)
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("ETag")
}

// rewriteAction 处理一个 action：同主机的改写成经代理的地址（返回门票键与门票），其它主机的原样保留 href、去掉带已知令牌的头。
// ok 为假表示这个 action 不能交给 orb。
func (h *Handler) rewriteAction(t target, name, method string, a *lfs.Action, now time.Time) (*lfs.Action, string, lfsTicket, bool) {
	if a == nil {
		return nil, "", lfsTicket{}, false
	}
	u, err := url.Parse(a.Href)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return nil, "", lfsTicket{}, false
	}
	if !strings.EqualFold(u.Hostname(), t.url.Hostname()) {
		hdr := map[string]string{}
		for k, v := range a.Header {
			if !carriesSecret(v, t.redact) {
				hdr[k] = v
			}
		}
		return &lfs.Action{Href: a.Href, Header: nilIfEmpty(hdr), ExpiresIn: a.ExpiresIn, ExpiresAt: a.ExpiresAt}, "", lfsTicket{}, true
	}
	if !strings.EqualFold(u.Scheme, t.url.Scheme) || portOf(u) != portOf(t.url) || !canonicalPath(u.Path) {
		return nil, "", lfsTicket{}, false
	}
	exp := now.Add(lfsTicketTTL)
	switch {
	case a.ExpiresIn > 0:
		if a.ExpiresIn < int64(lfsTicketTTL/time.Second) {
			exp = now.Add(time.Duration(a.ExpiresIn) * time.Second)
		}
	default:
		if e, ok := a.Expiry(now); ok && e.Before(exp) {
			exp = e
		}
	}
	if !exp.After(now) {
		return nil, "", lfsTicket{}, false
	}
	hdr := map[string]string{}
	for k, v := range a.Header {
		if lfsKeepHeaders[http.CanonicalHeaderKey(k)] {
			hdr[k] = v
		}
	}
	up := *u
	up.Fragment, up.RawFragment = "", ""
	key := lfsTicketKey(method, t.gitHost, u.EscapedPath(), u.RawQuery)
	href := strings.TrimPrefix(key, method+" ")
	secs := int64(exp.Sub(now) / time.Second)
	tk := lfsTicket{upstream: &up, host: t.gitHost, repo: t.repo, write: name != "download", expires: exp}
	return &lfs.Action{Href: href, Header: nilIfEmpty(hdr), ExpiresIn: max(secs, 1)}, key, tk, true
}

// carriesSecret 报告请求头的值是否含有已知令牌：值本身，或按 `Basic <base64>` 解出的明文。
func carriesSecret(v string, secrets []string) bool {
	vals := []string{v}
	if scheme, cred, ok := strings.Cut(strings.TrimSpace(v), " "); ok && strings.EqualFold(scheme, "Basic") {
		if dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cred)); err == nil {
			vals = append(vals, string(dec))
		}
	}
	for _, s := range secrets {
		if s == "" {
			continue
		}
		for _, x := range vals {
			if strings.Contains(x, s) {
				return true
			}
		}
	}
	return false
}

func nilIfEmpty(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

// portOf 返回 URL 的端口，缺省时按 scheme 补上。
func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// lfsBadGateway 把响应换成 502 的 LFS 错误体（上游的内容一概不交给 orb）。
func lfsBadGateway(resp *http.Response, msg string) {
	_ = resp.Body.Close()
	body := lfs.ErrorBody(msg)
	resp.StatusCode = http.StatusBadGateway
	resp.Status = "502 Bad Gateway"
	resp.Header = http.Header{"Content-Type": {lfs.MediaType}, "Content-Length": {strconv.Itoa(len(body))}}
	resp.Trailer = nil
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.TransferEncoding = nil
	resp.Uncompressed = false
}
