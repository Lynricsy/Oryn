// Package uiprompt 把 omp 的待答请求（`extension_ui_request` 帧）呈现为纯文本，并把文本作答转换成
// `extension_ui_response` 帧。Telegram 通知与 `oryn thread attach` 共用；不依赖其它内部包。
//
// 支持的请求：select、confirm、input / editor（登录 / 密钥类 input 除外）、单个字段的 form、单个单选题的 ask / askDialog。
// 其余（多字段表单、多选、plan_proposal 等）Describe 返回 Supported=false，应引导用户到 Web 作答。
package uiprompt

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

var (
	// ErrUnsupported 表示该请求无法用文本作答。
	ErrUnsupported = errors.New("uiprompt: request cannot be answered with text")
	// ErrNeedOption 表示该请求只接受选项，而给出的答案不是任何选项。
	ErrNeedOption = errors.New("uiprompt: answer must be one of the options")
)

// Prompt 是待答请求的纯文本呈现。
type Prompt struct {
	Title     string   // 单行问题
	Body      string   // 说明（可多行）
	Options   []string // 可点选项的显示文字；空 = 只能文字作答
	FreeText  bool     // 接受任意文字
	Supported bool     // false：无法用文本作答，应引导到 Web
}

// Labels 是 confirm 的两个选项文字（按渠道语言）；为空时取 `Yes` / `No`。
type Labels struct{ Yes, No string }

// confirmYes / confirmNo 是 confirm 文字作答额外认可的词（比较时忽略大小写与首尾空白）。
var (
	confirmYes = []string{"y", "yes", "是", "好", "确认"}
	confirmNo  = []string{"n", "no", "否", "不", "取消"}
)

// request 是 extension_ui_request / plan_proposal 帧中本包关心的字段。
type request struct {
	Type           string     `json:"type"`
	ID             string     `json:"id"`
	Method         string     `json:"method"`
	Title          string     `json:"title"`
	Message        string     `json:"message"`
	Options        []string   `json:"options"`
	Secret         bool       `json:"secret"`
	AuthCodeFor    string     `json:"authCodeFor"`
	LoginPromptFor string     `json:"loginPromptFor"`
	Questions      []question `json:"questions"`
	Sections       []section  `json:"sections"`
}

type question struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Options  []struct {
		Label string `json:"label"`
	} `json:"options"`
	Multi bool `json:"multi"`
}

type span struct {
	Text string `json:"text"`
}

type section struct {
	Title  []span      `json:"title"`
	Blocks []formBlock `json:"blocks"`
}

type formBlock struct {
	Kind    string     `json:"kind"`
	Spans   []span     `json:"spans"`
	Items   [][]span   `json:"items"`
	Ordered bool       `json:"ordered"`
	Text    string     `json:"text"`
	Field   *formField `json:"field"`
}

type formField struct {
	Kind    string `json:"kind"` // choice | text
	Name    string `json:"name"`
	Multi   bool   `json:"multi"`
	Other   bool   `json:"other"`
	Label   []span `json:"label"`
	Options []struct {
		Value string `json:"value"`
		Label []span `json:"label"`
	} `json:"options"`
}

func spansText(ss []span) string {
	var b strings.Builder
	for _, s := range ss {
		b.WriteString(s.Text)
	}
	return strings.TrimSpace(b.String())
}

// plan 是解析后的请求：呈现与作答方式。
type plan struct {
	req    request
	prompt Prompt
	// answer 把选中的选项序号（opt ≥ 0）或自由文本（opt < 0）构造成应答帧的方法相关部分。
	answer func(opt int, text string) any
	// confirm 为真时文字作答另认 confirmYes / confirmNo。
	confirm bool
}

