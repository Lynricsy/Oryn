package share_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/realtime"
	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

var update = flag.Bool("update", false, "重写 testdata 下的金文件")

// ---- 假依赖 ----

type fakeThreads struct {
	mu        sync.Mutex
	published []string
	blobs     map[string]string // sha → 文件路径
}

func (f *fakeThreads) Publish(_ context.Context, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, id)
}

func (f *fakeThreads) Blob(_ context.Context, sha string) (db.Blob, string, error) {
	p, ok := f.blobs[sha]
	if !ok {
		return db.Blob{}, "", threads.ErrNotFound
	}
	return db.Blob{Sha256: sha, Mime: "image/png", CreatedAt: 1}, p, nil
}

func (f *fakeThreads) publishCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.published {
		if p == id {
			n++
		}
	}
	return n
}

type fakeHub struct {
	mu     sync.Mutex
	closed []string
}

func (h *fakeHub) ServePublic(w http.ResponseWriter, _ *http.Request, _ realtime.PublicSub, _ int64, _ *int64) {
	w.WriteHeader(http.StatusTeapot)
}

func (h *fakeHub) ClosePublic(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = append(h.closed, id)
}

// fakeOrbs 模拟 orbd 本机 API 的 /fs/stat 与 /fs/file。
type fakeOrbs struct {
	mu        sync.Mutex
	files     map[string][]byte
	dirs      map[string]bool
	sizes     map[string]int64 // 覆盖 stat 报告的大小
	ensureErr error
}

func (f *fakeOrbs) EnsureUsable(context.Context, string, string, bool, time.Duration) error {
	return f.ensureErr
}

func (f *fakeOrbs) Do(_ context.Context, _ string, req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := httptest.NewRecorder()
	p := req.URL.Query().Get("path")
	data, isFile := f.files[p]
	switch {
	case req.URL.Path == "/fs/stat" && f.dirs[p]:
		_ = json.NewEncoder(rec).Encode(apigen.FsEntry{Name: filepath.Base(p), Path: p, Type: apigen.FsEntryTypeDir})
	case req.URL.Path == "/fs/stat" && isFile:
		size := int64(len(data))
		if n, ok := f.sizes[p]; ok {
			size = n
		}
		_ = json.NewEncoder(rec).Encode(apigen.FsEntry{Name: filepath.Base(p), Path: p, Type: apigen.FsEntryTypeFile, Size: size})
	case req.URL.Path == "/fs/file" && isFile:
		_, _ = rec.Write(data)
	default:
		rec.WriteHeader(http.StatusNotFound)
		_, _ = rec.WriteString(`{"code":"not_found","message":"no such file"}`)
	}
	return rec.Result(), nil
}

type fakeSecrets map[string]string

func (f fakeSecrets) All(context.Context) (map[string]string, error) { return f, nil }

// ---- 夹具 ----

var shareTime = time.Date(2026, 11, 13, 0, 30, 0, 0, time.UTC)

type env struct {
	t    *testing.T
	ctx  context.Context
	st   *store.Store
	svc  *share.Service
	th   *fakeThreads
	hub  *fakeHub
	att  *attachments.Service
	orbs *fakeOrbs
	dir  string
	now  time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(dir, "data"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	orbs := &fakeOrbs{files: map[string][]byte{}, dirs: map[string]bool{}, sizes: map[string]int64{}}
	att, err := attachments.New(st, filepath.Join(dir, "data"), orbs)
	require.NoError(t, err)
	e := &env{t: t, ctx: ctx, st: st, th: &fakeThreads{blobs: map[string]string{}}, hub: &fakeHub{}, att: att, orbs: orbs, dir: dir, now: shareTime}
	secs := fakeSecrets{"db/password": "correct-horse-battery", "short": "abc"}
	e.svc = share.New(share.Config{PublicURL: "https://oryn.example.com/", Now: func() time.Time { return e.now }}, st, e.th, e.hub, att, orbs, secs)
	return e
}

func (e *env) thread(id, title string, orbID *string) {
	e.t.Helper()
	require.NoError(e.t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{
		Kind: "work", ID: id, OrbID: orbID, Title: title, TitleSource: "auto", State: "idle", SessionDir: "/home/user/.oryn/threads/" + id,
		CreatedAt: shareTime.Add(-time.Hour).UnixMilli(), UpdatedAt: 1,
	}))
}

func (e *env) orb(id string) *string {
	e.t.Helper()
	_, err := e.st.DB.Exec(`INSERT OR IGNORE INTO providers (id, name, token_hash, created_at) VALUES ('prov_1', 'local', 'ph', 1)`)
	require.NoError(e.t, err)
	_, err = e.st.DB.Exec(`INSERT INTO orbs (id, provider_id, type, cpus, memory_mb, image, state, state_changed_at, token_hash, created_at)
		VALUES (?, 'prov_1', 'small', 2, 4096, 'img', 'ready', 1, 'h-'||?, 1)`, id, id)
	require.NoError(e.t, err)
	return &id
}

// add 追加一条条目并返回其 seq；parent 为空表示无父。
func (e *env) add(thread, file, id, parent, typ string, payload map[string]any) int64 {
	e.t.Helper()
	payload["type"] = typ
	payload["id"] = id
	var pp *string
	if parent != "" {
		payload["parentId"] = parent
		pp = &parent
	}
	b, err := json.Marshal(payload)
	require.NoError(e.t, err)
	seq, err := e.st.Q.UpsertEntry(e.ctx, db.UpsertEntryParams{ThreadID: thread, File: file, EntryID: id, ParentID: pp, Type: typ, Payload: b})
	require.NoError(e.t, err)
	return seq
}

