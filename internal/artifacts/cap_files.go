package artifacts

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// files 能力（M14 步骤 7.4）：只读地列出与读取「创建这个 artifact 的线程的工作目录」。需要用户同意（consent.go），
// 最新版本必须声明 files，声明了它的页面用严格 CSP（policy.go）。没有任何写操作。参数是位置参数：
//   - list(path?) → [{name, path, type: "file" | "folder", size}]：文件夹在前、按名排序，最多 MaxProjectList 项；
//     path 缺省（或空串）为工作目录本身。只列出按路径规则读得到的项（不含 .git）。
//   - read(path, {as: "text" | "bytes"}?) → 文本文件的字符串（不是合法 UTF-8 时 invalid）；as: "bytes" 时是 base64，
//     页面运行时把它转成 Uint8Array。
//
// 文件经 orb 本机 API 的 /fs/list 与 /fs/file 取（OrbFS.Do），路径 = 工作目录 + 相对路径，并且总是带
// confine=<工作目录>：orbd 按展开符号链接后的真实路径判定，仓库里指到工作目录之外或指进 .git 的链接都读不到。
// 相对路径先在控制面按 projectPathProblem 校验。
//
// 可用条件（不满足时 use 回 null、call 回 unsupported）：artifact 有创建线程且是工作线程、线程有 orb、
// 那台 orb 声明了 fs 特性并且 EnsureUsable（会唤醒）在 filesWake 内成功。

// files 能力的上限。
const (
	// MaxProjectFileBytes 是 read 的单文件上限。
	MaxProjectFileBytes = 20 << 20
	// MaxProjectPathChars 是相对路径的字符数上限。
	MaxProjectPathChars = 4096
	// MaxProjectList 是 list 一次返回的项数上限（与 orbd 的目录列表上限相同）。
	MaxProjectList = 5000
	// MaxProjectReads 是每个 artifact 同时进行的 list / read 上限。
	MaxProjectReads = 4
)

const (
	// filesWake 是可用性检查等 orb 醒来的上限。
	filesWake = 25 * time.Second
	// filesListTimeout / filesReadTimeout 是一次 orbd 调用（含响应体）的截止时间。
	filesListTimeout = 45 * time.Second
	filesReadTimeout = 2 * time.Minute
	// maxFilesListBody / maxFilesErrBody 是 orbd 的目录列表与错误响应体的字节上限。
	maxFilesListBody = 16 << 20
	maxFilesErrBody  = 64 << 10
	// confinedHeader 是 orbd 认得 confine 时在应答里回的头（orbdrt.ConfinedHeader）。
	confinedHeader = "X-Oryn-Confined"
)

// OrbFS 是 files 能力访问 orb 本机 API 的出口（*orbs.Service 实现）。
type OrbFS interface {
	EnsureUsable(ctx context.Context, orbID, feature string, wake bool, wait time.Duration) error
	Do(ctx context.Context, orbID string, req *http.Request) (*http.Response, error)
}

// filesSet 是 files 能力的进程内状态（零值可用）。
type filesSet struct {
	mu   sync.Mutex
	orbs OrbFS
	// busy：artifact id → 进行中的 list / read 数。
	busy map[string]int
}

// AttachOrbFS 接上 orb 本机 API 的出口；不接时 files 能力一律不可用。须在开始服务之前调用。
func (s *Service) AttachOrbFS(o OrbFS) {
	s.files.mu.Lock()
	s.files.orbs = o
	s.files.mu.Unlock()
}

func (s *Service) orbFS() OrbFS {
	s.files.mu.Lock()
	defer s.files.mu.Unlock()
	return s.files.orbs
}

// filesSlot 占一个 artifact 的读取名额；满了回 rate_limited。成功时返回释放函数。
func (s *Service) filesSlot(id string) (func(), error) {
	s.files.mu.Lock()
	defer s.files.mu.Unlock()
	if s.files.busy[id] >= MaxProjectReads {
		return nil, opErr(CodeRateLimited, "同时读取项目文件的调用太多了（每个 artifact 最多 %d 个），等前面的完成再试。", MaxProjectReads)
	}
	if s.files.busy == nil {
		s.files.busy = map[string]int{}
	}
	s.files.busy[id]++
	return func() {
		s.files.mu.Lock()
		defer s.files.mu.Unlock()
		if s.files.busy[id]--; s.files.busy[id] <= 0 {
			delete(s.files.busy, id)
		}
	}, nil
}

