package api

// ai_consent.go：主人对「把个人数据发给第三方 AI 服务」的同意（App Store 审核指南 5.1.2(i)）。
//
// 执行层面的选择：服务端不按同意状态拦截模型调用。同意框是界面级的门槛（AppShell 在 required 时整页拦住），
// 已有的定时任务、后台研究、记忆抽取照常运行——模型请求分散在 orb 内的 omp、协调宿主、记忆中转、助手记忆判断等
// 多条出站路径上，没有一个干净的统一卡点；在其中任一处硬拦都会让主人看不懂的后台任务半途失败。
// 接收方由当前配置推导（deriveAIRecipients），新的接收方出现时 required 变为真，界面随即重新征求同意。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// aiConsentCatalogWait 是推导接收方时等模型目录的上限：目录在 models.yml 变化后要重跑 omp（最长一分钟），
// 同意框每次加载都会请求本接口，不能被它拖住；等不到时只按 models.yml 推导。
const aiConsentCatalogWait = 3 * time.Second

// webRecipientID 是全局助手联网的接收方：搜索词与打开的网址发往公网网站（grounded 搜索本身走已列出的模型 provider）。
const webRecipientID = "web:public"

// localProviders 是没写 baseUrl 时默认连本机的 provider，不是第三方。
var localProviders = map[string]bool{"ollama": true, "lmstudio": true}

// providerLabels 是已知 provider 的显示名（与 web/src/lib/model-picker.ts 的 PROVIDER_LABELS 一致）。
var providerLabels = map[string]string{
	"anthropic":         "Anthropic",
	"openai":            "OpenAI",
	"openai-codex":      "OpenAI Codex",
	"azure-openai":      "Azure OpenAI",
	"azure":             "Azure",
	"google":            "Google",
	"google-gemini-cli": "Gemini CLI",
	"google-vertex":     "Vertex AI",
	"vertex":            "Vertex AI",
	"bedrock":           "AWS Bedrock",
	"amazon-bedrock":    "AWS Bedrock",
	"deepseek":          "DeepSeek",
	"xai":               "xAI",
	"mistral":           "Mistral",
	"moonshot":          "Moonshot",
	"zhipu":             "Zhipu AI",
	"zai":               "Z.ai",
	"minimax":           "MiniMax",
	"openrouter":        "OpenRouter",
	"groq":              "Groq",
	"cerebras":          "Cerebras",
	"together":          "Together",
	"fireworks":         "Fireworks",
	"github-copilot":    "GitHub Copilot",
	"vercel-ai-gateway": "Vercel AI Gateway",
}

// hostLabels 是已知上游主机的显示名（记忆接口等只有地址、没有 provider 键的接收方用）。
var hostLabels = func() map[string]string {
	m := map[string]string{"api.cohere.com": "Cohere", "api.cohere.ai": "Cohere", "api.voyageai.com": "Voyage AI", "api.jina.ai": "Jina AI"}
	for key, base := range agentcfg.BuiltinBaseURLs {
		if h := urlHost(base); h != "" {
			m[h] = providerLabels[key]
		}
	}
	return m
}()

// aiRecipientSources 是推导接收方的输入：当前配置里会把主人数据送出去的各处。
type aiRecipientSources struct {
	// ModelsYML 是全局 omp-home 的 models.yml（可为空）。
	ModelsYML []byte
	// CatalogProviders 是中央模型目录里出现的 provider 键（目录不可用时为空）。
	CatalogProviders []string
	// MemoryProviders 是记忆（M16，已启用时）各用途生效的 provider 键。
	MemoryProviders []string
	// MemoryEmbedURL、MemoryRerankURL 是记忆已启用时配置的嵌入 / 重排接口地址。
	MemoryEmbedURL, MemoryRerankURL string
	// DecideURL 是助手记忆运行且判断模型接口已配置时的地址。
	DecideURL string
	// Web 为真表示全局助手存在且联网开着。
	Web bool
}

