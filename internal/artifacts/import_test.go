package artifacts

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// multipartOf 把 (路径, 内容) 对编码成 multipart 请求体。
func multipartOf(t *testing.T, kv ...string) *multipart.Reader {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for i := 0; i+1 < len(kv); i += 2 {
		w, err := mw.CreateFormFile(kv[i], kv[i])
		require.NoError(t, err)
		_, _ = io.WriteString(w, kv[i+1])
	}
	require.NoError(t, mw.Close())
	return multipart.NewReader(&buf, mw.Boundary())
}

func TestImport(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// 单个 HTML 文件：标题取 <title>，origin = import，没有线程，作者是用户
	a, err := e.s.Import(ctx, multipartOf(t, "index.html", page), ImportParams{Name: "report.html"})
	require.NoError(t, err)
	assert.Equal(t, "周报", a.Title)
	assert.Equal(t, OriginImport, a.Origin)
	assert.Equal(t, KindWeb, a.Kind)
	assert.Empty(t, a.ThreadID)
	vs, err := e.s.Versions(ctx, a.ID)
	require.NoError(t, err)
	require.Len(t, vs, 1)
	assert.Empty(t, vs[0].AuthorThreadID)
	assert.Equal(t, page, readFile(t, e.s, a.ID, 0, "index.html"))

	// 文件夹：隐藏文件被跳过；没有 <title> 时用上传名
	a, err = e.s.Import(ctx, multipartOf(t,
		"index.html", "<p>hi</p>", "css/a.css", "p{}", ".DS_Store", "x", ".git/config", "y", "img/.hidden/z.png", "z",
	), ImportParams{Name: "我的站点"})
	require.NoError(t, err)
	assert.Equal(t, "我的站点", a.Title)
	files, err := e.s.Files(ctx, a.ID, 0)
	require.NoError(t, err)
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	assert.Equal(t, []string{"css/a.css", "index.html"}, paths)

	a, err = e.s.Import(ctx, multipartOf(t, "index.html", "<p>hi</p>"), ImportParams{Name: "report.htm"})
	require.NoError(t, err)
	assert.Equal(t, "report", a.Title)
	a, err = e.s.Import(ctx, multipartOf(t, "index.html", "<p>hi</p>"), ImportParams{})
	require.NoError(t, err)
	assert.Equal(t, untitled, a.Title)

	// 只上传一个 HTML 文件（界面「导入 › HTML 文件」以文件名作字段名）：它就是页面本身，存成 index.html
	a, err = e.s.Import(ctx, multipartOf(t, "周报.html", page), ImportParams{Name: "周报.html"})
	require.NoError(t, err)
	assert.Equal(t, "周报", a.Title)
	assert.Equal(t, KindWeb, a.Kind)
	assert.Equal(t, page, readFile(t, e.s, a.ID, 0, "index.html"))
	files, err = e.s.Files(ctx, a.ID, 0)
	require.NoError(t, err)
	require.Len(t, files, 1)

	// 只上传一个 Markdown 文件：导入成文档 artifact，内容存为 document.md，标题取文件名
	const doc = "# 周报\n\n本周完成了 **导入**。\n"
	a, err = e.s.Import(ctx, multipartOf(t, "周报.md", doc), ImportParams{Name: "周报.md"})
	require.NoError(t, err)
	assert.Equal(t, TypeDocument, a.Type)
	assert.Equal(t, KindDocument, a.Kind)
	assert.Equal(t, "周报", a.Title)
	assert.Equal(t, doc, readFile(t, e.s, a.ID, 0, EntryDocument))
	a, err = e.s.Import(ctx, multipartOf(t, "Notes.MARKDOWN", "hi\n"), ImportParams{})
	require.NoError(t, err)
	assert.Equal(t, TypeDocument, a.Type)
	assert.Equal(t, "Notes", a.Title)

	// 失败
	_, err = e.s.Import(ctx, multipartOf(t, "a.html", "<p>hi</p>", "b.html", "<p>b</p>"), ImportParams{})
	assert.Equal(t, CodeNoIndex, code(err))
	_, err = e.s.Import(ctx, multipartOf(t, "site/a.html", "<p>hi</p>"), ImportParams{})
	assert.Equal(t, CodeNoIndex, code(err))
	_, err = e.s.Import(ctx, multipartOf(t, ".DS_Store", "x"), ImportParams{})
	assert.Equal(t, CodeNoIndex, code(err))
	// 以点开头的段（含 ..）一律跳过，不会落成文件；其余不合法的路径回 bad_name
	for _, bad := range []string{"a\\b.js", "/abs.js", "_oryn/x.js", "a//b.js", "con:1"} {
		_, err = e.s.Import(ctx, multipartOf(t, "index.html", "x", bad, "x"), ImportParams{})
		assert.Equal(t, CodeBadName, code(err), bad)
	}
	_, err = e.s.Import(ctx, multipartOf(t, "index.html", "x", "A.css", "x", "a.css", "y"), ImportParams{})
	assert.Equal(t, CodeBadName, code(err))
	_, err = e.s.Import(ctx, multipartOf(t, "index.html", "\xff\xfe"), ImportParams{})
	assert.Equal(t, CodeNotUTF8, code(err))
	_, err = e.s.Import(ctx, multipartOf(t, "index.html", "x"), ImportParams{ProjectID: "P-none"})
	assert.Equal(t, CodeInvalid, code(err))
	// 画布：canvas.json 严格解析，画框文件必须一起导入
	a, err = e.s.Import(ctx, multipartOf(t, "canvas.json", `{"pages":[{"id":"p1","name":"A","file":"a.html"}]}`, "a.html", "<p>a</p>"), ImportParams{})
	require.NoError(t, err)
	assert.Equal(t, KindCanvas, a.Kind)
	assert.Equal(t, 1, a.Pages)
	for _, bad := range []string{`{"pages":[{"id":"p1","name":"A","file":"gone.html"}]}`, `{"pages":[],"x":1}`, `{`} {
		_, err = e.s.Import(ctx, multipartOf(t, "canvas.json", bad, "a.html", "<p>a</p>"), ImportParams{})
		assert.Equal(t, CodeBadCanvas, code(err), bad)
	}

	kv := []string{"index.html", "<p>x</p>"}
	for i := range MaxVersionFiles {
		kv = append(kv, fmt.Sprintf("f/%d.txt", i), "x")
	}
	_, err = e.s.Import(ctx, multipartOf(t, kv...), ImportParams{})
	assert.Equal(t, CodeTooMany, code(err))
	assert.Equal(t, http.StatusRequestEntityTooLarge, (&OpError{Code: CodeTooMany}).Status())
	assert.Equal(t, http.StatusBadRequest, (&OpError{Code: CodeNoIndex}).Status())

	// 请求体超过上限 → too_large
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	w, _ := mw.CreateFormFile("index.html", "index.html")
	_, _ = io.WriteString(w, strings.Repeat("x", 4096))
	_ = mw.Close()
	limited := http.MaxBytesReader(nil, io.NopCloser(&buf), 1024)
	_, err = e.s.Import(ctx, multipart.NewReader(limited, mw.Boundary()), ImportParams{})
	assert.Equal(t, CodeTooLarge, code(err))
}

