package attachments

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// fakeOrb 以 handler 充当 orbd 本机 API。
type fakeOrb struct {
	h      http.Handler
	usable error
}

func (f *fakeOrb) Do(_ context.Context, _ string, req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec.Result(), nil
}

func (f *fakeOrb) EnsureUsable(context.Context, string, string, bool, time.Duration) error {
	return f.usable
}

func newSvc(t *testing.T, orb OrbHTTP) (*Service, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	s, err := New(st, dir, orb)
	require.NoError(t, err)
	return s, st, dir
}

func mkThread(t *testing.T, st *store.Store, id string) {
	t.Helper()
	require.NoError(t, st.Q.CreateThread(context.Background(), db.CreateThreadParams{
		Kind: "work", ID: id, TitleSource: "auto", State: "idle", SessionDir: "~/.oryn/threads/" + id,
	}))
}

func TestSafeName(t *testing.T) {
	long := strings.Repeat("长", 100) + ".tar.gz" // 300 字节 + 扩展名
	for in, want := range map[string]string{
		"a/b/../report.pdf": "report.pdf",
		`..\..\x.txt`:       "_.._.._x.txt",
		".env":              "_.env",
		"":                  "_",
		"a\x00b\nc":         "a_b_c",
		"bad\xffutf8":       "bad_utf8",
	} {
		assert.Equal(t, want, SafeName(in), "%q", in)
	}
	got := SafeName(long)
	assert.LessOrEqual(t, len(got), maxName)
	assert.True(t, strings.HasSuffix(got, ".gz"))
	assert.True(t, strings.HasPrefix(got, "长"))
	// 恒等：orbd 对已净化的名字不再改写，投递路径与控制面记录的名字一致
	for _, n := range []string{got, "report.pdf", "_.env", "_.._.._x.txt"} {
		assert.Equal(t, n, SafeName(n))
	}
}

func TestUploadOpenAndMime(t *testing.T) {
	s, _, dir := newSvc(t, &fakeOrb{})
	ctx := context.Background()
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 600)...)
	a, err := s.Upload(ctx, "dir/shot.png", "", bytes.NewReader(png))
	require.NoError(t, err)
	sum := sha256.Sum256(png)
	assert.Equal(t, hex.EncodeToString(sum[:]), a.Sha256)
	assert.Equal(t, "image/png", a.Mime)
	assert.Equal(t, "shot.png", a.Name)
	assert.True(t, ValidID(a.ID))

	// 嗅探不出时按扩展名
	b, err := s.Upload(ctx, "doc.pdf", "", bytes.NewReader([]byte{0, 1, 2, 3}))
	require.NoError(t, err)
	assert.Equal(t, "application/pdf", b.Mime)

	f, row, err := s.Open(ctx, a.ID)
	require.NoError(t, err)
	got, _ := io.ReadAll(f)
	_ = f.Close()
	assert.Equal(t, png, got)
	assert.Equal(t, a, row)

	_, err = s.Upload(ctx, "x", "T-missing", bytes.NewReader(nil))
	assert.Equal(t, CodeThreadNotFound, err.(*Error).Code)

	// 超过 100MiB：too_large，且不留临时文件与行
	_, err = s.Upload(ctx, "big.bin", "", io.LimitReader(zeros{}, MaxBytes+1))
	var ae *Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, CodeTooLarge, ae.Code)
	tmp, _ := os.ReadDir(filepath.Join(dir, "attachments", ".tmp"))
	assert.Empty(t, tmp)
	files, _ := os.ReadDir(filepath.Join(dir, "attachments"))
	assert.Len(t, files, 3, ".tmp + 两个附件")
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestDeliverBindsAndUploadsMissing(t *testing.T) {
	var puts []string
	files := map[string][]byte{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /attachments/ensure", func(w http.ResponseWriter, r *http.Request) {
		var req apigen.OrbAttachmentEnsureRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		p := "/home/user/.oryn/attachments/" + req.ThreadID + "/" + req.ID + "/" + req.Name
		_, ok := files[p]
		_ = json.NewEncoder(w).Encode(apigen.OrbAttachmentEnsureResult{Present: ok, Path: p})
	})
	mux.HandleFunc("PUT /attachments/file", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		b, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(b)
		require.Equal(t, q.Get("sha256"), hex.EncodeToString(sum[:]))
		p := "/home/user/.oryn/attachments/" + q.Get("threadId") + "/" + q.Get("id") + "/" + q.Get("name")
		files[p] = b
		puts = append(puts, q.Get("id"))
		_ = json.NewEncoder(w).Encode(apigen.OrbAttachmentEnsureResult{Present: true, Path: p})
	})
	orb := &fakeOrb{h: mux}
	s, st, _ := newSvc(t, orb)
	ctx := context.Background()
	mkThread(t, st, "T-a")
	mkThread(t, st, "T-b")
	a, err := s.Upload(ctx, "notes.txt", "", strings.NewReader("hello"))
	require.NoError(t, err)

	d, err := s.Deliver(ctx, "orb-x", "T-a", []string{a.ID})
	require.NoError(t, err)
	require.Len(t, d, 1)
	assert.Equal(t, "/home/user/.oryn/attachments/T-a/"+a.ID+"/notes.txt", d[0].Path)
	assert.Equal(t, int64(5), d[0].Size)
	assert.Equal(t, []string{a.ID}, puts)
	row, err := s.Get(ctx, a.ID)
	require.NoError(t, err)
	require.NotNil(t, row.ThreadID)
	assert.Equal(t, "T-a", *row.ThreadID)

	// 再投递：已在 orb 内，不重传
	_, err = s.Deliver(ctx, "orb-x", "T-a", []string{a.ID})
	require.NoError(t, err)
	assert.Len(t, puts, 1)

	// 已属于 T-a 的附件不能投递到 T-b
	_, err = s.Deliver(ctx, "orb-x", "T-b", []string{a.ID})
	var ae *Error
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, CodeAttachNotFound, ae.Code)
	assert.Error(t, s.CheckUnbound(ctx, "", []string{a.ID}), "新线程只能用未绑定的附件")

	// orb 不可用（旧 orbd）：明确的错误码，而不是悄悄丢弃
	orb.usable = orbs.ErrOrbdOutdated
	_, err = s.Deliver(ctx, "orb-x", "T-a", []string{a.ID})
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, CodeOrbdOutdated, ae.Code)
}

