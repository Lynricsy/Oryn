package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/config"
)

// M3 §5.2–5.3：消息附件——上传到控制面、建线程 / WS cmd 时投递到 orb 的 ~/.oryn/attachments/<thread>/<id>/<name>
// （仓库之外），用户消息末尾带 <oryn-attachments> 块；process 驱动 + 真实 orbd + fakeomp。

type attachmentView struct {
	ID       string  `json:"id"`
	ThreadID *string `json:"threadId"`
	Name     string  `json:"name"`
	Mime     string  `json:"mime"`
	Size     int64   `json:"size"`
	Sha256   string  `json:"sha256"`
}

func (s *stack) upload(name, threadID string, data []byte) attachmentView {
	s.t.Helper()
	q := url.Values{"name": {name}}
	if threadID != "" {
		q.Set("threadId", threadID)
	}
	resp := s.orbReq("POST", "/api/v1/attachments?"+q.Encode(), bytes.NewReader(data), map[string]string{"Content-Type": "application/octet-stream"})
	b := readAll(s.t, resp)
	require.Equal(s.t, http.StatusCreated, resp.StatusCode, "%s", b)
	var a attachmentView
	require.NoError(s.t, json.Unmarshal(b, &a))
	return a
}

// userTexts 返回线程主会话中全部 user 消息的文本。
func (s *stack) userTexts(threadID string) []string {
	s.t.Helper()
	var page struct {
		Entries []struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		} `json:"entries"`
	}
	s.rest("GET", "/api/v1/threads/"+threadID+"/entries?limit=500", nil, &page, 200)
	var out []string
	for _, e := range page.Entries {
		var p struct {
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if e.Type != "message" || json.Unmarshal(e.Payload, &p) != nil || p.Message.Role != "user" {
			continue
		}
		for _, c := range p.Message.Content {
			if c.Type == "text" {
				out = append(out, c.Text)
			}
		}
	}
	return out
}