// filesTarget 是 files 能力要访问的 orb 与线程的工作目录（cwd 为 nil 时由 orbd 的当前工作目录决定）。
type filesTarget struct {
	orbID string
	cwd   *string
}

// filesTargetOf 找 artifact 的创建线程所在的 orb；没有创建线程、不是工作线程或线程没有 orb 时 ok 为假。
func (s *Service) filesTargetOf(ctx context.Context, a Artifact) (filesTarget, bool, error) {
	if a.ThreadID == "" {
		return filesTarget{}, false, nil
	}
	t, err := s.st.Q.GetThread(ctx, a.ThreadID)
	if errors.Is(err, sql.ErrNoRows) {
		return filesTarget{}, false, nil
	}
	if err != nil {
		return filesTarget{}, false, err
	}
	if t.Kind != threads.KindWork || t.OrbID == nil {
		return filesTarget{}, false, nil
	}
	return filesTarget{orbID: *t.OrbID, cwd: t.Cwd}, true, nil
}

// filesCap 是 files 能力。
func (h *Host) filesCap() runtimeCap {
	return runtimeCap{
		declared:  true,
		available: h.filesAvailable,
		ops: map[string]runtimeOp{
			"list": h.filesList,
			"read": h.filesRead,
		},
	}
}

func (h *Host) filesAvailable(ctx context.Context, rc *runtimeCall) bool {
	o := h.s.orbFS()
	if o == nil {
		return false
	}
	tg, ok, err := h.s.filesTargetOf(ctx, rc.art)
	if err != nil {
		slog.Warn("artifacts: files 能力查不到创建线程", "artifact", rc.art.ID, "err", err)
		return false
	}
	if !ok {
		return false
	}
	if err := o.EnsureUsable(ctx, tg.orbID, orbs.FeatureFS, true, filesWake); err != nil {
		slog.Debug("artifacts: files 能力的 orb 不可用", "artifact", rc.art.ID, "orb", tg.orbID, "err", err)
		return false
	}
	return true
}

// filesSession 是一次 list / read 的准备：占名额、找到 orb 与工作目录。done 释放名额。
type filesSession struct {
	o  OrbFS
	tg filesTarget
	wd string
}

func (h *Host) filesBegin(ctx context.Context, a Artifact) (*filesSession, func(), error) {
	release, err := h.s.filesSlot(a.ID)
	if err != nil {
		return nil, nil, err
	}
	fs, err := h.filesOpen(ctx, a)
	if err != nil {
		release()
		return nil, nil, err
	}
	return fs, release, nil
}

// errWorkdirMissing 是 orbd 对 `/fs/roots?strict=1` 回的 404 workdir_missing：请求带的工作目录不存在。
var errWorkdirMissing = errors.New("files: workdir missing on orb")

func (h *Host) filesOpen(ctx context.Context, a Artifact) (*filesSession, error) {
	unavailable := opErr(codeUnsupported, "这个 artifact 现在读不到项目文件（创建它的线程或它的 orb 已不在）。")
	o := h.s.orbFS()
	if o == nil {
		return nil, unavailable
	}
	tg, ok, err := h.s.filesTargetOf(ctx, a)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, unavailable
	}
	fs := &filesSession{o: o, tg: tg}
	if tg.cwd != nil && path.IsAbs(*tg.cwd) {
		fs.wd = path.Clean(*tg.cwd)
	} else {
		// 没有 cwd → 取 orbd 的缺省工作目录；cwd 不是绝对路径（box 线程的 `~/…`，M23）→ 带 strict=1 请 orbd 展开，
		// 目录已不在（app 下线）时 orbd 回 404 workdir_missing：判为不可用，不退回缺省工作目录——否则 artifact 的
		// 读取范围会在 app 下线后变成整个工作区
		var q url.Values
		if tg.cwd != nil && *tg.cwd != "" {
			q = url.Values{"strict": {"1"}}
		}
		ctx, cancel := context.WithTimeout(ctx, filesListTimeout)
		defer cancel()
		res, err := fs.get(ctx, "/fs/roots", q, "")
		if errors.Is(err, errWorkdirMissing) {
			return nil, unavailable
		}
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		var roots apigen.FsRoots
		if err := json.NewDecoder(io.LimitReader(res.Body, maxFilesErrBody)).Decode(&roots); err != nil {
			return nil, fmt.Errorf("files: orb %s 的 /fs/roots 应答不合法: %w", tg.orbID, err)
		}
		fs.wd = path.Clean(roots.Workdir)
	}
	if !path.IsAbs(fs.wd) {
		return nil, unavailable
	}
	return fs, nil
}

