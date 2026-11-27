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

	mu      sync.Mutex
	key     string
	failDel bool // DELETE 一律 500
	// failRefresh 非 0 时 mental model 的 refresh 返回这个状态
	failRefresh int
	reflects    []map[string]any
	// history 是 mental model 历史里的 previous_content（Hindsight 会一直保留旧版本全文）
	history map[string]string
	// pauses 按 "METHOD path" 暂停一次请求（不持有 mu），用来确定性地制造并发交错
	pauseMu sync.Mutex
	pauses  map[string]*admPause
	docs    map[string]*admDoc
	models  map[string]*admModel
	ops     map[string]map[string]any
	files   map[string][]byte
	calls   []string // "METHOD path"
	retains []map[string]any
	recalls []map[string]any
	refresh []string // "<id>:<mode>"
	creates []map[string]any
	patches []map[string]any
	docDels []string
	mmDels  []string
	clock   int
}

type admDoc struct {
	text    string
	tags    []string
	created string
	updated string
}

type admModel struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	SourceQuery string         `json:"source_query"`
	Tags        []string       `json:"tags"`
	Content     string         `json:"content"`
	Trigger     map[string]any `json:"trigger"`
	LastFull    string         `json:"last_full_refreshed_at,omitempty"`
}

func newAdmFake(t *testing.T) *admFake {
	t.Helper()
	f := &admFake{docs: map[string]*admDoc{}, models: map[string]*admModel{}, ops: map[string]map[string]any{}, files: map[string][]byte{}, history: map[string]string{}}
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
	f.with(func(f *admFake) { f.models[id] = &admModel{ID: id, Name: id, Tags: tags, Content: "内容 " + id} })
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

// admPause 是一次暂停：请求到达时关闭 entered，等测试关闭 release 后才继续（继续时读取的是放行那一刻的状态）。
type admPause struct{ entered, release chan struct{} }

// pause 让下一个 METHOD path 请求停在入口。
func (f *admFake) pause(method, path string) *admPause {
	p := &admPause{entered: make(chan struct{}), release: make(chan struct{})}
	f.pauseMu.Lock()
	defer f.pauseMu.Unlock()
	if f.pauses == nil {
		f.pauses = map[string]*admPause{}
	}
	f.pauses[method+" "+path] = p
	return p
}

func (f *admFake) serve(w http.ResponseWriter, r *http.Request) {
	f.pauseMu.Lock()
	hold := f.pauses[r.Method+" "+r.URL.Path]
	delete(f.pauses, r.Method+" "+r.URL.Path)
	f.pauseMu.Unlock()
	if hold != nil {
		close(hold.entered)
		<-hold.release
	}
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
	case rel == "/reflect" && r.Method == http.MethodPost:
		f.reflects = append(f.reflects, body)
		writeJSONTo(w, 200, map[string]any{"text": "反思结果"})
	case rel == "/mental-models" && r.Method == http.MethodGet:
		// all 匹配：带上无标签模型，orynd 要在客户端过滤；不带 tags（网关）时列出全部
		tag := q.Get("tags")
		items := []*admModel{}
		ids := make([]string, 0, len(f.models))
		for id := range f.models {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			m := f.models[id]
			if tag == "" || len(m.Tags) == 0 || slices.Contains(m.Tags, tag) {
				items = append(items, m)
			}
		}
		writeJSONTo(w, 200, map[string]any{"items": items})
	case rel == "/mental-models" && r.Method == http.MethodPost:
		f.creates = append(f.creates, body)
		m := &admModel{}
		raw, _ := json.Marshal(body)
		_ = json.Unmarshal(raw, m)
		m.ID = body["id"].(string)
		f.models[m.ID] = m
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
			if f.failRefresh != 0 {
				writeJSONTo(w, f.failRefresh, map[string]string{"detail": "worker busy"})
				return
			}
			// 每次提交一个新的刷新操作，初始为 pending；测试改 f.ops[op]["status"] 模拟执行进度
			op := fmt.Sprintf("op-refresh-%d", len(f.refresh))
			f.ops[op] = map[string]any{"operation_id": op, "status": "pending", "operation_type": "refresh_mental_model", "mental_model_id": id}
			writeJSONTo(w, 200, map[string]any{"operation_id": op, "status": "queued"})
		case action == "history" && r.Method == http.MethodGet:
			writeJSONTo(w, 200, []map[string]any{{"previous_content": f.history[id], "changed_at": "2027-01-15T08:00:00Z"}})
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
	cfg memory.Config
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
	e.cfg = memory.Config{Q: st.Q, Box: box, PublicURL: testPublicURL, Now: func() time.Time { return e.now }}
	if mod != nil {
		mod(&e.cfg)
	}
	e.svc = memory.New(e.cfg)
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
	require.Len(t, pend.Items, 3)
	assert.Equal(t, "PRJ_A", *pend.Items[0].ProjectID)
	assert.Contains(t, *pend.Items[0].LastError, "500")
	// 删除前已标记：遗忘尚未彻底完成，主人在列表里能看到
	assert.Equal(t, "mental-model-refresh:project-conventions-prj_a", string(pend.Items[2].DocumentID))
	assert.Contains(t, *pend.Items[2].LastError, "遗忘后等待 mental model 刷新")
}

// tombstonesOf 只留删除重试队列的条目（去掉遗忘后等待刷新的 mental model）。
func tombstonesOf(items []apigen.MemoryPendingDelete) []apigen.MemoryPendingDelete {
	var out []apigen.MemoryPendingDelete
	for _, p := range items {
		if !strings.HasPrefix(string(p.DocumentID), "mental-model-refresh:") {
			out = append(out, p)
		}
	}
	return out
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
	for _, p := range tombstonesOf(pend.Items) {
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
	for _, p := range tombstonesOf(pend.Items) {
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

// gwAs 以项目线程的身份向网关发请求（target 是 Hindsight 路径），返回状态与应答原文。
func gwAs(t *testing.T, gw *memory.Gateway, projectID, method, target string, body any) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(buf)
	}
	req := httptest.NewRequest(method, target, rd)
	req = req.WithContext(memory.WithCaller(req.Context(), memory.Caller{OrbID: "orb-x", ThreadID: "T-x", ProjectID: &projectID}))
	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// modelContent 从网关的 list 或 get 应答里取模型 id 的 content（不存在时 ok 为假）。
func modelContent(t *testing.T, raw, id string) (string, bool) {
	t.Helper()
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &resp), raw)
	if resp.Items == nil {
		var one map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &one), raw)
		resp.Items = []map[string]any{one}
	}
	for _, m := range resp.Items {
		if m["id"] == id {
			c, _ := m["content"].(string)
			return c, true
		}
	}
	return "", false
}

