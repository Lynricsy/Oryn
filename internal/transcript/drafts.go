package transcript

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// draftEditsMarker 是 orynd 追加在用户消息末尾的写作块改稿块的开标签（threads.draftEditsHead 的首行）。
const draftEditsMarker = "<oryn-draft-edits>"

// draftHeaderRE 匹配改稿块里一份草稿的开头行（同 threads.draftEditsBlock 的输出）。
var draftHeaderRE = regexp.MustCompile(`^<draft kind="(?:email|message|doc)" title="[^"]*">$`)

// redactDraftEdits 把用户消息里的改稿块缩成只含各份草稿 kind / title 的空壳：改稿全文是主人尚未发出的私人草稿，
// 公开面（分享条目、实时帧、`.md`）只说明「附上了哪份草稿」。附件块原样保留在最后。
// 没有改稿块（或不是 user 消息）时 changed=false，payload 原样返回。
func redactDraftEdits(payload json.RawMessage) (_ json.RawMessage, changed bool) {
	if !bytes.Contains(payload, []byte("oryn-draft-edits")) {
		return payload, false
	}
	var p map[string]json.RawMessage
	if json.Unmarshal(payload, &p) != nil {
		return payload, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(p["message"], &m) != nil {
		return payload, false
	}
	var role string
	if json.Unmarshal(m["role"], &role) != nil || role != "user" {
		return payload, false
	}
	var content json.RawMessage
	var s string
	if json.Unmarshal(m["content"], &s) == nil {
		red, ok := redactDraftEditsText(s)
		if !ok {
			return payload, false
		}
		content = marshalNoEscape(red)
	} else {
		var bs []map[string]json.RawMessage
		if json.Unmarshal(m["content"], &bs) != nil {
			return payload, false
		}
		for _, b := range bs {
			var typ, text string
			if json.Unmarshal(b["type"], &typ) != nil || typ != "text" || json.Unmarshal(b["text"], &text) != nil {
				continue
			}
			if red, ok := redactDraftEditsText(text); ok {
				b["text"] = marshalNoEscape(red)
				changed = true
			}
		}
		if !changed {
			return payload, false
		}
		content = marshalNoEscape(bs)
	}
	m["content"] = content
	p["message"] = marshalNoEscape(m)
	out := marshalNoEscape(p)
	if out == nil {
		return payload, false
	}
	return out, true
}

// redactDraftEditsText 在一段正文上做 redactDraftEdits：先让出末尾的附件块，再把正文末尾的改稿块换成空壳。
// 草稿正文里的 `</draft` 在写入时已转义为 `<\/draft`，因此开头行之后第一个 `</draft>` 就是这份草稿的结尾；
// 正文里伪造的 `<draft …>` 行不会被当成开头行。
func redactDraftEditsText(text string) (string, bool) {
	if !strings.Contains(text, draftEditsMarker) {
		return text, false
	}
	body, tail := text, ""
	if m := attachmentBlockRE.FindStringIndex(text); m != nil {
		body, tail = text[:m[0]], text[m[0]:]
	}
	m := draftEditsBlockRE.FindStringSubmatchIndex(body)
	if m == nil {
		return text, false
	}
	var b strings.Builder
	b.WriteString(body[:m[0]])
	if strings.HasPrefix(body[m[0]:], "\n\n") {
		b.WriteString("\n\n")
	}
	b.WriteString(draftEditsMarker + "\n")
	rest := body[m[2]:m[3]]
	for {
		i := strings.Index(rest, "<draft kind=")
		if i < 0 {
			break
		}
		rest = rest[i:]
		nl := strings.IndexByte(rest, '\n')
		if nl < 0 {
			break
		}
		if header := rest[:nl]; draftHeaderRE.MatchString(header) {
			b.WriteString(header + "\n</draft>\n")
		}
		end := strings.Index(rest, "</draft>")
		if end < 0 {
			break
		}
		rest = rest[end+len("</draft>"):]
	}
	b.WriteString("</oryn-draft-edits>")
	b.WriteString(tail)
	return b.String(), true
}

// marshalNoEscape 序列化且不把 `<` `>` `&` 转成 \u003c 等（公开条目保持与原载荷相同的字面，授权的子串比对不受影响）。
func marshalNoEscape(v any) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return nil
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}
