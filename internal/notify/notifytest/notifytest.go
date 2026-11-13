// Package notifytest 提供通知服务的测试替身：一个本机的假 Telegram Bot API（getMe / getUpdates / sendMessage /
// answerCallbackQuery / editMessageReplyMarkup）。测试往里塞更新、从通道里取发出的调用，不依赖外网、不 sleep。
package notifytest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Call 是假 Bot API 收到的一次调用（getUpdates 除外）。
type Call struct {
	Method string
	Body   map[string]any
}

// Int 返回 Body 里的整数字段（JSON 数字解为 float64）。
func (c Call) Int(key string) int64 {
	v, _ := c.Body[key].(float64)
	return int64(v)
}

// Str 返回 Body 里的字符串字段。
func (c Call) Str(key string) string {
	v, _ := c.Body[key].(string)
	return v
}

// Buttons 返回 reply_markup.inline_keyboard 的按钮（逐行展开）；没有 reply_markup 时为 nil。
func (c Call) Buttons() []map[string]any {
	rm, ok := c.Body["reply_markup"].(map[string]any)
	if !ok {
		return nil
	}
	rows, _ := rm["inline_keyboard"].([]any)
	out := []map[string]any{}
	for _, r := range rows {
		for _, b := range r.([]any) {
			out = append(out, b.(map[string]any))
		}
	}
	return out
}

// Bot 是假 Telegram Bot API。
type Bot struct {
	Server   *httptest.Server
	Token    string
	Username string
	// Calls 收到 getUpdates 以外的全部调用（按到达顺序）。
	Calls chan Call

	mu       sync.Mutex
	queue    []json.RawMessage
	ids      []int64
	nextUpd  int64
	nextMsg  int64
	offsets  []int64
	arrived  chan struct{}
	pollFail int
}

// NewBot 启动假 Bot API；只接受 token 与之相同的请求。
func NewBot(token, username string) *Bot {
	b := &Bot{Token: token, Username: username, Calls: make(chan Call, 256), nextUpd: 100, nextMsg: 1000, arrived: make(chan struct{})}
	b.Server = httptest.NewServer(http.HandlerFunc(b.serve))
	return b
}

// URL 是 apiBase。
func (b *Bot) URL() string { return b.Server.URL }

// Close 关闭服务。
func (b *Bot) Close() { b.Server.Close() }

// Push 追加一条更新（自动分配 update_id）并返回该 id。update 是不含 update_id 的更新体，如 {"message":{…}}。
func (b *Bot) Push(update map[string]any) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextUpd
	b.nextUpd++
	update["update_id"] = id
	raw, _ := json.Marshal(update)
	b.queue = append(b.queue, raw)
	b.ids = append(b.ids, id)
	close(b.arrived)
	b.arrived = make(chan struct{})
	return id
}

// FailPolls 让接下来 n 次 getUpdates 回 409（模拟别处在轮询）。
func (b *Bot) FailPolls(n int) {
	b.mu.Lock()
	b.pollFail = n
	b.mu.Unlock()
}

// Offsets 返回每次 getUpdates 请求带的 offset（未带为 0）。
func (b *Bot) Offsets() []int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int64(nil), b.offsets...)
}

// Next 取下一条调用；timeout 内没有则返回 ok=false。
func (b *Bot) Next(timeout time.Duration) (Call, bool) {
	select {
	case c := <-b.Calls:
		return c, true
	case <-time.After(timeout):
		return Call{}, false
	}
}

// Drain 取出当前已到达的全部调用（不等待）。
func (b *Bot) Drain() []Call {
	var out []Call
	for {
		select {
		case c := <-b.Calls:
			out = append(out, c)
		default:
			return out
		}
	}
}

// PrivateMessage 构造私聊文字消息更新。
func PrivateMessage(chatID int64, text string) map[string]any {
	return map[string]any{"message": map[string]any{"message_id": 1, "chat": map[string]any{"id": chatID, "type": "private", "first_name": "Tester"}, "text": text}}
}

// Reply 构造回复某条消息的私聊更新。
func Reply(chatID, replyTo int64, text string) map[string]any {
	u := PrivateMessage(chatID, text)
	u["message"].(map[string]any)["reply_to_message"] = map[string]any{"message_id": replyTo, "chat": map[string]any{"id": chatID, "type": "private"}}
	return u
}

// Callback 构造按钮回调更新。
func Callback(chatID, messageID int64, data string) map[string]any {
	return map[string]any{"callback_query": map[string]any{
		"id": "cb-" + data, "data": data, "from": map[string]any{"id": chatID},
		"message": map[string]any{"message_id": messageID, "chat": map[string]any{"id": chatID, "type": "private"}},
	}}
}

func writeOK(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func writeErr(w http.ResponseWriter, status int, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": status, "description": desc})
}

func (b *Bot) serve(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/bot")
	tok, method, ok2 := strings.Cut(rest, "/")
	if !ok || !ok2 || tok != b.Token {
		writeErr(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	raw, _ := io.ReadAll(r.Body)
	body := map[string]any{}
	_ = json.Unmarshal(raw, &body)
	switch method {
	case "getMe":
		writeOK(w, map[string]any{"id": 42, "is_bot": true, "username": b.Username})
	case "getUpdates":
		b.getUpdates(w, r, body)
	case "sendMessage":
		b.mu.Lock()
		b.nextMsg++
		id := b.nextMsg
		b.mu.Unlock()
		b.Calls <- Call{Method: method, Body: body}
		writeOK(w, map[string]any{"message_id": id, "chat": map[string]any{"id": body["chat_id"]}, "text": body["text"]})
	case "answerCallbackQuery", "editMessageReplyMarkup":
		b.Calls <- Call{Method: method, Body: body}
		writeOK(w, true)
	default:
		writeErr(w, http.StatusNotFound, "Not Found: method "+method)
	}
}

func (b *Bot) getUpdates(w http.ResponseWriter, r *http.Request, body map[string]any) {
	off, _ := body["offset"].(float64)
	wait, _ := body["timeout"].(float64)
	b.mu.Lock()
	b.offsets = append(b.offsets, int64(off))
	if b.pollFail > 0 {
		b.pollFail--
		b.mu.Unlock()
		writeErr(w, http.StatusConflict, "Conflict: terminated by other getUpdates request")
		return
	}
	b.mu.Unlock()
	deadline := time.After(time.Duration(wait) * time.Second)
	for {
		b.mu.Lock()
		var out []json.RawMessage
		for i, id := range b.ids {
			if id >= int64(off) {
				out = append(out, b.queue[i])
			}
		}
		arrived := b.arrived
		b.mu.Unlock()
		if len(out) > 0 {
			writeOK(w, out)
			return
		}
		select {
		case <-arrived:
		case <-deadline:
			writeOK(w, []any{})
			return
		case <-r.Context().Done():
			return
		}
	}
}
