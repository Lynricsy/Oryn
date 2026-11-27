package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Lynricsy/Oryn/internal/aiconsent"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 模型中转（步骤 2.5）：Hindsight 是 RelayPathPrefix 的唯一调用方，凭中转令牌鉴权。段之后的路径是该协议的原生路径
// （附录 A 第 3 项实测，非流式）：
//   - `<用途>/chat/completions`、`<用途>/responses`（openai）、`<用途>/v1/messages`（anthropic）；
//   - `gemini/v1beta/models/<别名>:generateContent`（用途按别名认）；
//   - `embed/embeddings`、`rerank/v1/rerank`：按 memory_endpoints 转发；嵌入响应缺失的 `data[].index` 按位置补齐。
// 其余路径一律 404，别的东西到不了上游。

// ModelUpstream 是中央模型目录里一个模型的上游，由 orynd 用 models.yml 的出站路由实现（relayroute 包）。
type ModelUpstream struct {
	// Family 是模型的协议类型。
	Family apigen.MemoryFamily
	// Forward 转发一个原生协议请求：r.URL.Path 是原生路径（如 `/chat/completions`、
	// `/v1beta/models/<真实模型>:generateContent`），请求里已不带任何凭据；实现负责注入 provider 的真实密钥，
	// 并把响应里回显的密钥脱敏（与 orb 出站代理的规则相同）。
	Forward http.Handler
}

const (
	// relayBodyMax 是中转请求体的上限（retain 的提示词、嵌入的批量输入）。
	relayBodyMax = 32 << 20
	// relayRespMax 是嵌入与重排响应的上限（要整体读入以便脱敏与计数）。
	relayRespMax = 64 << 20
	// usageCaptureMax 是为解析 usage 而留存的 LLM 响应字节上限；超出时只计调用次数。
	usageCaptureMax = 8 << 20
	// usageRetention 是用量行的保留期：页面只显示 7 天，多留一天余量。
	usageRetention = 8 * 24 * time.Hour
)

// endpointKeyAAD 是嵌入 / 重排接口 key 在 secrets.Box 里的 AAD（与迁移 0036 的注释一致）。
func endpointKeyAAD(kind apigen.MemoryEndpointKind) string {
	return "memory/endpoint/" + string(kind) + "/api-key"
}

// RelayHandler 返回挂在 RelayPathPrefix 下的模型中转入口（不在 /api/v1 鉴权组内）。
func (s *Service) RelayHandler() http.Handler { return &relay{s: s} }

type relay struct {
	s *Service
	// pruned 是最近一次清理过期用量时的小时（Unix 秒），每小时至多清理一次。
	pruned atomic.Int64
}

// relayCredentialHeaders 是 Hindsight 各 SDK 放中转令牌的请求头（附录 A 第 3 项）；转发前一律删除。
var relayCredentialHeaders = []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key"}

// relayTokens 取出请求里所有可能是中转令牌的值：`Authorization: Bearer`、`x-api-key`、`x-goog-api-key`，
// 以及 gemini 的 `?key=`（google-genai 实测用请求头，查询参数只作兼容）。
func relayTokens(r *http.Request) []string {
	var out []string
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		out = append(out, strings.TrimSpace(v))
	}
	for _, h := range []string{"X-Api-Key", "X-Goog-Api-Key"} {
		if v := r.Header.Get(h); v != "" {
			out = append(out, v)
		}
	}
	if v := r.URL.Query().Get("key"); v != "" {
		out = append(out, v)
	}
	return out
}

func (h *relay) authorized(r *http.Request) bool {
	for _, t := range relayTokens(r) {
		if h.s.VerifyRelayToken(r.Context(), t) {
			return true
		}
	}
	return false
}

