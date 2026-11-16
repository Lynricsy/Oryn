package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// 停机 orb 的只读文件预览（offline fs）：带 offline=1 的 roots / list / stat / file 读请求在 orb 处于 stopped 时
// 不唤醒 orb，而是经 provider 文件中继（jobs.FSRelay）由 provider 直接读取停机容器；其它状态与不带参数完全相同。
// provider 的回应与 orbd 一样视为不可信：状态码白名单、响应头白名单、JSON 体限长、文件字节一律以附件返回。

// offlineFsStatuses 是 provider 回应允许的状态码；其余 → 502 orbd_bad_response。
var offlineFsStatuses = map[int]bool{
	http.StatusOK: true, http.StatusNoContent: true, http.StatusPartialContent: true,
	http.StatusBadRequest: true, http.StatusForbidden: true, http.StatusNotFound: true, http.StatusConflict: true,
	http.StatusRequestedRangeNotSatisfiable: true, http.StatusInternalServerError: true, http.StatusServiceUnavailable: true,
}

// offlineFsResponseHeaders 是从 provider 回应中放行的头（Retry-After 仅随 503 放行）。
var offlineFsResponseHeaders = []string{"Content-Length", "Content-Range", "Etag", "Accept-Ranges"}

// offlineDefaultWorkdirs 是 roots 在线程工作目录之后的工作目录候选。
var offlineDefaultWorkdirs = []string{"/home/user/workspace/repo", "/home/user/workspace"}

func offlineFlag(p *apigen.OrbOfflineQuery) bool { return p != nil && *p }

// serveOfflineFs 处理带 offline=1 的 fs 读请求（op 为 list / stat / read 时 fsPath 是请求路径）。
// orb 处于 stopped 时经文件中继应答并返回 true（响应已写出）；其它情况返回 false，由调用方照常 proxyOrb。
// 不计用户交互、不唤醒。
func (s *Server) serveOfflineFs(w http.ResponseWriter, r *http.Request, orbID string, op apigen.ProviderFsRequestOp, fsPath string) bool {
	svc, ok := s.orbsSvc(w)
	if !ok {
		return true
	}
	ctx := r.Context()
	o, err := svc.Get(ctx, orbID)
	if err != nil || o.State != orbs.StateStopped {
		// 不存在、读库失败或未停机：与不带 offline 完全相同
		return false
	}
	wd, ok := s.threadWorkdir(w, r, orbID, r.URL.Query().Get("thread"))
	if !ok {
		return true
	}
	if orbs.IsRunner(o) {
		WriteError(w, http.StatusConflict, "offline_unsupported", "runner 不支持停机预览")
		return true
	}
	p, err := s.Store.Q.GetProvider(ctx, o.ProviderID)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusConflict, "offline_unsupported", "orb 的 provider 不存在")
		return true
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return true
	}
	var caps apigen.ProviderCapabilities
	if json.Unmarshal([]byte(p.Capabilities), &caps) != nil || caps.OfflineFs == nil || !*caps.OfflineFs {
		WriteError(w, http.StatusConflict, "offline_unsupported", "orb 的 provider 不支持停机预览")
		return true
	}
	// 在线 = 未停用且有实例持有未过期的租约（同 providerJSON）
	if p.DisabledAt != nil || p.InstanceID == nil || p.LeaseUntil == nil || *p.LeaseUntil <= time.Now().UnixMilli() {
		WriteError(w, http.StatusServiceUnavailable, "provider_unavailable", "orb 的 provider 不在线")
		return true
	}

	req := apigen.ProviderFsRequest{OrbID: orbID, Op: op}
	if op == apigen.Roots {
		req.Workdirs = make([]string, 0, len(offlineDefaultWorkdirs)+1)
		if wd != "" {
			req.Workdirs = append(req.Workdirs, wd)
		}
		req.Workdirs = append(req.Workdirs, offlineDefaultWorkdirs...)
	} else {
		req.Path = &fsPath
	}
	if v := r.Header.Get("Range"); op == apigen.Read && v != "" {
		req.Range = &v
	}
	rep, err := svc.Jobs().FSRelay().Do(ctx, o.ProviderID, req)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		// 客户端已离开：无人接收响应
		return true
	case errors.Is(err, jobs.ErrProviderUnavailable):
		WriteError(w, http.StatusServiceUnavailable, "provider_unavailable", "provider 未认领文件请求")
		return true
	case errors.Is(err, jobs.ErrFSReplyTimeout):
		slog.Warn("provider 未按时回应停机预览请求", "orb", orbID, "op", op)
		WriteError(w, http.StatusGatewayTimeout, "orbd_timeout", "provider 未按时回应")
		return true
	default:
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return true
	}
	defer rep.Done()
	writeOfflineFsReply(w, r, rep, orbID, op == apigen.Read, path.Base(fsPath))
	return true
}

// writeOfflineFsReply 按在线代理的响应头规则把 provider 的回应写给客户端。raw：GET fs/file（成功时以附件返回 filename）。
func writeOfflineFsReply(w http.ResponseWriter, r *http.Request, rep *jobs.FSReply, orbID string, raw bool, filename string) {
	status := rep.Status
	ok := status >= 200 && status < 300
	jsonBody := !raw || !ok
	length := int64(-1)
	if v := rep.Header.Get("Content-Length"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			length = n
		}
	}
	if !offlineFsStatuses[status] || (jsonBody && length > maxOrbJSONResponse) {
		slog.Warn("provider 停机预览回应异常", "orb", orbID, "status", status, "bytes", length)
		WriteError(w, http.StatusBadGateway, "orbd_bad_response", "provider 返回了不符合约定的响应")
		return
	}
	hdr := w.Header()
	if status != http.StatusNoContent {
		for _, k := range offlineFsResponseHeaders {
			if v := rep.Header.Values(k); len(v) > 0 {
				hdr[k] = v
			}
		}
	}
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Cache-Control", "no-store")
	switch {
	case raw && ok:
		hdr.Set("Content-Type", rawContentType(filename))
		hdr.Set("Content-Disposition", attachments.Disposition(filename))
		hdr.Set("Content-Security-Policy", "sandbox")
	case status != http.StatusNoContent:
		hdr.Set("Content-Type", "application/json")
	}
	if v := rep.Header.Get("Retry-After"); status == http.StatusServiceUnavailable && v != "" {
		hdr.Set("Retry-After", v)
	}
	w.WriteHeader(status)
	if status == http.StatusNoContent {
		return
	}
	body := rep.Body
	if jsonBody {
		// 未声明长度的 JSON / 错误体同样限长：超出即中止响应
		body = &cappedBody{ReadCloser: io.NopCloser(body), left: maxOrbJSONResponse}
	}
	if _, err := io.Copy(w, body); err != nil && r.Context().Err() == nil {
		// 响应头已发出：中止连接，客户端看到的是不完整的响应而不是被截断的「成功」
		slog.Debug("停机预览响应中断", "orb", orbID, "err", err)
		panic(http.ErrAbortHandler)
	}
}
