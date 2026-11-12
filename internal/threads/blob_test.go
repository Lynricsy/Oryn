package threads

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
