// Package mcpprobe 以 MCP 客户端的身份连上一个 Streamable HTTP 服务器，列出它的工具（M26：主人勾选后台研究可用的
// 只读工具时用）。只做握手与 tools/list，不调用任何工具；不引入第三方依赖。
package mcpprobe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/version"
)

// Tool 是服务器自报的一个工具。ReadOnly 取自 annotations.readOnlyHint：服务器没说时为 nil；这只是服务器的说法，不是保证。
type Tool struct {
	Name        string
	Description string
	ReadOnly    *bool
}

const (
	// protocolVersion 是 initialize 请求的协议版本；之后的请求带服务器协商回来的版本。
	protocolVersion = "2025-06-18"
	// bodyMax 是单个响应体的上限。
	bodyMax = 2 << 20
	// pagesMax 是 tools/list 翻页的上限；超过时返回已取到的部分。
	pagesMax = 10
	// redirectsMax 是同主机重定向的跟随上限。
	redirectsMax = 10
	// endTimeout 是结束会话（DELETE）的时限：调用方的 ctx 已到期时仍尽力结束一次。
	endTimeout = 5 * time.Second
	// errTextMax 是错误信息里引用服务器文字的最大长度（字节）。
	errTextMax = 200
	// redactMin 是参与错误脱敏的请求头值的最短长度：更短的值替换起来只会误伤正文。
	redactMin = 4
)

var errTooLarge = errors.New("response body exceeds 2 MiB")

// ListTools 依次发送 initialize、notifications/initialized 与 tools/list（按 nextCursor 翻页，至多 pagesMax 页），
// 服务器给了会话 id 时最后 DELETE 结束会话（忽略结果）。headers 是要带上的请求头（通常是鉴权头）；hc 为 nil 时用
// http.DefaultClient。不跟随跨主机（或换 scheme）的重定向。整体时限由 ctx 决定。返回的错误不含请求地址与任何请求头的值。
func ListTools(ctx context.Context, hc *http.Client, url string, headers map[string]string) ([]Tool, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	c := *hc
	c.CheckRedirect = sameOriginRedirect
	p := &prober{hc: &c, url: url, headers: headers}
	tools, err := p.listTools(ctx)
	if err != nil {
		return nil, p.scrub(err)
	}
	return tools, nil
}

// sameOriginRedirect 只跟随同 scheme、同主机的重定向：自定义的鉴权头在跨主机重定向时也会被 Go 的客户端带过去。
func sameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= redirectsMax {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
		return errors.New("refusing cross-host redirect")
	}
	return nil
}

type prober struct {
	hc      *http.Client
	url     string
	headers map[string]string
	version string // 协商后的协议版本；握手完成前为空
	session string // Mcp-Session-Id
	nextID  int
}