func (h *relay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, RelayPathPrefix)
	if !ok || r.URL.RawPath != "" {
		WriteError(w, http.StatusNotFound, "not_found", "未知的中转路径")
		return
	}
	if !h.authorized(r) {
		WriteError(w, http.StatusUnauthorized, "unauthorized", "中转令牌无效")
		return
	}
	if r.Method != http.MethodPost {
		WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "中转只接受 POST")
		return
	}
	segment, native, _ := strings.Cut(rest, "/")
	native = "/" + native
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, relayBodyMax))
	if err != nil {
		WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "请求体过大或读取失败")
		return
	}
	switch segment {
	case RelayEmbed:
		if native != "/embeddings" {
			break
		}
		h.endpoint(w, r, apigen.MemoryEndpointEmbed, native, body)
		return
	case RelayRerank:
		if native != rerankEndpoint {
			break
		}
		h.endpoint(w, r, apigen.MemoryEndpointRerank, native, body)
		return
	case RelayGemini:
		// `/v1beta/models/<别名>:<方法>`：gemini 不读各用途的 BASE_URL，用途只能按别名认
		alias, method, ok := strings.Cut(strings.TrimPrefix(native, "/v1beta/models/"), ":")
		purpose, known := PurposeForAlias(alias)
		if !strings.HasPrefix(native, "/v1beta/models/") || !ok || !known ||
			(method != "generateContent" && method != "streamGenerateContent") {
			break
		}
		h.llm(w, r, purpose, apigen.MemoryFamilyGemini, body, func(model string) string {
			return "/v1beta/models/" + model + ":" + method
		}, method == "streamGenerateContent")
		return
	default:
		purpose := apigen.MemoryPurpose(segment)
		var fam apigen.MemoryFamily
		switch native {
		case "/chat/completions", "/responses":
			fam = apigen.MemoryFamilyOpenai
		case "/v1/messages":
			fam = apigen.MemoryFamilyAnthropic
		}
		if !purpose.Valid() || fam == "" {
			break
		}
		h.llm(w, r, purpose, fam, body, func(string) string { return native }, false)
		return
	}
	WriteError(w, http.StatusNotFound, "not_found", "未知的中转路径")
}

// selectedModel 返回用途选定的模型：没单独选的跟随 default；都没选时 ok 为假。
func (s *Service) selectedModel(ctx context.Context, p apigen.MemoryPurpose) (db.MemoryModel, bool, error) {
	rows, err := s.cfg.Q.ListMemoryModels(ctx)
	if err != nil {
		return db.MemoryModel{}, false, err
	}
	var def *db.MemoryModel
	for i := range rows {
		switch apigen.MemoryPurpose(rows[i].Purpose) {
		case p:
			return rows[i], true, nil
		case apigen.MemoryPurposeDefault:
			def = &rows[i]
		}
	}
	if def == nil {
		return db.MemoryModel{}, false, nil
	}
	return *def, true, nil
}

// deployedFamily 返回 `.env` 里该用途已部署的协议类型（主人点「已重启」后更新）；没有记录时按 openai。
func (s *Service) deployedFamily(ctx context.Context, p apigen.MemoryPurpose) (apigen.MemoryFamily, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return "", err
	}
	var fams map[apigen.MemoryPurpose]apigen.MemoryFamily
	if st.DeployedFamilies != "" {
		if err := json.Unmarshal([]byte(st.DeployedFamilies), &fams); err != nil {
			return "", err
		}
	}
	if f := fams[p]; f.Valid() {
		return f, nil
	}
	return apigen.MemoryFamilyOpenai, nil
}

