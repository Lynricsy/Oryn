// Package orbsock 是 orbd 本地控制口（M2.5 / M4 / M8）：`$HOME/.oryn/orbd.sock` 上的 JSON 行协议，
// orbd（服务端）与 orb 内的 `oryn orb setup …` / `oryn orb service …` / `oryn orb desktop …`（客户端）共用。
//
// 每个连接一个请求：客户端写一行 `{"method": "...", "params": {...}}`，服务端回若干行：
// 零或多条 `{"type":"log","stream":"stdout|stderr|system","data":"..."}`，
// 最后恰好一条 `{"type":"result","result":...}` 或 `{"type":"error","code":"...","message":"..."}`。
// 客户端在收到最后一条之前不得关闭连接：服务端把连接关闭视为调用方放弃（会取消正在运行的重跑）。
package orbsock

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 方法名。
const (
	MethodSetupRerun        = "setup.rerun"
	MethodSetupStatus       = "setup.status"
	MethodProjectScriptsGet = "project.scripts.get"
	MethodProjectScriptsSet = "project.scripts.set"

	// 受管服务（M4）：service.start / service.restart / service.ensure 在拉起后等待状态离开 starting
	// （上限 60s，调用方断开即返回），期间把 `[orbd] …` 状态行作为 system 日志流回。
	MethodServiceEnsure  = "service.ensure" // 无参数：按 services.yaml 确保全部服务运行
	MethodServiceStart   = "service.start"
	MethodServiceStop    = "service.stop"
	MethodServiceRestart = "service.restart"
	MethodServiceList    = "service.list"

	// 桌面（M8）：desktop.browser.start 启动浏览器并等到 DevTools 就绪（上限 30s）；桌面未启用时回 not_supported。
	MethodDesktopStatus       = "desktop.status"
	MethodDesktopBrowserStart = "desktop.browser.start"
	MethodDesktopBrowserStop  = "desktop.browser.stop"

	// 录屏（M25）：desktop.record.start 开始录制桌面（DesktopRecordStartResult）；desktop.record.stop 停止并返回
	// DesktopRecording。每台 orb 同时最多一个录制：已在录回 already_recording，显示未就绪回 not_ready，
	// 用户接管或有待答的 desktop_ask_user 时回 control_user；没在录、也没有因上限、出错或控制状态变化停下且未取走的
	// 结果时，stop 回 not_recording。
	MethodDesktopRecordStart = "desktop.record.start"
	MethodDesktopRecordStop  = "desktop.record.stop"

	// 线程与定时（M9）：参数与结果同名 ctl 方法的模型（apigen.LinkCtl*），threadId 恒为调用方线程（ORYN_THREAD_ID）。
	// thread.new → ctl thread.create（LinkCtlThreadCreateParams → LinkCtlThreadInfo）；thread.send → thread.send
	// （LinkCtlThreadSendParams → {}）；thread.get → thread.get（LinkCtlThreadGetParams → LinkCtlThreadInfo）；
	// thread.wait 由 orbd 轮询 thread.get 直到 settled（ThreadWaitParams → LinkCtlThreadInfo，超时回 CodeTimeout）；
	// schedule.get / schedule.delete（LinkCtlScheduleParams）、schedule.set → schedule.put（LinkCtlSchedulePutParams）、
	// schedule.enable（LinkCtlScheduleEnableParams），结果均为 LinkCtlScheduleResult。
	MethodThreadNew      = "thread.new"
	MethodThreadSend     = "thread.send"
	MethodThreadGet      = "thread.get"
	MethodThreadWait     = "thread.wait"
	MethodScheduleGet    = "schedule.get"
	MethodScheduleSet    = "schedule.set"
	MethodScheduleDelete = "schedule.delete"
	MethodScheduleEnable = "schedule.enable"

	// 文件库（M13）：library.put → ctl library.put（LinkCtlLibraryPutParams → AssistantFile），path 为 orb 内文件的绝对路径。
	// orbd 在转发期间允许控制面经 library.fetch 取回该路径；文件以「库内共享」存入全局助手文件库。
	MethodLibraryPut = "library.put"

	// Artifacts（M14）：artifact.call → ctl artifact.call（LinkCtlArtifactCallParams → LinkCtlArtifactCallResult），
	// threadId 恒为调用方线程。params.files[*].source 必须是 orb 内文件的绝对路径；orbd 在转发期间允许控制面经
	// library.fetch 取回这些路径。
	MethodArtifactCall = "artifact.call"

	// GitHub 工具（M15）：github.call → ctl github.call（LinkCtlGithubCallParams → LinkCtlGithubCallResult），
	// threadId 恒为调用方线程；github.prepare 由 orbd 在线程工作目录里执行门禁准备（GithubPrepareParams →
	// orbdrt.PrepareResult），通过后经 ctl github.prepare.report 上报。两者只在 orbd.sock 上，runner.sock 不提供。
	MethodGithubCall    = "github.call"
	MethodGithubPrepare = "github.prepare"

	// SSH 工具（M18）：ssh.call → ctl ssh.call（LinkCtlSshCallParams → LinkCtlSshCallResult），threadId 恒为调用方线程；
	// 控制面按调用方 orb 可见的主机执行（ssh_exec_many）。只在 orbd.sock 上，runner.sock 不提供。
	MethodSshCall = "ssh.call"

	// Boxes（M23）：box.call → ctl box.call（LinkCtlBoxCallParams → LinkCtlBoxCallResult），threadId 恒为调用方线程；
	// 由 orb 内 `oryn boxes …` 发出。只在 orbd.sock 上，runner.sock 不提供。
	MethodBoxCall = "box.call"

	// 后台研究（M26）：follow.add → ctl follow.add（LinkCtlFollowAddParams → LinkCtlFollowAddResult），threadId 恒为调用方线程；
	// 由 orb 内 `oryn follow add` 发出，控制面核对 quote 是主人在该线程里的原话。只在 orbd.sock 上，runner.sock 不提供。
	MethodFollowAdd = "follow.add"

	// Agent 沙箱（M30，docs/plan-m30.md C5）：sandbox.call（SandboxCallParams）由 orbd 在 link 上打开一条 `sandbox` 流转给
	// 控制面，op 与 params 原样放进请求行；exec 的输出作为 stdout / stderr 日志流回，结果是控制面回的 result 原文，
	// 控制面的错误码原样透传。threadId 恒为调用方线程。只在 orbd.sock 上，runner.sock 与沙箱自身不提供。
	MethodSandboxCall = "sandbox.call"

	// 设计画布（M29，docs/plan-m29.md C3 / C4）：orbd 在 `~/designs/<artifactId>/` 维护工作副本，经 ctl artifact.call
	// 的内部操作物化与封存。只在 orbd.sock 上，runner.sock 不提供。threadId 恒为调用方线程。
	// design.open（DesignOpenParams → DesignOpenResult）建立或接管本线程的工作副本并起 dev 服务；
	// design.turn（DesignTurnParams → {}）记下本回合，上一回合留下的脏副本先以旧回合封存；
	// design.seal（DesignSealParams → DesignSealResult，上限 5 分钟）构建并封存本线程的每个脏副本，构建失败回
	// build_failed（message 是 stderr 末尾 4 KiB），封存冲突原样回控制面的 conflict；
	// design.check（DesignCheckParams → DesignCheckResult，上限 90 秒）在 orb 里用 headless Chromium 对工作副本的帧跑
	// 设计探针（plan-m29 C8），可附每帧截图；
	// design.status（DesignStatusParams → DesignStatusResult）列出本线程的工作副本；
	// design.eject（DesignEjectParams → DesignEjectResult）把设计画布指定版本导出为独立 Vite 工程并解到目标空目录。
	MethodDesignOpen   = "design.open"
	MethodDesignTurn   = "design.turn"
	MethodDesignSeal   = "design.seal"
	MethodDesignCheck  = "design.check"
	MethodDesignStatus = "design.status"
	MethodDesignEject  = "design.eject"
)

