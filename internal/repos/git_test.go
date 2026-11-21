package repos

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

func TestTokenOnlyInEnv(t *testing.T) {
	const tok = "gho_secret_token_123"
	e := newTestEnv(t, nil, nil, fakeGitHub{tok: tok})
	id := e.project(t, widgetsURL)
	if st := e.settle(t, id); st.State != StateReady {
		t.Fatalf("Status = %+v", st)
	}
	calls := e.trace.forDir(id)
	if len(calls) == 0 {
		t.Fatal("没有记到 git 调用")
	}
	wantHeader := "GIT_CONFIG_VALUE_0=Authorization: Basic " + basicAuth(tok)
	sawHeader := false
	for _, c := range calls {
		argv, env := c[0], c[1]
		if strings.Contains(strings.Join(argv, " "), tok) || strings.Contains(strings.Join(argv, " "), basicAuth(tok)) {
			t.Fatalf("argv 里有令牌：%q", argv)
		}
		if slices.Contains(env, wantHeader) {
			sawHeader = true
		}
	}
	if !sawHeader {
		t.Fatal("拉取的环境里没有 Authorization 头")
	}
	refs := e.fake.CallsTo("GET", "/acme/widgets.git/info/refs")
	if len(refs) == 0 {
		t.Fatal("假 GitHub 没收到 info/refs")
	}
	for _, c := range refs {
		raw, ok := strings.CutPrefix(c.Auth, "Basic ")
		if !ok {
			t.Fatalf("Auth = %q，想要 Basic", c.Auth)
		}
		dec, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || string(dec) != "x-access-token:"+tok {
			t.Fatalf("Auth 解码 = %q (%v)", dec, err)
		}
	}
}

func TestRedactSecrets(t *testing.T) {
	got := redactSecrets("fatal: tok dG9r "+basicAuth("tok"), "tok")
	if strings.Contains(got, "tok") || strings.Contains(got, "dG9r") || strings.Contains(got, basicAuth("tok")) {
		t.Fatalf("redactSecrets 没有脱干净：%q", got)
	}
	if got := redactSecrets("nothing", ""); got != "nothing" {
		t.Fatalf("空值改动了文本：%q", got)
	}
}

func TestGuard(t *testing.T) {
	e := newTestEnv(t, nil, fakeSecrets{"localhost": "tok"}, nil)
	blocked := e.project(t, "https://127.0.0.1/acme/x")
	st := e.settle(t, blocked)
	if st.State != StateError || st.LastError == nil || st.LastError.Code != "blocked_address" {
		t.Fatalf("未担保的内网地址 Status = %+v，想要 blocked_address", st)
	}
	// 有令牌的主机是担保主机：绕过守卫直连，失败原因是连不上而不是被拦截
	vouched := e.project(t, "https://localhost/acme/x")
	st = e.settle(t, vouched)
	if st.LastError == nil || st.LastError.Code != "unreachable" {
		t.Fatalf("担保主机 Status = %+v，想要 unreachable", st)
	}
	if strings.Contains(st.LastError.Message, "tok") {
		t.Fatalf("错误信息里有令牌：%q", st.LastError.Message)
	}
}

func TestProxyEnv(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.ProxyEnv = []string{"HTTPS_PROXY=http://p:1"} }, fakeSecrets{"localhost": "tok"}, nil)
	vouched := e.project(t, "https://localhost/acme/x")
	open := e.project(t, "https://127.0.0.1/acme/y")
	e.settle(t, vouched)
	e.settle(t, open)

	hasProxy := func(id string) (exact, any bool) {
		for _, c := range e.trace.forDir(id) {
			for _, kv := range c[1] {
				k, _, _ := strings.Cut(kv, "=")
				if strings.HasSuffix(strings.ToUpper(k), "_PROXY") {
					any = true
				}
				exact = exact || kv == "HTTPS_PROXY=http://p:1"
			}
		}
		return exact, any
	}
	if exact, _ := hasProxy(vouched); !exact {
		t.Fatal("担保主机的环境里没有 HTTPS_PROXY")
	}
	if _, anyProxy := hasProxy(open); anyProxy {
		t.Fatal("未担保主机的环境里有 *_PROXY 变量")
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		stderr string
		token  bool
		want   string
	}{
		{"fatal: unable to access 'x': CONNECT tunnel failed, response 403", false, "blocked_address"},
		{"fatal: unable to access 'x': Received HTTP code 403 from proxy after CONNECT", false, "blocked_address"},
		{"fatal: could not read Username for 'https://h': terminal prompts disabled", false, "auth_required"},
		{"fatal: Authentication failed for 'https://h/'", true, "auth_failed"},
		{"remote: Repository not found.", false, "not_found"},
		{"fatal: unable to access 'x': Could not resolve host: h", false, "unreachable"},
		{"fatal: something else", false, "fetch_failed"},
	} {
		if got := classify(c.stderr, c.token); got != c.want {
			t.Errorf("classify(%q, %v) = %q，想要 %q", c.stderr, c.token, got, c.want)
		}
	}
}
