package orbdrt

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 沙箱的命令执行（M30 步骤 2.4，docs/plan-m30.md C5 / C6）：只在精简模式下注册。
// POST /sandbox/exec 同步时与 POST /box/exec 同一 NDJSON 格式；detach 时作为后台作业启动并回 {jobId}。

const (
	sandboxExecMaxCommand     = 32000 // 字符
	sandboxExecDefaultTimeout = 600
	sandboxExecMaxTimeout     = 1800
	sandboxJobMaxTimeout      = 86400
	sandboxExecMaxEnv         = 64
	sandboxExecMaxEnvBytes    = 64 << 10
)

var sandboxEnvKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (r *Runtime) sandboxRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /sandbox/exec", r.handleSandboxExec)
	mux.HandleFunc("GET /sandbox/jobs", r.handleSandboxJobs)
	mux.HandleFunc("GET /sandbox/jobs/{id}/log", r.handleSandboxJobLog)
	mux.HandleFunc("POST /sandbox/jobs/{id}/kill", r.handleSandboxJobKill)
}

// validSandboxExec 按 C5 的范围校验请求，返回命令的时限（0 = 不限，只用于 detach）。
func validSandboxExec(body *apigen.OrbSandboxExecRequest) (time.Duration, error) {
	if strings.TrimSpace(body.Command) == "" {
		return 0, badRequest("command is required")
	}
	if utf8.RuneCountInString(body.Command) > sandboxExecMaxCommand {
		return 0, badRequest("command exceeds %d characters", sandboxExecMaxCommand)
	}
	if strings.ContainsRune(body.Command, 0) {
		return 0, badRequest("command contains NUL")
	}
	if len(body.Env) > sandboxExecMaxEnv {
		return 0, badRequest("env has more than %d entries", sandboxExecMaxEnv)
	}
	total := 0
	for k, v := range body.Env {
		if !sandboxEnvKey.MatchString(k) {
			return 0, badRequest("invalid env name %q", k)
		}
		if strings.ContainsRune(v, 0) {
			return 0, badRequest("env %s contains NUL", k)
		}
		total += len(k) + len(v)
	}
	if total > sandboxExecMaxEnvBytes {
		return 0, badRequest("env exceeds %d bytes", sandboxExecMaxEnvBytes)
	}
	sec := body.TimeoutSec
	if body.Detach != nil && *body.Detach {
		if sec < 0 || sec > sandboxJobMaxTimeout {
			return 0, badRequest("timeoutSec must be 0 (no limit) or 1-%d for a detached job", sandboxJobMaxTimeout)
		}
		return time.Duration(sec) * time.Second, nil
	}
	if sec == 0 {
		sec = sandboxExecDefaultTimeout
	}
	if sec < 1 || sec > sandboxExecMaxTimeout {
		return 0, badRequest("timeoutSec must be 1-%d", sandboxExecMaxTimeout)
	}
	return time.Duration(sec) * time.Second, nil
}

func (r *Runtime) handleSandboxExec(w http.ResponseWriter, req *http.Request) {
	var body apigen.OrbSandboxExecRequest
	if err := readBoxJSON(w, req, &body); err != nil {
		writeErr(w, err)
		return
	}
	timeout, err := validSandboxExec(&body)
	if err != nil {
		writeErr(w, err)
		return
	}
	dir, err := r.execDir(body.Cwd)
	if err != nil {
		writeErr(w, err)
		return
	}
	if body.Detach != nil && *body.Detach {
		id, err := r.sbxJobs.start(body.Command, dir, body.Env, timeout)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, apigen.OrbSandboxJobStart{JobID: id})
		return
	}
	r.runExecNDJSON(w, req, dir, body.Command, body.Env, timeout)
}

func (r *Runtime) handleSandboxJobs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, apigen.OrbSandboxJobList{Jobs: r.sbxJobs.list()})
}

func (r *Runtime) handleSandboxJobLog(w http.ResponseWriter, req *http.Request) {
	var (
		offset    int64
		hasOffset bool
	)
	if v := req.URL.Query().Get("offset"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, badRequest("invalid offset=%q", v))
			return
		}
		offset, hasOffset = n, true
	}
	res, err := r.sbxJobs.log(req.PathValue("id"), offset, hasOffset)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (r *Runtime) handleSandboxJobKill(w http.ResponseWriter, req *http.Request) {
	if err := r.sbxJobs.kill(req.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apigen.LinkCtlOkResult{Ok: true})
}
