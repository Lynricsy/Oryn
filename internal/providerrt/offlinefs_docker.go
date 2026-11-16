package providerrt

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// docker 驱动的停机 orb 只读预览：经 `docker container inspect` 确认容器已停机，用 `docker cp <orb>:<path> -`
// 的 tar 流建索引、嗅探与读取文件（停机容器无法 exec）。

const (
	// offlineInspectTimeout 是读取容器状态的时限。
	offlineInspectTimeout = 15 * time.Second
	// offlineSniffTimeout 是 stat 读取文件前缀的时限。
	offlineSniffTimeout = 30 * time.Second
	// offlineHeaderTimeout 是 read 等到 tar 流第一个条目头的时限（之后的内容只受请求上下文约束）。
	offlineHeaderTimeout = 30 * time.Second
)

// offlineInspectFormat 一次取出缓存键所需的容器字段。
const offlineInspectFormat = "{{.Id}} {{.State.Status}} {{.State.FinishedAt}}"

// OfflineFS 实现 OfflineFS。
func (d *Docker) OfflineFS(ctx, life context.Context, req apigen.ProviderFsRequest) FsReply {
	key, err := d.offlineKey(ctx, req.OrbID)
	if err != nil {
		return errReply(err)
	}
	orbID := req.OrbID
	ix, err := d.offline.get(ctx, life, orbID, key, func(bctx context.Context, scanned *atomic.Int64) (*fsIndex, error) {
		return d.indexHome(bctx, orbID, scanned)
	})
	if err != nil {
		return indexErrReply(err)
	}
	return serveOffline(ctx, ix, req, dockerOffline{d: d, orbID: orbID})
}

// dockerOffline 是 docker 的 offlineBackend：按路径经 `docker cp` 读取（停机容器无法 exec）。
type dockerOffline struct {
	d     *Docker
	orbID string
}

func (o dockerOffline) sniff(ctx context.Context, real string, _ *fsNode) (string, bool, error) {
	return o.d.offlineSniff(ctx, o.orbID, real)
}

func (o dockerOffline) read(ctx context.Context, real string, _ *fsNode, rangeHdr string) FsReply {
	return o.d.offlineRead(ctx, o.orbID, real, rangeHdr)
}

// offlineKey 读取容器状态并返回索引缓存键（Id + FinishedAt + Status）。只服务已停机（exited / created）的容器。
func (d *Docker) offlineKey(ctx context.Context, orbID string) (string, error) {
	ictx, cancel := context.WithTimeout(ctx, offlineInspectTimeout)
	defer cancel()
	var stdout bytes.Buffer
	err := d.exec(ictx, &stdout, io.Discard, nil, "container", "inspect", "--format", offlineInspectFormat, orbID)
	if isNoSuch(err) {
		return "", fsErrf(http.StatusNotFound, "not_found", "no such orb container %s", orbID)
	}
	if err != nil {
		return "", err
	}
	f := strings.Fields(stdout.String())
	if len(f) < 3 {
		return "", fmt.Errorf("docker inspect %s: 无法解析输出 %q", orbID, strings.TrimSpace(stdout.String()))
	}
	if f[1] != "exited" && f[1] != "created" {
		return "", fsErrf(http.StatusConflict, "orb_not_stopped", "orb container %s is %s", orbID, f[1])
	}
	return f[0] + " " + f[2] + " " + f[1], nil
}

// indexHome 用 `docker cp <orb>:OrbHome -` 的 tar 流建立索引（达到条目上限时提前结束 docker 进程）。
func (d *Docker) indexHome(ctx context.Context, orbID string, scanned *atomic.Int64) (*fsIndex, error) {
	s, err := d.execStream(ctx, "cp", orbID+":"+OrbHome, "-")
	if err != nil {
		return nil, err
	}
	ix, err := buildIndex(s, func(n int) { scanned.Store(int64(n)) })
	if err != nil {
		_ = s.Close()
		if msg := strings.TrimSpace(s.stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w（docker: %s）", err, msg)
		}
		return nil, err
	}
	if ix.truncated {
		_ = s.Close()
		return ix, nil
	}
	if err := s.Wait(); err != nil {
		return nil, err
	}
	return ix, nil
}

