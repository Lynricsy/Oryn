package orbdrt

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// orb 内 SSH 网关（M18）：orbd 在本机回环端口上接受 ssh 连接，每条连接对应一条 `ssh` 流，由控制面的网关代为登录目标机。
// orb 里只有回环别名（<名>@127.0.0.1:<端口>）与网关的主机公钥，没有任何上游私钥或密码；runner 上另有每个线程一把、
// 只用来向网关表明身份的密钥（M21）。

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
// 写到 <配置根>/ssh/known_hosts，SSH 配置见 rewriteRunnerSSHConfig，bash 里用 `ssh -F ~/.oryn/ssh/config <名>`。
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
	names := []string{}
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

	if r.runner != nil {
		// runner：网关公钥只进 Oryn 自己的 known_hosts。
		knownHosts := r.runnerKnownHosts()
		if err := os.MkdirAll(filepath.Dir(knownHosts), 0o700); err != nil {
			return err
		}
		if err := writeGatewayKnownHost(knownHosts, port, cfg.HostKey); err != nil {
			return err
		}
		r.sshGlobal = names
		return r.rewriteRunnerSSHConfig()
	}

	// 2. ~/.oryn/ssh/config：给 bash 里的 `ssh <名>` 用（经 ~/.ssh/config 的 Include）
	var cf strings.Builder
	cf.WriteString("# 由 Oryn 生成，每次下发都会重写\n")
	for _, n := range names {
		fmt.Fprintf(&cf, "\nHost %s\n  HostName 127.0.0.1\n  Port %d\n  User %s\n", n, port, n)
	}
	orynSSH := filepath.Join(r.home, ".oryn", "ssh")
	if err := os.MkdirAll(orynSSH, 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(orynSSH, "config"), []byte(cf.String()), 0o600); err != nil {
		return err
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

// runner 上每个线程一把 SSH 密钥（M21）：网关按公钥认出线程，再按线程的项目放行主机。
const (
	// envSSHKey 是 agent 环境里线程私钥的路径（SSH 配置的 IdentityFile 引用它）。
	envSSHKey = "ORYN_SSH_KEY"
	// envSSHPort 是 agent 环境里本机网关端口（项目层 ssh.json 的端口引用它，见 sshgw.RunnerSSHPortRef）。
	envSSHPort = "ORYN_SSH_PORT"
)

// runnerKnownHosts 是 runner 上网关主机公钥所在的 known_hosts（<配置根>/ssh/known_hosts，Core 隔离模式的 UserKnownHostsFile）。
func (r *Runtime) runnerKnownHosts() string {
	return filepath.Join(r.ompConfigRoot(), "ssh", "known_hosts")
}

// threadSSHKeyPath 是线程私钥的路径 `$HOME/.oryn/threads/<线程>/ssh/id_ed25519`（与线程默认会话目录同根）。
func (r *Runtime) threadSSHKeyPath(threadID string) string {
	return filepath.Join(r.home, ".oryn", "threads", threadID, "ssh", "id_ed25519")
}

// rewriteRunnerSSHConfig 重写 runner 的 SSH 配置（调用方持 ompMu）：Core 隔离模式 `-F` 用的 <配置根>/ssh/config 与 bash
// 用的 ~/.oryn/ssh/config 写同一份内容——全局主机与各项目层 ssh.json 里的主机（多个项目合并无妨：网关按线程把关）各一个
// Host 块，外加 `Host 127.0.0.1`（Core 以 ssh.json 的显式 host / port / user 连接时按它匹配）；都用线程自己的密钥
// `${ORYN_SSH_KEY}`（OpenSSH ≥ 8.4 展开）。Core 只在该文件不存在时建空文件，orbd 是唯一写入者。全局主机尚未下发或网关
// 端口没有监听时不写。
func (r *Runtime) rewriteRunnerSSHConfig() error {
	port := r.sshPort()
	if port == 0 || r.sshGlobal == nil {
		return nil
	}
	names := slices.Clone(r.sshGlobal)
	for _, n := range r.runnerProjectSSHNames() {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	auth := fmt.Sprintf("  IdentityFile ${%s}\n  IdentitiesOnly yes\n  UserKnownHostsFile \"%s\"\n", envSSHKey, r.runnerKnownHosts())
	var cf strings.Builder
	cf.WriteString("# 由 Oryn 生成，每次下发都会重写\n")
	for _, n := range names {
		fmt.Fprintf(&cf, "\nHost %s\n  HostName 127.0.0.1\n  Port %d\n  User %s\n%s", n, port, n, auth)
	}
	fmt.Fprintf(&cf, "\nHost 127.0.0.1\n%s", auth)
	for _, dir := range []string{filepath.Join(r.ompConfigRoot(), "ssh"), filepath.Join(r.home, ".oryn", "ssh")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := writeFileAtomic(filepath.Join(dir, "config"), []byte(cf.String()), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// runnerProjectSSHNames 返回 runner 上各项目层版本目录（runnerproject.go）里 `.omp/ssh.json` 的主机名；读不了的跳过。
func (r *Runtime) runnerProjectSSHNames() []string {
	entries, err := os.ReadDir(r.projectConfigRoot())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		i := strings.LastIndexByte(e.Name(), '-')
		if i <= 0 || !e.IsDir() || !runnerProjectIDRe.MatchString(e.Name()[:i]) || !runnerProjectVersion(e.Name(), e.Name()[:i]) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(r.projectConfigRoot(), e.Name(), projectOmpDir, "ssh.json"))
		if err != nil {
			continue
		}
		var f struct {
			Hosts map[string]json.RawMessage `json:"hosts"`
		}
		if json.Unmarshal(b, &f) != nil {
			continue
		}
		for n := range f.Hosts {
			if sshHostNameRe.MatchString(n) {
				out = append(out, n)
			}
		}
	}
	return out
}

// refreshRunnerSSHConfig 在 agent.start 落好项目层之后重写 runner 的 SSH 配置（新项目的主机名进 bash 的配置）。
func (r *Runtime) refreshRunnerSSHConfig() {
	r.ompMu.Lock()
	defer r.ompMu.Unlock()
	if err := r.rewriteRunnerSSHConfig(); err != nil {
		r.log.Warn("rewrite runner ssh config failed", "err", err)
	}
}

// writeThreadSSHKey 生成一把 ed25519 密钥：私钥（OpenSSH 格式）原子写到 path（0600，所在目录 0700），返回
// authorized_keys 单行公钥（不带换行）。
func writeThreadSSHKey(path string) (string, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
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
