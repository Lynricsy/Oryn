package artifacts

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

// Office 类型：真实的 Word / Excel / PowerPoint / PDF 文件本身就是版本内容（版本里只有入口这一个文件）。
// 控制面只读 OOXML 的容器清单（zip 目录与 [Content_Types].xml）做校验并算页数，不解析正文、不生成 OOXML。
const (
	TypeDocx = "docx"
	TypeDocm = "docm"
	TypeXlsx = "xlsx"
	TypeXlsm = "xlsm"
	TypePptx = "pptx"
	TypePptm = "pptm"
	TypePDF  = "pdf"

	EntryDocx = "document.docx"
	EntryDocm = "document.docm"
	EntryXlsx = "workbook.xlsx"
	EntryXlsm = "workbook.xlsm"
	EntryPptx = "presentation.pptx"
	EntryPptm = "presentation.pptm"
	EntryPDF  = "document.pdf"
)

// 主部件的路径与 content type（[Content_Types].xml 里 PartName 的 Override）。
const (
	partWord  = "/word/document.xml"
	partExcel = "/xl/workbook.xml"
	partPpt   = "/ppt/presentation.xml"

	ctDocx = "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"
	ctDocm = "application/vnd.ms-word.document.macroEnabled.main+xml"
	ctXlsx = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"
	ctXlsm = "application/vnd.ms-excel.sheet.macroEnabled.main+xml"
	ctPptx = "application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"
	ctPptm = "application/vnd.ms-powerpoint.presentation.macroEnabled.main+xml"
)

// officeTypes 是按注册顺序排列的 Office 类型（文案与扩展名映射用）。
var officeTypes = []string{TypeDocx, TypeDocm, TypeXlsx, TypeXlsm, TypePptx, TypePptm, TypePDF}

// ooxmlParts 是 OOXML 类型 → 主部件路径与期望的 content type（识别「内容其实是另一种类型」用）。
var ooxmlParts = map[string]struct{ part, ct string }{
	TypeDocx: {partWord, ctDocx},
	TypeDocm: {partWord, ctDocm},
	TypeXlsx: {partExcel, ctXlsx},
	TypeXlsm: {partExcel, ctXlsm},
	TypePptx: {partPpt, ctPptx},
	TypePptm: {partPpt, ctPptm},
}

func init() {
	for typ, entry := range map[string]string{
		TypeDocx: EntryDocx, TypeDocm: EntryDocm, TypeXlsx: EntryXlsx, TypeXlsm: EntryXlsm,
		TypePptx: EntryPptx, TypePptm: EntryPptm,
	} {
		p := ooxmlParts[typ]
		typeSpecs[typ] = &TypeSpec{Name: typ, Entry: entry, Format: 1, Kind: officeKind(typ), Viewer: officeViewer(typ),
			Inspect: inspectOOXML(typ, entry, p.part, p.ct)}
	}
	typeSpecs[TypePDF] = &TypeSpec{Name: TypePDF, Entry: EntryPDF, Format: 1, Kind: KindDocument, Viewer: "pdf", Inspect: inspectPDF}
}

func officeKind(typ string) string {
	switch typ {
	case TypeXlsx, TypeXlsm:
		return KindSheet
	case TypePptx, TypePptm:
		return KindSlides
	}
	return KindDocument
}

func officeViewer(typ string) string {
	switch typ {
	case TypeXlsx, TypeXlsm:
		return "xlsx"
	case TypePptx, TypePptm:
		return "pptx"
	case TypePDF:
		return "pdf"
	}
	return "docx"
}

// IsBinaryType 报告 typ 是否是二进制（Office）类型：入口不读进内存，版本里只能有入口这一个文件。
func IsBinaryType(typ string) bool {
	t, ok := typeSpecs[typ]
	return ok && t.Inspect != nil
}

// OfficeTypeByExt 按文件名的小写扩展名返回 Office 类型；不认识时返回空串。
func OfficeTypeByExt(name string) string {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
	for _, t := range officeTypes {
		if t == ext {
			return t
		}
	}
	return ""
}

// LegacyOfficeExt 报告 name 是否是旧版或 OpenDocument 格式，并给出应转成的 OOXML 类型。
func LegacyOfficeExt(name string) (target string, ok bool) {
	switch strings.ToLower(path.Ext(name)) {
	case ".doc", ".odt":
		return TypeDocx, true
	case ".xls", ".ods":
		return TypeXlsx, true
	case ".ppt", ".odp":
		return TypePptx, true
	}
	return "", false
}

// OfficeExt 返回 Office 类型入口文件的扩展名（带点）；其余类型返回空串。
func OfficeExt(typ string) string {
	if !IsBinaryType(typ) {
		return ""
	}
	return path.Ext(TypeEntry(typ))
}

var (
	magicOLE    = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
	reSlidePart = regexp.MustCompile(`^ppt/slides/slide[0-9]+\.xml$`)
	reSheetPart = regexp.MustCompile(`^xl/worksheets/sheet[0-9]+\.xml$`)
)

