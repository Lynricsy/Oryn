package coord

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/schema"
)

// 视图（M33）：全局助手的 render_ui / render_html。视图本身由 Web 按转录里的工具调用渲染，宿主只做最低检查、给出
// C3 的结果；await 的调用在对话里挂一个 vwa_ 开头的 input 请求（threads.RequestUI，placeholder 为 oryn-view:<toolCallId>），
// 等主人在视图里作答（应答经 UIAnswered 转来）、10 分钟或调用取消。在途的等待只在 orynd 内存里（D14）。

const (
	// viewAwaitIDPrefix 是视图阻塞作答请求的 id 前缀（ids.New("vwa")）。
	viewAwaitIDPrefix = "vwa_"
	// viewAwaitMark 是阻塞作答请求 placeholder 的前缀，Web 按它把请求交给转录里对应 toolCallId 的视图。
	viewAwaitMark = "oryn-view:"
	// viewMaxBlocks / viewMaxHTML 是参数的最低检查（与 view-tools.json 的 schema 一致；Core 已先按 schema 校验）。
	viewMaxBlocks = 40
	viewMaxHTML   = 200_000
	// viewMaxDepth 是检查交互块时的递归上限（schema 只允许 3 层容器）。
	viewMaxDepth = 4
	// viewToolTimeout 是视图工具的单次调用上限：比阻塞等待多一分钟，等待到时总能先返回。
	viewToolTimeout = 11 * time.Minute
	// viewAnswerGrace 是等待到时、请求却已不在（主人恰好作答）时再等应答转来的时长。
	viewAnswerGrace = 5 * time.Second
)

// viewAwaitTimeout 是 await 的等待上限（D13；包内变量供测试缩短）。
var viewAwaitTimeout = 10 * time.Minute

// C3 的结果文本（给模型）。
const (
	viewTextRendered            = "Rendered."
	viewTextRenderedInteractive = "Rendered. The user's input will arrive as their next message; end this turn now."
	viewTextAnswered            = "The user answered in the view:\n"
	viewTextTimeout             = "No response within 10 minutes. The view stays usable: a later answer arrives as a user message. Continue without it or end the turn."
	viewTextDismissed           = "The user chose to reply in text instead; their message follows."
	viewTextAborted             = "Aborted."
)

// 阻塞作答请求的缺省标题（视图没有 title 时；收件箱与推送正文取它）。
const (
	viewWaitingTitleZh = "有一个视图等你操作"
	viewWaitingTitleEn = "A view is waiting for your input"
)