// 消息类型。
const (
	TypeLog    = "log"
	TypeResult = "result"
	TypeError  = "error"
)

// 日志流。
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
	StreamSystem = "system"
)

// 错误码（控制面经 ctl 返回的错误码如 `no_project` 原样透传）。
const (
	CodeBusy           = "busy"      // 已有 setup / resume 在运行
	CodeNotReady       = "not_ready" // orbd 还没收到过 setup.run / resume.run；桌面：显示或 Profile 未就绪
	CodeOffline        = "offline"   // orbd 当前未连上控制面
	CodeNoProject      = "no_project"
	CodeInvalidParams  = "invalid_params"
	CodeMethodNotFound = "method_not_found"
	CodeInternal       = "internal"

	CodeServiceNotFound = "service_not_found" // 没有该名字的服务
	CodeInvalidService  = "invalid_service"   // 服务定义无效（或对文件服务给了 remove）
	CodeNameConflict    = "name_conflict"     // 动态服务与 services.yaml 里的服务同名

	CodeNotSupported = "not_supported" // 本 orb 没有启用桌面

	CodeAlreadyRecording = "already_recording" // 桌面已在录制
	CodeNotRecording     = "not_recording"     // 桌面没有在录制，也没有待取走的录制结果
	CodeControlUser      = "control_user"      // 用户接管了桌面或有待答的 desktop_ask_user（控制状态 user / ask），不能开始录制

	CodeTimeout = "timeout" // thread.wait 到时目标仍未静止

	CodeBuildFailed = "build_failed" // design.seal：vite build 或依赖安装失败，没有落版本
	CodeNoDesign    = "no_design"    // 本线程没有该 artifact 的工作副本（先 design.open）
)

