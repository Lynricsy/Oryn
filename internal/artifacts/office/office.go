// Package office 是 Office 引擎（WOPI 客户端；参考实现 Collabora Online）的客户端（M32）：读 discovery 的 action 表与
// proof-key、读引擎能力、调用转换接口。会话、占用与 WOPI 端点在 artifacts 包里（它们要直接用内容存储与写入路径）。
//
// 协议核实结论（collabora/code 26.04.6.2，2026-11-27 实测；计划步骤 9）：
//
//   - PutFile 带 X-COOL-WOPI-IsAutosave / X-COOL-WOPI-IsExitSave：显式 Action_Save 两者都为 false；空闲自动保存
//     （idlesave 30 秒）IsAutosave=true；移除 iframe 时做一次退出保存，IsExitSave=true，随后 UNLOCK。另带
//     X-COOL-WOPI-IsModifiedByUser 与 X-COOL-WOPI-Timestamp（上一次 CheckFileInfo / PutFile 回的 LastModifiedTime）。
//     因此不需要单独的「提交」端点：自动保存只更新工作副本，显式保存与退出保存才落版本。
//   - CheckFileInfo 声明 SupportsLocks 时，引擎加载后 LOCK（锁串形如 cool-lock<hex>），PutFile 带 X-WOPI-Lock，退出后 UNLOCK。
//   - 默认镜像不发 X-WOPI-Proof：没有 /etc/coolwsd/proof_key，discovery 里也没有 proof-key 元素。discovery 没有
//     proof-key 时跳过验签（记一次日志）；有 proof-key 时必须验签。
//   - POST <internal_url>/cool/convert-to/<fmt>（multipart 字段 data）可用：docx→pdf、docx→doc、doc→docx 都回 200；
//     /hosting/capabilities 的 convert-to.available 报告是否可用。
//   - 浏览器侧 postMessage：App_LoadingStatus（Status 为 Initialized / Frame_Ready / Document_Loaded）、
//     Action_Save → Action_Save_Resp {success}、Action_Paste {Mimetype, Data}、Doc_ModifiedStatus、Doc_LastModifiedTime；
//     宿主回 Host_PostmessageReady。
//   - 宏默认不执行（coolwsd.xml 的 security.enable_macros_execution 缺省 false）。
//   - discovery 的 edit urlsrc 形如 http://<engine>/browser/<hash>/cool.html?，主机是引擎自己认为的地址，
//     拼给浏览器时要换成 artifacts.office.url。
package office

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DiscoveryTTL 是 discovery 与能力的缓存时长；FailTTL 是失败结果的缓存时长（引擎不可用时不每次都去等超时）。
const (
	DiscoveryTTL = 10 * time.Minute
	FailTTL      = 30 * time.Second
	// probeTimeout 是读 discovery 与能力的超时。
	probeTimeout = 10 * time.Second
	// maxDiscoveryBytes 是 discovery 与能力响应体的上限。
	maxDiscoveryBytes = 4 << 20
)

// Config 是引擎客户端的配置。URL 是浏览器访问引擎的地址，InternalURL 是控制面访问引擎的地址（空 = 同 URL）；
// 两者都不带结尾斜杠。Client 为空用不带总超时的默认客户端（转换大文件可能要几分钟，靠 ctx 取消）；Now 为空用 time.Now。
type Config struct {
	URL, InternalURL string
	Client           *http.Client
	Now              func() time.Time
}

// Engine 是一个 Office 引擎。
type Engine struct {
	url, internal string
	client        *http.Client
	now           func() time.Time

	mu      sync.Mutex
	disc    cached[*Discovery]
	caps    cached[Capabilities]
	discFil sync.Mutex // 串行化 discovery 的拉取（同一时刻只有一个请求打到引擎）
	capsFil sync.Mutex
}

type cached[T any] struct {
	v   T
	err error
	at  time.Time
	ok  bool
}