// inspectOOXML 返回一个 OOXML 类型的校验函数：识别旧版 / 加密文件、zip 包、展开大小、[Content_Types].xml 与
// 主部件的 content type，并按 zip 目录算页数。
func inspectOOXML(typ, entry, mainPart, contentType string) func(io.ReaderAt, int64) (int, []Problem) {
	bad := func(format string, args ...any) (int, []Problem) {
		return 0, []Problem{{Where: entry, Message: fmt.Sprintf(format, args...)}}
	}
	return func(r io.ReaderAt, size int64) (int, []Problem) {
		head := make([]byte, len(magicOLE))
		if n, _ := r.ReadAt(head, 0); n == len(head) && bytes.Equal(head, magicOLE) {
			return bad("这是旧版二进制格式，或加了打开密码的文档；先在 orb 里用 soffice --headless --convert-to 转成 OOXML（加密文档先去掉密码）。")
		}
		zr, err := zip.NewReader(r, size)
		if err != nil {
			return bad("不是合法的 Office 文件（不是 zip 包）。")
		}
		if len(zr.File) > maxOfficeZipEntries {
			return bad("压缩包展开后过大。")
		}
		var total uint64
		var ctFile *zip.File
		pages := 0
		for _, f := range zr.File {
			total += f.UncompressedSize64
			if total > maxOfficeUnzippedBytes {
				return bad("压缩包展开后过大。")
			}
			switch {
			case f.Name == "[Content_Types].xml":
				ctFile = f
			case mainPart == partPpt && reSlidePart.MatchString(f.Name):
				pages++
			case mainPart == partExcel && reSheetPart.MatchString(f.Name):
				pages++
			}
		}
		if ctFile == nil || ctFile.UncompressedSize64 > maxContentTypesBytes {
			return bad("缺少 [Content_Types].xml，不是合法的 Office 文件。")
		}
		overrides, err := contentTypeOverrides(ctFile)
		if err != nil {
			return bad("缺少 [Content_Types].xml，不是合法的 Office 文件。")
		}
		if got := overrides[mainPart]; got != contentType {
			for _, other := range officeTypes {
				p, ok := ooxmlParts[other]
				if ok && other != typ && overrides[p.part] == p.ct {
					return bad("内容是 %s（%s）；把 type 改成 %s、文件名改成 %s。", typeLabel(other), other, other, TypeEntry(other))
				}
			}
			actual := got
			if actual == "" {
				actual = "缺少主部件"
			}
			return bad("不支持的文档变体（%s）；先另存为标准的 %s。", actual, strings.TrimPrefix(path.Ext(entry), "."))
		}
		switch mainPart {
		case partPpt:
			return pages, nil
		case partExcel:
			return max(pages, 1), nil
		}
		return 1, nil
	}
}

// contentTypeOverrides 流式解 [Content_Types].xml，只取 Override 元素：PartName → ContentType。
func contentTypeOverrides(f *zip.File) (map[string]string, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(io.LimitReader(rc, maxContentTypesBytes))
	out := map[string]string{}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Override" {
			continue
		}
		var part, ct string
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "PartName":
				part = a.Value
			case "ContentType":
				ct = a.Value
			}
		}
		if part != "" {
			out[part] = ct
		}
	}
}

// inspectPDF 要求前 1024 字节内出现 %PDF-。
func inspectPDF(r io.ReaderAt, size int64) (int, []Problem) {
	head := make([]byte, min(size, 1024))
	n, _ := r.ReadAt(head, 0)
	if !bytes.Contains(head[:n], []byte("%PDF-")) {
		return 0, []Problem{{Where: EntryPDF, Message: "不是合法的 PDF 文件（文件头里没有 %PDF-）。"}}
	}
	return 1, nil
}

// guideOffice 是 read_artifact_guide 的 office 主题。
const guideOffice = `# Office 文件（Word / Excel / PowerPoint / PDF）

真实的 Office 文件本身就是 artifact 的版本内容。七个类型与入口文件：

| type | 入口文件 | 分类 |
|---|---|---|
| docx | document.docx | 文档 |
| docm | document.docm | 文档（启用宏） |
| xlsx | workbook.xlsx | 表格 |
| xlsm | workbook.xlsm | 表格（启用宏） |
| pptx | presentation.pptx | 演示 |
| pptm | presentation.pptm | 演示（启用宏） |
| pdf | document.pdf | 文档 |

## 选型
- 用户点名 Word / Excel / PowerPoint / PDF、要可下载的 Office 文件、或给了 Office 文件 → 用对应的 Office 类型。
- 表格数据一律 xlsx。
- 其余文档与演示仍默认 document / slides。

## 写法
- 文件先在本机用真实工具链做好，再登记：create_artifact {type: "docx", files: [{path: "document.docx", source: "<本机路径>"}]}。
- 只能用 source；不能给 content、html、capabilities，版本里只能有入口这一个文件。
- 修改：先 pull_artifact 取回到工作目录，在原文件上改，再 update_artifact {baseVersion, files: [{path: <入口>, source}]}。
- Office 类型单文件不超过 256 MiB；超过 64 MiB 的文件不做预览、不出缩略图。
- 旧版（.doc .xls .ppt）与 OpenDocument（.odt .ods .odp）先用 soffice --headless --convert-to 转成 docx / xlsx / pptx。
- 启用宏的文件只存储与显示，Oryn 不执行宏。

## 评论锚点
read_artifact_comments 的每串评论给出：
- page：页（幻灯片 / 工作表）序号 number 与名字 name（工作表名、幻灯片标题）。
- point：页内归一化坐标 {x, y}（0–1，左上角为原点）；没有表示整页。
- range：表格的单元格区域（如 B2:C3），配合 page 的工作表。
- block：Markdown 文档的块序号（从 0 起）。

具体做法（工具链、渲染自查、表格重算）见 oryn-office 技能。
`