// MaxRequestBytes 是请求行上限（项目脚本随 project.scripts.set 传入）。
const MaxRequestBytes = 4 << 20

// DesignFilesPageBytes 是内部操作 fetch_design_files 一页结果（ctl artifact.call 的整条结果 JSON，含 base64 膨胀与
// 字段开销）的上限：MaxRequestBytes 的一半。
const DesignFilesPageBytes = MaxRequestBytes / 2

// Path 返回 $HOME 下的控制口路径。
func Path(home string) string { return filepath.Join(home, ".oryn", "orbd.sock") }

// RunnerPath 返回 runner 形态的 artifact 控制口路径（M14）：只服务 artifact.call，不与同一 HOME 下 orb 形态的
// orbd.sock 相撞，也不把 thread.* / schedule.* / setup.* 暴露给 runner 上的 agent。
func RunnerPath(home string) string { return filepath.Join(home, ".oryn", "orbd", "runner.sock") }

// Request 是客户端发出的唯一一行。
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Message 是服务端回送的一行。
type Message struct {
	Type    string          `json:"type"`
	Stream  string          `json:"stream,omitempty"`
	Data    string          `json:"data,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
}

// Error 是控制口错误，同时实现 error。
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf 构造一个控制口错误。
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// RerunParams 是 setup.rerun 的参数。
type RerunParams struct {
	Resume bool `json:"resume,omitempty"`
}

// SandboxCallParams 是 sandbox.call 的参数（M30）。
type SandboxCallParams struct {
	ThreadID string          `json:"threadId"`
	Op       string          `json:"op"`
	Params   json.RawMessage `json:"params,omitempty"`
}

// Status 是 setup.status 的结果，也是 `$HOME/.oryn/setup-status.json` 的内容。
type Status struct {
	Setup  *apigen.LinkCtlSetupResultParams `json:"setup"`
	Resume *apigen.LinkCtlSetupResultParams `json:"resume"`
}

// ServiceStartParams 是 service.start 的参数。Command 为 nil 时启动已声明的服务（文件或已有的动态服务），
// 否则建立 / 替换名为 Name 的动态服务。Portal=true 且无其它链接时链接为 `[{title: Name, path: "/"}]`。
type ServiceStartParams struct {
	Name     string            `json:"name"`
	Command  *string           `json:"command,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	Port     int               `json:"port,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Health   string            `json:"health,omitempty"`
	Portal   bool              `json:"portal,omitempty"`
	ThreadID string            `json:"threadId,omitempty"`
}