func (p *prober) listTools(ctx context.Context) ([]Tool, error) {
	init, err := p.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "oryn", "version": version.Version},
	})
	if p.session != "" {
		defer p.end(ctx)
	}
	if err != nil {
		return nil, err
	}
	var ir struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	p.version = protocolVersion
	if json.Unmarshal(init, &ir) == nil && ir.ProtocolVersion != "" {
		p.version = ir.ProtocolVersion
	}
	if err := p.notify(ctx, "notifications/initialized"); err != nil {
		return nil, err
	}
	var tools []Tool
	cursor := ""
	for range pagesMax {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := p.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Annotations struct {
					ReadOnlyHint *bool `json:"readOnlyHint"`
				} `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("tools/list: invalid result: %w", err)
		}
		for _, t := range res.Tools {
			if t.Name == "" {
				continue
			}
			tools = append(tools, Tool{Name: t.Name, Description: t.Description, ReadOnly: t.Annotations.ReadOnlyHint})
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return tools, nil
}

// newRequest 构造带调用方请求头、协议头与会话 id 的请求。
func (p *prober) newRequest(ctx context.Context, method string, body []byte) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.url, rd)
	if err != nil {
		return nil, err
	}
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if p.version != "" {
		req.Header.Set("Mcp-Protocol-Version", p.version)
	}
	if p.session != "" {
		req.Header.Set("Mcp-Session-Id", p.session)
	}
	return req, nil
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call 发一个 JSON-RPC 请求并返回 result。
func (p *prober) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	p.nextID++
	id := p.nextID
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	req, err := p.newRequest(ctx, http.MethodPost, body)
	if err != nil {
		return nil, err
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()
	if method == "initialize" {
		p.session = resp.Header.Get("Mcp-Session-Id")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", method, resp.StatusCode)
	}
	rr, err := readResponse(resp, strconv.Itoa(id))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	if rr.Error != nil {
		return nil, fmt.Errorf("%s: JSON-RPC error %d: %s", method, rr.Error.Code, clip(rr.Error.Message))
	}
	if len(rr.Result) == 0 || string(rr.Result) == "null" {
		return nil, fmt.Errorf("%s: response has no result", method)
	}
	return rr.Result, nil
}

// notify 发一个 JSON-RPC 通知；服务器应回 202（也接受其它 2xx），不读应答内容。
func (p *prober) notify(ctx context.Context, method string) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		return err
	}
	req, err := p.newRequest(ctx, http.MethodPost, body)
	if err != nil {
		return err
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	drain(resp)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: HTTP %d", method, resp.StatusCode)
	}
	return nil
}

// end 以 DELETE 结束会话，忽略一切错误。
func (p *prober) end(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), endTimeout)
	defer cancel()
	req, err := p.newRequest(ctx, http.MethodDelete, nil)
	if err != nil {
		return
	}
	if resp, err := p.hc.Do(req); err == nil {
		drain(resp)
	}
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, bodyMax))
	resp.Body.Close()
}

// readResponse 按 Content-Type 读应答：JSON 直接解析；SSE 逐个事件解析，取 id 等于 want 的那条（服务器的请求与通知跳过）。
func readResponse(resp *http.Response, want string) (rpcResponse, error) {
	body := &capped{r: resp.Body, n: bodyMax}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mt {
	case "application/json":
		b, err := io.ReadAll(body)
		if err != nil {
			return rpcResponse{}, err
		}
		rr, ok := matchResponse(b, want)
		if !ok {
			return rpcResponse{}, errors.New("invalid JSON-RPC response")
		}
		return rr, nil
	case "text/event-stream":
		br := bufio.NewReader(body)
		var data []string
		for {
			line, err := br.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return rpcResponse{}, err
			}
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
			// 空行（或流结束）分派一个事件
			if (line == "" || err != nil) && len(data) > 0 {
				if rr, ok := matchResponse([]byte(strings.Join(data, "\n")), want); ok {
					return rr, nil
				}
				data = data[:0]
			}
			if err != nil {
				return rpcResponse{}, errors.New("event stream ended without a response")
			}
		}
	default:
		return rpcResponse{}, fmt.Errorf("unexpected Content-Type %q", clip(mt))
	}
}

// matchResponse 报告 b 是否是 id 等于 want 的 JSON-RPC 应答（带 result 或 error）。
func matchResponse(b []byte, want string) (rpcResponse, bool) {
	var rr rpcResponse
	if json.Unmarshal(b, &rr) != nil || string(bytes.TrimSpace(rr.ID)) != want || (rr.Result == nil && rr.Error == nil) {
		return rpcResponse{}, false
	}
	return rr, true
}

// capped 读满 n 字节后，底层还有内容时报 errTooLarge。
type capped struct {
	r io.Reader
	n int64
}

func (c *capped) Read(b []byte) (int, error) {
	if c.n <= 0 {
		var one [1]byte
		k, err := c.r.Read(one[:])
		if k > 0 {
			return 0, errTooLarge
		}
		return 0, err
	}
	if int64(len(b)) > c.n {
		b = b[:c.n]
	}
	k, err := c.r.Read(b)
	c.n -= int64(k)
	return k, err
}

// scrub 去掉错误里的请求地址（*url.Error 的外层带完整 URL），并把请求头的值（以及 `<scheme> <凭据>` 里的凭据部分）
// 换成 [redacted]：服务器可能在错误信息里回显它们。
func (p *prober) scrub(err error) error {
	msg := err.Error()
	if ue, ok := errors.AsType[*url.Error](err); ok {
		msg = strings.Replace(msg, ue.Error(), ue.Err.Error(), 1)
	}
	for _, v := range p.headers {
		v = strings.TrimSpace(v)
		if len(v) >= redactMin {
			msg = strings.ReplaceAll(msg, v, "[redacted]")
		}
		if _, cred, ok := strings.Cut(v, " "); ok && len(strings.TrimSpace(cred)) >= redactMin {
			msg = strings.ReplaceAll(msg, strings.TrimSpace(cred), "[redacted]")
		}
	}
	return errors.New(msg)
}

// clip 截短服务器给的文字。
func clip(s string) string {
	if len(s) <= errTextMax {
		return s
	}
	return strings.ToValidUTF8(s[:errTextMax], "") + "…"
}
