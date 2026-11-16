package memory_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// admFake 是管理接口用到的假 Hindsight：documents、retain、recall、mental models、operations、导出下载。
type admFake struct {
	srv *httptest.Server

	mu       sync.Mutex
	key      string
	failDel  bool // DELETE 一律 500
	docs     map[string]*admDoc
	models   map[string]*admModel
	ops      map[string]map[string]any
	files    map[string][]byte
	calls    []string // "METHOD path"
	retains  []map[string]any
	recalls  []map[string]any
	refresh  []string // "<id>:<mode>"
	creates  []map[string]any
	patches  []map[string]any
	docDels  []string
	mmDels   []string
	clock    int
}

type admDoc struct {
	text    string
	tags    []string
	created string
	updated string
}

type admModel struct {
	Name        string         `json:"name"`
	SourceQuery string         `json:"source_query"`
	Tags        []string       `json:"tags"`
	Content     string         `json:"content"`
	Trigger     map[string]any `json:"trigger"`
}

func newAdmFake(t *testing.T) *admFake {
	t.Helper()
	f := &admFake{docs: map[string]*admDoc{}, models: map[string]*admModel{}, ops: map[string]map[string]any{}, files: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *admFake) with(fn func(f *admFake)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *admFake) stamp() string {
	f.clock++
	return time.Unix(1_800_000_000+int64(f.clock), 0).UTC().Format("2006-01-02T15:04:05.000000+00:00")
}

func (f *admFake) addDoc(id, text string, tags ...string) {
	f.with(func(f *admFake) {
		ts := f.stamp()
		f.docs[id] = &admDoc{text: text, tags: tags, created: ts, updated: ts}
	})
}

func (f *admFake) addModel(id string, tags ...string) {
	f.with(func(f *admFake) { f.models[id] = &admModel{Name: id, Tags: tags, Content: "内容 " + id} })
}

func (f *admFake) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func writeJSONTo(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *admFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.key == "" || r.Header.Get("Authorization") != "Bearer "+f.key {
		writeJSONTo(w, http.StatusUnauthorized, map[string]string{"detail": "Authentication failed: Invalid API key"})
		return
	}
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	const bank = "/v1/default/banks/oryn"
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/v1/default/files/download/") && r.Method == http.MethodGet:
		data, ok := f.files[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// 真 Hindsight 的文件名与 result_metadata 不一致；orynd 不应转发任何上游头
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="oryn-documents.zip"`)
		w.Header().Set("X-Upstream-Key", f.key)
		_, _ = w.Write(data)
	case !strings.HasPrefix(p, bank+"/"):
		http.NotFound(w, r)
	default:
		f.bank(w, r, strings.TrimPrefix(p, bank), body)
	}
}

func (f *admFake) bank(w http.ResponseWriter, r *http.Request, rel string, body map[string]any) {
	q := r.URL.Query()
	switch {
	case rel == "/documents" && r.Method == http.MethodGet:
		tag := q.Get("tags")
		var ids []string
		for id, d := range f.docs {
			if slices.Contains(d.tags, tag) {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return f.docs[ids[i]].updated > f.docs[ids[j]].updated })
		off, _ := strconv.Atoi(q.Get("offset"))
		lim, _ := strconv.Atoi(q.Get("limit"))
		total := len(ids)
		ids = ids[min(off, total):min(off+lim, total)]
		items := []map[string]any{}
		for _, id := range ids {
			d := f.docs[id]
			items = append(items, map[string]any{"id": id, "created_at": d.created, "updated_at": d.updated, "tags": d.tags})
		}
		writeJSONTo(w, 200, map[string]any{"items": items, "total": total})
	case strings.HasPrefix(rel, "/documents/"):
		id := strings.TrimPrefix(rel, "/documents/")
		d, ok := f.docs[id]
		switch {
		case r.Method == http.MethodDelete && f.failDel:
			writeJSONTo(w, 500, map[string]string{"detail": "db down"})
		case !ok:
			writeJSONTo(w, 404, map[string]string{"detail": "Document not found"})
		case r.Method == http.MethodGet:
			writeJSONTo(w, 200, map[string]any{"id": id, "original_text": d.text, "tags": d.tags, "created_at": d.created, "updated_at": d.updated})
		case r.Method == http.MethodDelete:
			delete(f.docs, id)
			f.docDels = append(f.docDels, id)
			for _, m := range f.models {
				m.Content += "（stale）"
			}
			writeJSONTo(w, 200, map[string]any{"success": true, "document_id": id})
		}
	case rel == "/memories" && r.Method == http.MethodPost:
		f.retains = append(f.retains, body)
		for _, raw := range body["items"].([]any) {
			it := raw.(map[string]any)
			var tags []string
			for _, t := range it["tags"].([]any) {
				tags = append(tags, t.(string))
			}
			id := it["document_id"].(string)
			ts := f.stamp()
			if old, ok := f.docs[id]; ok {
				old.text, old.tags, old.updated = it["content"].(string), tags, ts
			} else {
				f.docs[id] = &admDoc{text: it["content"].(string), tags: tags, created: ts, updated: ts}
			}
		}
		writeJSONTo(w, 200, map[string]any{"success": true, "bank_id": "oryn", "items_count": 1, "async": false})
	case rel == "/memories/recall" && r.Method == http.MethodPost:
		// 故意不按标签过滤：orynd 必须自己再按标签过滤一遍
		f.recalls = append(f.recalls, body)
		results := []map[string]any{}
		ids := make([]string, 0, len(f.docs))
		for id := range f.docs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			d := f.docs[id]
			results = append(results, map[string]any{"text": d.text, "type": "world", "document_id": id, "tags": d.tags, "mentioned_at": d.created})
		}
		writeJSONTo(w, 200, map[string]any{"results": results})
	case rel == "/mental-models" && r.Method == http.MethodGet:
		// all 匹配：带上无标签模型，orynd 要在客户端过滤
		tag := q.Get("tags")
		items := []map[string]any{}
		ids := make([]string, 0, len(f.models))
		for id := range f.models {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			m := f.models[id]
			if len(m.Tags) == 0 || slices.Contains(m.Tags, tag) {
				items = append(items, map[string]any{"id": id, "name": m.Name, "tags": m.Tags, "content": m.Content, "source_query": m.SourceQuery})
			}
		}
		writeJSONTo(w, 200, map[string]any{"items": items})
	case rel == "/mental-models" && r.Method == http.MethodPost:
		f.creates = append(f.creates, body)
		m := &admModel{}
		raw, _ := json.Marshal(body)
		_ = json.Unmarshal(raw, m)
		f.models[body["id"].(string)] = m
		writeJSONTo(w, 200, map[string]any{"mental_model_id": body["id"], "operation_id": "op-mm"})
	case strings.HasPrefix(rel, "/mental-models/"):
		id, action, _ := strings.Cut(strings.TrimPrefix(rel, "/mental-models/"), "/")
		m, ok := f.models[id]
		switch {
		case r.Method == http.MethodDelete && f.failDel:
			writeJSONTo(w, 500, map[string]string{"detail": "db down"})
		case !ok:
			writeJSONTo(w, 404, map[string]string{"detail": "Mental model not found"})
		case action == "refresh" && r.Method == http.MethodPost:
			mode, _ := body["mode"].(string)
			f.refresh = append(f.refresh, id+":"+mode)
			writeJSONTo(w, 200, map[string]any{"operation_id": "op-r", "status": "queued"})
		case action == "" && r.Method == http.MethodGet:
			writeJSONTo(w, 200, m)
		case action == "" && r.Method == http.MethodPatch:
			f.patches = append(f.patches, body)
			raw, _ := json.Marshal(body)
			_ = json.Unmarshal(raw, m)
			writeJSONTo(w, 200, m)
		case action == "" && r.Method == http.MethodDelete:
			delete(f.models, id)
			f.mmDels = append(f.mmDels, id)
			writeJSONTo(w, 200, map[string]string{"status": "deleted"})
		default:
			http.NotFound(w, r)
		}
	case rel == "/transfer/export" && r.Method == http.MethodPost:
		id := fmt.Sprintf("op-exp-%d", len(f.ops)+1)
		f.ops[id] = map[string]any{"operation_id": id, "status": "pending", "operation_type": "export_bank", "created_at": "2027-01-15T08:00:00.000000+00:00"}
		writeJSONTo(w, 202, map[string]any{"operation_id": id, "status": "pending"})
	case strings.HasPrefix(rel, "/operations/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(rel, "/operations/")
		op, ok := f.ops[id]
		if !ok {
			writeJSONTo(w, 200, map[string]any{"operation_id": id, "status": "not_found", "operation_type": nil})
			return
		}
		writeJSONTo(w, 200, op)
	default:
		http.NotFound(w, r)
	}
}

type admEnv struct {
	st  *store.Store
	svc *memory.Service
	now time.Time
}

func newAdmEnv(t *testing.T, mod func(*memory.Config)) *admEnv {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	e := &admEnv{st: st, now: time.Unix(1_800_000_000, 0)}
	cfg := memory.Config{Q: st.Q, Box: box, PublicURL: testPublicURL, Now: func() time.Time { return e.now }}
	if mod != nil {
		mod(&cfg)
	}
	e.svc = memory.New(cfg)
	return e
}

// configure 把记忆指向 base 并生成 .env，返回 Hindsight API key。
func (e *admEnv) configure(t *testing.T, base string) string {
	t.Helper()
	ctx := context.Background()
	_, err := e.svc.PutSettings(ctx, apigen.MemorySettingsUpdate{Enabled: true, HindsightURL: &base})
	require.NoError(t, err)
	out, err := e.svc.GenerateEnvFile(ctx)
	require.NoError(t, err)
	return envValue(t, out.Content, "HINDSIGHT_API_TENANT_API_KEY")
}

func (e *admEnv) withFake(t *testing.T) *admFake {
	t.Helper()
	f := newAdmFake(t)
	key := e.configure(t, f.srv.URL)
	f.with(func(f *admFake) { f.key = key })
	return f
}

func (e *admEnv) project(t *testing.T, id string) {
	t.Helper()
	require.NoError(t, e.st.Q.CreateProject(context.Background(), db.CreateProjectParams{ID: id, Name: id, CreatedAt: 1, UpdatedAt: 1}))
}

func (e *admEnv) thread(t *testing.T, id, projectID, title string) {
	t.Helper()
	require.NoError(t, e.st.Q.CreateThread(context.Background(), db.CreateThreadParams{
		ID: id, ProjectID: &projectID, Title: title, TitleSource: "user", State: "idle", StateChangedAt: 1,
		SessionDir: "d", CreatedAt: 1, UpdatedAt: 1, Kind: "work",
	}))
}

// record 记下 document 的归属（网关 retain 时做的事）。
func (e *admEnv) record(t *testing.T, docID string, projectID, threadID *string) {
	t.Helper()
	require.NoError(t, e.st.Q.UpsertMemoryDocument(context.Background(), db.UpsertMemoryDocumentParams{
		DocumentID: docID, ProjectID: projectID, ThreadID: threadID, Now: e.now.Unix(),
	}))
}

func memErr(t *testing.T, err error) *memory.Error {
	t.Helper()
	var me *memory.Error
	require.True(t, errors.As(err, &me), "期望领域错误，实际 %v", err)
	return me
}

// R19：按线程遗忘只删该线程的 document，并从头刷新该项目的 mental model。
func TestMemoryAdminForgetThread(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	f := e.withFake(t)
	tag := memory.ProjectTag("PRJ_A")
	f.addDoc("sess-a1", "线程 A 的第一段", tag)
	f.addDoc("sess-a2", "线程 A 的第二段", tag)
	f.addDoc("sess-b1", "线程 B", tag)
	f.addModel("project-conventions-prj_a", tag)
	f.addModel("project-conventions-prj_b", memory.ProjectTag("PRJ_B"))
	p := "PRJ_A"
	e.record(t, "sess-a1", &p, new("T-a"))
	e.record(t, "sess-a2", &p, new("T-a"))
	e.record(t, "sess-b1", &p, new("T-b"))

	out, err := e.svc.ForgetThread(ctx, "T-a")
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryForgetResult{Deleted: 2, Pending: 0}, out)
	f.with(func(f *admFake) {
		assert.ElementsMatch(t, []string{"sess-a1", "sess-a2"}, f.docDels)
		assert.Contains(t, f.docs, "sess-b1", "其它线程的 document 不动")
		assert.Equal(t, []string{"project-conventions-prj_a:full"}, f.refresh, "删除后从头刷新本项目的 mental model，不碰别的项目")
	})
	rows, err := e.st.Q.ListMemoryDocumentsByThread(ctx, new("T-a"))
	require.NoError(t, err)
	assert.Empty(t, rows)
	rows, err = e.st.Q.ListMemoryDocumentsByThread(ctx, new("T-b"))
	require.NoError(t, err)
	assert.Len(t, rows, 1)

	// 没有记录的线程：不访问 Hindsight
	n := f.callCount()
	out, err = e.svc.ForgetThread(ctx, "T-none")
	require.NoError(t, err)
	assert.Zero(t, out.Deleted)
	assert.Equal(t, n, f.callCount())

	// Hindsight 删除失败：进入重试队列计 pending
	f.addDoc("sess-b2", "线程 B 第二段", tag)
	e.record(t, "sess-b2", &p, new("T-b"))
	f.with(func(f *admFake) { f.failDel = true })
	out, err = e.svc.ForgetThread(ctx, "T-b")
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryForgetResult{Deleted: 0, Pending: 2}, out)
	pend, err := e.svc.ListPendingDeletes(ctx)
	require.NoError(t, err)
	require.Len(t, pend.Items, 2)
	assert.Equal(t, "PRJ_A", *pend.Items[0].ProjectID)
	assert.Contains(t, *pend.Items[0].LastError, "500")
}

// R19：浏览（预览、线程标题、全局内容）与召回（结果按范围再过滤一遍）；删除单个 document 后刷新对应 mental model。
func TestMemoryAdminBrowseSearchDelete(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	f := e.withFake(t)
	e.project(t, "PRJ_A")
	e.thread(t, "T-a", "PRJ_A", "修数据库迁移")
	tagA := memory.ProjectTag("PRJ_A")
	f.addDoc("sess-a1", strings.Repeat("长", 500), tagA)
	f.addDoc("sess-a2", "第二条", tagA)
	f.addDoc("sess-q", "项目 Q 的秘密", memory.ProjectTag("PRJ_Q"))
	f.addDoc("oryn-g1", "全局：注释用中文", memory.GlobalTag)
	p := "PRJ_A"
	e.record(t, "sess-a1", &p, new("T-a"))

	_, err := e.svc.ListDocuments(ctx, &p, true, 0, 10)
	assert.Equal(t, "invalid_request", memErr(t, err).Code)
	list, err := e.svc.ListDocuments(ctx, &p, false, 0, 1)
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.True(t, list.HasMore)
	assert.Equal(t, "sess-a2", list.Items[0].ID, "按更新时间倒序")
	list, err = e.svc.ListDocuments(ctx, &p, false, 1, 1)
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.False(t, list.HasMore)
	d := list.Items[0]
	assert.Equal(t, "sess-a1", d.ID)
	assert.Len(t, []rune(d.Preview), 400)
	assert.Equal(t, "T-a", *d.ThreadID)
	assert.Equal(t, "修数据库迁移", *d.ThreadTitle)
	assert.Equal(t, "PRJ_A", *d.ProjectID)
	assert.Nil(t, d.Content)

	glist, err := e.svc.ListDocuments(ctx, nil, true, 0, 30)
	require.NoError(t, err)
	require.Len(t, glist.Items, 1)
	assert.Equal(t, "全局：注释用中文", *glist.Items[0].Content)

	res, err := e.svc.Search(ctx, "迁移", &p)
	require.NoError(t, err)
	var got []string
	for _, h := range res.Items {
		got = append(got, *h.DocumentID)
		assert.Equal(t, *h.DocumentID == "oryn-g1", *h.Global)
	}
	assert.ElementsMatch(t, []string{"sess-a1", "sess-a2", "oryn-g1"}, got, "别的项目的结果被丢弃")
	f.with(func(f *admFake) {
		assert.Equal(t, []any{tagA, memory.GlobalTag}, f.recalls[0]["tags"])
		assert.Equal(t, "any_strict", f.recalls[0]["tags_match"])
	})
	res, err = e.svc.Search(ctx, "注释", nil)
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	assert.Equal(t, "oryn-g1", *res.Items[0].DocumentID)

	// 删除：有记录的按记录归属，没有记录的按 Hindsight 的标签归属
	f.addModel("project-decisions-prj_a", tagA)
	require.NoError(t, e.svc.DeleteDocument(ctx, "sess-a1"))
	require.NoError(t, e.svc.DeleteDocument(ctx, "sess-a2"))
	_, err = e.st.Q.GetMemoryDocument(ctx, "sess-a1")
	assert.Error(t, err)
	f.with(func(f *admFake) {
		assert.Equal(t, []string{"sess-a1", "sess-a2"}, f.docDels)
		assert.Equal(t, []string{"project-decisions-prj_a:full", "project-decisions-prj_a:full"}, f.refresh)
	})
	assert.Equal(t, http.StatusNotFound, memErr(t, e.svc.DeleteDocument(ctx, "nope")).Status)
}

// R20：批准写入 oryn:global（用编辑后的内容），记进 memory_documents（项目为 NULL），删掉候选；
// R21：user-preferences 按 Core 种子创建，只从全局记忆提炼。
func TestCandidateApproveEdited(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	f := e.withFake(t)
	e.project(t, "PRJ_A")
	e.thread(t, "T-a", "PRJ_A", "来源线程")
	require.NoError(t, e.st.Q.InsertMemoryCandidate(ctx, db.InsertMemoryCandidateParams{
		ID: "mcd_01J00000000000000000000001", Content: "主人喜欢 tab 缩进", Context: new("对话里提到"),
		SourceThreadID: new("T-a"), SourceProjectID: new("PRJ_A"), CreatedAt: 5,
	}))
	cands, err := e.svc.ListCandidates(ctx)
	require.NoError(t, err)
	require.Len(t, cands.Items, 1)
	assert.Equal(t, "来源线程", *cands.Items[0].SourceThreadTitle)

	doc, err := e.svc.ApproveCandidate(ctx, "mcd_01J00000000000000000000001", new("  主人偏好用 4 个空格缩进  "))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(doc.ID, "oryn-"), doc.ID)
	assert.Equal(t, "主人偏好用 4 个空格缩进", *doc.Content)
	f.with(func(f *admFake) {
		require.Len(t, f.retains, 1)
		body := f.retains[0]
		assert.Equal(t, []any{memory.GlobalTag}, body["document_tags"])
		assert.Equal(t, false, body["async"])
		item := body["items"].([]any)[0].(map[string]any)
		assert.Equal(t, "主人偏好用 4 个空格缩进", item["content"])
		assert.Equal(t, []any{memory.GlobalTag}, item["tags"])
		assert.Equal(t, doc.ID, item["document_id"])
		assert.Equal(t, "对话里提到", item["context"])
		require.Len(t, f.creates, 1, "user-preferences 不存在时创建")
		c := f.creates[0]
		assert.Equal(t, memory.UserPrefsModelID, c["id"])
		assert.Equal(t, []any{memory.GlobalTag}, c["tags"])
		assert.Contains(t, c["source_query"], "What does the user prefer")
		assert.Equal(t, "all_strict", c["trigger"].(map[string]any)["tags_match"])
		assert.Empty(t, f.refresh, "创建即刷新，不再重复提交")
	})
	row, err := e.st.Q.GetMemoryDocument(ctx, doc.ID)
	require.NoError(t, err)
	assert.Nil(t, row.ProjectID)
	assert.Nil(t, row.ThreadID, "全局记忆不归线程，按线程遗忘不会删它")
	cands, err = e.svc.ListCandidates(ctx)
	require.NoError(t, err)
	assert.Empty(t, cands.Items)
	assert.Equal(t, http.StatusNotFound, memErr(t, func() error {
		_, err := e.svc.ApproveCandidate(ctx, "mcd_01J00000000000000000000001", nil)
		return err
	}()).Status)

	// 写入失败时候选保留
	require.NoError(t, e.st.Q.InsertMemoryCandidate(ctx, db.InsertMemoryCandidateParams{ID: "mcd_01J00000000000000000000002", Content: "x", CreatedAt: 6}))
	f.with(func(f *admFake) { f.key = "rotated" })
	_, err = e.svc.ApproveCandidate(ctx, "mcd_01J00000000000000000000002", nil)
	assert.Equal(t, "memory_upstream", memErr(t, err).Code)
	_, err = e.st.Q.GetMemoryCandidate(ctx, "mcd_01J00000000000000000000002")
	assert.NoError(t, err)
}

// R20：拒绝只删候选，不访问 Hindsight。
func TestCandidateRejectLeavesHindsightUntouched(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	f := e.withFake(t)
	require.NoError(t, e.st.Q.InsertMemoryCandidate(ctx, db.InsertMemoryCandidateParams{ID: "mcd_01J00000000000000000000003", Content: "x", CreatedAt: 1}))
	require.NoError(t, e.svc.RejectCandidate(ctx, "mcd_01J00000000000000000000003"))
	assert.Zero(t, f.callCount())
	assert.Equal(t, http.StatusNotFound, memErr(t, e.svc.RejectCandidate(ctx, "mcd_01J00000000000000000000003")).Status)
}

// R20：全局记忆的新建、改写（同一 document_id + replace）与删除；R21：每次变动后刷新 user-preferences。
func TestMemoryAdminGlobalMemory(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	f := e.withFake(t)
	// 早先按 Core 无标签种子建出的 user-preferences 会被改回只从全局提炼
	f.addModel(memory.UserPrefsModelID)

	doc, err := e.svc.CreateGlobal(ctx, "迁移必须可回滚")
	require.NoError(t, err)
	upd, err := e.svc.UpdateGlobal(ctx, doc.ID, "迁移必须可回滚，且写 Down")
	require.NoError(t, err)
	assert.Equal(t, doc.ID, upd.ID)
	f.with(func(f *admFake) {
		require.Len(t, f.retains, 2)
		item := f.retains[1]["items"].([]any)[0].(map[string]any)
		assert.Equal(t, doc.ID, item["document_id"])
		assert.Equal(t, "replace", item["update_mode"])
		assert.Equal(t, "迁移必须可回滚，且写 Down", item["content"])
		require.Len(t, f.patches, 1)
		assert.Equal(t, []any{memory.GlobalTag}, f.patches[0]["tags"])
		assert.Equal(t, []string{"user-preferences:", "user-preferences:full"}, f.refresh)
	})

	// 项目 document 不能当全局记忆改写
	p := "PRJ_A"
	f.addDoc("sess-a", "项目记忆", memory.ProjectTag(p))
	e.record(t, "sess-a", &p, new("T-a"))
	_, err = e.svc.UpdateGlobal(ctx, "sess-a", "篡改")
	assert.Equal(t, http.StatusNotFound, memErr(t, err).Status)
	_, err = e.svc.UpdateGlobal(ctx, "missing", "x")
	assert.Equal(t, http.StatusNotFound, memErr(t, err).Status)
	_, err = e.svc.CreateGlobal(ctx, "   ")
	assert.Equal(t, "invalid_request", memErr(t, err).Code)

	require.NoError(t, e.svc.DeleteDocument(ctx, doc.ID))
	f.with(func(f *admFake) {
		assert.NotContains(t, f.docs, doc.ID)
		assert.Equal(t, "user-preferences:full", f.refresh[len(f.refresh)-1])
	})

	// mental models：全局只列 oryn:global 的，项目只列本项目的
	f.addModel("project-conventions-prj_a", memory.ProjectTag(p))
	f.addModel("stray-untagged")
	gl, err := e.svc.ListMentalModels(ctx, nil)
	require.NoError(t, err)
	require.Len(t, gl.Items, 1)
	assert.Equal(t, memory.UserPrefsModelID, gl.Items[0].ID)
	assert.True(t, gl.Items[0].Global)
	pl, err := e.svc.ListMentalModels(ctx, &p)
	require.NoError(t, err)
	require.Len(t, pl.Items, 1)
	assert.Equal(t, "project-conventions-prj_a", pl.Items[0].ID)
	assert.Equal(t, p, *pl.Items[0].ProjectID)
	require.NoError(t, e.svc.RefreshMentalModel(ctx, "project-conventions-prj_a"))
	assert.Equal(t, http.StatusNotFound, memErr(t, e.svc.RefreshMentalModel(ctx, "missing")).Status)
}

// R23：级联删除项目的 document 与 mental model；别的项目不受影响。
func TestPurgeProjectDeletesDocsAndModels(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	f := e.withFake(t)
	a, q := "PRJ_A", "PRJ_Q"
	f.addDoc("sess-a1", "a1", memory.ProjectTag(a))
	f.addDoc("sess-a2", "a2", memory.ProjectTag(a))
	f.addDoc("sess-q1", "q1", memory.ProjectTag(q))
	e.record(t, "sess-a1", &a, new("T-a"))
	e.record(t, "sess-a2", &a, new("T-a2"))
	e.record(t, "sess-q1", &q, new("T-q"))
	f.addModel("project-conventions-prj_a", memory.ProjectTag(a))
	f.addModel("custom-a", memory.ProjectTag(a))
	f.addModel("project-conventions-prj_q", memory.ProjectTag(q))
	f.addModel(memory.UserPrefsModelID, memory.GlobalTag)

	require.NoError(t, e.svc.PurgeProject(ctx, a))
	f.with(func(f *admFake) {
		assert.ElementsMatch(t, []string{"sess-a1", "sess-a2"}, f.docDels)
		assert.Contains(t, f.docs, "sess-q1")
		assert.ElementsMatch(t, []string{"project-conventions-prj_a", "custom-a"}, f.mmDels)
		assert.Contains(t, f.models, "project-conventions-prj_q")
		assert.Contains(t, f.models, memory.UserPrefsModelID)
	})
	ids, err := e.st.Q.ListAllMemoryDocumentIDsByProject(ctx, &a)
	require.NoError(t, err)
	assert.Empty(t, ids)
	pend, err := e.svc.ListPendingDeletes(ctx)
	require.NoError(t, err)
	assert.Empty(t, pend.Items)
}

// R23：删除失败进入重试队列（带退避与失败原因），重试成功后移出队列。
func TestPurgeFailureTombstonesThenRetry(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	f := e.withFake(t)
	a := "PRJ_A"
	f.addDoc("sess-a1", "a1", memory.ProjectTag(a))
	e.record(t, "sess-a1", &a, new("T-a"))
	f.addModel("project-decisions-prj_a", memory.ProjectTag(a))
	f.with(func(f *admFake) { f.failDel = true })

	require.NoError(t, e.svc.PurgeProject(ctx, a))
	pend, err := e.svc.ListPendingDeletes(ctx)
	require.NoError(t, err)
	var gotIDs []string
	for _, p := range pend.Items {
		gotIDs = append(gotIDs, p.DocumentID)
		assert.Equal(t, a, *p.ProjectID)
		assert.Zero(t, p.Attempts)
		assert.Greater(t, p.NextAttemptAt, e.now.Unix())
	}
	assert.ElementsMatch(t, []string{"sess-a1", "mental-model:project-conventions-prj_a", "mental-model:project-decisions-prj_a"}, gotIDs)
	ids, err := e.st.Q.ListAllMemoryDocumentIDsByProject(ctx, &a)
	require.NoError(t, err)
	assert.Empty(t, ids, "失败的由重试队列接管")

	// 未到期不重试
	done, err := e.svc.RetryTombstones(ctx)
	require.NoError(t, err)
	assert.Zero(t, done)
	// 到期但仍失败：次数加一，按退避推迟
	e.now = e.now.Add(time.Minute)
	done, err = e.svc.RetryTombstones(ctx)
	require.NoError(t, err)
	assert.Zero(t, done)
	pend, err = e.svc.ListPendingDeletes(ctx)
	require.NoError(t, err)
	for _, p := range pend.Items {
		assert.Equal(t, 1, p.Attempts)
		assert.Equal(t, e.now.Add(time.Minute).Unix(), p.NextAttemptAt)
	}
	// 恢复后重试成功，队列清空
	f.with(func(f *admFake) { f.failDel = false })
	e.now = e.now.Add(2 * time.Minute)
	done, err = e.svc.RetryTombstones(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, done)
	pend, err = e.svc.ListPendingDeletes(ctx)
	require.NoError(t, err)
	assert.Empty(t, pend.Items)
	f.with(func(f *admFake) {
		assert.Equal(t, []string{"sess-a1"}, f.docDels)
		assert.Equal(t, []string{"project-decisions-prj_a"}, f.mmDels)
	})
}

// R23：Hindsight 不可达或记忆未配置都不报错，失败的进入队列；未配置时重试不计次数。
func TestPurgeHindsightDownOrUnconfigured(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	e.configure(t, dead.URL)
	a := "PRJ_A"
	e.record(t, "sess-a1", &a, new("T-a"))
	require.NoError(t, e.svc.PurgeProject(ctx, a))
	pend, err := e.svc.ListPendingDeletes(ctx)
	require.NoError(t, err)
	assert.Len(t, pend.Items, 3)

	u := newAdmEnv(t, nil)
	require.NoError(t, u.svc.PurgeProject(ctx, a), "从未配置、没有记录：无事可做")
	u.record(t, "sess-x", &a, nil)
	require.NoError(t, u.svc.PurgeProject(ctx, a))
	pend, err = u.svc.ListPendingDeletes(ctx)
	require.NoError(t, err)
	assert.Len(t, pend.Items, 3)
	u.now = u.now.Add(time.Hour)
	done, err := u.svc.RetryTombstones(ctx)
	require.NoError(t, err)
	assert.Zero(t, done)
	pend, err = u.svc.ListPendingDeletes(ctx)
	require.NoError(t, err)
	assert.Zero(t, pend.Items[0].Attempts)
}

// R22：导出经 orynd 代理下载；未完成时 409；只代理 Hindsight 的下载前缀。
func TestExportProxyDownload(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	f := e.withFake(t)
	exp, err := e.svc.StartExport(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryExportRunning, exp.State)
	got, err := e.svc.GetExport(ctx, exp.ID)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryExportRunning, got.State)
	assert.NotZero(t, got.CreatedAt)
	_, err = e.svc.OpenExport(ctx, exp.ID)
	assert.Equal(t, "export_not_ready", memErr(t, err).Code)

	zip := []byte("PK\x03\x04fake-zip")
	f.with(func(f *admFake) {
		f.ops[exp.ID]["status"] = "completed"
		f.ops[exp.ID]["result_metadata"] = map[string]any{"download_url": "/v1/default/files/download/tenants/public/banks/oryn/exports/x/transfer.zip"}
		f.files["/v1/default/files/download/tenants/public/banks/oryn/exports/x/transfer.zip"] = zip
	})
	got, err = e.svc.GetExport(ctx, exp.ID)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryExportDone, got.State)
	arc, err := e.svc.OpenExport(ctx, exp.ID)
	require.NoError(t, err)
	data, err := io.ReadAll(arc.Body)
	require.NoError(t, arc.Body.Close())
	require.NoError(t, err)
	assert.Equal(t, zip, data)
	assert.Equal(t, "oryn-memory-"+exp.ID+".zip", arc.Filename)

	_, err = e.svc.GetExport(ctx, "op-missing")
	assert.Equal(t, http.StatusNotFound, memErr(t, err).Status)
	f.with(func(f *admFake) {
		f.ops["op-other"] = map[string]any{"status": "completed", "operation_type": "batch_retain"}
		f.ops["op-evil"] = map[string]any{"status": "completed", "operation_type": "export_bank", "result_metadata": map[string]any{"download_url": "https://evil.example/x.zip"}}
		f.ops["op-failed"] = map[string]any{"status": "failed", "operation_type": "export_bank", "error_message": "磁盘满"}
	})
	_, err = e.svc.GetExport(ctx, "op-other")
	assert.Equal(t, http.StatusNotFound, memErr(t, err).Status, "只认导出操作")
	_, err = e.svc.OpenExport(ctx, "op-evil")
	assert.Equal(t, "memory_upstream", memErr(t, err).Code, "不带着 Bearer 去取别的地址")
	failed, err := e.svc.GetExport(ctx, "op-failed")
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryExportFailed, failed.State)
	assert.Equal(t, "磁盘满", *failed.Error)
}

// R17：嵌入锁定后改 model/dimensions 需 reembed；Hindsight 仍在运行时 409 hindsight_running，连不上时接受。
func TestMemoryAdminEndpointsReembed(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	f := newAdmFake(t)
	key := e.configure(t, f.srv.URL)
	f.with(func(f *admFake) { f.key = key })
	dims := 1024
	eps, err := e.svc.PutEndpoint(ctx, apigen.MemoryEndpointEmbed, apigen.MemoryEndpointUpdate{
		BaseURL: "https://embed.example.com/v1", Model: "qwen3-embed", Dimensions: &dims, APIKey: new("sk-embed"),
	})
	require.NoError(t, err)
	assert.True(t, eps.Embed.HasKey)
	assert.Equal(t, 1024, *eps.Embed.Dimensions)
	_, err = e.svc.PutEndpoint(ctx, apigen.MemoryEndpointRerank, apigen.MemoryEndpointUpdate{BaseURL: "http://example.com", Model: "r"})
	assert.Equal(t, "insecure_url", memErr(t, err).Code)

	cnt := int64(5)
	require.NoError(t, e.st.Q.SetMemoryHealth(ctx, &cnt)) // bank 有记忆 → embed_locked
	eps, err = e.svc.Endpoints(ctx)
	require.NoError(t, err)
	assert.True(t, eps.EmbedLocked)
	assert.Equal(t, "qwen3-embed", eps.Embed.Model)
	assert.Nil(t, eps.Rerank)

	// 只改地址与 key 不受限制，key 省略保留原值
	eps, err = e.svc.PutEndpoint(ctx, apigen.MemoryEndpointEmbed, apigen.MemoryEndpointUpdate{BaseURL: "https://embed2.example.com/v1", Model: "qwen3-embed", Dimensions: &dims})
	require.NoError(t, err)
	assert.True(t, eps.Embed.HasKey)

	other := 4096
	change := apigen.MemoryEndpointUpdate{BaseURL: "https://embed2.example.com/v1", Model: "qwen3-embed-8b", Dimensions: &other}
	_, err = e.svc.PutEndpoint(ctx, apigen.MemoryEndpointEmbed, change)
	assert.Equal(t, "embed_locked", memErr(t, err).Code)
	yes := true
	change.Reembed = &yes
	_, err = e.svc.PutEndpoint(ctx, apigen.MemoryEndpointEmbed, change)
	assert.Equal(t, "hindsight_running", memErr(t, err).Code, "Hindsight 能连上（即使返回错误状态）就拒绝")

	f.srv.Close()
	eps, err = e.svc.PutEndpoint(ctx, apigen.MemoryEndpointEmbed, change)
	require.NoError(t, err, "Hindsight 已停：接受")
	assert.Equal(t, "qwen3-embed-8b", eps.Embed.Model)
	assert.Equal(t, 4096, *eps.Embed.Dimensions)
	clear := true
	eps, err = e.svc.PutEndpoint(ctx, apigen.MemoryEndpointEmbed, apigen.MemoryEndpointUpdate{BaseURL: eps.Embed.BaseURL, Model: eps.Embed.Model, Dimensions: &other, ClearKey: &clear})
	require.NoError(t, err)
	assert.False(t, eps.Embed.HasKey)
}

// v5：重排可选。没配重排接口为 rrf，配了为 cohere；与已部署的不同时 needsRestart 并给出 .env 片段，确认重启后清除。
// 重排接口可删（幂等），嵌入接口不能删；v5 之前没有 rerank 键的 deployed_families 按 cohere。
func TestMemoryAdminRerankOptional(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	eps, err := e.svc.Endpoints(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryRerankRrf, eps.RerankMode)
	assert.False(t, eps.NeedsRestart, "还没生成 .env，谈不上重启")

	e.configure(t, "http://127.0.0.1:1")
	eps, err = e.svc.Endpoints(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryRerankRrf, eps.RerankMode)
	assert.False(t, eps.NeedsRestart, "生成的 .env 已写 rrf")
	assert.Nil(t, eps.EnvSnippet)

	// 配上重排接口 → cohere，需要重启，片段带三行 cohere 配置（密钥保持原值）
	eps, err = e.svc.PutEndpoint(ctx, apigen.MemoryEndpointRerank, apigen.MemoryEndpointUpdate{
		BaseURL: "https://rerank.example.com", Model: "bge-reranker", APIKey: new("sk-rerank"),
	})
	require.NoError(t, err)
	require.NotNil(t, eps.Rerank)
	assert.Equal(t, apigen.MemoryRerankCohere, eps.RerankMode)
	assert.True(t, eps.NeedsRestart)
	require.NotNil(t, eps.EnvSnippet)
	assert.Equal(t, "HINDSIGHT_API_RERANKER_PROVIDER=cohere\n"+
		"HINDSIGHT_API_RERANKER_COHERE_BASE_URL="+testPublicURL+"/memory-relay/v1/rerank/v1/rerank\n"+
		"HINDSIGHT_API_RERANKER_COHERE_API_KEY=<保持原值>\n"+
		"HINDSIGHT_API_RERANKER_COHERE_MODEL=oryn-rerank\n", *eps.EnvSnippet)
	assert.NotContains(t, *eps.EnvSnippet, "sk-rerank")

	_, err = e.svc.ConfirmDeployed(ctx, nil)
	require.NoError(t, err)
	eps, err = e.svc.Endpoints(ctx)
	require.NoError(t, err)
	assert.False(t, eps.NeedsRestart, "确认重启后清除")
	assert.Nil(t, eps.EnvSnippet)

	// 删除重排接口 → rrf，需要重启，片段只有 provider 一行
	eps, err = e.svc.DeleteEndpoint(ctx, apigen.MemoryEndpointRerank)
	require.NoError(t, err)
	assert.Nil(t, eps.Rerank)
	assert.Equal(t, apigen.MemoryRerankRrf, eps.RerankMode)
	assert.True(t, eps.NeedsRestart)
	require.NotNil(t, eps.EnvSnippet)
	assert.Equal(t, "HINDSIGHT_API_RERANKER_PROVIDER=rrf\n", *eps.EnvSnippet)
	_, err = e.svc.DeleteEndpoint(ctx, apigen.MemoryEndpointRerank)
	require.NoError(t, err, "删除幂等")
	_, err = e.svc.DeleteEndpoint(ctx, apigen.MemoryEndpointEmbed)
	me := memErr(t, err)
	assert.Equal(t, http.StatusBadRequest, me.Status)
	assert.Equal(t, "invalid_request", me.Code)
	_, err = e.svc.DeleteEndpoint(ctx, apigen.MemoryEndpointKind("bogus"))
	assert.Equal(t, "not_found", memErr(t, err).Code)

	_, err = e.svc.ConfirmDeployed(ctx, nil)
	require.NoError(t, err)
	row, err := e.st.Q.GetMemorySettings(ctx)
	require.NoError(t, err)
	assert.Contains(t, row.DeployedFamilies, `"rerank":"rrf"`)

	// v5 之前的 deployed_families 没有 rerank 键：当年 .env 固定写 cohere
	_, err = e.st.Q.SetMemoryDeployedFamilies(ctx, db.SetMemoryDeployedFamiliesParams{DeployedFamilies: `{"default":"openai"}`, UpdatedAt: 3})
	require.NoError(t, err)
	eps, err = e.svc.Endpoints(ctx)
	require.NoError(t, err)
	assert.Equal(t, apigen.MemoryRerankRrf, eps.RerankMode)
	assert.True(t, eps.NeedsRestart, "旧部署按 cohere，与当前 rrf 不同")
	m, err := e.svc.Models(ctx, nil)
	require.NoError(t, err)
	for _, it := range m.Items {
		assert.False(t, it.NeedsRestart, "rerank 键不影响各用途的协议类型")
	}
}

// R16：模型选择、协议不一致时 needsRestart 与 .env 片段，确认重启后清除；不在目录或协议不支持的模型 400。
func TestMemoryAdminModels(t *testing.T) {
	ctx := context.Background()
	fams := map[string]apigen.MemoryFamily{"openai": apigen.MemoryFamilyOpenai, "claude": apigen.MemoryFamilyAnthropic}
	e := newAdmEnv(t, func(c *memory.Config) {
		c.Family = func(_ context.Context, pk, _ string) (apigen.MemoryFamily, error) {
			if f, ok := fams[pk]; ok {
				return f, nil
			}
			return "", errors.New("ChatGPT 订阅接口不能用于 Hindsight")
		}
	})
	e.configure(t, "http://127.0.0.1:1")
	catalog := []memory.CatalogModel{
		{ProviderKey: "openai", ModelID: "gpt-x", Name: "GPT X"},
		{ProviderKey: "openai", ModelID: "gpt-y", Name: "GPT Y"},
		{ProviderKey: "claude", ModelID: "haiku", Name: "Haiku"},
		{ProviderKey: "chatgpt", ModelID: "codex", Name: "Codex"},
	}
	m, err := e.svc.Models(ctx, catalog)
	require.NoError(t, err)
	require.Len(t, m.Items, 5)
	assert.Nil(t, m.Items[0].ProviderKey)
	assert.False(t, m.Items[0].FollowsDefault)
	assert.True(t, m.Items[1].FollowsDefault)
	require.Len(t, m.Options, 4)
	assert.Nil(t, m.Options[3].Family)
	assert.Contains(t, *m.Options[3].Reason, "ChatGPT")

	sel := func(p apigen.MemoryPurpose, pk, id string) struct {
		ModelID     *string              `json:"modelId,omitempty"`
		ProviderKey *string              `json:"providerKey,omitempty"`
		Purpose     apigen.MemoryPurpose `json:"purpose"`
	} {
		return struct {
			ModelID     *string              `json:"modelId,omitempty"`
			ProviderKey *string              `json:"providerKey,omitempty"`
			Purpose     apigen.MemoryPurpose `json:"purpose"`
		}{ModelID: &id, ProviderKey: &pk, Purpose: p}
	}
	up := apigen.MemoryModelsUpdate{}
	up.Items = append(up.Items, sel(apigen.MemoryPurposeRetain, "openai", "gpt-y"))
	_, err = e.svc.PutModels(ctx, up, catalog)
	assert.Equal(t, "invalid_request", memErr(t, err).Code, "default 必须指定")
	up.Items = append(up.Items, sel(apigen.MemoryPurposeDefault, "chatgpt", "codex"))
	_, err = e.svc.PutModels(ctx, up, catalog)
	assert.Equal(t, "unsupported_model", memErr(t, err).Code)
	up.Items[1] = sel(apigen.MemoryPurposeDefault, "openai", "nope")
	_, err = e.svc.PutModels(ctx, up, catalog)
	assert.Equal(t, "unsupported_model", memErr(t, err).Code)
	_, err = e.svc.PutModels(ctx, up, nil)
	assert.Equal(t, "catalog_unavailable", memErr(t, err).Code)

	up.Items[1] = sel(apigen.MemoryPurposeDefault, "openai", "gpt-x")
	up.Items = append(up.Items, sel(apigen.MemoryPurposeReflect, "claude", "haiku"))
	m, err = e.svc.PutModels(ctx, up, catalog)
	require.NoError(t, err)
	byP := map[apigen.MemoryPurpose]apigen.MemoryModelChoice{}
	for _, it := range m.Items {
		byP[it.Purpose] = it
	}
	assert.Equal(t, "gpt-y", *byP[apigen.MemoryPurposeRetain].ModelID)
	assert.False(t, byP[apigen.MemoryPurposeRetain].NeedsRestart, "同协议换模型立即生效")
	assert.True(t, byP[apigen.MemoryPurposeConsolidation].FollowsDefault)
	assert.Equal(t, "gpt-x", *byP[apigen.MemoryPurposeConsolidation].ModelID)
	assert.True(t, byP[apigen.MemoryPurposeReflect].NeedsRestart)
	require.NotNil(t, m.EnvSnippet)
	assert.Contains(t, *m.EnvSnippet, "HINDSIGHT_API_REFLECT_LLM_PROVIDER=anthropic")

	m, err = e.svc.ConfirmDeployed(ctx, catalog)
	require.NoError(t, err)
	for _, it := range m.Items {
		assert.False(t, it.NeedsRestart, it.Purpose)
	}
	assert.Nil(t, m.EnvSnippet)

	// 不带 providerKey = 跟随 default
	up = apigen.MemoryModelsUpdate{}
	up.Items = append(up.Items, sel(apigen.MemoryPurposeDefault, "openai", "gpt-x"))
	m, err = e.svc.PutModels(ctx, up, catalog)
	require.NoError(t, err)
	assert.True(t, m.Items[2].FollowsDefault)
	assert.True(t, m.Items[2].NeedsRestart, "reflect 回到 openai，与已部署的 anthropic 不同")
}

// R18：用量按 24 小时与 7 天汇总，七种用途都有一项。
func TestMemoryAdminUsage(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	hour := e.now.Truncate(time.Hour).Unix()
	require.NoError(t, e.st.Q.AddMemoryUsage(ctx, db.AddMemoryUsageParams{Hour: hour, Purpose: "retain", InputTokens: 10, OutputTokens: 2}))
	require.NoError(t, e.st.Q.AddMemoryUsage(ctx, db.AddMemoryUsageParams{Hour: hour - 3*86400, Purpose: "retain", InputTokens: 100, OutputTokens: 20}))
	require.NoError(t, e.st.Q.AddMemoryUsage(ctx, db.AddMemoryUsageParams{Hour: hour, Purpose: "embed", InputTokens: 7}))
	u, err := e.svc.Usage(ctx)
	require.NoError(t, err)
	require.Len(t, u.Items, 7)
	byP := map[string]int{}
	for i, it := range u.Items {
		byP[it.Purpose] = i
	}
	r := u.Items[byP["retain"]]
	assert.Equal(t, apigen.MemoryUsageCounts{Calls: 1, InputTokens: 10, OutputTokens: 2}, r.Day)
	assert.Equal(t, apigen.MemoryUsageCounts{Calls: 2, InputTokens: 110, OutputTokens: 22}, r.Week)
	assert.Equal(t, int64(7), u.Items[byP["embed"]].Day.InputTokens)
	assert.Zero(t, u.Items[byP["rerank"]].Week.Calls)
}
