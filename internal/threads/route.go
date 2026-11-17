package threads

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// 命令路由结果。
const (
	RoutePass         = "pass"
	RouteManaged      = "managed_by_oryn"
	RouteUseOrynFork  = "use_oryn_fork"
	RouteInvalidFrame = "invalid_frame"
)

// 线程↔会话绑定：线程的会话目录是 `~/.oryn/threads/<id>`，转录 = 目录里各主会话文件按创建顺序拼接（transcript.ActiveBranch），
// agent 重启时 open_session 续上目录里最新的非空会话。据此把 omp 里会换会话文件的命令分三类：
//   - Oryn 能跟随：在本目录开一个空的新会话文件（plan_approval 的 execute / save、set_loop_mode 的 reset 轮次）——
//     转录在文件之间画「新会话」分隔线，重启后续上新文件。放行。
//   - 把历史复制进新主会话文件（fork、branch，以及 fork_from 的产物）：拼接后旧路径会重复显示，叶子也会同时落在两个文件里。
//     语义就是分叉 → use_oryn_fork（POST threads/{id}/fork 建新线程承接）。
//   - 其余破坏绑定的 → managed_by_oryn：切到 / 打开目录外的文件或把文件移出目录（switch_session、open_session、move_session、
//     collab_join），删除当前会话文件（drop_session），btw_branch（复制当前路径并追加旁路问答，Oryn 的 fork 表达不了
//     追加的问答；把回答并进当前对话用 btw_inject）。new_session 虽能被拼接跟随，但「新对话」在 Oryn 里是新线程。
//
// 原地改动不换文件，照常放行：clear_context、fresh、handoff（写压缩条目）、compact、navigate_tree / switch_leaf（只移叶子）、
// tan（克隆写在产物子目录里，按子 agent 转录处理）、import_foreign_session（写到 omp 默认会话目录，不切换）、pr_checkout。

// managedCommands 是线程↔会话绑定、凭据、宿主浏览器等由 Oryn 管理、不允许客户端直接下发的命令。
// oryn_mark_session_start 只由控制面在 agent 就绪时发（markSessionStartLocked）：客户端发它能把手动选的模型冒充成启动来源。
// set_model 不拦：旧标签页 / 旧 App 仍要能切模型，结果是「未记录来源的切换」→ 回合头回落，方向安全。
var managedCommands = map[string]bool{
	"new_session": true, "drop_session": true, "switch_session": true, "open_session": true, "move_session": true,
	"btw_branch":   true,
	"collab_start": true, "collab_join": true, "collab_leave": true,
	"browser_host_start": true, "browser_host_stop": true,
	"worktree_create": true, "worktree_remove": true,
	"get_omp_update": true, "login": true, "logout": true, "set_api_key": true,
	"oryn_mark_session_start": true,
}

// forkCommands 是把历史复制进新会话文件的命令，客户端改走 Oryn 的 fork。
var forkCommands = map[string]bool{"fork": true, "fork_from": true, "branch": true}

// managedSlashCommands 是 prompt 正文里会被 omp 当作内置斜杠命令执行、并换掉会话文件的命令（名字含别名）：
// `/move` 与 `/wt`（`/worktree`）把会话文件移出线程目录（同 move_session），`/session delete` 删除当前会话文件（同 drop_session）。
// 值为空表示整条命令都拦；非空表示只拦该子命令（且其后没有余参）。
var managedSlashCommands = map[string]string{"move": "", "wt": "", "worktree": "", "session": "delete"}

// sideChannelTypes 是 ui.response 可以携带的侧通道应答类型，与 omp `dispatchRpcControlFrame` 的判定一致：
// 这些帧不进入 omp 的命令处理器；其余任何类型都会被 omp 当作普通命令执行，必须走 Command 的路由检查。
var sideChannelTypes = map[string]bool{
	"extension_ui_response": true,
	"host_tool_result":      true,
	"host_tool_update":      true,
	"host_uri_result":       true,
}

// passiveCommand 报告 cmd 是否为只读查询（`get_*`、终端列表 / 快照）。客户端在收到帧、快照或 live.reset 后
// 会自动发这类查询来刷新界面，它们不代表用户交互：不唤醒 orb、不记用户交互、不清未读；agent 尚未就绪但会自行就绪时
// （新线程 provisioning、orb 恢复途中、agent 启动 / 重启中）排队等就绪后下发，确定不会就绪时（orb 正在或已经挂起 /
// 失败 / 删除、agent 重启耗尽）直接拒绝（code agent_unavailable），见 Service.passiveLocked。
func passiveCommand(cmdType string) bool {
	return strings.HasPrefix(cmdType, "get_") || cmdType == "terminal_list" || cmdType == "terminal_snapshot"
}

// frameHeader 严格读取帧的 `type` 与 `id`：只认精确的小写键，出现大小写不同的同名键（如 `Type`、`ID`）即判为非法。
// Go 的结构体解码对键名大小写不敏感（且取最后一个），而 omp（JS）只认精确键：二者对同一帧可能读出不同的类型，
// 例如 `{"type":"login","Type":"prompt"}` 在 Go 里是 prompt、在 omp 里是 login。重复的精确键两边都取最后一个，一致。
func frameHeader(frame json.RawMessage) (typ, id string, ok bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(frame, &m) != nil || m == nil {
		return "", "", false
	}
	for k := range m {
		if (k != "type" && strings.EqualFold(k, "type")) || (k != "id" && strings.EqualFold(k, "id")) {
			return "", "", false
		}
	}
	if raw, has := m["type"]; !has || json.Unmarshal(raw, &typ) != nil || typ == "" {
		return "", "", false
	}
	if raw, has := m["id"]; has {
		if json.Unmarshal(raw, &id) != nil {
			return "", "", false
		}
	}
	return typ, id, true
}

