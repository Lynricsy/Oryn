package memory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 网关（步骤 2.2）：orb 与 runner 里 Core 的 Hindsight 请求经出站代理 `/memory/t/<threadId>/…` 到达这里。
// 调用方身份（orb、线程、项目）由出站代理按 link 令牌与线程归属解析后放进 context，网关从不采信请求里的
// bank、标签与项目：
//   - 只放行附录 B 的端点，其余一律 403；路径里的 bank 一律改写为 `oryn`；
//   - 请求体只保留白名单字段，标签与标签匹配方式由网关强制；
//   - 转发时不带任何客户端请求头，Authorization 换成真实 key；应答里出现的 key 换成 `oryn-redacted`。

const (
	// maxGatewayRequest 是 orb 发来的请求体上限（自动写入会带整段会话记录）。
	maxGatewayRequest = 32 << 20
	// maxGatewayResponse 是网关读入（脱敏、过滤）的 Hindsight 应答上限。
	maxGatewayResponse = 32 << 20
	// mentalModelPage 是网关向上游翻 mental model 列表的页大小（与 Core 的 MENTAL_MODEL_PAGE_SIZE 一致）。
	mentalModelPage = 1000
	// mentalModelMaxPages 是翻页上限，防止上游分页异常时无限循环。
	mentalModelMaxPages = 50
	// redacted 是应答里真实 key 的替换值。
	redacted = "oryn-redacted"
	// bankRoot 是转发到 Hindsight 的 bank 根路径。
	bankRoot = "/v1/default/banks/" + BankID
)

// 网关错误码。
const (
	codeDisabled  = "memory_disabled"
	codeForbidden = "memory_forbidden"
	codeInvalid   = "invalid_request"
)

// 只保留这些字段（其余如 tag_groups、observation_scopes、apply_all_directives 都可能绕过标签隔离）。
var (
	retainItemFields = []string{"content", "timestamp", "context", "metadata", "document_id", "update_mode"}
	recallFields     = []string{"query", "types", "budget", "max_tokens", "query_timestamp"}
	reflectFields    = []string{"query", "budget", "context", "max_tokens"}
	modelFields      = []string{"id", "name", "source_query", "max_tokens"}
	triggerFields    = []string{"mode", "refresh_after_consolidation"}
)

// modelIDUnsafe 与 Core 生成 seed id 时的清洗规则一致（mental-models.ts 的 seedIdSuffixFromProjectTag：
// 去掉 `project:` 前缀后 trim，`[^A-Za-z0-9._-]+` → `-`，去首尾 `-`，为空时取 `project`）。
var modelIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ProjectModelIDs 返回项目 P 的 orb 允许创建的两个 mental model id（Core 的 project-conventions / project-decisions seed）。
func ProjectModelIDs(projectID string) []string {
	label := strings.TrimSpace(strings.TrimPrefix(ProjectTag(projectID), "project:"))
	label = strings.Trim(modelIDUnsafe.ReplaceAllString(label, "-"), "-")
	if label == "" {
		label = "project"
	}
	return []string{"project-conventions-" + label, "project-decisions-" + label}
}

type callerKey struct{}

// WithCaller 把可信调用方身份放进 context（只由出站代理调用）。
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom 取出 WithCaller 放入的调用方身份。
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok && c.OrbID != "" && c.ThreadID != ""
}