// ServiceStopParams 是 service.stop 的参数；Remove 同时删除动态服务的定义与端口分配。
type ServiceStopParams struct {
	Name   string `json:"name"`
	Remove bool   `json:"remove,omitempty"`
}

// ServiceNameParams 是 service.restart 的参数。
type ServiceNameParams struct {
	Name string `json:"name"`
}

// ServiceResult 是 service.start / stop / restart 的结果。
type ServiceResult struct {
	Service apigen.OrbService `json:"service"`
}

// ServiceListResult 是 service.list / service.ensure 的结果；FileError 是 services.yaml 的语法错误
// （此时文件服务保持上一次成功解析的定义）。
type ServiceListResult struct {
	Services  []apigen.OrbService `json:"services"`
	FileError string              `json:"fileError,omitempty"`
}

// DesktopResult 是 desktop.status / desktop.browser.start / desktop.browser.stop 的结果。
// Display 是 X 显示名（`:1`），CDPURL 是 CDP 前门地址，ProfileName 是本地标记里的 Profile 名字（没有为空）。
type DesktopResult struct {
	State       apigen.LinkCtlDesktopStateParams `json:"state"`
	Display     string                           `json:"display"`
	CDPURL      string                           `json:"cdpUrl"`
	ProfileName string                           `json:"profileName,omitempty"`
}

// DesktopRecordStartResult 是 desktop.record.start（以及本机路由 POST /desktop/record/start）的结果：
// Path 是录制文件在 orb 内的绝对路径（`$HOME/recordings/<UTC yyyyMMdd-HHmmss>.mp4`）。
type DesktopRecordStartResult struct {
	Path string `json:"path"`
}

// 录制停止的原因（DesktopRecording.StoppedBy）。
const (
	RecordStoppedByAgent   = "agent"   // 调用方 stop
	RecordStoppedByLimit   = "limit"   // 到达时长上限（10 分钟）自动停止
	RecordStoppedByError   = "error"   // ffmpeg 自己退出，或显示失效
	RecordStoppedByControl = "control" // 用户接管、agent 请求人工操作或回合结束：放开人工输入之前先停录
)

