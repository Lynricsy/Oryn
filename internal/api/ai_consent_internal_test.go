package api

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Lynricsy/Oryn/internal/aiconsent"
	"github.com/Lynricsy/Oryn/internal/apigen"
)

func TestDeriveAIRecipients(t *testing.T) {
	chat := []apigen.AiDataPurpose{apigen.AiDataPurposeChat}
	mem := []apigen.AiDataPurpose{apigen.AiDataPurposeMemory}
	host := func(h string) *string { return &h }

	t.Run("空配置没有接收方", func(t *testing.T) {
		assert.Empty(t, deriveAIRecipients(aiRecipientSources{}))
		assert.Empty(t, deriveAIRecipients(aiRecipientSources{ModelsYML: []byte("not: [valid")}))
	})

	t.Run("models.yml 与模型目录", func(t *testing.T) {
		yml := []byte(`providers:
  anthropic:
    apiKey: "${oryn:secret:omp/models/anthropic/apiKey}"
  openai:
    models: [{ id: x }]
  custom:
    baseUrl: https://llm.example.com/v1
  ollama:
    apiKey: none
  lmstudio:
    baseUrl: http://192.168.1.20:1234/v1
  box:
    baseUrl: http://llm-box:8080/v1
  tail:
    baseUrl: http://100.101.1.2/v1
  home:
    apiKey: k
    baseUrl: http://gpu.local/v1
  cloudollama:
    baseUrl: https://ollama.com
  broken: 3
`)
		got := deriveAIRecipients(aiRecipientSources{ModelsYML: yml, CatalogProviders: []string{"anthropic", "deepseek", "ollama", "custom"}})
		assert.Equal(t, []apigen.AiDataRecipient{
			{ID: "model:anthropic@api.anthropic.com", Name: "Anthropic", Host: host("api.anthropic.com"), Purposes: chat},
			{ID: "model:cloudollama@ollama.com", Name: "cloudollama", Host: host("ollama.com"), Purposes: chat},
			{ID: "model:custom@llm.example.com", Name: "custom", Host: host("llm.example.com"), Purposes: chat},
			{ID: "model:deepseek@api.deepseek.com", Name: "DeepSeek", Host: host("api.deepseek.com"), Purposes: chat},
		}, got, "openai 没有凭据不算；ollama 与内网 / 单标签 / CGNAT / .local 上游都不是第三方")
	})

	t.Run("模型条目自带的 baseUrl 是独立接收方", func(t *testing.T) {
		// 顶层指向 A、某个模型单独指向 B：两条上游都要列出，否则同意框只写 A、实际也发给 B
		yml := []byte(`providers:
  gw:
    apiKey: k
    baseUrl: https://a.example.com/v1
    models:
      - { id: m1 }
      - { id: m2, baseUrl: https://b.example.net/v1 }
      - { id: m3, baseUrl: http://10.0.0.5:8000/v1 }
      - 7
  home:
    baseUrl: http://192.168.1.20:1234/v1
    models:
      - { id: big, baseUrl: https://cloud.example.org/v1 }
`)
		got := deriveAIRecipients(aiRecipientSources{ModelsYML: yml, MemoryProviders: []string{"gw"}})
		both := []apigen.AiDataPurpose{apigen.AiDataPurposeChat, apigen.AiDataPurposeMemory}
		assert.Equal(t, []apigen.AiDataRecipient{
			{ID: "model:gw@a.example.com", Name: "gw", Host: host("a.example.com"), Purposes: both},
			{ID: "model:gw@b.example.net", Name: "gw", Host: host("b.example.net"), Purposes: both},
			{ID: "model:home@cloud.example.org", Name: "home", Host: host("cloud.example.org"), Purposes: chat},
		}, got, "内网的模型上游不列；顶层在内网、模型在公网时只列公网那条")
	})

	t.Run("记忆与联网", func(t *testing.T) {
		got := deriveAIRecipients(aiRecipientSources{
			ModelsYML:       []byte("providers:\n  openai:\n    apiKey: k\n"),
			MemoryProviders: []string{"openai", "google"},
			MemoryEmbedURL:  "https://api.openai.com/v1",
			MemoryRerankURL: "http://127.0.0.1:8000",
			DecideURL:       "https://generativelanguage.googleapis.com",
			Web:             true,
		})
		assert.Equal(t, []apigen.AiDataRecipient{
			{ID: "model:google@generativelanguage.googleapis.com", Name: "Google", Host: host("generativelanguage.googleapis.com"), Purposes: mem},
			{ID: "model:openai@api.openai.com", Name: "OpenAI", Host: host("api.openai.com"), Purposes: []apigen.AiDataPurpose{apigen.AiDataPurposeChat, apigen.AiDataPurposeMemory}},
			{ID: "memory-embed:api.openai.com", Name: "OpenAI", Host: host("api.openai.com"), Purposes: mem},
			{ID: "memory-decide:generativelanguage.googleapis.com", Name: "Google", Host: host("generativelanguage.googleapis.com"), Purposes: mem},
			{ID: aiconsent.WebRecipientID, Name: "Web", Purposes: []apigen.AiDataPurpose{apigen.AiDataPurposeWeb}},
		}, got, "本机的重排接口不列出")
	})

	t.Run("本机的 gemini-rerank 适配器按 Google 列出", func(t *testing.T) {
		got := deriveAIRecipients(aiRecipientSources{MemoryRerankURL: "http://127.0.0.1:27183", MemoryRerankModel: "gemini-decision-1"})
		assert.Equal(t, []apigen.AiDataRecipient{
			{ID: "memory-rerank:generativelanguage.googleapis.com", Name: "Google", Host: host("generativelanguage.googleapis.com"), Purposes: mem},
		}, got)
	})
}
