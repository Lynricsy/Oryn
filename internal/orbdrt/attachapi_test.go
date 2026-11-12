package orbdrt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

const (
	attThread = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"
	attID     = "att_01JF0000000000000000000000"
	attID2    = "att_01JF0000000000000000000001"
)

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestAttachmentsEnsurePutDelete(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	rt.maxAttachmentBytes = 64 << 10
	o := newOrbHTTP(t, cp, rt)
	data := []byte("%PDF-1.7 fake pdf\n")
	sum := sha256Hex(data)
	wantPath := filepath.Join(rt.home, ".oryn", "attachments", attThread, attID, "report.pdf")

	ensure := func(id, name string, size int64) apigen.OrbAttachmentEnsureResult {
		t.Helper()
		var res apigen.OrbAttachmentEnsureResult
		o.json(http.MethodPost, "/attachments/ensure", nil, apigen.OrbAttachmentEnsureRequest{
			ThreadID: attThread, ID: id, Name: name, Size: size, Sha256: sum,
		}, http.StatusOK, &res)
		return res
	}
	assert.Equal(t, apigen.OrbAttachmentEnsureResult{Present: false, Path: wantPath}, ensure(attID, "report.pdf", int64(len(data))))

	put := func(id, name, sha string, body []byte) (*http.Response, []byte) {
		q := url.Values{"threadId": {attThread}, "id": {id}, "name": {name}, "sha256": {sha}}
		return o.do(http.MethodPut, "/attachments/file", q, bytes.NewReader(body), nil)
	}
	resp, b := put(attID, "report.pdf", sum, data)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	var res apigen.OrbAttachmentEnsureResult
	require.NoError(t, json.Unmarshal(b, &res))
	assert.Equal(t, apigen.OrbAttachmentEnsureResult{Present: true, Path: wantPath}, res)
	got, err := os.ReadFile(wantPath)
	require.NoError(t, err)
	assert.Equal(t, data, got)
	assert.True(t, ensure(attID, "report.pdf", int64(len(data))).Present)
	assert.False(t, ensure(attID, "report.pdf", int64(len(data))+1).Present, "大小不同视为不在")

	// sha256 不符 → 422，不落地、不留临时文件。
	resp, b = put(attID2, "other.bin", sum, []byte("different"))
	assert.Equal(t, "checksum_mismatch", o.errCode(resp, b, http.StatusUnprocessableEntity))
	assert.NoDirExists(t, filepath.Join(rt.home, ".oryn", "attachments", attThread, attID2))
	// 超过上限 → 413（带 Content-Length 与分块两种）。
	big := bytes.Repeat([]byte("x"), 64<<10+1)
	resp, b = put(attID2, "big.bin", sha256Hex(big), big)
	assert.Equal(t, "too_large", o.errCode(resp, b, http.StatusRequestEntityTooLarge))
	q := url.Values{"threadId": {attThread}, "id": {attID2}, "name": {"big.bin"}, "sha256": {sha256Hex(big)}}
	resp, b = o.do(http.MethodPut, "/attachments/file", q, struct{ *bytes.Reader }{bytes.NewReader(big)}, nil)
	assert.Equal(t, "too_large", o.errCode(resp, b, http.StatusRequestEntityTooLarge))
	assert.Empty(t, tmpResidue(t, rt))

	// 文件名净化：路径分隔符与前导点不能逃出附件目录。
	evil := []byte("evil")
	resp, b = put(attID2, "../../.bashrc", sha256Hex(evil), evil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	require.NoError(t, json.Unmarshal(b, &res))
	assert.Equal(t, filepath.Join(rt.home, ".oryn", "attachments", attThread, attID2, "_.bashrc"), res.Path)
	assert.NoFileExists(t, filepath.Join(rt.home, ".bashrc"))

	// 参数校验。
	for _, c := range []url.Values{
		{"threadId": {"../x"}, "id": {attID}, "name": {"a"}, "sha256": {sum}},
		{"threadId": {attThread}, "id": {"att_lower0000000000000000000"}, "name": {"a"}, "sha256": {sum}},
		{"threadId": {attThread}, "id": {attID}, "name": {""}, "sha256": {sum}},
		{"threadId": {attThread}, "id": {attID}, "name": {"a"}, "sha256": {"abc"}},
	} {
		resp, b := o.do(http.MethodPut, "/attachments/file", c, bytes.NewReader(data), nil)
		assert.Equal(t, "invalid_request", o.errCode(resp, b, http.StatusBadRequest), c.Encode())
	}

	// 删除单个附件，再删除整个线程目录。
	o.json(http.MethodDelete, "/attachments", url.Values{"threadId": {attThread}, "id": {attID}}, nil, http.StatusNoContent, nil)
	assert.NoDirExists(t, filepath.Join(rt.home, ".oryn", "attachments", attThread, attID))
	assert.DirExists(t, filepath.Join(rt.home, ".oryn", "attachments", attThread, attID2))
	o.json(http.MethodDelete, "/attachments", url.Values{"threadId": {attThread}}, nil, http.StatusNoContent, nil)
	assert.NoDirExists(t, filepath.Join(rt.home, ".oryn", "attachments", attThread))
	resp, b = o.do(http.MethodDelete, "/attachments", url.Values{"threadId": {".."}}, nil, nil)
	assert.Equal(t, "invalid_request", o.errCode(resp, b, http.StatusBadRequest))
	assert.DirExists(t, filepath.Join(rt.home, ".oryn"))
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":        "report.pdf",
		"dir/sub/file.txt":  "file.txt",
		`a\b.txt`:           "a_b.txt",
		".env":              "_.env",
		"..":                "_..",
		"":                  "_",
		"tab\there\x00.txt": "tab_here_.txt",
		"bad\xffutf8.txt":   "bad_utf8.txt",
		"截图 2026-11-13.png": "截图 2026-11-13.png",
	} {
		assert.Equal(t, want, safeName(in), "%q", in)
	}
	long := strings.Repeat("名", 100) + ".tar.gz" // 300 字节的主干
	got := safeName(long)
	assert.LessOrEqual(t, len(got), 200)
	assert.True(t, strings.HasSuffix(got, ".gz"), "保留扩展名")
	assert.True(t, utf8.ValidString(got))
}