// DesktopRecording 是一次结束的录屏（desktop.record.stop 与 POST /desktop/record/stop 的结果）。
// Width / Height 是开始时的根窗口尺寸；Truncated 表示 ffmpeg 没在等待时限内收尾、被强行结束，文件可能不完整。
type DesktopRecording struct {
	Path       string `json:"path"`
	Bytes      int64  `json:"bytes"`
	DurationMs int64  `json:"durationMs"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Truncated  bool   `json:"truncated"`
	StoppedBy  string `json:"stoppedBy"`
}

// ThreadWaitParams 是 thread.wait 的参数：TimeoutSeconds 缺省 1800，上限 86400。
type ThreadWaitParams struct {
	ThreadID       string `json:"threadId"`
	Target         string `json:"target"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

// GithubPrepareParams 是 github.prepare 的参数（M15）：threadId 恒为调用方线程；level 为 push | pr；
// skipChecks 时 reason 必填。
type GithubPrepareParams struct {
	ThreadID   string `json:"threadId"`
	Level      string `json:"level"`
	SkipChecks bool   `json:"skipChecks,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// DesignOpenParams 是 design.open 的参数：没有 ID 时新建一个空设计画布（Title 为空用缺省标题）；
// 有 ID 时它必须是设计画布，或者还没有画框的画布。
type DesignOpenParams struct {
	ThreadID string `json:"threadId"`
	ID       string `json:"id,omitempty"`
	Title    string `json:"title,omitempty"`
}

// DesignOpenResult 是 design.open 的结果：Dir 是工作副本根（`~/designs/<id>`），Version 是副本所基于的版本，
// DevURL 是 dev 服务的本机地址 `http://127.0.0.1:<port>/`（帧在 `/frames/<帧 id>`）。
type DesignOpenResult struct {
	ID      string `json:"id"`
	Dir     string `json:"dir"`
	Version int    `json:"version"`
	DevURL  string `json:"devUrl"`
}

// DesignTurnParams 是 design.turn 的参数：TurnID 由扩展生成，Title 是回合的用户输入原文。
type DesignTurnParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Title    string `json:"title,omitempty"`
}

// DesignSealParams 是 design.seal 的参数。
type DesignSealParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// DesignSealed 是封存出的一个版本。
type DesignSealed struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// DesignSealResult 是 design.seal 的结果：Sealed 是落了新版本的副本，Skipped 是源码自上次封存以来没变的副本。
type DesignSealResult struct {
	Sealed  []DesignSealed `json:"sealed"`
	Skipped []string       `json:"skipped"`
}

// DesignCheckParams 是 design.check 的参数：Frames 为空时检查 src/frames/ 下全部帧（不含 _ 开头的共享外壳），
// 否则只检查这些帧 id；Screenshot 为真时另附每帧截图。
type DesignCheckParams struct {
	ThreadID   string   `json:"threadId"`
	ID         string   `json:"id"`
	Frames     []string `json:"frames,omitempty"`
	Screenshot bool     `json:"screenshot,omitempty"`
}

// DesignCheckIssue 是一条探针发现，与 internal/artifacts/design.Issue 同形（orbsock 不依赖 artifacts 包）：Frame 是帧
// id，Loc 是 `src/frames/x.tsx:行:列`，Detail 是规则自带的数字或字符串。
type DesignCheckIssue struct {
	Rule   string          `json:"rule"`
	Frame  string          `json:"frame,omitempty"`
	Loc    string          `json:"loc,omitempty"`
	Text   string          `json:"text,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"`
}

// DesignCheckImage 是一帧的截图：Data 是 PNG 的 base64。
type DesignCheckImage struct {
	Frame string `json:"frame"`
	Data  string `json:"data"`
}

// DesignCheckResult 是 design.check 的结果：Status ∈ clean | issues | unverified（任何一帧没跑成且没有发现问题时为
// unverified，例如 orb 里没有 Chromium）；Message 说明没跑成的帧与原因（全部跑成时为空）；Images 只在 screenshot 时
// 有内容，否则为空数组。
type DesignCheckResult struct {
	Status  string             `json:"status"`
	Issues  []DesignCheckIssue `json:"issues"`
	Images  []DesignCheckImage `json:"images"`
	Message string             `json:"message,omitempty"`
}

// DesignStatusParams 是 design.status 的参数。
type DesignStatusParams struct {
	ThreadID string `json:"threadId"`
}

// DesignStatus 是本线程的一个工作副本：Dirty 表示源码自上次封存（或物化）以来变了；Check 是最新版本的设计检查，
// 状态已按副本当前源码换算（指纹不同为 stale），没有检查过为 nil。
type DesignStatus struct {
	ID    string                `json:"id"`
	Dir   string                `json:"dir"`
	Dirty bool                  `json:"dirty"`
	Check *apigen.ArtifactCheck `json:"check"`
}

// DesignStatusResult 是 design.status 的结果。
type DesignStatusResult struct {
	Designs []DesignStatus `json:"designs"`
}

// DesignEjectParams 是 design.eject 的参数：把设计画布的指定版本（Version 为 0 取最新）导出为可独立构建与部署到
// box 的 Vite 工程，安全解压到 To（须是绝对路径；若已存在须是空目录）。
type DesignEjectParams struct {
	ThreadID string `json:"threadId"`
	ID       string `json:"id"`
	Version  int    `json:"version,omitempty"`
	To       string `json:"to"`
}

// DesignEjectResult 是 design.eject 的结果：Dir 是最终导出目录的绝对路径，Files 是解压出的普通文件数。
type DesignEjectResult struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Dir     string `json:"dir"`
	Files   int    `json:"files"`
}