// llm 中转一个 LLM 请求：查出用途选定的模型，核对协议类型，把别名换成真实模型，经 Upstream 转发并计用量。
// reqFam 是请求所用的协议（由路径决定）；nativePath 按真实模型名给出转发的原生路径（gemini 的模型名在路径里）。
func (h *relay) llm(w http.ResponseWriter, r *http.Request, purpose apigen.MemoryPurpose, reqFam apigen.MemoryFamily,
	body []byte, nativePath func(model string) string, stream bool) {
	ctx := r.Context()
	if h.s.cfg.Upstream == nil {
		WriteError(w, http.StatusServiceUnavailable, "relay_unavailable", "模型中转未装配")
		return
	}
	sel, ok, err := h.s.selectedModel(ctx, purpose)
	if err != nil {
		slog.Warn("记忆中转：读取选定模型失败", "purpose", purpose, "err", err)
		WriteError(w, http.StatusInternalServerError, "internal", "读取选定模型失败")
		return
	}
	if !ok {
		WriteError(w, http.StatusConflict, "model_not_selected", "管理页还没有为记忆选择模型")
		return
	}
	deployed, err := h.s.deployedFamily(ctx, purpose)
	if err != nil {
		slog.Warn("记忆中转：读取已部署协议失败", "err", err)
		WriteError(w, http.StatusInternalServerError, "internal", "读取已部署协议失败")
		return
	}
	up, err := h.s.cfg.Upstream(ctx, sel.ProviderKey, sel.ModelID)
	if err != nil {
		slog.Warn("记忆中转：解析模型上游失败", "purpose", purpose, "provider", sel.ProviderKey, "model", sel.ModelID, "err", err)
		WriteError(w, http.StatusBadGateway, "upstream_unavailable", "解析模型上游失败："+err.Error())
		return
	}
	// R16：选定模型换了协议、而主人还没按新 `.env` 重启 Hindsight 并点「已重启」之前，该用途一律 409
	if up.Family != deployed || reqFam != deployed {
		WriteError(w, http.StatusConflict, "family_mismatch",
			"用途 "+string(purpose)+" 选定模型的协议（"+string(up.Family)+"）与已部署的（"+string(deployed)+"）不一致，请按新的 .env 重启 Hindsight")
		return
	}
	if reqFam != apigen.MemoryFamilyGemini {
		var isStream bool
		if body, isStream, err = replaceModel(body, sel.ModelID); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid_request", "请求体不是 JSON 对象")
			return
		}
		stream = stream || isStream
	}
	out := cleanRequest(r, nativePath(sel.ModelID), body)
	rec := &captureWriter{ResponseWriter: w, limit: usageCaptureMax}
	up.Forward.ServeHTTP(rec, out)
	if rec.ok() {
		var in, outTok int64
		if !stream && !rec.truncated {
			in, outTok = parseUsage(rec.buf.Bytes())
		}
		h.record(context.WithoutCancel(ctx), string(purpose), in, outTok)
	}
}

// replaceModel 把请求体顶层的 `model` 换成真实模型名，并报告是否为流式请求（流式不解析用量，原样透传）。
func replaceModel(body []byte, model string) ([]byte, bool, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return nil, false, errors.New("not a JSON object")
	}
	stream := bytes.Equal(bytes.TrimSpace(m["stream"]), []byte("true"))
	mv, err := json.Marshal(model)
	if err != nil {
		return nil, false, err
	}
	m["model"] = mv
	b, err := json.Marshal(m)
	return b, stream, err
}

// cleanRequest 构造转发请求：原生路径、改写后的请求体，去掉中转令牌（各凭据头与 `key` 查询参数）、Cookie 与
// Accept-Encoding（让 Transport 自己协商并解压，脱敏与用量解析读到的是明文）。
func cleanRequest(r *http.Request, native string, body []byte) *http.Request {
	out := r.Clone(r.Context())
	out.URL.Path, out.URL.RawPath, out.RequestURI = native, "", ""
	q := out.URL.Query()
	q.Del("key")
	out.URL.RawQuery = q.Encode()
	for _, k := range relayCredentialHeaders {
		out.Header.Del(k)
	}
	out.Header.Del("Cookie")
	out.Header.Del("Accept-Encoding")
	out.Header.Del("Content-Length")
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	out.GetBody = nil
	return out
}

