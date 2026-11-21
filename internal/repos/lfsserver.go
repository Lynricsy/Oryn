package repos

// 托管仓库的 Git LFS 服务端（M24 阶段 ③）：batch 接口 + basic 传输（对象上传下载），不实现文件锁。
// 对象存在 <data_dir>/repos/<id>.git/lfs/objects/<oid[0:2]>/<oid[2:4]>/<oid>（lfs.ObjectPath），上传的临时文件在
// <id>.git/lfs/tmp/：随项目删除，计入 size_bytes，不设大小上限。
//
// 协议依据：
//   - Batch API：https://github.com/git-lfs/git-lfs/blob/main/docs/api/batch.md
//   - Basic 传输：https://github.com/git-lfs/git-lfs/blob/main/docs/api/basic-transfers.md
//   - 文件锁：https://github.com/git-lfs/git-lfs/blob/main/docs/api/locking.md（404 不会中止推送）

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/lfs"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	lfsServeGlobalSlots = 16               // 托管 LFS 对象传输（GET / PUT）的全局名额
	lfsServeOrbSlots    = 8                // 每台 orb 的名额（git-lfs 缺省 8 路并发传输）
	lfsSlotWait         = 30 * time.Second // 没有名额时排队的上限
	lfsHrefTTL          = 3600             // batch 给出的传输地址的有效期（秒）
	lfsFreeCheckEvery   = 64 << 20         // 上传中每写这么多字节复查一次数据盘可用空间
	lfsBatchPath        = "info/lfs/objects/batch"
	lfsObjectsPrefix    = "info/lfs/objects/"
	lfsLocksPath        = "info/lfs/locks"
)

var (
	errLFSSlotBusy  = errors.New("lfs: no transfer slot")
	errLFSPutNoDisk = errors.New("lfs: low disk space")
)

// serveHostedLFS 是 ServeHostedGit 的 info/lfs/ 分流：前置检查（git 可用、项目是托管项目、目录在）的错误用 LFS 的
// JSON 错误体，git-lfs 会显示其中的 message。归档的只读判断由 serveLFS 按读写自己做。
func (s *Service) serveHostedLFS(w http.ResponseWriter, r *http.Request, projectID, endpoint string, pusher egress.HostedPusher) {
	if s.reason != "" {
		lfs.WriteError(w, http.StatusServiceUnavailable, "oryn: 控制面没有可用的 git")
		return
	}
	prj, err := s.project(r.Context(), projectID)
	switch {
	case errors.Is(err, errNotFound) || (err == nil && !prj.Hosted):
		lfs.WriteError(w, http.StatusNotFound, "oryn: 仓库不存在")
		return
	case err != nil:
		slog.Warn("托管仓库 LFS：读取项目", "project", projectID, "err", err)
		lfs.WriteError(w, http.StatusInternalServerError, "oryn: 读取项目失败")
		return
	}
	dir := s.dir(projectID)
	if adopted, err := hostedDirState(dir); err != nil || !adopted {
		lfs.WriteError(w, http.StatusNotFound, "oryn: 仓库不存在")
		return
	}
	s.serveLFS(w, r, prj, dir, endpoint, pusher)
}

// serveLFS 服务托管仓库的 Git LFS 端点：batch + basic 传输；不实现文件锁（一律 404，git-lfs 据此静默关闭推送前的锁校验，locks.go:272）。
func (s *Service) serveLFS(w http.ResponseWriter, r *http.Request, prj db.Project, dir, endpoint string, pusher egress.HostedPusher) {
	objDir := filepath.Join(dir, "lfs", "objects")
	oid, isObject := strings.CutPrefix(endpoint, lfsObjectsPrefix)
	switch {
	case endpoint == lfsBatchPath:
		s.lfsBatch(w, r, prj, objDir, pusher)
	case isObject:
		// 不依赖调用方的路径正则：oid 不合法（含 ../ 之类）一律 404
		if !lfs.ValidOID(oid) {
			lfs.WriteError(w, http.StatusNotFound, "oryn: 对象不存在")
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			s.lfsServeObject(w, r, objDir, oid, pusher)
		case http.MethodPut:
			s.lfsReceiveObject(w, r, prj, dir, objDir, oid, pusher)
		default:
			w.Header().Set("Allow", "GET, HEAD, PUT")
			lfs.WriteError(w, http.StatusMethodNotAllowed, "oryn: 只接受 GET 与 PUT")
		}
	case endpoint == lfsLocksPath || strings.HasPrefix(endpoint, lfsLocksPath+"/"):
		lfs.WriteError(w, http.StatusNotFound, "oryn: 不支持 Git LFS 文件锁")
	default:
		lfs.WriteError(w, http.StatusNotFound, "oryn: 未知的 Git LFS 端点")
	}
}

