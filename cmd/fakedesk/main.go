// fakedesk 是 orbd 桌面栈（M8）的测试替身：按 argv[0] 的文件名扮演三个程序。
//
//   - Xtigervnc：解析 -rfbunixpath，在该 unix socket 上做字节回显；收到 SIGTERM 退出。
//   - openbox：挂起直到被杀（SIGTERM 正常退出）。
//   - chromium：解析 --user-data-dir；在临时端口上应答 `/json/version`，其它路径回显方法、路径与请求体；
//     写 `DevToolsActivePort`（先写临时文件再 rename）；在 profile 里建 `Default/Cookies`；
//     每收到一次 SIGCONT 往 `Default/sigcont` 追加一个字节；收到 SIGTERM 时往 `Default/exit-clean` 写一个字节再退出。
//
// 用法：构建后以 Xtigervnc / openbox / chromium 三个名字的符号链接放进 PATH。
package main

import (
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
	"syscall"
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
			_ = os.WriteFile(filepath.Join(def, "exit-clean"), []byte{1}, 0o600)
			return nil
		}
	}
	return nil
}