// 遗忘之后，mental model 的旧摘要在 Hindsight 全量刷新之前对 orb 隐藏，reflect 排除它；刷新完成后才恢复。
func TestMemoryAdminForgetHidesStaleModel(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	f := e.withFake(t)
	p := "PRJ_A"
	e.project(t, p)
	const mm, oldText, newText = "project-conventions-prj_a", "旧：用 goose 写迁移", "新：用 sqlc"
	const t0, t1 = "2027-01-15T08:00:00.000000+00:00", "2027-01-15T09:00:00.000000+00:00"
	f.addDoc("sess-a1", "我们用 goose 写迁移", memory.ProjectTag(p))
	e.record(t, "sess-a1", &p, new("T-a"))
	f.with(func(f *admFake) {
		f.models[mm] = &admModel{ID: mm, Name: "Project Conventions", Tags: []string{memory.ProjectTag(p)}, Content: oldText, LastFull: t0}
		f.history[mm] = oldText
		f.failRefresh = http.StatusServiceUnavailable
	})

	out, err := e.svc.ForgetThread(ctx, "T-a")
	require.NoError(t, err)
	assert.Equal(t, 1, out.Deleted)
	f.with(func(f *admFake) {
		assert.Equal(t, []string{mm + ":full"}, f.refresh, "删除后立即提交全量刷新")
	})

	// hidden 以 P 线程身份核对网关：list / get 的内容为空、历史为空、reflect 排除该模型
	hidden := func(gw *memory.Gateway) {
		t.Helper()
		code, raw := gwAs(t, gw, p, http.MethodGet, "/v1/default/banks/oryn/mental-models?detail=content", nil)
		require.Equal(t, http.StatusOK, code, raw)
		c, ok := modelContent(t, raw, mm)
		require.True(t, ok, "条目保留，Core 不会去重建")
		assert.Empty(t, c)
		assert.NotContains(t, raw, "goose")
		code, raw = gwAs(t, gw, p, http.MethodGet, "/v1/default/banks/oryn/mental-models/"+mm, nil)
		require.Equal(t, http.StatusOK, code, raw)
		c, _ = modelContent(t, raw, mm)
		assert.Empty(t, c)
		assert.NotContains(t, raw, "goose")
		code, raw = gwAs(t, gw, p, http.MethodGet, "/v1/default/banks/oryn/mental-models/"+mm+"/history", nil)
		require.Equal(t, http.StatusOK, code, raw)
		assert.NotContains(t, raw, "goose")
		code, raw = gwAs(t, gw, p, http.MethodPost, "/v1/default/banks/oryn/reflect",
			map[string]any{"query": "怎么写迁移", "exclude_mental_model_ids": []string{"forged"}})
		require.Equal(t, http.StatusOK, code, raw)
		f.with(func(f *admFake) {
			last := f.reflects[len(f.reflects)-1]
			assert.Equal(t, []any{mm}, last["exclude_mental_model_ids"], "网关自己填写，不采信客户端的值")
		})
	}
	hidden(memory.NewGateway(e.svc))
	pend, err := e.svc.ListPendingDeletes(ctx)
	require.NoError(t, err)
	require.Len(t, pend.Items, 1)
	assert.Equal(t, "mental-model-refresh:"+mm, string(pend.Items[0].DocumentID))
	assert.Equal(t, 1, pend.Items[0].Attempts)
	assert.Contains(t, *pend.Items[0].LastError, "503")

	// 重启后仍然隐藏（记录持久化在库里）
	restarted := memory.New(e.cfg)
	hidden(memory.NewGateway(restarted))

	// step 推进时钟后跑一轮重试循环，返回本轮清除的记录数
	step := func(d time.Duration) int {
		t.Helper()
		e.now = e.now.Add(d)
		n, err := restarted.RetryStaleModels(ctx)
		require.NoError(t, err)
		return n
	}
	refreshes := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.refresh)
	}
	setOp := func(op, status, msg string) {
		f.with(func(f *admFake) {
			f.ops[op]["status"] = status
			if msg != "" {
				f.ops[op]["error_message"] = msg
			}
		})
	}

	// 恢复：未到 2 分钟退避不重发；到期后重试循环提交全量刷新并记下 operation id
	f.with(func(f *admFake) { f.failRefresh = 0 })
	assert.Zero(t, step(time.Minute))
	assert.Equal(t, 1, refreshes(), "未到 2 分钟退避不重发")
	assert.Zero(t, step(time.Minute))
	assert.Equal(t, 2, refreshes())
	const op2 = "op-refresh-2"

	// 删除之前就开始的刷新在删除之后完成：last_full_refreshed_at 变了，内容仍来自删除之前，记录不能清除
	f.with(func(f *admFake) { f.models[mm].LastFull = t1 })
	assert.Zero(t, step(4*time.Minute))
	hidden(memory.NewGateway(restarted))
	assert.Equal(t, 2, refreshes(), "我们的刷新还在排队：不重发")

	// 执行中：继续隐藏，不重发，按退避再查
	setOp(op2, "processing", "")
	assert.Zero(t, step(8*time.Minute))
	assert.Equal(t, 2, refreshes())
	hidden(memory.NewGateway(restarted))

	// 失败：清掉 refresh_op、记下原因，到期重新提交
	setOp(op2, "failed", "LLM 超时")
	assert.Zero(t, step(16*time.Minute))
	assert.Equal(t, 2, refreshes(), "本轮只记失败，到期再提交")
	pend, err = restarted.ListPendingDeletes(ctx)
	require.NoError(t, err)
	require.Len(t, pend.Items, 1)
	assert.Contains(t, *pend.Items[0].LastError, "LLM 超时")
	hidden(memory.NewGateway(restarted))
	assert.Zero(t, step(32*time.Minute))
	assert.Equal(t, 3, refreshes(), "失败后重新提交")
	const op3 = "op-refresh-3"

	// Hindsight 已写出新内容，但我们的操作还没报告完成：仍然隐藏
	f.with(func(f *admFake) {
		f.models[mm].Content, f.models[mm].LastFull = newText, "2027-01-15T10:00:00.000000+00:00"
	})
	assert.Zero(t, step(time.Hour))
	hidden(memory.NewGateway(restarted))

	// 我们在删除之后提交的刷新完成：记录清除，网关给出新内容，旧文本不再出现在任何应答里
	setOp(op3, "completed", "")
	assert.Equal(t, 1, step(time.Hour))
	gw := memory.NewGateway(restarted)
	code, raw := gwAs(t, gw, p, http.MethodGet, "/v1/default/banks/oryn/mental-models?detail=content", nil)
	require.Equal(t, http.StatusOK, code, raw)
	c, _ := modelContent(t, raw, mm)
	assert.Equal(t, newText, c)
	code, raw2 := gwAs(t, gw, p, http.MethodGet, "/v1/default/banks/oryn/mental-models/"+mm, nil)
	require.Equal(t, http.StatusOK, code, raw2)
	c, _ = modelContent(t, raw2, mm)
	assert.Equal(t, newText, c)
	_, raw3 := gwAs(t, gw, p, http.MethodGet, "/v1/default/banks/oryn/mental-models/"+mm+"/history", nil)
	_, raw4 := gwAs(t, gw, p, http.MethodPost, "/v1/default/banks/oryn/reflect", map[string]any{"query": "怎么写迁移"})
	for _, r := range []string{raw, raw2, raw3, raw4} {
		assert.NotContains(t, r, "goose")
	}
	f.with(func(f *admFake) {
		_, has := f.reflects[len(f.reflects)-1]["exclude_mental_model_ids"]
		assert.False(t, has, "没有待刷新的模型时不排除")
	})
	pend, err = restarted.ListPendingDeletes(ctx)
	require.NoError(t, err)
	assert.Empty(t, pend.Items)
}

