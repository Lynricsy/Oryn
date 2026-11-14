package orbdrt

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// orbd 本机 API（M3 计划 3.1 / 4.1 / 5.1，docs/protocol.md「orbd 本机 API」）：控制面在 link 上打开 `fs` 流，
// 流上是 HTTP/1.1（一条流一个请求），由这里的 http.Server 服务。orb 侧对控制面而言不可信，
// 但本机 API 只服务控制面经 orb 令牌鉴权的 link，因此不再做额外鉴权；路径一律限制在 $HOME 之下。

const (
	// MaxFileBytes 是 PUT /fs/file 的单文件上限（控制面与 orbd 各校验一次）。
	MaxFileBytes = 512 << 20
	// MaxAttachmentBytes 是 PUT /attachments/file 的单文件上限。
	MaxAttachmentBytes = 100 << 20
	// localWriteTimeout 是 fs 流写方向的滚动截止时间：控制面中止下载后，写方向最多挂这么久。
	localWriteTimeout = 60 * time.Second
	// maxJSONBody 是 JSON 请求体上限。
	maxJSONBody = 1 << 20
)

// rollingConn 在每次写之前把写截止时间推到 now+d。
type rollingConn struct {
	net.Conn
	d time.Duration
}

func (c rollingConn) Write(b []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(c.d))
	return c.Conn.Write(b)
}

// localHandler 返回本机 API 的路由：文件（fs）、改动（git）、附件（attachments）、受管服务（services）、sync 与桌面（desktop）。
func (r *Runtime) localHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fs/roots", r.handleFsRoots)
	mux.HandleFunc("GET /fs/list", r.handleFsList)
	mux.HandleFunc("GET /fs/stat", r.handleFsStat)
	mux.HandleFunc("GET /fs/file", r.handleFsRead)
	mux.HandleFunc("PUT /fs/file", r.handleFsWrite)
	mux.HandleFunc("POST /fs/mkdir", r.handleFsMkdir)
	mux.HandleFunc("POST /fs/rename", r.handleFsRename)
	mux.HandleFunc("POST /fs/delete", r.handleFsDelete)
	mux.HandleFunc("GET /git/status", r.handleGitStatus)
	mux.HandleFunc("GET /git/diff", r.handleGitDiff)
	mux.HandleFunc("GET /git/commits", r.handleGitCommits)
	mux.HandleFunc("POST /git/stage", r.handleGitStage)
	mux.HandleFunc("POST /git/unstage", r.handleGitUnstage)
	mux.HandleFunc("POST /attachments/ensure", r.handleAttachEnsure)
	mux.HandleFunc("PUT /attachments/file", r.handleAttachPut)
	mux.HandleFunc("DELETE /attachments", r.handleAttachDelete)
	mux.HandleFunc("POST /services/{name}/start", r.handleServiceStart)
	mux.HandleFunc("POST /services/{name}/stop", r.handleServiceStop)
	mux.HandleFunc("POST /services/{name}/restart", r.handleServiceRestart)
	mux.HandleFunc("GET /services/{name}/log", r.handleServiceLog)
	mux.HandleFunc("POST /sync/prepare", r.handleSyncPrepare)
	mux.HandleFunc("GET /sync/bundle", r.handleSyncBundle)
	r.deskRoutes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, &apiError{status: http.StatusNotFound, code: "not_found", msg: "no such route"})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if h := r.cfg.OnLocalRequest; h != nil {
			h(req)
		}
		mux.ServeHTTP(w, req)
	})
}

// requestLogger 返回把每个请求（方法、路径、查询串、请求头）作为一行 JSON 追加到 path 的钩子。
func requestLogger(path string) func(*http.Request) {
	var mu sync.Mutex
	return func(req *http.Request) {
		line, err := json.Marshal(map[string]any{
			"method": req.Method, "path": req.URL.Path, "rawQuery": req.URL.RawQuery, "header": req.Header,
		})
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return
		}
		_, _ = f.Write(append(line, '\n'))
		_ = f.Close()
	}
}

// ---- 错误 ----

// apiError 是带 HTTP 状态与错误码的本机 API 错误（错误码表见 docs/protocol.md）。
type apiError struct {
	status int
	code   string
	msg    string
}

func (e *apiError) Error() string { return e.code + ": " + e.msg }

func errf(status int, code, format string, args ...any) *apiError {
	return &apiError{status: status, code: code, msg: fmt.Sprintf(format, args...)}
}

func badRequest(format string, args ...any) *apiError {
	return errf(http.StatusBadRequest, "invalid_request", format, args...)
}

func badPath(format string, args ...any) *apiError {
	return errf(http.StatusBadRequest, "invalid_path", format, args...)
}

// fsError 把文件系统错误映射为 apiError。
func fsError(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &mbe):
		return errf(http.StatusRequestEntityTooLarge, "too_large", "body exceeds %d bytes", mbe.Limit)
	case errors.Is(err, fs.ErrNotExist):
		return errf(http.StatusNotFound, "not_found", "%v", err)
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return errf(http.StatusInsufficientStorage, "no_space", "%v", err)
	case errors.Is(err, syscall.ENOTDIR):
		return errf(http.StatusConflict, "not_a_directory", "%v", err)
	case errors.Is(err, syscall.EISDIR):
		return errf(http.StatusConflict, "is_a_directory", "%v", err)
	case errors.Is(err, fs.ErrExist), errors.Is(err, syscall.ENOTEMPTY):
		return errf(http.StatusConflict, "exists", "%v", err)
	case errors.Is(err, fs.ErrPermission):
		return errf(http.StatusForbidden, "forbidden", "%v", err)
	default:
		return errf(http.StatusInternalServerError, "internal", "%v", err)
	}
}