// openOfflineFile 用 `docker cp <orb>:<real> -` 打开文件，返回 tar 流第一个条目（必须是普通文件）的头与内容读端。
// 调用方负责 Close 返回的 streamCmd。
func (d *Docker) openOfflineFile(ctx context.Context, orbID, real string) (*streamCmd, *tar.Header, *tar.Reader, error) {
	s, err := d.execStream(ctx, "cp", orbID+":"+real, "-")
	if err != nil {
		return nil, nil, nil, err
	}
	t := time.AfterFunc(offlineHeaderTimeout, s.cancel)
	tr := tar.NewReader(s)
	hdr, err := tr.Next()
	switch {
	case !t.Stop():
		_ = s.Close()
		return nil, nil, nil, fmt.Errorf("docker cp %s:%s: %s 内没有输出", orbID, real, offlineHeaderTimeout)
	case errors.Is(err, io.EOF):
		werr := s.Wait()
		if isNoSuch(werr) {
			return nil, nil, nil, notFound(real)
		}
		if werr == nil {
			werr = fmt.Errorf("docker cp %s:%s: 空的 tar 流", orbID, real)
		}
		return nil, nil, nil, werr
	case err != nil:
		_ = s.Close()
		return nil, nil, nil, fmt.Errorf("读取 docker cp 输出: %w", err)
	}
	if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != '\x00' && hdr.Typeflag != tar.TypeGNUSparse {
		_ = s.Close()
		return nil, nil, nil, fsErrf(http.StatusBadRequest, "invalid_path", "%s is not a regular file", real)
	}
	return s, hdr, tr, nil
}

// offlineSniff 读文件前 offlineSniffBytes 字节（读够即结束 docker 进程），给出 mime 与是否文本。
func (d *Docker) offlineSniff(ctx context.Context, orbID, real string) (string, bool, error) {
	sctx, cancel := context.WithTimeout(ctx, offlineSniffTimeout)
	defer cancel()
	s, _, tr, err := d.openOfflineFile(sctx, orbID, real)
	if err != nil {
		return "", false, err
	}
	defer s.Close()
	buf := make([]byte, offlineSniffBytes)
	n, err := io.ReadFull(tr, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", false, err
	}
	m, text := sniffContent(real, buf[:n], n == offlineSniffBytes)
	return m, text, nil
}

// offlineRead 流式读取文件（单区间 Range）。应答体关闭时结束 docker 进程。
func (d *Docker) offlineRead(ctx context.Context, orbID, real, rangeHdr string) FsReply {
	s, hdr, tr, err := d.openOfflineFile(ctx, orbID, real)
	if err != nil {
		return errReply(err)
	}
	rep, start, length, ok := rangeReply(rangeHdr, hdr.Size, hdr.ModTime)
	if !ok {
		_ = s.Close()
		return rep
	}
	rep.Body = &offlineBody{s: s, r: tr, skip: start, left: length}
	return rep
}

// offlineBody 是 read 的应答体：先跳过 skip 字节（首次读取时），再给出 left 字节；Close 结束 docker 进程。
type offlineBody struct {
	s    *streamCmd
	r    io.Reader
	skip int64
	left int64
	once sync.Once
}

func (b *offlineBody) Read(p []byte) (int, error) {
	if b.skip > 0 {
		n, err := io.CopyN(io.Discard, b.r, b.skip)
		b.skip -= n
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return 0, err
		}
	}
	if b.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.r.Read(p)
	b.left -= int64(n)
	if errors.Is(err, io.EOF) && b.left > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func (b *offlineBody) Close() error {
	b.once.Do(func() { _ = b.s.Close() })
	return nil
}
