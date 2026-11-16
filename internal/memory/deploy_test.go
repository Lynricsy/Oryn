package memory_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
)

// 「我已重启」一次把各用途的协议类型与重排模式都记为已部署，所以 LLM 协议与重排模式同时待部署时，
// 模型卡与接口卡给出的片段都必须覆盖两部分：只按其中一张卡改 `.env` 就确认，会误清另一部分的提醒。
func TestMemoryDeploySnippetCoversAllPending(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, func(c *memory.Config) {
		c.Family = func(_ context.Context, pk, _ string) (apigen.MemoryFamily, error) {
			if pk == "claude" {
				return apigen.MemoryFamilyAnthropic, nil
			}
			return apigen.MemoryFamilyOpenai, nil
		}
	})
	e.configure(t, "http://127.0.0.1:1") // .env 已生成：各用途 openai、重排 rrf
	catalog := []memory.CatalogModel{
		{ProviderKey: "openai", ModelID: "gpt-x", Name: "GPT X"},
		{ProviderKey: "claude", ModelID: "haiku", Name: "Haiku"},
	}
	pick := func(p apigen.MemoryPurpose, pk, id string) struct {
		ModelID     *string              `json:"modelId,omitempty"`
		ProviderKey *string              `json:"providerKey,omitempty"`
		Purpose     apigen.MemoryPurpose `json:"purpose"`
	} {
		return struct {
			ModelID     *string              `json:"modelId,omitempty"`
			ProviderKey *string              `json:"providerKey,omitempty"`
			Purpose     apigen.MemoryPurpose `json:"purpose"`
		}{ModelID: &id, ProviderKey: &pk, Purpose: p}
	}
	const reflectLine = "HINDSIGHT_API_REFLECT_LLM_PROVIDER=anthropic\n"
	const rerankLine = "HINDSIGHT_API_RERANKER_PROVIDER=cohere\n"

	// 只有重排待部署：接口卡只给重排行，模型卡没有提醒
	_, err := e.svc.PutEndpoint(ctx, apigen.MemoryEndpointRerank, apigen.MemoryEndpointUpdate{
		BaseURL: "https://rerank.example.com", Model: "bge-reranker", APIKey: new("sk-rerank"),
	})
	require.NoError(t, err)
	eps, err := e.svc.Endpoints(ctx)
	require.NoError(t, err)
	require.True(t, eps.NeedsRestart)
	assert.Contains(t, *eps.EnvSnippet, rerankLine)
	assert.NotContains(t, *eps.EnvSnippet, "_LLM_")
	m, err := e.svc.Models(ctx, catalog)
	require.NoError(t, err)
	assert.Nil(t, m.EnvSnippet)

	// 再把 reflect 换成 anthropic：两张卡的片段都同时带 reflect 与重排的行
	up := apigen.MemoryModelsUpdate{}
	up.Items = append(up.Items, pick(apigen.MemoryPurposeDefault, "openai", "gpt-x"), pick(apigen.MemoryPurposeReflect, "claude", "haiku"))
	m, err = e.svc.PutModels(ctx, up, catalog)
	require.NoError(t, err)
	require.NotNil(t, m.EnvSnippet)
	assert.Contains(t, *m.EnvSnippet, reflectLine)
	assert.Contains(t, *m.EnvSnippet, rerankLine, "模型卡的片段要带上待部署的重排")
	eps, err = e.svc.Endpoints(ctx)
	require.NoError(t, err)
	require.NotNil(t, eps.EnvSnippet)
	assert.Equal(t, *m.EnvSnippet, *eps.EnvSnippet, "两张卡给出同一份完整片段")
	assert.NotContains(t, *eps.EnvSnippet, "sk-rerank")

	// 确认后两边都清除
	m, err = e.svc.ConfirmDeployed(ctx, catalog)
	require.NoError(t, err)
	assert.Nil(t, m.EnvSnippet)
	eps, err = e.svc.Endpoints(ctx)
	require.NoError(t, err)
	assert.False(t, eps.NeedsRestart)
	assert.Nil(t, eps.EnvSnippet)

	// 只有 LLM 协议待部署：接口卡不提醒，模型卡只给 LLM 行
	up.Items = up.Items[:1]
	m, err = e.svc.PutModels(ctx, up, catalog)
	require.NoError(t, err)
	require.NotNil(t, m.EnvSnippet)
	assert.Contains(t, *m.EnvSnippet, "HINDSIGHT_API_REFLECT_LLM_PROVIDER=openai\n")
	assert.NotContains(t, *m.EnvSnippet, "RERANKER")
	eps, err = e.svc.Endpoints(ctx)
	require.NoError(t, err)
	assert.False(t, eps.NeedsRestart)
}
