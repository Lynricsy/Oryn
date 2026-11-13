package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/version"
)

// 渠道配置（加密存储的明文 JSON；缺省值在校验时补齐）。
type webhookConfig struct {
	URL     string            `json:"url"`
	Secret  string            `json:"secret,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type ntfyConfig struct {
	Server string `json:"server"`
	Topic  string `json:"topic"`
	Token  string `json:"token,omitempty"`
}

type barkConfig struct {
	Server    string `json:"server"`
	DeviceKey string `json:"deviceKey"`
}

const (
	defaultNtfyServer = "https://ntfy.sh"
	defaultBarkServer = "https://api.day.app"
	maxWebhookHeaders = 16
	maxRespBody       = 64 << 10
)

// forbiddenHeaders 是不允许自定义的 Webhook 请求头。
var forbiddenHeaders = map[string]bool{"Host": true, "Content-Length": true, "Content-Type": true}

// httpURL 校验 http(s) 地址并去掉末尾斜杠。
func httpURL(field, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errInvalidChannel("%s must be an http(s) URL", field)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func validWebhook(in *apigen.WebhookChannelConfig) (webhookConfig, error) {
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return webhookConfig{}, errInvalidChannel("webhook.url must be an http(s) URL")
	}
	if len(in.Headers) > maxWebhookHeaders {
		return webhookConfig{}, errInvalidChannel("webhook.headers: at most %d headers", maxWebhookHeaders)
	}
	cfg := webhookConfig{URL: u.String()}
	if in.Secret != nil {
		cfg.Secret = *in.Secret
	}
	if len(in.Headers) > 0 {
		cfg.Headers = make(map[string]string, len(in.Headers))
		for k, v := range in.Headers {
			name := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(k))
			if name == "" || strings.ContainsAny(name, " \t:\r\n") || forbiddenHeaders[name] {
				return webhookConfig{}, errInvalidChannel("webhook.headers: header %q is not allowed", k)
			}
			if strings.ContainsAny(v, "\r\n") {
				return webhookConfig{}, errInvalidChannel("webhook.headers: invalid value for %q", k)
			}
			cfg.Headers[name] = v
		}
	}
	return cfg, nil
}

func validNtfy(in *apigen.NtfyChannelConfig) (ntfyConfig, error) {
	cfg := ntfyConfig{Server: defaultNtfyServer, Topic: strings.TrimSpace(in.Topic)}
	if in.Server != nil && strings.TrimSpace(*in.Server) != "" {
		u, err := httpURL("ntfy.server", *in.Server)
		if err != nil {
			return ntfyConfig{}, err
		}
		cfg.Server = u
	}
	if cfg.Topic == "" || strings.ContainsAny(cfg.Topic, "/ ") {
		return ntfyConfig{}, errInvalidChannel("ntfy.topic is required and must not contain '/' or spaces")
	}
	if in.Token != nil {
		cfg.Token = *in.Token
	}
	return cfg, nil
}

func validBark(in *apigen.BarkChannelConfig) (barkConfig, error) {
	cfg := barkConfig{Server: defaultBarkServer, DeviceKey: strings.TrimSpace(in.DeviceKey)}
	if in.Server != nil && strings.TrimSpace(*in.Server) != "" {
		u, err := httpURL("bark.server", *in.Server)
		if err != nil {
			return barkConfig{}, err
		}
		cfg.Server = u
	}
	if cfg.DeviceKey == "" {
		return barkConfig{}, errInvalidChannel("bark.deviceKey is required")
	}
	return cfg, nil
}

// ---- 发送方 ----

// kindTag / kindEmoji 等按通知种类取值的小表。
var (
	ntfyTags     = map[string]string{KindDone: "white_check_mark", KindInput: "question", KindError: "warning", KindTest: "bell"}
	kindTextKeys = map[string]string{KindDone: "done", KindInput: "input", KindError: "error"}
)

// displayTitle 返回消息标题：线程标题为空时用渠道语言的「未命名线程」。
func displayTitle(m Message) string {
	if m.Title != "" {
		return m.Title
	}
	if m.Kind == KindTest {
		return text(m.Locale, "testTitle")
	}
	return text(m.Locale, "untitled")
}

// displayBody 返回消息正文：通知正文为空时用该种类的固定文案。
func displayBody(m Message) string {
	if m.Body != "" {
		return m.Body
	}
	if m.Kind == KindInput && isPlanFrame(m.Request) {
		return text(m.Locale, "plan")
	}
	if k, ok := kindTextKeys[m.Kind]; ok {
		return text(m.Locale, k)
	}
	return ""
}

func isPlanFrame(frame json.RawMessage) bool {
	var h struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(frame, &h) == nil && h.Type == "plan_proposal"
}

// postJSON 以 JSON 体发 POST，返回结果归类。
func (s *Service) postJSON(ctx context.Context, u string, body []byte, hdr map[string]string) (Result, []byte) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return Result{Err: err}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Oryn/"+version.Version)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return netResult(err), nil
	}
	defer func() { _ = resp.Body.Close() }()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	return httpResult(resp, rb), rb
}

type webhookSender struct {
	s   *Service
	cfg webhookConfig
}

func (s *Service) webhookSender(_ db.NotificationChannel, raw []byte) (Sender, error) {
	var cfg webhookConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return webhookSender{s, cfg}, nil
}

// webhookStates 是各通知种类对应的线程状态（Webhook 体的 thread.state）。
var webhookStates = map[string]string{KindDone: "idle", KindInput: "awaiting_input", KindError: "error"}

func (w webhookSender) Send(ctx context.Context, m Message) Result {
	event := "thread." + m.Kind
	if m.Kind == KindTest {
		event = "test"
	}
	body := map[string]any{"event": event, "id": m.NotificationID, "ts": w.s.now(), "title": displayTitle(m), "body": m.Body}
	if m.ThreadID != "" {
		body["thread"] = map[string]string{"id": m.ThreadID, "title": m.Title, "state": webhookStates[m.Kind], "url": m.URL}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return Result{Err: err}
	}
	hdr := map[string]string{}
	for k, v := range w.cfg.Headers {
		hdr[k] = v
	}
	hdr["X-Oryn-Event"] = event
	if w.cfg.Secret != "" {
		mac := hmac.New(sha256.New, []byte(w.cfg.Secret))
		mac.Write(b)
		hdr["X-Oryn-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	r, _ := w.s.postJSON(ctx, w.cfg.URL, b, hdr)
	return r
}

type ntfySender struct {
	s   *Service
	cfg ntfyConfig
}

func (s *Service) ntfySender(_ db.NotificationChannel, raw []byte) (Sender, error) {
	var cfg ntfyConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return ntfySender{s, cfg}, nil
}

func (n ntfySender) Send(ctx context.Context, m Message) Result {
	priority := 3
	if m.Kind == KindInput {
		priority = 4
	}
	b, err := json.Marshal(map[string]any{
		"topic": n.cfg.Topic, "title": displayTitle(m), "message": displayBody(m), "click": m.URL,
		"tags": []string{ntfyTags[m.Kind]}, "priority": priority,
	})
	if err != nil {
		return Result{Err: err}
	}
	hdr := map[string]string{}
	if n.cfg.Token != "" {
		hdr["Authorization"] = "Bearer " + n.cfg.Token
	}
	r, _ := n.s.postJSON(ctx, n.cfg.Server, b, hdr)
	return r
}

type barkSender struct {
	s   *Service
	cfg barkConfig
}

func (s *Service) barkSender(_ db.NotificationChannel, raw []byte) (Sender, error) {
	var cfg barkConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return barkSender{s, cfg}, nil
}

func (b barkSender) Send(ctx context.Context, m Message) Result {
	level := "active"
	if m.Kind == KindInput {
		level = "timeSensitive"
	}
	body, err := json.Marshal(map[string]any{
		"device_key": b.cfg.DeviceKey, "title": displayTitle(m), "body": displayBody(m), "url": m.URL, "group": "Oryn", "level": level,
	})
	if err != nil {
		return Result{Err: err}
	}
	r, rb := b.s.postJSON(ctx, b.cfg.Server+"/push", body, nil)
	if r.Err != nil {
		return r
	}
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rb, &resp); err != nil || resp.Code != 200 {
		return Result{Err: fmt.Errorf("bark: code %d: %s", resp.Code, truncate(resp.Message, 200))}
	}
	return Result{}
}
