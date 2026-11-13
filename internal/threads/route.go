package threads

import (
	"encoding/json"
	"strings"
)

// 命令路由结果。
const (
	RoutePass         = "pass"
	RouteManaged      = "managed_by_oryn"
	RouteUseOrynFork  = "use_oryn_fork"
	RouteInvalidFrame = "invalid_frame"
)

// managedCommands 是线程↔会话绑定、凭据、宿主浏览器等由 Oryn 管理、不允许客户端直接下发的命令。
var managedCommands = map[string]bool{
	"new_session": true, "switch_session": true, "open_session": true, "move_session": true,
	"collab_start": true, "collab_join": true, "collab_leave": true,
	"browser_host_start": true, "browser_host_stop": true,
	"worktree_create": true, "worktree_remove": true,
	"get_omp_update": true, "login": true, "logout": true, "set_api_key": true,
}

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

// RouteCommand 判定客户端 cmd 帧的处理方式，返回路由结果与命令类型。
func RouteCommand(frame json.RawMessage) (route, cmdType string) {
	typ, _, ok := frameHeader(frame)
	if !ok {
		return RouteInvalidFrame, ""
	}
	switch {
	case managedCommands[typ]:
		return RouteManaged, typ
	case typ == "fork" || typ == "fork_from":
		return RouteUseOrynFork, typ
	case sideChannelTypes[typ]:
		// 侧通道应答经 ui.response 发送（先到先得、广播 ui.resolved）
		return RouteInvalidFrame, typ
	}
	return RoutePass, typ
}

// RouteSideChannel 校验 ui.response 的帧：必须是带字符串 id 的侧通道应答，返回其类型与 id。
func RouteSideChannel(frame json.RawMessage) (typ, id string, ok bool) {
	typ, id, ok = frameHeader(frame)
	if !ok || !sideChannelTypes[typ] || id == "" {
		return "", "", false
	}
	return typ, id, true
}
