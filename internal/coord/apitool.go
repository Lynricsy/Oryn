package coord

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// oryn 工具（全局助手）：一个顶层工具执行 oryn 技能里记载的全部操作。REST 操作在进程内走与网页相同的处理器，
// 按 apiPolicy 定级（read 直接执行、write 计限速并审计、confirm 挂确认卡、deny 拒绝）；技能原生操作按名字转给现有实现。
const (
	ToolOryn = "oryn"
	// apiCallTimeout 是单个 REST 操作的缺省时限。
	apiCallTimeout = 60 * time.Second
	// apiRespMax 是进程内调用缓冲的响应上限；超出部分丢弃，只记总长。
	apiRespMax = 4 << 20
	// apiResultCap 是给模型的正文上限。
	apiResultCap = 64 << 10
	// apiConfirmDetailMax 是确认执行结果消息里附带的响应正文上限。
	apiConfirmDetailMax = 3000
	// actionAPICall 是 oryn 执行 REST 操作的审计动作与确认卡动作。
	actionAPICall = string(apigen.ActionAPICall)
)

// API 是进程内的 REST 调用入口（*api.Internal 实现）。
type API interface {
	Do(ctx context.Context, threadID, method, target, contentType string, body []byte, max int) (status int, header http.Header, out []byte, total int)
}

// SetAPI 设置 REST 调用入口；须在服务对外可用之前调用。未设置时 REST 操作回 coordinator_unavailable。
func (s *Service) SetAPI(a API) { s.api = a }

// CodeSecretsFound 是分享与发布前的密钥扫描发现疑似密钥（REST 的 409 secrets_found）。
const CodeSecretsFound = "secrets_found"

// allowShare 按 shareRules 与 mutateRules 给分享类写操作与确认请求计数（每个对话）。
func (s *Service) allowShare(c toolCtx) error {
	if !s.lim.allow(c.ThreadID+"|share", s.cfg.Now(), shareRules) {
		return errf(CodeRateLimited, "sharing limit reached (10 per 10 minutes per conversation)")
	}
	return s.allowMutate(c)
}

// orbTarget 取 orb 类操作的目标：不存在或已删除 → not_found；协调宿主、预热池里未被认领的 orb、助手的电脑与 box
// 不能作目标（它们由控制面自己管理）→ invalid_target。
func (s *Service) orbTarget(ctx context.Context, orbID string) (db.Orb, error) {
	if orbID == "" {
		return db.Orb{}, errf(CodeInvalid, "orb id is required")
	}
	o, err := s.st.Q.GetOrb(ctx, orbID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && o.DeletedAt != nil) {
		return db.Orb{}, errf(CodeNotFound, "orb %s not found", orbID)
	}
	if err != nil {
		return db.Orb{}, err
	}
	ps := ""
	if o.PoolState != nil {
		ps = *o.PoolState
	}
	switch {
	case o.Coordinator:
		return db.Orb{}, errf(CodeInvalidTarget, "orb %s is the coordinator host; it is managed by Oryn", orbID)
	case o.Computer == 1:
		return db.Orb{}, errf(CodeInvalidTarget, "orb %s is your own computer; it is managed by Oryn", orbID)
	case o.Box == 1:
		return db.Orb{}, errf(CodeInvalidTarget, "orb %s is a box; use the box operations", orbID)
	case orbs.IsSandbox(o):
		return db.Orb{}, errf(CodeInvalidTarget, "orb %s is a sandbox; it is managed by the thread that created it", orbID)
	case ps == orbs.PoolWarming || ps == orbs.PoolIdle:
		return db.Orb{}, errf(CodeInvalidTarget, "orb %s is a warm pool orb; it is managed by Oryn", orbID)
	}
	return o, nil
}

func orynToolDef() apigen.OmpFrame {
	return essentialTool(ToolOryn, "Run one Oryn operation documented in the oryn skill (skill://oryn). Read the operation's doc first; never guess names or arguments.", obj{
		"op": str("Operation name exactly as documented, e.g. updateProject or abort_thread."),
		"args": obj{"type": "object", "additionalProperties": true,
			"description": `Arguments of the operation: path and query parameters by name, the request body under "body".`},
	}, "op")
}

