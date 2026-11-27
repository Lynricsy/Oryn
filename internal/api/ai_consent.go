package api

// ai_consent.go：主人对「把个人数据发给第三方 AI 服务」的同意（App Store 审核指南 5.1.2(i)）。
//
// 服务端强制执行：接收方 id 的规则在 internal/aiconsent（本文件推导设置页列出的接收方，各出站卡点按同一规则算出 id），
// 每个受管的出站路径在发出第一个字节之前用 aiconsent.Gate 现查同意记录，没有同意（含从未同意、撤回之后、新出现的接收方）
// 一律拒绝并回 `ai_consent_required`：
//   - orb、协调宿主、记忆中转、artifact sample 的模型请求 → egress 的 /llm/ 路由（按路由的 provider 与上游主机）；
//   - 不带 Oryn 密钥、上游在公网的 provider 由 agentcfg 同样改写到 /llm/ 路由；以环境变量降级的密钥在 orb setup 时
//     只对已同意的 provider 注入，同意记录变化后由 aiConsentChanged 从在跑的 orb 里收回（orbs.Service.WithdrawEnvKeys）；
//   - 记忆的嵌入 / 重排接口 → 记忆中转（memory.relay.endpoint）；助手记忆的判断模型 → ownermem.decideScores；
//   - 全局助手与后台研究联网 → egress 的正向代理（web:public）。
// 后台任务（定时、后台研究、记忆抽取）在主人同意之前因此暂停（请求被拒、稍后重试或记失败），同意后的下一个请求即恢复。
// 接收方由当前配置推导（deriveAIRecipients），新的接收方出现时 required 变为真，界面随即重新征求同意。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/aiconsent"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// aiConsentCatalogWait 是推导接收方时等模型目录的上限：目录在 models.yml 变化后要重跑 omp（最长一分钟），
// 同意框每次加载都会请求本接口，不能被它拖住；等不到时只按 models.yml 推导。
const aiConsentCatalogWait = 3 * time.Second

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
		if h := aiconsent.URLHost(base); h != "" {
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
	// MemoryEmbedURL、MemoryRerankURL 是记忆已启用时配置的嵌入 / 重排接口地址，*Model 是对应的模型名。
	MemoryEmbedURL, MemoryRerankURL     string
	MemoryEmbedModel, MemoryRerankModel string
	// DecideURL、DecideModel 是助手记忆运行且判断模型接口已配置时的地址与模型。
	DecideURL, DecideModel string
	// Web 为真表示全局助手存在且联网开着。
	Web bool
}

// deriveAIRecipients 推导会收到主人个人数据做 AI 处理的第三方，按实际出站上游（provider + 主机）逐条列出：
//   - models.yml 里有凭据（apiKey）或自定义上游（baseUrl）的 provider，以及模型目录里出现的 provider → 它的上游
//     （顶层 baseUrl，缺省取内置上游）；provider 下各模型条目自带的 baseUrl 是另一条上游，同样列出——agentcfg 会把
//     它们分别改写成独立的出站路由（`<key>.m<i>`），不列就会出现「同意框只写了 A、实际发给了 B」；
//     记忆用途选中的 provider 在它的每条上游上追加 memory；
//   - 记忆的嵌入 / 重排接口 → memory-embed:<host> / memory-rerank:<host>，助手记忆的判断模型 → memory-decide:<host>
//     （aiconsent.EndpointRecipient：本机的 gemini-rerank 适配器转发到 Google，按 Google 列出）；
//   - 助手联网 → web:public。
//
// id 规则在 aiconsent（ModelRecipient、EndpointRecipient），出站卡点用同一套规则判定。上游换了主机，id 随之变化，
// 界面重新征求同意。上游在本机、内网、链路本地或单标签主机名上的不算第三方；没写 baseUrl 的 ollama / lmstudio 同样排除。
func deriveAIRecipients(src aiRecipientSources) []apigen.AiDataRecipient {
	type modelRecip struct {
		key, host string
		purposes  []apigen.AiDataPurpose
	}
	models := map[string]*modelRecip{}
	yml := modelsYMLProviders(src.ModelsYML)
	addHost := func(key, host string, p apigen.AiDataPurpose) {
		id, third := aiconsent.ModelRecipient(key, host)
		if !third {
			return
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
			host := aiconsent.URLHost(prov.BaseURL)
			if host == "" {
				host = aiconsent.URLHost(agentcfg.BuiltinBaseURLs[key])
			}
			addHost(key, host, p)
		}
		for _, mb := range prov.ModelBaseURLs {
			if host := aiconsent.URLHost(mb); host != "" {
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
	for _, ep := range []struct{ prefix, url, model string }{
		{aiconsent.PrefixMemoryEmbed, src.MemoryEmbedURL, src.MemoryEmbedModel},
		{aiconsent.PrefixMemoryRerank, src.MemoryRerankURL, src.MemoryRerankModel},
		{aiconsent.PrefixMemoryDecide, src.DecideURL, src.DecideModel},
	} {
		id, third := aiconsent.EndpointRecipient(ep.prefix, ep.url, ep.model)
		if !third {
			continue
		}
		host := strings.TrimPrefix(id, ep.prefix)
		name := hostLabels[host]
		if name == "" {
			name = host
		}
		out = append(out, apigen.AiDataRecipient{ID: id, Name: name, Host: &host, Purposes: []apigen.AiDataPurpose{apigen.AiDataPurposeMemory}})
	}
	if src.Web {
		out = append(out, apigen.AiDataRecipient{ID: aiconsent.WebRecipientID, Name: "Web", Purposes: []apigen.AiDataPurpose{apigen.AiDataPurposeWeb}})
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
			src.MemoryEmbedURL, src.MemoryEmbedModel = eps.Embed.BaseURL, eps.Embed.Model
		}
		if eps.Rerank != nil {
			src.MemoryRerankURL, src.MemoryRerankModel = eps.Rerank.BaseURL, eps.Rerank.Model
		}
	}
	if s.OwnerMemory != nil && s.OwnerMemory.Service != nil && s.OwnerMemory.Service.Enabled(ctx) {
		st, err := s.OwnerMemory.Service.Settings(ctx)
		if err != nil {
			return src, err
		}
		if st.Decide != nil && st.Decide.HasKey {
			src.DecideURL, src.DecideModel = st.Decide.BaseURL, st.Decide.Model
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
	s.aiConsentChanged(r.Context())
	s.GetAiDataConsent(w, r)
}

// DeleteAiDataConsent 对应 `DELETE /api/v1/settings/ai-data-consent`：清空同意记录；尚未初始化所有者 → 409。
func (s *Server) DeleteAiDataConsent(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.Q.ClearOwnerAiConsent(r.Context())
	if !savedOwner(w, r, n, err) {
		return
	}
	s.aiConsentChanged(r.Context())
	s.GetAiDataConsent(w, r)
}

// aiConsentChanged 在同意记录变化之后收回 orb 环境里已不被同意的环境变量降级密钥（orbs.Service.WithdrawEnvKeys：
// 带着这类密钥的工作 orb 重走 resume、停掉其上的 agent）。没有被扣下的 provider 时不做任何事。
func (s *Server) aiConsentChanged(ctx context.Context) {
	if s.Orbs != nil && s.Orbs.Service != nil {
		s.Orbs.Service.WithdrawEnvKeys(context.WithoutCancel(ctx), "consent")
	}
}
