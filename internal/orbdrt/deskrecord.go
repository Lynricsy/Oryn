package orbdrt

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// 录屏（M25 计划 3.1）：ffmpeg x11grab 录 :1，每台 orb 同时最多一个录制。文件放在
// `$HOME/recordings/<UTC yyyyMMdd-HHmmss>.mp4`，只保留最近 recordKeep 个。停止时往 ffmpeg 的 stdin 写 `q`
// 让它收尾（写完 moov），最多等 recordStopGrace，超时对进程组 SIGKILL 并标记 Truncated。
// 到时长上限自动停止（StoppedBy=limit）；ffmpeg 自己退出或显示失效（Xvnc 退出、重启）时按 StoppedBy=error 收尾。
// 自动停下的结果留给下一次 stop 取走。本机路由与 orbd.sock 方法共用 start / stop。

const (
	recordFPS = 15
	// recordMaxDuration 是单次录制的时长上限。
	recordMaxDuration = 10 * time.Minute
	// recordStopGrace 是写 `q` 之后等 ffmpeg 收尾的上限。
	recordStopGrace = 10 * time.Second
	// recordKeep 是 $HOME/recordings 里保留的录制文件数。
	recordKeep = 20
	// recordProbeTimeout 是 xdpyinfo 取根窗口尺寸的上限。
	recordProbeTimeout = 5 * time.Second
	// recordFallbackWidth / Height 是取不到根窗口尺寸时的分辨率（与 Xvnc 的 -geometry 相同）。
	recordFallbackWidth  = 1280
	recordFallbackHeight = 800
)

var (
	errAlreadyRecording = errors.New("desktop: already recording")
	errNotRecording     = errors.New("desktop: not recording")
	errDisplayNotReady  = errors.New("desktop: display not ready")
)

// recordNameRe 匹配录制器写的文件名：时间戳，同一秒内的后续录制带 `-<n>` 后缀。保留策略只动这些文件。
var recordNameRe = regexp.MustCompile(`^(\d{8}-\d{6})(?:-(\d+))?\.mp4$`)

// xdpyinfoDimsRe 匹配 xdpyinfo 输出里的 `dimensions:    1280x800 pixels`。
var xdpyinfoDimsRe = regexp.MustCompile(`dimensions:\s+(\d+)x(\d+)\s+pixels`)

// deskRecorders 把录制器挂到 desktop 上（懒创建；每个 Runtime 至多一个 desktop）。
var deskRecorders sync.Map // *desktop → *deskRecorder

// recorder 返回 d 的录制器。
func (d *desktop) recorder() *deskRecorder {
	if v, ok := deskRecorders.Load(d); ok {
		return v.(*deskRecorder)
	}
	v, _ := deskRecorders.LoadOrStore(d, &deskRecorder{
		d:     d,
		limit: recordMaxDuration,
		grace: recordStopGrace,
		keep:  recordKeep,
	})
	return v.(*deskRecorder)
}

// deskRecorder 是桌面的录制器。limit / grace / keep 只在 mu 下读写（测试里缩短）。
type deskRecorder struct {
	d *desktop

	mu    sync.Mutex
	limit time.Duration
	grace time.Duration
	keep  int
	cur   *recording // 进行中的录制；nil = 没在录
	last  *recording // 因上限或出错自动停下、还没被 stop 取走的录制
}

// recording 是一次录制。res 在 finished 关闭之前写好，之后只读。
type recording struct {
	path    string
	w, h    int
	started time.Time
	pid     int
	stdin   io.WriteCloser
	exited  <-chan struct{} // ffmpeg 被回收后关闭
	stopReq chan struct{}   // 容量 1：调用方要求停止

	finished chan struct{}
	res      orbsock.DesktopRecording
}

// active 报告是否有录制在进行。
func (rr *deskRecorder) active() bool {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return rr.cur != nil
}

// start 开始录制：已在录回 errAlreadyRecording，显示未就绪回 errDisplayNotReady。
func (rr *deskRecorder) start(ctx context.Context) (orbsock.DesktopRecordStartResult, error) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.cur != nil {
		return orbsock.DesktopRecordStartResult{}, errAlreadyRecording
	}
	d := rr.d
	d.mu.Lock()
	ready, xpid := d.displayReady, d.xvncPid
	d.mu.Unlock()
	if !ready {
		return orbsock.DesktopRecordStartResult{}, errDisplayNotReady
	}
	w, h := d.rootSize(ctx)
	path, err := newRecordingPath(filepath.Join(d.r.home, "recordings"), time.Now())
	if err != nil {
		return orbsock.DesktopRecordStartResult{}, err
	}
	rec, err := d.startFFmpeg(path, w, h)
	if err != nil {
		return orbsock.DesktopRecordStartResult{}, err
	}
	// 上一次自动停下的结果没人取走：新录制开始后不再给出。
	rr.cur, rr.last = rec, nil
	limit, grace := rr.limit, rr.grace
	d.log.Info("desktop recording started", "path", path, "size", fmt.Sprintf("%dx%d", w, h), "limit", limit)
	go rr.watch(rec, xpid, limit, grace)
	return orbsock.DesktopRecordStartResult{Path: path}, nil
}