// 待刷新的模型已被删除（404）时清掉记录；改写全局记忆（replace）标记 user-preferences，批准与新建不标记；
// 标记失败时不删除。
func TestMemoryAdminStaleModelLifecycle(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	f := e.withFake(t)
	p := "PRJ_A"
	f.addModel("project-decisions-prj_a", memory.ProjectTag(p))
	f.addDoc("sess-a1", "a1", memory.ProjectTag(p))
	e.record(t, "sess-a1", &p, new("T-a"))
	require.NoError(t, e.svc.DeleteDocument(ctx, "sess-a1"))
	ids, err := e.svc.StaleModelIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"project-decisions-prj_a": true}, ids)
	f.with(func(f *admFake) { delete(f.models, "project-decisions-prj_a") })
	e.now = e.now.Add(2 * time.Minute) // 刷新已提交，到期后才再检查
	cleared, err := e.svc.RetryStaleModels(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, cleared)
	ids, err = e.svc.StaleModelIDs(ctx)
	require.NoError(t, err)
	assert.Empty(t, ids)

	// 批准候选与新建全局记忆只增加内容：不产生待刷新记录
	f.addModel(memory.UserPrefsModelID, memory.GlobalTag)
	require.NoError(t, e.st.Q.InsertMemoryCandidate(ctx, db.InsertMemoryCandidateParams{ID: "mcd_01J00000000000000000000009", Content: "偏好 tab", CreatedAt: 1}))
	_, err = e.svc.ApproveCandidate(ctx, "mcd_01J00000000000000000000009", nil)
	require.NoError(t, err)
	doc, err := e.svc.CreateGlobal(ctx, "迁移必须可回滚")
	require.NoError(t, err)
	ids, err = e.svc.StaleModelIDs(ctx)
	require.NoError(t, err)
	assert.Empty(t, ids)

	// 改写（replace）会删掉旧事实：user-preferences 标为待刷新并提交全量刷新
	_, err = e.svc.UpdateGlobal(ctx, doc.ID, "迁移必须可回滚，且写 Down")
	require.NoError(t, err)
	ids, err = e.svc.StaleModelIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{memory.UserPrefsModelID: true}, ids)
	f.with(func(f *admFake) { assert.Equal(t, memory.UserPrefsModelID+":full", f.refresh[len(f.refresh)-1]) })

	// 标记失败（列不出 mental model）时不删除 document
	f.addDoc("oryn-g9", "全局", memory.GlobalTag)
	e.record(t, "oryn-g9", nil, nil)
	f.with(func(f *admFake) { f.key = "rotated" })
	assert.Equal(t, "memory_upstream", memErr(t, e.svc.DeleteDocument(ctx, "oryn-g9")).Code)
	f.with(func(f *admFake) { assert.Contains(t, f.docs, "oryn-g9") })
}