func msg(role, text string) map[string]any {
	return map[string]any{"message": map[string]any{"role": role, "content": []any{map[string]any{"type": "text", "text": text}}}}
}

func (e *env) leaf(thread, id string) {
	e.t.Helper()
	require.NoError(e.t, e.st.Q.SetThreadLeaf(e.ctx, db.SetThreadLeafParams{LeafEntryID: &id, ID: thread}))
}

// allEntries 以匿名分页读完整个范围（每页 limit 条），断言 seq 严格递增且不越过 lastSeq。
func (e *env) allEntries(slug string, limit int) []map[string]any {
	e.t.Helper()
	var out []map[string]any
	after := int64(0)
	for range 1000 {
		page, err := e.svc.PublicEntries(e.ctx, slug, after, limit)
		require.NoError(e.t, err)
		assert.LessOrEqual(e.t, len(page.Entries), limit)
		for _, en := range page.Entries {
			require.Greater(e.t, en.Seq, after)
			require.LessOrEqual(e.t, en.Seq, page.LastSeq, "不越过范围上界")
			after = en.Seq
			var m map[string]any
			b, _ := json.Marshal(en)
			require.NoError(e.t, json.Unmarshal(b, &m))
			out = append(out, m)
		}
		if !page.HasMore {
			return out
		}
	}
	e.t.Fatal("分页没有结束")
	return nil
}

func ids(es []map[string]any) []string {
	var out []string
	for _, en := range es {
		out = append(out, en["entryId"].(string))
	}
	return out
}

func payloadOf(en map[string]any) map[string]any { return en["payload"].(map[string]any) }

const tid = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"

// branchy 构造三段分支：主干 u1→a1，旁支 a1→u2→a2（被回退），新分支 a1→cm→u3→a3（当前叶子），
// 另有隐藏类型、display:false 的条目在链上，以及一个子 agent 文件。
func (e *env) branchy() {
	e.thread(tid, "Fix the build", nil)
	e.add(tid, "s1.jsonl", "u1", "", "message", msg("user", "trunk question"))
	e.add(tid, "s1.jsonl", "a1", "u1", "message", msg("assistant", "trunk answer"))
	e.add(tid, "s1.jsonl", "u2", "a1", "message", msg("user", "side question ZZ-SIDE sk-sidesidesidesideside1234"))
	e.add(tid, "s1.jsonl", "a2", "u2", "message", msg("assistant", "side answer"))
	e.add(tid, "s1.jsonl", "cm", "a1", "custom_message", map[string]any{"customType": "x", "content": "hidden injected", "display": false})
	e.add(tid, "s1.jsonl", "hk", "cm", "message", map[string]any{"message": map[string]any{"role": "hookMessage", "content": "hook secret", "display": false}})
	e.add(tid, "s1.jsonl", "lb", "hk", "label", map[string]any{"label": "internal label ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"})
	e.add(tid, "s1.jsonl", "u3", "lb", "message", msg("user", "new branch question"))
	e.add(tid, "s1/task-1.jsonl", "sa", "", "message", msg("assistant", "subagent secret sk-subsubsubsubsubsubsub1234"))
	e.add(tid, "s1.jsonl", "a3", "u3", "message", msg("assistant", "new branch answer"))
	e.leaf(tid, "a3")
}

// ---- 范围 ----

func TestScopeSnapshotAndLive(t *testing.T) {
	e := newEnv(t)
	e.branchy()
	snap, err := e.svc.Create(e.ctx, tid, apigen.ShareModeSnapshot, false)
	require.NoError(t, err)
	live, err := e.svc.Create(e.ctx, tid, apigen.ShareModeLive, false)
	require.NoError(t, err)

	want := []string{"u1", "a1", "cm", "hk", "lb", "u3", "a3"}
	got := e.allEntries(snap.Slug, 2)
	assert.Equal(t, want, ids(got), "快照只含创建时的活动分支，隐藏条目仍在链上")
	for _, en := range got {
		switch en["entryId"] {
		case "cm", "hk", "lb":
			assert.Equal(t, map[string]any{"type": en["type"]}, payloadOf(en), "隐藏 / display:false 条目的内容被清空")
		case "u3":
			assert.Contains(t, fmt.Sprint(payloadOf(en)), "new branch question")
		}
		assert.NotContains(t, en["file"], "/", "子 agent 文件不出控制面")
	}

	// 叶子之后的新条目：快照不变，实时跟随
	e.add(tid, "s1.jsonl", "u4", "a3", "message", msg("user", "after share"))
	e.add(tid, "s1.jsonl", "a4", "u4", "message", msg("assistant", "after share answer"))
	e.leaf(tid, "a4")
	assert.Equal(t, want, ids(e.allEntries(snap.Slug, 3)))
	assert.Equal(t, append(want, "u4", "a4"), ids(e.allEntries(live.Slug, 500)))

	// 实时随叶子变化：切回旁支
	e.leaf(tid, "a2")
	assert.Equal(t, []string{"u1", "a1", "u2", "a2"}, ids(e.allEntries(live.Slug, 1)))
	assert.Equal(t, want, ids(e.allEntries(snap.Slug, 500)))

	ps, err := e.svc.PublicShare(e.ctx, snap.Slug)
	require.NoError(t, err)
	require.NotNil(t, snap.UntilSeq)
	assert.Equal(t, *snap.UntilSeq, ps.LastSeq)
	assert.Nil(t, ps.State, "快照不带状态")
	assert.Equal(t, "a3", *ps.LeafEntryID)
	lps, err := e.svc.PublicShare(e.ctx, live.Slug)
	require.NoError(t, err)
	require.NotNil(t, lps.State)
	assert.Equal(t, apigen.ThreadState("idle"), *lps.State)
	assert.Equal(t, "a2", *lps.LeafEntryID)
}

