package api

import "github.com/Lynricsy/Oryn/internal/threads"

// ThreadsDeps 是线程领域（线程、条目、fork、唤醒、blob、WS）的依赖。
// `/api/v1/ws` 由 internal/realtime 的 Hub 经 Deps.Mounts[WSPath] 挂载。
type ThreadsDeps struct {
	Service *threads.Service
}