func TestE2EAttachments(t *testing.T) {
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})

	report := make([]byte, 300<<10)
	_, _ = rand.Read(report)
	a1 := s.upload("../季度 报告.bin", "", report)
	assert.Equal(t, "季度 报告.bin", a1.Name, "文件名净化为基名")
	assert.Nil(t, a1.ThreadID)
	assert.Equal(t, int64(len(report)), a1.Size)

	// 建线程时带附件：首条提示词在投递之后才下发
	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{
		"prompt": "read the report", "attachments": []string{a1.ID},
		"executor": map[string]any{"kind": "new_orb", "provider": "local"},
	}, &created, 201)
	// 附件块里有 "asked"：fakeomp 见到 "ask" 就发一个 select 请求，作答后回合结束
	ws := s.dialRaw()
	ws.send(map[string]any{"type": "sub", "threadId": created.Thread.ID, "afterSeq": 0})
	ws.answerAsk(created.Thread.ID)
	th := s.waitThread(created.Thread.ID, 60*time.Second, func(v threadView) bool { return v.State == "idle" && v.OrbID != "" })
	home := s.drv.HomeDir(th.OrbID)
	p1 := filepath.Join(home, ".oryn", "attachments", th.ID, a1.ID, a1.Name)
	got, err := os.ReadFile(p1)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(report, got), "orb 内的附件与上传字节一致")
	rel, err := filepath.Rel(filepath.Join(home, "workspace"), p1)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(rel, ".."), "附件不在工作区（仓库）内: %s", p1)

	texts := s.userTexts(th.ID)
	require.NotEmpty(t, texts)
	assert.True(t, strings.HasPrefix(texts[0], "read the report\n\n<oryn-attachments>\n"), "%q", texts[0])
	assert.Contains(t, texts[0], fmt.Sprintf("- %s (application/octet-stream, %d bytes)\n</oryn-attachments>", p1, len(report)))

	// WS cmd prompt{orynAttachments}：同样投递，omp 收到的帧里没有 orynAttachments
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	a2 := s.upload("shot.png", "", png)
	assert.Equal(t, "image/png", a2.Mime)
	ws.send(map[string]any{"type": "cmd", "threadId": th.ID, "id": "c1", "frame": map[string]any{
		"type": "prompt", "message": "and this", "orynAttachments": []string{a2.ID},
	}})
	resps := ws.res("c1", nil)
	assert.Contains(t, string(resps), `"success":true`)
	ws.answerAsk(th.ID)
	s.waitThread(th.ID, 30*time.Second, func(v threadView) bool { return v.State == "idle" })
	p2 := filepath.Join(home, ".oryn", "attachments", th.ID, a2.ID, "shot.png")
	got, err = os.ReadFile(p2)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(png, got))
	require.Eventually(t, func() bool {
		texts = s.userTexts(th.ID)
		return len(texts) >= 2
	}, 30*time.Second, 50*time.Millisecond)
	assert.Contains(t, texts[1], "and this\n\n<oryn-attachments>\n")
	assert.Contains(t, texts[1], fmt.Sprintf("- %s (image/png, %d bytes)", p2, len(png)))

	// 列表与内容：默认附件下载；仅栅格图片且 inline=1 时内联
	var list struct {
		Attachments []attachmentView `json:"attachments"`
	}
	s.rest("GET", "/api/v1/threads/"+th.ID+"/attachments", nil, &list, 200)
	require.Len(t, list.Attachments, 2)
	assert.Equal(t, []string{a1.ID, a2.ID}, []string{list.Attachments[0].ID, list.Attachments[1].ID})
	require.NotNil(t, list.Attachments[0].ThreadID)
	assert.Equal(t, th.ID, *list.Attachments[0].ThreadID)

	resp := s.orbReq("GET", "/api/v1/attachments/"+a1.ID+"/content", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, bytes.Equal(report, readAll(t, resp)))
	assert.Equal(t, "attachment; filename*=UTF-8''%E5%AD%A3%E5%BA%A6%20%E6%8A%A5%E5%91%8A.bin", resp.Header.Get("Content-Disposition"))
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "sandbox", resp.Header.Get("Content-Security-Policy"))
	resp = s.orbReq("GET", "/api/v1/attachments/"+a1.ID+"/content?inline=true", nil, nil)
	assert.Contains(t, resp.Header.Get("Content-Disposition"), "attachment", "非图片不内联")
	resp = s.orbReq("GET", "/api/v1/attachments/"+a2.ID+"/content?inline=true", nil, nil)
	assert.Equal(t, "inline", resp.Header.Get("Content-Disposition"))
	assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))
	resp = s.orbReq("GET", "/api/v1/attachments/"+a1.ID+"/content", nil, map[string]string{"Range": "bytes=0-9"})
	require.Equal(t, http.StatusPartialContent, resp.StatusCode)
	assert.Equal(t, report[:10], readAll(t, resp))

	// 别的线程的附件用于本线程 → 失败应答（并有 notice），不会悄悄丢掉
	var other struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{
		"prompt": "other", "executor": map[string]any{"kind": "orb", "orbId": th.OrbID},
	}, &other, 201)
	foreign := s.upload("x.txt", other.Thread.ID, []byte("not yours"))
	ws.send(map[string]any{"type": "cmd", "threadId": th.ID, "id": "c2", "frame": map[string]any{
		"type": "prompt", "message": "steal", "orynAttachments": []string{foreign.ID},
	}})
	var notices []string
	r2 := ws.res("c2", &notices)
	assert.Contains(t, string(r2), `"success":false`)
	assert.Contains(t, string(r2), "attachment delivery failed")
	require.Len(t, notices, 1, "订阅者收到一条 notice")
	assert.Contains(t, notices[0], "attachment delivery failed")
	_, err = os.Stat(filepath.Join(home, ".oryn", "attachments", th.ID, foreign.ID))
	assert.True(t, os.IsNotExist(err))

	// 建线程时用别的线程的附件 → 404，且不建线程
	s.rest("POST", "/api/v1/threads", map[string]any{
		"prompt": "steal", "attachments": []string{foreign.ID},
		"executor": map[string]any{"kind": "orb", "orbId": th.OrbID},
	}, nil, 404)

	// 删除附件：content 404，orb 内的副本被尽力删除
	s.rest("DELETE", "/api/v1/attachments/"+a1.ID, nil, nil, 204)
	resp = s.orbReq("GET", "/api/v1/attachments/"+a1.ID+"/content", nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "not_found", errCode(t, resp))
	_, err = os.Stat(filepath.Dir(p1))
	assert.True(t, os.IsNotExist(err), "orb 内副本已删除")
	s.rest("DELETE", "/api/v1/attachments/"+a1.ID, nil, nil, 404)

	// 删除线程：控制面文件与行、orb 内目录都消失
	s.rest("DELETE", "/api/v1/threads/"+th.ID, nil, nil, 204)
	resp = s.orbReq("GET", "/api/v1/attachments/"+a2.ID+"/content", nil, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	_, err = os.Stat(filepath.Join(s.cfg.DataDir, "attachments", a2.ID))
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(filepath.Join(home, ".oryn", "attachments", th.ID))
	assert.True(t, os.IsNotExist(err))
	s.rest("GET", "/api/v1/threads/"+th.ID+"/attachments", nil, nil, 404)

	// 声明的 Content-Length 超过 100MiB：不读请求体，直接 413 too_large
	conn, err := net.Dial("tcp", s.http.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()
	_, err = fmt.Fprintf(conn, "POST /api/v1/attachments?name=big.bin HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nContent-Length: %d\r\n\r\n",
		s.token, attachments.MaxBytes+1)
	require.NoError(t, err)
	big, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusRequestEntityTooLarge, big.StatusCode)
	assert.Equal(t, "too_large", errCode(t, big))
}

