package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// maxHindsightBody 是 orynd 自己解析的 Hindsight 响应体上限：召回与记忆列表（M17 主人记忆）可能有几百 KB，
// 超出会被截断成无法解析的 JSON。
const maxHindsightBody = 8 << 20

// bankMission 是 orynd 创建 bank `oryn` 时写入的 reflect mission。orb 发来的建 bank 请求一律丢弃 body，
// 共享 bank 的 mission 只由这里决定。
const bankMission = "这是 Oryn 的共享记忆库，记录主人在各个软件项目中的约定、决策、偏好与踩坑经验。" +
	"带 `project:<项目>` 标签的记忆只属于该项目；带 `oryn:global` 标签的是主人批准过的全局记忆，适用于所有项目。" +
	"回答与提炼一律使用简体中文，保留代码、命令、路径与专有名词的原文。"

// Hindsight 是 orynd 访问 Hindsight API 的客户端：根地址与 API key 来自记忆设置，请求一律带 Bearer。
// 只包含 orynd 自己用到的端点；网关转发用 NewRequest / Do。
type Hindsight struct {
	base string
	key  string
	http *http.Client
}

// NewHindsight 以根地址（不带末尾斜杠）与 API key 构造客户端。
func NewHindsight(base, key string, hc *http.Client) *Hindsight {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Hindsight{base: strings.TrimRight(base, "/"), key: key, http: hc}
}

// HTTPError 是 Hindsight 返回的非 2xx 应答。
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Hindsight 返回 %d：%s", e.Status, e.Body)
}

// statusOf 取 err 里 Hindsight 的 HTTP 状态；不是 HTTPError 时返回 0。
func statusOf(err error) int {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// Key 返回真实的 API key（只供网关做响应脱敏，不得写进任何发往 orb 的内容）。
func (h *Hindsight) Key() string { return h.key }

// NewRequest 构造发往 Hindsight 的请求：path 以 `/` 开头（可带查询串），自动带 Bearer。
func (h *Hindsight) NewRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, h.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.key)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// Do 发送请求。
func (h *Hindsight) Do(req *http.Request) (*http.Response, error) { return h.http.Do(req) }

// call 发送 JSON 请求并把 2xx 应答解进 out（out 为 nil 时丢弃）；非 2xx 返回 *HTTPError。
func (h *Hindsight) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(buf)
	}
	req, err := h.NewRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHindsightBody))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &HTTPError{Status: resp.StatusCode, Body: truncate(string(raw), 300)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("解析 Hindsight 应答失败：%w", err)
	}
	return nil
}

// Version 读 `/version` 的 `api_version`。
func (h *Hindsight) Version(ctx context.Context) (string, error) {
	var v struct {
		APIVersion string `json:"api_version"`
	}
	if err := h.call(ctx, http.MethodGet, "/version", nil, &v); err != nil {
		return "", err
	}
	return v.APIVersion, nil
}

// Ready 探测 `/health/ready`：200 = 数据库可达；其余状态返回 *HTTPError。
func (h *Hindsight) Ready(ctx context.Context) error {
	return h.call(ctx, http.MethodGet, "/health/ready", nil, nil)
}

// BankStats 是 `/v1/default/banks/{bank}/stats` 里 orynd 关心的字段。
type BankStats struct {
	// TotalNodes 是记忆单元（事实）总数，即状态页的「记忆数」。
	TotalNodes     int64 `json:"total_nodes"`
	TotalDocuments int64 `json:"total_documents"`
}

// BankStats 读 bank 的统计；bank 不存在时返回 Status 404 的 *HTTPError。
func (h *Hindsight) BankStats(ctx context.Context, bank string) (BankStats, error) {
	var st BankStats
	err := h.call(ctx, http.MethodGet, "/v1/default/banks/"+url.PathEscape(bank)+"/stats", nil, &st)
	return st, err
}

// EnsureBank 创建（或更新）bank 并写入 Oryn 的中文 mission。Hindsight 的 PUT 是 upsert，重复调用无副作用。
func (h *Hindsight) EnsureBank(ctx context.Context, bank string) error {
	return h.call(ctx, http.MethodPut, "/v1/default/banks/"+url.PathEscape(bank),
		map[string]string{"reflect_mission": bankMission}, nil)
}

// truncate 按字节截断（保持 UTF-8 完整）。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
