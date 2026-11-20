package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// orb 内 SSH 网关（M18）：orbd 在本机回环端口上接受 ssh 连接，每条连接对应一条 `ssh` 流，由控制面的网关代为登录目标机。
// orb 里只有回环别名（<名>@127.0.0.1:<端口>）与网关的主机公钥，没有任何私钥或密码。

const (
	sshIncludeLine  = "Include ~/.oryn/ssh/config"
	sshKnownComment = "oryn-ssh-gateway"
)

var (
	sshHostNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)
	sshHostKeyRe  = regexp.MustCompile(`^[a-z0-9@.-]+ [A-Za-z0-9+/=]+$`)
)

// listenSSH 监听 SSH 网关端口。失败不影响 orbd 启动：这台 orb 里 SSH 主机不可用，hello 不带 ssh。
func (r *Runtime) listenSSH() net.Listener {
	if r.cfg.SSHListener != nil {
		return r.cfg.SSHListener
	}
	ln, err := net.Listen("tcp", r.cfg.SSHAddr)
	if err != nil {
		r.log.Warn("ssh gateway listen failed; SSH hosts are unavailable in this orb", "addr", r.cfg.SSHAddr, "err", err)
		return nil
	}
	return ln
}

// serveSSH 在 ln 上接受 orb 内的 ssh 连接，每条转成一条 `ssh` 流。
func (r *Runtime) serveSSH(ctx context.Context, ln net.Listener) {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			r.log.Warn("ssh accept failed", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go r.pipeToLink(ctx, nc, link.StreamHeader{T: link.StreamSSH}, "ssh")
	}
}

// sshPort 返回 SSH 网关实际监听的端口；没有监听时为 0。
func (r *Runtime) sshPort() int {
	if r.sshLn == nil {
		return 0
	}
	if a, ok := r.sshLn.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// sshJSONHost 是 Core 的 <agent 目录>/ssh.json 里的一台主机。
type sshJSONHost struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Description string `json:"description"`
	Compat      *bool  `json:"compat,omitempty"`
}