// WriteError 以 `{code, message}` 写出网关错误（Core 取 `detail ?? message` 作为错误详情）。
func WriteError(w http.ResponseWriter, status int, code, message string) {
	buf, _ := json.Marshal(map[string]string{"code": code, "message": message})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

// Gateway 是记忆网关，实现 http.Handler；请求的 context 必须带 WithCaller 放入的调用方身份。
type Gateway struct {
	svc *Service
}

// NewGateway 构造网关。
func NewGateway(svc *Service) *Gateway { return &Gateway{svc: svc} }

// gwReq 是一次网关请求的上下文。
type gwReq struct {
	w      http.ResponseWriter
	r      *http.Request
	caller Caller
	h      *Hindsight
	// projTag 是调用方项目的标签；无项目线程为空。
	projTag string
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, ok := CallerFrom(r.Context())
	if !ok {
		WriteError(w, http.StatusForbidden, codeForbidden, "记忆请求缺少可信的调用方身份")
		return
	}
	h, ok := g.client(r.Context(), c)
	if !ok {
		WriteError(w, http.StatusServiceUnavailable, codeDisabled, "记忆未启用")
		return
	}
	segs, ok := bankPath(r.URL.EscapedPath())
	if !ok {
		WriteError(w, http.StatusForbidden, codeForbidden, "不允许的记忆端点")
		return
	}
	q := &gwReq{w: w, r: r, caller: c, h: h}
	if c.ProjectID != nil {
		q.projTag = ProjectTag(*c.ProjectID)
	}
	m := r.Method
	switch {
	case m == http.MethodPut && len(segs) == 0:
		// 建 bank：丢弃 body，不转发（共享 bank 的 mission 只由 orynd 决定）
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxGatewayRequest))
		writeRaw(w, http.StatusOK, []byte(`{}`))
	case m == http.MethodPost && match(segs, "memories"):
		g.retain(q)
	case m == http.MethodPost && match(segs, "memories", "recall"):
		g.search(q, "/memories/recall", recallFields)
	case m == http.MethodPost && match(segs, "reflect"):
		g.search(q, "/reflect", reflectFields)
	case m == http.MethodGet && match(segs, "mental-models"):
		g.listModels(q)
	case m == http.MethodPost && match(segs, "mental-models"):
		g.createModel(q)
	case m == http.MethodGet && len(segs) == 2 && segs[0] == "mental-models":
		g.getModel(q, segs[1])
	case m == http.MethodPost && len(segs) == 3 && segs[0] == "mental-models" && segs[2] == "refresh":
		g.refreshModel(q, segs[1])
	case m == http.MethodGet && len(segs) == 3 && segs[0] == "mental-models" && segs[2] == "history":
		g.modelHistory(q, segs[1])
	default:
		// 含 DELETE mental model、/documents*、/memories/list 与一切未列出的端点（R7、R9）
		WriteError(w, http.StatusForbidden, codeForbidden, "不允许的记忆端点")
	}
}

// client 判断记忆对调用方是否生效（全局开关、已配置、项目没有单独关闭），生效时返回 Hindsight 客户端。
// 任何查询或解密出错都按未启用处理。
func (g *Gateway) client(ctx context.Context, c Caller) (*Hindsight, bool) {
	st, err := g.svc.loadSettings(ctx)
	if err != nil || st.Enabled == 0 || !configured(st) {
		return nil, false
	}
	if c.ProjectID != nil {
		on, _, err := g.svc.projectSwitch(ctx, *c.ProjectID, true)
		if err != nil || !on {
			return nil, false
		}
	}
	h, err := g.svc.clientFor(st)
	if err != nil {
		slog.Warn("记忆网关：构造 Hindsight 客户端失败", "err", err)
		return nil, false
	}
	return h, true
}

// bankPath 解析 `/v1/default/banks/<任意 bank>/…`，返回 bank 之后逐段解码的路径段。
// bank 段本身被忽略（一律改写为 oryn）；空段、`.`、`..` 与解码失败都不认。
func bankPath(escaped string) ([]string, bool) {
	segs := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
	if len(segs) < 4 || segs[0] != "v1" || segs[1] != "default" || segs[2] != "banks" || segs[3] == "" {
		return nil, false
	}
	rest := segs[4:]
	for i, s := range rest {
		d, err := url.PathUnescape(s)
		if err != nil || d == "" || d == "." || d == ".." {
			return nil, false
		}
		rest[i] = d
	}
	return rest, true
}

func match(segs []string, want ...string) bool { return slices.Equal(segs, want) }

// readBody 读入请求体（上限 maxGatewayRequest），解析为 JSON 对象；失败时已写出错误。
func (q *gwReq) readBody() (map[string]json.RawMessage, bool) {
	raw, err := io.ReadAll(io.LimitReader(q.r.Body, maxGatewayRequest+1))
	if err != nil {
		WriteError(q.w, http.StatusBadRequest, codeInvalid, "读取请求体失败")
		return nil, false
	}
	if len(raw) > maxGatewayRequest {
		WriteError(q.w, http.StatusRequestEntityTooLarge, codeInvalid, "请求体过大")
		return nil, false
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		WriteError(q.w, http.StatusBadRequest, codeInvalid, "请求体不是 JSON 对象")
		return nil, false
	}
	return body, true
}

