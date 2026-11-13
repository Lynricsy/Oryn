package share

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"sort"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// MaxFindings 是一次扫描返回的命中上限（超出置 truncated）。
const MaxFindings = 200

// minVaultSecret 是参与 vault_secret 规则的密钥值最短长度（过短的值误报太多）。
const minVaultSecret = 8

// scanRule 是一条正则规则。规则作用于 transcript.Public 之后的 payload 原始字节（JSON 文本），不解析 JSON。
// group > 0 时命中文本取该捕获组（其余部分是边界）。
type scanRule struct {
	name  apigen.ShareFindingRule
	re    *regexp.Regexp
	group int
}

// bounded 要求以固定前缀开头的规则在前缀前是行首、非字母数字字符或 JSON 转义的换行 / 制表（`\n` `\r` `\t`），
// 避免在普通单词（如 `risk-assessment-…` 里的 `sk-`）与 base64 长串中间误中。
func bounded(name apigen.ShareFindingRule, expr string) scanRule {
	return scanRule{name: name, re: regexp.MustCompile(`(?:^|[^A-Za-z0-9]|\\[nrt])(` + expr + `)`), group: 1}
}

// scanRules 是规则表（docs/protocol.md M5 节）。bearer_header 的分隔符额外允许 `\`：转录里 JSON 字符串内的引号是转义过的（`\"`）。
var scanRules = []scanRule{
	{name: apigen.ShareFindingRulePrivateKey, re: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	bounded(apigen.ShareFindingRuleAPIKeySk, `sk-[A-Za-z0-9_-]{20,}`),
	bounded(apigen.ShareFindingRuleGithubToken, `gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{22,}`),
	bounded(apigen.ShareFindingRuleAwsAccessKey, `AKIA[0-9A-Z]{16}`),
	bounded(apigen.ShareFindingRuleGoogleAPIKey, `AIza[0-9A-Za-z_-]{35}`),
	bounded(apigen.ShareFindingRuleSlackToken, `xox[baprs]-[A-Za-z0-9-]{10,}`),
	bounded(apigen.ShareFindingRuleJwt, `eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
	bounded(apigen.ShareFindingRuleOrynToken, `(?:opv|oot|odt|ost|ows|owt)_[A-Za-z0-9_-]{43}`),
	{name: apigen.ShareFindingRuleBearerHeader, re: regexp.MustCompile(`(?i)authorization["'\\:\s]+bearer\s+[A-Za-z0-9._~+/-]{20,}`)},
	{name: apigen.ShareFindingRuleURLCredentials, re: regexp.MustCompile(`[a-z][a-z0-9+.-]{1,15}://[^/\s:@"]+:[^/\s:@"]{6,}@`)},
}

// vaultNeedle 是一个密钥值的两种字节形态：原文与 JSON 字符串转义后的形态（与原文相同时为 nil）。
type vaultNeedle struct {
	name         string
	raw, escaped []byte
	preview      string
}

func (s *Service) vaultNeedles(ctx context.Context) []vaultNeedle {
	if s.sec == nil {
		return nil
	}
	all, err := s.sec.All(ctx)
	if err != nil {
		slog.Warn("share: 读取密钥库失败，跳过 vault_secret 规则", "err", err)
		return nil
	}
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []vaultNeedle
	for _, n := range names {
		v := all[n]
		if utf8.RuneCountInString(v) < minVaultSecret {
			continue
		}
		nd := vaultNeedle{name: n, raw: []byte(v), preview: redact(v)}
		if esc := jsonEscape(v); !bytes.Equal(esc, nd.raw) {
			nd.escaped = esc
		}
		out = append(out, nd)
	}
	return out
}

// jsonEscape 返回 v 作为 JSON 字符串内容的字节（不含两端引号；不转义 HTML 字符，同 JS 的 JSON.stringify）。
func jsonEscape(v string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return b[1 : len(b)-1]
}

// redact 返回脱敏预览：前 4 字符 + … + 后 2 字符；短于 12 字符时只留前 2 字符 + …。
func redact(s string) string {
	r := []rune(s)
	if len(r) < 12 {
		return string(r[:min(2, len(r))]) + "…"
	}
	return string(r[:4]) + "…" + string(r[len(r)-2:])
}

// 标题与附件名命中项的 entryId（seq 为 0：不对应任何条目）。
const (
	titleFindingID          = "__title__"
	attachmentFindingPrefix = "attachment:"
)

// scan 扫描公开面上的全部文本：标题、范围内每条公开条目的 payload、将要列出的附件名（F4）。
// 每个 (规则, 条目) 合并成一项；标题与附件名的命中以 entryId `__title__` / `attachment:<id>`、seq 0 报告。
// 范围内的条目总是读完（即使命中已截断）：创建快照时 accept 要据此固化每一行的摘要。
func (s *Service) scan(ctx context.Context, sc *scope, atts []db.Attachment) (apigen.ShareScan, error) {
	res := apigen.ShareScan{Findings: []apigen.ShareFinding{}}
	needles := s.vaultNeedles(ctx)
	push := func(fs []apigen.ShareFinding) {
		for _, f := range fs {
			if len(res.Findings) >= MaxFindings {
				res.Truncated = true
				return
			}
			res.Findings = append(res.Findings, f)
		}
	}
	push(scanText(titleFindingID, sc.title, needles))
	after := int64(0)
	for {
		es, more, err := sc.page(ctx, s.st.Q, after, sc.until, rangeBatch)
		if err != nil {
			return apigen.ShareScan{}, err
		}
		for _, e := range es {
			res.Scanned++
			push(scanEntry(e, needles))
		}
		if !more || len(es) == 0 {
			break
		}
		after = es[len(es)-1].Seq
	}
	for _, a := range atts {
		push(scanText(attachmentFindingPrefix+a.ID, a.Name, needles))
	}
	return res, nil
}

// scanEntry 返回一条条目的全部命中（先 vault_secret，再按规则表顺序）。
func scanEntry(e transcript.Entry, needles []vaultNeedle) []apigen.ShareFinding {
	var role, tool *string
	located := false
	return scanBytes([]byte(e.Payload), needles, func(f apigen.ShareFinding) apigen.ShareFinding {
		if !located {
			located = true
			if m, ok := transcript.ParseMessage(e); ok {
				if m.Role != "" {
					role = &m.Role
				}
				if m.Role == "toolResult" && m.ToolName != "" {
					tool = &m.ToolName
				}
			}
		}
		f.Seq, f.EntryID, f.Role, f.Tool = e.Seq, e.EntryID, role, tool
		return f
	})
}

// scanText 扫描一段纯文本（标题、附件名），命中项的 entryId 为 id、seq 为 0。
func scanText(id, text string, needles []vaultNeedle) []apigen.ShareFinding {
	if text == "" {
		return nil
	}
	return scanBytes([]byte(text), needles, func(f apigen.ShareFinding) apigen.ShareFinding {
		f.EntryID = id
		return f
	})
}

// scanBytes 对 p 跑 vault_secret 与规则表，每条命中的规则经 locate 补上位置信息后返回。
func scanBytes(p []byte, needles []vaultNeedle, locate func(apigen.ShareFinding) apigen.ShareFinding) []apigen.ShareFinding {
	var out []apigen.ShareFinding
	for _, nd := range needles {
		n := bytes.Count(p, nd.raw)
		if nd.escaped != nil {
			n += bytes.Count(p, nd.escaped)
		}
		if n > 0 {
			name := nd.name
			out = append(out, locate(apigen.ShareFinding{Rule: apigen.ShareFindingRuleVaultSecret, Preview: nd.preview, Count: n, SecretName: &name}))
		}
	}
	for _, r := range scanRules {
		ms := r.re.FindAllSubmatchIndex(p, -1)
		if len(ms) > 0 {
			m := ms[0]
			out = append(out, locate(apigen.ShareFinding{Rule: r.name, Preview: redact(string(p[m[2*r.group]:m[2*r.group+1]])), Count: len(ms)}))
		}
	}
	return out
}
