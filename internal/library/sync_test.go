package library

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbdrt"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

const hostOrb = "orb-coord"

// testHost 扮演控制面的 orbs.Service：一条经 net.Pipe 的 link，对端是真实的协调宿主运行时（orbdrt，协调宿主模式）。
type testHost struct {
	sess *link.Session
	ctl  *link.Ctl

	mu      sync.Mutex
	feats   []string
	streams map[string]orbs.StreamFunc
}

func (h *testHost) Ctl(string) (*link.Ctl, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ctl, h.ctl != nil
}

func (h *testHost) HasFeature(_, feature string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Contains(h.feats, feature)
}

func (h *testHost) OpenStream(ctx context.Context, _, _ string, hd link.StreamHeader) (*link.Stream, error) {
	h.mu.Lock()
	sess := h.sess
	h.mu.Unlock()
	return sess.Open(ctx, hd)
}

func (h *testHost) HandleStream(t string, fn orbs.StreamFunc) {
	h.mu.Lock()
	h.streams[t] = fn
	h.mu.Unlock()
}

// startHost 起一个协调宿主运行时并等它 hello；返回假控制面一侧与宿主的 $HOME。
func startHost(t *testing.T) (*testHost, string) {
	t.Helper()
	home := t.TempDir()
	h := &testHost{streams: map[string]orbs.StreamFunc{}}
	a, b := net.Pipe()
	ready := make(chan struct{})
	hello := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		sess, err := link.NewConnSession(b, false)
		if err != nil {
			return
		}
		st, err := sess.AcceptStream(ctx)
		if err != nil {
			return
		}
		ctl := link.NewCtl(st, link.CtlHandlers{OnNotify: func(_ context.Context, method string, params json.RawMessage) {
			if method != string(apigen.MethodHello) {
				return
			}
			var p apigen.LinkCtlHelloParams
			_ = json.Unmarshal(params, &p)
			h.mu.Lock()
			h.feats = p.Features
			h.mu.Unlock()
			close(hello)
		}})
		h.mu.Lock()
		h.sess, h.ctl = sess, ctl
		h.mu.Unlock()
		go func() {
			select {
			case <-hello:
				close(ready)
			case <-ctx.Done():
			}
		}()
		for {
			st, err := sess.AcceptStream(ctx)
			if err != nil {
				return
			}
			h.mu.Lock()
			fn := h.streams[st.Header.T]
			h.mu.Unlock()
			if fn == nil {
				_ = st.Close()
				continue
			}
			go func() {
				defer st.Close()
				fn(ctx, hostOrb, st.Header, st)
			}()
		}
	}()
	dialed := false
	rt, err := orbdrt.New(orbdrt.Config{
		Coordinator: true, OrbID: hostOrb, Home: home, AgentBin: "/bin/false", EgressAddr: "127.0.0.1:0", Version: "test",
		Connect: func(context.Context) (*link.Session, error) {
			if dialed {
				return nil, orbdrt.ErrNoRedial
			}
			dialed = true
			return link.NewConnSession(a, true)
		},
	})
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = rt.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		h.mu.Lock()
		sess := h.sess
		h.mu.Unlock()
		if sess != nil {
			_ = sess.Close()
		}
		_ = a.Close()
		<-done
	})
	select {
	case <-ready:
	case <-time.After(20 * time.Second):
		t.Fatal("coordinator host did not say hello")
	}
	return h, home
}

// hostFiles 列出宿主 ~/library/{shared,threads} 下的全部文件（相对 $HOME，`~/` 形式）→ 内容。
func hostFiles(t *testing.T, home string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, top := range []string{"shared", "threads"} {
		err := filepath.WalkDir(filepath.Join(home, "library", top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(home, p)
			b, rerr := os.ReadFile(p)
			require.NoError(t, rerr)
			out["~/"+filepath.ToSlash(rel)] = string(b)
			return nil
		})
		require.NoError(t, err)
	}
	return out
}