// forward 把请求发给 Hindsight 的 bank `oryn`（rel 相对 bank 根），返回状态与脱敏后的应答体。
// 不带任何客户端请求头；body 为 nil 时不发请求体。上游不可达时已写出 502，ok 为假。
func (q *gwReq) forward(method, rel string, query url.Values, body any) (int, []byte, bool) {
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			WriteError(q.w, http.StatusInternalServerError, "internal", "编码请求失败")
			return 0, nil, false
		}
		rd = bytes.NewReader(buf)
	}
	p := bankRoot + rel
	if len(query) > 0 {
		p += "?" + query.Encode()
	}
	req, err := q.h.NewRequest(q.r.Context(), method, p, rd)
	if err != nil {
		WriteError(q.w, http.StatusInternalServerError, "internal", "构造上游请求失败")
		return 0, nil, false
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := q.h.Do(req)
	if err != nil {
		slog.Warn("记忆网关：Hindsight 不可达", "orb", q.caller.OrbID, "thread", q.caller.ThreadID, "err", err)
		WriteError(q.w, http.StatusBadGateway, "upstream_unreachable", "Hindsight 不可达")
		return 0, nil, false
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxGatewayResponse+1))
	if err != nil || len(raw) > maxGatewayResponse {
		WriteError(q.w, http.StatusBadGateway, "upstream_unreachable", "读取 Hindsight 应答失败")
		return 0, nil, false
	}
	if k := q.h.Key(); k != "" {
		raw = bytes.ReplaceAll(raw, []byte(k), []byte(redacted))
	}
	return resp.StatusCode, raw, true
}

// writeRaw 写出 JSON 应答体。
func writeRaw(w http.ResponseWriter, status int, raw []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// writeJSON 编码并写出 JSON 应答。
func writeJSON(w http.ResponseWriter, status int, v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "编码应答失败")
		return
	}
	writeRaw(w, status, buf)
}

// pick 返回 body 里白名单字段组成的新对象。
func pick(body map[string]json.RawMessage, fields []string) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(fields)+2)
	for _, f := range fields {
		if v, ok := body[f]; ok {
			out[f] = v
		}
	}
	return out
}

func rawJSON(v any) json.RawMessage {
	buf, _ := json.Marshal(v)
	return buf
}

// scopeTags 是召回与 reflect 强制使用的标签：项目线程 [project:P, oryn:global]，无项目线程 [oryn:global]。
func (q *gwReq) scopeTags() []string {
	if q.projTag == "" {
		return []string{GlobalTag}
	}
	return []string{q.projTag, GlobalTag}
}

