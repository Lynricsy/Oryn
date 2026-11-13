package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// wsConn 是一条已鉴权的客户端 WebSocket（`POST ws-ticket` → `GET /api/v1/ws?ticket=`）。
type wsConn struct {
	ws *websocket.Conn
}

// wsEnvelope 是服务端消息的公共字段；Raw 是整条消息。
type wsEnvelope struct {
	Type      string          `json:"type"`
	ThreadID  string          `json:"threadId"`
	ID        string          `json:"id"`
	Code      string          `json:"code"`
	Message   string          `json:"message"`
	Response  json.RawMessage `json:"response"`
	Frame     json.RawMessage `json:"frame"`
	RequestID string          `json:"requestId"`
	Raw       json.RawMessage `json:"-"`
}

// dialWS 取票据并拨号。
func dialWS(ctx context.Context, c *apiClient) (*wsConn, error) {
	var tk apigen.WsTicket
	if err := c.do(ctx, http.MethodPost, "/api/v1/ws-ticket", nil, &tk); err != nil {
		return nil, err
	}
	u, err := url.Parse(c.base)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/api/v1/ws"
	u.RawQuery = url.Values{"ticket": {tk.Ticket}}.Encode()
	ws, _, err := websocket.Dial(ctx, u.String(), nil)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(64 << 20)
	return &wsConn{ws: ws}, nil
}

func (w *wsConn) send(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.ws.Write(ctx, websocket.MessageText, b)
}

// read 读下一条消息；无法解析的消息跳过。
func (w *wsConn) read(ctx context.Context) (wsEnvelope, error) {
	for {
		_, b, err := w.ws.Read(ctx)
		if err != nil {
			return wsEnvelope{}, err
		}
		var m wsEnvelope
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		m.Raw = b
		return m, nil
	}
}

func (w *wsConn) close() { _ = w.ws.Close(websocket.StatusNormalClosure, "") }

// wsCommand 经 WebSocket 下发一条 omp 命令并等待其应答。
func wsCommand(ctx context.Context, c *apiClient, threadID string, frame any) (json.RawMessage, error) {
	conn, err := dialWS(ctx, c)
	if err != nil {
		return nil, err
	}
	defer conn.ws.CloseNow()
	const id = "cli-1"
	if err := conn.send(ctx, map[string]any{"type": "cmd", "threadId": threadID, "id": id, "frame": frame}); err != nil {
		return nil, err
	}
	for {
		m, err := conn.read(ctx)
		if err != nil {
			return nil, err
		}
		if m.ID != id {
			continue
		}
		switch m.Type {
		case "res":
			conn.close()
			return m.Response, nil
		case "error":
			return nil, &apiError{Code: m.Code, Message: m.Message}
		}
	}
}