// abs 返回相对路径 rel（已校验，空串为工作目录本身）在 orb 里的绝对路径。
func (fs *filesSession) abs(rel string) string {
	return path.Join(fs.wd, rel)
}

// get 向 orbd 本机 API 发 GET；非 200 的应答按 rel 换成页面看的错误。q 带 confine 时，200 的应答必须有
// confinedHeader，否则说明 orbd 太旧、不认 confine，应答不用。调用方关闭返回的响应体。
func (fs *filesSession) get(ctx context.Context, endpoint string, q url.Values, rel string) (*http.Response, error) {
	u := url.URL{Path: endpoint, RawQuery: q.Encode()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	orbs.SetWorkdir(req.Header, fs.tg.cwd)
	res, err := fs.o.Do(ctx, fs.tg.orbID, req)
	if err != nil {
		return nil, fmt.Errorf("files: orb %s %s: %w", fs.tg.orbID, endpoint, err)
	}
	if res.StatusCode == http.StatusOK {
		if q.Get("confine") != "" && res.Header.Get(confinedHeader) != "1" {
			_ = res.Body.Close()
			return nil, opErr(codeUnsupported, "这台 orb 上的 orbd 版本太旧，还不能安全地读项目文件；更新 orbd 后再试。")
		}
		return res, nil
	}
	defer res.Body.Close()
	var e apigen.Error
	_ = json.NewDecoder(io.LimitReader(res.Body, maxFilesErrBody)).Decode(&e)
	if e.Code == "workdir_missing" {
		// `/fs/roots?strict=1`：线程的工作目录在 orb 上已不存在（filesOpen 据此判不可用）
		return nil, errWorkdirMissing
	}
	shown := rel
	if shown == "" {
		shown = "工作目录"
	}
	switch {
	case res.StatusCode == http.StatusNotFound, res.StatusCode == http.StatusForbidden:
		// 403 是路径展开后落在 orb 的范围之外：对页面来说就是没有
		return nil, opErr(CodeNotFound, "项目里没有 %s。", shown)
	case e.Code == "is_a_directory":
		return nil, opErr(CodeInvalid, "%s 是文件夹，用 list 列出它的内容。", shown)
	case e.Code == "not_a_directory":
		return nil, opErr(CodeInvalid, "%s 不是文件夹，用 read 读取。", shown)
	case res.StatusCode == http.StatusBadRequest:
		return nil, opErr(CodeInvalid, "读不了 %s：路径不合法或不是普通文件。", shown)
	}
	return nil, fmt.Errorf("files: orb %s %s 回 %d %s: %s", fs.tg.orbID, endpoint, res.StatusCode, e.Code, e.Message)
}

// projectEntry 是 list 返回的一项。
type projectEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

func (h *Host) filesList(ctx context.Context, rc *runtimeCall) (any, error) {
	list, err := posArgs(rc.args, 0, 1)
	if err != nil {
		return nil, err
	}
	rel := ""
	if argAt(list, 0) != nil {
		if rel, err = stringArg(list, 0, "path"); err != nil {
			return nil, err
		}
	}
	if rel != "" {
		if problem := projectPathProblem(rel); problem != "" {
			return nil, opErr(CodeInvalid, "%s", problem)
		}
	}
	fs, done, err := h.filesBegin(ctx, rc.art)
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, filesListTimeout)
	defer cancel()
	res, err := fs.get(ctx, "/fs/list", url.Values{"path": {fs.abs(rel)}, "confine": {fs.wd}}, rel)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var ls apigen.FsListing
	if err := json.NewDecoder(io.LimitReader(res.Body, maxFilesListBody)).Decode(&ls); err != nil {
		return nil, fmt.Errorf("files: orb %s 的目录列表不合法: %w", fs.tg.orbID, err)
	}
	out := make([]projectEntry, 0, min(len(ls.Entries), MaxProjectList))
	for _, e := range ls.Entries {
		if len(out) == MaxProjectList {
			break
		}
		kind := projectKind(e)
		if kind == "" || strings.Contains(e.Name, "/") || segmentProblem(e.Name) != "" {
			continue
		}
		p := e.Name
		if rel != "" {
			p = rel + "/" + e.Name
		}
		if utf8.RuneCountInString(p) > MaxProjectPathChars {
			continue
		}
		size := e.Size
		if kind == "folder" {
			size = 0
		}
		out = append(out, projectEntry{Name: e.Name, Path: p, Type: kind, Size: size})
	}
	return out, nil
}