// writeSSH 按下发的主机重写 SSH 配置：Core 的 ssh.json、~/.oryn/ssh/config 与网关的 known_hosts 条目。orb 上另在
// ~/.ssh/config 最前面 Include 前者、把网关条目写进 ~/.ssh/known_hosts；runner 不碰用户的 ~/.ssh（M21）：known_hosts
// 写到 <配置根>/ssh/known_hosts（Core 在隔离模式下以它为 UserKnownHostsFile），bash 里用 `ssh -F ~/.oryn/ssh/config <名>`。
// 调用方经 writeOmpConfigAt 串行化。
func (r *Runtime) writeSSH(cfg *apigen.LinkCtlSSHConfig) error {
	port := r.sshPort()
	if port == 0 {
		return nil
	}
	if !sshHostKeyRe.MatchString(cfg.HostKey) {
		return errors.New("invalid gateway host key")
	}
	hosts := map[string]sshJSONHost{}
	var names []string
	for _, h := range cfg.Hosts {
		if !sshHostNameRe.MatchString(h.Name) {
			r.log.Warn("ssh: skipping invalid host name", "name", h.Name)
			continue
		}
		hosts[h.Name] = sshJSONHost{
			Host: "127.0.0.1", Port: port, Username: h.Name, Description: oneLine(h.Description), Compat: h.Compat,
		}
		names = append(names, h.Name)
	}
	slices.Sort(names)

	// 1. Core 的主机目录；描述变了或已删除的主机，顺手清掉 Core 的远端系统探测缓存
	jsonPath := filepath.Join(r.ompAgentDir(), "ssh.json")
	r.dropStaleHostInfo(jsonPath, hosts)
	b, err := json.MarshalIndent(map[string]any{"hosts": hosts}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(jsonPath, append(b, '\n'), 0o600); err != nil {
		return err
	}

	// 2. ~/.oryn/ssh/config：给 bash 里的 `ssh <名>` 用（orb 经 ~/.ssh/config 的 Include，runner 经 ssh -F）
	knownHosts := filepath.Join(r.ompConfigRoot(), "ssh", "known_hosts")
	var cf strings.Builder
	cf.WriteString("# 由 Oryn 生成，每次下发都会重写\n")
	for _, n := range names {
		fmt.Fprintf(&cf, "\nHost %s\n  HostName 127.0.0.1\n  Port %d\n  User %s\n", n, port, n)
		if r.runner != nil {
			fmt.Fprintf(&cf, "  UserKnownHostsFile %s\n", knownHosts)
		}
	}
	orynSSH := filepath.Join(r.home, ".oryn", "ssh")
	if err := os.MkdirAll(orynSSH, 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(orynSSH, "config"), []byte(cf.String()), 0o600); err != nil {
		return err
	}

	if r.runner != nil {
		// runner：网关公钥只进 Oryn 自己的 known_hosts。
		if err := os.MkdirAll(filepath.Dir(knownHosts), 0o700); err != nil {
			return err
		}
		return writeGatewayKnownHost(knownHosts, port, cfg.HostKey)
	}

	// 3. ~/.ssh/config 最前面 Include 上面的文件（已有就不动）
	dotSSH := filepath.Join(r.home, ".ssh")
	if err := os.MkdirAll(dotSSH, 0o700); err != nil {
		return err
	}
	cfgPath := filepath.Join(dotSSH, "config")
	old, err := os.ReadFile(cfgPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := writeFileAtomic(cfgPath, []byte(sshIncludeLine+"\n"), 0o600); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		has := false
		for _, l := range strings.Split(string(old), "\n") {
			if strings.TrimSpace(l) == sshIncludeLine {
				has = true
				break
			}
		}
		if !has {
			mode := os.FileMode(0o600)
			if st, err := os.Stat(cfgPath); err == nil {
				mode = st.Mode().Perm()
			}
			if err := writeFileAtomic(cfgPath, append([]byte(sshIncludeLine+"\n"), old...), mode); err != nil {
				return err
			}
		}
	}

	// 4. known_hosts
	return writeGatewayKnownHost(filepath.Join(dotSSH, "known_hosts"), port, cfg.HostKey)
}

// writeGatewayKnownHost 重写 known_hosts 文件 khPath 里的网关条目：去掉旧的（含端口变了的），追加当前的。
func writeGatewayKnownHost(khPath string, port int, hostKey string) error {
	prefix := fmt.Sprintf("[127.0.0.1]:%d", port)
	var kept []string
	if data, err := os.ReadFile(khPath); err == nil {
		for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			f := strings.Fields(l)
			if l == "" || (len(f) > 0 && f[0] == prefix) || strings.HasSuffix(strings.TrimSpace(l), " "+sshKnownComment) {
				continue
			}
			kept = append(kept, l)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	kept = append(kept, prefix+" "+hostKey+" "+sshKnownComment)
	return writeFileAtomic(khPath, []byte(strings.Join(kept, "\n")+"\n"), 0o600)
}

// dropStaleHostInfo 对旧 ssh.json 里描述变了或已不在的主机，尽力删掉 Core 的远端系统探测缓存
// （<配置根>/remote-host/<名>.json）：同名可能已指向另一台机器。
func (r *Runtime) dropStaleHostInfo(jsonPath string, now map[string]sshJSONHost) {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return
	}
	var old struct {
		Hosts map[string]sshJSONHost `json:"hosts"`
	}
	if json.Unmarshal(data, &old) != nil {
		return
	}
	for name, h := range old.Hosts {
		if cur, ok := now[name]; ok && cur.Description == h.Description {
			continue
		}
		if !sshHostNameRe.MatchString(name) {
			continue
		}
		_ = os.Remove(filepath.Join(r.ompConfigRoot(), "remote-host", name+".json"))
	}
}

// oneLine 把描述压成一行（ssh.json 是 JSON，换行本身无害，但 Core 的列表按行展示）。
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