// stop 停止当前录制并返回结果；没在录时取走上一次自动停下的结果，都没有回 errNotRecording。
func (rr *deskRecorder) stop(ctx context.Context) (orbsock.DesktopRecording, error) {
	rr.mu.Lock()
	rec := rr.cur
	if rec == nil {
		last := rr.last
		rr.last = nil
		rr.mu.Unlock()
		if last == nil {
			return orbsock.DesktopRecording{}, errNotRecording
		}
		return last.res, nil
	}
	rr.mu.Unlock()
	select {
	case rec.stopReq <- struct{}{}:
	default:
	}
	select {
	case <-rec.finished:
	case <-ctx.Done():
		return orbsock.DesktopRecording{}, ctx.Err()
	}
	// 与上限或出错同时发生时，结果已经记成待取走：由这次 stop 取走。
	rr.mu.Lock()
	if rr.last == rec {
		rr.last = nil
	}
	rr.mu.Unlock()
	return rec.res, nil
}

// watch 等到调用方停止、到达上限、ffmpeg 自己退出或显示失效（显示掉了，或 Xvnc 换了进程）之一，然后收尾。
func (rr *deskRecorder) watch(rec *recording, xpid int, limit, grace time.Duration) {
	d := rr.d
	timer := time.NewTimer(limit)
	defer timer.Stop()
	reason := ""
	for reason == "" {
		d.mu.Lock()
		changed := d.changed
		lost := !d.displayReady || d.xvncPid != xpid
		d.mu.Unlock()
		if lost {
			d.log.Warn("display lost while recording; stopping", "path", rec.path)
			reason = orbsock.RecordStoppedByError
			break
		}
		select {
		case <-changed:
		case <-rec.stopReq:
			reason = orbsock.RecordStoppedByAgent
		case <-timer.C:
			reason = orbsock.RecordStoppedByLimit
		case <-rec.exited:
			d.log.Warn("ffmpeg exited while recording", "path", rec.path)
			reason = orbsock.RecordStoppedByError
		}
	}
	rr.finish(rec, reason, grace)
}

// finish 让 ffmpeg 收尾（stdin 写 `q`，至多等 grace，超时 SIGKILL 进程组），记下结果并按保留数清理旧文件。
func (rr *deskRecorder) finish(rec *recording, reason string, grace time.Duration) {
	d := rr.d
	end := time.Now()
	truncated := false
	select {
	case <-rec.exited:
	default:
		_, _ = io.WriteString(rec.stdin, "q")
		_ = rec.stdin.Close()
		t := time.NewTimer(grace)
		select {
		case <-rec.exited:
		case <-t.C:
			d.log.Warn("ffmpeg did not finish in time; killing it", "path", rec.path, "grace", grace)
			killWait(rec.pid, rec.exited)
			truncated = true
		}
		t.Stop()
	}
	var size int64
	if fi, err := os.Stat(rec.path); err == nil {
		size = fi.Size()
	}
	rec.res = orbsock.DesktopRecording{
		Path:       rec.path,
		Bytes:      size,
		DurationMs: end.Sub(rec.started).Milliseconds(),
		Width:      rec.w,
		Height:     rec.h,
		Truncated:  truncated,
		StoppedBy:  reason,
	}
	rr.mu.Lock()
	rr.cur = nil
	if reason != orbsock.RecordStoppedByAgent {
		rr.last = rec
	}
	keep := rr.keep
	// 在 mu 下清理：不会与下一次 start 新建的文件交错。
	pruneRecordings(filepath.Dir(rec.path), keep, d.log)
	rr.mu.Unlock()
	d.log.Info("desktop recording stopped", "path", rec.path, "by", reason, "bytes", size, "truncated", truncated)
	close(rec.finished)
}

// rootSize 用 xdpyinfo 取根窗口尺寸（宽高向下取偶数，libx264 的 yuv420p 要求偶数）；取不到时用 1280x800。
func (d *desktop) rootSize(ctx context.Context) (int, int) {
	w, h := recordFallbackWidth, recordFallbackHeight
	if _, err := d.r.bins.look("xdpyinfo"); err == nil {
		pctx, cancel := context.WithTimeout(ctx, recordProbeTimeout)
		cmd := d.r.bins.command(pctx, "xdpyinfo", "-display", desktopDisplay)
		cmd.Env = envList(d.r.baseEnv())
		out, err := cmd.Output()
		cancel()
		if m := xdpyinfoDimsRe.FindSubmatch(out); err == nil && m != nil {
			pw, _ := strconv.Atoi(string(m[1]))
			ph, _ := strconv.Atoi(string(m[2]))
			if pw >= 2 && ph >= 2 {
				w, h = pw, ph
			}
		} else {
			d.log.Warn("xdpyinfo failed; recording at the default size", "err", err)
		}
	}
	return w &^ 1, h &^ 1
}

