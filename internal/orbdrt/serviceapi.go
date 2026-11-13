package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// 受管服务的两个入口（M4 计划 2.7 / 2.8）：orbd.sock 的 service.* 方法（orb 内 `oryn orb service …`）
// 与本机 API 的 /services/*（控制面经 fs 流转发的 REST）。

const (
	// defaultServiceLogTail / maxServiceLogTail 是 GET /services/{name}/log 的 tail（字节）缺省值与上限。
	defaultServiceLogTail = 64 << 10
	maxServiceLogTail     = 1 << 20
)

// setupParamsKnown 报告是否已收到过 setup.run / resume.run（工作区根要等它确定）。
func (r *Runtime) setupParamsKnown() bool {
	r.envMu.Lock()
	defer r.envMu.Unlock()
	return r.haveParams
}

// ---- orbd.sock ----

// sockErr 把服务操作错误映射为控制口错误（错误码相同）。
func sockErr(err error) error {
	if e, ok := isSvcError(err); ok {
		return &orbsock.Error{Code: e.code, Message: e.msg}
	}
	return err
}

// handleServiceSock 处理 service.* 方法；第二个返回值为 false 表示不是服务方法。
func (r *Runtime) handleServiceSock(ctx context.Context, method string, params json.RawMessage, out *orbsock.Responder) (any, bool, error) {
	switch method {
	case orbsock.MethodServiceList:
		return orbsock.ServiceListResult{Services: r.svc.List(), FileError: r.svc.fileError()}, true, nil
	case orbsock.MethodServiceEnsure:
		if !r.setupParamsKnown() {
			return nil, true, sockErr(errServicesNotReady)
		}
		unsub := r.svc.subscribe(func(_, line string) { out.Log(orbsock.StreamSystem, line) })
		defer unsub()
		list, fileErr := r.svc.Ensure()
		r.waitSettledAll(ctx, list)
		return orbsock.ServiceListResult{Services: r.svc.List(), FileError: fileErr}, true, nil
	case orbsock.MethodServiceStart:
		var p orbsock.ServiceStartParams
		if err := decodeSock(params, &p); err != nil {
			return nil, true, err
		}
		unsub := r.svc.subscribe(nameFilter(p.Name, out))
		defer unsub()
		o, err := r.svc.Start(p)
		if err != nil {
			return nil, true, sockErr(err)
		}
		return orbsock.ServiceResult{Service: r.settle(ctx, o)}, true, nil
	case orbsock.MethodServiceRestart:
		var p orbsock.ServiceNameParams
		if err := decodeSock(params, &p); err != nil {
			return nil, true, err
		}
		unsub := r.svc.subscribe(nameFilter(p.Name, out))
		defer unsub()
		o, err := r.svc.Restart(p.Name)
		if err != nil {
			return nil, true, sockErr(err)
		}
		return orbsock.ServiceResult{Service: r.settle(ctx, o)}, true, nil
	case orbsock.MethodServiceStop:
		var p orbsock.ServiceStopParams
		if err := decodeSock(params, &p); err != nil {
			return nil, true, err
		}
		o, err := r.svc.Stop(p.Name, p.Remove)
		if err != nil {
			return nil, true, sockErr(err)
		}
		return orbsock.ServiceResult{Service: o}, true, nil
	}
	return nil, false, nil
}

func nameFilter(name string, out *orbsock.Responder) func(string, string) {
	return func(n, line string) {
		if n == name {
			out.Log(orbsock.StreamSystem, line)
		}
	}
}

// settle 等服务状态离开 starting（上限 serviceSettleTimeout，ctx 结束即返回）。
func (r *Runtime) settle(ctx context.Context, o apigen.OrbService) apigen.OrbService {
	if o.Status != apigen.OrbServiceStatusStarting {
		return o
	}
	ctx, cancel := context.WithTimeout(ctx, serviceSettleTimeout)
	defer cancel()
	return r.svc.WaitSettled(ctx, o.Name)
}

// waitSettledAll 并行等待列表中处于 starting 的服务（共用一个 serviceSettleTimeout）。
func (r *Runtime) waitSettledAll(ctx context.Context, list []apigen.OrbService) {
	ctx, cancel := context.WithTimeout(ctx, serviceSettleTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, o := range list {
		if o.Status == apigen.OrbServiceStatusStarting {
			wg.Go(func() { r.svc.WaitSettled(ctx, o.Name) })
		}
	}
	wg.Wait()
}

// ---- 本机 API ----

// serviceAPIError 把服务操作错误映射为本机 API 错误（not_ready → 409 services_not_ready）。
func serviceAPIError(err error) error {
	e, ok := isSvcError(err)
	if !ok {
		return err
	}
	switch e.code {
	case orbsock.CodeServiceNotFound:
		return errf(http.StatusNotFound, e.code, "%s", e.msg)
	case orbsock.CodeInvalidService:
		return errf(http.StatusBadRequest, e.code, "%s", e.msg)
	case orbsock.CodeNameConflict:
		return errf(http.StatusConflict, e.code, "%s", e.msg)
	case orbsock.CodeNotReady:
		return errf(http.StatusConflict, "services_not_ready", "%s", e.msg)
	}
	return errf(http.StatusInternalServerError, e.code, "%s", e.msg)
}

// serviceName 取路径里的服务名；不合规返回 400 invalid_service。
func serviceName(req *http.Request) (string, error) {
	name := req.PathValue("name")
	if !serviceNameRe.MatchString(name) {
		return "", errf(http.StatusBadRequest, orbsock.CodeInvalidService, "invalid service name %q", name)
	}
	return name, nil
}

func (r *Runtime) handleServiceStart(w http.ResponseWriter, req *http.Request) {
	name, err := serviceName(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	o, err := r.svc.Start(orbsock.ServiceStartParams{Name: name})
	if err != nil {
		writeErr(w, serviceAPIError(err))
		return
	}
	writeJSON(w, http.StatusOK, r.settle(req.Context(), o))
}

func (r *Runtime) handleServiceStop(w http.ResponseWriter, req *http.Request) {
	name, err := serviceName(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	o, err := r.svc.Stop(name, false)
	if err != nil {
		writeErr(w, serviceAPIError(err))
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (r *Runtime) handleServiceRestart(w http.ResponseWriter, req *http.Request) {
	name, err := serviceName(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	o, err := r.svc.Restart(name)
	if err != nil {
		writeErr(w, serviceAPIError(err))
		return
	}
	writeJSON(w, http.StatusOK, r.settle(req.Context(), o))
}

// handleServiceLog 返回服务日志末尾 tail 字节（从整行开始）；没有日志时返回空体。
func (r *Runtime) handleServiceLog(w http.ResponseWriter, req *http.Request) {
	name, err := serviceName(req)
	if err != nil {
		writeErr(w, err)
		return
	}
	tail := int64(defaultServiceLogTail)
	if v := req.URL.Query().Get("tail"); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n < 0 {
			writeErr(w, badRequest("invalid tail=%q", v))
			return
		}
		tail = min(n, maxServiceLogTail)
	}
	b, err := readLogTail(filepath.Join(r.svc.logDir, name+".log"), tail)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// readLogTail 读文件末尾至多 n 字节，从整行开始（起点落在行中间时丢掉该行的残片）。
func readLogTail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() <= n {
		return io.ReadAll(f)
	}
	// 多读起点前的一个字节：它是换行时起点恰在行首。
	if _, err := f.Seek(st.Size()-n-1, io.SeekStart); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, n+1))
	if err != nil {
		return nil, err
	}
	if i := slices.Index(b, '\n'); i >= 0 {
		return b[i+1:], nil
	}
	return nil, nil
}
