package orbdrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// M2.5：setup 运行结果上报、本地控制口（`oryn orb setup …`）与 setup 现状文件。

// maxPendingResults 是断连期间暂存的 setup.result 上限（超出丢最旧的）。
const maxPendingResults = 32

// ---- setup 互斥：自动 setup / resume（含后台 resume）与手动重跑同一时刻只有一个 ----

func (r *Runtime) acquireSetup(ctx context.Context) error {
	select {
	case r.setupSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) tryAcquireSetup() bool {
	select {
	case r.setupSem <- struct{}{}:
		return true
	default:
		return false
	}
}

// releaseSetup 释放 setup 互斥；此后已完成的自动运行不再接受合并（见 runSetup）。
func (r *Runtime) releaseSetup() {
	r.autoMu.Lock()
	r.semHolder = nil
	r.autoMu.Unlock()
	<-r.setupSem
}

func cloneScripts(s apigen.LinkCtlSetupScripts) apigen.LinkCtlSetupScripts {
	cp := func(p *string) *string {
		if p == nil {
			return nil
		}
		return new(*p)
	}
	return apigen.LinkCtlSetupScripts{Setup: cp(s.Setup), Resume: cp(s.Resume)}
}

// ---- 结果上报与现状文件 ----

func (r *Runtime) statusPath() string { return filepath.Join(r.home, ".oryn", "setup-status.json") }

// loadSetupStatus 读取上次 orbd 进程留下的现状（orbd 重启后 `oryn orb setup status` 仍有内容）。
func (r *Runtime) loadSetupStatus() {
	b, err := os.ReadFile(r.statusPath())
	if err != nil {
		return
	}
	var st orbsock.Status
	if err := json.Unmarshal(b, &st); err == nil {
		r.status = st
	}
}

// setupStatus 返回最近一次各阶段的结果。
func (r *Runtime) setupStatus() orbsock.Status {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	return r.status
}

// publishResult 记录一次 setup / resume 结果：更新内存现状、原子重写 setup-status.json、发 setup.result。
func (r *Runtime) publishResult(res apigen.LinkCtlSetupResultParams) {
	r.statusMu.Lock()
	if res.Phase == apigen.SetupPhaseSetup {
		r.status.Setup = new(res)
	} else {
		r.status.Resume = new(res)
	}
	b, _ := json.MarshalIndent(r.status, "", "  ")
	if err := writeFileAtomic(r.statusPath(), append(b, '\n'), 0o644); err != nil {
		r.log.Warn("write setup status", "err", err)
	}
	r.statusMu.Unlock()

	r.resultsMu.Lock()
	defer r.resultsMu.Unlock()
	r.pendingResults = append(r.pendingResults, res)
	if n := len(r.pendingResults) - maxPendingResults; n > 0 {
		r.pendingResults = r.pendingResults[n:]
	}
	if c := r.current(); c != nil {
		r.flushResultsLocked(c)
	}
}

// flushResultsLocked 按序发出暂存的 setup.result（断连期间结束的运行在重连后补发）；调用方持有 resultsMu。
func (r *Runtime) flushResultsLocked(c *conn) {
	for len(r.pendingResults) > 0 {
		if err := c.ctl.Notify(string(apigen.MethodSetupResult), r.pendingResults[0]); err != nil {
			r.log.Debug("setup.result notify failed; will retry after reconnect", "err", err)
			return
		}
		r.pendingResults = r.pendingResults[1:]
	}
}

// ---- 本地控制口 ----

// serveSocket 在 $HOME/.oryn/orbd.sock 上服务到 ctx 结束；监听失败只记日志（不影响 orbd 其它功能）。
func (r *Runtime) serveSocket(ctx context.Context) {
	path := orbsock.Path(r.home)
	ln, err := orbsock.Listen(path)
	if err != nil {
		r.log.Warn("local control socket unavailable", "path", path, "err", err)
		return
	}
	orbsock.Serve(ctx, ln, r.handleSock)
	_ = os.Remove(path)
}

func (r *Runtime) handleSock(ctx context.Context, method string, params json.RawMessage, out *orbsock.Responder) (any, error) {
	switch method {
	case orbsock.MethodSetupRerun:
		var p orbsock.RerunParams
		if err := decodeSock(params, &p); err != nil {
			return nil, err
		}
		return r.rerun(ctx, p.Resume, out)
	case orbsock.MethodSetupStatus:
		return r.setupStatus(), nil
	case orbsock.MethodProjectScriptsGet:
		return r.projectScripts(ctx, apigen.MethodProjectScriptsGet, apigen.LinkCtlProjectScriptsGetParams{})
	case orbsock.MethodProjectScriptsSet:
		var p apigen.LinkCtlProjectScriptsSetParams
		if err := decodeSock(params, &p); err != nil {
			return nil, err
		}
		if p.Setup == nil && p.Resume == nil {
			return nil, orbsock.Errorf(orbsock.CodeInvalidParams, "nothing to set: give setup and/or resume")
		}
		return r.projectScripts(ctx, apigen.MethodProjectScriptsSet, p)
	case orbsock.MethodDesktopStatus, orbsock.MethodDesktopBrowserStart, orbsock.MethodDesktopBrowserStop:
		return r.handleDesktopSock(ctx, method)
	default:
		if res, ok, err := r.handleServiceSock(ctx, method, params, out); ok {
			return res, err
		}
		return nil, orbsock.Errorf(orbsock.CodeMethodNotFound, "unknown method %q", method)
	}
}

func decodeSock(params json.RawMessage, v any) error {
	if len(params) == 0 {
		return nil
	}
	if err := json.Unmarshal(params, v); err != nil {
		return orbsock.Errorf(orbsock.CodeInvalidParams, "%v", err)
	}
	return nil
}

// rerun 手动重跑 setup（忽略标记、不 clone，成功时重写标记）或 resume（前台等到结束），
// 使用最近一次 setup.run / resume.run 的参数；输出同时写日志文件、setup.log 通知与调用方。
func (r *Runtime) rerun(ctx context.Context, resume bool, out *orbsock.Responder) (apigen.LinkCtlSetupResultParams, error) {
	if !r.tryAcquireSetup() {
		return apigen.LinkCtlSetupResultParams{}, orbsock.Errorf(orbsock.CodeBusy, "another setup or resume run is in progress; try again when it finishes")
	}
	r.envMu.Lock()
	have, scripts := r.haveParams, cloneScripts(r.lastScripts)
	r.envMu.Unlock()
	if !have {
		r.releaseSetup()
		return apigen.LinkCtlSetupResultParams{}, orbsock.Errorf(orbsock.CodeNotReady, "orbd has not received setup parameters from the control plane yet")
	}
	workdir := r.currentWorkdir()
	env := r.baseEnv()
	maps.Copy(env, r.setupEnvSnapshot())
	tee := func(stream apigen.LinkCtlSetupLogParamsStream, data []byte) { out.Log(string(stream), string(data)) }

	if resume {
		return r.runResume(ctx, nil, workdir, env, scripts.Resume, apigen.SetupTriggerManual, tee, true), nil
	}

	defer r.releaseSetup()
	start := time.Now()
	res := apigen.LinkCtlSetupResultParams{
		Phase: apigen.SetupPhaseSetup, Trigger: apigen.SetupTriggerManual, SetupRan: true,
		StartedAt: start.UnixMilli(), HasProjectScript: nonEmpty(scripts.Setup),
		HasRepoScript: repoScript(workdir, "setup") != "",
	}
	lg, closeLog, err := r.openLogger("setup.log", tee)
	if err == nil {
		defer closeLog()
		lg.system(apigen.Setup, "manual setup rerun in "+workdir)
		err = r.setupScripts(ctx, lg, workdir, env, scripts.Setup)
	}
	res.Ok = err == nil
	if err != nil {
		res.Error = new(err.Error())
	}
	res.DurationMs = time.Since(start).Milliseconds()
	r.publishResult(res)
	return res, nil
}

// projectScripts 经 ctl 读写本 orb 所属项目的项目脚本，并让之后的手动重跑用上最新内容。
func (r *Runtime) projectScripts(ctx context.Context, method apigen.LinkCtlMethod, params any) (apigen.LinkCtlProjectScripts, error) {
	var res apigen.LinkCtlProjectScripts
	c := r.current()
	if c == nil {
		return res, orbsock.Errorf(orbsock.CodeOffline, "orbd is not connected to the control plane")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.ctl.Call(cctx, string(method), params, &res); err != nil {
		var ce *link.CtlError
		if errors.As(err, &ce) {
			return res, &orbsock.Error{Code: ce.Code, Message: ce.Message}
		}
		return res, orbsock.Errorf(orbsock.CodeOffline, "%s: %v", method, err)
	}
	r.envMu.Lock()
	r.lastScripts = cloneScripts(apigen.LinkCtlSetupScripts{Setup: res.Setup, Resume: res.Resume})
	r.envMu.Unlock()
	return res, nil
}

// ---- agent 环境说明（扩展）与 oryn-setup 技能 ----

func (r *Runtime) extensionPath() string {
	return filepath.Join(r.home, ".oryn", "core", "extensions", "oryn-orb.ts")
}

func (r *Runtime) skillPath() string {
	return filepath.Join(r.home, ".agents", "skills", "oryn-setup", "SKILL.md")
}

// writeAgentAssets 写出内嵌的扩展与技能（每次启动覆盖，保持与 orbd 版本一致）；桌面启用时另写给 omp 的
// 设置覆盖层（地址取 CDP 前门的实际地址）与 openbox 配置，未启用时删除残留的覆盖层。
func (r *Runtime) writeAgentAssets() error {
	files := map[string][]byte{r.extensionPath(): orbExtension, r.skillPath(): setupSkill}
	if d := r.desk; d != nil {
		files[r.desktopOverlayPath()] = desktopOverlay(d.cdpAddr())
		files[d.rcPath()] = openboxRC
	} else if err := os.Remove(r.desktopOverlayPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", r.desktopOverlayPath(), err)
	}
	for path, data := range files {
		if err := writeFileAtomic(path, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}
