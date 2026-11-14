package orbdrt

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"syscall"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// 桌面本机 API（M8 计划 2.7，docs/protocol.md「orbd 本机 API（desktop 特性）」）与 orbd.sock 方法（2.10）。
// 桌面未启用（r.desk == nil）时这些路由回 404 not_found、sock 方法回 not_supported。

var profileIDRe = regexp.MustCompile(`^bpf_[0-9A-Z]{26}$`)

// deskRoutes 把桌面路由挂到本机 API。
func (r *Runtime) deskRoutes(mux *http.ServeMux) {
	route := func(pattern string, h func(*desktop, http.ResponseWriter, *http.Request)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			if r.desk == nil {
				writeErr(w, errf(http.StatusNotFound, "not_found", "the desktop is not enabled on this orb"))
				return
			}
			h(r.desk, w, req)
		})
	}
	route("GET /desktop/state", func(d *desktop, w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, d.snapshot())
	})
	route("POST /desktop/browser/start", (*desktop).handleBrowserStart)
	route("POST /desktop/browser/stop", func(d *desktop, w http.ResponseWriter, _ *http.Request) {
		d.stopBrowser(browserStopGrace)
		writeJSON(w, http.StatusOK, d.snapshot())
	})
	route("PUT /desktop/profile", (*desktop).handleProfilePut)
	route("DELETE /desktop/profile", (*desktop).handleProfileDelete)
	route("GET /desktop/profile/export", (*desktop).handleProfileExport)
}

