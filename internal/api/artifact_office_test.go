package api_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// officeDocx 合成一个最小的 docx 包（只有 [Content_Types].xml 与主部件）；note 让不同调用得到不同字节。
func officeDocx(t *testing.T, note string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("[Content_Types].xml")
	require.NoError(t, err)
	fmt.Fprint(w, `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`+
		`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`)
	w, err = zw.Create("word/document.xml")
	require.NoError(t, err)
	fmt.Fprintf(w, "<w:document>%s</w:document>", note)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// Office 文件的导入、上传新版本（含陈旧 baseVersion 的 409）与原件导出。
func TestArtifactOfficeREST(t *testing.T) {
	srv, _ := newFramesEnv(t)
	src := officeDocx(t, "v1")
	body, ct := multipartBody(t, "季度报告.docx", string(src))
	resp, b := send(t, "POST", srv.URL+"/api/v1/artifacts/import?name="+url.QueryEscape("季度报告.docx"), body,
		map[string]string{"X-Test-Auth": "ok", "Content-Type": ct})
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(b))
	a := decodeAs[apigen.Artifact](t, b)
	assert.Equal(t, "季度报告", a.Title)
	assert.Equal(t, apigen.ArtifactOrigin("import"), a.Origin)
	require.NotNil(t, a.Type)
	assert.Equal(t, apigen.ArtifactType("docx"), *a.Type)
	assert.Equal(t, apigen.ArtifactKind("document"), a.Kind)

	body, ct = multipartBody(t, "old.doc", "x")
	resp, b = send(t, "POST", srv.URL+"/api/v1/artifacts/import", body, map[string]string{"X-Test-Auth": "ok", "Content-Type": ct})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "unsupported_format", decodeAs[map[string]any](t, b)["code"])

	upload := func(base int, data []byte) (*http.Response, []byte) {
		return send(t, "POST", fmt.Sprintf("%s/api/v1/artifacts/%s/upload?baseVersion=%d", srv.URL, a.ID, base), bytes.NewReader(data),
			map[string]string{"X-Test-Auth": "ok", "Content-Type": "application/octet-stream"})
	}
	v2 := officeDocx(t, "v2")
	resp, b = upload(1, v2)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	assert.Equal(t, 2, decodeAs[apigen.Artifact](t, b).Version)
	resp, b = upload(1, officeDocx(t, "v3"))
	assert.Equal(t, http.StatusConflict, resp.StatusCode, string(b))
	resp, b = upload(2, []byte("not a zip"))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, string(b))
	assert.Equal(t, "bad_office", decodeAs[map[string]any](t, b)["code"])
	resp, b = send(t, "GET", srv.URL+"/api/v1/artifacts/"+a.ID, nil, authed)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	assert.Equal(t, 2, decodeAs[apigen.ArtifactDetail](t, b).Head, "损坏的文件不产生版本")

	resp, b = send(t, "GET", srv.URL+"/api/v1/artifacts/"+a.ID+"/export?format=original", nil, authed)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	assert.Equal(t, v2, b)
	assert.Contains(t, resp.Header.Get("Content-Disposition"), ".docx")
}
