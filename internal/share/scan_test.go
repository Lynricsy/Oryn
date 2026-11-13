package share

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// textEntry 构造一条 assistant 文本消息条目（payload 为 omp 会话行的 JSON）。
func textEntry(t *testing.T, text string) transcript.Entry {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type": "message", "id": "e1",
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}},
	})
	require.NoError(t, err)
	return transcript.Entry{Seq: 7, File: "s.jsonl", EntryID: "e1", Type: "message", Payload: b}
}

func rulesOf(fs []apigen.ShareFinding) []apigen.ShareFindingRule {
	var out []apigen.ShareFindingRule
	for _, f := range fs {
		out = append(out, f.Rule)
	}
	return out
}

// TestScanRules：每条规则一个正例与一个不该命中的反例。
func TestScanRules(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	cases := []struct {
		rule     apigen.ShareFindingRule
		positive string
		negative string
	}{
		{apigen.ShareFindingRulePrivateKey, "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjE=", "-----BEGIN PUBLIC KEY-----"},
		{apigen.ShareFindingRuleAPIKeySk, "export OPENAI_API_KEY=sk-test-abcdefghijklmnopqrstuvwx0123", "see the risk-assessment-framework-document-v2"},
		{apigen.ShareFindingRuleGithubToken, "token ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", "commit 3f786850e387550fdab836ed7e6dc881de23001b"},
		{apigen.ShareFindingRuleGithubToken, "github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyz", "github_pages_site"},
		{apigen.ShareFindingRuleAwsAccessKey, "aws_access_key_id = AKIAIOSFODNN7EXAMPLE", "AKIA-not-a-key"},
		{apigen.ShareFindingRuleGoogleAPIKey, "key=AIzaSyA1234567890abcdefghijklmnopqrstuv", "signature xyzAIzaSyA1234567890abcdefghijklmnopqrstuv"},
		{apigen.ShareFindingRuleSlackToken, "SLACK=xoxb-123456789012-abcdefABCDEF", "xoxo hugs"},
		{apigen.ShareFindingRuleJwt, "Cookie: session=" + jwt, "eyJhbGciOiJIUzI1NiJ9 alone"},
		{apigen.ShareFindingRuleOrynToken, "ORYN_TOKEN=odt_" + strings.Repeat("Ab3_", 10) + "xyz", "odt_short"},
		{apigen.ShareFindingRuleBearerHeader, `curl -H "Authorization: Bearer abcdefghijklmnopqrstuvwxyz012345"`, "Authorization header uses the bearer scheme"},
		{apigen.ShareFindingRuleURLCredentials, "git clone https://user:hunter2secret@example.com/repo.git", "https://example.com/path?a=b:c@d"},
	}
	for _, c := range cases {
		t.Run(string(c.rule), func(t *testing.T) {
			got := scanEntry(textEntry(t, c.positive), nil)
			assert.Contains(t, rulesOf(got), c.rule, "正例应命中")
			for _, f := range got {
				if f.Rule == c.rule {
					assert.Equal(t, int64(7), f.Seq)
					assert.Equal(t, "e1", f.EntryID)
					require.NotNil(t, f.Role)
					assert.Equal(t, "assistant", *f.Role)
					assert.Equal(t, 1, f.Count)
				}
			}
			assert.NotContains(t, rulesOf(scanEntry(textEntry(t, c.negative), nil)), c.rule, "反例不应命中")
		})
	}
}

// TestScanNoFalsePositives：git 哈希、base64 签名、普通 URL 不命中任何规则。
func TestScanNoFalsePositives(t *testing.T) {
	b, err := json.Marshal(map[string]any{
		"type": "message",
		"message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "hmm", "thinkingSignature": "EqQBCkgIARABGAIiQL3rZ8w0d1qS0f9+Yf2l3q6v7+8u9w0x1y2z3A4B5C6D7E8F9G0H1I2J3K4L5M6N7O8P9Q0R1S2T3U4V5W6X7Y8Z9a0b1c2d3e4f5g6h7i8j9k0l1m2n3o4p5q6r7s8t9u0v1w2x3y4z5==/Ab+cD"},
			map[string]any{"type": "text", "text": "HEAD is 3f786850e387550fdab836ed7e6dc881de23001b; docs at https://pkg.go.dev/net/http and http://localhost:7600/api/v1/threads"},
		}},
	})
	require.NoError(t, err)
	got := scanEntry(transcript.Entry{Seq: 1, File: "s.jsonl", EntryID: "x", Type: "message", Payload: b}, nil)
	assert.Empty(t, got)
}

// TestScanEscapedNewline：转录 JSON 里换行后的密钥（`\nsk-…`）仍命中；同一条目内多次命中合并计数。
func TestScanEscapedNewline(t *testing.T) {
	got := scanEntry(textEntry(t, "first line\nsk-abcdefghijklmnopqrstuvwxyz\nand sk-zyxwvutsrqponmlkjihgfedcba"), nil)
	require.Len(t, got, 1)
	assert.Equal(t, apigen.ShareFindingRuleAPIKeySk, got[0].Rule)
	assert.Equal(t, 2, got[0].Count)
	assert.Equal(t, "sk-a…yz", got[0].Preview)
}

// TestScanVaultSecret：密钥库的值（原文与 JSON 转义形态）命中并带名称；短值不参与。
func TestScanVaultSecret(t *testing.T) {
	needles := []vaultNeedle{{name: "db/password", raw: []byte(`pa"ss\word-123`), escaped: jsonEscape(`pa"ss\word-123`), preview: redact(`pa"ss\word-123`)}}
	got := scanEntry(textEntry(t, `the password is pa"ss\word-123 ok`), needles)
	require.Len(t, got, 1)
	assert.Equal(t, apigen.ShareFindingRuleVaultSecret, got[0].Rule)
	require.NotNil(t, got[0].SecretName)
	assert.Equal(t, "db/password", *got[0].SecretName)
	assert.Equal(t, `pa"s…23`, got[0].Preview)
	assert.NotContains(t, got[0].Preview, "word")
}

func TestRedact(t *testing.T) {
	assert.Equal(t, "sk-t…23", redact("sk-test-abcdefghijklmnopqrstuvwx0123"))
	assert.Equal(t, "ab…", redact("abcdefghijk"))
	assert.Equal(t, "密钥是一…示例", redact("密钥是一段很长的中文示例"))
	assert.Equal(t, "a…", redact("a"))
}

func TestEncodeURIComponent(t *testing.T) {
	assert.Equal(t, "a%20b%2Fc.txt", encodeURIComponent("a b/c.txt"))
	assert.Equal(t, "%E6%8A%A5%E5%91%8A(1).pdf", encodeURIComponent("报告(1).pdf"))
	assert.Equal(t, "it's!~*", encodeURIComponent("it's!~*"))
}