// deriveAIRecipients 推导会收到主人个人数据做 AI 处理的第三方，按实际出站上游（provider + 主机）逐条列出：
//   - models.yml 里有凭据（apiKey）或自定义上游（baseUrl）的 provider，以及模型目录里出现的 provider → 它的上游
//     （顶层 baseUrl，缺省取内置上游）；provider 下各模型条目自带的 baseUrl 是另一条上游，同样列出——agentcfg 会把
//     它们分别改写成独立的出站路由（`<key>.m<i>`），不列就会出现「同意框只写了 A、实际发给了 B」；
//     记忆用途选中的 provider 在它的每条上游上追加 memory；
//   - 记忆的嵌入 / 重排接口 → memory-embed:<host> / memory-rerank:<host>，助手记忆的判断模型 → memory-decide:<host>；
//   - 助手联网 → web:public。
//
// 模型接收方的 id 是 `model:<key>@<host>`（主机未知时为 `model:<key>`）：上游换了主机，id 随之变化，界面重新征求同意。
// 上游在本机、内网、链路本地或单标签主机名上的不算第三方；没写 baseUrl 的 ollama / lmstudio 同样排除。
func deriveAIRecipients(src aiRecipientSources) []apigen.AiDataRecipient {
	type modelRecip struct {
		key, host string
		purposes  []apigen.AiDataPurpose
	}
	models := map[string]*modelRecip{}
	yml := modelsYMLProviders(src.ModelsYML)
	addHost := func(key, host string, p apigen.AiDataPurpose) {
		if (host == "" && localProviders[key]) || isLocalHost(host) {
			return
		}
		id := "model:" + key
		if host != "" {
			id += "@" + host
		}
		m := models[id]
		if m == nil {
			m = &modelRecip{key: key, host: host}
			models[id] = m
		}
		if !slices.Contains(m.purposes, p) {
			m.purposes = append(m.purposes, p)
		}
	}
	// addProvider 列出 provider 的上游：withBase 时含顶层（或内置）上游，各模型条目自带的上游总是列出。
	addProvider := func(key string, p apigen.AiDataPurpose, withBase bool) {
		if key == "" {
			return
		}
		prov := yml[key]
		if withBase {
			host := urlHost(prov.BaseURL)
			if host == "" {
				host = urlHost(agentcfg.BuiltinBaseURLs[key])
			}
			addHost(key, host, p)
		}
		for _, mb := range prov.ModelBaseURLs {
			if host := urlHost(mb); host != "" {
				addHost(key, host, p)
			}
		}
	}
	for key, prov := range yml {
		addProvider(key, apigen.AiDataPurposeChat, prov.APIKey != "" || prov.BaseURL != "")
	}
	for _, key := range src.CatalogProviders {
		addProvider(key, apigen.AiDataPurposeChat, true)
	}
	for _, key := range src.MemoryProviders {
		addProvider(key, apigen.AiDataPurposeMemory, true)
	}

	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]apigen.AiDataRecipient, 0, len(ids)+4)
	for _, id := range ids {
		m := models[id]
		name := providerLabels[m.key]
		if name == "" {
			name = m.key
		}
		out = append(out, apigen.AiDataRecipient{ID: id, Name: name, Host: optHost(m.host), Purposes: m.purposes})
	}
	for _, ep := range []struct{ prefix, url string }{
		{"memory-embed:", src.MemoryEmbedURL},
		{"memory-rerank:", src.MemoryRerankURL},
		{"memory-decide:", src.DecideURL},
	} {
		host := urlHost(ep.url)
		if host == "" || isLocalHost(host) {
			continue
		}
		name := hostLabels[host]
		if name == "" {
			name = host
		}
		out = append(out, apigen.AiDataRecipient{ID: ep.prefix + host, Name: name, Host: &host, Purposes: []apigen.AiDataPurpose{apigen.AiDataPurposeMemory}})
	}
	if src.Web {
		out = append(out, apigen.AiDataRecipient{ID: webRecipientID, Name: "Web", Purposes: []apigen.AiDataPurpose{apigen.AiDataPurposeWeb}})
	}
	return out
}

