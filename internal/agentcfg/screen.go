package agentcfg

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// MinKnownSecretLen 是按「包含已知密钥值」判定时值的最短长度：更短的值（如 4 位 PIN）会误伤普通文本，不按包含判定。
const MinKnownSecretLen = 8

// placeholderRe 匹配下发里的占位令牌（Placeholder）：它们本身不是密钥，扫描前抹掉，免得被当作形似密钥的长串。
var placeholderRe = regexp.MustCompile(`oryn-ph-[A-Za-z0-9_-]+`)

// Withheld 是一个因含形似密钥的内容而没有下发的文件；Reason 已脱敏，不含密钥。
type Withheld struct {
	Path   string
	Reason string
}

// ScreenSecrets 是 runner 的无密钥投递校验（M21）：runner 是用户自己的机器，下发给它的文件里不得有密钥明文。
// 逐个文本文件（合法 UTF-8 且不含 NUL；二进制文件不查）检查形似密钥的串（ScanSecretLike，占位令牌除外）与
// known 里任一已知密钥值（名 → 明文，长度 ≥ MinKnownSecretLen）；命中的文件从 files 中删掉，返回按路径排序的扣下清单。
// models.yml 渲染后只剩占位符，同样过一遍。
func ScreenSecrets(files map[string][]byte, known map[string]string) []Withheld {
	names := make([]string, 0, len(known))
	for n, v := range known {
		if len(v) >= MinKnownSecretLen {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	var out []Withheld
	for _, p := range sortedKeys(files) {
		b := files[p]
		if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		var reasons []string
		var hit []string
		for _, n := range names {
			if bytes.Contains(b, []byte(known[n])) {
				hit = append(hit, n)
			}
		}
		if len(hit) > 0 {
			reasons = append(reasons, fmt.Sprintf("包含密钥 %s 的值", strings.Join(hit, "、")))
		}
		if like := ScanSecretLike(placeholderRe.ReplaceAll(b, []byte(" "))); len(like) > 0 {
			reasons = append(reasons, fmt.Sprintf("含形似密钥的内容（%s）", strings.Join(like, "、")))
		}
		if len(reasons) > 0 {
			delete(files, p)
			out = append(out, Withheld{Path: p, Reason: strings.Join(reasons, "；")})
		}
	}
	return out
}
