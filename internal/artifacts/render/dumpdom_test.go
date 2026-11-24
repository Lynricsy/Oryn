package render

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestDumpDOMArgs(t *testing.T) {
	args := chromiumArgs(chromiumJob{Dir: "/j", Port: 9, URL: "http://render.oryn.invalid/t/x.html?__oryn_probe=1", Width: 1280, Height: 800, Scale: 1, DumpDOM: true})
	for _, want := range []string{"--dump-dom", "--virtual-time-budget=5000", "--proxy-server=http://127.0.0.1:9"} {
		if !slices.Contains(args, want) {
			t.Errorf("args lack %q: %q", want, args)
		}
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--screenshot") || strings.HasPrefix(a, "--print-to-pdf") {
			t.Errorf("dump-dom job has output flag %q", a)
		}
	}
	if args[len(args)-1] != "http://render.oryn.invalid/t/x.html?__oryn_probe=1" {
		t.Fatalf("URL is not last: %q", args)
	}
}

func TestDumpDOMCap(t *testing.T) {
	c := &capBuffer{limit: 8}
	_, _ = c.Write([]byte("12345"))
	if c.over || string(c.buf) != "12345" {
		t.Fatalf("buf=%q over=%v", c.buf, c.over)
	}
	n, err := c.Write([]byte("67890"))
	if n != 5 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if !c.over || string(c.buf) != "12345678" {
		t.Fatalf("buf=%q over=%v", c.buf, c.over)
	}
	_, _ = c.Write([]byte("x"))
	if string(c.buf) != "12345678" {
		t.Fatalf("buf grew past limit: %q", c.buf)
	}
}

func TestDumpDOMRejectsPDF(t *testing.T) {
	r := &Renderer{}
	if _, err := r.run(context.Background(), Job{ArtifactID: "a", Width: 10, Height: 10, PDF: true, DumpDOM: true}, nil); err == nil {
		t.Fatal("PDF+DumpDOM accepted")
	}
}

// 真 Chromium：页面脚本改过的 DOM 经 --dump-dom 带出（ORYN_TEST_CHROMIUM=1）。virtual time 下没有空闲期，
// 不带 timeout 的 requestIdleCallback 永远不触发（实测），探针同样带 timeout。
func TestChromiumDumpDOM(t *testing.T) {
	site := &fakeSite{pages: map[string]string{"/index.html": `<!doctype html><html><body><p>静态</p>` +
		`<script>requestIdleCallback(() => { const s = document.createElement("script"); s.id = "oryn-probe";` +
		` s.type = "application/json"; s.textContent = JSON.stringify({v: 1, q: location.search}); document.body.append(s); }, {timeout: 1000})</script>` +
		`</body></html>`}}
	r, _ := chromiumRenderer(t, site)
	out, err := r.Run(context.Background(), Job{ArtifactID: "a", Query: "__oryn_probe=1", Width: 640, Height: 400, DumpDOM: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `<script id="oryn-probe" type="application/json">{"v":1,"q":"?__oryn_probe=1"}</script>`) {
		t.Fatalf("DOM lacks probe node:\n%s", out)
	}
}