// lfsHrefBase：pusher.OrbID 为空（主人本机）时是 orbs.HostedCloneURL(PublicURL, id)；否则把它的 scheme 换成 http，
// 这样 orb 内 git-lfs 的传输请求经 http.http://<host>/.proxy 回到出站代理的托管通道（步骤 3.6）。
func (s *Service) lfsHrefBase(projectID string, pusher egress.HostedPusher) string {
	base := orbs.HostedCloneURL(s.cfg.PublicURL, projectID)
	if pusher.OrbID == "" {
		return base
	}
	if rest, ok := strings.CutPrefix(base, "https://"); ok {
		return "http://" + rest
	}
	return base
}

// lfsBatch 服务 POST info/lfs/objects/batch。传输地址带 authenticated:true 与回显的凭据头：
// 回显的是请求自己的令牌，重新拼成 Basic base64("oryn:"+令牌)（不照抄请求头，免得超长用户名乘以对象数放大响应）。
// 经出站代理的请求 Authorization 已被删掉，不会回显。
func (s *Service) lfsBatch(w http.ResponseWriter, r *http.Request, prj db.Project, objDir string, pusher egress.HostedPusher) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		lfs.WriteError(w, http.StatusMethodNotAllowed, "oryn: 只接受 POST")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || (mt != lfs.MediaType && mt != "application/json") {
		lfs.WriteError(w, http.StatusUnsupportedMediaType, "oryn: 请求类型必须是 "+lfs.MediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, lfs.BatchBodyMax))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			lfs.WriteError(w, http.StatusRequestEntityTooLarge, "oryn: 批量请求过大")
			return
		}
		lfs.WriteError(w, http.StatusBadRequest, "oryn: 读取请求体失败")
		return
	}
	var req lfs.BatchRequest
	if json.Unmarshal(body, &req) != nil {
		lfs.WriteError(w, http.StatusUnprocessableEntity, "oryn: 请求体不是合法的批量请求")
		return
	}
	switch {
	case req.Operation != "download" && req.Operation != "upload":
		lfs.WriteError(w, http.StatusUnprocessableEntity, "oryn: operation 必须是 download 或 upload")
		return
	case len(req.Transfers) > 0 && !slices.Contains(req.Transfers, "basic"):
		lfs.WriteError(w, http.StatusUnprocessableEntity, "oryn: 只支持 basic 传输")
		return
	case len(req.Objects) == 0:
		lfs.WriteError(w, http.StatusUnprocessableEntity, "oryn: objects 不能为空")
		return
	case len(req.Objects) > lfs.BatchMaxObjects:
		lfs.WriteError(w, http.StatusRequestEntityTooLarge, "oryn: 批量请求过大")
		return
	}
	upload := req.Operation == "upload"
	if upload && prj.ArchivedAt != nil {
		lfs.WriteError(w, http.StatusForbidden, "oryn: 项目已归档，仓库只读")
		return
	}

	base := s.lfsHrefBase(prj.ID, pusher) + "/" + lfsObjectsPrefix
	var header map[string]string
	if tok := auth.GitRequestToken(r); tok != "" {
		header = map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("oryn:"+tok))}
	}
	action := "download"
	if upload {
		action = "upload"
	}
	out := lfs.BatchResponse{Transfer: "basic", HashAlgo: "sha256", Objects: make([]lfs.ObjectResult, 0, len(req.Objects))}
	for _, o := range req.Objects {
		res := lfs.ObjectResult{OID: o.OID, Size: o.Size}
		switch {
		case req.HashAlgo != "" && req.HashAlgo != "sha256":
			res.Error = &lfs.ObjectError{Code: http.StatusConflict, Message: "oryn: 只支持 sha256"}
		case !lfs.ValidOID(o.OID) || o.Size < 0:
			res.Error = &lfs.ObjectError{Code: http.StatusUnprocessableEntity, Message: "oryn: oid 或 size 无效"}
		default:
			size, have := objectSize(lfs.ObjectPath(objDir, o.OID))
			switch {
			case !upload && !have:
				res.Error = &lfs.ObjectError{Code: http.StatusNotFound, Message: "Object does not exist"}
			case !upload && size != o.Size:
				res.Error = &lfs.ObjectError{Code: http.StatusUnprocessableEntity, Message: "oryn: 对象大小与请求不符"}
			case upload && have && size == o.Size:
				// 已有且大小相符：省略 actions（batch.md）
			default:
				// 上传时对象不存在，或已存在但大小不符（上次写坏了）：给 upload，PUT 校验通过后覆盖。不给 verify，PUT 当场校验。
				res.Authenticated = true
				res.Actions = map[string]*lfs.Action{action: {Href: base + o.OID, Header: header, ExpiresIn: lfsHrefTTL}}
			}
		}
		out.Objects = append(out.Objects, res)
	}
	b, err := json.Marshal(out)
	if err != nil {
		lfs.WriteError(w, http.StatusInternalServerError, "oryn: 编码批量响应失败")
		return
	}
	h := w.Header()
	h.Set("Content-Type", lfs.MediaType)
	h.Set("Cache-Control", "no-store") // 响应体里有设备令牌，不能被中间层缓存
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// objectSize 是常规文件 path 的大小；不存在或不是常规文件时 ok=false。
func objectSize(path string) (size int64, ok bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return 0, false
	}
	return fi.Size(), true
}