// ---- 扫描与创建 ----

func TestCreateScan(t *testing.T) {
	e := newEnv(t)
	e.branchy()
	// 当前活动分支上的两处密钥：vault 值与 sk- 串；旁支、隐藏条目与子 agent 里的不算
	e.add(tid, "s1.jsonl", "u5", "a3", "message", msg("user", "db pass is correct-horse-battery and short abc"))
	e.add(tid, "s1.jsonl", "a5", "u5", "message", msg("assistant", "here: sk-test-abcdefghijklmnopqrstuvwx0123 and sk-test-abcdefghijklmnopqrstuvwx9999"))
	e.leaf(tid, "a5")

	_, err := e.svc.Create(e.ctx, tid, apigen.ShareModeSnapshot, false)
	require.ErrorIs(t, err, share.ErrSecretsFound)
	var se *share.ScanError
	require.ErrorAs(t, err, &se)
	assert.False(t, se.Scan.Truncated)
	assert.Equal(t, 9, se.Scan.Scanned)
	require.Len(t, se.Scan.Findings, 2)
	v := se.Scan.Findings[0]
	assert.Equal(t, apigen.ShareFindingRuleVaultSecret, v.Rule)
	require.NotNil(t, v.SecretName)
	assert.Equal(t, "db/password", *v.SecretName)
	assert.Equal(t, "u5", v.EntryID)
	assert.Equal(t, "user", *v.Role)
	k := se.Scan.Findings[1]
	assert.Equal(t, apigen.ShareFindingRuleAPIKeySk, k.Rule)
	assert.Equal(t, 2, k.Count)
	assert.Equal(t, "sk-t…23", k.Preview)
	b, _ := json.Marshal(se.Scan)
	assert.NotContains(t, string(b), "abcdefghijklmnop", "预览已脱敏")
	assert.NotContains(t, string(b), "correct-horse")
	assert.NotContains(t, string(b), "sidesidesideside")
	assert.NotContains(t, string(b), "subsubsubsub")
	assert.NotContains(t, string(b), "ghp_")
	assert.Zero(t, e.th.publishCount(tid), "被拒时不写入、不推送")
	n, err := e.st.Q.CountActiveThreadShares(e.ctx, tid)
	require.NoError(t, err)
	assert.Zero(t, n)

	sh, err := e.svc.Create(e.ctx, tid, apigen.ShareModeSnapshot, true)
	require.NoError(t, err)
	assert.Equal(t, 2, sh.Findings)
	assert.Regexp(t, `^[0-9a-f]{32}$`, sh.Slug)
	assert.Regexp(t, `^shr_[0-9A-Z]{26}$`, sh.ID)
	assert.Equal(t, "https://oryn.example.com/s/"+sh.Slug, sh.URL)
	assert.Equal(t, "Fix the build", sh.ThreadTitle)
	row, err := e.st.Q.GetThread(e.ctx, tid)
	require.NoError(t, err)
	require.NotNil(t, sh.UntilSeq)
	assert.Equal(t, row.LastSeq, *sh.UntilSeq, "快照的 until_seq 等于创建时的 last_seq")
	dbRow, err := e.st.Q.GetThreadShare(e.ctx, sh.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, dbRow.Findings)
	assert.Equal(t, "a5", *dbRow.LeafEntryID)
	assert.Equal(t, 1, e.th.publishCount(tid), "创建后推送 index.patch")

	lv, err := e.svc.Create(e.ctx, tid, apigen.ShareModeLive, true)
	require.NoError(t, err)
	assert.Nil(t, lv.UntilSeq)
	dbLive, err := e.st.Q.GetThreadShare(e.ctx, lv.ID)
	require.NoError(t, err)
	assert.Nil(t, dbLive.UntilSeq)
	assert.Nil(t, dbLive.LeafEntryID)

	_, err = e.svc.Create(e.ctx, tid, "forever", true)
	var shErr *share.Error
	require.ErrorAs(t, err, &shErr)
	assert.Equal(t, http.StatusBadRequest, shErr.Status)
	_, err = e.svc.Create(e.ctx, "T-nope", apigen.ShareModeLive, true)
	assert.ErrorIs(t, err, share.ErrNotFound)
}

