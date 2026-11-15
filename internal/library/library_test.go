package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func newSvc(t *testing.T) (*Service, *store.Store, *attachments.Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	att, err := attachments.New(st, dir, nil) // 只用 Upload / Open，不触达 orb
	require.NoError(t, err)
	s, err := New(st, dir, att)
	require.NoError(t, err)
	return s, st, att, dir
}

func mkThread(t *testing.T, st *store.Store, id string) {
	t.Helper()
	require.NoError(t, st.Q.CreateThread(context.Background(), db.CreateThreadParams{
		Kind: "work", ID: id, TitleSource: "auto", State: "idle", SessionDir: "~/.oryn/threads/" + id,
	}))
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func readAll(t *testing.T, s *Service, id string) []byte {
	t.Helper()
	rc, _, err := s.Open(context.Background(), id)
	require.NoError(t, err)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	return b
}

func listIDs(t *testing.T, s *Service, f Filter) []string {
	t.Helper()
	list, err := s.List(context.Background(), f)
	require.NoError(t, err)
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = x.ID
	}
	return out
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestUploadListScopeDelete(t *testing.T) {
	ctx := context.Background()
	s, st, _, dir := newSvc(t)
	mkThread(t, st, "T-1")
	mkThread(t, st, "T-2")

	content := []byte("hello library\n")
	up, err := s.Upload(ctx, "../x/.notes.txt", "T-1", bytes.NewReader(content))
	require.NoError(t, err)
	assert.True(t, ValidID(up.ID))
	assert.Equal(t, "_.notes.txt", up.Name, "文件名经 attachments.SafeName 净化")
	assert.Equal(t, "text/plain; charset=utf-8", up.Mime)
	assert.EqualValues(t, len(content), up.Size)
	assert.Equal(t, sum(content), up.Sha256)
	assert.Equal(t, ScopeConversation, up.Scope)
	assert.Equal(t, "T-1", up.ThreadID)
	assert.Equal(t, SourceUpload, up.Source)
	assert.False(t, up.Bound)
	fi, err := os.Stat(filepath.Join(dir, "assistant-files"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())

	got, err := s.Get(ctx, up.ID)
	require.NoError(t, err)
	assert.Equal(t, up, got)
	assert.Equal(t, content, readAll(t, s, up.ID))

	assert.Empty(t, listIDs(t, s, Filter{}), "未绑定的文件默认不列出")
	assert.Equal(t, []string{up.ID}, listIDs(t, s, Filter{IncludeUnbound: true}))
	require.NoError(t, s.Bind(ctx, "T-1", []string{up.ID}))

	shared, err := s.Put(ctx, PutParams{
		Name: "report.md", Scope: ScopeShared, Source: SourceOrb, SourceThreadID: "T-9", R: strings.NewReader("# r"),
	})
	require.NoError(t, err)
	assert.True(t, shared.Bound, "Put 的文件直接视为已绑定")
	assert.Equal(t, "T-9", shared.SourceThreadID)
	assert.Empty(t, shared.ThreadID)

	assert.Equal(t, []string{shared.ID, up.ID}, listIDs(t, s, Filter{}), "按创建时间倒序")
	assert.Equal(t, []string{shared.ID, up.ID}, listIDs(t, s, Filter{ThreadID: "T-1"}), "所属对话可见自己的文件与共享文件")
	assert.Equal(t, []string{shared.ID}, listIDs(t, s, Filter{ThreadID: "T-2"}), "别的对话只见共享文件")
	assert.Equal(t, []string{up.ID}, listIDs(t, s, Filter{Scope: ScopeConversation}))
	assert.Equal(t, []string{shared.ID}, listIDs(t, s, Filter{Scope: ScopeShared}))
	_, err = s.List(ctx, Filter{Scope: "public"})
	require.ErrorIs(t, err, ErrInvalid)

	require.NoError(t, s.SetScope(ctx, up.ID, ScopeShared))
	assert.Equal(t, []string{shared.ID, up.ID}, listIDs(t, s, Filter{ThreadID: "T-2"}), "切换为共享后别的对话可见")
	require.ErrorIs(t, s.SetScope(ctx, up.ID, "public"), ErrInvalid)
	require.ErrorIs(t, s.SetScope(ctx, ids.AssistantFile(), ScopeShared), ErrNotFound)

	require.NoError(t, s.Delete(ctx, up.ID))
	_, err = s.Get(ctx, up.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = s.Open(ctx, up.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = os.Stat(filepath.Join(dir, "assistant-files", up.ID))
	assert.ErrorIs(t, err, os.ErrNotExist, "字节随行一起删除")
	require.ErrorIs(t, s.Delete(ctx, up.ID), ErrNotFound)
	assert.Equal(t, []string{shared.ID}, listIDs(t, s, Filter{}))

	_, err = s.Upload(ctx, "a.txt", "T-404", strings.NewReader("x"))
	require.ErrorIs(t, err, ErrThreadNotFound)
	_, err = s.Upload(ctx, "  ", "", strings.NewReader("x"))
	require.ErrorIs(t, err, ErrInvalid)
	_, err = s.Put(ctx, PutParams{Name: "a", Source: "web", R: strings.NewReader("x")})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = s.Get(ctx, "att_01J00000000000000000000000")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestUploadTooLarge(t *testing.T) {
	ctx := context.Background()
	s, _, _, dir := newSvc(t)
	_, err := s.Upload(ctx, "big.bin", "", io.LimitReader(zeros{}, attachments.MaxBytes+1))
	require.ErrorIs(t, err, ErrTooLarge)
	tmp, err := os.ReadDir(filepath.Join(dir, "assistant-files", ".tmp"))
	require.NoError(t, err)
	assert.Empty(t, tmp, "不留临时文件")
	assert.Empty(t, listIDs(t, s, Filter{IncludeUnbound: true}), "不留行")
}

// TestThreadDeleteKeepsFile：线程是硬删除，所属对话删除后文件留在库里、thread_id 置空。
func TestThreadDeleteKeepsFile(t *testing.T) {
	ctx := context.Background()
	s, st, _, _ := newSvc(t)
	mkThread(t, st, "T-1")
	f, err := s.Put(ctx, PutParams{Name: "a.txt", ThreadID: "T-1", Source: SourceAssistant, R: strings.NewReader("a")})
	require.NoError(t, err)
	n, err := st.Q.DeleteThread(ctx, "T-1")
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	got, err := s.Get(ctx, f.ID)
	require.NoError(t, err)
	assert.Empty(t, got.ThreadID)
	assert.True(t, got.Bound)
	assert.Equal(t, []byte("a"), readAll(t, s, f.ID))
}

func TestBindRules(t *testing.T) {
	ctx := context.Background()
	s, st, _, _ := newSvc(t)
	for _, id := range []string{"T-1", "T-2", "T-3"} {
		mkThread(t, st, id)
	}
	up := func(threadID string) File {
		f, err := s.Upload(ctx, "f.txt", threadID, strings.NewReader("x"))
		require.NoError(t, err)
		return f
	}

	own := up("T-1")
	require.ErrorIs(t, s.Bind(ctx, "T-2", []string{own.ID}), ErrForbidden, "属于别的对话且未共享")
	require.NoError(t, s.Bind(ctx, "T-1", []string{own.ID}))
	require.NoError(t, s.Bind(ctx, "T-1", []string{own.ID}), "重复绑定到同一对话是幂等的")
	got, _ := s.Get(ctx, own.ID)
	assert.True(t, got.Bound)
	assert.Equal(t, "T-1", got.ThreadID)

	orphan := up("")
	require.NoError(t, s.Bind(ctx, "T-2", []string{orphan.ID}))
	got, _ = s.Get(ctx, orphan.ID)
	assert.True(t, got.Bound)
	assert.Equal(t, "T-2", got.ThreadID, "thread_id 为空时填上")

	require.NoError(t, s.SetScope(ctx, own.ID, ScopeShared))
	require.NoError(t, s.Bind(ctx, "T-3", []string{own.ID}), "共享文件可被任意对话引用")
	got, _ = s.Get(ctx, own.ID)
	assert.Equal(t, "T-1", got.ThreadID, "已有所属对话时不改")

	// 一个失败则整批不绑定
	fresh := up("")
	other := up("T-1")
	require.ErrorIs(t, s.Bind(ctx, "T-3", []string{fresh.ID, other.ID}), ErrForbidden)
	got, _ = s.Get(ctx, fresh.ID)
	assert.False(t, got.Bound)
	assert.Empty(t, got.ThreadID)

	require.ErrorIs(t, s.Bind(ctx, "T-3", []string{ids.AssistantFile()}), ErrNotFound)
	require.ErrorIs(t, s.Bind(ctx, "T-3", []string{"nope"}), ErrNotFound)
	require.ErrorIs(t, s.Bind(ctx, "T-404", []string{fresh.ID}), ErrThreadNotFound)
	require.ErrorIs(t, s.Bind(ctx, "", []string{fresh.ID}), ErrInvalid)
}

func TestCopyToAttachment(t *testing.T) {
	ctx := context.Background()
	s, st, att, _ := newSvc(t)
	mkThread(t, st, "T-1")
	mkThread(t, st, "T-2")
	content := []byte("%PDF-1.4 fake")
	f, err := s.Put(ctx, PutParams{Name: "doc.pdf", ThreadID: "T-1", Source: SourceAssistant, R: bytes.NewReader(content)})
	require.NoError(t, err)

	attID, err := s.CopyToAttachment(ctx, f.ID, "T-2")
	require.NoError(t, err)
	assert.True(t, attachments.ValidID(attID), "新 att_ 行")
	rc, a, err := att.Open(ctx, attID)
	require.NoError(t, err)
	b, err := io.ReadAll(rc)
	_ = rc.Close()
	require.NoError(t, err)
	assert.Equal(t, content, b)
	require.NotNil(t, a.ThreadID)
	assert.Equal(t, "T-2", *a.ThreadID, "绑定到目标线程")
	assert.Equal(t, f.Sha256, a.Sha256)
	assert.Equal(t, f.Name, a.Name)
	assert.Equal(t, f.Mime, a.Mime)

	second, err := s.CopyToAttachment(ctx, f.ID, "T-2")
	require.NoError(t, err)
	assert.NotEqual(t, attID, second, "每次复制都是新附件")

	require.NoError(t, s.Delete(ctx, f.ID))
	_, _, err = att.Open(ctx, attID)
	require.NoError(t, err, "删除库文件不影响已复制的附件")

	g, err := s.Put(ctx, PutParams{Name: "x", Source: SourceOrb, R: strings.NewReader("x")})
	require.NoError(t, err)
	_, err = s.CopyToAttachment(ctx, g.ID, "T-404")
	require.ErrorIs(t, err, ErrThreadNotFound)
	_, err = s.CopyToAttachment(ctx, g.ID, "")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = s.CopyToAttachment(ctx, ids.AssistantFile(), "T-2")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestGC(t *testing.T) {
	ctx := context.Background()
	s, st, _, dir := newSvc(t)
	mkThread(t, st, "T-1")
	now := time.Now()
	fdir := filepath.Join(dir, "assistant-files")

	s.now = func() time.Time { return now.Add(-25 * time.Hour) }
	oldUnbound, err := s.Upload(ctx, "a", "T-1", strings.NewReader("a"))
	require.NoError(t, err)
	oldBound, err := s.Upload(ctx, "b", "", strings.NewReader("b"))
	require.NoError(t, err)
	require.NoError(t, s.Bind(ctx, "T-1", []string{oldBound.ID}))
	oldPut, err := s.Put(ctx, PutParams{Name: "c", Scope: ScopeShared, Source: SourceOrb, R: strings.NewReader("c")})
	require.NoError(t, err)
	s.now = func() time.Time { return now.Add(-23 * time.Hour) }
	freshUnbound, err := s.Upload(ctx, "d", "", strings.NewReader("d"))
	require.NoError(t, err)
	s.now = time.Now

	// 没有行的文件：旧的删，新的（可能正处在“先 rename 再插行”的窗口）留
	oldOrphan, freshOrphan := ids.AssistantFile(), ids.AssistantFile()
	stale := now.Add(-2 * time.Hour)
	for _, id := range []string{oldOrphan, freshOrphan} {
		require.NoError(t, os.WriteFile(filepath.Join(fdir, id), []byte("o"), 0o600))
	}
	require.NoError(t, os.Chtimes(filepath.Join(fdir, oldOrphan), stale, stale))
	oldTmp := filepath.Join(fdir, ".tmp", "up-1")
	require.NoError(t, os.WriteFile(oldTmp, []byte("t"), 0o600))
	require.NoError(t, os.Chtimes(oldTmp, stale, stale))

	s.GC(ctx, now)

	_, err = s.Get(ctx, oldUnbound.ID)
	require.ErrorIs(t, err, ErrNotFound, "未绑定超过 24h 被回收")
	_, err = os.Stat(filepath.Join(fdir, oldUnbound.ID))
	assert.ErrorIs(t, err, os.ErrNotExist)
	for _, f := range []File{oldBound, oldPut, freshUnbound} {
		_, err = s.Get(ctx, f.ID)
		require.NoError(t, err, "已绑定或不足 24h 的保留：%s", f.Name)
		_, err = os.Stat(filepath.Join(fdir, f.ID))
		require.NoError(t, err)
	}
	_, err = os.Stat(filepath.Join(fdir, oldOrphan))
	assert.ErrorIs(t, err, os.ErrNotExist, "旧孤儿文件被删")
	_, err = os.Stat(filepath.Join(fdir, freshOrphan))
	assert.NoError(t, err, "新孤儿文件保留")
	_, err = os.Stat(oldTmp)
	assert.ErrorIs(t, err, os.ErrNotExist, "旧临时文件被删")
}