// toolOryn 执行 oryn{op, args}：技能原生操作转给 HostToolCall，其余按 REST 操作执行。
func (s *Service) toolOryn(ctx context.Context, c toolCtx) threads.HostToolResult {
	var a struct {
		Op   string          `json:"op"`
		Args json.RawMessage `json:"args"`
	}
	if err := decodeArgs(c.Args, &a); err != nil {
		return failResult(err)
	}
	if a.Op == "" {
		return failResult(errf(CodeInvalid, "op is required; read skill://oryn for the list"))
	}
	if len(a.Args) == 0 || string(a.Args) == "null" {
		a.Args = json.RawMessage(`{}`)
	}
	if _, ok := skillNativeDomain[a.Op]; ok {
		d := s.HostToolTimeout(a.Op)
		if d == 0 {
			d = apiCallTimeout
		}
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		call := c.HostToolCall
		call.Name, call.Args = a.Op, a.Args
		return s.HostToolCall(ctx, call)
	}
	return s.apiOp(ctx, c, a.Op, a.Args)
}

// apiOp 执行一个 REST 操作。
func (s *Service) apiOp(ctx context.Context, c toolCtx, op string, args json.RawMessage) threads.HostToolResult {
	rt, ok := apiRoutes()[op]
	if !ok {
		return failResult(errf(CodeUnknownOp, "no operation named %q; read skill://oryn for the list", op))
	}
	pol := apiPolicy[op]
	if pol.Class == opDeny {
		return failResult(apiDenied(op, pol))
	}
	if s.api == nil {
		return failResult(errf(CodeCoordinatorUnavailable, "Oryn operations are not configured"))
	}
	req, err := buildAPIRequest(rt, args)
	if err != nil {
		return failResult(err)
	}
	class := pol.Class
	if class == opWrite && pol.ConfirmWhen != nil && pol.ConfirmWhen(req.bodyObj, req.query) {
		class = opConfirm
	}
	if class == opRead {
		if err := s.checkAPITarget(ctx, c, pol, req); err != nil {
			return failResult(err)
		}
		return apiToolResult(s.execAPI(ctx, c.ThreadID, pol, req))
	}

	auditThread := ""
	if pol.Guard == guardThread {
		auditThread = req.pathVals["id"]
	}
	summary := req.summary(op)
	err = s.checkAPITarget(ctx, c, pol, req)
	if err == nil {
		err = s.allowMutate(c)
	}
	if err == nil && pol.Share {
		err = s.allowShare(c)
	}
	if err != nil {
		s.audit(ctx, threads.KindAssistant, c.ThreadID, c.OwnerTurn, actionAPICall, auditThread, "", summary, err)
		return failResult(err)
	}
	if class == opConfirm {
		v, err := s.requestConfirm(ctx, c, s.apiConfirmReq(ctx, op, rt, pol, req, args, auditThread))
		if err != nil {
			s.audit(ctx, threads.KindAssistant, c.ThreadID, c.OwnerTurn, actionAPICall, auditThread, "", summary, err)
			return failResult(err)
		}
		return threads.HostToolResult{Text: jsonText(v), Details: v}
	}
	res := s.execAPI(ctx, c.ThreadID, pol, req)
	_, _, err = res.present()
	s.audit(ctx, threads.KindAssistant, c.ThreadID, c.OwnerTurn, actionAPICall, auditThread, "", summary, err)
	return apiToolResult(res)
}

func apiDenied(op string, pol opPolicy) error {
	why := pol.Deny
	if why == "" {
		why = "not a user operation"
	}
	return errf(CodeNotAvailable, "%s is not available to you: %s", op, why)
}

// checkAPITarget 跑策略的额外参数规则与目标守卫。
func (s *Service) checkAPITarget(ctx context.Context, c toolCtx, pol opPolicy, req apiRequest) error {
	if pol.Check != nil {
		if err := pol.Check(req.pathVals); err != nil {
			return err
		}
	}
	switch pol.Guard {
	case guardThread:
		_, err := s.resolveActTarget(ctx, c, req.pathVals["id"], false)
		return err
	case guardOrb:
		_, err := s.orbTarget(ctx, req.pathVals["id"])
		return err
	}
	return nil
}