// search 处理 recall 与 reflect：只保留白名单字段，强制 tags 与 tags_match=any_strict（附录 A 第 1 项）。
// reflect 还强制排除遗忘后尚未全量刷新的 mental model（exclude_mental_model_ids 由网关填写，客户端的值不采信）。
func (g *Gateway) search(q *gwReq, rel string, fields []string) {
	body, ok := q.readBody()
	if !ok {
		return
	}
	out := pick(body, fields)
	out["tags"] = rawJSON(q.scopeTags())
	out["tags_match"] = rawJSON("any_strict")
	if rel == "/reflect" {
		stale, ok := g.staleModels(q)
		if !ok {
			return
		}
		if len(stale) > 0 {
			ids := make([]string, 0, len(stale))
			for id := range stale {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			out["exclude_mental_model_ids"] = rawJSON(ids)
		}
	}
	if status, raw, ok := q.forward(http.MethodPost, rel, nil, out); ok {
		writeRaw(q.w, status, raw)
	}
}

// staleModels 读遗忘后尚未全量刷新的 mental model；查询出错时写出 500（失败按拒绝处理，不给出可能过期的内容）。
func (g *Gateway) staleModels(q *gwReq) (map[string]bool, bool) {
	stale, err := g.svc.StaleModelIDs(q.r.Context())
	if err != nil {
		slog.Warn("记忆网关读取待刷新的 mental model 失败", "err", err)
		WriteError(q.w, http.StatusInternalServerError, "internal", "读取 mental model 状态失败")
		return nil, false
	}
	return stale, true
}

// ---- retain ----

// errForeignDocument 表示 document_id 已归属其他项目（或全局记忆），orb 不能覆盖它。
var errForeignDocument = errors.New("document 属于其他项目")

// retain 处理写入（R5、R8）：
//   - 项目线程里带 `tags` 的条目：标签与 document_tags 强制为 [project:P]，登记 document 归属后转发；
//   - `tags` 键缺失的条目（Core 的 scope:"global"）与无项目线程的全部条目：进候选队列，不写入 Hindsight。
//
// 整个请求都进了候选队列时，按附录 A §4 伪造异步写入的成功应答（Core 的 outbox 只认 2xx）。
func (g *Gateway) retain(q *gwReq) {
	body, ok := q.readBody()
	if !ok {
		return
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(body["items"], &items); err != nil || len(items) == 0 {
		WriteError(q.w, http.StatusBadRequest, codeInvalid, "items 必须是非空数组")
		return
	}
	ctx := q.r.Context()
	now := g.svc.cfg.Now().Unix()
	var forward []map[string]json.RawMessage
	var candidates []db.InsertMemoryCandidateParams
	for _, it := range items {
		if it == nil {
			WriteError(q.w, http.StatusBadRequest, codeInvalid, "items 里的条目必须是对象")
			return
		}
		docID, ok := optString(it["document_id"])
		if !ok {
			WriteError(q.w, http.StatusBadRequest, codeInvalid, "document_id 必须是字符串")
			return
		}
		tags, hasTags := it["tags"]
		if q.projTag == "" || !hasTags || string(tags) == "null" {
			c, ok := q.candidate(it, docID, now)
			if !ok {
				return
			}
			candidates = append(candidates, c)
			continue
		}
		out := pick(it, retainItemFields)
		if docID == "" {
			docID = "oryn-" + uuid.NewString()
		}
		out["document_id"] = rawJSON(docID)
		out["tags"] = rawJSON([]string{q.projTag})
		forward = append(forward, out)
	}
	for _, c := range candidates {
		if err := g.svc.cfg.Q.InsertMemoryCandidate(ctx, c); err != nil {
			slog.Warn("记忆网关：写候选队列失败", "thread", q.caller.ThreadID, "err", err)
			WriteError(q.w, http.StatusInternalServerError, "internal", "写候选队列失败")
			return
		}
	}
	if len(forward) == 0 {
		writeJSON(q.w, http.StatusOK, map[string]any{
			"success": true, "bank_id": BankID, "items_count": len(candidates), "async": true,
			"operation_id": uuid.NewString(), "operation_ids": nil, "usage": nil,
		})
		return
	}
	for _, it := range forward {
		var docID string
		_ = json.Unmarshal(it["document_id"], &docID)
		if err := g.claimDocument(ctx, docID, *q.caller.ProjectID, q.caller.ThreadID, now); err != nil {
			if errors.Is(err, errForeignDocument) {
				WriteError(q.w, http.StatusForbidden, codeForbidden, "document_id 已属于其他项目")
			} else {
				slog.Warn("记忆网关：登记 document 失败", "thread", q.caller.ThreadID, "err", err)
				WriteError(q.w, http.StatusInternalServerError, "internal", "登记 document 失败")
			}
			return
		}
	}
	out := map[string]any{"items": forward, "document_tags": []string{q.projTag}}
	if v, ok := body["async"]; ok {
		out["async"] = v
	}
	if status, raw, ok := q.forward(http.MethodPost, "/memories", nil, out); ok {
		writeRaw(q.w, status, raw)
	}
}

// candidate 把一条写入转成候选。id 由线程与 document_id 派生，Core 重发同一事件时不会重复入队。
func (q *gwReq) candidate(it map[string]json.RawMessage, docID string, now int64) (db.InsertMemoryCandidateParams, bool) {
	var content string
	if err := json.Unmarshal(it["content"], &content); err != nil || strings.TrimSpace(content) == "" {
		WriteError(q.w, http.StatusBadRequest, codeInvalid, "全局记忆的 content 必须是非空字符串")
		return db.InsertMemoryCandidateParams{}, false
	}
	cctx, ok := optString(it["context"])
	if !ok {
		WriteError(q.w, http.StatusBadRequest, codeInvalid, "context 必须是字符串")
		return db.InsertMemoryCandidateParams{}, false
	}
	id := ids.New("mcd")
	if docID != "" {
		id = "mcd_" + ids.Hash(q.caller.ThreadID + "\x00" + docID)[:32]
	}
	c := db.InsertMemoryCandidateParams{
		ID: id, Content: content, SourceThreadID: new(q.caller.ThreadID), SourceProjectID: q.caller.ProjectID, CreatedAt: now,
	}
	if cctx != "" {
		c.Context = &cctx
	}
	return c, true
}

// claimDocument 登记 document → (项目, 线程)。document_id 已归属其他项目或全局记忆时返回 errForeignDocument：
// 否则别的项目可以用同一个 id 以 replace 覆盖它。先插入再读回，并发登记同一个 id 时只有先到者成功。
func (g *Gateway) claimDocument(ctx context.Context, docID, projectID, threadID string, now int64) error {
	cur, err := g.svc.cfg.Q.GetMemoryDocument(ctx, docID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	case cur.ProjectID == nil || *cur.ProjectID != projectID:
		return errForeignDocument
	}
	if err := g.svc.cfg.Q.UpsertMemoryDocument(ctx, db.UpsertMemoryDocumentParams{
		DocumentID: docID, ProjectID: &projectID, ThreadID: &threadID, Now: now,
	}); err != nil {
		return err
	}
	cur, err = g.svc.cfg.Q.GetMemoryDocument(ctx, docID)
	if err != nil {
		return err
	}
	if cur.ProjectID == nil || *cur.ProjectID != projectID {
		return errForeignDocument
	}
	return nil
}

// optString 解析可缺省的字符串字段；缺省或 null 时返回空串。
func optString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// ---- mental models（R9） ----

// modelScope 判断 mental model 对调用方是否可见：标签非空且全部属于 {project:P, oryn:global}。
// global 为真表示它只带全局标签（应答里把 tags 改写为 []，否则 Core 的 modelVisibleForTags 会隐藏它）。
func (q *gwReq) modelScope(m map[string]json.RawMessage) (visible, global bool) {
	var tags []string
	if err := json.Unmarshal(m["tags"], &tags); err != nil || len(tags) == 0 {
		return false, false
	}
	own := false
	for _, t := range tags {
		switch {
		case q.projTag != "" && t == q.projTag:
			own = true
		case t == GlobalTag:
		default:
			return false, false
		}
	}
	return true, !own
}

// rewriteModel 去掉溯源字段，并把全局模型的 tags 改写为 []。stale 为真（遗忘后尚未全量刷新）时内容置空：
// 条目保留，Core 看到模型已存在不会重建，又因 content 为空不会注入。
func rewriteModel(m map[string]json.RawMessage, global, stale bool) {
	delete(m, "reflect_response")
	if global {
		m["tags"] = json.RawMessage(`[]`)
	}
	if stale {
		m["content"] = json.RawMessage(`""`)
	}
}

// modelID 取 mental model 条目的 id。
func modelID(m map[string]json.RawMessage) string {
	id, _ := optString(m["id"])
	return id
}

// detailQuery 只放行 detail=metadata|content；full（含大量溯源）与其它取值降级为 content。
func detailQuery(r *http.Request) string {
	if r.URL.Query().Get("detail") == "metadata" {
		return "metadata"
	}
	return "content"
}

// listModels 翻完上游列表后只留调用方可见的模型，再按请求的 limit / offset 分页。
func (g *Gateway) listModels(q *gwReq) {
	stale, ok := g.staleModels(q)
	if !ok {
		return
	}
	detail := detailQuery(q.r)
	var all []map[string]json.RawMessage
	for page := range mentalModelMaxPages {
		status, raw, ok := q.forward(http.MethodGet, "/mental-models", url.Values{
			"detail": {detail}, "limit": {strconv.Itoa(mentalModelPage)}, "offset": {strconv.Itoa(page * mentalModelPage)},
		}, nil)
		if !ok {
			return
		}
		if status != http.StatusOK {
			writeRaw(q.w, status, raw)
			return
		}
		var resp struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			WriteError(q.w, http.StatusBadGateway, "upstream_unreachable", "解析 Hindsight 应答失败")
			return
		}
		for _, m := range resp.Items {
			if m == nil {
				continue
			}
			if vis, global := q.modelScope(m); vis {
				rewriteModel(m, global, stale[modelID(m)])
				all = append(all, m)
			}
		}
		if len(resp.Items) < mentalModelPage {
			break
		}
	}
	limit, offset := mentalModelPage, 0
	if v, err := strconv.Atoi(q.r.URL.Query().Get("limit")); err == nil && v > 0 && v <= mentalModelPage {
		limit = v
	}
	if v, err := strconv.Atoi(q.r.URL.Query().Get("offset")); err == nil && v > 0 {
		offset = v
	}
	items := []map[string]json.RawMessage{}
	if offset < len(all) {
		items = all[offset:min(offset+limit, len(all))]
	}
	writeJSON(q.w, http.StatusOK, map[string]any{"items": items, "total": len(all), "limit": limit, "offset": offset})
}

// lookupModel 读单个 mental model 并判断可见性；不可见时写出 404（与不存在一致，不泄露其他项目的模型）。
// 上游非 200 时原样写出。ok 为假时已写出应答。
func (q *gwReq) lookupModel(id, detail string) (m map[string]json.RawMessage, global, ok bool) {
	status, raw, ok := q.forward(http.MethodGet, "/mental-models/"+url.PathEscape(id), url.Values{"detail": {detail}}, nil)
	if !ok {
		return nil, false, false
	}
	if status != http.StatusOK {
		writeRaw(q.w, status, raw)
		return nil, false, false
	}
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		WriteError(q.w, http.StatusBadGateway, "upstream_unreachable", "解析 Hindsight 应答失败")
		return nil, false, false
	}
	vis, global := q.modelScope(m)
	if !vis {
		writeJSON(q.w, http.StatusNotFound, map[string]string{"detail": "Mental model not found"})
		return nil, false, false
	}
	return m, global, true
}

