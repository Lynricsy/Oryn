package share

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRedactText：规则表命中（bounded 规则保留边界字符）与密钥库值都替换成占位符，重叠命中只替换一次，零值 Service 可用。
func TestRedactText(t *testing.T) {
	ctx := context.Background()
	s := &Service{sec: mapSecrets{"db/password": "correct-horse-battery", "short": "abc"}}

	got, n := s.RedactText(ctx, "key=sk-test-abcdefghijklmnopqrstuvwx0123，密码 correct-horse-battery。")
	assert.Equal(t, "key="+RedactPlaceholder+"，密码 "+RedactPlaceholder+"。", got)
	assert.Equal(t, 2, n)

	got, n = s.RedactText(ctx, `curl -H "Authorization: Bearer abcdefghijklmnopqrstuvwxyz012345" https://x`)
	assert.Equal(t, `curl -H "`+RedactPlaceholder+`" https://x`, got)
	assert.Equal(t, 1, n)

	// 密钥库值正好是一个 sk- 密钥：两条规则命中同一段，只替换一次。
	s2 := &Service{sec: mapSecrets{"openai": "sk-test-abcdefghijklmnopqrstuvwx0123"}}
	got, n = s2.RedactText(ctx, "a sk-test-abcdefghijklmnopqrstuvwx0123 b sk-test-abcdefghijklmnopqrstuvwx0123")
	assert.Equal(t, "a "+RedactPlaceholder+" b "+RedactPlaceholder, got)
	assert.Equal(t, 2, n)

	// 短于下限的密钥库值不参与；普通文本原样返回。
	got, n = s.RedactText(ctx, "abc 与 risk-assessment-framework-document-v2")
	assert.Equal(t, "abc 与 risk-assessment-framework-document-v2", got)
	assert.Zero(t, n)

	got, n = (&Service{}).RedactText(ctx, "token ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 done")
	assert.Equal(t, "token "+RedactPlaceholder+" done", got)
	assert.Equal(t, 1, n)

	got, n = (&Service{}).RedactText(ctx, "")
	assert.Empty(t, got)
	assert.Zero(t, n)
}