// ymlProvider 是 models.yml 里一个 provider 与同意有关的字段。
type ymlProvider struct {
	BaseURL string `yaml:"baseUrl"`
	APIKey  string `yaml:"apiKey"`
	// ModelBaseURLs 是 models[] 各条目自带的 baseUrl（非空的）。
	ModelBaseURLs []string `yaml:"-"`
}

// modelsYMLProviders 解析 models.yml 的 providers；格式坏了或某个 provider 不是映射时跳过它，
// models 里不是映射或没有 baseUrl 的条目跳过。
func modelsYMLProviders(content []byte) map[string]ymlProvider {
	var doc struct {
		Providers map[string]yaml.Node `yaml:"providers"`
	}
	if len(content) == 0 || yaml.Unmarshal(content, &doc) != nil {
		return nil
	}
	out := make(map[string]ymlProvider, len(doc.Providers))
	for key, n := range doc.Providers {
		var p ymlProvider
		if n.Kind != yaml.MappingNode || n.Decode(&p) != nil {
			continue
		}
		p.APIKey, p.BaseURL = strings.TrimSpace(p.APIKey), strings.TrimSpace(p.BaseURL)
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value != "models" || n.Content[i+1].Kind != yaml.SequenceNode {
				continue
			}
			for _, m := range n.Content[i+1].Content {
				var mb struct {
					BaseURL string `yaml:"baseUrl"`
				}
				if m.Kind != yaml.MappingNode || m.Decode(&mb) != nil {
					continue
				}
				if u := strings.TrimSpace(mb.BaseURL); u != "" {
					p.ModelBaseURLs = append(p.ModelBaseURLs, u)
				}
			}
		}
		out[key] = p
	}
	return out
}

// urlHost 返回地址的小写主机名；解析不了时为空。
func urlHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// isLocalHost 判断主机是否在主人自己的机器或内网：localhost、*.localhost / *.local / *.internal、单标签主机名
// （容器网络里的服务名），以及回环、私有、链路本地、未指定与 CGNAT（100.64.0.0/10，Tailscale 等）地址。空主机返回 false。
func isLocalHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() ||
			netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
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

func optHost(h string) *string {
	if h == "" {
		return nil
	}
	return &h
}

// aiRecipientSources 读取推导接收方所需的当前配置。
func (s *Server) aiRecipientSources(ctx context.Context) (aiRecipientSources, error) {
	var src aiRecipientSources
	if s.Auth != nil && s.Auth.Home != nil {
		b, _, err := s.Auth.Home.Get(agentcfg.ModelsFile)
		if err == nil {
			src.ModelsYML = b
		}
	}
	if s.Auth != nil && s.Auth.ModelCatalog != nil {
		cctx, cancel := context.WithTimeout(ctx, aiConsentCatalogWait)
		res, err := s.Auth.ModelCatalog.Get(cctx, false)
		cancel()
		if err != nil {
			slog.Debug("AI 同意：模型目录暂不可用，只按 models.yml 推导", "err", err)
		}
		for _, m := range res.Models {
			if !slices.Contains(src.CatalogProviders, m.Provider) {
				src.CatalogProviders = append(src.CatalogProviders, m.Provider)
			}
		}
	}
	if s.Memory != nil && s.Memory.Service != nil && s.Memory.Service.Enabled(ctx) {
		svc := s.Memory.Service
		models, err := svc.Models(ctx, nil)
		if err != nil {
			return src, err
		}
		for _, it := range models.Items {
			if it.ProviderKey != nil && !slices.Contains(src.MemoryProviders, *it.ProviderKey) {
				src.MemoryProviders = append(src.MemoryProviders, *it.ProviderKey)
			}
		}
		eps, err := svc.Endpoints(ctx)
		if err != nil {
			return src, err
		}
		if eps.Embed != nil {
			src.MemoryEmbedURL = eps.Embed.BaseURL
		}
		if eps.Rerank != nil {
			src.MemoryRerankURL = eps.Rerank.BaseURL
		}
	}
	if s.OwnerMemory != nil && s.OwnerMemory.Service != nil && s.OwnerMemory.Service.Enabled(ctx) {
		st, err := s.OwnerMemory.Service.Settings(ctx)
		if err != nil {
			return src, err
		}
		if st.Decide != nil && st.Decide.HasKey {
			src.DecideURL = st.Decide.BaseURL
		}
	}
	if s.Coord != nil && s.Coord.Service != nil {
		// 没有设置行时联网缺省开（与 orbs.DefaultSetup.assistantSettings 一致）
		row, err := s.Store.Q.GetAssistantSettings(ctx)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			src.Web = true
		case err != nil:
			return src, err
		default:
			src.Web = row.WebAccess != 0
		}
	}
	return src, nil
}