// DesignHead 是 ctl artifact.call 内部操作 design_head `{id}` 的结果（结果的 text 是它的 JSON）：设计画布（或还没有
// 画框的画布）的最新版本号、该版本的 check_json 原文（design.Check 的形状，没检查过为空），以及该版本 `src/**`
// （不含 node_modules）与 `dist/**` 的文件摘要。orbd 用它对账、判断封存要传哪些文件、回报 design.status 的检查。
// Aesthetic、Surface 是探针的审美配置（已选定设计方向时打开审美规则；surface 为 app 时探针再关掉两条落地页规则），
// design.check 把它们拼进页面地址（`__oryn_aesthetic=1`、`__oryn_surface=<landing|app>`）。Archived 是落选方案的帧 id
// （方案组已选定时本组其它方案的画框经 source 换成帧 id，按字典序）：design.check 与回合结束的检查都跳过它们。
type DesignHead struct {
	Version   int              `json:"version"`
	Check     json.RawMessage  `json:"check,omitempty"`
	Files     []DesignHeadFile `json:"files"`
	Aesthetic bool             `json:"aesthetic,omitempty"`
	Surface   string           `json:"surface,omitempty"`
	Archived  []string         `json:"archived,omitempty"`
}

// DesignHeadFile 是 DesignHead 里一个文件的摘要。
type DesignHeadFile struct {
	Path   string `json:"path"`
	Sha256 string `json:"sha256"`
}

// DesignFilesPage 是 ctl artifact.call 内部操作 fetch_design_files `{id, version?, cursor?}` 的结果（结果的 text 是它的
// JSON）：版本里 `src/**`（不含 node_modules）的文件按路径排序、逐片返回。一个文件可以跨页：每片是 [Offset, Offset+len)
// 的原始字节（Data 为 base64），Size 与 Sha256 是整个文件的。Next 非空时还有下一页，下一次带 cursor=Next 与同一个
// version 再取。整条结果序列化后不超过 DesignFilesPageBytes。
type DesignFilesPage struct {
	Version int               `json:"version"`
	Files   []DesignFileChunk `json:"files"`
	Next    string            `json:"next,omitempty"`
}

// DesignFileChunk 是一个文件的一片。
type DesignFileChunk struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Data   string `json:"data"`
	Size   int64  `json:"size"`
	Sha256 string `json:"sha256"`
}

// DesignProjectPage 是 ctl artifact.call 内部操作 export_design_project `{id, version?, cursor?}` 的结果（结果的 text
// 是它的 JSON）：设计画布版本导出的工程 zip 按字节偏移分页返回。每片是 [Offset, Offset+len) 的原始字节（Data 为
// base64），Size 与 Sha256 是整个 zip 的。Next 非空时还有下一页，下一次带 cursor=Next 与同一个 version 再取。
// 整条结果序列化后不超过 DesignFilesPageBytes。
type DesignProjectPage struct {
	Version int    `json:"version"`
	Size    int64  `json:"size"`
	Sha256  string `json:"sha256"`
	Offset  int64  `json:"offset"`
	Data    string `json:"data"`
	Next    string `json:"next,omitempty"`
}

// DesignRuntimeFiles 是内部操作 fetch_design_runtime `{}` 的结果：控制面 Web 构建里的 `sandbox/runtime.js`、
// `sandbox/probe.js`（Name 为 runtime.js / probe.js，Data 为 base64），缺哪个就没有哪个。
type DesignRuntimeFiles struct {
	Files []DesignRuntimeFile `json:"files"`
}

