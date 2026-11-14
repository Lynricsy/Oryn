package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// M9：orb 内 `oryn thread …` / `oryn schedule …` 经 orbd.sock 转成 ctl 请求（v1 4.1）。
// 参数与结果的 JSON 与 ctl 模型相同，orbd 只做转发；亲属规则、限额与审计都在控制面。

const (
	// threadWaitDefault / threadWaitMax 是 thread.wait 的缺省与最大等待时长。
	threadWaitDefault = 1800 * time.Second
	threadWaitMax     = 86400 * time.Second
	// ctlForwardTimeout 是单次转发的 ctl 请求上限。
	ctlForwardTimeout = 30 * time.Second
)

// sockToCtl 把 orbd.sock 的方法名映射到 ctl 方法与其参数类型的解码器。
var sockToCtl = map[string]struct {
	method apigen.LinkCtlMethod
	decode func(json.RawMessage) (any, error)
}{
	orbsock.MethodThreadNew:      {apigen.MethodThreadCreate, decodeAs[apigen.LinkCtlThreadCreateParams]},
	orbsock.MethodThreadSend:     {apigen.MethodThreadSend, decodeAs[apigen.LinkCtlThreadSendParams]},
	orbsock.MethodThreadGet:      {apigen.MethodThreadGet, decodeAs[apigen.LinkCtlThreadGetParams]},
	orbsock.MethodScheduleGet:    {apigen.MethodScheduleGet, decodeAs[apigen.LinkCtlScheduleParams]},
	orbsock.MethodScheduleSet:    {apigen.MethodSchedulePut, decodeAs[apigen.LinkCtlSchedulePutParams]},
	orbsock.MethodScheduleDelete: {apigen.MethodScheduleDelete, decodeAs[apigen.LinkCtlScheduleParams]},
	orbsock.MethodScheduleEnable: {apigen.MethodScheduleEnable, decodeAs[apigen.LinkCtlScheduleEnableParams]},
}

func decodeAs[T any](raw json.RawMessage) (any, error) {
	var v T
	if err := decodeSock(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// handleThreadSock 处理线程与定时的八个方法；ok=false 表示不是这类方法。
func (r *Runtime) handleThreadSock(ctx context.Context, method string, params json.RawMessage) (any, bool, error) {
	if method == orbsock.MethodThreadWait {
		var p orbsock.ThreadWaitParams
		if err := decodeSock(params, &p); err != nil {
			return nil, true, err
		}
		res, err := r.threadWait(ctx, p)
		return res, true, err
	}
	m, ok := sockToCtl[method]
	if !ok {
		return nil, false, nil
	}
	p, err := m.decode(params)
	if err != nil {
		return nil, true, err
	}
	res, err := r.ctlForward(ctx, m.method, p)
	return res, true, err
}

// ctlForward 经当前 link 发一次 ctl 请求；控制面的 *link.CtlError 原样映射为控制口错误。
func (r *Runtime) ctlForward(ctx context.Context, method apigen.LinkCtlMethod, params any) (json.RawMessage, error) {
	c := r.current()
	if c == nil {
		return nil, orbsock.Errorf(orbsock.CodeOffline, "orbd is not connected to the control plane")
	}
	cctx, cancel := context.WithTimeout(ctx, ctlForwardTimeout)
	defer cancel()
	var res json.RawMessage
	if err := c.ctl.Call(cctx, string(method), params, &res); err != nil {
		var ce *link.CtlError
		if errors.As(err, &ce) {
			return nil, &orbsock.Error{Code: ce.Code, Message: ce.Message}
		}
		return nil, orbsock.Errorf(orbsock.CodeOffline, "%s: %v", method, err)
	}
	if len(res) == 0 {
		res = json.RawMessage("{}")
	}
	return res, nil
}

// threadWait 每 ThreadPollInterval 调一次 thread.get，目标静止即返回其信息；到时回 timeout，
// 调用方断开（ctx 取消）即停。link 暂时断开不算失败，继续等到超时。
func (r *Runtime) threadWait(ctx context.Context, p orbsock.ThreadWaitParams) (apigen.LinkCtlThreadInfo, error) {
	timeout := threadWaitDefault
	if p.TimeoutSeconds > 0 {
		timeout = min(time.Duration(p.TimeoutSeconds)*time.Second, threadWaitMax)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	get := apigen.LinkCtlThreadGetParams{ThreadID: p.ThreadID, Target: p.Target}
	for {
		raw, err := r.ctlForward(ctx, apigen.MethodThreadGet, get)
		var se *orbsock.Error
		switch {
		case err == nil:
			var info apigen.LinkCtlThreadInfo
			if err := json.Unmarshal(raw, &info); err != nil {
				return info, orbsock.Errorf(orbsock.CodeInternal, "thread.get: %v", err)
			}
			if info.Settled {
				return info, nil
			}
		case errors.As(err, &se) && se.Code == orbsock.CodeOffline:
			if ctx.Err() != nil {
				return apigen.LinkCtlThreadInfo{}, ctx.Err()
			}
		default:
			return apigen.LinkCtlThreadInfo{}, err
		}
		t := time.NewTimer(r.cfg.ThreadPollInterval)
		select {
		case <-t.C:
		case <-deadline.C:
			t.Stop()
			return apigen.LinkCtlThreadInfo{}, orbsock.Errorf(orbsock.CodeTimeout, "thread %s did not settle within %s", p.Target, timeout)
		case <-ctx.Done():
			t.Stop()
			return apigen.LinkCtlThreadInfo{}, ctx.Err()
		}
	}
}
