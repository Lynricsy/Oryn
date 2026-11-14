package orbdrt

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/coder/websocket"
)

// 浏览器级 CDP 调用（orbd 自己用，不经前门）：优雅关闭浏览器、取 / 回灌 Cookie。
//
// 为什么需要它（在 orb 镜像里的 Chromium 156 上实测）：
//   - Chromium 的 Cookie 库大约每 30 秒才写一次盘。对进程组发 SIGTERM 时，网络服务进程和主进程同时被杀，
//     还没写盘的 Cookie 全部丢失。只给主进程发 SIGTERM 也一样会丢：主进程走快速退出，网络服务被直接结束。
//     只有 CDP `Browser.close` 走完整的关闭流程，Cookie 和 Local State 都会落盘。
//   - 活体保存时浏览器必须继续运行，没有能强制 Cookie 落盘的 CDP 方法。所以导出前用 `Storage.getCookies`
//     取一份当时的持久 Cookie，作为归档里的附加文件 cookieSnapshotRel；装载后浏览器第一次启动时用
//     `Storage.clearCookies` + `Storage.setCookies` 回灌，然后删掉这个文件。

// cookieSnapshotRel 是活体归档里 Cookie 快照的相对路径（JSON 数组，元素是 CDP 的 Network.Cookie）。
const cookieSnapshotRel = "Default/oryn-cookies.json"

// cdpTimeout 是单次 CDP 调用（含拨号）的上限。
const cdpTimeout = 10 * time.Second

// cdpReadLimit 是 CDP 应答的大小上限（Storage.getCookies 在 Cookie 很多时可能有几 MiB）。
const cdpReadLimit = 64 << 20

// errCDPNoReply 表示命令已经发出，但应答到达前连接就断了（Browser.close 时可能出现，此时浏览器多半已在退出）。
var errCDPNoReply = errors.New("cdp: connection closed before the reply")

// cdpError 是 CDP 应答里的 error 字段。
type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *cdpError) Error() string { return fmt.Sprintf("cdp error %d: %s", e.Code, e.Message) }

// cdpCall 连到 DevTools 端口上的浏览器级目标，发一条命令并等应答；result 非 nil 时解码 result 字段。
func cdpCall(ctx context.Context, port int, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(ctx, cdpTimeout)
	defer cancel()
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/version", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("cdp: %w", err)
	}
	var ver struct {
		URL string `json:"webSocketDebuggerUrl"`
	}
	err = json.NewDecoder(resp.Body).Decode(&ver)
	_ = resp.Body.Close()
	if err != nil || ver.URL == "" {
		return fmt.Errorf("cdp: no browser websocket in /json/version (status %d): %v", resp.StatusCode, err)
	}
	c, _, err := websocket.Dial(ctx, ver.URL, nil)
	if err != nil {
		return fmt.Errorf("cdp: dial: %w", err)
	}
	defer c.CloseNow()
	c.SetReadLimit(cdpReadLimit)
	if params == nil {
		params = struct{}{}
	}
	msg, err := json.Marshal(map[string]any{"id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	if err := c.Write(ctx, websocket.MessageText, msg); err != nil {
		return fmt.Errorf("cdp: send %s: %w", method, err)
	}
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return fmt.Errorf("%w (%s): %v", errCDPNoReply, method, err)
		}
		var m struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *cdpError       `json:"error"`
		}
		if json.Unmarshal(b, &m) != nil || m.ID != 1 {
			continue // 事件或别的消息
		}
		if m.Error != nil {
			return fmt.Errorf("%s: %w", method, m.Error)
		}
		if result != nil {
			if err := json.Unmarshal(m.Result, result); err != nil {
				return fmt.Errorf("cdp: decode %s result: %w", method, err)
			}
		}
		_ = c.Close(websocket.StatusNormalClosure, "")
		return nil
	}
}

// liveCookies 经 CDP 取浏览器里的全部持久 Cookie，编码成 cookieSnapshotRel 的内容。会话 Cookie
// （session=true）不收，与关闭式保存一致：浏览器正常关闭时也不会把它们写盘。
// 结果按 domain / path / name / partitionKey 排序；Cookie 不变时字节也不变，控制面据此去重。
func liveCookies(ctx context.Context, port int) ([]byte, error) {
	var res struct {
		Cookies []json.RawMessage `json:"cookies"`
	}
	if err := cdpCall(ctx, port, "Storage.getCookies", nil, &res); err != nil {
		return nil, err
	}
	type entry struct {
		key [4]string
		raw json.RawMessage
	}
	out := make([]entry, 0, len(res.Cookies))
	for _, raw := range res.Cookies {
		var c struct {
			Name         string          `json:"name"`
			Domain       string          `json:"domain"`
			Path         string          `json:"path"`
			Session      bool            `json:"session"`
			PartitionKey json.RawMessage `json:"partitionKey"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("cdp: decode cookie: %w", err)
		}
		if c.Session {
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			return nil, err
		}
		out = append(out, entry{key: [4]string{c.Domain, c.Path, c.Name, string(c.PartitionKey)}, raw: compact.Bytes()})
	}
	slices.SortFunc(out, func(a, b entry) int {
		for i := range a.key {
			if c := cmp.Compare(a.key[i], b.key[i]); c != 0 {
				return c
			}
		}
		return 0
	})
	list := make([]json.RawMessage, len(out))
	for i, e := range out {
		list[i] = e.raw
	}
	return json.Marshal(list)
}

// restoreCookies 在浏览器刚启动、DevTools 就绪后执行：profile 里有 cookieSnapshotRel 时，先清空浏览器里的
// Cookie（启动时从可能过时的 Cookie 库读出的那份），再整体写入快照，然后删除快照文件。无论回灌成败都删除，
// 免得之后某次重启又拿这份旧快照覆盖掉新的 Cookie；失败只记日志。
func (d *desktop) restoreCookies(ctx context.Context, port int) {
	p := filepath.Join(d.profileDir(), filepath.FromSlash(cookieSnapshotRel))
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	defer func() {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			d.log.Warn("remove cookie snapshot", "err", err)
		}
	}()
	var cookies []json.RawMessage
	if err == nil {
		err = json.Unmarshal(b, &cookies)
	}
	if err == nil {
		err = cdpCall(ctx, port, "Storage.clearCookies", nil, nil)
	}
	if err == nil {
		err = cdpCall(ctx, port, "Storage.setCookies", map[string]any{"cookies": cookies}, nil)
	}
	if err != nil {
		d.log.Warn("restore cookie snapshot failed", "err", err)
		return
	}
	d.log.Info("restored cookie snapshot", "cookies", len(cookies))
}