// aiDataConsent 组装当前状态；尚未初始化所有者时同意记录为空。
func (s *Server) aiDataConsent(ctx context.Context) (apigen.AiDataConsent, error) {
	src, err := s.aiRecipientSources(ctx)
	if err != nil {
		return apigen.AiDataConsent{}, err
	}
	out := apigen.AiDataConsent{Recipients: deriveAIRecipients(src), ConsentedRecipientIds: []string{}}
	row, err := s.Store.Q.GetOwnerAiConsent(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil && row.AiConsentAt != nil {
		out.ConsentedAt = row.AiConsentAt
		if json.Unmarshal([]byte(row.AiConsentRecipients), &out.ConsentedRecipientIds) != nil || out.ConsentedRecipientIds == nil {
			out.ConsentedRecipientIds = []string{}
		}
	}
	for _, r := range out.Recipients {
		if !slices.Contains(out.ConsentedRecipientIds, r.ID) {
			out.Required = true
			break
		}
	}
	return out, nil
}

// GetAiDataConsent 对应 `GET /api/v1/settings/ai-data-consent`。
func (s *Server) GetAiDataConsent(w http.ResponseWriter, r *http.Request) {
	out, err := s.aiDataConsent(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutAiDataConsent 对应 `PUT /api/v1/settings/ai-data-consent`：id 须都在当前接收方里；尚未初始化所有者 → 409。
func (s *Server) PutAiDataConsent(w http.ResponseWriter, r *http.Request) {
	var req apigen.AiDataConsentUpdate
	if !decodeJSON(w, r, &req) {
		return
	}
	src, err := s.aiRecipientSources(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	current := deriveAIRecipients(src)
	ids := make([]string, 0, len(req.RecipientIds))
	for _, id := range req.RecipientIds {
		if !slices.ContainsFunc(current, func(rc apigen.AiDataRecipient) bool { return rc.ID == id }) {
			writeErr(w, r, errInvalid("recipientIds 含当前不存在的接收方："+id))
			return
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	raw, _ := json.Marshal(ids)
	now := time.Now().UnixMilli()
	n, err := s.Store.Q.SetOwnerAiConsent(r.Context(), db.SetOwnerAiConsentParams{ConsentedAt: &now, Recipients: string(raw)})
	if !savedOwner(w, r, n, err) {
		return
	}
	s.GetAiDataConsent(w, r)
}

// DeleteAiDataConsent 对应 `DELETE /api/v1/settings/ai-data-consent`：清空同意记录；尚未初始化所有者 → 409。
func (s *Server) DeleteAiDataConsent(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.Q.ClearOwnerAiConsent(r.Context())
	if !savedOwner(w, r, n, err) {
		return
	}
	s.GetAiDataConsent(w, r)
}