// rawWS 是只收原始消息的 WS 客户端（需要 res 的 id / response 字段）。
type rawWS struct {
	t  *testing.T
	c  *websocket.Conn
	ch chan []byte
}

func (s *stack) dialRaw() *rawWS {
	s.t.Helper()
	var tk struct {
		Ticket string `json:"ticket"`
	}
	s.rest("POST", "/api/v1/ws-ticket", nil, &tk, 200)
	c, _, err := websocket.Dial(context.Background(), strings.Replace(s.http.URL, "http", "ws", 1)+"/api/v1/ws?ticket="+tk.Ticket, nil)
	require.NoError(s.t, err)
	c.SetReadLimit(64 << 20)
	w := &rawWS{t: s.t, c: c, ch: make(chan []byte, 4096)}
	go func() {
		defer close(w.ch)
		for {
			_, b, err := c.Read(context.Background())
			if err != nil {
				return
			}
			w.ch <- b
		}
	}()
	s.t.Cleanup(func() { _ = c.CloseNow() })
	return w
}

// answerAsk 等到线程的一个待答 select 请求（快照的 pendingUi 或实时帧）并作答。
func (w *rawWS) answerAsk(threadID string) {
	w.t.Helper()
	deadline := time.After(60 * time.Second)
	for {
		select {
		case b, ok := <-w.ch:
			require.True(w.t, ok, "WS 连接被关闭")
			var m struct {
				Type     string          `json:"type"`
				ThreadID string          `json:"threadId"`
				Frame    json.RawMessage `json:"frame"`
				Agent    struct {
					PendingUI []json.RawMessage `json:"pendingUi"`
				} `json:"agent"`
			}
			if json.Unmarshal(b, &m) != nil || m.ThreadID != threadID {
				continue
			}
			cands := m.Agent.PendingUI
			if m.Type == "frame" {
				cands = []json.RawMessage{m.Frame}
			}
			for _, f := range cands {
				var req struct {
					Type   string `json:"type"`
					ID     string `json:"id"`
					Method string `json:"method"`
				}
				if json.Unmarshal(f, &req) == nil && req.Type == "extension_ui_request" && req.Method == "select" {
					w.send(map[string]any{"type": "ui.response", "threadId": threadID, "frame": map[string]any{"type": "extension_ui_response", "id": req.ID, "value": "alpha"}})
					return
				}
			}
		case <-deadline:
			w.t.Fatal("等待 ask 请求超时")
		}
	}
}

func (w *rawWS) send(v any) {
	b, _ := json.Marshal(v)
	require.NoError(w.t, w.c.Write(context.Background(), websocket.MessageText, b))
}

// res 等待客户端 id 为 id 的 `res`，返回其 response；期间收到的 notice 帧文本追加到 notices。
func (w *rawWS) res(id string, notices *[]string) json.RawMessage {
	w.t.Helper()
	deadline := time.After(60 * time.Second)
	for {
		select {
		case b, ok := <-w.ch:
			require.True(w.t, ok, "WS 连接被关闭")
			var m struct {
				Type     string          `json:"type"`
				ID       string          `json:"id"`
				Response json.RawMessage `json:"response"`
				Frame    struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"frame"`
			}
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			if m.Type == "frame" && m.Frame.Type == "notice" && notices != nil {
				*notices = append(*notices, m.Frame.Message)
			}
			if m.Type == "res" && m.ID == id {
				return m.Response
			}
		case <-deadline:
			w.t.Fatal("等待 res 超时")
		}
	}
}