// DesignRuntimeFile 是 fetch_design_runtime 返回的一个文件。
type DesignRuntimeFile struct {
	Name string `json:"name"`
	Data string `json:"data"`
}

// ---- 服务端 ----

// Responder 向调用方流式写日志行；并发安全，写失败（调用方已断开）后静默丢弃。
type Responder struct {
	mu     sync.Mutex
	enc    *json.Encoder
	broken bool
}

// Log 写一条日志消息。
func (r *Responder) Log(stream, data string) {
	r.write(Message{Type: TypeLog, Stream: stream, Data: data})
}

func (r *Responder) write(m Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.broken {
		return
	}
	if err := r.enc.Encode(m); err != nil {
		r.broken = true
	}
}

// Handler 处理一个请求：返回 (result, nil) 时写 result 消息；返回 *Error 时原样写错误，
// 其它错误映射为 code=internal。ctx 在调用方断开连接时取消。
type Handler func(ctx context.Context, method string, params json.RawMessage, out *Responder) (any, error)

// Listen 在 path 上监听 unix socket（权限 0600）。残留的 socket 文件若无人监听则删除；
// 已有活的监听者时报错。
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close()
			return nil, fmt.Errorf("orbsock: %s is already being served", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve 接受连接直到 ln 关闭或 ctx 结束；每个连接一个 goroutine。
func Serve(ctx context.Context, ln net.Listener, h Handler) {
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				slog.Warn("orbsock: accept failed", "err", err)
			}
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			serveConn(ctx, c, h)
		}()
	}
}

func serveConn(ctx context.Context, c net.Conn, h Handler) {
	defer c.Close()
	// 服务端退出（ctx 结束）时关闭连接：解除阻塞在读请求行或写回应上的连接，Serve 才能返回。
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	out := &Responder{enc: json.NewEncoder(c)}
	br := bufio.NewReaderSize(c, 64<<10)
	line, err := readLine(br, MaxRequestBytes)
	if err != nil {
		out.write(Message{Type: TypeError, Code: CodeInvalidParams, Message: err.Error()})
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil || req.Method == "" {
		out.write(Message{Type: TypeError, Code: CodeInvalidParams, Message: "request must be one JSON line with a method"})
		return
	}
	// 调用方断开（读到 EOF）即取消请求。
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, br)
		cancel()
	}()
	res, err := h(cctx, req.Method, req.Params, out)
	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			e = &Error{Code: CodeInternal, Message: err.Error()}
		}
		out.write(Message{Type: TypeError, Code: e.Code, Message: e.Message})
		return
	}
	b, err := json.Marshal(res)
	if err != nil {
		out.write(Message{Type: TypeError, Code: CodeInternal, Message: err.Error()})
		return
	}
	out.write(Message{Type: TypeResult, Result: b})
}

func readLine(br *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > limit {
			return nil, fmt.Errorf("request exceeds %d bytes", limit)
		}
		switch {
		case err == nil:
			return buf, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return nil, err
		}
	}
}

// ---- 客户端 ----

// Call 发出一个请求，把日志消息交给 onLog（可为 nil），返回 result 的原始 JSON。
// 服务端错误以 *Error 返回。
func Call(ctx context.Context, path, method string, params any, onLog func(stream, data string)) (json.RawMessage, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	req := Request{Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		req.Params = b
	}
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(c)
	for {
		var m Message
		if err := dec.Decode(&m); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return nil, errors.New("orbsock: connection closed before result")
			}
			return nil, err
		}
		switch m.Type {
		case TypeLog:
			if onLog != nil {
				onLog(m.Stream, m.Data)
			}
		case TypeResult:
			return m.Result, nil
		case TypeError:
			return nil, &Error{Code: m.Code, Message: m.Message}
		default:
			return nil, fmt.Errorf("orbsock: unexpected message type %q", m.Type)
		}
	}
}