// M13 4.3：同步之后协调宿主 ~/library 与清单（共享文件 + 属于某个对话的文件）一致；删掉库文件、改 scope 之后再同步，
// 宿主上随之清理 / 换位置。
func TestSyncIntoCoordinatorHost(t *testing.T) {
	ctx := context.Background()
	s, st, _, _ := newSvc(t)
	mkThread(t, st, "T-1")
	mkThread(t, st, "T-2")
	host, home := startHost(t)
	y := NewSyncer(s, host)

	conv, err := s.Put(ctx, PutParams{Name: "notes.md", ThreadID: "T-1", Source: SourceAssistant, R: strings.NewReader("# notes\n")})
	require.NoError(t, err)
	shared, err := s.Put(ctx, PutParams{Name: "data.csv", Scope: ScopeShared, Source: SourceOrb, SourceThreadID: "T-2", R: strings.NewReader("a,b\n1,2\n")})
	require.NoError(t, err)
	pending, err := s.Upload(ctx, "draft.txt", "T-2", strings.NewReader("draft"))
	require.NoError(t, err)
	_, err = s.Upload(ctx, "orphan.txt", "", strings.NewReader("no conversation yet"))
	require.NoError(t, err)

	require.NoError(t, y.Sync(ctx, hostOrb))
	assert.Equal(t, map[string]string{
		SandboxPath(conv):    "# notes\n",
		SandboxPath(shared):  "a,b\n1,2\n",
		SandboxPath(pending): "draft",
	}, hostFiles(t, home), "与清单一致；还不属于任何对话的上传不同步")
	require.NoError(t, y.Sync(ctx, hostOrb), "再次同步：没有缺失")

	require.NoError(t, s.Delete(ctx, conv.ID))
	require.NoError(t, s.SetScope(ctx, pending.ID, ScopeShared))
	pending.Scope = ScopeShared
	require.NoError(t, y.Sync(ctx, hostOrb))
	assert.Equal(t, map[string]string{
		SandboxPath(shared):  "a,b\n1,2\n",
		SandboxPath(pending): "draft",
	}, hostFiles(t, home), "删除的文件被清理，改为共享的文件换到 shared/")
}

// M13 4.3 / 4.4：取回只服务 ~/library/out/；PutFrom 把取回的字节存进库，摘要一致。
func TestFetchFromCoordinatorHost(t *testing.T) {
	ctx := context.Background()
	s, st, _, _ := newSvc(t)
	mkThread(t, st, "T-1")
	host, home := startHost(t)
	y := NewSyncer(s, host)
	shared, err := s.Put(ctx, PutParams{Name: "data.csv", Scope: ScopeShared, Source: SourceOrb, R: strings.NewReader("secret")})
	require.NoError(t, err)
	require.NoError(t, y.Sync(ctx, hostOrb))

	for _, p := range []string{"../shared/" + shared.ID + "/data.csv", "~/library/out/../shared/" + shared.ID + "/data.csv", SandboxPath(shared)} {
		_, _, err := y.Fetch(ctx, hostOrb, p)
		var ce *link.CtlError
		require.ErrorAs(t, err, &ce, p)
		assert.Equal(t, link.CodeInvalidParams, ce.Code, p)
	}

	out := filepath.Join(home, "library", "out", "report.md")
	content := "# Q3 report\n"
	require.NoError(t, os.WriteFile(out, []byte(content), 0o644))
	f, err := y.PutFrom(ctx, hostOrb, "~/library/out/report.md", PutParams{Name: "report.md", ThreadID: "T-1", Source: SourceAssistant})
	require.NoError(t, err)
	assert.Equal(t, SourceAssistant, f.Source)
	assert.Equal(t, "T-1", f.ThreadID)
	assert.Equal(t, sum([]byte(content)), f.Sha256)
	assert.Equal(t, content, string(readAll(t, s, f.ID)))
}

// 宿主没有 library 特性（旧版 orbd）时同步什么也不做。
func TestSyncSkipsHostWithoutFeature(t *testing.T) {
	s, _, _, _ := newSvc(t)
	h := &testHost{streams: map[string]orbs.StreamFunc{}, feats: []string{"coordinator", "omphome-apply"}}
	y := NewSyncer(s, h)
	assert.NoError(t, y.Sync(context.Background(), hostOrb))
	assert.NotNil(t, h.streams[link.StreamLibrary], "注册了 library 流的处理器")
}