// endpoint 中转嵌入或重排请求到管理页配置的远端接口：别名换成配置的模型，注入配置的 key，响应里的 key 脱敏。
func (h *relay) endpoint(w http.ResponseWriter, r *http.Request, kind apigen.MemoryEndpointKind, native string, body []byte) {
	ctx := r.Context()
	eps, err := h.s.cfg.Q.ListMemoryEndpoints(ctx)
	if err != nil {
		slog.Warn("记忆中转：读取接口配置失败", "kind", kind, "err", err)
		WriteError(w, http.StatusInternalServerError, "internal", "读取接口配置失败")
		return
	}
	var ep *db.MemoryEndpoint
	for i := range eps {
		if eps[i].Kind == string(kind) {
			ep = &eps[i]
		}
	}
	if ep == nil {
		WriteError(w, http.StatusConflict, "endpoint_not_configured", "管理页还没有配置"+string(kind)+"接口")
		return
	}
	var key string
	if len(ep.ApiKeyEnc) > 0 {
		plain, err := h.s.cfg.Box.Open(endpointKeyAAD(kind), ep.ApiKeyEnc)
		if err != nil {
			slog.Warn("记忆中转：解密接口 key 失败", "kind", kind, "err", err)
			WriteError(w, http.StatusInternalServerError, "internal", "解密接口 key 失败")
			return
		}
		key = string(plain)
	}
	if body, _, err = replaceModel(body, ep.Model); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request", "请求体不是 JSON 对象")
		return
	}
	u, err := url.Parse(strings.TrimRight(ep.BaseUrl, "/") + native)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		WriteError(w, http.StatusBadGateway, "upstream_unavailable", "接口地址无效")
		return
	}
	// AI 数据同意：第三方接口须主人已同意，拒绝时不发出请求（本机的 gemini-rerank 适配器按 Google 判定）
	prefix := aiconsent.PrefixMemoryEmbed
	if kind == apigen.MemoryEndpointRerank {
		prefix = aiconsent.PrefixMemoryRerank
	}
	if id, third := aiconsent.EndpointRecipient(prefix, ep.BaseUrl, ep.Model); third {
		if err := aiconsent.Check(ctx, h.s.cfg.AIConsent, id); err != nil {
			WriteError(w, http.StatusForbidden, aiconsent.Code, err.Error())
			return
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		WriteError(w, http.StatusBadGateway, "upstream_unavailable", "接口地址无效")
		return
	}
	for _, k := range []string{"Content-Type", "Accept", "User-Agent"} {
		if v := r.Header.Get(k); v != "" {
			req.Header.Set(k, v)
		}
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := h.s.cfg.HTTP.Do(req)
	if err != nil {
		slog.Warn("记忆中转：接口不可达", "kind", kind, "host", u.Host, "err", redact(err.Error(), key))
		WriteError(w, http.StatusBadGateway, "upstream_unreachable", "接口不可达")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, relayRespMax+1))
	if err != nil || len(respBody) > relayRespMax {
		WriteError(w, http.StatusBadGateway, "upstream_unreachable", "接口响应读取失败或过大")
		return
	}
	if key != "" {
		respBody = bytes.ReplaceAll(respBody, []byte(key), []byte(redacted))
	}
	if kind == apigen.MemoryEndpointEmbed && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		respBody = fillEmbeddingIndex(respBody)
	}
	for k, vs := range resp.Header {
		if hopHeader(k) {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, redact(v, key))
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		in, out := parseUsage(respBody)
		h.record(context.WithoutCancel(ctx), string(kind), in, out)
	}
}

// fillEmbeddingIndex 补齐嵌入响应里缺失或为 null 的 `data[].index`（按数组位置）。Gemini 的 OpenAI 兼容接口按 proto3
// 省略零值，第 0 条的 index 是 null；Hindsight 按 index 排序（embeddings.py `sorted(response.data, key=lambda x: x.index)`），
// 一批两条以上时 None 与 int 比较直接抛 TypeError，retain 与 consolidation 全部失败。不是预期形状或无需补齐时原样返回。
func fillEmbeddingIndex(body []byte) []byte {
	var resp map[string]json.RawMessage
	if json.Unmarshal(body, &resp) != nil {
		return body
	}
	var data []map[string]json.RawMessage
	if json.Unmarshal(resp["data"], &data) != nil {
		return body
	}
	filled := false
	for i, item := range data {
		if v, ok := item["index"]; ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			continue
		}
		item["index"] = json.RawMessage(strconv.Itoa(i))
		filled = true
	}
	if !filled {
		return body
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return body
	}
	resp["data"] = raw
	out, err := json.Marshal(resp)
	if err != nil {
		return body
	}
	return out
}