// staleRow 读一个模型的待刷新记录。
func staleRow(t *testing.T, e *admEnv, id string) db.MemoryStaleModel {
	t.Helper()
	row, err := e.st.Q.GetMemoryStaleModel(context.Background(), id)
	require.NoError(t, err)
	return row
}

// holdCount 是模型当前持有的删除 hold 数。
func holdCount(t *testing.T, e *admEnv, id string) int {
	t.Helper()
	var n int
	require.NoError(t, e.st.DB.QueryRow(`SELECT COUNT(*) FROM memory_stale_holds WHERE model_id = ?`, id).Scan(&n))
	return n
}

// staleEnv 是并发测试的公共准备：项目 P 有模型 project-conventions-prj_a，以及 n 个各归一个线程（T-1…T-n）的 document。
func staleEnv(t *testing.T, n int) (*admEnv, *admFake, string) {
	t.Helper()
	e := newAdmEnv(t, nil)
	f := e.withFake(t)
	p := "PRJ_A"
	e.project(t, p)
	tag := memory.ProjectTag(p)
	f.addModel("project-conventions-prj_a", tag)
	for i := 1; i <= n; i++ {
		doc := fmt.Sprintf("sess-%d", i)
		f.addDoc(doc, "段落 "+doc, tag)
		e.record(t, doc, &p, new(fmt.Sprintf("T-%d", i)))
	}
	return e, f, p
}

