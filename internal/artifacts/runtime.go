package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// 页面运行时端点：帧根下的 `POST /_oryn/v1`。页面里的 `oryn.use(cap)` 与能力调用都经它到达控制面。
//
// 请求体是一条消息 `{v: 1, id, kind, cap, op?, args?}`：
//   - kind "use"：问能力是否可用。可用回 `{ok: true, value: {ops: [...]}}`；任何拒绝（不是可用运行时的帧、
//     artifact 不在、能力未知或当前不可用）都回 `{ok: true, value: null}`，页面分不出是哪一种。
//   - kind "call"：调用能力的操作 op；args 是位置参数的 JSON 数组（缺省为 []）。成功回 `{ok: true, value}`，
//     失败回 `{ok: false, error: {code, message}}`。
//
// 应答一律带 `{v: 1, id}`。消息形状不对时回 400 与 invalid。阶段 7 在这张能力表上扩充（db、files、sample、
// 流式调用、限速与同意）。
const (
	runtimeEndpoint   = ReservedPrefix + "/v1"
	maxRuntimeMessage = 8 << 20
	// maxContentText 是 content.write 的文本上限。
	maxContentText = 6 << 20
)

// 运行时端点的错误码（除与 agent 工具共用的 invalid、conflict、too_large、not_found 之外）。
const (
	codeNotAllowed  = "not_allowed"
	codeUnsupported = "unsupported"
	codeFailed      = "failed"
)

var (
	runtimeIDRe  = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	runtimeCapRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	runtimeOpRe  = regexp.MustCompile(`^[a-z][A-Za-z0-9_]{0,31}$`)
)

// runtimeMsg 是页面发来的一条消息。
type runtimeMsg struct {
	V    int             `json:"v"`
	ID   string          `json:"id"`
	Kind string          `json:"kind"`
	Cap  string          `json:"cap"`
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args"`
}

// runtimeCall 是一次能力调用的上下文：发起的帧与它的 artifact（已确认存在、不在回收站里）。
type runtimeCall struct {
	token string
	frame frame
	art   Artifact
	args  json.RawMessage
}

// runtimeOp 执行一个操作；返回值作为应答的 value。业务错误是 *OpError（Code 取运行时的错误码）。
type runtimeOp func(ctx context.Context, rc *runtimeCall) (any, error)

// runtimeCap 是一个页面能力。
type runtimeCap struct {
	// available 报告这个能力对这次调用是否可用；不可用时 use 回 null、call 回 unsupported。
	available func(ctx context.Context, rc *runtimeCall) bool
	ops       map[string]runtimeOp
}

// opNames 返回能力的操作名（排序）。
func (c runtimeCap) opNames() []string {
	out := make([]string, 0, len(c.ops))
	for n := range c.ops {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// runtimeCaps 是运行时端点认得的能力表。
func (h *Host) runtimeCaps() map[string]runtimeCap {
	return map[string]runtimeCap{"content": h.contentCap()}
}

// frameOrigin 返回帧自己的源（scheme://host[:port]）。
func (h *Host) frameOrigin(token string) string {
	u, err := url.Parse(h.frameURL(token))
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// serveRuntime 处理帧根下的 `/_oryn/v1`：POST 一条消息，OPTIONS 是跨源预检（页面在不透明源里，发 JSON 时浏览器
// 先预检）。请求若带 Origin，必须是 null（不透明源）或帧自己的源。
func (h *Host) serveRuntime(w http.ResponseWriter, r *http.Request, token string, f frame) {
	setFrameHeaders(w, r, framePolicy(sandboxFrame, h.frameSelf(token), f.ancestors, true))
	if o := r.Header.Get("Origin"); o != "" && o != "null" && o != h.frameOrigin(token) {
		frameError(w, http.StatusForbidden, "来源不对")
		return
	}
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodPost:
	default:
		w.Header().Set("Allow", "POST, OPTIONS")
		frameError(w, http.StatusMethodNotAllowed, "只支持 POST")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRuntimeMessage))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeRuntime(w, http.StatusRequestEntityTooLarge, "", nil, opErr(CodeTooLarge, "消息超过 %d MiB。", maxRuntimeMessage>>20))
		return
	}
	if err != nil {
		return
	}
	var m runtimeMsg
	if err := decodeParams(body, &m); err != nil {
		writeRuntime(w, http.StatusBadRequest, "", nil, opErr(CodeInvalid, "消息不是合法的 JSON 对象。"))
		return
	}
	if problem := m.problem(); problem != "" {
		id := m.ID
		if !runtimeIDRe.MatchString(id) {
			id = ""
		}
		writeRuntime(w, http.StatusBadRequest, id, nil, opErr(CodeInvalid, "%s", problem))
		return
	}
	value, err := h.dispatch(r.Context(), token, f, m)
	writeRuntime(w, http.StatusOK, m.ID, value, err)
}

// problem 检查消息形状，返回问题说明（空 = 合法）。
func (m runtimeMsg) problem() string {
	switch {
	case m.V != 1:
		return "v 必须是 1。"
	case !runtimeIDRe.MatchString(m.ID):
		return "id 不合法。"
	case m.Kind != "use" && m.Kind != "call":
		return "kind 只能是 use 或 call。"
	case !runtimeCapRe.MatchString(m.Cap):
		return "cap 不合法。"
	case m.Kind == "call" && !runtimeOpRe.MatchString(m.Op):
		return "op 不合法。"
	case m.Kind == "use" && (m.Op != "" || len(m.Args) > 0):
		return "use 不带 op 与 args。"
	}
	return ""
}

