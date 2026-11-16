package memory

// admin.go：管理页经 orynd 操作 Hindsight（步骤 2.6）：浏览与召回、删除 document、按线程遗忘、全局候选与全局记忆、
// mental models、导出。所有请求只发往 bank `oryn`，标签一律由这里按项目或全局计算，从不采信请求里的标签。
// Hindsight 删除 document 后派生的事实与 observation 会级联消失，但 mental model 只被标成 is_stale（附录 A 第 5 项），
// 所以每次删除、遗忘或改写全局记忆后都主动刷新受影响的 mental model。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	// UserPrefsModelID 是 orynd 维护的全局 mental model；只从 `oryn:global` 记忆中提炼（U3）。
	UserPrefsModelID = "user-preferences"
	// 以下取自 Core 的种子 `packages/coding-agent/src/hindsight/seeds.json`（user-preferences）。
	userPrefsName      = "User Preferences"
	userPrefsQuery     = "What does the user prefer in coding style, tooling, communication, and review? Capture only durable preferences expressed across sessions, not one-off requests."
	userPrefsMaxTokens = 600

	// adminBank 是 bank `oryn` 的 API 前缀。
	adminBank = "/v1/default/banks/" + BankID
	// previewRunes 是 document 预览的最大字数。
	previewRunes = 400
	// adminTimeout 是一次管理操作访问 Hindsight 的总时限；同步 retain 要等 LLM 抽取事实，给得宽一些。
	adminTimeout = 3 * time.Minute
	// previewWorkers 是并发读取 document 原文的上限。
	previewWorkers = 8
	// exportDownloadPrefix 是 Hindsight 导出包的下载路径前缀；只代理这个前缀，防止带着 Bearer 去取别的地址。
	exportDownloadPrefix = "/v1/default/files/download/"
)

// exportIDRe 与 OpenAPI 的 MemoryExportIdPath 一致。
var exportIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func errInvalid(msg string) error {
	return &Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: msg}
}

func errNotFound(msg string) error {
	return &Error{Status: http.StatusNotFound, Code: "not_found", Message: msg}
}

// errUpstream 把访问 Hindsight 的失败包装成 502；领域错误原样返回。
func errUpstream(what string, err error) error {
	var me *Error
	if errors.As(err, &me) {
		return err
	}
	return &Error{Status: http.StatusBadGateway, Code: "memory_upstream", Message: fmt.Sprintf("%s失败：%v", what, err)}
}

// adminClient 是管理接口用到的 Hindsight 端点（只在本文件与 purge.go 里使用）。
type adminClient struct{ *Hindsight }

// admin 按当前设置构造 Hindsight 客户端；未配置时返回 ErrNotConfigured。
func (s *Service) admin(ctx context.Context) (adminClient, error) {
	h, err := s.Client(ctx)
	if err != nil {
		return adminClient{}, err
	}
	return adminClient{h}, nil
}