// assertHidden 以 P 线程身份核对网关仍隐藏模型内容。
func assertHidden(t *testing.T, e *admEnv, p, mm string) {
	t.Helper()
	code, raw := gwAs(t, memory.NewGateway(e.svc), p, http.MethodGet, "/v1/default/banks/oryn/mental-models?detail=content", nil)
	require.Equal(t, http.StatusOK, code, raw)
	c, ok := modelContent(t, raw, mm)
	require.True(t, ok)
	assert.Empty(t, c, "仍然隐藏")
}

func (f *admFake) refreshCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.refresh)
}

func (f *admFake) setOp(op, status string) {
	f.with(func(f *admFake) { f.ops[op]["status"] = status })
}

// retryOnce 跑一轮重试循环并返回清除数。
func retryOnce(t *testing.T, e *admEnv) int {
	t.Helper()
	n, err := e.svc.RetryStaleModels(context.Background())
	require.NoError(t, err)
	return n
}

// 并发 D1（ABA，旧的清除晚到）：循环读到遗忘 #1 的记录、查询 op1 时停住；期间记录被清除、遗忘 #2 重新插入同一模型；
// op1 随后报告完成，旧请求按旧令牌清除也碰不到新记录。之后只有遗忘 #2 自己的刷新完成才清除。
func TestMemoryAdminStaleLateClear(t *testing.T) {
	ctx := context.Background()
	e, f, p := staleEnv(t, 2)
	const mm = "project-conventions-prj_a"
	_, err := e.svc.ForgetThread(ctx, "T-1")
	require.NoError(t, err)
	row1 := staleRow(t, e, mm)
	require.NotNil(t, row1.RefreshOp)
	assert.Equal(t, "op-refresh-1", *row1.RefreshOp)

	e.now = e.now.Add(2 * time.Minute)
	pz := f.pause(http.MethodGet, "/v1/default/banks/oryn/operations/op-refresh-1")
	done := make(chan int, 1)
	go func() {
		n, err := e.svc.RetryStaleModels(ctx)
		assert.NoError(t, err)
		done <- n
	}()
	<-pz.entered
	// 记录被清除后，遗忘 #2 重新插入同一模型（按代际递增的方案里新记录会从头计数，与旧请求相同）
	require.NoError(t, e.st.Q.DeleteMemoryStaleModel(ctx, mm))
	_, err = e.svc.ForgetThread(ctx, "T-2")
	require.NoError(t, err)
	row2 := staleRow(t, e, mm)
	assert.NotEqual(t, row1.Token, row2.Token)
	require.NotNil(t, row2.RefreshOp)
	assert.Equal(t, "op-refresh-2", *row2.RefreshOp)
	f.setOp("op-refresh-1", "completed")
	close(pz.release)
	assert.Zero(t, <-done, "旧令牌的清除碰不到新记录")

	assert.Equal(t, row2, staleRow(t, e, mm), "记录与 refresh_op 都没被旧请求改动")
	assertHidden(t, e, p, mm)
	e.now = e.now.Add(2 * time.Minute)
	assert.Zero(t, retryOnce(t, e), "op2 还在排队")
	f.setOp("op-refresh-2", "completed")
	e.now = e.now.Add(4 * time.Minute)
	assert.Equal(t, 1, retryOnce(t, e))
}

