package designengine

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 导出工程的 server.mjs：坏请求目标回 400、不让进程崩掉，之后的正常请求照常服务；越出 dist 的路径不泄露文件。
// 用真的 node 跑嵌入的模板；没有 node 时跳过。
func TestEjectServer(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("没有 node")
	}
	dir := t.TempDir()
	var server []byte
	for _, f := range ProjectFiles(Project{Title: "t", Frames: []ProjectFrame{{ID: "a", Name: "a"}}}) {
		if f.Path == "server.mjs" {
			server = f.Data
		}
	}
	if server == nil {
		t.Fatal("模板里没有 server.mjs")
	}
	write := func(rel, s string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("server.mjs", string(server))
	write("dist/index.html", "<!doctype html>INDEX_OK")
	write("dist/assets/app.js", "console.log(1)")
	write("secret.txt", "SECRET")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	cmd := exec.Command(node, "server.mjs")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PORT="+strconv.Itoa(port))
	var logs strings.Builder
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})

	addr := "127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server.mjs 没有起来：%v\n%s", err, logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	// raw 发一行请求（net/http 客户端会拒绝坏 URL），返回状态码与正文。
	raw := func(target string) (int, string) {
		t.Helper()
		c, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatalf("连接失败（进程可能已崩）：%v\n%s", err, logs.String())
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", target)
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatalf("读响应 %q：%v\n%s", target, err, logs.String())
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	code, _ := raw("http://[::")
	if code != http.StatusBadRequest {
		t.Fatalf("坏请求目标 = %d，want 400", code)
	}
	for range 3 {
		if code, body := raw("/"); code != 200 || !strings.Contains(body, "INDEX_OK") {
			t.Fatalf("坏请求之后 / = %d %q\n%s", code, body, logs.String())
		}
	}
	if code, body := raw("/assets/app.js"); code != 200 || body != "console.log(1)" {
		t.Fatalf("/assets/app.js = %d %q", code, body)
	}
	if _, body := raw("/../secret.txt"); strings.Contains(body, "SECRET") {
		t.Fatal("越出 dist 的路径读到了文件")
	}
	if _, body := raw("/%2e%2e/secret.txt"); strings.Contains(body, "SECRET") {
		t.Fatal("编码的 .. 越出了 dist")
	}
	select {
	case err := <-exited:
		t.Fatalf("server.mjs 退出了：%v\n%s", err, logs.String())
	default:
	}
}