// dispatch 按「帧的运行时位 → artifact 存在且不在回收站 → 能力已注册 → 能力可用 → 操作存在」的顺序校验并执行。
// use 的任何拒绝都回 (nil, nil)。
func (h *Host) dispatch(ctx context.Context, token string, f frame, m runtimeMsg) (any, error) {
	reject := func(err *OpError) (any, error) {
		if m.Kind == "use" {
			return nil, nil
		}
		return nil, err
	}
	if !f.runtime {
		return reject(opErr(codeNotAllowed, "这个页面不能使用运行时能力（旧版本或在浏览器中单独打开）。"))
	}
	a, err := h.s.Get(ctx, f.artifactID)
	if err != nil {
		if _, ok := AsOpError(err); ok {
			return reject(opErr(CodeNotFound, "artifact 已不在。"))
		}
		return nil, err
	}
	if a.Deleted() {
		return reject(opErr(CodeNotFound, "artifact 已被删除。"))
	}
	c, ok := h.caps[m.Cap]
	if !ok {
		return reject(opErr(codeUnsupported, "没有能力 %s。", m.Cap))
	}
	rc := &runtimeCall{token: token, frame: f, art: a, args: m.Args}
	if !c.available(ctx, rc) {
		return reject(opErr(codeUnsupported, "能力 %s 在这个页面上不可用。", m.Cap))
	}
	if m.Kind == "use" {
		return map[string]any{"ops": c.opNames()}, nil
	}
	op, ok := c.ops[m.Op]
	if !ok {
		return nil, opErr(codeUnsupported, "能力 %s 没有操作 %s。", m.Cap, m.Op)
	}
	return op(ctx, rc)
}

// writeRuntime 写一条应答：err 为 nil 时 `{v, id, ok: true, value}`，否则 `{v, id, ok: false, error}`。
// 不是 *OpError 的错误记日志，对页面只报 failed。
func writeRuntime(w http.ResponseWriter, status int, id string, value any, err error) {
	reply := map[string]any{"v": 1, "id": id, "ok": err == nil}
	if err == nil {
		reply["value"] = value
	} else {
		oe, ok := AsOpError(err)
		if !ok {
			if !errors.Is(err, context.Canceled) {
				slog.Warn("artifacts: 运行时调用失败", "err", err)
			}
			oe = opErr(codeFailed, "暂时无法完成，请稍后再试。")
		}
		reply["error"] = map[string]string{"code": oe.Code, "message": oe.Message}
	}
	b, _ := json.Marshal(reply)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// oneArg 把位置参数解成最多一个对象参数 v（缺省或空数组时 v 保持零值）；多余的参数或未知字段报 invalid。
func oneArg(args json.RawMessage, v any) error {
	raw := bytes.TrimSpace(args)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return opErr(CodeInvalid, "args 必须是参数数组。")
	}
	switch len(list) {
	case 0:
		return nil
	case 1:
		return decodeParams(list[0], v)
	}
	return opErr(CodeInvalid, "这个操作最多一个参数，收到 %d 个。", len(list))
}

// contentCap 是 content 能力：类型化 artifact 的查看器读写入口文件。只对类型化 artifact 可用。
//   - read({version?})：回 {type, format, version, head, entry, text}；version 缺省为最新。
//   - write({baseVersion, text})：把入口文件换成 text，落成一个用户版本，回 {version}。
func (h *Host) contentCap() runtimeCap {
	return runtimeCap{
		available: func(_ context.Context, rc *runtimeCall) bool { return rc.art.Type != "" },
		ops: map[string]runtimeOp{
			"read":  h.contentRead,
			"write": h.contentWrite,
		},
	}
}

func (h *Host) contentRead(ctx context.Context, rc *runtimeCall) (any, error) {
	var p struct {
		Version int `json:"version"`
	}
	if err := oneArg(rc.args, &p); err != nil {
		return nil, err
	}
	if p.Version < 0 {
		return nil, opErr(CodeInvalid, "version 不能是负数。")
	}
	v, err := h.s.Version(ctx, rc.art.ID, p.Version)
	if err != nil {
		return nil, err
	}
	entry := rc.art.Entry()
	b, _, err := h.s.ReadFile(ctx, rc.art.ID, v.Version, entry)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type": rc.art.Type, "format": v.Format, "version": v.Version, "head": rc.art.Version, "entry": entry, "text": string(b),
	}, nil
}

func (h *Host) contentWrite(ctx context.Context, rc *runtimeCall) (any, error) {
	var p struct {
		BaseVersion int     `json:"baseVersion"`
		Text        *string `json:"text"`
	}
	if err := oneArg(rc.args, &p); err != nil {
		return nil, err
	}
	switch {
	case p.BaseVersion < 1:
		return nil, opErr(CodeInvalid, "缺少 baseVersion（页面所在的版本号）。")
	case p.Text == nil:
		return nil, opErr(CodeInvalid, "缺少 text。")
	case len(*p.Text) > maxContentText:
		return nil, opErr(CodeTooLarge, "内容超过 %d MiB。", maxContentText>>20)
	case !utf8.ValidString(*p.Text):
		return nil, opErr(CodeInvalid, "内容不是合法的 UTF-8。")
	}
	a, err := h.s.Write(ctx, WriteParams{
		ID: rc.art.ID, BaseVersion: p.BaseVersion,
		Files: []FileChange{{Path: rc.art.Entry(), Data: strings.NewReader(*p.Text)}},
	})
	if err != nil {
		if oe, ok := AsOpError(err); ok && oe.Code == CodeDeleted {
			return nil, opErr(CodeNotFound, "artifact 已被删除。")
		}
		return nil, err
	}
	return map[string]any{"version": a.Version}, nil
}