// newRecordingPath 建 dir 并返回本次录制的文件路径：UTC 时间戳，同一秒内已有文件时加 `-2`、`-3`……
func newRecordingPath(dir string, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	stamp := now.UTC().Format("20060102-150405")
	for n := 1; ; n++ {
		name := stamp + ".mp4"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.mp4", stamp, n)
		}
		p := filepath.Join(dir, name)
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p, nil
		} else if err != nil {
			return "", err
		}
	}
}

// pruneRecordings 只保留 dir 里最新的 keep 个录制文件（按文件名里的时间戳与序号排序，不认识的文件不动）。
func pruneRecordings(dir string, keep int, log *slog.Logger) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		log.Warn("list recordings failed", "err", err)
		return
	}
	type entry struct {
		name, stamp string
		n           int
	}
	var files []entry
	for _, e := range ents {
		m := recordNameRe.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		n := 1
		if m[2] != "" {
			n, _ = strconv.Atoi(m[2])
		}
		files = append(files, entry{name: e.Name(), stamp: m[1], n: n})
	}
	if len(files) <= keep {
		return
	}
	slices.SortFunc(files, func(a, b entry) int {
		return cmp.Or(cmp.Compare(a.stamp, b.stamp), cmp.Compare(a.n, b.n))
	})
	for _, f := range files[:len(files)-keep] {
		if err := os.Remove(filepath.Join(dir, f.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warn("remove old recording failed", "file", f.name, "err", err)
		}
	}
}

// startFFmpeg 以独立进程组启动 ffmpeg 录 :1 到 path（输出追加到 ffmpeg.log），stdin 留给停止时写 `q`。
func (d *desktop) startFFmpeg(path string, w, h int) (*recording, error) {
	args := []string{
		"-hide_banner", "-nostats", "-loglevel", "warning",
		"-f", "x11grab", "-framerate", strconv.Itoa(recordFPS), "-video_size", fmt.Sprintf("%dx%d", w, h),
		"-i", desktopDisplay,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "28", "-pix_fmt", "yuv420p", "-movflags", "+faststart",
		"-y", path,
	}
	lg := d.logFor("ffmpeg")
	cmd := d.r.bins.command(context.Background(), "ffmpeg", args...)
	cmd.Dir = d.r.home
	cmd.Env = envList(d.r.baseEnv())
	cmd.Stdout, cmd.Stderr = lg, lg
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(lg, "[orbd] starting ffmpeg -> %s (%dx%d)\n", path, w, h)
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintf(lg, "[orbd] start failed: %v\n", err)
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	exited := make(chan struct{})
	go func() {
		err := cmd.Wait()
		_, _ = fmt.Fprintf(lg, "[orbd] ffmpeg exited: %v\n", err)
		close(exited)
	}()
	return &recording{
		path:     path,
		w:        w,
		h:        h,
		started:  time.Now(),
		pid:      cmd.Process.Pid,
		stdin:    stdin,
		exited:   exited,
		stopReq:  make(chan struct{}, 1),
		finished: make(chan struct{}),
	}, nil
}

// ---- 本机路由与 orbd.sock 的错误映射 ----

func (d *desktop) handleRecordStart(w http.ResponseWriter, req *http.Request) {
	res, err := d.recorder().start(req.Context())
	if err != nil {
		writeErr(w, recordAPIErr(err))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (d *desktop) handleRecordStop(w http.ResponseWriter, req *http.Request) {
	res, err := d.recorder().stop(req.Context())
	if err != nil {
		writeErr(w, recordAPIErr(err))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// recordAPIErr 把录制器的错误映射为本机 API 错误。
func recordAPIErr(err error) *apiError {
	switch {
	case errors.Is(err, errAlreadyRecording):
		return errf(http.StatusConflict, "already_recording", "the desktop is already being recorded")
	case errors.Is(err, errNotRecording):
		return errf(http.StatusConflict, "not_recording", "the desktop is not being recorded")
	case errors.Is(err, errDisplayNotReady):
		return errf(http.StatusConflict, "not_ready", "the desktop display is not ready yet")
	}
	return errf(http.StatusInternalServerError, "failed", "desktop recording: %v", err)
}

// recordSockErr 把录制器的错误映射为 orbd.sock 错误。
func recordSockErr(err error) *orbsock.Error {
	switch {
	case errors.Is(err, errAlreadyRecording):
		return orbsock.Errorf(orbsock.CodeAlreadyRecording, "the desktop is already being recorded")
	case errors.Is(err, errNotRecording):
		return orbsock.Errorf(orbsock.CodeNotRecording, "the desktop is not being recorded")
	case errors.Is(err, errDisplayNotReady):
		return orbsock.Errorf(orbsock.CodeNotReady, "the desktop display is not ready yet")
	}
	return orbsock.Errorf(orbsock.CodeInternal, "desktop recording: %v", err)
}