// lfsServeObject 服务对象下载（GET，支持 Range）。内容是 agent 能上传的任意字节：一律 attachment + nosniff，
// 不让浏览器在 Oryn 的源下把它当页面打开。
func (s *Service) lfsServeObject(w http.ResponseWriter, r *http.Request, objDir, oid string, pusher egress.HostedPusher) {
	release, ok := s.lfsTransferSlot(w, r, pusher)
	if !ok {
		return
	}
	defer release()
	f, err := os.Open(lfs.ObjectPath(objDir, oid))
	if errors.Is(err, fs.ErrNotExist) {
		lfs.WriteError(w, http.StatusNotFound, "oryn: 对象不存在")
		return
	}
	if err != nil {
		slog.Warn("托管仓库 LFS：打开对象", "oid", oid, "err", err)
		lfs.WriteError(w, http.StatusInternalServerError, "oryn: 读取对象失败")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		lfs.WriteError(w, http.StatusNotFound, "oryn: 对象不存在")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Disposition", "attachment")
	h.Set("ETag", `"`+oid+`"`)
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// lfsReceiveObject 服务对象上传（PUT）：流式写进 lfs/tmp，边写边算 sha256，字节数与摘要都对才原子改名到位
// （目标已存在也覆盖，写坏的对象因此能被修复）。所有失败路径（含客户端中途断开）都不留临时文件。
func (s *Service) lfsReceiveObject(w http.ResponseWriter, r *http.Request, prj db.Project, dir, objDir, oid string, pusher egress.HostedPusher) {
	if prj.ArchivedAt != nil {
		lfs.WriteError(w, http.StatusForbidden, "oryn: 项目已归档，仓库只读")
		return
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	default:
		lfs.WriteError(w, http.StatusUnsupportedMediaType, "oryn: 上传不能压缩")
		return
	}
	n := r.ContentLength
	if n < 0 {
		lfs.WriteError(w, http.StatusLengthRequired, "oryn: 上传需要 Content-Length")
		return
	}
	release, ok := s.lfsTransferSlot(w, r, pusher)
	if !ok {
		return
	}
	defer release()
	if free, err := freeBytes(s.cfg.Dir); err == nil && free-n < s.cfg.MinFreeBytes {
		lfs.WriteError(w, http.StatusInsufficientStorage, "oryn: 磁盘空间不足")
		return
	}

	tmpDir := filepath.Join(dir, "lfs", "tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		s.lfsPutFailed(w, oid, "建临时目录", err)
		return
	}
	f, err := os.CreateTemp(tmpDir, oid+"-*")
	if err != nil {
		s.lfsPutFailed(w, oid, "建临时文件", err)
		return
	}
	defer os.Remove(f.Name()) // 改名成功后是空操作
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()

	sum := sha256.New()
	fw := &freeWatchWriter{w: f, s: s, total: n}
	// 请求体 stdinIdle 没有新数据就中止（同 receive-pack）；读到的任何错误都表现为 EOF，由字节数校验兜住
	pump := startStdinPump(io.LimitReader(r.Body, n+1), http.NewResponseController(w), stdinIdle)
	written, err := io.Copy(io.MultiWriter(fw, sum), pump)
	_ = pump.Close()
	if err != nil { // 读侧只会给 EOF，这里只剩写盘错误
		s.lfsPutFailed(w, oid, "写对象", err)
		return
	}
	if written != n {
		lfs.WriteError(w, http.StatusBadRequest, "oryn: 请求体长度与 Content-Length 不符")
		return
	}
	if hex.EncodeToString(sum.Sum(nil)) != oid {
		lfs.WriteError(w, http.StatusUnprocessableEntity, "oryn: 内容的 sha256 与 oid 不符")
		return
	}
	if err := f.Sync(); err != nil {
		s.lfsPutFailed(w, oid, "落盘", err)
		return
	}
	closed = true
	if err := f.Close(); err != nil {
		s.lfsPutFailed(w, oid, "落盘", err)
		return
	}
	dst := lfs.ObjectPath(objDir, oid)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		s.lfsPutFailed(w, oid, "建对象目录", err)
		return
	}
	if err := os.Rename(f.Name(), dst); err != nil {
		s.lfsPutFailed(w, oid, "放置对象", err)
		return
	}
	if err := syncDir(filepath.Dir(dst)); err != nil {
		slog.Warn("托管仓库 LFS：同步对象目录", "oid", oid, "err", err)
	}
	w.WriteHeader(http.StatusOK)
}

// lfsPutFailed 回上传失败：磁盘写满 → 507，其余 → 500（记日志）。
func (s *Service) lfsPutFailed(w http.ResponseWriter, oid, step string, err error) {
	if errors.Is(err, errLFSPutNoDisk) || errors.Is(err, syscall.ENOSPC) {
		lfs.WriteError(w, http.StatusInsufficientStorage, "oryn: 磁盘空间不足")
		return
	}
	slog.Warn("托管仓库 LFS：上传对象", "oid", oid, "step", step, "err", err)
	lfs.WriteError(w, http.StatusInternalServerError, "oryn: 保存对象失败")
}

// freeWatchWriter 每写 lfsFreeCheckEvery 字节复查一次数据盘：可用空间减去还没写的部分低于 MinFreeBytes 时报
// errLFSPutNoDisk（并发上传会一起消耗空间，开始前的检查不够）。
type freeWatchWriter struct {
	w            io.Writer
	s            *Service
	total, wrote int64
	since        int64
}

func (fw *freeWatchWriter) Write(b []byte) (int, error) {
	if fw.since += int64(len(b)); fw.since >= lfsFreeCheckEvery {
		fw.since = 0
		if free, err := freeBytes(fw.s.cfg.Dir); err == nil && free-max(fw.total-fw.wrote, 0) < fw.s.cfg.MinFreeBytes {
			return 0, errLFSPutNoDisk
		}
	}
	n, err := fw.w.Write(b)
	fw.wrote += int64(n)
	return n, err
}

// syncDir 让目录项（改名）落盘。
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// lfsTransferSlot 占一个对象传输名额；没有名额时排队，等满 lfsSlotWait 回 503 + Retry-After。ok=false 时已写好响应。
func (s *Service) lfsTransferSlot(w http.ResponseWriter, r *http.Request, pusher egress.HostedPusher) (release func(), ok bool) {
	release, err := s.lfsSlots.acquire(r.Context(), pusher.OrbID)
	if err != nil {
		w.Header().Set("Retry-After", "5")
		lfs.WriteError(w, http.StatusServiceUnavailable, "oryn: 仓库正忙，请稍后重试")
		return nil, false
	}
	return release, true
}

// lfsSlots 是托管 LFS 对象传输的名额：全局 lfsServeGlobalSlots 个，每台 orb lfsServeOrbSlots 个；零值可用。
// 不与 git 进程共用 smartSlots：git-lfs 缺省 8 路并发传输，共用的话一次 LFS 推送就会占满每台 orb 2 个的 git 名额。
type lfsSlots struct {
	mu    sync.Mutex
	total int
	byOrb map[string]int
	freed chan struct{} // 有名额释放时关闭并换新，排队的据此重试
	wait  time.Duration // 排队上限；0 = lfsSlotWait（测试可改）
}

// acquire 占一个名额，没有时排队：ctx 结束返回 ctx.Err()，等满上限返回 errLFSSlotBusy。orbID 为空（主人本机）只占全局名额。
func (l *lfsSlots) acquire(ctx context.Context, orbID string) (release func(), err error) {
	t := time.NewTimer(cmp.Or(l.wait, lfsSlotWait))
	defer t.Stop()
	for {
		l.mu.Lock()
		if l.total < lfsServeGlobalSlots && (orbID == "" || l.byOrb[orbID] < lfsServeOrbSlots) {
			l.total++
			if orbID != "" {
				if l.byOrb == nil {
					l.byOrb = map[string]int{}
				}
				l.byOrb[orbID]++
			}
			l.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { l.release(orbID) }) }, nil
		}
		if l.freed == nil {
			l.freed = make(chan struct{})
		}
		freed := l.freed
		l.mu.Unlock()
		select {
		case <-freed:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
			return nil, errLFSSlotBusy
		}
	}
}

func (l *lfsSlots) release(orbID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if orbID != "" {
		if l.byOrb[orbID]--; l.byOrb[orbID] <= 0 {
			delete(l.byOrb, orbID)
		}
	}
	if l.freed != nil {
		close(l.freed)
		l.freed = nil
	}
}

// hostedLFSObject 打开托管项目库里的 LFS 对象（读接口用）：不经缓存、不访问网络，对任何大小都返回文件。
func (s *Service) hostedLFSObject(projectID string, p lfs.Pointer) (*os.File, error) {
	f, err := os.Open(lfs.ObjectPath(filepath.Join(s.dir(projectID), "lfs", "objects"), p.OID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errLFSMissing
	}
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if fi.Size() != p.Size {
		_ = f.Close()
		return nil, lfsErr(errLFSCorrupt, "托管库里的对象大小与指针不符")
	}
	return f, nil
}