// New 创建引擎客户端。
func New(cfg Config) *Engine {
	internal := cfg.InternalURL
	if internal == "" {
		internal = cfg.URL
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Engine{url: strings.TrimRight(cfg.URL, "/"), internal: strings.TrimRight(internal, "/"), client: client, now: now}
}

// URL 返回浏览器访问引擎的地址（不带结尾斜杠）。
func (e *Engine) URL() string { return e.url }

// Discovery 是 /hosting/discovery 的解析结果。
type Discovery struct {
	// actions 是 扩展名 → action 名 → urlsrc（已去掉 <…> 占位）。
	actions map[string]map[string]string
	// Proof 是验签公钥；nil = discovery 没有 proof-key（引擎不发 X-WOPI-Proof）。
	Proof *ProofKeys
}

// Action 返回扩展名 ext（不带点，小写）的 action（如 edit）的 urlsrc。
func (d *Discovery) Action(ext, action string) (string, bool) {
	u, ok := d.actions[ext][action]
	return u, ok
}

type xmlDiscovery struct {
	Zones []struct {
		Apps []struct {
			Actions []struct {
				Name   string `xml:"name,attr"`
				Ext    string `xml:"ext,attr"`
				URLSrc string `xml:"urlsrc,attr"`
			} `xml:"action"`
		} `xml:"app"`
	} `xml:"net-zone"`
	ProofKey *struct {
		Modulus     string `xml:"modulus,attr"`
		Exponent    string `xml:"exponent,attr"`
		OldModulus  string `xml:"oldmodulus,attr"`
		OldExponent string `xml:"oldexponent,attr"`
	} `xml:"proof-key"`
}

// placeholderRe 匹配 urlsrc 里 WOPI 约定的可选参数占位（如 <ui=UI_LLCC&>）。
var placeholderRe = regexp.MustCompile(`<[^>]*>`)

// ParseDiscovery 解析 discovery XML。没有任何 action 视为错误。
func ParseDiscovery(r io.Reader) (*Discovery, error) {
	var x xmlDiscovery
	if err := xml.NewDecoder(r).Decode(&x); err != nil {
		return nil, fmt.Errorf("解析 discovery 失败：%w", err)
	}
	d := &Discovery{actions: map[string]map[string]string{}}
	for _, z := range x.Zones {
		for _, app := range z.Apps {
			for _, a := range app.Actions {
				ext := strings.ToLower(a.Ext)
				if ext == "" || a.Name == "" || a.URLSrc == "" {
					continue
				}
				if d.actions[ext] == nil {
					d.actions[ext] = map[string]string{}
				}
				if _, dup := d.actions[ext][a.Name]; !dup {
					d.actions[ext][a.Name] = placeholderRe.ReplaceAllString(a.URLSrc, "")
				}
			}
		}
	}
	if len(d.actions) == 0 {
		return nil, errors.New("discovery 里没有任何 action")
	}
	if pk := x.ProofKey; pk != nil && pk.Modulus != "" {
		keys, err := parseProofKeys(pk.Modulus, pk.Exponent, pk.OldModulus, pk.OldExponent)
		if err != nil {
			return nil, err
		}
		d.Proof = keys
	}
	return d, nil
}

// Discover 返回引擎的 discovery（成功缓存 DiscoveryTTL，失败缓存 FailTTL）。
func (e *Engine) Discover(ctx context.Context) (*Discovery, error) {
	return load(e, &e.discFil, &e.disc, func() (*Discovery, error) {
		body, err := e.get(ctx, "/hosting/discovery")
		if err != nil {
			return nil, err
		}
		defer body.Close()
		return ParseDiscovery(io.LimitReader(body, maxDiscoveryBytes))
	})
}

// Capabilities 是 /hosting/capabilities 里用到的部分。
type Capabilities struct {
	// Convert 报告 convert-to 是否可用。
	Convert bool
	// Product 是产品名与版本（日志与原因文案用）。
	Product string
}

// Capabilities 返回引擎能力（缓存规则同 Discover）。
func (e *Engine) Capabilities(ctx context.Context) (Capabilities, error) {
	return load(e, &e.capsFil, &e.caps, func() (Capabilities, error) {
		body, err := e.get(ctx, "/hosting/capabilities")
		if err != nil {
			return Capabilities{}, err
		}
		defer body.Close()
		var x struct {
			ConvertTo struct {
				Available bool `json:"available"`
			} `json:"convert-to"`
			ProductName    string `json:"productName"`
			ProductVersion string `json:"productVersion"`
		}
		if err := json.NewDecoder(io.LimitReader(body, maxDiscoveryBytes)).Decode(&x); err != nil {
			return Capabilities{}, fmt.Errorf("解析 capabilities 失败：%w", err)
		}
		return Capabilities{Convert: x.ConvertTo.Available, Product: strings.TrimSpace(x.ProductName + " " + x.ProductVersion)}, nil
	})
}

// get 以 probeTimeout 取引擎的一个只读端点。
func (e *Engine) get(ctx context.Context, path string) (io.ReadCloser, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.internal+path, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("访问 Office 引擎 %s 失败：%w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("访问 Office 引擎 %s 回 HTTP %d", path, resp.StatusCode)
	}
	return cancelOnClose{resp.Body, cancel}, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// ConvertError 是引擎拒绝转换时的错误（Reason 是引擎回的文本，截到 500 字节）。
type ConvertError struct {
	Status int
	Reason string
}

func (e *ConvertError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("Office 引擎转换失败（HTTP %d）", e.Status)
	}
	return fmt.Sprintf("Office 引擎转换失败（HTTP %d）：%s", e.Status, e.Reason)
}

// load 取缓存；过期时由 fill 串行化地重新拉取一次。
func load[T any](e *Engine, fill *sync.Mutex, c *cached[T], fetch func() (T, error)) (T, error) {
	fresh := func() (cached[T], bool) {
		e.mu.Lock()
		defer e.mu.Unlock()
		ttl := DiscoveryTTL
		if c.err != nil {
			ttl = FailTTL
		}
		return *c, c.ok && e.now().Sub(c.at) < ttl
	}
	if got, ok := fresh(); ok {
		return got.v, got.err
	}
	fill.Lock()
	defer fill.Unlock()
	if got, ok := fresh(); ok {
		return got.v, got.err
	}
	v, err := fetch()
	e.mu.Lock()
	*c = cached[T]{v: v, err: err, at: e.now(), ok: true}
	e.mu.Unlock()
	return v, err
}

var formatRe = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// Convert 把 r（文件名 name，引擎据扩展名识别格式）交给引擎转成 format（如 pdf、docx），流式返回结果；调用方负责关闭。
// 请求体经管道流式发送，不在内存里攒整个文件。
func (e *Engine) Convert(ctx context.Context, r io.Reader, name, format string) (io.ReadCloser, error) {
	if !formatRe.MatchString(format) {
		return nil, fmt.Errorf("不支持的目标格式 %q", format)
	}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("data", name)
		if err == nil {
			_, err = io.Copy(part, r)
		}
		if err == nil {
			err = mw.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.internal+"/cool/convert-to/"+url.PathEscape(format), pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := e.client.Do(req)
	if err != nil {
		_ = pr.CloseWithError(err)
		return nil, fmt.Errorf("访问 Office 引擎失败：%w", err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		_ = resp.Body.Close()
		_ = pr.CloseWithError(errors.New("引擎已拒绝"))
		return nil, &ConvertError{Status: resp.StatusCode, Reason: strings.TrimSpace(string(b))}
	}
	return resp.Body, nil
}