func TestCreateLimitAndTruncation(t *testing.T) {
	e := newEnv(t)
	e.thread(tid, "", nil)
	parent := ""
	for i := range 210 {
		id := fmt.Sprintf("m%03d", i)
		e.add(tid, "s1.jsonl", id, parent, "message", msg("assistant", fmt.Sprintf("key AKIAABCDEFGHIJKL%04d", i)))
		parent = id
	}
	e.leaf(tid, parent)
	_, err := e.svc.Create(e.ctx, tid, apigen.ShareModeSnapshot, false)
	var se *share.ScanError
	require.ErrorAs(t, err, &se)
	assert.Len(t, se.Scan.Findings, share.MaxFindings)
	assert.True(t, se.Scan.Truncated)
	assert.Equal(t, 210, se.Scan.Scanned)

	for range share.MaxSharesPerThread {
		_, err := e.svc.Create(e.ctx, tid, apigen.ShareModeLive, true)
		require.NoError(t, err)
	}
	_, err = e.svc.Create(e.ctx, tid, apigen.ShareModeLive, true)
	var shErr *share.Error
	require.ErrorAs(t, err, &shErr, "第 21 个被拒")
	assert.Equal(t, http.StatusConflict, shErr.Status)
	assert.Equal(t, "invalid", shErr.Code)

	list, err := e.svc.List(e.ctx, tid)
	require.NoError(t, err)
	require.Len(t, list, share.MaxSharesPerThread)
	require.NoError(t, e.svc.Revoke(e.ctx, list[0].ID))
	assert.Equal(t, []string{list[0].ID}, e.hub.closed)
	_, err = e.svc.Create(e.ctx, tid, apigen.ShareModeLive, true)
	require.NoError(t, err, "撤销后名额释放")
	assert.ErrorIs(t, e.svc.Revoke(e.ctx, list[0].ID), share.ErrNotFound, "重复撤销 404")
	assert.ErrorIs(t, e.svc.Revoke(e.ctx, "shr_nope"), share.ErrNotFound)
}

// ---- 匿名面 ----

func TestPublicSurface(t *testing.T) {
	e := newEnv(t)
	orbID := e.orb("orb-01jabcdefghjkmnpqrstvwxyz0")
	e.thread(tid, "Pics", orbID)
	other := "T-01932f6e-8d1a-7c3b-9f2e-ffffffffffff"
	e.thread(other, "Other", nil)
	sha := func(c byte) string { return strings.Repeat(string(c), 64) }
	img := func(s string) map[string]any {
		return map[string]any{"message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "look"}, map[string]any{"type": "image", "data": "blob:sha256:" + s, "mimeType": "image/png"},
		}}}
	}
	e.add(tid, "s1.jsonl", "u1", "", "message", img(sha('a')))
	e.add(tid, "s1.jsonl", "u2", "u1", "message", img(sha('b'))) // 旁支
	e.add(tid, "s1.jsonl", "a1", "u1", "message", msg("assistant", "nice"))
	e.leaf(tid, "a1")
	e.add(other, "o.jsonl", "x1", "", "message", img(sha('c')))
	for _, c := range []byte("abcd") {
		p := filepath.Join(e.dir, "blob-"+string(c))
		require.NoError(t, os.WriteFile(p, []byte("png-"+string(c)), 0o600))
		e.th.blobs[sha(c)] = p
	}

	att1, err := e.att.Upload(e.ctx, "before.txt", tid, strings.NewReader("before"))
	require.NoError(t, err)
	e.now = time.UnixMilli(att1.CreatedAt)
	sh, err := e.svc.Create(e.ctx, tid, apigen.ShareModeSnapshot, false)
	require.NoError(t, err)
	att2, err := e.att.Upload(e.ctx, "after.txt", tid, strings.NewReader("after"))
	require.NoError(t, err)
	_, err = e.st.DB.Exec(`UPDATE attachments SET created_at = ? WHERE id = ?`, att1.CreatedAt+1, att2.ID)
	require.NoError(t, err)
	pub, err := e.svc.Publish(e.ctx, att1.ID)
	require.NoError(t, err)

	ps, err := e.svc.PublicShare(e.ctx, sh.Slug)
	require.NoError(t, err)
	assert.Equal(t, "Pics", ps.Title)
	assert.Equal(t, apigen.ShareModeSnapshot, ps.Mode)
	require.Len(t, ps.Attachments, 1, "快照只含创建时已存在的附件")
	assert.Equal(t, "before.txt", ps.Attachments[0].Name)
	require.NotNil(t, ps.Attachments[0].URL)
	assert.Equal(t, pub.URL, *ps.Attachments[0].URL)
	page, err := e.svc.PublicEntries(e.ctx, sh.Slug, 0, 500)
	require.NoError(t, err)
	for _, v := range []any{ps, page} {
		b, _ := json.Marshal(v)
		assert.NotContains(t, string(b), tid, "公开 JSON 不含线程 id")
		assert.NotContains(t, string(b), *orbID, "公开 JSON 不含 orb id")
		assert.NotContains(t, string(b), "orb-")
	}

	// 图片：范围内 → 200；旁支引用、别的线程、随机 sha → 404
	b, f, err := e.svc.PublicBlob(e.ctx, sh.Slug, sha('a'))
	require.NoError(t, err)
	data, _ := io.ReadAll(f)
	_ = f.Close()
	assert.Equal(t, "png-a", string(data))
	assert.Equal(t, "image/png", b.Mime)
	for _, s := range []string{sha('b'), sha('c'), sha('d'), "deadbeef", strings.ToUpper(sha('a'))} {
		_, _, err := e.svc.PublicBlob(e.ctx, sh.Slug, s)
		assert.ErrorIs(t, err, share.ErrNotFound, s)
	}

	// 未知 / 已撤销 / 形态不对的 slug → 404
	for _, slug := range []string{strings.Repeat("0", 32), "xyz", sh.Slug + "0"} {
		_, err := e.svc.PublicShare(e.ctx, slug)
		assert.ErrorIs(t, err, share.ErrNotFound)
		_, err = e.svc.PublicEntries(e.ctx, slug, 0, 10)
		assert.ErrorIs(t, err, share.ErrNotFound)
	}
	require.NoError(t, e.svc.Revoke(e.ctx, sh.ID))
	_, err = e.svc.PublicShare(e.ctx, sh.Slug)
	assert.ErrorIs(t, err, share.ErrNotFound)
	_, err = e.svc.PublicEntries(e.ctx, sh.Slug, 0, 10)
	assert.ErrorIs(t, err, share.ErrNotFound)
	_, _, err = e.svc.PublicBlob(e.ctx, sh.Slug, sha('a'))
	assert.ErrorIs(t, err, share.ErrNotFound)

	// 线程删除：分享随外键消失
	sh2, err := e.svc.Create(e.ctx, tid, apigen.ShareModeLive, false)
	require.NoError(t, err)
	_, err = e.st.Q.DeleteThread(e.ctx, tid)
	require.NoError(t, err)
	_, err = e.svc.PublicShare(e.ctx, sh2.Slug)
	assert.ErrorIs(t, err, share.ErrNotFound)
}