// apiResponse 是一次进程内调用的结果。
type apiResponse struct {
	op     string
	status int
	header http.Header
	out    []byte
	total  int
}

// execAPI 以对话 threadID 的身份执行请求（时限取策略的 Timeout，缺省 apiCallTimeout）。
func (s *Service) execAPI(ctx context.Context, threadID string, pol opPolicy, req apiRequest) apiResponse {
	d := pol.Timeout
	if d == 0 {
		d = apiCallTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	status, hdr, out, total := s.api.Do(ctx, threadID, req.method, req.target, req.contentType, req.body, apiRespMax)
	return apiResponse{op: req.op, status: status, header: hdr, out: out, total: total}
}

// present 把响应转成给模型的文本：成功时 text 是加了不可信包裹的正文（过长截断），details 是未截断的 JSON；
// 失败时 err 带 REST 的错误码。
func (r apiResponse) present() (text string, details any, err error) {
	if r.status >= 400 {
		return "", nil, apiError(r)
	}
	if r.total == 0 {
		v := map[string]any{"ok": true, "status": r.status}
		return jsonText(v), v, nil
	}
	ct := r.header.Get("Content-Type")
	mt, _, _ := mime.ParseMediaType(ct)
	isJSON := mt == "application/json" || strings.HasSuffix(mt, "+json")
	body, truncated := capUTF8(r.out, apiResultCap)
	if !isJSON && !strings.HasPrefix(mt, "text/") && !utf8.Valid(body) {
		return "", nil, errf(CodeBinaryResult, "%s returned binary data (%s)", r.op, ct)
	}
	truncated = truncated || r.total > len(r.out)
	text = string(body)
	if truncated {
		text += fmt.Sprintf("\n…[truncated: %d bytes; narrow the query with its limit or filter parameters]", r.total)
	} else if isJSON && json.Valid(r.out) {
		details = json.RawMessage(r.out)
	}
	return wrapUntrustedText(text), details, nil
}

func apiToolResult(r apiResponse) threads.HostToolResult {
	text, details, err := r.present()
	if err != nil {
		return failResult(err)
	}
	return threads.HostToolResult{Text: text, Details: details}
}

// apiError 把 ≥ 400 的响应转成同码错误；secrets_found 不回传扫描结果，只给数量。
func apiError(r apiResponse) error {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Scan    struct {
			Findings []json.RawMessage `json:"findings"`
		} `json:"scan"`
	}
	if json.Unmarshal(r.out, &e) == nil && e.Code != "" {
		if e.Code == CodeSecretsFound {
			return errf(e.Code, "%d secret-like string(s) found; nothing was shared or published. Tell the owner; retry with acknowledgeFindings only if the owner says so.", len(e.Scan.Findings))
		}
		return errf(e.Code, "%s", e.Message)
	}
	msg, _ := capUTF8(bytes.TrimSpace(r.out), 500)
	if len(msg) == 0 {
		msg = []byte(http.StatusText(r.status))
	}
	return errf("http_"+strconv.Itoa(r.status), "%s", msg)
}

