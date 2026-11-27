package api

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
			{ID: "model:anthropic", Name: "Anthropic", Host: host("api.anthropic.com"), Purposes: chat},
			{ID: "model:cloudollama", Name: "cloudollama", Host: host("ollama.com"), Purposes: chat},
			{ID: "model:custom", Name: "custom", Host: host("llm.example.com"), Purposes: chat},
			{ID: "model:deepseek", Name: "DeepSeek", Host: host("api.deepseek.com"), Purposes: chat},
		}, got, "openai 没有凭据不算；ollama 与内网 / 单标签 / CGNAT / .local 上游都不是第三方")
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
			{ID: "model:google", Name: "Google", Host: host("generativelanguage.googleapis.com"), Purposes: mem},
			{ID: "model:openai", Name: "OpenAI", Host: host("api.openai.com"), Purposes: []apigen.AiDataPurpose{apigen.AiDataPurposeChat, apigen.AiDataPurposeMemory}},
			{ID: "memory-embed:api.openai.com", Name: "OpenAI", Host: host("api.openai.com"), Purposes: mem},
			{ID: "memory-decide:generativelanguage.googleapis.com", Name: "Google", Host: host("generativelanguage.googleapis.com"), Purposes: mem},
			{ID: webRecipientID, Name: "Web", Purposes: []apigen.AiDataPurpose{apigen.AiDataPurposeWeb}},
		}, got, "本机的重排接口不列出")
	})
}

func TestIsLocalHost(t *testing.T) {
	for h, want := range map[string]bool{
		"": false, "localhost": true, "LOCALHOST.": true, "a.localhost": true, "nas.local": true, "host.docker.internal": true,
		"ollama": true, "127.0.0.1": true, "::1": true, "10.0.0.2": true, "172.16.3.4": true, "192.168.0.1": true,
		"169.254.1.1": true, "fe80::1": true, "fd00::1": true, "100.64.0.1": true, "0.0.0.0": true,
		"8.8.8.8": false, "2001:4860::1": false, "api.openai.com": false,
	} {
		assert.Equal(t, want, isLocalHost(h), h)
	}
}