func TestOverview(t *testing.T) {
	e := newEnv(t)
	e.thread(tid, "T", nil)
	e.add(tid, "s1.jsonl", "u1", "", "message", msg("user", "hi"))
	sh, err := e.svc.Create(e.ctx, tid, apigen.ShareModeLive, false)
	require.NoError(t, err)
	a, err := e.att.Upload(e.ctx, "x.txt", tid, strings.NewReader("x"))
	require.NoError(t, err)
	pf, err := e.svc.Publish(e.ctx, a.ID)
	require.NoError(t, err)
	ov, err := e.svc.Overview(e.ctx)
	require.NoError(t, err)
	require.Len(t, ov.Shares, 1)
	assert.Equal(t, sh.ID, ov.Shares[0].ID)
	assert.Equal(t, "T", ov.Shares[0].ThreadTitle)
	require.Len(t, ov.Files, 1)
	assert.Equal(t, pf.ID, ov.Files[0].ID)
	urls, err := e.svc.PublicURLs(e.ctx, []string{a.ID, "att_nope"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{a.ID: pf.URL}, urls)
	_, err = e.svc.List(e.ctx, "T-nope")
	assert.ErrorIs(t, err, share.ErrNotFound)
}

// ---- 页面与 .md ----

func spa() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Security-Policy", "default-src 'self'")
		_, _ = fmt.Fprintf(w, "SPA %s", r.URL.Path)
	})
}

