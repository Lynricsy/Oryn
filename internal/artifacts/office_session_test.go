package artifacts

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts/office"
)

// fakeEngine 是 httptest 假引擎：discovery（可带 proof-key）、能力与 convert-to。convert 返回 nil 表示拒绝转换。
type fakeEngine struct {
	srv      *httptest.Server
	proof    *rsa.PrivateKey
	convert  bool
	converts atomic.Int32
	// out 按目标格式给出转换结果；没有的格式回 400。
	out map[string][]byte
}

const fakeDiscovery = `<?xml version="1.0" encoding="utf-8"?><wopi-discovery><net-zone name="external-http">` +
	`<app name="writer"><action ext="docx" name="edit" urlsrc="http://coolwsd:9980/browser/h1/cool.html?"/>` +
	`<action ext="docm" name="edit" urlsrc="http://coolwsd:9980/browser/h1/cool.html?"/></app>` +
	`<app name="calc"><action ext="xlsx" name="edit" urlsrc="http://coolwsd:9980/browser/h1/cool.html?"/></app>` +
	`</net-zone>%s</wopi-discovery>`

func newFakeEngine(t *testing.T, convert bool, proof *rsa.PrivateKey) *fakeEngine {
	t.Helper()
	f := &fakeEngine{proof: proof, convert: convert, out: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/hosting/discovery":
			pk := ""
			if f.proof != nil {
				pk = fmt.Sprintf(`<proof-key modulus="%s" exponent="%s"/>`,
					base64.StdEncoding.EncodeToString(f.proof.N.Bytes()), base64.StdEncoding.EncodeToString(big.NewInt(int64(f.proof.E)).Bytes()))
			}
			fmt.Fprintf(w, fakeDiscovery, pk)
		case r.URL.Path == "/hosting/capabilities":
			fmt.Fprintf(w, `{"convert-to":{"available":%t},"productName":"Fake"}`, f.convert)
		case strings.HasPrefix(r.URL.Path, "/cool/convert-to/"):
			f.converts.Add(1)
			src, _, err := r.FormFile("data")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_, _ = io.Copy(io.Discard, src)
			b, ok := f.out[strings.TrimPrefix(r.URL.Path, "/cool/convert-to/")]
			if !ok {
				http.Error(w, "conversion failed: bad input", http.StatusBadRequest)
				return
			}
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

const testWOPIBase = "https://oryn.test"

func useFakeEngine(e *env, f *fakeEngine) {
	e.s.UseOffice(office.New(office.Config{URL: "https://office.test", InternalURL: f.srv.URL, Now: e.clk.now}), testWOPIBase)
}

// wopiClient 是假 WOPI 客户端（扮演引擎回调控制面）。
type wopiClient struct {
	t     *testing.T
	h     http.Handler
	id    string
	token string
	proof *rsa.PrivateKey
	now   func() time.Time
}

func (c wopiClient) do(method, sub string, body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	c.t.Helper()
	target := "/wopi/files/" + c.id + sub + "?access_token=" + url.QueryEscape(c.token)
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if c.proof != nil {
		ticks := c.now().UnixNano()/100 + 621355968000000000
		sum := sha256.Sum256(office.ProofData(c.token, testWOPIBase+target, ticks))
		sig, err := rsa.SignPKCS1v15(rand.Reader, c.proof, crypto.SHA256, sum[:])
		require.NoError(c.t, err)
		req.Header.Set("X-WOPI-TimeStamp", strconv.FormatInt(ticks, 10))
		req.Header.Set("X-WOPI-Proof", base64.StdEncoding.EncodeToString(sig))
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func (c wopiClient) put(body []byte, hdr map[string]string) *httptest.ResponseRecorder {
	return c.do(http.MethodPost, "/contents", body, hdr)
}

func (c wopiClient) lock(op, lock string) *httptest.ResponseRecorder {
	return c.do(http.MethodPost, "", nil, map[string]string{"X-WOPI-Override": op, "X-WOPI-Lock": lock})
}

func mkDocx(t *testing.T, e *env) Artifact {
	t.Helper()
	a, err := e.s.Create(context.Background(), CreateParams{Title: "季度报告", Type: TypeDocx, Files: []FileChange{putBytes(EntryDocx, docxBytes(t))}})
	require.NoError(t, err)
	return a
}

// docxV 是内容不同的合法 docx（多一个部件）。
func docxV(t *testing.T, n int) []byte {
	return ooxmlZip(t, partWord, ctDocx, "word/document.xml", fmt.Sprintf("word/media/image%d.png", n))
}

func openWOPI(t *testing.T, e *env, id string) (OfficeSession, wopiClient) {
	t.Helper()
	sess, err := e.s.OpenOffice(context.Background(), id, "zh-CN")
	require.NoError(t, err)
	return sess, wopiClient{t: t, h: e.s.WOPIHandler(), id: id, token: sess.Token, now: e.clk.now}
}

func TestOfficeStatus(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	st := e.s.OfficeStatus(ctx)
	assert.False(t, st.Edit)
	assert.False(t, st.Convert)
	assert.Contains(t, st.Reason, "没有配置 Office 引擎")
	useFakeEngine(e, newFakeEngine(t, true, nil))
	assert.Equal(t, OfficeStatus{Edit: true, Convert: true}, e.s.OfficeStatus(ctx))

	e2 := newEnv(t)
	useFakeEngine(e2, newFakeEngine(t, false, nil))
	st = e2.s.OfficeStatus(ctx)
	assert.True(t, st.Edit)
	assert.False(t, st.Convert)
	assert.Contains(t, st.Reason, "convert-to")
}

func TestOfficeOpen(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := mkDocx(t, e)
	_, err := e.s.OpenOffice(ctx, a.ID, "")
	assert.Equal(t, CodeInvalid, code(err), "没有引擎")
	useFakeEngine(e, newFakeEngine(t, true, nil))

	sess, err := e.s.OpenOffice(ctx, a.ID, "zh-CN")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(sess.ID, "aos_"))
	assert.Len(t, sess.Token, 43, "32 字节 base64url")
	assert.Equal(t, e.clk.now().Add(12*time.Hour).UnixMilli(), sess.TokenExpires.UnixMilli())
	u, err := url.Parse(sess.ActionURL)
	require.NoError(t, err)
	assert.Equal(t, "https://office.test/browser/h1/cool.html", u.Scheme+"://"+u.Host+u.Path, "主机换成 office.url")
	assert.Equal(t, testWOPIBase+"/wopi/files/"+a.ID, u.Query().Get("WOPISrc"))
	assert.Equal(t, "zh-CN", u.Query().Get("lang"))
	row, err := e.st.Q.GetArtifactOfficeSession(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, tokenHash(sess.Token), row.TokenHash, "只存令牌的哈希")
	assert.NotContains(t, row.TokenHash, sess.Token)

	editing, err := e.s.Editing(ctx, a.ID)
	require.NoError(t, err)
	assert.True(t, editing)

	_, err = e.s.OpenOffice(ctx, a.ID, "zh-CN\"><x")
	assert.Equal(t, CodeInvalid, code(err))
	pdf, err := e.s.Create(ctx, CreateParams{Title: "p", Type: TypePDF, Files: []FileChange{putBytes(EntryPDF, pdfBytes())}})
	require.NoError(t, err)
	_, err = e.s.OpenOffice(ctx, pdf.ID, "")
	assert.Equal(t, CodeInvalid, code(err), "PDF 不能编辑")
	pp, err := e.s.Create(ctx, CreateParams{Title: "p", Type: TypePptx, Files: []FileChange{putBytes(EntryPptx, pptxBytes(t))}})
	require.NoError(t, err)
	_, err = e.s.OpenOffice(ctx, pp.ID, "")
	assert.Contains(t, err.Error(), "不支持编辑 .pptx", "discovery 里没有 pptx 的 edit")
	_, err = e.s.Delete(ctx, mkDocx(t, e).ID)
	require.NoError(t, err)
}

func TestWOPIFlow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	useFakeEngine(e, newFakeEngine(t, true, nil))
	a := mkDocx(t, e)
	_, c := openWOPI(t, e, a.ID)

	// CheckFileInfo
	rec := c.do(http.MethodGet, "", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var info map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	v1, err := e.s.Version(ctx, a.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, "季度报告.docx", info["BaseFileName"])
	assert.EqualValues(t, len(docxBytes(t)), info["Size"])
	assert.Equal(t, "oryn", info["OwnerId"])
	assert.Equal(t, "owner", info["UserId"])
	assert.Equal(t, true, info["UserCanWrite"])
	assert.Equal(t, true, info["SupportsLocks"])
	assert.Equal(t, true, info["SupportsUpdate"])
	assert.Equal(t, true, info["UserCanNotWriteRelative"])
	assert.Equal(t, "http://localhost:7600", info["PostMessageOrigin"])
	assert.Equal(t, wopiTime(v1.CreatedAt), info["LastModifiedTime"])

	// GetFile
	rec = c.do(http.MethodGet, "/contents", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, docxBytes(t), rec.Body.Bytes())

	// 锁
	assert.Equal(t, http.StatusOK, c.lock("LOCK", "L1").Code)
	assert.Equal(t, http.StatusOK, c.lock("LOCK", "L1").Code, "同一把锁再 LOCK 是刷新")
	rec = c.lock("LOCK", "L2")
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "L1", rec.Header().Get("X-WOPI-Lock"))
	assert.Equal(t, http.StatusOK, c.lock("REFRESH_LOCK", "L1").Code)
	assert.Equal(t, http.StatusConflict, c.lock("UNLOCK", "L2").Code)
	rec = c.do(http.MethodPost, "", nil, map[string]string{"X-WOPI-Override": "GET_LOCK"})
	assert.Equal(t, "L1", rec.Header().Get("X-WOPI-Lock"))
	assert.Equal(t, http.StatusNotImplemented, c.do(http.MethodPost, "", nil, map[string]string{"X-WOPI-Override": "PUT_RELATIVE"}).Code)

	// 锁串不符的 PutFile 回 409
	rec = c.put(docxV(t, 1), map[string]string{"X-WOPI-Lock": "L9"})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "L1", rec.Header().Get("X-WOPI-Lock"))
	// 不合法的正文回 400
	rec = c.put([]byte("not a zip"), map[string]string{"X-WOPI-Lock": "L1"})
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// 自动保存：只更新工作副本，不出版本
	auto := map[string]string{"X-WOPI-Lock": "L1", "X-COOL-WOPI-IsAutosave": "true", "X-COOL-WOPI-Timestamp": wopiTime(v1.CreatedAt)}
	rec = c.put(docxV(t, 1), auto)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"LastModifiedTime":"`+wopiTime(v1.CreatedAt)+`"}`, rec.Body.String())
	assert.Equal(t, 1, mustGet(t, e, a.ID).Version)
	assert.Equal(t, docxV(t, 1), c.do(http.MethodGet, "/contents", nil, nil).Body.Bytes(), "GetFile 给草稿")

	// 占用期间其他写入一律 locked
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Author: Author{ThreadID: "T-1"}, Files: []FileChange{putBytes(EntryDocx, docxV(t, 2))}})
	require.Equal(t, CodeLocked, code(err))
	assert.Contains(t, err.Error(), "用户正在编辑器里编辑这个文档；请让用户保存并关闭编辑器后再试。")
	_, err = e.s.UploadVersion(ctx, a.ID, 1, bytes.NewReader(docxV(t, 2)))
	assert.Equal(t, CodeLocked, code(err))

	// 显式保存：落版本，作者是用户；回新版本的时间
	e.clk.add(time.Second)
	rec = c.put(docxV(t, 3), map[string]string{"X-WOPI-Lock": "L1", "X-COOL-WOPI-IsAutosave": "false", "X-COOL-WOPI-Timestamp": wopiTime(v1.CreatedAt)})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := mustGet(t, e, a.ID)
	require.Equal(t, 2, got.Version)
	v2, err := e.s.Version(ctx, a.ID, 2)
	require.NoError(t, err)
	assert.Empty(t, v2.AuthorThreadID, "作者是用户")
	assert.JSONEq(t, `{"LastModifiedTime":"`+wopiTime(v2.CreatedAt)+`"}`, rec.Body.String())
	assert.Equal(t, string(docxV(t, 3)), readFile(t, e.s, a.ID, 2, EntryDocx))
	h, err := e.st.Q.GetArtifactOfficeHold(ctx, a.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, h.BaseVersion)
	assert.Nil(t, h.DraftSha, "草稿已清")

	// 内容没变的退出保存不出版本
	rec = c.put(docxV(t, 3), map[string]string{"X-WOPI-Lock": "L1", "X-COOL-WOPI-IsExitSave": "true", "X-COOL-WOPI-Timestamp": wopiTime(v2.CreatedAt)})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 2, mustGet(t, e, a.ID).Version)

	// 旧的时间戳：存储里的文件被改过
	rec = c.put(docxV(t, 4), map[string]string{"X-WOPI-Lock": "L1", "X-COOL-WOPI-Timestamp": wopiTime(v1.CreatedAt)})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.JSONEq(t, `{"COOLStatusCode":1010}`, rec.Body.String())

	assert.Equal(t, http.StatusOK, c.lock("UNLOCK", "L1").Code)
	h, err = e.st.Q.GetArtifactOfficeHold(ctx, a.ID)
	require.NoError(t, err)
	assert.Empty(t, h.Lock)
}

func TestWOPIAuth(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	useFakeEngine(e, newFakeEngine(t, true, nil))
	a := mkDocx(t, e)
	b := mkDocx(t, e)
	_, c := openWOPI(t, e, a.ID)
	assert.Equal(t, http.StatusOK, c.do(http.MethodGet, "", nil, nil).Code)

	bad := c
	bad.token = "nope"
	assert.Equal(t, http.StatusUnauthorized, bad.do(http.MethodGet, "", nil, nil).Code, "无效令牌")
	other := c
	other.id = b.ID
	assert.Equal(t, http.StatusUnauthorized, other.do(http.MethodGet, "", nil, nil).Code, "令牌属于另一个 artifact")
	none := c
	none.token = ""
	assert.Equal(t, http.StatusUnauthorized, none.do(http.MethodGet, "", nil, nil).Code)

	// 令牌过期
	e.clk.add(officeTokenTTL)
	assert.Equal(t, http.StatusUnauthorized, c.do(http.MethodGet, "", nil, nil).Code)
	_, err := e.s.OpenOffice(ctx, a.ID, "")
	require.NoError(t, err)
}

func TestWOPIProof(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	e := newEnv(t)
	useFakeEngine(e, newFakeEngine(t, true, key))
	a := mkDocx(t, e)
	_, c := openWOPI(t, e, a.ID)
	assert.Equal(t, http.StatusUnauthorized, c.do(http.MethodGet, "", nil, nil).Code, "有 proof-key 时不带签名")
	signed := c
	signed.proof = key
	assert.Equal(t, http.StatusOK, signed.do(http.MethodGet, "", nil, nil).Code)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	forged := c
	forged.proof = other
	assert.Equal(t, http.StatusUnauthorized, forged.do(http.MethodGet, "", nil, nil).Code, "验签失败")
}

func TestOfficeCloseAndSweep(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	useFakeEngine(e, newFakeEngine(t, true, nil))
	a := mkDocx(t, e)
	sess, c := openWOPI(t, e, a.ID)
	require.Equal(t, http.StatusOK, c.put(docxV(t, 1), map[string]string{"X-COOL-WOPI-IsAutosave": "true"}).Code)

	require.NoError(t, e.s.CloseOffice(ctx, sess.ID))
	editing, err := e.s.Editing(ctx, a.ID)
	require.NoError(t, err)
	assert.False(t, editing, "关闭后 editing 立即为假")
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{putBytes(EntryDocx, docxV(t, 2))}})
	assert.Equal(t, CodeLocked, code(err), "宽限期内占用仍在")
	require.NoError(t, e.s.CloseOffice(ctx, sess.ID), "重复关闭不报错")
	require.NoError(t, e.s.CloseOffice(ctx, "aos_none"))

	// 宽限期内仍可接收退出保存（这里内容与草稿相同，仍只是工作副本）
	e.clk.add(10 * time.Second)
	assert.Equal(t, http.StatusOK, c.put(docxV(t, 1), map[string]string{"X-COOL-WOPI-IsAutosave": "true"}).Code)
	e.s.SweepOffice(ctx)
	assert.Equal(t, 1, mustGet(t, e, a.ID).Version, "宽限期内不释放")

	// 宽限期满：会话被删，草稿落版本（作者是用户、备注「编辑器自动保存」），占用释放
	e.clk.add(officeCloseGrace)
	assert.Equal(t, http.StatusUnauthorized, c.do(http.MethodGet, "", nil, nil).Code)
	e.s.SweepOffice(ctx)
	got := mustGet(t, e, a.ID)
	require.Equal(t, 2, got.Version)
	v, err := e.s.Version(ctx, a.ID, 2)
	require.NoError(t, err)
	assert.Empty(t, v.AuthorThreadID)
	assert.Equal(t, officeAutosaveNote, v.Note)
	assert.Equal(t, string(docxV(t, 1)), readFile(t, e.s, a.ID, 2, EntryDocx))
	_, err = e.st.Q.GetArtifactOfficeHold(ctx, a.ID)
	assert.Error(t, err, "占用已释放")
	_, err = e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 2, Files: []FileChange{putBytes(EntryDocx, docxV(t, 2))}})
	require.NoError(t, err, "释放后写入放开")

	// 没有草稿时释放不出版本
	sess, _ = openWOPI(t, e, a.ID)
	require.NoError(t, e.s.CloseOffice(ctx, sess.ID))
	e.clk.add(officeCloseGrace + time.Second)
	e.s.SweepOffice(ctx)
	assert.Equal(t, 3, mustGet(t, e, a.ID).Version)
	ed, err := e.s.Editing(ctx, a.ID)
	require.NoError(t, err)
	assert.False(t, ed)
}

func TestOfficeIdle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	useFakeEngine(e, newFakeEngine(t, true, nil))
	a := mkDocx(t, e)
	sess, c := openWOPI(t, e, a.ID)
	require.Equal(t, http.StatusOK, c.put(docxV(t, 5), map[string]string{"X-COOL-WOPI-IsAutosave": "true"}).Code)

	// 心跳维持占用
	e.clk.add(20 * time.Minute)
	require.NoError(t, e.s.BeatOffice(ctx, sess.ID))
	e.clk.add(20 * time.Minute)
	e.s.SweepOffice(ctx)
	assert.Equal(t, 1, mustGet(t, e, a.ID).Version, "最后一次活动不到 30 分钟")

	// 空闲 30 分钟：落版本并放开写入
	e.clk.add(10*time.Minute + time.Second)
	e.s.SweepOffice(ctx)
	assert.Equal(t, 2, mustGet(t, e, a.ID).Version)
	assert.Equal(t, CodeNotFound, code(e.s.BeatOffice(ctx, sess.ID)))
	_, err := e.s.RestoreVersion(ctx, a.ID, 1, 2, Author{})
	require.NoError(t, err, "释放后能恢复版本")
}

func TestOfficeEngineImportAndPDF(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := newFakeEngine(t, true, nil)
	f.out["docx"] = docxBytes(t)
	f.out["pdf"] = pdfBytes()
	useFakeEngine(e, f)

	a, err := e.s.Import(ctx, multipartOf(t, "old.doc", "\xd0\xcf\x11\xe0legacy"), ImportParams{})
	require.NoError(t, err)
	assert.Equal(t, TypeDocx, a.Type)
	assert.Equal(t, "old", a.Title)
	assert.Equal(t, OriginImport, a.Origin)
	assert.Equal(t, string(docxBytes(t)), readFile(t, e.s, a.ID, 1, EntryDocx))

	_, err = e.s.Import(ctx, multipartOf(t, "old.xls", "x"), ImportParams{})
	require.Equal(t, CodeBadOffice, code(err))
	assert.Contains(t, err.Error(), "conversion failed: bad input", "带引擎的原因")
	f.out["pptx"] = docxBytes(t)
	_, err = e.s.Import(ctx, multipartOf(t, "old.ppt", "x"), ImportParams{})
	require.Equal(t, CodeBadOffice, code(err), "转出来的内容不对")

	x, err := e.s.Create(ctx, CreateParams{Title: "表", Type: TypeXlsx, Files: []FileChange{putBytes(EntryXlsx, xlsxBytes(t))}})
	require.NoError(t, err)
	exp, err := e.s.Export(ctx, x.ID, 0, ExportPDF)
	require.NoError(t, err)
	assert.Equal(t, "表.pdf", exp.Name)
	assert.True(t, bytes.HasPrefix(exportBytes(t, exp), []byte("%PDF-")))
	exp, err = e.s.Export(ctx, a.ID, 0, ExportPDF)
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(exportBytes(t, exp), []byte("%PDF-")), "Word 也走引擎")

	// 没有转换能力时回到 M31 的行为
	e2 := newEnv(t)
	useFakeEngine(e2, newFakeEngine(t, false, nil))
	_, err = e2.s.Import(ctx, multipartOf(t, "a.doc", "x"), ImportParams{})
	assert.Equal(t, CodeUnsupportedFormat, code(err))
	x2, err := e2.s.Create(ctx, CreateParams{Title: "表", Type: TypeXlsx, Files: []FileChange{putBytes(EntryXlsx, xlsxBytes(t))}})
	require.NoError(t, err)
	_, err = e2.s.Export(ctx, x2.ID, 0, ExportPDF)
	assert.Contains(t, err.Error(), "需要 Office 引擎")
}

// 自动保存的工作副本还没落成版本时，内容清理把它当作引用；之后释放占用能把它落成版本。
func TestOfficeDraftSurvivesPurge(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	useFakeEngine(e, newFakeEngine(t, true, nil))
	a := mkDocx(t, e)
	_, c := openWOPI(t, e, a.ID)
	require.Equal(t, http.StatusOK, c.put(docxV(t, 7), map[string]string{"X-COOL-WOPI-IsAutosave": "true"}).Code)
	e.clk.add(2 * time.Hour)
	e.s.Purge(ctx)
	h, err := e.st.Q.GetArtifactOfficeHold(ctx, a.ID)
	require.NoError(t, err)
	require.NotNil(t, h.DraftSha)
	b, err := e.s.c.read(*h.DraftSha)
	require.NoError(t, err, "草稿没被清理")
	assert.Equal(t, docxV(t, 7), b)
	e.s.SweepOffice(ctx)
	assert.Equal(t, 2, mustGet(t, e, a.ID).Version)
	assert.Equal(t, string(docxV(t, 7)), readFile(t, e.s, a.ID, 2, EntryDocx))
}

// 编辑会话存在时 agent 的 update_artifact 回 locked；关闭编辑器（宽限期满、清扫释放占用）后重试成功。
func TestOfficeAgentUpdateLocked(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	useFakeEngine(e, newFakeEngine(t, true, nil))
	mkThread(t, e.st, "thr_a", "")
	c := fetchCaller("thr_a", map[string][]byte{"/w/a.docx": docxBytes(t), "/w/b.docx": docxV(t, 9)}, nil)
	id := textObj(t, mustCall(t, e.s, c, OpCreate, `{"title":"报告","type":"docx","files":[{"path":"document.docx","source":"/w/a.docx"}]}`))["id"].(string)
	sess, _ := openWOPI(t, e, id)
	update := `{"id":"` + id + `","baseVersion":1,"files":[{"path":"document.docx","source":"/w/b.docx"}]}`
	_, err := agentCall(t, e.s, c, OpUpdate, update)
	require.Equal(t, CodeLocked, code(err))
	assert.Contains(t, err.Error(), "用户正在编辑器里编辑这个文档")
	// 只改标题同样被占用挡住，标题不变
	rename := `{"id":"` + id + `","title":"改名"}`
	_, err = agentCall(t, e.s, c, OpUpdate, rename)
	require.Equal(t, CodeLocked, code(err))
	a, err := e.s.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "报告", a.Title)

	require.NoError(t, e.s.CloseOffice(ctx, sess.ID))
	e.clk.add(officeCloseGrace + time.Second)
	e.s.SweepOffice(ctx)
	out := textObj(t, mustCall(t, e.s, c, OpUpdate, update))
	assert.EqualValues(t, 2, out["version"])
	mustCall(t, e.s, c, OpUpdate, rename)
	a, err = e.s.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "改名", a.Title, "占用释放后改名成功")
}