// capUTF8 把 b 截到至多 n 字节，不切开 UTF-8 字符。
func capUTF8(b []byte, n int) ([]byte, bool) {
	if len(b) <= n {
		return b, false
	}
	b = b[:n]
	for i := 0; i < utf8.UTFMax && len(b) > 0; i++ {
		if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size > 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return b, true
}

// ---- 请求构造 ----

// apiRequest 是由 oryn 的 args 构造出的一次 REST 请求。
type apiRequest struct {
	op          string
	method      string
	target      string // 已转义的路径加 ?query
	path        string // 解码后的路径（显示用）
	pathVals    map[string]string
	query       url.Values
	contentType string
	body        []byte
	bodyObj     map[string]any // JSON 对象请求体（ConfirmWhen 用）；其余为 nil
	redacted    string         // 打码后的紧凑 args JSON（空 args 为空串）
}

// summary 是审计摘要：<op> <解码后的路径> 加打码后的 args。
func (r apiRequest) summary(op string) string {
	s := op + " " + r.path
	if r.redacted != "" {
		s += " " + r.redacted
	}
	return s
}

// buildAPIRequest 按路由把 args 映射成请求：path 参数必填、query 参数按名字、请求体在 body 下；未知参数与未知的请求体顶层字段一律拒绝。
func buildAPIRequest(rt apiRoute, raw json.RawMessage) (apiRequest, error) {
	req := apiRequest{op: rt.Op, method: rt.Method, pathVals: map[string]string{}, query: url.Values{}}
	args := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return req, errf(CodeInvalid, "args of %s must be an object", rt.Op)
		}
	}
	allowed := make([]string, 0, len(rt.PathParams)+len(rt.QueryParams)+1)
	known := map[string]bool{}
	for _, p := range rt.PathParams {
		allowed = append(allowed, p.Name)
		known[p.Name] = true
	}
	for _, p := range rt.QueryParams {
		allowed = append(allowed, p.Name)
		known[p.Name] = true
	}
	if rt.BodyType != "" {
		allowed = append(allowed, "body")
		known["body"] = true
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if k == "body" && rt.BodyType == "" {
			return req, errf(CodeInvalid, "%s takes no body", rt.Op)
		}
		if !known[k] {
			return req, errf(CodeInvalid, "unknown argument %q for %s; allowed: %s", k, rt.Op, strings.Join(allowed, ", "))
		}
	}

	escaped, display := rt.Path, rt.Path
	for _, p := range rt.PathParams {
		v, ok := args[p.Name]
		if !ok {
			return req, errf(CodeInvalid, "path parameter %q is required for %s", p.Name, rt.Op)
		}
		s, ok := jsonScalar(v, false)
		if !ok || s == "" {
			return req, errf(CodeInvalid, "path parameter %q of %s must be a non-empty string or number", p.Name, rt.Op)
		}
		req.pathVals[p.Name] = s
		escaped = strings.ReplaceAll(escaped, "{"+p.Name+"}", url.PathEscape(s))
		display = strings.ReplaceAll(display, "{"+p.Name+"}", s)
	}
	req.path = display

	for _, p := range rt.QueryParams {
		v, ok := args[p.Name]
		if !ok || isJSONNull(v) {
			continue
		}
		if t := bytes.TrimSpace(v); len(t) > 0 && t[0] == '[' {
			var items []json.RawMessage
			if err := json.Unmarshal(t, &items); err != nil {
				return req, errf(CodeInvalid, "query parameter %q of %s: %s", p.Name, rt.Op, err.Error())
			}
			for _, it := range items {
				s, ok := jsonScalar(it, true)
				if !ok {
					return req, errf(CodeInvalid, "query parameter %q of %s: array items must be strings, numbers or booleans", p.Name, rt.Op)
				}
				req.query.Add(p.Name, s)
			}
			continue
		}
		s, ok := jsonScalar(v, true)
		if !ok {
			return req, errf(CodeInvalid, "query parameter %q of %s must be a string, number, boolean or array", p.Name, rt.Op)
		}
		req.query.Add(p.Name, s)
	}
	req.target = escaped
	if len(req.query) > 0 {
		req.target += "?" + req.query.Encode()
	}

	body, hasBody := args["body"]
	if hasBody && isJSONNull(body) {
		hasBody = false
	}
	switch {
	case !hasBody && rt.BodyRequired:
		return req, errf(CodeInvalid, "body is required for %s", rt.Op)
	case !hasBody:
	case rt.BodyType == "application/octet-stream":
		var text string
		if err := json.Unmarshal(body, &text); err != nil {
			return req, errf(CodeInvalid, "body of %s must be a string (the file text)", rt.Op)
		}
		req.contentType, req.body = rt.BodyType, []byte(text)
	default:
		t := bytes.TrimSpace(body)
		if len(t) == 0 || (t[0] != '{' && t[0] != '[') {
			return req, errf(CodeInvalid, "body of %s must be a JSON object or array", rt.Op)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, t); err != nil {
			return req, errf(CodeInvalid, "body of %s: %s", rt.Op, err.Error())
		}
		if t[0] == '{' {
			if err := json.Unmarshal(t, &req.bodyObj); err != nil {
				return req, errf(CodeInvalid, "body of %s: %s", rt.Op, err.Error())
			}
			if fields, check := bodyFields(rt.BodySchema); check {
				for _, k := range slices.Sorted(maps.Keys(req.bodyObj)) {
					if !slices.Contains(fields, k) {
						return req, errf(CodeInvalid, "unknown body field %q for %s; fields: %s", k, rt.Op, strings.Join(fields, ", "))
					}
				}
			}
		}
		req.contentType, req.body = rt.BodyType, compact.Bytes()
	}
	req.redacted = redactArgs(args, rt.BodyType == "application/octet-stream")
	return req, nil
}

