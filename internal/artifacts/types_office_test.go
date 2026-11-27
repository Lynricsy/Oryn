package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ooxmlZip 合成一个最小 OOXML 包：[Content_Types].xml 里给 mainPart 一条 Override（ct 为空则不写），再加上 extra 里的条目。
func ooxmlZip(t testing.TB, mainPart, ct string, extra ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("[Content_Types].xml")
	require.NoError(t, err)
	override := ""
	if ct != "" {
		override = fmt.Sprintf(`<Override PartName="%s" ContentType="%s"/>`, mainPart, ct)
	}
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`+
		`<Default Extension="xml" ContentType="application/xml"/>%s</Types>`, override)
	for _, name := range extra {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, _ = w.Write([]byte("<x/>"))
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func docxBytes(t testing.TB) []byte { return ooxmlZip(t, partWord, ctDocx, "word/document.xml") }
func xlsxBytes(t testing.TB) []byte {
	return ooxmlZip(t, partExcel, ctXlsx, "xl/workbook.xml", "xl/worksheets/sheet1.xml", "xl/worksheets/sheet2.xml")
}
func pptxBytes(t testing.TB) []byte {
	return ooxmlZip(t, partPpt, ctPptx, "ppt/presentation.xml", "ppt/slides/slide1.xml", "ppt/slides/slide2.xml", "ppt/slides/slide3.xml", "ppt/slides/_rels/slide1.xml.rels")
}
func pdfBytes() []byte { return []byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n1 0 obj<<>>endobj\n%%EOF\n") }
func putBytes(path string, b []byte) FileChange {
	return FileChange{Path: path, Data: bytes.NewReader(b)}
}

func TestOfficeTypes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, c := range []struct {
		typ, entry, kind string
		data             []byte
		pages            int
	}{
		{TypeDocx, EntryDocx, KindDocument, docxBytes(t), 1},
		{TypeDocm, EntryDocm, KindDocument, ooxmlZip(t, partWord, ctDocm), 1},
		{TypeXlsx, EntryXlsx, KindSheet, xlsxBytes(t), 2},
		{TypeXlsm, EntryXlsm, KindSheet, ooxmlZip(t, partExcel, ctXlsm), 1},
		{TypePptx, EntryPptx, KindSlides, pptxBytes(t), 3},
		{TypePptm, EntryPptm, KindSlides, ooxmlZip(t, partPpt, ctPptm, "ppt/slides/slide1.xml"), 1},
		{TypePDF, EntryPDF, KindDocument, pdfBytes(), 1},
	} {
		t.Run(c.typ, func(t *testing.T) {
			a, err := createTyped(t, e, c.typ, putBytes(c.entry, c.data))
			require.NoError(t, err)
			assert.Equal(t, c.kind, a.Kind)
			assert.Equal(t, c.typ, a.Type)
			assert.Equal(t, c.pages, a.Pages)
			assert.Equal(t, c.entry, a.Entry())
			assert.True(t, IsBinaryType(c.typ))
		})
	}

	a, err := createTyped(t, e, TypeDocx, putBytes(EntryDocx, docxBytes(t)))
	require.NoError(t, err)
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("images/a.png", pngBytes)}})
	require.Equal(t, CodeInvalid, code(err))
	assert.Contains(t, err.Error(), "Word 文档 artifact 只能有 document.docx 这一个文件。")
}

func TestInspectOOXMLProblems(t *testing.T) {
	e := newEnv(t)
	ole := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 32)...)
	var many bytes.Buffer
	zw := zip.NewWriter(&many)
	for i := 0; i <= maxOfficeZipEntries; i++ {
		_, _ = zw.Create(fmt.Sprintf("f%d", i))
	}
	require.NoError(t, zw.Close())
	for name, c := range map[string]struct {
		typ, entry string
		data       []byte
		want       string
	}{
		"ole":      {TypeDocx, EntryDocx, ole, "这是旧版二进制格式，或加了打开密码的文档"},
		"not zip":  {TypeDocx, EntryDocx, []byte("hello world"), "不是合法的 Office 文件（不是 zip 包）。"},
		"too many": {TypeDocx, EntryDocx, many.Bytes(), "压缩包展开后过大。"},
		"no manifest": {TypeDocx, EntryDocx, func() []byte {
			var b bytes.Buffer
			z := zip.NewWriter(&b)
			_, _ = z.Create("word/document.xml")
			_ = z.Close()
			return b.Bytes()
		}(), "缺少 [Content_Types].xml"},
		"other type":    {TypeDocx, EntryDocx, xlsxBytes(t), "内容是 Excel 表格（xlsx）；把 type 改成 xlsx、文件名改成 workbook.xlsx。"},
		"macro variant": {TypeDocx, EntryDocx, ooxmlZip(t, partWord, "application/vnd.ms-word.template.main+xml"), "不支持的文档变体（application/vnd.ms-word.template.main+xml）；先另存为标准的 docx。"},
		"not pdf":       {TypePDF, EntryPDF, []byte("hello"), "不是合法的 PDF 文件"},
		"multi files":   {TypeXlsx, EntryXlsx, nil, "Excel 表格 artifact 只能有 workbook.xlsx 这一个文件。"},
	} {
		t.Run(name, func(t *testing.T) {
			files := []FileChange{putBytes(c.entry, c.data)}
			if c.data == nil {
				files = []FileChange{putBytes(c.entry, xlsxBytes(t)), put("extra.txt", "x")}
			}
			_, err := createTyped(t, e, c.typ, files...)
			require.Equal(t, CodeInvalid, code(err), "%v", err)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

func TestOfficeTypeHelpers(t *testing.T) {
	assert.Equal(t, TypeDocx, OfficeTypeByExt("季度报告.DOCX"))
	assert.Equal(t, TypePDF, OfficeTypeByExt("a.pdf"))
	assert.Empty(t, OfficeTypeByExt("a.doc"))
	target, ok := LegacyOfficeExt("old.ODP")
	assert.True(t, ok)
	assert.Equal(t, TypePptx, target)
	_, ok = LegacyOfficeExt("a.docx")
	assert.False(t, ok)
	assert.False(t, IsBinaryType(TypeDocument))
	assert.Equal(t, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", MimeOf("workbook.xlsx"))
}
