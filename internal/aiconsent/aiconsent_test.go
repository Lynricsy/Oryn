package aiconsent

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

func TestIsLocalHost(t *testing.T) {
	for h, want := range map[string]bool{
		"": false, "localhost": true, "LOCALHOST.": true, "a.localhost": true, "nas.local": true, "host.docker.internal": true,
		"ollama": true, "127.0.0.1": true, "::1": true, "10.0.0.2": true, "172.16.3.4": true, "192.168.0.1": true,
		"169.254.1.1": true, "fe80::1": true, "fd00::1": true, "100.64.0.1": true, "0.0.0.0": true,
		"8.8.8.8": false, "2001:4860::1": false, "api.openai.com": false,
	} {
		assert.Equal(t, want, IsLocalHost(h), h)
	}
}

func TestRecipientIDs(t *testing.T) {
	for _, tc := range []struct {
		provider, host, id string
		third              bool
	}{
		{"anthropic", "api.anthropic.com", "model:anthropic@api.anthropic.com", true},
		{"gw", "B.Example.NET", "model:gw@b.example.net", true},
		{"custom", "", "model:custom", true}, // 环境变量降级：主机未知
		{"ollama", "", "", false},
		{"lmstudio", "", "", false},
		{"ollama", "ollama.com", "model:ollama@ollama.com", true},
		{"home", "192.168.1.20", "", false},
		{"box", "llm-box", "", false},
	} {
		id, third := ModelRecipient(tc.provider, tc.host)
		assert.Equal(t, tc.third, third, tc.provider+"@"+tc.host)
		assert.Equal(t, tc.id, id, tc.provider+"@"+tc.host)
	}

	for _, tc := range []struct {
		prefix, base, model, id string
		third                   bool
	}{
		{PrefixMemoryEmbed, "https://api.openai.com/v1", "text-embedding-3-small", "memory-embed:api.openai.com", true},
		{PrefixMemoryRerank, "http://127.0.0.1:8000", "bge-reranker", "", false},
		// 本机的 gemini-rerank 适配器把请求转给 Google
		{PrefixMemoryRerank, "http://127.0.0.1:27183", "gemini-decision-1", "memory-rerank:generativelanguage.googleapis.com", true},
		{PrefixMemoryDecide, "https://generativelanguage.googleapis.com", "models/gemini-decision-1", "memory-decide:generativelanguage.googleapis.com", true},
		{PrefixMemoryEmbed, "not a url", "x", "", false},
	} {
		id, third := EndpointRecipient(tc.prefix, tc.base, tc.model)
		assert.Equal(t, tc.third, third, tc.base)
		assert.Equal(t, tc.id, id, tc.base)
	}
}

type fakeStore struct {
	row db.GetOwnerAiConsentRow
	err error
}

func (f *fakeStore) GetOwnerAiConsent(context.Context) (db.GetOwnerAiConsentRow, error) {
	return f.row, f.err
}

func TestGate(t *testing.T) {
	ctx := context.Background()
	at := int64(1)
	st := &fakeStore{err: sql.ErrNoRows}
	g := NewGate(st)
	const a, b = "model:gw@a.example.com", "model:gw@b.example.net"

	err := g.Check(ctx, a)
	require.Error(t, err, "还没有所有者：拒绝")
	assert.True(t, IsDenied(err))
	assert.Contains(t, err.Error(), Code)
	assert.Contains(t, err.Error(), "设置 → 隐私与数据")

	st.err, st.row = nil, db.GetOwnerAiConsentRow{AiConsentRecipients: `["` + a + `"]`}
	assert.Error(t, g.Check(ctx, a), "没有同意时间（撤回之后）：拒绝")

	st.row.AiConsentAt = &at
	assert.NoError(t, g.Check(ctx, a), "同意之后立即放行（每次现读）")
	assert.Error(t, g.Check(ctx, b), "新的接收方没有同意")

	st.row = db.GetOwnerAiConsentRow{AiConsentAt: &at, AiConsentRecipients: "[]"}
	assert.Error(t, g.Check(ctx, a), "撤回之后立即拒绝")

	st.row.AiConsentRecipients = "{bad"
	assert.Error(t, g.Check(ctx, a), "记录坏了：拒绝")
	st.err = errors.New("db down")
	assert.True(t, IsDenied(g.Check(ctx, a)), "读库失败：拒绝")

	assert.True(t, IsDenied(Check(ctx, nil, a)), "没有接上同意来源：拒绝")
	assert.NoError(t, Check(ctx, CheckerFunc(func(context.Context, string) error { return nil }), a))
}