// projectKind 把 orbd 的目录项换成 file / folder；符号链接按它指向的类型，悬空的与其它类型回空串（不列出）。
func projectKind(e apigen.FsEntry) string {
	switch e.Type {
	case apigen.FsEntryTypeDir:
		return "folder"
	case apigen.FsEntryTypeFile:
		return "file"
	case apigen.FsEntryTypeSymlink:
		if e.TargetType != nil {
			switch *e.TargetType {
			case apigen.FsEntryTargetTypeDir:
				return "folder"
			case apigen.FsEntryTargetTypeFile:
				return "file"
			}
		}
	}
	return ""
}

type filesReadOpts struct {
	As string `json:"as"`
}

func (h *Host) filesRead(ctx context.Context, rc *runtimeCall) (any, error) {
	list, err := posArgs(rc.args, 1, 2)
	if err != nil {
		return nil, err
	}
	rel, err := stringArg(list, 0, "path")
	if err != nil {
		return nil, err
	}
	if rel == "" {
		return nil, opErr(CodeInvalid, "read 要给文件的路径；列出工作目录用 list()。")
	}
	if problem := projectPathProblem(rel); problem != "" {
		return nil, opErr(CodeInvalid, "%s", problem)
	}
	var opts filesReadOpts
	if err := optsArg(list, 1, &opts); err != nil {
		return nil, err
	}
	if opts.As != "" && opts.As != "text" && opts.As != "bytes" {
		return nil, opErr(CodeInvalid, `as 只能是 "text" 或 "bytes"。`)
	}
	fs, done, err := h.filesBegin(ctx, rc.art)
	if err != nil {
		return nil, err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, filesReadTimeout)
	defer cancel()
	res, err := fs.get(ctx, "/fs/file", url.Values{"path": {fs.abs(rel)}, "confine": {fs.wd}}, rel)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	tooLarge := opErr(CodeTooLarge, "%s 超过 %d MiB，读不了。", rel, MaxProjectFileBytes>>20)
	if res.ContentLength > MaxProjectFileBytes {
		return nil, tooLarge
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, MaxProjectFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("files: 读 orb %s 的 %s: %w", fs.tg.orbID, rel, err)
	}
	if len(data) > MaxProjectFileBytes {
		return nil, tooLarge
	}
	if opts.As == "bytes" {
		return base64.StdEncoding.EncodeToString(data), nil
	}
	if !utf8.Valid(data) {
		return nil, opErr(CodeInvalid, `%s 不是 UTF-8 文本；要读原始字节用 read(path, {as: "bytes"})。`, rel)
	}
	return string(data), nil
}

// projectPathProblem 检查 files 能力的相对路径，返回问题说明（空 = 合法）：相对工作目录；不含反斜杠、冒号与控制字符；
// 没有空段、. 与 ..；每段不以点或空格结尾；任何一段（不分大小写）都不是 .git；不超过 MaxProjectPathChars 个字符。
func projectPathProblem(p string) string {
	switch {
	case p == "":
		return "路径不能为空。"
	case !utf8.ValidString(p):
		return "路径不是合法的 UTF-8。"
	case utf8.RuneCountInString(p) > MaxProjectPathChars:
		return fmt.Sprintf("路径超过 %d 个字符。", MaxProjectPathChars)
	case strings.HasPrefix(p, "/"):
		return "路径要写成相对工作目录的路径，不能以 / 开头。"
	}
	for seg := range strings.SplitSeq(p, "/") {
		if problem := segmentProblem(seg); problem != "" {
			return problem
		}
	}
	return ""
}

// segmentProblem 检查路径的一段。
func segmentProblem(seg string) string {
	for _, r := range seg {
		if r == '\\' || r == ':' || unicode.IsControl(r) {
			return "路径不能含反斜杠、冒号或控制字符。"
		}
	}
	switch {
	case seg == "":
		return "路径里不能有空段（开头、结尾或连续的 /）。"
	case seg == "." || seg == "..":
		return "路径里不能有 . 或 .. 段。"
	case strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " "):
		return "路径的每一段不能以点或空格结尾。"
	case strings.EqualFold(seg, ".git"):
		return "不能读取 .git 里的内容。"
	}
	return ""
}