func isJSONNull(v json.RawMessage) bool { return string(bytes.TrimSpace(v)) == "null" }

// jsonScalar 取 JSON 字符串或数字的文本；allowBool 时布尔取字面量。其余类型返回 false。
func jsonScalar(v json.RawMessage, allowBool bool) (string, bool) {
	t := bytes.TrimSpace(v)
	if len(t) == 0 {
		return "", false
	}
	switch {
	case t[0] == '"':
		var s string
		if json.Unmarshal(t, &s) != nil {
			return "", false
		}
		return s, true
	case string(t) == "true" || string(t) == "false":
		return string(t), allowBool
	case t[0] == '-' || (t[0] >= '0' && t[0] <= '9'):
		var n json.Number
		if json.Unmarshal(t, &n) != nil {
			return "", false
		}
		return n.String(), true
	}
	return "", false
}

// bodyFields 返回请求 schema（合并 allOf）的顶层字段；schema 含 oneOf / anyOf、允许额外字段或没有 properties 时 check 为假。
func bodyFields(schema map[string]any) (fields []string, check bool) {
	var walk func(map[string]any) bool
	walk = func(sc map[string]any) bool {
		if sc == nil {
			return true
		}
		if _, ok := sc["oneOf"]; ok {
			return false
		}
		if _, ok := sc["anyOf"]; ok {
			return false
		}
		if ap, ok := sc["additionalProperties"]; ok && ap != false {
			return false
		}
		if props, ok := sc["properties"].(map[string]any); ok {
			for k := range props {
				if !slices.Contains(fields, k) {
					fields = append(fields, k)
				}
			}
		}
		if all, ok := sc["allOf"].([]any); ok {
			for _, part := range all {
				m, _ := part.(map[string]any)
				if !walk(m) {
					return false
				}
			}
		}
		return true
	}
	if !walk(schema) || len(fields) == 0 {
		return nil, false
	}
	slices.Sort(fields)
	return fields, true
}

// redactKeys 是确认卡正文与审计摘要里要打码的键（小写比较）。
var redactKeys = map[string]bool{
	"value": true, "secret": true, "token": true, "bottoken": true, "devicekey": true, "apikey": true,
	"clientsecret": true, "password": true, "privatekey": true, "passphrase": true, "headers": true,
}

// redactArgs 返回打码后的紧凑 args JSON（空 args 为空串）：redactKeys 里的键值为非空字符串、对象或数组时换成 "***"；
// octetBody 为真时 body 显示为 "<N bytes>"。
func redactArgs(args map[string]json.RawMessage, octetBody bool) string {
	if len(args) == 0 {
		return ""
	}
	out := make(map[string]any, len(args))
	for k, raw := range args {
		if k == "body" && octetBody {
			var text string
			_ = json.Unmarshal(raw, &text)
			out[k] = fmt.Sprintf("<%d bytes>", len(text))
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if dec.Decode(&v) != nil {
			continue
		}
		out[k] = v
	}
	return jsonText(redactValue(out))
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if redactKeys[strings.ToLower(k)] && sensitiveValue(val) {
				t[k] = "***"
				continue
			}
			t[k] = redactValue(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = redactValue(val)
		}
		return t
	}
	return v
}