// RouteCommand 判定客户端 cmd 帧的处理方式，返回路由结果与命令类型。prompt 正文是会换会话文件的内置斜杠命令时
// 路由为 managed_by_oryn，cmdType 换成该斜杠命令（如 `/move`、`/session delete`），供报错说明被拦的是什么。
func RouteCommand(frame json.RawMessage) (route, cmdType string) {
	typ, _, ok := frameHeader(frame)
	if !ok {
		return RouteInvalidFrame, ""
	}
	switch {
	case managedCommands[typ]:
		return RouteManaged, typ
	case forkCommands[typ]:
		return RouteUseOrynFork, typ
	case sideChannelTypes[typ]:
		// 侧通道应答经 ui.response 发送（先到先得、广播 ui.resolved）
		return RouteInvalidFrame, typ
	case typ == "prompt":
		if slash, managed := managedSlash(frame); managed {
			return RouteManaged, slash
		}
	}
	return RoutePass, typ
}

// parseSlash 按 omp 的判定（executeAcpBuiltinSlashCommand → parseSlashCommand）解析 prompt 帧正文里的内置斜杠命令：
// 只认精确的 `message` 键；正文必须以 "/" 开头（不去前导空白）；命令名截到第一个空白或 ":"，大小写敏感；
// 参数去首尾空白。空白按 JS 正则 `\s`（jsSpace）。
func parseSlash(frame json.RawMessage) (name, args string, ok bool) {
	var m map[string]json.RawMessage
	var msg string
	if json.Unmarshal(frame, &m) != nil || json.Unmarshal(m["message"], &msg) != nil {
		return "", "", false
	}
	body, has := strings.CutPrefix(msg, "/")
	if !has || body == "" {
		return "", "", false
	}
	name = body
	if i := strings.IndexFunc(body, func(r rune) bool { return r == ':' || jsSpace(r) }); i >= 0 {
		_, size := utf8.DecodeRuneInString(body[i:])
		name, args = body[:i], strings.TrimFunc(body[i+size:], jsSpace)
	}
	return name, args, true
}

// splitVerb 把斜杠命令的参数拆成子命令（截到第一个空白）与去首尾空白的余参（同 omp 的 parseSubcommand）。
func splitVerb(args string) (verb, rest string) {
	verb = args
	if i := strings.IndexFunc(args, jsSpace); i >= 0 {
		verb, rest = args[:i], strings.TrimFunc(args[i:], jsSpace)
	}
	return verb, rest
}

// managedSlash 报告 prompt 帧的 message 是否是 managedSlashCommands 里的内置斜杠命令，返回 `/名字[ 子命令]`。
// 解析见 parseSlash；子命令转小写比较，其后不得有余参。只有 prompt 执行内置命令，steer / follow_up / abort_and_prompt
// 不执行；扩展的 input 钩子可能先改写正文，这在路由层看不到。
func managedSlash(frame json.RawMessage) (string, bool) {
	name, args, ok := parseSlash(frame)
	if !ok {
		return "", false
	}
	sub, has := managedSlashCommands[name]
	if !has {
		return "", false
	}
	if sub == "" {
		return "/" + name, true
	}
	verb, rest := splitVerb(args)
	if strings.ToLower(verb) != sub || rest != "" {
		return "", false
	}
	return "/" + name + " " + sub, true
}

// sshManageCommand 报告命令是否会改动 omp 的 SSH 主机目录（ssh.json）：`ssh_manage` RPC，或 prompt 正文是
// `/ssh add|remove|rm …`（子命令不分大小写，其后可以有余参）。orb 线程的主机由 Oryn 集中管理（M18），这类命令要拦下；
// 只读的 get_ssh_hosts、ssh_test、`/ssh list`、`/ssh help` 照常直通。
func sshManageCommand(cmdType string, frame json.RawMessage) bool {
	switch cmdType {
	case "ssh_manage":
		return true
	case "prompt":
		name, args, ok := parseSlash(frame)
		if !ok || name != "ssh" {
			return false
		}
		verb, _ := splitVerb(args)
		switch strings.ToLower(verb) {
		case "add", "remove", "rm":
			return true
		}
	}
	return false
}

// jsSpace 报告 r 是否属于 JS 正则 `\s`（也是 String.prototype.trim 去掉的字符集）；它与 unicode.IsSpace 不同：
// 含 U+FEFF，不含 U+0085。
func jsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// RouteSideChannel 校验 ui.response 的帧：必须是带字符串 id 的侧通道应答，返回其类型与 id。
func RouteSideChannel(frame json.RawMessage) (typ, id string, ok bool) {
	typ, id, ok = frameHeader(frame)
	if !ok || !sideChannelTypes[typ] || id == "" {
		return "", "", false
	}
	return typ, id, true
}
