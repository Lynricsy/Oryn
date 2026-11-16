package artifacts

import (
	"bytes"
	"errors"
	"html"
	"net/http"
	"strconv"
	"strings"
)

// gatePolicy 是访问码页与「已不在线上」页的 CSP：我们自己的静态 HTML，没有脚本，表单只能提交回本源；
// 另加 frame-ancestors 'none'，不让别的页面把访问码输入框套进自己的界面。
const gatePolicy = "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'"

// unlockMaxBytes 是 `POST _oryn/unlock` 表单的上限。
const unlockMaxBytes = 4 << 10

// gateText 是访问门页面的文案。页面里不出现 artifact 的任何内容（连标题也没有）：拿不到访问码的人看不到它是什么。
type gateText struct {
	lang, title, lead, label, submit, wrong, locked, gone string
}

var gateTexts = map[string]gateText{
	"zh-CN": {
		lang:   "zh-CN",
		title:  "需要访问码",
		lead:   "这个页面需要访问码才能打开。",
		label:  "访问码",
		submit: "打开",
		wrong:  "访问码不对，请再试一次。",
		locked: "访问码被输错太多次，暂时不能输入。请向分享者要一个新的访问码。",
		gone:   "这个页面已不在线上。",
	},
	"en": {
		lang:   "en",
		title:  "Access code required",
		lead:   "This page needs an access code to open.",
		label:  "Access code",
		submit: "Open",
		wrong:  "That access code is not right. Try again.",
		locked: "The access code was entered wrong too many times, so it can't be entered for now. Ask the person who shared it for a new access code.",
		gone:   "This page is no longer online.",
	},
}

// gateLang 按 Accept-Language 选文案：最先出现的 zh 或 en 胜出，都没有时用中文。
func gateLang(r *http.Request) gateText {
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		tag = strings.ToLower(tag)
		switch {
		case tag == "zh" || strings.HasPrefix(tag, "zh-"):
			return gateTexts["zh-CN"]
		case tag == "en" || strings.HasPrefix(tag, "en-"):
			return gateTexts["en"]
		}
	}
	return gateTexts["zh-CN"]
}

// 访问码页的状态。
type gateState int

const (
	gateAsk gateState = iota
	gateWrong
	gateLocked
)

const gateStyle = `:root{color-scheme:light dark;font-family:system-ui,-apple-system,"Segoe UI",sans-serif}` +
	`body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:Canvas;color:CanvasText}` +
	`main{width:min(22rem,calc(100vw - 3rem))}h1{font-size:1.25rem;margin:0 0 .5rem}p{margin:0 0 1rem;line-height:1.5}` +
	`label{display:block;font-size:.875rem;margin-bottom:.375rem}` +
	`input{box-sizing:border-box;width:100%;font:inherit;font-size:1.25rem;letter-spacing:.3em;padding:.5rem .75rem;border:1px solid GrayText;border-radius:.5rem;background:Field;color:FieldText}` +
	`button{margin-top:.75rem;width:100%;font:inherit;padding:.5rem;border:0;border-radius:.5rem;background:CanvasText;color:Canvas;cursor:pointer}` +
	`button:disabled,input:disabled{opacity:.5;cursor:not-allowed}.err{color:#dc2626}`

// writeGatePage 写出访问门的静态页面（不缓存、不被索引、不能被内嵌）。HEAD 只写头。
func writeGatePage(w http.ResponseWriter, r *http.Request, status int, t gateText, body string) {
	var b bytes.Buffer
	b.WriteString(`<!doctype html><html lang="` + t.lang + `"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex">`)
	b.WriteString(`<title>` + html.EscapeString(t.title) + `</title><style>` + gateStyle + `</style></head><body><main>`)
	b.WriteString(body)
	b.WriteString("</main></body></html>\n")
	h := w.Header()
	h.Set("Content-Security-Policy", gatePolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(b.Len()))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b.Bytes())
	}
}

// writeCodePage 写出访问码页：表单 POST 到 action（发布根下的 `_oryn/unlock`）。锁定时禁用表单。
func writeCodePage(w http.ResponseWriter, r *http.Request, action string, state gateState) {
	t := gateLang(r)
	status := http.StatusOK
	var b strings.Builder
	b.WriteString(`<h1>` + html.EscapeString(t.title) + `</h1><p>` + html.EscapeString(t.lead) + `</p>`)
	switch state {
	case gateWrong:
		status = http.StatusForbidden
		b.WriteString(`<p class="err" role="alert">` + html.EscapeString(t.wrong) + `</p>`)
	case gateLocked:
		status = http.StatusForbidden
		b.WriteString(`<p class="err" role="alert">` + html.EscapeString(t.locked) + `</p>`)
	}
	disabled := ""
	if state == gateLocked {
		disabled = " disabled"
	}
	b.WriteString(`<form method="post" action="` + html.EscapeString(action) + `"><fieldset style="border:0;margin:0;padding:0"` + disabled + `>`)
	b.WriteString(`<label for="code">` + html.EscapeString(t.label) + `</label>`)
	b.WriteString(`<input id="code" name="code" required autocomplete="off" autocapitalize="off" spellcheck="false" maxlength="32" autofocus>`)
	b.WriteString(`<button type="submit">` + html.EscapeString(t.submit) + `</button></fieldset></form>`)
	writeGatePage(w, r, status, t, b.String())
}

// writeGonePage 写出「这个页面已不在线上」（发布记录不存在：从未发布或已取消发布）。
func writeGonePage(w http.ResponseWriter, r *http.Request) {
	t := gateLang(r)
	writeGatePage(w, r, http.StatusNotFound, t, `<p>`+html.EscapeString(t.gone)+`</p>`)
}

// unlockResult 把 UnlockCode 的错误换成访问码页的状态；ok 为假表示不是访问码本身的问题（交给调用方）。
func unlockResult(err error) (gateState, bool) {
	switch {
	case errors.Is(err, ErrCodeWrong):
		return gateWrong, true
	case errors.Is(err, ErrCodeLocked):
		return gateLocked, true
	}
	return gateAsk, false
}
