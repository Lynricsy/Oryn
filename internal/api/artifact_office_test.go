package api_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts/office"
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

// fakeOfficeEngine 是只有 discovery 与能力的假 Office 引擎。
func fakeOfficeEngine(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hosting/discovery":
			fmt.Fprint(w, `<wopi-discovery><net-zone name="external-http"><app name="writer">`+
				`<action ext="docx" name="edit" urlsrc="http://coolwsd:9980/browser/h/cool.html?"/></app></net-zone></wopi-discovery>`)
		case "/hosting/capabilities":
			fmt.Fprint(w, `{"convert-to":{"available":true}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// M32：能力里的 office、编辑会话的打开 / 心跳 / 关闭、详情的 editing 与占用期间上传回 423 locked。
func TestArtifactOfficeSessionsREST(t *testing.T) {
	srv, svc := newFramesEnv(t)
	resp, b := send(t, "GET", srv.URL+"/api/v1/artifacts/capabilities", nil, authed)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	caps := decodeAs[apigen.ArtifactCapabilities](t, b)
	assert.False(t, caps.Office.Edit)
	assert.False(t, caps.Office.Convert)
	require.NotNil(t, caps.Office.Reason)

	body, ct := multipartBody(t, "报告.docx", string(officeDocx(t, "v1")))
	resp, b = send(t, "POST", srv.URL+"/api/v1/artifacts/import", body, map[string]string{"X-Test-Auth": "ok", "Content-Type": ct})
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(b))
	a := decodeAs[apigen.Artifact](t, b)
	resp, b = send(t, "POST", srv.URL+"/api/v1/artifacts/"+a.ID+"/office-sessions", strings.NewReader(`{}`), authed)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "没有引擎："+string(b))

	svc.UseOffice(office.New(office.Config{URL: "https://office.test", InternalURL: fakeOfficeEngine(t).URL}), "https://oryn.test")
	resp, b = send(t, "GET", srv.URL+"/api/v1/artifacts/capabilities", nil, authed)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	caps = decodeAs[apigen.ArtifactCapabilities](t, b)
	assert.True(t, caps.Office.Edit)
	assert.True(t, caps.Office.Convert)
	assert.Nil(t, caps.Office.Reason)

	detail := func() apigen.ArtifactDetail {
		resp, b := send(t, "GET", srv.URL+"/api/v1/artifacts/"+a.ID, nil, authed)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
		return decodeAs[apigen.ArtifactDetail](t, b)
	}
	require.NotNil(t, detail().Editing)
	assert.False(t, *detail().Editing)

	resp, b = send(t, "POST", srv.URL+"/api/v1/artifacts/"+a.ID+"/office-sessions", strings.NewReader(`{"locale":"zh-CN"}`), authed)
	require.Equal(t, http.StatusCreated, resp.StatusCode, string(b))
	sess := decodeAs[apigen.ArtifactOfficeSession](t, b)
	assert.True(t, strings.HasPrefix(sess.SessionID, "aos_"))
	assert.NotEmpty(t, sess.AccessToken)
	assert.Greater(t, sess.AccessTokenTTL, time.Now().UnixMilli())
	assert.Contains(t, sess.ActionURL, "https://office.test/browser/h/cool.html?WOPISrc="+url.QueryEscape("https://oryn.test/wopi/files/"+a.ID)+"&lang=zh-CN")
	assert.True(t, *detail().Editing)

	resp, b = send(t, "POST", fmt.Sprintf("%s/api/v1/artifacts/%s/upload?baseVersion=1", srv.URL, a.ID), bytes.NewReader(officeDocx(t, "v2")),
		map[string]string{"X-Test-Auth": "ok", "Content-Type": "application/octet-stream"})
	assert.Equal(t, http.StatusLocked, resp.StatusCode, string(b))
	assert.Equal(t, "locked", decodeAs[map[string]any](t, b)["code"])

	resp, _ = send(t, "POST", srv.URL+"/api/v1/artifact-office-sessions/"+sess.SessionID+"/beat", nil, authed)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp, _ = send(t, "POST", srv.URL+"/api/v1/artifact-office-sessions/aos_01M8DNTBYA6YTTF9SXCTG6B6VZ/beat", nil, authed)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = send(t, "DELETE", srv.URL+"/api/v1/artifact-office-sessions/"+sess.SessionID, nil, authed)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.False(t, *detail().Editing, "关闭后 editing 立即为假")
	resp, _ = send(t, "DELETE", srv.URL+"/api/v1/artifact-office-sessions/"+sess.SessionID, nil, authed)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode, "重复关闭也回 204")
}