// 并发 D2（ABA，旧的写回晚到）：遗忘 #1 删除后立即提交刷新时停住（已读到旧令牌）；期间记录被清除、遗忘 #2 重新插入
// 并记下自己的刷新；遗忘 #1 的提交随后返回，按旧令牌的写回作废，refresh_op 仍是遗忘 #2 的。
func TestMemoryAdminStaleLateRetryWriteBack(t *testing.T) {
	ctx := context.Background()
	e, f, p := staleEnv(t, 2)
	const mm = "project-conventions-prj_a"
	pz := f.pause(http.MethodPost, "/v1/default/banks/oryn/mental-models/"+mm+"/refresh")
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.ForgetThread(ctx, "T-1")
		done <- err
	}()
	<-pz.entered
	token1 := staleRow(t, e, mm).Token
	require.NoError(t, e.st.Q.DeleteMemoryStaleModel(ctx, mm))
	_, err := e.svc.ForgetThread(ctx, "T-2")
	require.NoError(t, err)
	row2 := staleRow(t, e, mm)
	assert.NotEqual(t, token1, row2.Token)
	require.NotNil(t, row2.RefreshOp)
	assert.Equal(t, "op-refresh-1", *row2.RefreshOp, "遗忘 #2 的提交先被处理")
	close(pz.release)
	require.NoError(t, <-done)
	assert.Equal(t, 2, f.refreshCount(), "遗忘 #1 的提交照常发出")
	assert.Equal(t, row2, staleRow(t, e, mm), "按旧令牌的写回作废，refresh_op 没被改成遗忘 #1 的操作")
	assertHidden(t, e, p, mm)
	f.setOp("op-refresh-2", "completed") // 遗忘 #1 的操作完成也无关
	e.now = e.now.Add(2 * time.Minute)
	assert.Zero(t, retryOnce(t, e))
	f.setOp("op-refresh-1", "completed")
	e.now = e.now.Add(4 * time.Minute)
	assert.Equal(t, 1, retryOnce(t, e))
}

// 并发 E（重叠删除乱序结束）：A 标记后删除停住；B 标记同一模型并删完。A 的 hold 还在，此时不提交刷新，循环推进
// 时钟也不提交；A 删完释放后恰好提交一次，那次操作完成才清除。随后：B 的刷新在 A 标记之前提交、在 A 标记之后完成，
// 也不清除（令牌已变）。
func TestMemoryAdminStaleOverlappingDeletes(t *testing.T) {
	ctx := context.Background()
	e, f, p := staleEnv(t, 4)
	const mm = "project-conventions-prj_a"
	forgetPaused := func(thread, doc string) (*admPause, chan error) {
		pz := f.pause(http.MethodDelete, "/v1/default/banks/oryn/documents/"+doc)
		done := make(chan error, 1)
		go func() {
			_, err := e.svc.ForgetThread(ctx, thread)
			done <- err
		}()
		<-pz.entered
		return pz, done
	}

	pzA, doneA := forgetPaused("T-1", "sess-1")
	_, err := e.svc.ForgetThread(ctx, "T-2")
	require.NoError(t, err)
	assert.Equal(t, 1, holdCount(t, e, mm), "只剩 A 的 hold")
	assert.Zero(t, f.refreshCount(), "A 的删除仍在进行：B 删完也不提交刷新")
	e.now = e.now.Add(10 * time.Minute)
	assert.Zero(t, retryOnce(t, e))
	assert.Zero(t, f.refreshCount(), "循环同样不提交")
	assertHidden(t, e, p, mm)
	close(pzA.release)
	require.NoError(t, <-doneA)
	assert.Zero(t, holdCount(t, e, mm))
	assert.Equal(t, 1, f.refreshCount(), "A 删完后恰好提交一次")
	row := staleRow(t, e, mm)
	require.NotNil(t, row.RefreshOp)
	assert.Equal(t, "op-refresh-1", *row.RefreshOp)
	e.now = e.now.Add(2 * time.Minute)
	assert.Zero(t, retryOnce(t, e), "操作未完成不清除")
	f.setOp("op-refresh-1", "completed")
	e.now = e.now.Add(4 * time.Minute)
	assert.Equal(t, 1, retryOnce(t, e))

	// B 的刷新在 A 标记之前提交、在 A 标记之后完成：不清除
	_, err = e.svc.ForgetThread(ctx, "T-3")
	require.NoError(t, err)
	assert.Equal(t, "op-refresh-2", *staleRow(t, e, mm).RefreshOp)
	pzA, doneA = forgetPaused("T-4", "sess-4")
	f.setOp("op-refresh-2", "completed")
	e.now = e.now.Add(10 * time.Minute)
	assert.Zero(t, retryOnce(t, e), "A 的 hold 还在：不处理")
	close(pzA.release)
	require.NoError(t, <-doneA)
	row = staleRow(t, e, mm)
	require.NotNil(t, row.RefreshOp)
	assert.Equal(t, "op-refresh-3", *row.RefreshOp, "A 自己提交的刷新")
	e.now = e.now.Add(2 * time.Minute)
	assert.Zero(t, retryOnce(t, e), "B 的操作已完成也不清除：令牌已变，记录上是 A 的操作")
	assertHidden(t, e, p, mm)
	f.setOp("op-refresh-3", "completed")
	e.now = e.now.Add(4 * time.Minute)
	assert.Equal(t, 1, retryOnce(t, e))
}