func parse(frame json.RawMessage, l Labels) plan {
	var r request
	if json.Unmarshal(frame, &r) != nil {
		return plan{}
	}
	pl := plan{req: r, prompt: Prompt{Title: oneLine(r.Title)}}
	if r.Type != "extension_ui_request" {
		return pl
	}
	switch r.Method {
	case "select":
		pl.prompt = Prompt{Title: oneLine(r.Title), Options: r.Options, Supported: true}
		pl.answer = func(opt int, _ string) any { return valueAnswer{r.Options[opt]} }
	case "confirm":
		if l.Yes == "" {
			l.Yes = "Yes"
		}
		if l.No == "" {
			l.No = "No"
		}
		pl.prompt = Prompt{Title: oneLine(r.Title), Body: r.Message, Options: []string{l.Yes, l.No}, Supported: true}
		pl.confirm = true
		pl.answer = func(opt int, _ string) any { return confirmAnswer{opt == 0} }
	case "input", "editor":
		ok := r.Method == "editor" || (!r.Secret && r.AuthCodeFor == "" && r.LoginPromptFor == "")
		pl.prompt = Prompt{Title: oneLine(r.Title), FreeText: true, Supported: ok}
		if ok {
			pl.answer = func(_ int, text string) any { return valueAnswer{text} }
		}
	case "ask", "askDialog":
		if len(r.Questions) != 1 || r.Questions[0].Multi {
			break
		}
		q := r.Questions[0]
		labels := make([]string, len(q.Options))
		for i, o := range q.Options {
			labels[i] = o.Label
		}
		pl.prompt = Prompt{Title: oneLine(q.Question), Options: labels, FreeText: true, Supported: true}
		pl.answer = func(opt int, text string) any {
			selected := []string{}
			custom := ""
			if opt >= 0 {
				selected = []string{labels[opt]}
			} else {
				custom = strings.TrimSpace(text)
			}
			if r.Method == "ask" {
				return askAnswer{[]askItem{{ID: q.ID, SelectedOptions: selected, CustomInput: custom}}}
			}
			return askDialogAnswer{askDialogResult{Kind: "submit", Results: []askDialogItem{{
				ID: q.ID, Question: q.Question, Options: labels, Multi: false, SelectedOptions: selected, CustomInput: custom,
			}}}}
		}
	case "form":
		formPlan(&pl)
	}
	return pl
}

// formPlan 处理恰好一个字段（非多选）的表单。
func formPlan(pl *plan) {
	var blocks []formBlock
	for _, s := range pl.req.Sections {
		blocks = append(blocks, s.Blocks...)
	}
	fieldAt := -1
	for i, b := range blocks {
		if b.Kind == "field" {
			if fieldAt >= 0 || b.Field == nil {
				return
			}
			fieldAt = i
		}
	}
	if fieldAt < 0 {
		return
	}
	f := blocks[fieldAt].Field
	if (f.Kind != "choice" && f.Kind != "text") || (f.Kind == "choice" && f.Multi) {
		return
	}
	title := spansText(f.Label)
	titleAt := -1
	if title == "" {
		for i := fieldAt - 1; i >= 0; i-- {
			if blocks[i].Kind == "text" {
				if t := spansText(blocks[i].Spans); t != "" {
					title, titleAt = t, i
					break
				}
			}
		}
	}
	if title == "" && len(pl.req.Sections) > 0 {
		title = spansText(pl.req.Sections[0].Title)
	}
	var body []string
	for i, b := range blocks {
		if i == titleAt {
			continue
		}
		switch b.Kind {
		case "text":
			if t := spansText(b.Spans); t != "" {
				body = append(body, t)
			}
		case "list":
			for j, it := range b.Items {
				marker := "- "
				if b.Ordered {
					marker = strconv.Itoa(j+1) + ". "
				}
				body = append(body, marker+spansText(it))
			}
		case "pre":
			if b.Text != "" {
				body = append(body, strings.TrimRight(b.Text, "\n"))
			}
		}
	}
	pl.prompt = Prompt{Title: oneLine(title), Body: strings.Join(body, "\n"), Supported: true}
	name := f.Name
	if f.Kind == "text" {
		pl.prompt.FreeText = true
		pl.answer = func(_ int, text string) any { return formAnswer{map[string]any{name: text}} }
		return
	}
	values := make([]string, len(f.Options))
	labels := make([]string, len(f.Options))
	for i, o := range f.Options {
		values[i] = o.Value
		labels[i] = spansText(o.Label)
	}
	pl.prompt.Options = labels
	pl.prompt.FreeText = f.Other
	pl.answer = func(opt int, text string) any {
		if opt >= 0 {
			return formAnswer{map[string]any{name: values[opt]}}
		}
		return formAnswer{map[string]any{name: strings.TrimSpace(text)}}
	}
}

