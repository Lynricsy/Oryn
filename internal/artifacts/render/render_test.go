package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Lynricsy/Oryn/internal/sandbox"
)

// TestMain 让测试二进制自己充当 `orynd render-exec`（Renderer 的 Exe 指向测试二进制）。
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == ExecCommand {
		os.Exit(ExecMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

func TestParseExecArgs(t *testing.T) {
	want := ExecArgs{UID: 65534, GID: 65534, Port: 4321, Argv: []string{"/usr/bin/chromium", "--headless=new", "--", "x"}}
	args := want.Args()
	if args[0] != ExecCommand {
		t.Fatalf("Args()[0] = %q", args[0])
	}
	got, err := ParseExecArgs(args[1:])
	if err != nil {
		t.Fatal(err)
	}
	if got.UID != want.UID || got.GID != want.GID || got.Port != want.Port || !slices.Equal(got.Argv, want.Argv) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	for name, args := range map[string][]string{
		"uid 0":       {"--uid", "0", "--gid", "0", "--port", "1", "--", "/bin/true"},
		"no uid":      {"--gid", "1", "--port", "1", "--", "/bin/true"},
		"no gid":      {"--uid", "1", "--port", "1", "--", "/bin/true"},
		"port 0":      {"--uid", "1", "--gid", "1", "--port", "0", "--", "/bin/true"},
		"port big":    {"--uid", "1", "--gid", "1", "--port", "70000", "--", "/bin/true"},
		"no dashdash": {"--uid", "1", "--gid", "1", "--port", "1", "/bin/true"},
		"relative":    {"--uid", "1", "--gid", "1", "--port", "1", "--", "chromium"},
		"no argv":     {"--uid", "1", "--gid", "1", "--port", "1", "--"},
		"unknown":     {"--uid", "1", "--gid", "1", "--port", "1", "--evil", "--", "/bin/true"},
	} {
		if _, err := ParseExecArgs(args); err == nil {
			t.Errorf("%s: ParseExecArgs(%q) succeeded", name, args)
		}
	}
}

func TestChromiumArgsFixed(t *testing.T) {
	args := chromiumArgs(chromiumJob{Dir: "/j", Port: 9, URL: "http://render.oryn.invalid/t/", Width: 1280, Height: 800, Scale: 0.5625})
	for _, want := range []string{"--proxy-server=http://127.0.0.1:9", "--proxy-bypass-list=<-loopback>", "--window-size=1280,800",
		"--force-device-scale-factor=0.5625", "--screenshot=/j/out.png", "--user-data-dir=/j/profile"} {
		if !slices.Contains(args, want) {
			t.Errorf("args lack %q: %q", want, args)
		}
	}
	if slices.Contains(args, "--no-sandbox") {
		t.Fatal("--no-sandbox without no_sandbox")
	}
	if args[len(args)-1] != "http://render.oryn.invalid/t/" {
		t.Fatalf("URL is not last: %q", args)
	}
	pdf := chromiumArgs(chromiumJob{Dir: "/j", Port: 9, URL: "u", Width: 1, Height: 1, Scale: 1, PDF: true, NoSandbox: true})
	for _, want := range []string{"--print-to-pdf=/j/out.pdf", "--no-pdf-header-footer", "--no-sandbox"} {
		if !slices.Contains(pdf, want) {
			t.Errorf("pdf args lack %q", want)
		}
	}
}

func TestRootWithoutUserDisabled(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	r, err := New(Config{Dir: t.TempDir(), Exe: "/bin/true", Site: &fakeSite{}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.Preflight(context.Background())
	ok, reason := r.Status()
	if ok || !strings.Contains(reason, "artifacts.render.user") {
		t.Fatalf("Status() = %v, %q", ok, reason)
	}
	if _, err := r.Run(context.Background(), Job{ArtifactID: "a", Width: 10, Height: 10}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Run err = %v, want ErrUnavailable", err)
	}
}

// fakeSite 按路径回固定页面，不发任何 CSP（网络隔离不能依赖页面的 CSP）。
type fakeSite struct {
	pages  map[string]string
	strict bool
}

func (f *fakeSite) RenderTarget(_ context.Context, id string, version int) (int, bool, error) {
	if id == "missing" {
		return 0, false, errors.New("not_found")
	}
	return max(version, 1), f.strict, nil
}

func (f *fakeSite) ServeRender(w http.ResponseWriter, r *http.Request, _ Target, p string) {
	if strings.HasSuffix(p, "/") {
		p += "index.html"
	}
	body, ok := f.pages[p]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(body))
}

// proxyLog 记录代理放行与拒绝的请求。
type proxyLog struct {
	mu      sync.Mutex
	allowed []string
	denied  []string
}

func (l *proxyLog) observe(method, target string, allowed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if allowed {
		l.allowed = append(l.allowed, method+" "+target)
	} else {
		l.denied = append(l.denied, method+" "+target)
	}
}

// chromiumRenderer 在 ORYN_TEST_CHROMIUM=1 时返回一个能用的渲染器（root 下用 nobody 跑 Chromium）。
func chromiumRenderer(t *testing.T, site Site) (*Renderer, *proxyLog) {
	t.Helper()
	if os.Getenv("ORYN_TEST_CHROMIUM") != "1" {
		t.Skip("set ORYN_TEST_CHROMIUM=1 to run Chromium integration tests")
	}
	cfg := Config{Site: site, CDNs: []string{"https://cdn.jsdelivr.net"}}
	if os.Geteuid() == 0 {
		if _, err := user.Lookup("nobody"); err != nil {
			t.Skip("root without a nobody account")
		}
		cfg.User = "nobody"
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Exe = exe
	// 渲染账号要能进入作业目录的上级：/tmp 下自建一个 0711 的目录（t.TempDir 是 0700）
	base, err := os.MkdirTemp("", "oryn-render-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if err := os.Chmod(base, 0o711); err != nil {
		t.Fatal(err)
	}
	cfg.Dir = filepath.Join(base, "jobs")
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	log := &proxyLog{}
	r.proxy.observe = log.observe
	r.Preflight(context.Background())
	if ok, reason := r.Status(); !ok {
		t.Fatalf("preflight failed: %s", reason)
	}
	return r, log
}

func TestChromiumScreenshot(t *testing.T) {
	site := &fakeSite{pages: map[string]string{"/index.html": `<!doctype html><html><body style="margin:0;background:#fff">` +
		`<h1 style="color:#f00;font-size:120px">红色标题</h1></body></html>`}}
	r, _ := chromiumRenderer(t, site)
	out, err := r.Run(context.Background(), Job{ArtifactID: "a", Width: 1280, Height: 800})
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 1280 || b.Dy() != 800 {
		t.Fatalf("size %d×%d, want 1280×800", b.Dx(), b.Dy())
	}
	red := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += 2 {
		for x := b.Min.X; x < b.Max.X; x += 2 {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			if cr>>8 > 0xd0 && cg>>8 < 0x40 && cb>>8 < 0x40 {
				red++
			}
		}
	}
	if red == 0 {
		t.Fatal("no red pixels in the screenshot")
	}
	if _, err := r.Run(context.Background(), Job{ArtifactID: "missing", Width: 64, Height: 64}); err == nil {
		t.Fatal("rendering a missing artifact succeeded")
	}
}

func TestChromiumFetchDenied(t *testing.T) {
	site := &fakeSite{pages: map[string]string{"/index.html": `<!doctype html><html><body><p>x</p>` +
		`<script>fetch("http://127.0.0.1:1/").catch(() => {})</script></body></html>`}}
	r, log := chromiumRenderer(t, site)
	if _, err := r.Run(context.Background(), Job{ArtifactID: "a", Width: 640, Height: 400}); err != nil {
		t.Fatal(err)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.allowed) == 0 {
		t.Fatal("proxy saw no allowed request at all")
	}
	for _, a := range log.allowed {
		if !strings.HasPrefix(a, "GET http://"+Host+"/") {
			t.Errorf("proxy allowed %q", a)
		}
	}
}

// TestChromiumIsolation：页面不带任何 CSP，直接去连本机的 TCP 端口与 STUN（UDP）；这些连接都不能离开 Chromium。
func TestChromiumIsolation(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	var hits sync.Map
	go func() {
		for {
			c, err := tcp.Accept()
			if err != nil {
				return
			}
			hits.Store("tcp", true)
			_ = c.Close()
		}
	}()
	go func() {
		buf := make([]byte, 2048)
		for {
			if _, _, err := udp.ReadFrom(buf); err != nil {
				return
			}
			hits.Store("udp", true)
		}
	}()
	tcpPort := tcp.Addr().(*net.TCPAddr).Port
	udpPort := udp.LocalAddr().(*net.UDPAddr).Port
	page := fmt.Sprintf(`<!doctype html><html><body><p>x</p><img src="http://127.0.0.1:%[1]d/i.png"><script>
fetch("http://127.0.0.1:%[1]d/f").catch(() => {});
try { new WebSocket("ws://127.0.0.1:%[1]d/ws"); } catch (e) {}
try {
  const pc = new RTCPeerConnection({iceServers: [{urls: "stun:127.0.0.1:%[2]d"}]});
  pc.createDataChannel("x");
  pc.createOffer().then(o => pc.setLocalDescription(o));
} catch (e) {}
</script></body></html>`, tcpPort, udpPort)
	r, log := chromiumRenderer(t, &fakeSite{pages: map[string]string{"/index.html": page}})
	if _, err := r.Run(context.Background(), Job{ArtifactID: "a", Width: 640, Height: 400}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	for _, k := range []string{"tcp", "udp"} {
		if _, ok := hits.Load(k); ok {
			t.Errorf("a %s connection left Chromium", k)
		}
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	for _, a := range log.allowed {
		if !strings.HasPrefix(a, "GET http://"+Host+"/") {
			t.Errorf("proxy allowed %q", a)
		}
	}
	t.Logf("denied by proxy: %q", log.denied)
}

// TestExecRestrictsConnect：render-exec 之后的程序只能 connect 代理端口（Landlock ABI ≥ 4 时）。
func TestExecRestrictsConnect(t *testing.T) {
	if abi, _ := sandbox.Probe(); abi < 4 {
		t.Skipf("landlock ABI %d < 4", abi)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		u, err := user.Lookup("nobody")
		if err != nil {
			t.Skip("root without a nobody account")
		}
		fmt.Sscan(u.Uid, &uid)
		fmt.Sscan(u.Gid, &gid)
	}
	allowed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer allowed.Close()
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	go func() {
		for _, l := range []net.Listener{allowed, other} {
			go func() {
				for {
					c, err := l.Accept()
					if err != nil {
						return
					}
					_ = c.Close()
				}
			}()
		}
	}()
	ap, op := allowed.Addr().(*net.TCPAddr).Port, other.Addr().(*net.TCPAddr).Port
	exe, _ := os.Executable()
	script := fmt.Sprintf(`exec 3<>/dev/tcp/127.0.0.1/%d || exit 11; exec 4<>/dev/tcp/127.0.0.1/%d && exit 12; exit 0`, ap, op)
	a := ExecArgs{UID: uid, GID: gid, Port: uint16(ap), Argv: []string{bash, "-c", script}}
	cmd := exec.Command(exe, a.Args()...)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("render-exec: %v\n%s", err, out)
	}
}