// viewToolsFile 是 schema/view-tools.json 的结构。
type viewToolsFile struct {
	Guide        string `json:"guide"`
	WritingGuide string `json:"writingGuide"`
	Tools        []struct {
		Name        string          `json:"name"`
		Label       string          `json:"label"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"tools"`
}

// viewTools 解析一次内嵌的 view-tools.json（文件随二进制发布，由 schema 包的测试保证合法）。
var viewTools = sync.OnceValue(func() viewToolsFile {
	var f viewToolsFile
	if err := json.Unmarshal(schema.ViewTools, &f); err != nil {
		panic("coord: schema/view-tools.json 无法解析: " + err.Error())
	}
	return f
})

// viewToolDefs 是视图工具的宿主工具定义：名字、标签、描述与参数 schema 原样取自 view-tools.json，以顶层工具出现。
func viewToolDefs() []apigen.OmpFrame {
	f := viewTools()
	defs := make([]apigen.OmpFrame, 0, len(f.Tools))
	for _, t := range f.Tools {
		var params obj
		if err := json.Unmarshal(t.Parameters, &params); err != nil {
			panic("coord: view-tools.json 的参数 schema 无法解析: " + err.Error())
		}
		defs = append(defs, apigen.OmpFrame{
			"name": t.Name, "label": t.Label, "description": t.Description, "parameters": params, "loadMode": "essential",
		})
	}
	return defs
}

// generativeUI 读取生成式 UI 总开关；尚未初始化所有者或读取异常时按开启处理（缺省开启）。
func (s *Service) generativeUI(ctx context.Context) bool {
	v, err := s.st.Q.GetOwnerGenerativeUI(ctx)
	return err != nil || v == 1
}

// viewWaits 是在途的视图阻塞作答：请求 id → 等待者。
type viewWaits struct {
	mu sync.Mutex
	m  map[string]viewWait
}

type viewWait struct {
	threadID string
	ch       chan json.RawMessage
}

// viewDetails 是结果的 details.view（C3）。
type viewDetails struct {
	Outcome string          `json:"outcome"`
	Spec    any             `json:"spec"`
	Answer  json.RawMessage `json:"answer,omitempty"`
}

func viewResult(text, outcome string, spec any, answer json.RawMessage) threads.HostToolResult {
	return threads.HostToolResult{
		Text: text, IsError: outcome == "aborted",
		Details: map[string]any{"view": viewDetails{Outcome: outcome, Spec: spec, Answer: answer}},
	}
}

// toolView 执行 render_ui / render_html（只给全局助手）。
func (s *Service) toolView(ctx context.Context, c toolCtx) threads.HostToolResult {
	if c.Kind != threads.KindAssistant {
		return failResult(errf("unknown_tool", "no tool named %q", c.Name))
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(c.Args, &args); err != nil || args == nil {
		return failResult(errf(CodeInvalid, "invalid arguments: expected an object"))
	}
	var await, endTurn bool
	if raw, ok := args["await"]; ok {
		_ = json.Unmarshal(raw, &await)
	}
	if raw, ok := args["endTurn"]; ok {
		_ = json.Unmarshal(raw, &endTurn)
	}
	delete(args, "await")
	delete(args, "endTurn")
	var (
		spec        any
		interactive bool
	)
	switch c.Name {
	case ToolRenderUI:
		var blocks []json.RawMessage
		if json.Unmarshal(args["blocks"], &blocks) != nil || len(blocks) < 1 || len(blocks) > viewMaxBlocks {
			return failResult(errf(CodeInvalid, "blocks must be an array of 1-%d blocks", viewMaxBlocks))
		}
		spec, interactive = args, viewInteractive(blocks, 0)
	default:
		var html string
		if json.Unmarshal(args["html"], &html) != nil || html == "" || utf8.RuneCountInString(html) > viewMaxHTML {
			return failResult(errf(CodeInvalid, "html must be a string of 1-%d characters", viewMaxHTML))
		}
		// 回放用的规格不重复存 html（Web 与帧服务从工具调用参数取）
		hs := map[string]json.RawMessage{}
		for _, k := range []string{"libs", "height"} {
			if v, ok := args[k]; ok {
				hs[k] = v
			}
		}
		spec, interactive = hs, true
	}
	// 真正挂起等作答时忽略 endTurn：作答要回到这次调用、同一回合继续
	if !await || !interactive || !viewCallAnswerable(c.ToolCallID) {
		text := viewTextRendered
		if interactive && c.Name == ToolRenderUI {
			text = viewTextRenderedInteractive
		}
		r := viewResult(text, "rendered", spec, nil)
		if endTurn {
			// Core 把带 orynEndTurn 的结果当终止型：这批工具跑完就结束回合，不再请求模型
			r.Details = map[string]any{"view": viewDetails{Outcome: "rendered", Spec: spec}, "orynEndTurn": true}
		}
		return r
	}
	var title string
	_ = json.Unmarshal(args["title"], &title)
	if title = strings.TrimSpace(title); title == "" {
		title = s.viewWaitingTitle(ctx)
	}
	return s.awaitView(ctx, c, title, spec)
}

// awaitView 挂起阻塞作答请求并等待：作答 → answered；主人改为打字（应答 cancelled）→ dismissed；10 分钟 → 撤回请求、
// timeout；调用取消（回合中止、agent 退出）→ 撤回请求、aborted。请求挂不上时退为非阻塞（视图照常可操作）。
func (s *Service) awaitView(ctx context.Context, c toolCtx, title string, spec any) threads.HostToolResult {
	id := ids.New("vwa")
	ch := make(chan json.RawMessage, 1)
	s.views.mu.Lock()
	if s.views.m == nil {
		s.views.m = map[string]viewWait{}
	}
	s.views.m[id] = viewWait{threadID: c.ThreadID, ch: ch}
	s.views.mu.Unlock()
	defer func() {
		s.views.mu.Lock()
		delete(s.views.m, id)
		s.views.mu.Unlock()
	}()
	frame, err := json.Marshal(map[string]any{
		"type": "extension_ui_request", "id": id, "method": "input", "title": title, "placeholder": viewAwaitMark + c.ToolCallID,
	})
	if err == nil {
		err = s.th.RequestUI(ctx, c.ThreadID, frame)
	}
	if err != nil {
		slog.Warn("coord: 挂出视图作答请求失败，按非阻塞返回", "thread", c.ThreadID, "tool", c.Name, "err", err)
		if c.Name == ToolRenderUI {
			return viewResult(viewTextRenderedInteractive, "rendered", spec, nil)
		}
		return viewResult(viewTextRendered, "rendered", spec, nil)
	}
	timer := time.NewTimer(viewAwaitTimeout)
	defer timer.Stop()
	select {
	case resp := <-ch:
		return viewAnswerResult(resp, spec)
	case <-timer.C:
		if s.th.WithdrawUI(context.WithoutCancel(ctx), c.ThreadID, id) {
			return viewResult(viewTextTimeout, "timeout", spec, nil)
		}
		// 请求已不在：主人恰好作答（应答正在转来）或对话已不在
		grace := time.NewTimer(viewAnswerGrace)
		defer grace.Stop()
		select {
		case resp := <-ch:
			return viewAnswerResult(resp, spec)
		case <-grace.C:
			return viewResult(viewTextTimeout, "timeout", spec, nil)
		case <-ctx.Done():
			return viewResult(viewTextAborted, "aborted", spec, nil)
		}
	case <-ctx.Done():
		s.th.WithdrawUI(context.WithoutCancel(ctx), c.ThreadID, id)
		return viewResult(viewTextAborted, "aborted", spec, nil)
	}
}

// viewAnswered 把主人对视图阻塞作答请求的应答交给等待者（UIAnswered 按 vwa_ 前缀分流到这里）。没有等待者
// （已到时、已取消、orynd 重启过）时丢弃。
func (s *Service) viewAnswered(threadID, requestID string, response json.RawMessage) {
	s.views.mu.Lock()
	w, ok := s.views.m[requestID]
	if ok && w.threadID == threadID {
		delete(s.views.m, requestID)
	} else {
		ok = false
	}
	s.views.mu.Unlock()
	if !ok {
		return
	}
	select {
	case w.ch <- append(json.RawMessage(nil), response...):
	default:
	}
}

// viewAnswerResult 把 extension_ui_response 帧转成结果（C4）：cancelled → dismissed；value 是 {v:1, answer, text}
// → answered（text 进文本、answer 进 details）；其余字符串 value 整段当作 text。
func viewAnswerResult(resp json.RawMessage, spec any) threads.HostToolResult {
	var r struct {
		Cancelled bool    `json:"cancelled"`
		Value     *string `json:"value"`
	}
	if json.Unmarshal(resp, &r) != nil || r.Cancelled || r.Value == nil {
		return viewResult(viewTextDismissed, "dismissed", spec, nil)
	}
	var v struct {
		V      int             `json:"v"`
		Answer json.RawMessage `json:"answer"`
		Text   *string         `json:"text"`
	}
	if json.Unmarshal([]byte(*r.Value), &v) == nil && v.V == 1 && v.Text != nil && strings.HasPrefix(string(v.Answer), "{") {
		return viewResult(viewTextAnswered+*v.Text, "answered", spec, v.Answer)
	}
	return viewResult(viewTextAnswered+*r.Value, "answered", spec, nil)
}

// viewWaitingTitle 是视图没有 title 时阻塞作答请求的标题，按全局语言设置取（读不到按简体中文）。
func (s *Service) viewWaitingTitle(ctx context.Context) string {
	if v, err := s.st.Q.GetOwnerLanguage(ctx); err == nil && apigen.LanguageCode(v) == apigen.En {
		return viewWaitingTitleEn
	}
	return viewWaitingTitleZh
}

// viewCallAnswerable 报告 toolCallId 能否写进作答标记（C4：不含 "、空白、<、>，至多 256 字符）；不能时视图不可交互，
// await 退为非阻塞。
func viewCallAnswerable(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	return !strings.ContainsFunc(id, func(r rune) bool { return r == '"' || r == '<' || r == '>' || unicode.IsSpace(r) })
}

// viewBlock 是检查交互块用的块头：容器的子块按类型取（form 的 columns 是数字，不能直接按列表解）。
type viewBlock struct {
	Type    string          `json:"type"`
	Blocks  json.RawMessage `json:"blocks"`
	Columns json.RawMessage `json:"columns"`
	Tabs    json.RawMessage `json:"tabs"`
}

// viewInteractive 报告块里（含容器内）是否有 form 或 actions。
func viewInteractive(blocks []json.RawMessage, depth int) bool {
	if depth > viewMaxDepth {
		return false
	}
	for _, raw := range blocks {
		var b viewBlock
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		var groups json.RawMessage
		switch b.Type {
		case "form", "actions":
			return true
		case "card":
			var sub []json.RawMessage
			if json.Unmarshal(b.Blocks, &sub) == nil && viewInteractive(sub, depth+1) {
				return true
			}
			continue
		case "columns":
			groups = b.Columns
		case "tabs":
			groups = b.Tabs
		default:
			continue
		}
		var gs []struct {
			Blocks []json.RawMessage `json:"blocks"`
		}
		if json.Unmarshal(groups, &gs) != nil {
			continue
		}
		for _, g := range gs {
			if viewInteractive(g.Blocks, depth+1) {
				return true
			}
		}
	}
	return false
}