// Describe 返回请求帧的纯文本呈现；无法解析或不支持的请求 Supported=false（Title 尽量取帧的 `title`）。
func Describe(frame json.RawMessage, l Labels) Prompt {
	return parse(frame, l).prompt
}

// AnswerOption 以第 i 个选项（从 0 起）作答，返回应答帧。
func AnswerOption(frame json.RawMessage, i int) (json.RawMessage, error) {
	pl := parse(frame, Labels{})
	if !pl.prompt.Supported || pl.answer == nil {
		return nil, ErrUnsupported
	}
	if i < 0 || i >= len(pl.prompt.Options) {
		return nil, ErrNeedOption
	}
	return respond(pl.req.ID, pl.answer(i, ""))
}

// AnswerText 以一段文字作答：全为数字且在 1..len(Options) 内 = 该序号的选项；与某选项文字相等
// （忽略首尾空白与大小写）= 该选项；confirm 另认 `y yes 是 好 确认` / `n no 否 不 取消`；
// 否则 FreeText 为真时按自由文本作答，为假时返回 ErrNeedOption。
func AnswerText(frame json.RawMessage, text string) (json.RawMessage, error) {
	pl := parse(frame, Labels{})
	if !pl.prompt.Supported || pl.answer == nil {
		return nil, ErrUnsupported
	}
	if i := matchOption(pl.prompt.Options, text); i >= 0 {
		return respond(pl.req.ID, pl.answer(i, ""))
	}
	if pl.confirm {
		switch {
		case containsFold(confirmYes, text):
			return respond(pl.req.ID, pl.answer(0, ""))
		case containsFold(confirmNo, text):
			return respond(pl.req.ID, pl.answer(1, ""))
		}
	}
	if !pl.prompt.FreeText {
		return nil, ErrNeedOption
	}
	return respond(pl.req.ID, pl.answer(-1, text))
}

// matchOption 返回文字对应的选项序号，无匹配返回 -1。
func matchOption(opts []string, text string) int {
	t := strings.TrimSpace(text)
	if t == "" {
		return -1
	}
	if allDigits(t) {
		if n, err := strconv.Atoi(t); err == nil && n >= 1 && n <= len(opts) {
			return n - 1
		}
	}
	for i, o := range opts {
		if strings.EqualFold(strings.TrimSpace(o), t) {
			return i
		}
	}
	return -1
}

func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func containsFold(words []string, text string) bool {
	t := strings.TrimSpace(text)
	for _, w := range words {
		if strings.EqualFold(w, t) {
			return true
		}
	}
	return false
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// 应答帧的方法相关部分；respond 把它们与 `type` / `id` 合并。
type (
	valueAnswer struct {
		Value string `json:"value"`
	}
	confirmAnswer struct {
		Confirmed bool `json:"confirmed"`
	}
	formAnswer struct {
		FormData map[string]any `json:"formData"`
	}
	askItem struct {
		ID              string   `json:"id"`
		SelectedOptions []string `json:"selectedOptions"`
		CustomInput     string   `json:"customInput,omitempty"`
	}
	askAnswer struct {
		Answers []askItem `json:"answers"`
	}
	askDialogItem struct {
		ID              string   `json:"id"`
		Question        string   `json:"question"`
		Options         []string `json:"options"`
		Multi           bool     `json:"multi"`
		SelectedOptions []string `json:"selectedOptions"`
		CustomInput     string   `json:"customInput,omitempty"`
	}
	askDialogResult struct {
		Kind    string          `json:"kind"`
		Results []askDialogItem `json:"results"`
	}
	askDialogAnswer struct {
		AskDialog askDialogResult `json:"askDialog"`
	}
)

// respond 构造 `{"type":"extension_ui_response","id":…, <body 的字段>}`。
func respond(id string, body any) (json.RawMessage, error) {
	head, err := json.Marshal(struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}{"extension_ui_response", id})
	if err != nil {
		return nil, err
	}
	rest, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	// 两个对象拼接：去掉 head 的 `}` 与 rest 的 `{`。
	out := make([]byte, 0, len(head)+len(rest))
	out = append(out, head[:len(head)-1]...)
	out = append(out, ',')
	out = append(out, rest[1:]...)
	return out, nil
}
