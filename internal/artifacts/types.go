package artifacts

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
)

// 类型化 artifact 的类型与入口文件。
const (
	TypeDocument  = "document"
	TypeSlides    = "slides"
	EntryDocument = "document.md"
	EntrySlides   = "slides.json"
)

const (
	// maxProblems 是一次校验最多报告的问题条数。
	maxProblems = 20
	// sniffBytes 是校验非入口文件时读取的文件头字节数（够识别 PNG / JPEG / GIF）。
	sniffBytes = 16
	// imagesDir 是类型化 artifact 放图片的目录。
	imagesDir = "images/"
)

// Problem 是类型化内容的一条校验问题：Where 是位置（`document.md:12`、`slides[2].title`、文件路径），
// Message 是中文原因。
type Problem struct{ Where, Message string }

// TypeSpec 是一种类型化 artifact 的规格。
type TypeSpec struct {
	Name, Entry string
	// Format 是这个类型当前的格式版本（记进 artifact_versions.format）。
	Format int
	// Validate 校验一个版本的全部文件：入口文件给完整内容，其余文件只给前 sniffBytes 个字节（文件头校验够用）。
	// 最多返回 maxProblems 条问题；合法时返回空。
	Validate func(files map[string][]byte) []Problem
	// Pages 返回页数；只对通过 Validate 的入口文件调用。
	Pages func(entry []byte) int
}

// typeSpecs 是类型注册表。
var typeSpecs = map[string]*TypeSpec{
	TypeDocument: {Name: TypeDocument, Entry: EntryDocument, Format: 1, Validate: validateDocument, Pages: func([]byte) int { return 1 }},
	TypeSlides:   {Name: TypeSlides, Entry: EntrySlides, Format: 1, Validate: validateSlides, Pages: deckPages},
}

// LookupType 返回类型的规格；plain（空串）与未知类型 ok 为假。
func LookupType(name string) (*TypeSpec, bool) {
	t, ok := typeSpecs[name]
	return t, ok
}

// TypeEntry 返回类型化 artifact 的入口文件；plain 与未知类型返回空串。
func TypeEntry(typ string) string {
	if t, ok := typeSpecs[typ]; ok {
		return t.Entry
	}
	return ""
}

// problems 收集校验问题，满 maxProblems 条之后不再记录。
type problems []Problem

func (ps *problems) add(where, format string, args ...any) {
	if len(*ps) < maxProblems {
		*ps = append(*ps, Problem{Where: where, Message: fmt.Sprintf(format, args...)})
	}
}

// typedInvalid 把问题列表写成 invalid 错误：逐条列出位置与原因，并指向对应的教程主题。
func typedInvalid(typ string, ps []Problem) *OpError {
	var b strings.Builder
	fmt.Fprintf(&b, "%s 内容不合法，逐条修正后再写（格式见 read_artifact_guide 的 %s 主题）：", typeLabel(typ), typ)
	for _, p := range ps {
		fmt.Fprintf(&b, "\n- %s: %s", p.Where, p.Message)
	}
	if len(ps) >= maxProblems {
		fmt.Fprintf(&b, "\n（只列出了前 %d 条）", maxProblems)
	}
	return opErr(CodeInvalid, "%s", b.String())
}

func typeLabel(typ string) string {
	switch typ {
	case TypeDocument:
		return "文档"
	case TypeSlides:
		return "幻灯片"
	}
	return typ
}

// 图片的文件头。
var (
	magicPNG   = []byte("\x89PNG\r\n\x1a\n")
	magicJPEG  = []byte("\xff\xd8\xff")
	magicGIF87 = []byte("GIF87a")
	magicGIF89 = []byte("GIF89a")
)

// imageExts 是文档 artifact 允许的图片扩展名（小写）→ 图片格式名。不收 SVG：它能带脚本，导出到 Word 也不可靠。
// 幻灯片不受这张表限制（images/ 下任意文件）。
var imageExts = map[string]string{".png": "PNG", ".jpg": "JPEG", ".jpeg": "JPEG", ".gif": "GIF"}

// sniffImage 按文件头识别图片格式；不认识时返回空串。
func sniffImage(head []byte) string {
	switch {
	case bytes.HasPrefix(head, magicPNG):
		return "PNG"
	case bytes.HasPrefix(head, magicJPEG):
		return "JPEG"
	case bytes.HasPrefix(head, magicGIF87), bytes.HasPrefix(head, magicGIF89):
		return "GIF"
	}
	return ""
}

// checkEntry 检查入口文件 entry 在文件集合里。
func checkEntry(entry string, files map[string][]byte, ps *problems) {
	if _, ok := files[entry]; !ok {
		ps.add(entry, "缺少入口文件 %s。", entry)
	}
}

// extraFiles 返回入口之外的文件路径（排好序，问题列表因此稳定）。
func extraFiles(entry string, files map[string][]byte) []string {
	paths := make([]string, 0, len(files))
	for p := range files {
		if p != entry {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	return paths
}
