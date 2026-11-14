// fakedesk 是 orbd 桌面栈（M8）的测试替身：按 argv[0] 的文件名扮演三个程序。
//
//   - Xtigervnc：解析 -rfbunixpath，在该 unix socket 上做字节回显；收到 SIGTERM 退出。
//   - openbox：挂起直到被杀（SIGTERM 正常退出）。
//   - chromium：解析 --user-data-dir；在临时端口上应答 `/json/version`，其它路径回显方法、路径与请求体；
//     写 `DevToolsActivePort`（先写临时文件再 rename）；在 profile 里建 `Default/Cookies`；
//     每收到一次 SIGCONT 往 `Default/sigcont` 追加一个字节。
//     `/devtools/browser/…` 是浏览器级 CDP（WebSocket）：Storage.getCookies / setCookies / clearCookies 操作内存里的
//     Cookie 表，Browser.close 把它写进 `Default/fake-cookies.json`、写 `Default/exit-clean` 后退出。
//     和真 Chromium 一样，Cookie 只在正常关闭时落盘：收到 SIGTERM 时只写 `Default/exit-term` 就退出，内存里的
//     Cookie 丢失。启动时从 `Default/fake-cookies.json` 读回 Cookie 表。
//
// 用法：构建后以 Xtigervnc / openbox / chromium 三个名字的符号链接放进 PATH。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/coder/websocket"
)

func main() {
	name := filepath.Base(os.Args[0])
	var err error
	switch name {
	case "Xtigervnc":
		err = runXvnc(os.Args[1:])
	case "openbox":
		err = runOpenbox()
	case "chromium":
		err = runChromium(os.Args[1:])
	default:
		err = fmt.Errorf("fakedesk: unknown role %q (link me as Xtigervnc, openbox or chromium)", name)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runXvnc 在 -rfbunixpath 上回显字节，直到 SIGTERM。
func runXvnc(args []string) error {
	path := ""
	for i := range len(args) - 1 {
		if args[i] == "-rfbunixpath" {
			path = args[i+1]
		}
	}
	if path == "" {
		return errors.New("fakedesk Xtigervnc: -rfbunixpath is required")
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	<-sig
	_ = ln.Close()
	return nil
}

func runOpenbox() error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	<-sig
	return nil
}

func runChromium(args []string) error {
	dir := ""
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--user-data-dir="); ok {
			dir = v
		}
	}
	if dir == "" {
		return errors.New("fakedesk chromium: --user-data-dir is required")
	}
	def := filepath.Join(dir, "Default")
	if err := os.MkdirAll(def, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(def, "Cookies"), []byte("fake cookies"), 0o600); err != nil {
		return err
	}
	jar, err := loadJar(filepath.Join(def, "fake-cookies.json"))
	if err != nil {
		return err
	}
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGCONT)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"Browser":              "FakeChromium/1.0",
			"webSocketDebuggerUrl": "ws://" + r.Host + "/devtools/browser/fake",
		})
	})
	mux.HandleFunc("/devtools/browser/", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		jar.serve(r.Context(), c, def)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "echo %s %s %s", r.Method, r.URL.Path, body)
	})
	go func() { _ = http.Serve(ln, mux) }()

	tmp := filepath.Join(dir, ".DevToolsActivePort.tmp")
	if err := os.WriteFile(tmp, fmt.Appendf(nil, "%d\n/devtools/browser/fake\n", port), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "DevToolsActivePort")); err != nil {
		return err
	}
	for s := range sig {
		switch s {
		case syscall.SIGCONT:
			f, err := os.OpenFile(filepath.Join(def, "sigcont"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err == nil {
				_, _ = f.Write([]byte{1})
				_ = f.Close()
			}
		case syscall.SIGTERM:
			_ = os.WriteFile(filepath.Join(def, "exit-term"), []byte{1}, 0o600)
			return nil
		}
	}
	return nil
}

// cookieJar 是假浏览器内存里的 Cookie 表（键 = domain、path、name），按插入顺序返回。
type cookieJar struct {
	mu      sync.Mutex
	cookies []map[string]any
}

func loadJar(path string) (*cookieJar, error) {
	j := &cookieJar{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	return j, json.Unmarshal(b, &j.cookies)
}

func cookieKey(c map[string]any) string {
	return fmt.Sprint(c["domain"], "\x00", c["path"], "\x00", c["name"])
}

// set 按键覆盖或追加；与 CDP 一样，没有正的 expires 就是会话 Cookie。
func (j *cookieJar) set(in []map[string]any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range in {
		exp, _ := c["expires"].(float64)
		c["session"] = exp <= 0
		if exp <= 0 {
			c["expires"] = -1
		}
		k := cookieKey(c)
		replaced := false
		for i, old := range j.cookies {
			if cookieKey(old) == k {
				j.cookies[i], replaced = c, true
			}
		}
		if !replaced {
			j.cookies = append(j.cookies, c)
		}
	}
}

// flush 把持久 Cookie 写盘（会话 Cookie 不写，同真 Chromium）。
func (j *cookieJar) flush(path string) error {
	j.mu.Lock()
	keep := []map[string]any{}
	for _, c := range j.cookies {
		if s, _ := c["session"].(bool); !s {
			keep = append(keep, c)
		}
	}
	j.mu.Unlock()
	b, err := json.Marshal(keep)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// serve 处理一条浏览器级 CDP 连接。
func (j *cookieJar) serve(ctx context.Context, c *websocket.Conn, def string) {
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(b, &req) != nil {
			return
		}
		reply := map[string]any{"id": req.ID, "result": map[string]any{}}
		switch req.Method {
		case "Storage.getCookies":
			j.mu.Lock()
			list := append([]map[string]any{}, j.cookies...)
			j.mu.Unlock()
			reply["result"] = map[string]any{"cookies": list}
		case "Storage.setCookies":
			var p struct {
				Cookies []map[string]any `json:"cookies"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				reply = map[string]any{"id": req.ID, "error": map[string]any{"code": -32602, "message": err.Error()}}
				break
			}
			j.set(p.Cookies)
		case "Storage.clearCookies":
			j.mu.Lock()
			j.cookies = nil
			j.mu.Unlock()
		case "Browser.close":
			if err := j.flush(filepath.Join(def, "fake-cookies.json")); err != nil {
				fmt.Fprintln(os.Stderr, "fakedesk chromium: flush cookies:", err)
				os.Exit(1)
			}
			_ = os.WriteFile(filepath.Join(def, "exit-clean"), []byte{1}, 0o600)
			out, _ := json.Marshal(reply)
			_ = c.Write(ctx, websocket.MessageText, out)
			os.Exit(0)
		default:
			reply = map[string]any{"id": req.ID, "error": map[string]any{"code": -32601, "message": "'" + req.Method + "' wasn't found"}}
		}
		out, _ := json.Marshal(reply)
		if err := c.Write(ctx, websocket.MessageText, out); err != nil {
			return
		}
	}
}