func do(t *testing.T, h http.Handler, method, target string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPages(t *testing.T) {
	e := newEnv(t)
	e.thread(tid, "Fix the build", nil)
	e.add(tid, "s1.jsonl", "u1", "", "message", msg("user", "please check"))
	e.add(tid, "s1.jsonl", "u2", "u1", "message", msg("user", "side branch text"))
	e.add(tid, "s1.jsonl", "a1", "u1", "message", map[string]any{"message": map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "thinking", "thinking": "Let me look."},
		map[string]any{"type": "text", "text": "I'll run a command."},
		map[string]any{"type": "toolCall", "id": "c1", "name": "bash", "arguments": map[string]any{"command": "ls -la"}},
	}}})
	e.add(tid, "s1.jsonl", "r1", "a1", "message", map[string]any{"message": map[string]any{"role": "toolResult", "toolCallId": "c1", "toolName": "bash",
		"content": []any{map[string]any{"type": "text", "text": "total 0\n```weird```"}}}})
	e.add(tid, "s1.jsonl", "a2", "r1", "message", msg("assistant", "All good."))
	e.leaf(tid, "a2")
	sh, err := e.svc.Create(e.ctx, tid, apigen.ShareModeSnapshot, false)
	require.NoError(t, err)
	h := e.svc.PageHandler(spa())

	rec := do(t, h, http.MethodGet, "/s/"+sh.Slug)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "SPA /", rec.Body.String(), "交给 SPA 并把路径改成 /")
	assert.Equal(t, "noindex, nofollow", rec.Header().Get("X-Robots-Tag"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "default-src 'self'", rec.Header().Get("Content-Security-Policy"), "SPA 的 CSP 不变")
	assert.Equal(t, http.StatusOK, do(t, h, http.MethodHead, "/s/"+sh.Slug).Code)

	md := do(t, h, http.MethodGet, "/s/"+sh.Slug+".md")
	require.Equal(t, http.StatusOK, md.Code)
	assert.Equal(t, "text/markdown; charset=utf-8", md.Header().Get("Content-Type"))
	assert.Equal(t, "inline; filename*=UTF-8''Fix%20the%20build.md", md.Header().Get("Content-Disposition"))
	assert.Equal(t, "noindex, nofollow", md.Header().Get("X-Robots-Tag"))
	assert.Equal(t, "no-store", md.Header().Get("Cache-Control"))
	golden := filepath.Join("testdata", "share.golden.md")
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(golden, md.Body.Bytes(), 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	assert.Equal(t, string(want), md.Body.String())
	assert.NotContains(t, md.Body.String(), "side branch text")

	for _, p := range []string{"/s/not-a-slug", "/s/" + strings.Repeat("0", 32), "/s/" + strings.ToUpper(sh.Slug), "/s/" + sh.Slug + "/x", "/s/"} {
		rec := do(t, h, http.MethodGet, p)
		assert.Equal(t, http.StatusNotFound, rec.Code, p)
		assert.Contains(t, rec.Body.String(), `<meta name="robots" content="noindex">`)
		assert.Equal(t, "noindex, nofollow", rec.Header().Get("X-Robots-Tag"))
	}
	post := do(t, h, http.MethodPost, "/s/"+sh.Slug)
	assert.Equal(t, http.StatusMethodNotAllowed, post.Code)
	assert.Equal(t, "GET, HEAD", post.Header().Get("Allow"))

	require.NoError(t, e.svc.Revoke(e.ctx, sh.ID))
	assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/s/"+sh.Slug).Code)
	assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/s/"+sh.Slug+".md").Code)
}

// ---- 文件发布 ----

func TestFiles(t *testing.T) {
	e := newEnv(t)
	orbID := e.orb("orb-01jabcdefghjkmnpqrstvwxyz0")
	e.thread(tid, "T", orbID)
	h := e.svc.FileHandler()

	unbound, err := e.att.Upload(e.ctx, "loose.txt", "", strings.NewReader("x"))
	require.NoError(t, err)
	_, err = e.svc.Publish(e.ctx, unbound.ID)
	var shErr *share.Error
	require.ErrorAs(t, err, &shErr, "未绑定线程的附件不能发布")
	assert.Equal(t, http.StatusBadRequest, shErr.Status)

	type fileCase struct {
		name, body, ctype, disp string
	}
	cases := []fileCase{
		{"photo.png", "\x89PNG\r\n\x1a\n" + strings.Repeat("p", 64), "image/png", "inline; filename*=UTF-8''photo.png"},
		{"notes 1.txt", "hello public world", "text/plain; charset=utf-8", "inline; filename*=UTF-8''notes%201.txt"},
		{"page.html", "<!doctype html><script>alert(1)</script>", "application/octet-stream", "attachment; filename*=UTF-8''page.html"},
		{"报告.pdf", "%PDF-1.4 fake", "application/octet-stream", "attachment; filename*=UTF-8''%E6%8A%A5%E5%91%8A.pdf"},
	}
	for _, c := range cases {
		a, err := e.att.Upload(e.ctx, c.name, tid, strings.NewReader(c.body))
		require.NoError(t, err)
		p1, err := e.svc.Publish(e.ctx, a.ID)
		require.NoError(t, err)
		p2, err := e.svc.Publish(e.ctx, a.ID)
		require.NoError(t, err)
		assert.Equal(t, p1, p2, "发布幂等")
		assert.Regexp(t, `^https://oryn\.example\.com/f/[0-9a-f]{32}/`, p1.URL)
		path := strings.TrimPrefix(p1.URL, "https://oryn.example.com")
		rec := do(t, h, http.MethodGet, path)
		require.Equal(t, http.StatusOK, rec.Code, c.name)
		assert.Equal(t, c.body, rec.Body.String())
		assert.Equal(t, c.ctype, rec.Header().Get("Content-Type"), c.name)
		assert.Equal(t, c.disp, rec.Header().Get("Content-Disposition"), c.name)
		assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
		assert.Equal(t, "sandbox", rec.Header().Get("Content-Security-Policy"))
		assert.Equal(t, "noindex, nofollow", rec.Header().Get("X-Robots-Tag"))
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	}

	a, err := e.att.Upload(e.ctx, "range.txt", tid, strings.NewReader("0123456789"))
	require.NoError(t, err)
	pf, err := e.svc.Publish(e.ctx, a.ID)
	require.NoError(t, err)
	path := strings.TrimPrefix(pf.URL, "https://oryn.example.com")
	rec := do(t, h, http.MethodGet, path, "Range", "bytes=2-5")
	assert.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Equal(t, "2345", rec.Body.String())
	slug := strings.Split(path, "/")[2]
	assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/f/"+slug+"/other.txt").Code, "名字不符")
	assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/f/"+slug).Code)
	assert.Equal(t, http.StatusMethodNotAllowed, do(t, h, http.MethodDelete, path).Code)

	require.NoError(t, e.svc.Unpublish(e.ctx, pf.ID))
	assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, path).Code, "撤回后 404")
	assert.ErrorIs(t, e.svc.Unpublish(e.ctx, pf.ID), share.ErrNotFound)
	pf2, err := e.svc.Publish(e.ctx, a.ID)
	require.NoError(t, err)
	assert.NotEqual(t, pf.URL, pf2.URL, "重新发布得到新 slug")
	require.NoError(t, e.att.Delete(e.ctx, a.ID))
	assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, strings.TrimPrefix(pf2.URL, "https://oryn.example.com")).Code, "删除附件后 404")
}