// redact 把 s 里的真实值换成 redacted；secret 为空时原样返回。
func redact(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, redacted)
}

// hopHeader 报告响应头是否不应原样转回：逐跳头，以及已由本进程重新决定的长度与编码。
func hopHeader(k string) bool {
	switch http.CanonicalHeaderKey(k) {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding",
		"Upgrade", "Content-Length", "Content-Encoding":
		return true
	}
	return false
}

// captureWriter 在把响应写回 Hindsight 的同时留存至多 limit 字节，用于解析 usage；Unwrap 让流式响应照常 flush。
type captureWriter struct {
	http.ResponseWriter
	status    int
	limit     int
	buf       bytes.Buffer
	truncated bool
}

func (c *captureWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if !c.truncated {
		if c.buf.Len()+len(p) > c.limit {
			c.truncated = true
			c.buf.Reset()
		} else {
			c.buf.Write(p)
		}
	}
	return c.ResponseWriter.Write(p)
}

func (c *captureWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// ok 报告上游返回了 2xx（只有成功的调用计入用量）。
func (c *captureWriter) ok() bool { return c.status >= 200 && c.status < 300 }

// usageBody 覆盖各协议响应里的用量字段（附录 A 第 3 项）。
type usageBody struct {
	Usage *struct {
		// openai chat completions、嵌入
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		// openai responses、anthropic
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
		// anthropic 的缓存命中与写入不计在 input_tokens 里
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
	// gemini
	UsageMetadata *struct {
		PromptTokenCount     int64 `json:"promptTokenCount"`
		CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int64 `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	// cohere 重排
	Meta *struct {
		BilledUnits *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"billed_units"`
	} `json:"meta"`
}

// parseUsage 从响应体解析输入与输出 token。口径与 openai 一致：输入含缓存命中的部分，输出含推理 token
// （anthropic 另计的缓存 token 加进输入，gemini 另计的 thoughts 加进输出）。解析不出时为 0。
func parseUsage(b []byte) (in, out int64) {
	var u usageBody
	if json.Unmarshal(b, &u) != nil {
		return 0, 0
	}
	switch {
	case u.Usage != nil:
		in = u.Usage.PromptTokens + u.Usage.InputTokens + u.Usage.CacheCreationInputTokens + u.Usage.CacheReadInputTokens
		out = u.Usage.CompletionTokens + u.Usage.OutputTokens
	case u.UsageMetadata != nil:
		in = u.UsageMetadata.PromptTokenCount
		out = u.UsageMetadata.CandidatesTokenCount + u.UsageMetadata.ThoughtsTokenCount
	case u.Meta != nil && u.Meta.BilledUnits != nil:
		in, out = u.Meta.BilledUnits.InputTokens, u.Meta.BilledUnits.OutputTokens
	}
	return in, out
}

// record 把一次成功的中转调用按小时计进 memory_usage（R18），并每小时清理一次过期行。计数失败只记日志。
func (h *relay) record(ctx context.Context, purpose string, in, out int64) {
	hour := h.s.cfg.Now().Unix() / 3600 * 3600
	if err := h.s.cfg.Q.AddMemoryUsage(ctx, db.AddMemoryUsageParams{
		Hour: hour, Purpose: purpose, InputTokens: in, OutputTokens: out,
	}); err != nil {
		slog.Warn("记忆中转：写入用量失败", "purpose", purpose, "err", err)
	}
	if last := h.pruned.Load(); hour > last && h.pruned.CompareAndSwap(last, hour) {
		if err := h.s.cfg.Q.PruneMemoryUsage(ctx, hour-int64(usageRetention/time.Second)); err != nil {
			slog.Warn("记忆中转：清理过期用量失败", "err", err)
		}
	}
}