func TestExport(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	single := mkPage(t, e, put("index.html", page))
	x, err := e.s.Export(ctx, single.ID, 0, ExportHTML)
	require.NoError(t, err)
	assert.Equal(t, "周报.html", x.Name)
	var out bytes.Buffer
	require.NoError(t, x.Write(&out))
	assert.Equal(t, page, out.String())

	multi := mkPage(t, e)
	_, err = e.s.Export(ctx, multi.ID, 0, ExportHTML)
	assert.Equal(t, CodeInvalid, code(err))
	_, err = e.s.Rename(ctx, multi.ID, "a/b")
	require.NoError(t, err)
	x, err = e.s.Export(ctx, multi.ID, 0, ExportZip)
	require.NoError(t, err)
	assert.Equal(t, "a_b.zip", x.Name)
	out.Reset()
	require.NoError(t, x.Write(&out))
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	require.NoError(t, err)
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		got[f.Name] = string(b)
	}
	assert.Equal(t, map[string]string{"index.html": page, "style.css": "h1{color:red}"}, got)

	_, err = e.s.Export(ctx, multi.ID, 0, "pdf")
	assert.Equal(t, CodeInvalid, code(err))
	_, err = e.s.Export(ctx, multi.ID, 7, ExportZip)
	assert.Equal(t, CodeNotFound, code(err))
}