// 并发 B：标记之后、删除结束之前（删除在假 Hindsight 里停住），时钟走到任何退避之后，重试循环也不提交刷新
// （这时刷新会读到删除之前的数据）；删除结束后才提交。
func TestMemoryAdminStaleNotReadyDuringDelete(t *testing.T) {
	ctx := context.Background()
	e, f, _ := staleEnv(t, 1)
	const mm = "project-conventions-prj_a"
	pz := f.pause(http.MethodDelete, "/v1/default/banks/oryn/documents/sess-1")
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.ForgetThread(ctx, "T-1")
		done <- err
	}()
	<-pz.entered
	assert.Equal(t, 1, holdCount(t, e, mm), "删除进行中")
	ids, err := e.svc.StaleModelIDs(ctx)
	require.NoError(t, err)
	assert.True(t, ids[mm], "删除进行中已经隐藏")

	e.now = e.now.Add(10 * time.Minute)
	assert.Zero(t, retryOnce(t, e))
	assert.Zero(t, f.refreshCount(), "删除结束之前不提交刷新")

	close(pz.release)
	require.NoError(t, <-done)
	f.with(func(f *admFake) { assert.Equal(t, []string{mm + ":full"}, f.refresh, "删除结束后立即提交") })
	assert.Zero(t, holdCount(t, e, mm))
	require.NotNil(t, staleRow(t, e, mm).RefreshOp)
}

// 并发 C：上个进程在删除中途退出，留下 hold；循环不处理该模型，新的 Service 启动时清空 hold，之后循环提交刷新。
func TestMemoryAdminStaleRecoverAfterRestart(t *testing.T) {
	ctx := context.Background()
	e := newAdmEnv(t, nil)
	f := e.withFake(t)
	p := "PRJ_A"
	const mm = "project-conventions-prj_a"
	f.addModel(mm, memory.ProjectTag(p))
	require.NoError(t, e.st.Q.InsertMemoryStaleHold(ctx, db.InsertMemoryStaleHoldParams{HoldID: "leftover", ModelID: mm, CreatedAt: e.now.Unix()}))
	require.NoError(t, e.st.Q.UpsertMemoryStaleModel(ctx, db.UpsertMemoryStaleModelParams{ModelID: mm, ProjectID: &p, Now: e.now.Unix(), Token: "t0", Bank: memory.BankID}))

	e.now = e.now.Add(time.Hour)
	assert.Zero(t, retryOnce(t, e))
	assert.Zero(t, f.refreshCount(), "有 hold 的模型不处理")

	restarted := memory.New(e.cfg)
	assert.Zero(t, holdCount(t, e, mm), "启动时清空 hold")
	_, err := restarted.RetryStaleModels(ctx)
	require.NoError(t, err)
	f.with(func(f *admFake) { assert.Equal(t, []string{mm + ":full"}, f.refresh) })
	require.NotNil(t, staleRow(t, e, mm).RefreshOp)
	ids, err := restarted.StaleModelIDs(ctx)
	require.NoError(t, err)
	assert.True(t, ids[mm], "刷新完成之前仍然隐藏")
}