func sensitiveValue(v any) bool {
	switch t := v.(type) {
	case string:
		return t != ""
	case map[string]any, []any:
		return true
	}
	return false
}

// ---- 确认卡 ----

// apiConfirmArgs 是 api_call 确认卡的执行参数。
type apiConfirmArgs struct {
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args"`
}

func (s *Service) apiConfirmReq(ctx context.Context, op string, rt apiRoute, pol opPolicy, req apiRequest, args json.RawMessage, auditThread string) confirmReq {
	label := s.apiTargetLabel(ctx, rt, req)
	targetLabel, title := rt.Summary, "Allow the assistant to run "+op+"?"
	if targetLabel == "" {
		targetLabel = op
	}
	if label != "" {
		targetLabel += " · " + label
		title = "Allow the assistant to run " + op + " on " + label + "?"
	}
	lines := []string{op + " — " + rt.Method + " " + req.path}
	if req.redacted != "" && req.redacted != "{}" {
		lines = append(lines, req.redacted)
	}
	if pol.Note != "" {
		lines = append(lines, pol.Note)
	}
	return confirmReq{
		Action: actionAPICall, Target: op + " " + req.path, TargetLabel: targetLabel, Title: title,
		Summary: strings.Join(lines, "\n"), AuditThread: auditThread, Args: apiConfirmArgs{Op: op, Args: args},
	}
}

// apiTargetLabel 是确认卡上的目标标签：线程、项目、Space 的 id 换成「名字 (id)」（查不到就用 id），其余 path 参数用原值，
// 多个用 " / " 连接。
func (s *Service) apiTargetLabel(ctx context.Context, rt apiRoute, req apiRequest) string {
	segs := strings.Split(strings.TrimPrefix(rt.Path, "/"), "/")
	kind := ""
	if len(segs) > 2 {
		kind = segs[2] // api / v1 / <kind>
	}
	parts := make([]string, 0, len(rt.PathParams))
	for _, p := range rt.PathParams {
		v := req.pathVals[p.Name]
		if p.Name == "id" {
			if name := s.apiObjectName(ctx, kind, v); name != "" {
				v = name + " (" + v + ")"
			}
		}
		parts = append(parts, v)
	}
	return strings.Join(parts, " / ")
}

func (s *Service) apiObjectName(ctx context.Context, kind, id string) string {
	switch kind {
	case "threads":
		if t, err := s.st.Q.GetThread(ctx, id); err == nil {
			return t.Title
		}
	case "projects":
		if p, err := s.st.Q.GetProject(ctx, id); err == nil {
			return p.Name
		}
	case "spaces":
		if sp, err := s.st.Q.GetSpace(ctx, id); err == nil {
			return sp.Name
		}
	}
	return ""
}

// confirmAPICall 执行主人已批准的 api_call：重新查路由与策略、重跑参数规则与守卫后执行。
func (s *Service) confirmAPICall(ctx context.Context, row db.AssistantConfirmation, raw json.RawMessage) (string, error) {
	var a apiConfirmArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", errf(CodeInvalid, "bad confirmation arguments")
	}
	rt, ok := apiRoutes()[a.Op]
	pol := apiPolicy[a.Op]
	if !ok || pol.Class == opDeny {
		return "", errf(CodeNotAvailable, "%s is no longer available", a.Op)
	}
	if s.api == nil {
		return "", errf(CodeCoordinatorUnavailable, "Oryn operations are not configured")
	}
	req, err := buildAPIRequest(rt, a.Args)
	if err != nil {
		return "", err
	}
	c := toolCtx{HostToolCall: threads.HostToolCall{ThreadID: row.ThreadID, Kind: threads.KindAssistant}}
	if err := s.checkAPITarget(ctx, c, pol, req); err != nil {
		return "", err
	}
	res := s.execAPI(ctx, row.ThreadID, pol, req)
	if res.status >= 400 {
		return "", apiError(res)
	}
	if res.total == 0 {
		return "", nil
	}
	body, _ := capUTF8(res.out, apiConfirmDetailMax)
	if !utf8.Valid(body) {
		return "", nil
	}
	return wrapUntrustedText(string(body)), nil
}