func TestGC(t *testing.T) {
	s, st, dir := newSvc(t, &fakeOrb{})
	ctx := context.Background()
	mkThread(t, st, "T-a")
	now := time.Now()
	s.now = func() time.Time { return now.Add(-25 * time.Hour) }
	old, err := s.Upload(ctx, "old.txt", "", strings.NewReader("old"))
	require.NoError(t, err)
	bound, err := s.Upload(ctx, "bound.txt", "T-a", strings.NewReader("bound"))
	require.NoError(t, err)
	s.now = func() time.Time { return now }
	fresh, err := s.Upload(ctx, "fresh.txt", "", strings.NewReader("fresh"))
	require.NoError(t, err)
	// 没有行的孤儿文件：足够旧才删（上传先 rename 再插行）
	orphanOld := filepath.Join(dir, "attachments", "att_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	orphanNew := filepath.Join(dir, "attachments", "att_01ARZ3NDEKTSV4RRFFQ69G5FAW")
	require.NoError(t, os.WriteFile(orphanOld, []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(orphanNew, []byte("x"), 0o600))
	require.NoError(t, os.Chtimes(orphanOld, now.Add(-2*time.Hour), now.Add(-2*time.Hour)))

	s.GC(ctx)

	_, err = s.Get(ctx, old.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.NoFileExists(t, s.path(old.ID))
	for _, id := range []string{bound.ID, fresh.ID} {
		_, err = s.Get(ctx, id)
		assert.NoError(t, err)
		assert.FileExists(t, s.path(id))
	}
	assert.NoFileExists(t, orphanOld)
	assert.FileExists(t, orphanNew)

	// 删除线程的全部附件
	require.NoError(t, s.DeleteThread(ctx, "T-a"))
	assert.NoFileExists(t, s.path(bound.ID))
	list, err := s.List(ctx, "T-a")
	require.NoError(t, err)
	assert.Empty(t, list)
}