func (d *desktop) handleBrowserStart(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithTimeout(req.Context(), browserStartWait)
	defer cancel()
	if err := d.startBrowser(ctx); err != nil {
		if errors.Is(err, errNotReady) {
			writeErr(w, errf(http.StatusConflict, "not_ready", "%v", err))
			return
		}
		writeErr(w, errf(http.StatusInternalServerError, "failed", "start browser: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, d.snapshot())
}

// profileModeQuery 解析 `mode=exclusive|readonly`（缺省 exclusive）。
func profileModeQuery(req *http.Request) (apigen.BrowserProfileAccess, error) {
	switch v := apigen.BrowserProfileAccess(req.URL.Query().Get("mode")); v {
	case "":
		return apigen.BrowserProfileAccessExclusive, nil
	case apigen.BrowserProfileAccessExclusive, apigen.BrowserProfileAccessReadonly:
		return v, nil
	default:
		return "", badRequest("invalid mode=%q (want exclusive or readonly)", v)
	}
}

// withMode 返回把 m 的使用方式改为 mode 的副本：转入只读时原因记为 requested（已是只读则保留原因）。
func withMode(m profileMarker, mode apigen.BrowserProfileAccess) profileMarker {
	if mode == apigen.BrowserProfileAccessReadonly {
		if m.access() != apigen.BrowserProfileAccessReadonly {
			m.ReadonlyReason, m.ReadonlyBy = apigen.BrowserProfileReadonlyReasonRequested, ""
		}
		m.Mode = mode
		return m
	}
	m.Mode = apigen.BrowserProfileAccessExclusive
	m.ReadonlyReason, m.ReadonlyBy = "", ""
	return m
}

// handleProfilePut：`keep=1` 只校验标记并改写使用方式；否则把请求体（tar.gz；Content-Length: 0 = 空 Profile）
// 先解到 profile.new，成功后停浏览器、换入目录、写标记、dirty=false。失败时原目录与标记不动。
func (d *desktop) handleProfilePut(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	id, name := q.Get("id"), q.Get("name")
	if !profileIDRe.MatchString(id) {
		writeErr(w, badRequest("invalid id=%q", id))
		return
	}
	mode, err := profileModeQuery(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	keep, err := boolQuery(req, "keep", false)
	if err != nil {
		writeErr(w, err)
		return
	}
	orbID := d.r.cfg.OrbID
	d.opMu.Lock()
	defer d.opMu.Unlock()

	if keep {
		d.mu.Lock()
		m := d.marker
		d.mu.Unlock()
		if m == nil || m.OrbID != orbID || m.ProfileID != id {
			writeErr(w, errf(http.StatusConflict, "profile_mismatch", "local profile does not match %s", id))
			return
		}
		nm := withMode(*m, mode)
		if name != "" {
			nm.Name = name
		}
		var dirty *bool
		switch {
		case mode == apigen.BrowserProfileAccessReadonly:
			dirty = new(false)
		case m.access() == apigen.BrowserProfileAccessReadonly:
			// 只读转回独占且沿用本地目录：运行中的浏览器之后的改动要能存回去。
			dirty = new(d.browserPid() != 0)
		}
		if err := d.commitMarker(nm, dirty); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	newDir := d.profileNewDir()
	if err := os.RemoveAll(newDir); err != nil {
		writeErr(w, err)
		return
	}
	if req.ContentLength == 0 {
		err = os.MkdirAll(newDir, 0o700)
	} else {
		err = extractProfileArchive(http.MaxBytesReader(w, req.Body, maxProfileArchiveBytes), newDir)
	}
	if err != nil {
		_ = os.RemoveAll(newDir)
		writeErr(w, archiveErr(err))
		return
	}
	d.stopBrowserLocked(browserStopGrace)
	// 先删标记再换目录：中途失败时 Profile 处于「未定」，控制面下次同步会重新装载。
	if err := d.removeMarker(); err != nil {
		_ = os.RemoveAll(newDir)
		writeErr(w, err)
		return
	}
	if err := os.RemoveAll(d.profileDir()); err != nil {
		_ = os.RemoveAll(newDir)
		writeErr(w, err)
		return
	}
	if err := os.Rename(newDir, d.profileDir()); err != nil {
		_ = os.RemoveAll(newDir)
		writeErr(w, err)
		return
	}
	if err := d.commitMarker(withMode(profileMarker{OrbID: orbID, ProfileID: id, Name: name}, mode), new(false)); err != nil {
		writeErr(w, err)
		return
	}
	d.log.Info("browser profile loaded", "profile", id, "mode", mode)
	w.WriteHeader(http.StatusNoContent)
}

// archiveErr 把装载错误映射为本机 API 错误：too_large / no_space 原样，格式与路径问题 → invalid_archive。
func archiveErr(err error) error {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	switch {
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return errf(http.StatusInsufficientStorage, "no_space", "%v", err)
	case errors.Is(err, syscall.ENOTDIR), errors.Is(err, syscall.EISDIR), errors.Is(err, fs.ErrExist):
		return invalidArchive("%v", err)
	}
	return err
}

// handleProfileDelete「定为不用 Profile」：标记已是 {本 orb, ""} 时什么都不动（不带 Profile 的 orb 每次唤醒都会
// 收到它，本地的临时浏览器数据因此跨冻结保留）；否则停浏览器、删除 profile/、写标记、dirty=false。
func (d *desktop) handleProfileDelete(w http.ResponseWriter, _ *http.Request) {
	orbID := d.r.cfg.OrbID
	d.opMu.Lock()
	defer d.opMu.Unlock()
	d.mu.Lock()
	m := d.marker
	d.mu.Unlock()
	if m != nil && m.OrbID == orbID && m.ProfileID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	d.stopBrowserLocked(browserStopGrace)
	if err := d.removeMarker(); err != nil {
		writeErr(w, err)
		return
	}
	if err := os.RemoveAll(d.profileDir()); err != nil {
		writeErr(w, err)
		return
	}
	if err := d.commitMarker(profileMarker{OrbID: orbID}, new(false)); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleProfileExport 流式输出 Profile 归档。closed：先停浏览器，成功后 dirty=false；live：浏览器在运行时
// 对其进程组 SIGSTOP，归档结束（无论成败）SIGCONT，dirty 不变。中途出错直接断开（分块编码没有终止块）。
func (d *desktop) handleProfileExport(w http.ResponseWriter, req *http.Request) {
	closed := false
	switch v := req.URL.Query().Get("mode"); v {
	case "live":
	case "closed":
		closed = true
	default:
		writeErr(w, badRequest("invalid mode=%q (want live or closed)", v))
		return
	}
	d.opMu.Lock()
	defer d.opMu.Unlock()
	d.mu.Lock()
	m := d.marker
	d.mu.Unlock()
	switch {
	case m == nil || m.ProfileID == "":
		writeErr(w, errf(http.StatusConflict, "no_profile", "this orb has no browser profile"))
		return
	case m.access() == apigen.BrowserProfileAccessReadonly:
		writeErr(w, errf(http.StatusConflict, "profile_readonly", "the browser profile is a read-only copy"))
		return
	}
	if closed {
		d.stopBrowserLocked(browserStopGrace)
	} else if pid := d.browserPid(); pid != 0 {
		_ = syscall.Kill(-pid, syscall.SIGSTOP)
		defer func() { _ = syscall.Kill(-pid, syscall.SIGCONT) }()
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("X-Oryn-Profile-Id", m.ProfileID)
	w.WriteHeader(http.StatusOK)
	if err := writeProfileArchive(w, d.profileDir()); err != nil {
		d.log.Warn("browser profile export failed", "profile", m.ProfileID, "err", err)
		panic(http.ErrAbortHandler)
	}
	if closed {
		d.mu.Lock()
		d.dirty = false
		d.bumpLocked()
		d.unlockAndEmit()
	}
}

// ---- orbd.sock ----

func (r *Runtime) handleDesktopSock(ctx context.Context, method string) (any, error) {
	d := r.desk
	if d == nil {
		return nil, orbsock.Errorf(orbsock.CodeNotSupported, "the desktop is not enabled on this orb")
	}
	switch method {
	case orbsock.MethodDesktopBrowserStart:
		sctx, cancel := context.WithTimeout(ctx, browserStartWait)
		err := d.startBrowser(sctx)
		cancel()
		switch {
		case errors.Is(err, errNotReady):
			return nil, orbsock.Errorf(orbsock.CodeNotReady, "the desktop display or browser profile is not ready yet")
		case err != nil:
			return nil, orbsock.Errorf(orbsock.CodeInternal, "start browser: %v", err)
		}
	case orbsock.MethodDesktopBrowserStop:
		d.stopBrowser(browserStopGrace)
	}
	return d.result(), nil
}

// result 是 desktop.* sock 方法的结果。
func (d *desktop) result() orbsock.DesktopResult {
	d.mu.Lock()
	st := d.snapshotLocked()
	name := ""
	if d.marker != nil && d.marker.ProfileID != "" {
		name = d.marker.Name
		if name == "" {
			name = d.marker.ProfileID
		}
	}
	d.mu.Unlock()
	return orbsock.DesktopResult{State: st, Display: desktopDisplay, CDPURL: "http://" + d.cdpAddr(), ProfileName: name}
}
