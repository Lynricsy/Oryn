package api

// WSPath 是客户端 WebSocket 的挂载路径（`GET /api/v1/ws?ticket=…`）。
// 处理器为 realtime.Hub：挂在 Deps.Mounts[WSPath]，位于 /api/v1 鉴权组之外，由一次性票据鉴权。
const WSPath = "/api/v1/ws"
