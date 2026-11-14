package coordhost

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/sandbox"
)

// SelfTest 是 `orynd --config <f> coordinator check`：可用性检查与目录准备之后，用与 spawn 相同的参数与
// 命名空间起 `coordinator-host --selftest`（HOME 为一次性的 <dir>/selftest-<随机>，结束后删除），
// 把探测结论、沙箱报告与自检各行（JSON）写到 out。全部通过返回 nil。
func SelfTest(ctx context.Context, cfg Config, out io.Writer) error {
	return selfTest(ctx, New(cfg, nil, nil, nil), out)
}

func selfTest(ctx context.Context, h *Host, out io.Writer) error {
	enc := json.NewEncoder(out)
	h.probe()
	probe := map[string]any{"netNamespace": h.useNS, "landlockAbi": h.abi}
	if h.probeErr != nil {
		probe["error"] = h.probeErr.Error()
	}
	_ = enc.Encode(map[string]any{"probe": probe})
	a, err := h.prepare(ctx)
	if err != nil {
		_ = enc.Encode(map[string]any{"available": false, "reason": err.Error()})
		return err
	}
	avail := map[string]any{"available": true}
	if a.uid >= 0 {
		avail["account"], avail["uid"] = a.name, a.uid
	}
	_ = enc.Encode(avail)

	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	home := filepath.Join(h.cfg.Dir, "selftest-"+hex.EncodeToString(rnd[:]))
	if err := prepareHome(home, a.uid, a.gid); err != nil {
		return err
	}
	defer os.RemoveAll(home)

	paths := []string{"/etc/shadow"}
	if h.cfg.DataDir != "" {
		paths = append(paths, filepath.Join(h.cfg.DataDir, "master.key"), filepath.Join(h.cfg.DataDir, "oryn.db"))
	}
	if h.cfg.ConfigPath != "" {
		paths = append(paths, h.cfg.ConfigPath)
	}
	var deny []string
	for _, p := range paths {
		// 子进程的 cwd 是 /：相对路径必须先转成绝对路径，否则「读不到」只是因为找不到。
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		deny = append(deny, "--deny-read", p)
	}
	// 宿主上的 abstract unix socket（stream 与 datagram 各一个）：沙箱里必须连不上。
	abstract := "oryn-selftest-" + hex.EncodeToString(rnd[:])
	sl, err := net.Listen("unix", "@"+abstract)
	if err != nil {
		return fmt.Errorf("listen abstract unix socket: %w", err)
	}
	defer sl.Close()
	dl, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: "@" + abstract + "-dgram", Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("listen abstract unix datagram socket: %w", err)
	}
	defer dl.Close()
	deny = append(deny, "--deny-abstract", abstract, "--deny-abstract", abstract+"-dgram")
	// 宿主文件系统上、沙箱白名单之外的 0666 unix datagram socket（像 /dev/log）：沙箱里 connect / sendto 都必须失败，
	// 且它什么也收不到。网络命名空间与 IPC 作用域都管不到路径型 socket，靠 seccomp 一律禁止 AF_UNIX 挡住。
	sockDir, err := os.MkdirTemp("", "oryn-selftest-sock-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(sockDir)
	if err := os.Chmod(sockDir, 0o755); err != nil {
		return err
	}
	dgramPath := filepath.Join(sockDir, "dgram.sock")
	pl, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: dgramPath, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("listen unix datagram socket: %w", err)
	}
	defer pl.Close()
	if err := os.Chmod(dgramPath, 0o666); err != nil {
		return err
	}
	deny = append(deny, "--deny-unix-dgram", dgramPath)
	// 宿主回环上真实在监听的白名单外 TCP 端口（开了 TFO）：沙箱里必须连不上（网络命名空间或 Landlock 的端口规则），
	// 且它接不到连接、也收不到随 SYN 带来的数据。
	tl, err := sandbox.ListenHost()
	if err != nil {
		return fmt.Errorf("listen tcp: %w", err)
	}
	defer tl.Close()
	deny = append(deny, "--host-listener", tl.Addr().String())
	args := h.childArgs(a, h.useNS, append([]string{"--selftest", "--home", home}, deny...)...)
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	defer pr.Close()
	cmd := exec.CommandContext(ctx, h.cfg.Command[0], args...)
	cmd.Env = childEnv()
	cmd.Dir = "/"
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.ExtraFiles = []*os.File{nil, pw} // fd 3（link）不提供；fd 4 = 状态管道
	cmd.SysProcAttr = sysProcAttr(h.useNS)
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return fmt.Errorf("start selftest: %w", err)
	}
	_ = pw.Close()
	report, _ := bufio.NewReader(pr).ReadBytes('\n')
	runErr := cmd.Wait()
	if r := bytes.TrimSpace(report); len(r) > 0 {
		_ = enc.Encode(map[string]json.RawMessage{"sandbox": r})
	}
	_, _ = out.Write(stdout.Bytes())
	if runErr != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			_ = enc.Encode(map[string]string{"stderr": s})
		}
		return fmt.Errorf("coordinator host selftest failed: %w", runErr)
	}
	// 子进程已退出：它若发进来过，数据报就在接收队列里。
	buf := make([]byte, 256)
	for _, l := range []*net.UnixConn{pl, dl} {
		_ = l.SetReadDeadline(time.Now())
		if n, _, err := l.ReadFrom(buf); err == nil {
			name := l.LocalAddr().String()
			_ = enc.Encode(map[string]any{"check": "unix dgram " + name + " received nothing", "ok": false, "detail": fmt.Sprintf("received %q", buf[:n])})
			return fmt.Errorf("coordinator host selftest failed: sandbox reached host unix datagram socket %s", name)
		}
	}
	if err := sandbox.HostReceived(tl); err != nil {
		_ = enc.Encode(map[string]any{"check": "tcp " + tl.Addr().String() + " received nothing", "ok": false, "detail": err.Error()})
		return fmt.Errorf("coordinator host selftest failed: sandbox reached host tcp port %s", tl.Addr())
	}
	return nil
}
