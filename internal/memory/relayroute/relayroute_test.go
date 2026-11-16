package relayroute

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
)

func TestRelayRouteJoinNative(t *testing.T) {
	assert.Equal(t, "/llm/oai/v1/chat/completions", joinNative("/llm/oai/v1", "/chat/completions"))
	assert.Equal(t, "/llm/claude/v1/messages", joinNative("/llm/claude", "/v1/messages"))
	assert.Equal(t, "/llm/claude/v1/messages", joinNative("/llm/claude/v1/", "/v1/messages"))
	assert.Equal(t, "/llm/g/v1beta/models/m:generateContent", joinNative("/llm/g/v1beta", "/v1beta/models/m:generateContent"))
	assert.Equal(t, "/llm/g/v1beta/models/m:generateContent", joinNative("/llm/g", "/v1beta/models/m:generateContent"))
}

func TestRelayRouteFamily(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	vault := secrets.NewVault(st, box)
	home, err := agentcfg.NewHome(t.TempDir())
	require.NoError(t, err)
	models := "providers:\n" +
		"  anthropic:\n    apiKey: sk-ant-literal-1\n" +
		"  google:\n    apiKey: AIza-literal-2\n" +
		"  custom:\n    baseUrl: https://llm.test/v1\n    apiKey: sk-custom-3\n    models:\n" +
		"      - id: special\n        api: anthropic-messages\n" +
		"  codex:\n    baseUrl: https://codex.test\n    api: openai-codex-responses\n    apiKey: sk-codex-4\n"
	_, err = home.Put(ctx, agentcfg.ModelsFile, []byte(models), 0, vault)
	require.NoError(t, err)
	r := New(home, vault)

	for _, tc := range []struct {
		provider, model string
		want            apigen.MemoryFamily
	}{
		{"anthropic", "claude", apigen.MemoryFamilyAnthropic}, // 内置 provider 没写 api
		{"google", "gemini", apigen.MemoryFamilyGemini},
		{"custom", "gpt", apigen.MemoryFamilyOpenai},        // 其它 provider 默认 OpenAI 兼容
		{"custom", "special", apigen.MemoryFamilyAnthropic}, // 模型条目的 api 优先
	} {
		got, err := r.Family(ctx, tc.provider, tc.model)
		require.NoError(t, err, tc.provider+"/"+tc.model)
		assert.Equal(t, tc.want, got, tc.provider+"/"+tc.model)
	}
	_, err = r.Family(ctx, "codex", "x")
	assert.True(t, errors.Is(err, ErrUnsupported), err)
	_, err = r.Family(ctx, "missing", "x")
	assert.Error(t, err)
}