func TestPublishOrbFile(t *testing.T) {
	e := newEnv(t)
	orbID := e.orb("orb-01jabcdefghjkmnpqrstvwxyz0")
	e.thread(tid, "T", orbID)
	e.orbs.files["~/workspace/out/report.csv"] = []byte("a,b\n1,2\n")
	e.orbs.dirs["~/workspace/out"] = true
	e.orbs.files["~/workspace/big.bin"] = []byte("x")
	e.orbs.sizes["~/workspace/big.bin"] = share.MaxOrbFileBytes + 1

	pf, err := e.svc.PublishOrbFile(e.ctx, *orbID, tid, "~/workspace/out/report.csv")
	require.NoError(t, err)
	assert.Equal(t, "report.csv", pf.Name)
	require.NotNil(t, pf.ThreadID)
	assert.Equal(t, tid, *pf.ThreadID)
	rec := do(t, e.svc.FileHandler(), http.MethodGet, strings.TrimPrefix(pf.URL, "https://oryn.example.com"))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "a,b\n1,2\n", rec.Body.String())

	// 发布的是此刻的副本
	e.orbs.files["~/workspace/out/report.csv"] = []byte("changed")
	rec = do(t, e.svc.FileHandler(), http.MethodGet, strings.TrimPrefix(pf.URL, "https://oryn.example.com"))
	assert.Equal(t, "a,b\n1,2\n", rec.Body.String())

	check := func(err error, status int, code string) {
		t.Helper()
		var se *share.Error
		require.ErrorAs(t, err, &se)
		assert.Equal(t, status, se.Status)
		assert.Equal(t, code, se.Code)
	}
	_, err = e.svc.PublishOrbFile(e.ctx, *orbID, tid, "~/workspace/out")
	check(err, http.StatusConflict, "is_a_directory")
	_, err = e.svc.PublishOrbFile(e.ctx, *orbID, tid, "~/workspace/big.bin")
	check(err, http.StatusRequestEntityTooLarge, "too_large")
	_, err = e.svc.PublishOrbFile(e.ctx, *orbID, tid, "~/workspace/missing")
	check(err, http.StatusNotFound, "not_found")
	_, err = e.svc.PublishOrbFile(e.ctx, "orb-other", tid, "~/workspace/out/report.csv")
	check(err, http.StatusBadRequest, "invalid")
	e.orbs.ensureErr = fmt.Errorf("wrapped: %w", orbs.ErrOrbNotReady)
	_, err = e.svc.PublishOrbFile(e.ctx, *orbID, tid, "~/workspace/out/report.csv")
	check(err, http.StatusConflict, "orb_not_ready")
}

// ---- 工具 ----

func TestSlugShape(t *testing.T) {
	assert.True(t, share.ValidSlug(strings.Repeat("a", 32)))
	assert.False(t, share.ValidSlug(strings.Repeat("A", 32)))
	assert.False(t, share.ValidSlug(strings.Repeat("a", 31)))
}

// ---- 审查修复回归 ----

// blobImg 是带一张 blob 图片的用户消息 payload。
func blobImg(sha string) map[string]any {
	return map[string]any{"message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "look"}, map[string]any{"type": "image", "data": "blob:sha256:" + sha, "mimeType": "image/png"},
	}}}
}

func (e *env) blobFile(sha string) {
	e.t.Helper()
	p := filepath.Join(e.dir, "blob-"+sha[:8])
	require.NoError(e.t, os.WriteFile(p, []byte("png-"+sha[:4]), 0o600))
	e.th.blobs[sha] = p
}

// B8：活动分支上的隐藏条目（display:false 的 custom_message / hookMessage、白名单外的类型）在公开面上被清空，
// 它们原始 payload 里引用的 blob 不得因此获得授权。
func TestPublicBlobFollowsSanitizedPayload(t *testing.T) {
	e := newEnv(t)
	e.thread(tid, "Pics", nil)
	sha := func(c byte) string { return strings.Repeat(string(c), 64) }
	e.add(tid, "s1.jsonl", "u1", "", "message", blobImg(sha('a')))
	e.add(tid, "s1.jsonl", "cm", "u1", "custom_message", map[string]any{"customType": "x", "display": false,
		"content": []any{map[string]any{"type": "image", "data": "blob:sha256:" + sha('b'), "mimeType": "image/png"}}})
	e.add(tid, "s1.jsonl", "hk", "cm", "message", map[string]any{"message": map[string]any{"role": "hookMessage", "display": false,
		"content": []any{map[string]any{"type": "image", "data": "blob:sha256:" + sha('c'), "mimeType": "image/png"}}}})
	e.add(tid, "s1.jsonl", "lb", "hk", "label", map[string]any{"label": "blob:sha256:" + sha('d')})
	e.add(tid, "s1.jsonl", "a1", "lb", "message", msg("assistant", "ok"))
	e.leaf(tid, "a1")
	for _, c := range []byte("abcd") {
		e.blobFile(sha(c))
	}
	for _, mode := range []apigen.ShareMode{apigen.ShareModeSnapshot, apigen.ShareModeLive} {
		sh, err := e.svc.Create(e.ctx, tid, mode, false)
		require.NoError(t, err)
		_, f, err := e.svc.PublicBlob(e.ctx, sh.Slug, sha('a'))
		require.NoError(t, err, "可见条目引用的图片照常放行（%s）", mode)
		_ = f.Close()
		for _, c := range []byte("bcd") {
			_, _, err := e.svc.PublicBlob(e.ctx, sh.Slug, sha(c))
			assert.ErrorIs(t, err, share.ErrNotFound, "隐藏条目引用的 blob %c 不得公开（%s）", c, mode)
		}
	}
}