func writeErr(w http.ResponseWriter, err error) {
	ae := fsError(err)
	writeJSON(w, ae.status, apigen.Error{Code: ae.code, Message: ae.msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// readJSON 解码请求体（≤1MiB）；失败返回 400 invalid_request。
func readJSON(w http.ResponseWriter, req *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxJSONBody))
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON body: %v", err)
	}
	return nil
}

// boolQuery 解析可选布尔查询参数（`1` / `true` 等，同 strconv.ParseBool）。
func boolQuery(req *http.Request, name string, def bool) (bool, error) {
	v := req.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	switch strings.ToLower(v) {
	case "1", "t", "true":
		return true, nil
	case "0", "f", "false":
		return false, nil
	}
	return false, badRequest("invalid %s=%q", name, v)
}

// ---- 路径策略 ----

// userPath 把请求里的路径（`~` / `~/…` / 绝对路径）解析为绝对路径，并要求它跟随符号链接后位于 $HOME 之下
// （403 outside_home）；allowHome 时允许 $HOME 本身（只读操作），否则 $HOME 本身返回 403 protected_path。
// 返回的是词法上的绝对路径（不展开符号链接），real 是展开后的路径。
func (r *Runtime) userPath(p string, allowHome bool) (abs, real string, err error) {
	if strings.TrimSpace(p) == "" {
		return "", "", badPath("empty path")
	}
	if strings.ContainsRune(p, 0) {
		return "", "", badPath("path contains NUL")
	}
	if p != "~" && !strings.HasPrefix(p, "~/") && !filepath.IsAbs(p) {
		return "", "", badPath("path must be absolute or start with ~/: %q", p)
	}
	abs = r.resolvePath(p)
	real, err = evalExisting(abs)
	if err != nil {
		return "", "", fsError(err)
	}
	rel, ok := r.relHome(real)
	if !ok {
		return "", "", errf(http.StatusForbidden, "outside_home", "path %q is outside $HOME", p)
	}
	if rel == "." && !allowHome {
		return "", "", errf(http.StatusForbidden, "protected_path", "$HOME itself cannot be modified")
	}
	return abs, real, nil
}

// relHome 返回 real（已展开符号链接）相对 $HOME 的路径；不在 $HOME 之下时 ok=false。
func (r *Runtime) relHome(real string) (string, bool) {
	home, err := filepath.EvalSymlinks(r.home)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(home, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// mutablePath 是变更类操作的路径检查：userPath 之外再拒绝 $HOME/.oryn/**（Oryn 自己的状态，只读）。
// 展开后的目标、词法路径与（父目录展开后的）目录项三者任一落在 .oryn 下即拒绝：delete / rename 作用于
// 目录项本身，`~/.oryn/foo -> ~/work` 这样的链接按目标判断会被放行。
func (r *Runtime) mutablePath(p string) (abs, real string, err error) {
	abs, real, err = r.userPath(p, false)
	if err != nil {
		return "", "", err
	}
	inOryn := func(rel string) bool { return rel == ".oryn" || strings.HasPrefix(rel, ".oryn/") }
	rel, _ := r.relHome(real)
	lex, lerr := filepath.Rel(r.home, abs)
	protected := inOryn(rel) || (lerr == nil && inOryn(lex))
	if entry, eerr := entryPath(abs); eerr == nil {
		if erel, ok := r.relHome(entry); ok && inOryn(erel) {
			protected = true
		}
	}
	if protected {
		return "", "", errf(http.StatusForbidden, "protected_path", "%q is managed by Oryn and read-only", p)
	}
	return abs, real, nil
}

// removablePath 是 delete / rename 源路径的检查：mutablePath 之外再拒绝当前工作目录本身及其上级目录
// （删掉或移走它们等于删掉工作目录）。
func (r *Runtime) removablePath(p string) (abs, real string, err error) {
	abs, real, err = r.mutablePath(p)
	if err != nil {
		return "", "", err
	}
	wd, werr := filepath.EvalSymlinks(r.currentWorkdir())
	if werr == nil {
		if rel, rerr := filepath.Rel(real, wd); rerr == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../"))) {
			return "", "", errf(http.StatusForbidden, "protected_path", "%q is (or contains) the working directory", p)
		}
	}
	return abs, real, nil
}

// resetDir 删除并重建目录（0700）。
func resetDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o700)
}

// tmpDir 是上传临时文件目录（与 $HOME 同一文件系统，rename 到位通常不跨设备）。
func (r *Runtime) tmpDir() string { return filepath.Join(r.home, ".oryn", "tmp") }

// receiveFile 把 body 流式写入 $HOME/.oryn/tmp 下的临时文件并 Sync；每块数据同时写给 tee（可为 nil）。
// 失败时临时文件已删除。调用方负责在 rename 失败时删除返回的临时文件。
func (r *Runtime) receiveFile(body io.Reader, prefix string, tee io.Writer) (string, error) {
	if err := os.MkdirAll(r.tmpDir(), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(r.tmpDir(), prefix+"*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	var dst io.Writer = f
	if tee != nil {
		dst = io.MultiWriter(f, tee)
	}
	_, err = io.Copy(dst, body)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}