func (g *Gateway) getModel(q *gwReq, id string) {
	stale, ok := g.staleModels(q)
	if !ok {
		return
	}
	m, global, ok := q.lookupModel(id, detailQuery(q.r))
	if !ok {
		return
	}
	rewriteModel(m, global, stale[id] || stale[modelID(m)])
	writeJSON(q.w, http.StatusOK, m)
}

// modelHistory 只核对可见性，历史一律返回空数组：历史里的 previous_content 是模型过去的全文，遗忘之后仍含已删除的
// 事实，而 orynd 分不清哪些版本早于删除。历史只供 Core 的手动命令 `/memory mm history` 展示。
func (g *Gateway) modelHistory(q *gwReq, id string) {
	if _, _, ok := q.lookupModel(id, "metadata"); !ok {
		return
	}
	writeRaw(q.w, http.StatusOK, []byte(`[]`))
}

// refreshModel 只允许刷新本项目的模型；全局模型（含 user-preferences）与其他项目的模型一律 403。
func (g *Gateway) refreshModel(q *gwReq, id string) {
	_, _ = io.Copy(io.Discard, io.LimitReader(q.r.Body, maxGatewayRequest))
	if q.projTag == "" {
		WriteError(q.w, http.StatusForbidden, codeForbidden, "无项目线程不能刷新 mental model")
		return
	}
	_, global, ok := q.lookupModel(id, "metadata")
	if !ok {
		return
	}
	if global {
		WriteError(q.w, http.StatusForbidden, codeForbidden, "全局 mental model 由 orynd 维护")
		return
	}
	if status, raw, ok := q.forward(http.MethodPost, "/mental-models/"+url.PathEscape(id)+"/refresh", nil, map[string]any{}); ok {
		writeRaw(q.w, status, raw)
	}
}