// F4：标题与将要列出的附件名也在扫描范围内；公开面只列已发布或被公开条目引用的附件。
func TestScanTitleAndAttachmentNames(t *testing.T) {
	e := newEnv(t)
	e.thread(tid, "deploy with ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", nil)
	ref, err := e.att.Upload(e.ctx, "sk-namenamenamenamename1234.txt", tid, strings.NewReader("r"))
	require.NoError(t, err)
	hidden, err := e.att.Upload(e.ctx, "AKIAABCDEFGHIJKLMNOP.txt", tid, strings.NewReader("h"))
	require.NoError(t, err)
	hiddenRef, err := e.att.Upload(e.ctx, "injected.txt", tid, strings.NewReader("i"))
	require.NoError(t, err)
	pubAtt, err := e.att.Upload(e.ctx, "published.txt", tid, strings.NewReader("p"))
	require.NoError(t, err)
	_, err = e.svc.Publish(e.ctx, pubAtt.ID)
	require.NoError(t, err)
	e.now = time.UnixMilli(pubAtt.CreatedAt) // 附件服务用真实时钟：分享时刻取在全部附件之后
	e.add(tid, "s1.jsonl", "u1", "", "message", msg("user", "see attachment "+ref.ID))
	e.add(tid, "s1.jsonl", "cm", "u1", "custom_message", map[string]any{"customType": "x", "content": "file " + hiddenRef.ID, "display": false})
	e.add(tid, "s1.jsonl", "a1", "cm", "message", msg("assistant", "ok"))
	e.leaf(tid, "a1")

	_, err = e.svc.Create(e.ctx, tid, apigen.ShareModeSnapshot, false)
	var se *share.ScanError
	require.ErrorAs(t, err, &se)
	type hit struct {
		rule    apigen.ShareFindingRule
		entryID string
		seq     int64
	}
	var got []hit
	for _, f := range se.Scan.Findings {
		got = append(got, hit{f.Rule, f.EntryID, f.Seq})
	}
	assert.Equal(t, []hit{
		{apigen.ShareFindingRuleGithubToken, "__title__", 0},
		{apigen.ShareFindingRuleAPIKeySk, "attachment:" + ref.ID, 0},
	}, got, "标题与被引用的附件名命中；未公开附件的名字不扫也不出")
	assert.Equal(t, 3, se.Scan.Scanned)

	sh, err := e.svc.Create(e.ctx, tid, apigen.ShareModeSnapshot, true)
	require.NoError(t, err)
	assert.Equal(t, 2, sh.Findings)
	ps, err := e.svc.PublicShare(e.ctx, sh.Slug)
	require.NoError(t, err)
	var names []string
	for _, a := range ps.Attachments {
		names = append(names, a.Name)
	}
	assert.ElementsMatch(t, []string{ref.Name, pubAtt.Name}, names, "只列已发布或被公开条目引用的附件")
	b, _ := json.Marshal(ps)
	assert.NotContains(t, string(b), hidden.Name)
	assert.NotContains(t, string(b), hiddenRef.ID)
}

// F5：快照固化创建时扫描过的条目集合与内容；条目被改写、叶子被删后，快照不回落到别的分支，
// 也不出现改写后的内容；标题同样冻结在创建时。
func TestSnapshotFrozenAfterRewrite(t *testing.T) {
	e := newEnv(t)
	e.thread(tid, "Original title", nil)
	e.add(tid, "s1.jsonl", "u1", "", "message", msg("user", "question"))
	e.add(tid, "s1.jsonl", "a1", "u1", "message", msg("assistant", "first answer"))
	e.add(tid, "s1.jsonl", "u2", "a1", "message", msg("user", "follow up"))
	// 被回退的旁支（创建时不在分支上、没被扫描），在文件里排在最后
	e.add(tid, "s1.jsonl", "x1", "a1", "message", msg("user", "side sk-sidesidesidesideside1234"))
	e.leaf(tid, "u2")
	sh, err := e.svc.Create(e.ctx, tid, apigen.ShareModeSnapshot, false)
	require.NoError(t, err, "旁支不在扫描范围内")
	want := []string{"u1", "a1", "u2"}
	require.Equal(t, want, ids(e.allEntries(sh.Slug, 500)))

	// 改写：a1 内容换成未经扫描的文本（并引用一张图片）
	sha := strings.Repeat("e", 64)
	e.blobFile(sha)
	rewritten := blobImg(sha)
	rewritten["message"].(map[string]any)["content"] = append(rewritten["message"].(map[string]any)["content"].([]any),
		map[string]any{"type": "text", "text": "rewritten sk-rewrittenrewritten1234"})
	e.add(tid, "s1.jsonl", "a1", "u1", "message", rewritten)
	got := e.allEntries(sh.Slug, 1)
	assert.Equal(t, []string{"u1", "u2"}, ids(got), "被改写的行从快照里消失，而不是换成新内容")
	b, _ := json.Marshal(got)
	assert.NotContains(t, string(b), "rewritten")
	_, _, err = e.svc.PublicBlob(e.ctx, sh.Slug, sha)
	assert.ErrorIs(t, err, share.ErrNotFound, "改写后才出现的图片引用不授权")

	// 叶子被删：不回落到旁支
	_, err = e.st.DB.Exec(`DELETE FROM entries WHERE thread_id = ? AND entry_id = 'u2'`, tid)
	require.NoError(t, err)
	_, err = e.st.DB.Exec(`UPDATE threads SET title = 'Renamed sk-titletitletitletitle1234' WHERE id = ?`, tid)
	require.NoError(t, err)
	got = e.allEntries(sh.Slug, 500)
	assert.Equal(t, []string{"u1"}, ids(got))
	b, _ = json.Marshal(got)
	assert.NotContains(t, string(b), "side", "叶子被删后不回落到未扫描的旁支")
	ps, err := e.svc.PublicShare(e.ctx, sh.Slug)
	require.NoError(t, err)
	assert.Equal(t, "Original title", ps.Title, "快照标题冻结在创建时")
	md := do(t, e.svc.PageHandler(spa()), http.MethodGet, "/s/"+sh.Slug+".md")
	require.Equal(t, http.StatusOK, md.Code)
	assert.Contains(t, md.Body.String(), "Original title")
	assert.NotContains(t, md.Body.String(), "side")
	assert.NotContains(t, md.Body.String(), "rewritten")
	assert.NotContains(t, md.Body.String(), "Renamed")
}
