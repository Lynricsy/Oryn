package threads

import "encoding/json"

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

// RouteCommand 判定客户端 cmd 帧的处理方式，返回路由结果与命令类型。
func RouteCommand(frame json.RawMessage) (route, cmdType string) {
	var h struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(frame, &h) != nil || h.Type == "" {
		return RouteInvalidFrame, ""
	}
	switch {
	case managedCommands[h.Type]:
		return RouteManaged, h.Type
	case h.Type == "fork" || h.Type == "fork_from":
		return RouteUseOrynFork, h.Type
	}
	return RoutePass, h.Type
}