// createModel 只允许创建 project-conventions-<P> / project-decisions-<P>，且 tags 恰为 [project:P]；
// trigger 只保留 mode 与 refresh_after_consolidation，并强制 tags_match=all_strict（只从本项目记忆提炼）。
func (g *Gateway) createModel(q *gwReq) {
	body, ok := q.readBody()
	if !ok {
		return
	}
	if q.projTag == "" {
		WriteError(q.w, http.StatusForbidden, codeForbidden, "无项目线程不能创建 mental model")
		return
	}
	id, _ := optString(body["id"])
	var tags []string
	_ = json.Unmarshal(body["tags"], &tags)
	if !slices.Contains(ProjectModelIDs(*q.caller.ProjectID), id) || !slices.Equal(tags, []string{q.projTag}) {
		WriteError(q.w, http.StatusForbidden, codeForbidden, "只能创建本项目的 mental model")
		return
	}
	out := pick(body, modelFields)
	out["tags"] = rawJSON(tags)
	trig := map[string]json.RawMessage{}
	if raw, ok := body["trigger"]; ok {
		var in map[string]json.RawMessage
		if err := json.Unmarshal(raw, &in); err == nil && in != nil {
			trig = pick(in, triggerFields)
		}
	}
	trig["tags_match"] = rawJSON("all_strict")
	out["trigger"] = rawJSON(trig)
	if status, raw, ok := q.forward(http.MethodPost, "/mental-models", nil, out); ok {
		writeRaw(q.w, status, raw)
	}
}