// hsDocument 是 Hindsight document 里 orynd 关心的字段。
type hsDocument struct {
	ID           string   `json:"id"`
	OriginalText string   `json:"original_text"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	Tags         []string `json:"tags"`
}

func (c adminClient) listDocuments(ctx context.Context, tag string, offset, limit int) ([]hsDocument, int, error) {
	q := url.Values{"tags": {tag}, "tags_match": {"any_strict"}, "offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}}
	var out struct {
		Items []hsDocument `json:"items"`
		Total int          `json:"total"`
	}
	if err := c.call(ctx, http.MethodGet, adminBank+"/documents?"+q.Encode(), nil, &out); err != nil {
		return nil, 0, err
	}
	return out.Items, out.Total, nil
}

func docPath(id string) string { return adminBank + "/documents/" + url.PathEscape(id) }

func (c adminClient) getDocument(ctx context.Context, id string) (hsDocument, error) {
	var out hsDocument
	err := c.call(ctx, http.MethodGet, docPath(id), nil, &out)
	return out, err
}

// deleteDocument 删除 document；Hindsight 里已经没有它（404）也算成功。
func (c adminClient) deleteDocument(ctx context.Context, id string) error {
	err := c.call(ctx, http.MethodDelete, docPath(id), nil, nil)
	if statusOf(err) == http.StatusNotFound {
		return nil
	}
	return err
}

// retainGlobal 同步写入一条全局记忆：标签固定为 `oryn:global`；同一 document_id 以 replace 方式重写。
// 用同步写入是为了随后刷新 user-preferences 时新事实已经抽取完成。
func (c adminClient) retainGlobal(ctx context.Context, docID, content string, context_ *string) error {
	item := map[string]any{
		"content": content, "document_id": docID, "tags": []string{GlobalTag}, "update_mode": "replace",
	}
	if context_ != nil && *context_ != "" {
		item["context"] = *context_
	}
	body := map[string]any{"items": []any{item}, "document_tags": []string{GlobalTag}, "async": false}
	return c.call(ctx, http.MethodPost, adminBank+"/memories", body, nil)
}

// hsMentalModel 是 Hindsight mental model 里 orynd 关心的字段。
type hsMentalModel struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	SourceQuery     *string         `json:"source_query"`
	Content         *string         `json:"content"`
	Tags            []string        `json:"tags"`
	LastRefreshedAt *string         `json:"last_refreshed_at"`
	Trigger         json.RawMessage `json:"trigger"`
}

func mmPath(id string) string { return adminBank + "/mental-models/" + url.PathEscape(id) }

// listMentalModels 列出带 tag 的 mental model，并在客户端再按 tags 过滤一遍（Hindsight 的 all 匹配会带上无标签的模型）。
func (c adminClient) listMentalModels(ctx context.Context, tag string, detail string) ([]hsMentalModel, error) {
	q := url.Values{"tags": {tag}, "tags_match": {"all"}, "detail": {detail}, "limit": {"1000"}}
	var out struct {
		Items []hsMentalModel `json:"items"`
	}
	if err := c.call(ctx, http.MethodGet, adminBank+"/mental-models?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	items := out.Items[:0]
	for _, m := range out.Items {
		if hasTag(m.Tags, tag) && (tag == GlobalTag || !hasTag(m.Tags, GlobalTag)) {
			items = append(items, m)
		}
	}
	return items, nil
}

// refreshMentalModel 提交一次异步刷新，返回 Hindsight 的 operation id；full 为真时从头重新生成
// （删除记忆后用，避免增量刷新保留已删除的内容）。
func (c adminClient) refreshMentalModel(ctx context.Context, id string, full bool) (string, error) {
	body := map[string]any{}
	if full {
		body["mode"] = "full"
	}
	var resp struct {
		OperationID string `json:"operation_id"`
	}
	err := c.call(ctx, http.MethodPost, mmPath(id)+"/refresh", body, &resp)
	return resp.OperationID, err
}

// deleteMentalModel 删除 mental model；不存在（404）也算成功。
func (c adminClient) deleteMentalModel(ctx context.Context, id string) error {
	err := c.call(ctx, http.MethodDelete, mmPath(id), nil, nil)
	if statusOf(err) == http.StatusNotFound {
		return nil
	}
	return err
}

// userPrefsSpec 是 user-preferences 的期望配置：只从 `oryn:global` 记忆中提炼（all_strict 排除无标签记忆，附录 A 第 2 项）。
func userPrefsSpec() map[string]any {
	return map[string]any{
		"name": userPrefsName, "source_query": userPrefsQuery, "tags": []string{GlobalTag}, "max_tokens": userPrefsMaxTokens,
		"trigger": map[string]any{"mode": "delta", "refresh_after_consolidation": true, "tags_match": "all_strict"},
	}
}

// ensureUserPrefs 保证 user-preferences 存在且只从全局记忆提炼：不存在时创建（Hindsight 创建后自动刷新一次），
// 标签或匹配方式不对（例如早先由 Core 按无标签种子创建）时改回来。返回是否新建。
func (c adminClient) ensureUserPrefs(ctx context.Context) (bool, error) {
	var cur hsMentalModel
	err := c.call(ctx, http.MethodGet, mmPath(UserPrefsModelID), nil, &cur)
	if statusOf(err) == http.StatusNotFound {
		body := userPrefsSpec()
		body["id"] = UserPrefsModelID
		if err := c.call(ctx, http.MethodPost, adminBank+"/mental-models", body, nil); err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var trig struct {
		TagsMatch *string `json:"tags_match"`
	}
	_ = json.Unmarshal(cur.Trigger, &trig)
	if len(cur.Tags) == 1 && cur.Tags[0] == GlobalTag && trig.TagsMatch != nil && *trig.TagsMatch == "all_strict" {
		return false, nil
	}
	return false, c.call(ctx, http.MethodPatch, mmPath(UserPrefsModelID), userPrefsSpec(), nil)
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// parseHSTime 把 Hindsight 的 ISO 8601 时间转成 Unix 秒；解析失败返回 0。
func parseHSTime(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// preview 取原文开头最多 previewRunes 个字。
func preview(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > previewRunes {
		return string(r[:previewRunes])
	}
	return s
}

// scopeTag 返回 document 归属的标签：项目 → `project:<P>`，全局 → `oryn:global`。
func scopeTag(projectID *string) string {
	if projectID == nil {
		return GlobalTag
	}
	return ProjectTag(*projectID)
}

// refreshScopes 刷新这些标签下的 mental model：`oryn:global` 刷新 user-preferences，项目标签刷新该项目的全部模型。
// 失败只记日志：触发刷新的删除或写入已经成功，模型下次刷新时会追上。
func (s *Service) refreshScopes(ctx context.Context, c adminClient, tags map[string]bool, full bool) {
	for tag := range tags {
		if tag == GlobalTag {
			created, err := c.ensureUserPrefs(ctx)
			if err == nil && !created {
				_, err = c.refreshMentalModel(ctx, UserPrefsModelID, full)
			}
			if err != nil {
				slog.Warn("刷新 user-preferences 失败", "err", err)
			}
			continue
		}
		models, err := c.listMentalModels(ctx, tag, "metadata")
		if err != nil {
			slog.Warn("列出项目 mental model 失败", "tag", tag, "err", err)
			continue
		}
		for _, m := range models {
			if _, err := c.refreshMentalModel(ctx, m.ID, full); err != nil {
				slog.Warn("刷新 mental model 失败", "model", m.ID, "err", err)
			}
		}
	}
}

// threadTitles 查线程标题；线程已删除时没有标题。
func (s *Service) threadTitle(ctx context.Context, threadID *string, cache map[string]*string) *string {
	if threadID == nil {
		return nil
	}
	if t, ok := cache[*threadID]; ok {
		return t
	}
	var title *string
	if th, err := s.cfg.Q.GetThread(ctx, *threadID); err == nil {
		title = &th.Title
	}
	cache[*threadID] = title
	return title
}

// ListDocuments 按项目（标签 `project:P`）或全局（`oryn:global`）分页列出 document，按更新时间倒序（R19）。
// 线程归属取自 memory_documents；全局记忆附带完整内容。
func (s *Service) ListDocuments(ctx context.Context, projectID *string, global bool, offset, limit int) (apigen.MemoryDocumentList, error) {
	if (projectID == nil) == !global {
		return apigen.MemoryDocumentList{}, errInvalid("projectId 与 global 须二选一")
	}
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	offset = max(offset, 0)
	c, err := s.admin(ctx)
	if err != nil {
		return apigen.MemoryDocumentList{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	var scope *string
	if !global {
		scope = projectID
	}
	docs, total, err := c.listDocuments(ctx, scopeTag(scope), offset, limit)
	if err != nil {
		return apigen.MemoryDocumentList{}, errUpstream("列出记忆", err)
	}
	// 列表里没有原文，逐个读取（限并发）
	texts := make([]string, len(docs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, previewWorkers)
	for i, d := range docs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			if full, err := c.getDocument(ctx, d.ID); err == nil {
				texts[i] = full.OriginalText
			}
		}()
	}
	wg.Wait()
	titles := map[string]*string{}
	out := apigen.MemoryDocumentList{Items: make([]apigen.MemoryDocument, 0, len(docs)), HasMore: offset+len(docs) < total}
	for i, d := range docs {
		item := apigen.MemoryDocument{ID: d.ID, Preview: preview(texts[i]), CreatedAt: parseHSTime(d.CreatedAt), UpdatedAt: parseHSTime(d.UpdatedAt)}
		if global {
			item.Content = &texts[i]
		} else {
			item.ProjectID = projectID
		}
		if row, err := s.cfg.Q.GetMemoryDocument(ctx, d.ID); err == nil {
			item.ThreadID = row.ThreadID
			item.ThreadTitle = s.threadTitle(ctx, row.ThreadID, titles)
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// Search 在项目（含全局）或仅全局范围内召回（R19）：标签由 orynd 计算，tags_match 固定 any_strict（附录 A 第 1 项），
// 结果再按标签过滤一遍，不属于该范围的一律丢弃。
func (s *Service) Search(ctx context.Context, q string, projectID *string) (apigen.MemorySearchResult, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return apigen.MemorySearchResult{}, errInvalid("q 不能为空")
	}
	tags := []string{GlobalTag}
	if projectID != nil {
		tags = []string{ProjectTag(*projectID), GlobalTag}
	}
	c, err := s.admin(ctx)
	if err != nil {
		return apigen.MemorySearchResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	var resp struct {
		Results []struct {
			Text        string   `json:"text"`
			Type        *string  `json:"type"`
			DocumentID  *string  `json:"document_id"`
			MentionedAt *string  `json:"mentioned_at"`
			Tags        []string `json:"tags"`
		} `json:"results"`
	}
	if err := c.call(ctx, http.MethodPost, adminBank+"/memories/recall",
		map[string]any{"query": q, "tags": tags, "tags_match": "any_strict"}, &resp); err != nil {
		return apigen.MemorySearchResult{}, errUpstream("召回", err)
	}
	out := apigen.MemorySearchResult{Items: []apigen.MemorySearchHit{}}
	for _, r := range resp.Results {
		allowed := false
		for _, t := range tags {
			allowed = allowed || hasTag(r.Tags, t)
		}
		if !allowed {
			continue
		}
		global := hasTag(r.Tags, GlobalTag)
		out.Items = append(out.Items, apigen.MemorySearchHit{Text: r.Text, Type: r.Type, DocumentID: r.DocumentID, MentionedAt: r.MentionedAt, Global: &global})
	}
	return out, nil
}

// DeleteDocument 删除一个 document（连同派生记忆），并让它所在范围的 mental model 在全量刷新前对 orb 隐藏（R19）。
// 归属先查 memory_documents，查不到再看 Hindsight 里的标签；两边都没有时 404。标记失败时不删除。
func (s *Service) DeleteDocument(ctx context.Context, docID string) error {
	c, err := s.admin(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	scopes := forgetScopes{}
	row, err := s.cfg.Q.GetMemoryDocument(ctx, docID)
	switch {
	case err == nil:
		scopes.add(row.ProjectID)
	case errors.Is(err, sql.ErrNoRows):
		doc, err := c.getDocument(ctx, docID)
		if statusOf(err) == http.StatusNotFound {
			return errNotFound("记忆不存在")
		}
		if err != nil {
			return errUpstream("读取记忆", err)
		}
		for _, t := range doc.Tags {
			if t == GlobalTag || strings.HasPrefix(t, "project:") {
				scopes[t] = nil
			}
		}
	default:
		return err
	}
	marks, err := s.markStale(ctx, c, scopes)
	if err != nil {
		return errUpstream("标记待刷新的 mental model", err)
	}
	// 删除结束（成功或失败）才释放 hold；defer 兜底，任何返回路径都释放
	defer s.releaseStale(ctx, marks)
	derr := c.deleteDocument(ctx, docID)
	s.releaseStale(ctx, marks)
	if derr != nil {
		return errUpstream("删除记忆", derr)
	}
	if _, err := s.cfg.Q.DeleteMemoryDocument(ctx, docID); err != nil {
		return err
	}
	if err := s.cfg.Q.DeleteMemoryTombstone(ctx, docID); err != nil {
		return err
	}
	s.refreshStale(ctx, c, marks)
	return nil
}

// ForgetThread 删除该线程写入的全部 document（memory_documents 里记到这个线程的），其它线程的不动（R19）。
// 删除失败的进入重试队列计入 pending；受影响项目的 mental model 在全量刷新前对 orb 隐藏。
func (s *Service) ForgetThread(ctx context.Context, threadID string) (apigen.MemoryForgetResult, error) {
	rows, err := s.cfg.Q.ListMemoryDocumentsByThread(ctx, &threadID)
	if err != nil {
		return apigen.MemoryForgetResult{}, err
	}
	if len(rows) == 0 {
		return apigen.MemoryForgetResult{}, nil
	}
	docs := make([]tombstoneTarget, 0, len(rows))
	for _, r := range rows {
		docs = append(docs, tombstoneTarget{id: r.DocumentID, projectID: r.ProjectID})
	}
	deleted, pending, err := s.deleteDocuments(ctx, docs, true)
	if err != nil {
		return apigen.MemoryForgetResult{}, err
	}
	return apigen.MemoryForgetResult{Deleted: deleted, Pending: pending}, nil
}

// ListCandidates 列出待批准的全局候选（新的在前）。
func (s *Service) ListCandidates(ctx context.Context) (apigen.MemoryCandidateList, error) {
	rows, err := s.cfg.Q.ListMemoryCandidates(ctx)
	if err != nil {
		return apigen.MemoryCandidateList{}, err
	}
	titles := map[string]*string{}
	out := apigen.MemoryCandidateList{Items: make([]apigen.MemoryCandidate, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, apigen.MemoryCandidate{
			ID: r.ID, Content: r.Content, Context: r.Context, CreatedAt: r.CreatedAt,
			SourceThreadID: r.SourceThreadID, SourceProjectID: r.SourceProjectID,
			SourceThreadTitle: s.threadTitle(ctx, r.SourceThreadID, titles),
		})
	}
	return out, nil
}

// RejectCandidate 拒绝候选：直接删除，不碰 Hindsight（R20）。
func (s *Service) RejectCandidate(ctx context.Context, id string) error {
	n, err := s.cfg.Q.DeleteMemoryCandidate(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return errNotFound("候选不存在")
	}
	return nil
}

// ApproveCandidate 批准候选（content 非空时用改写后的内容），写入 Hindsight 成为全局记忆，随后刷新 user-preferences（R20、R21）。
// 写入失败时候选保留。
func (s *Service) ApproveCandidate(ctx context.Context, id string, content *string) (apigen.MemoryDocument, error) {
	cand, err := s.cfg.Q.GetMemoryCandidate(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.MemoryDocument{}, errNotFound("候选不存在")
	}
	if err != nil {
		return apigen.MemoryDocument{}, err
	}
	text := cand.Content
	if content != nil {
		text = *content
	}
	doc, err := s.writeGlobal(ctx, newGlobalDocID(), text, cand.Context, false)
	if err != nil {
		return apigen.MemoryDocument{}, err
	}
	if _, err := s.cfg.Q.DeleteMemoryCandidate(ctx, id); err != nil {
		return apigen.MemoryDocument{}, err
	}
	return doc, nil
}

// CreateGlobal 新建一条全局记忆（R20）。
func (s *Service) CreateGlobal(ctx context.Context, content string) (apigen.MemoryDocument, error) {
	return s.writeGlobal(ctx, newGlobalDocID(), content, nil, false)
}

// UpdateGlobal 改写全局记忆：同一 document_id、update_mode=replace（R20）。只接受全局 document。
func (s *Service) UpdateGlobal(ctx context.Context, docID, content string) (apigen.MemoryDocument, error) {
	row, err := s.cfg.Q.GetMemoryDocument(ctx, docID)
	switch {
	case err == nil:
		if row.ProjectID != nil {
			return apigen.MemoryDocument{}, errNotFound("全局记忆不存在")
		}
	case errors.Is(err, sql.ErrNoRows):
		c, err := s.admin(ctx)
		if err != nil {
			return apigen.MemoryDocument{}, err
		}
		gctx, cancel := context.WithTimeout(ctx, adminTimeout)
		doc, err := c.getDocument(gctx, docID)
		cancel()
		if statusOf(err) == http.StatusNotFound || (err == nil && !hasTag(doc.Tags, GlobalTag)) {
			return apigen.MemoryDocument{}, errNotFound("全局记忆不存在")
		}
		if err != nil {
			return apigen.MemoryDocument{}, errUpstream("读取记忆", err)
		}
	default:
		return apigen.MemoryDocument{}, err
	}
	return s.writeGlobal(ctx, docID, content, nil, true)
}

// newGlobalDocID 是管理页写入的 document id：`oryn-<ulid>`。
func newGlobalDocID() string { return "oryn-" + strings.TrimPrefix(ids.New("x"), "x_") }

// writeGlobal 以标签 `oryn:global` 写入（或以 replace 重写）一条全局记忆，记进 memory_documents（project 为 NULL）。
// 新建只增加内容，随后刷新 user-preferences；replace 会删掉旧内容的事实，属于遗忘：写入之前先把全局模型标为待刷新
// （标记失败时不写），写入后提交全量刷新。
func (s *Service) writeGlobal(ctx context.Context, docID, content string, context_ *string, replace bool) (apigen.MemoryDocument, error) {
	content = strings.TrimSpace(content)
	if content == "" || len([]rune(content)) > 4000 {
		return apigen.MemoryDocument{}, errInvalid("内容须为 1–4000 字")
	}
	c, err := s.admin(ctx)
	if err != nil {
		return apigen.MemoryDocument{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	var marks []staleMark
	if replace {
		if marks, err = s.markStale(ctx, c, forgetScopes{GlobalTag: nil}); err != nil {
			return apigen.MemoryDocument{}, errUpstream("标记待刷新的 mental model", err)
		}
		// 改写结束（成功或失败）才释放 hold；defer 兜底
		defer s.releaseStale(ctx, marks)
	}
	rerr := c.retainGlobal(ctx, docID, content, context_)
	s.releaseStale(ctx, marks)
	if rerr != nil {
		return apigen.MemoryDocument{}, errUpstream("写入全局记忆", rerr)
	}
	now := s.cfg.Now().Unix()
	if err := s.cfg.Q.UpsertMemoryDocument(ctx, db.UpsertMemoryDocumentParams{DocumentID: docID, Now: now}); err != nil {
		return apigen.MemoryDocument{}, err
	}
	created := now
	if row, err := s.cfg.Q.GetMemoryDocument(ctx, docID); err == nil {
		created = row.CreatedAt
	}
	if replace {
		s.refreshStale(ctx, c, marks)
	} else {
		s.refreshScopes(ctx, c, map[string]bool{GlobalTag: true}, false)
	}
	return apigen.MemoryDocument{ID: docID, Preview: preview(content), Content: &content, CreatedAt: created, UpdatedAt: now}, nil
}

// ListMentalModels 列出项目（标签 `project:P`）或全局（`oryn:global`，即 user-preferences）的 mental model（R21）。
// 列全局时先保证 user-preferences 存在。
func (s *Service) ListMentalModels(ctx context.Context, projectID *string) (apigen.MemoryMentalModelList, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return apigen.MemoryMentalModelList{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	if projectID == nil {
		if _, err := c.ensureUserPrefs(ctx); err != nil {
			return apigen.MemoryMentalModelList{}, errUpstream("创建 user-preferences", err)
		}
	}
	models, err := c.listMentalModels(ctx, scopeTag(projectID), "content")
	if err != nil {
		return apigen.MemoryMentalModelList{}, errUpstream("列出 mental model", err)
	}
	out := apigen.MemoryMentalModelList{Items: make([]apigen.MemoryMentalModel, 0, len(models))}
	for _, m := range models {
		out.Items = append(out.Items, apigen.MemoryMentalModel{
			ID: m.ID, Name: m.Name, Global: projectID == nil, ProjectID: projectID,
			Content: m.Content, SourceQuery: m.SourceQuery, LastRefreshedAt: m.LastRefreshedAt,
		})
	}
	return out, nil
}

// RefreshMentalModel 手动提交一次刷新（R21）。user-preferences 不存在时先创建（创建即刷新）。
func (s *Service) RefreshMentalModel(ctx context.Context, id string) error {
	c, err := s.admin(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	if id == UserPrefsModelID {
		created, err := c.ensureUserPrefs(ctx)
		if err != nil {
			return errUpstream("创建 user-preferences", err)
		}
		if created {
			return nil
		}
	}
	_, err = c.refreshMentalModel(ctx, id, false)
	if statusOf(err) == http.StatusNotFound {
		return errNotFound("mental model 不存在")
	}
	if err != nil {
		return errUpstream("刷新 mental model", err)
	}
	return nil
}

// EnsureUserPrefs 在记忆启用时保证 user-preferences 存在（后台循环调用）。
func (s *Service) EnsureUserPrefs(ctx context.Context) error {
	if !s.Enabled(ctx) {
		return nil
	}
	c, err := s.admin(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	_, err = c.ensureUserPrefs(ctx)
	return err
}

// hsOperation 是 `operations/{id}` 里 orynd 关心的字段。
type hsOperation struct {
	Status         string  `json:"status"`
	OperationType  *string `json:"operation_type"`
	CreatedAt      *string `json:"created_at"`
	ErrorMessage   *string `json:"error_message"`
	ResultMetadata struct {
		DownloadURL string `json:"download_url"`
	} `json:"result_metadata"`
}

// StartExport 提交整个 bank 的异步导出（R22）；导出 id 即 Hindsight 的 operation id。
func (s *Service) StartExport(ctx context.Context) (apigen.MemoryExport, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return apigen.MemoryExport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	var resp struct {
		OperationID string `json:"operation_id"`
	}
	if err := c.call(ctx, http.MethodPost, adminBank+"/transfer/export", nil, &resp); err != nil {
		return apigen.MemoryExport{}, errUpstream("开始导出", err)
	}
	if !exportIDRe.MatchString(resp.OperationID) {
		return apigen.MemoryExport{}, errUpstream("开始导出", fmt.Errorf("Hindsight 返回了无效的 operation id"))
	}
	return apigen.MemoryExport{ID: resp.OperationID, State: apigen.MemoryExportRunning, CreatedAt: s.cfg.Now().Unix()}, nil
}

// exportOp 读导出操作；不存在或不是导出操作时 404。
func (s *Service) exportOp(ctx context.Context, c adminClient, id string) (hsOperation, error) {
	if !exportIDRe.MatchString(id) {
		return hsOperation{}, errNotFound("导出不存在")
	}
	var op hsOperation
	if err := c.call(ctx, http.MethodGet, adminBank+"/operations/"+url.PathEscape(id), nil, &op); err != nil {
		if statusOf(err) == http.StatusNotFound {
			return hsOperation{}, errNotFound("导出不存在")
		}
		return hsOperation{}, errUpstream("查询导出", err)
	}
	if op.Status == "not_found" || op.OperationType == nil || *op.OperationType != "export_bank" {
		return hsOperation{}, errNotFound("导出不存在")
	}
	return op, nil
}

// GetExport 查询导出进度（R22）。
func (s *Service) GetExport(ctx context.Context, id string) (apigen.MemoryExport, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return apigen.MemoryExport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	op, err := s.exportOp(ctx, c, id)
	if err != nil {
		return apigen.MemoryExport{}, err
	}
	out := apigen.MemoryExport{ID: id, State: apigen.MemoryExportRunning}
	if op.CreatedAt != nil {
		out.CreatedAt = parseHSTime(*op.CreatedAt)
	}
	switch op.Status {
	case "completed":
		out.State = apigen.MemoryExportDone
	case "failed", "cancelled":
		out.State = apigen.MemoryExportFailed
		msg := "导出失败"
		if op.ErrorMessage != nil && *op.ErrorMessage != "" {
			msg = *op.ErrorMessage
		}
		out.Error = &msg
	}
	return out, nil
}

// ExportArchive 是代理下载的导出包；调用方负责关闭 Body。
type ExportArchive struct {
	Body     io.ReadCloser
	Size     int64
	Filename string
}

// OpenExport 由 orynd 带 Bearer 从 Hindsight 取导出包（附录 A 第 6 项：下载要鉴权，不能把地址交给浏览器）。
// 未完成时 409 `export_not_ready`。文件名由 orynd 决定，Hindsight 的响应头一概不转发。
func (s *Service) OpenExport(ctx context.Context, id string) (ExportArchive, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return ExportArchive{}, err
	}
	qctx, cancel := context.WithTimeout(ctx, adminTimeout)
	op, err := s.exportOp(qctx, c, id)
	cancel()
	if err != nil {
		return ExportArchive{}, err
	}
	if op.Status != "completed" {
		return ExportArchive{}, &Error{Status: http.StatusConflict, Code: "export_not_ready", Message: "导出尚未完成"}
	}
	dl := op.ResultMetadata.DownloadURL
	if !strings.HasPrefix(dl, exportDownloadPrefix) || strings.Contains(dl, "..") {
		return ExportArchive{}, errUpstream("下载导出", fmt.Errorf("Hindsight 返回了意外的下载地址"))
	}
	req, err := c.NewRequest(ctx, http.MethodGet, dl, nil)
	if err != nil {
		return ExportArchive{}, err
	}
	req.Header.Set("Accept", "application/zip, application/octet-stream")
	resp, err := c.Do(req)
	if err != nil {
		return ExportArchive{}, errUpstream("下载导出", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		return ExportArchive{}, errUpstream("下载导出", &HTTPError{Status: resp.StatusCode, Body: truncate(string(raw), 300)})
	}
	return ExportArchive{Body: resp.Body, Size: resp.ContentLength, Filename: "oryn-memory-" + id + ".zip"}, nil
}

// ListPendingDeletes 列出删除失败、等待重试的条目（R23），以及遗忘之后仍在等待全量刷新的 mental model
// （documentId 为 `mental-model-refresh:<模型 id>`）：它们在列表里说明遗忘尚未彻底完成。
func (s *Service) ListPendingDeletes(ctx context.Context) (apigen.MemoryPendingDeleteList, error) {
	rows, err := s.cfg.Q.ListMemoryTombstones(ctx)
	if err != nil {
		return apigen.MemoryPendingDeleteList{}, err
	}
	stale, err := s.cfg.Q.ListMemoryStaleModels(ctx)
	if err != nil {
		return apigen.MemoryPendingDeleteList{}, err
	}
	out := apigen.MemoryPendingDeleteList{Items: make([]apigen.MemoryPendingDelete, 0, len(rows)+len(stale))}
	for _, r := range rows {
		out.Items = append(out.Items, apigen.MemoryPendingDelete{
			DocumentID: r.DocumentID, ProjectID: r.ProjectID, Attempts: int(r.Attempts),
			NextAttemptAt: r.NextAttemptAt, LastError: r.LastError,
		})
	}
	for _, r := range stale {
		reason := staleReason
		if r.LastError != nil {
			reason += "（上次提交刷新失败：" + *r.LastError + "）"
		}
		out.Items = append(out.Items, apigen.MemoryPendingDelete{
			DocumentID: staleRefreshPrefix + r.ModelID, ProjectID: r.ProjectID, Attempts: int(r.Attempts),
			NextAttemptAt: r.NextAttemptAt, LastError: &reason,
		})
	}
	return out, nil
}
