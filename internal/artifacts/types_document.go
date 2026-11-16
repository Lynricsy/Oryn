package artifacts

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// mdImageRe 找出 Markdown 的行内图片引用 `![alt](目标`。
var mdImageRe = regexp.MustCompile(`!\[[^\]]*\]\(([^)\s]+)`)

// validateDocument 校验文档 artifact：document.md 是 UTF-8；行内图片引用的本地路径都在这个版本里（围栏代码块里
// 的不算）；文件集合与图片见 checkTypedFiles。
func validateDocument(files map[string][]byte) []Problem {
	const entry = EntryDocument
	var ps problems
	checkTypedFiles(TypeDocument, entry, files, &ps)
	md, ok := files[entry]
	if !ok {
		return ps
	}
	if !utf8.Valid(md) {
		ps.add(entry, "不是合法的 UTF-8；用 UTF-8 编码重写。")
		return ps
	}
	fence := ""
	for i, line := range strings.Split(string(md), "\n") {
		if marker := fenceMarker(line); marker != "" {
			switch {
			case fence == "":
				fence = marker
			case marker[0] == fence[0] && len(marker) >= len(fence) && strings.Trim(strings.TrimSpace(line), marker[:1]) == "":
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		for _, m := range mdImageRe.FindAllStringSubmatch(line, -1) {
			target := m[1]
			lower := strings.ToLower(target)
			if strings.HasPrefix(lower, "http:") || strings.HasPrefix(lower, "https:") || strings.HasPrefix(lower, "data:") {
				continue
			}
			if p := localRef(target); p == entry || !hasFile(files, p) {
				ps.add(entry+":"+strconv.Itoa(i+1), "图片 %s 不在这个版本里；把图片放进 images/ 一起写入，或改成版本里已有的路径。", target)
			}
		}
	}
	return ps
}

func hasFile(files map[string][]byte, p string) bool {
	_, ok := files[p]
	return ok
}

// fenceMarker 返回行首（最多 3 个空格缩进）的围栏代码块标记（``` 或 ~~~ 的连续串）；不是围栏行返回空串。
func fenceMarker(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 {
		return ""
	}
	c := trimmed[0]
	if c != '`' && c != '~' {
		return ""
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	if n < 3 {
		return ""
	}
	return trimmed[:n]
}

// localRef 把图片引用的目标规整成版本里的路径：去掉尖括号、开头的 ./、查询串与片段，再做百分号解码。
func localRef(target string) string {
	t := strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">")
	t = strings.TrimPrefix(t, "./")
	if i := strings.IndexAny(t, "?#"); i >= 0 {
		t = t[:i]
	}
	if u, err := url.PathUnescape(t); err == nil {
		t = u
	}
	return t
}

// guideDocument 是 document 主题。
const guideDocument = `# 文档 artifact

文档是一份 Markdown，Oryn 用内置的查看器显示，用户可以直接在里面改文字，也能导出成 Word 或 Markdown。

## 创建与修改

- create_artifact 带 type: "document"，files 里给 document.md（入口文件）；不要给 html。
- 图片放在 images/ 下（只收 .png、.jpg、.jpeg、.gif，文件内容必须与扩展名一致；不收 SVG），在 document.md 里用相对路径引用：![图表](images/chart.png)。引用的图片必须在同一个版本里。http(s) 图片显示不出来（沙箱只放行少数 CDN），不要用。
- 文档 artifact 里只能有 document.md 与 images/ 下的图片，别的文件会被拒。
- 改文字时先 read_artifact（缺省读 document.md），再用 update_artifact 的 edits（缺省改 document.md）或 files 整个替换 document.md，带上 baseVersion。
- 用户在查看器里改过的文字会成为新版本（作者是用户）；改之前一定先读最新版本。

## 能显示的 Markdown

标题（# 到 ######）、段落、强调（*斜体*、**加粗**）、~~删除线~~、` + "`行内代码`" + `、链接、有序列表、无序列表、任务列表（- [ ] / - [x]）、引用（>）、围栏代码块（带语言名会高亮）、表格（GFM 管道表格）、分隔线（---）、图片。

不显示：原生 HTML（会被忽略）、脚注、数学公式。需要这些效果时改用上面的语法，或者做成普通页面 artifact。

## 写作建议

- 用一个 # 一级标题开头，下面用 ## 分节；段落不要太长。
- 表格的每一行列数一致；代码块写上语言名（` + "```" + `go、` + "```" + `ts）。
`
