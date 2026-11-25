package threads

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 客户端收到含 blob 引用的条目后立即请求：库中还没有、但拉取在途时 Blob 等到入库再返回。
func TestBlobWaitsForInflightFetch(t *testing.T) {
	f := newFixture(t, Config{})
	data := []byte("\x89PNG\r\n\x1a\nfake image bytes")
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	_, _, err := f.s.Blob(f.ctx, sha)
	require.Error(t, err, "没有在途拉取：立即 not_found")

	fetch := &blobFetch{orbID: "orb-x", done: make(chan struct{})}
	f.s.mu.Lock()
	f.s.blobs[sha] = fetch
	f.s.mu.Unlock()
	go func() {
		time.Sleep(300 * time.Millisecond)
		assert.NoError(t, f.s.storeBlob(f.ctx, sha, int64(len(data)), bytes.NewReader(data)))
		f.s.mu.Lock()
		delete(f.s.blobs, sha)
		f.s.mu.Unlock()
		close(fetch.done)
	}()
	start := time.Now()
	b, path, err := f.s.Blob(f.ctx, sha)
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), b.Size)
	assert.Equal(t, f.s.BlobPath(sha), path)
	assert.GreaterOrEqual(t, time.Since(start), 250*time.Millisecond)

	// 拉取失败（只删登记、不关 done）：轮询发现登记消失即 not_found
	other := hex.EncodeToString(make([]byte, 32))
	f.s.mu.Lock()
	f.s.blobs[other] = &blobFetch{orbID: "orb-x", done: make(chan struct{})}
	f.s.mu.Unlock()
	go func() {
		time.Sleep(100 * time.Millisecond)
		f.s.mu.Lock()
		delete(f.s.blobs, other)
		f.s.mu.Unlock()
	}()
	start = time.Now()
	_, _, err = f.s.Blob(f.ctx, other)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second)
}

type blobCtlOrbs struct {
	Orbs
	orbID string
	ctl   *link.Ctl
}

func (o *blobCtlOrbs) Ctl(orbID string) (*link.Ctl, bool) {
	if orbID == o.orbID && o.ctl != nil {
		return o.ctl, true
	}
	return o.Orbs.Ctl(orbID)
}

// 历史会话中缺失的协调宿主 blob：当协调宿主在线时 Blob() 按需发一次 blob.fetch 补拉；不存在的 sha 立即返回 not_found。
func TestBlobBackfillsFromCoordinatorOrb(t *testing.T) {
	f := newFixture(t, Config{})
	prov := mkOrbRow(f.ctx, f.st, "ready")
	po, err := f.st.Q.GetOrb(f.ctx, prov)
	require.NoError(t, err)
	coordOrbID := ids.Orb()
	require.NoError(t, f.st.Q.CreateOrb(f.ctx, db.CreateOrbParams{
		ID: coordOrbID, ProviderID: po.ProviderID, Type: "host", Image: "",
		State: "ready", StateChangedAt: 1, TokenHash: ids.Hash("oot_" + coordOrbID), CreatedAt: 1, Coordinator: true,
	}))
	data := []byte("\x89PNG\r\n\x1a\ncoordinator image")
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	missing := hex.EncodeToString(make([]byte, 32))

	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	cpSess, err := link.NewConnSession(a, false)
	require.NoError(t, err)
	orbdSess, err := link.NewConnSession(b, true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cpSess.Close(); _ = orbdSess.Close() })

	ctlReady := make(chan *link.Ctl, 1)
	go func() {
		for {
			st, err := cpSess.AcceptStream(context.Background())
			if err != nil {
				return
			}
			switch st.Header.T {
			case link.StreamCtl:
				ctlReady <- link.NewCtl(st, link.CtlHandlers{})
			case link.StreamBlob:
				go func() {
					defer st.Close()
					f.s.onBlobStream(f.ctx, coordOrbID, st.Header, st)
				}()
			}
		}
	}()
	orbdCtlSt, err := orbdSess.Open(f.ctx, link.StreamHeader{T: link.StreamCtl})
	require.NoError(t, err)
	orbdCtl := link.NewCtl(orbdCtlSt, link.CtlHandlers{
		OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if method != string(apigen.MethodBlobFetch) {
				return nil, link.ErrMethodNotFound
			}
			var p apigen.LinkCtlBlobFetchParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			if p.Sha256 != sha {
				return apigen.LinkCtlBlobFetchResult{Found: false}, nil
			}
			st, err := orbdSess.Open(ctx, link.StreamHeader{T: link.StreamBlob, SHA256: sha, Size: int64(len(data))})
			if err != nil {
				return nil, err
			}
			go func() {
				defer st.Close()
				_, _ = st.Write(data)
			}()
			return apigen.LinkCtlBlobFetchResult{Found: true}, nil
		},
	})
	t.Cleanup(func() { _ = orbdCtl.Close() })

	cpCtl := <-ctlReady
	t.Cleanup(func() { _ = cpCtl.Close() })
	f.s.orbs = &blobCtlOrbs{Orbs: f.s.orbs, orbID: coordOrbID, ctl: cpCtl}
	// 存在的 blob：按需补拉成功并落盘
	bRow, path, err := f.s.Blob(f.ctx, sha)
	require.NoError(t, err)
	assert.Equal(t, int64(len(data)), bRow.Size)
	assert.Equal(t, "image/png", bRow.Mime)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, data, got)

	// 不存在的 blob：协调宿主回 Found=false，立即返回 not_found（不等 200ms 轮询）
	start := time.Now()
	_, _, err = f.s.Blob(f.ctx, missing)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 150*time.Millisecond)
}
