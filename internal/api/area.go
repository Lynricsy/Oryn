package api

import "github.com/Lynricsy/Oryn/internal/portal"

// area 聚合各领域处理器的依赖。每个领域在自己的 server_<领域>.go 中定义 <领域>Deps，
// 并在此处加一个指针字段（这是唯一需要多方编辑的位置，改动只加行）。
type area struct {
	Auth        *AuthDeps
	Orbs        *OrbsDeps
	Threads     *ThreadsDeps
	Attachments *AttachmentsDeps
	// PortalShares 为 nil（未配置 portal_domain）时分享接口回 409 portal_disabled。
	PortalShares *PortalDeps
	// Shares 是线程公开分享与文件发布（shares.go）；Notify 是通知与渠道（notifications.go）。
	Shares *ShareDeps
	Notify *NotifyDeps
	// Desktop 是 M8 桌面与浏览器 Profile（desktop.go）；nil 时这些接口回 501。
	Desktop *DesktopDeps
	// Coord 是 M9 的 Space、全局助手、协调宿主状态与操作记录（spaces.go、coordinator.go）；nil 时回 501。
	Coord *CoordDeps
	// Schedules 是 M9 的定时（schedules.go）；nil 时回 501。
	Schedules *ScheduleDeps
	// Stats 是 M10 的统计 meta 与预算（stats.go）；nil 时这两类接口回 501（只读聚合不依赖它）。
	Stats *StatsDeps
	// Library 是 M13 的全局助手文件库（library.go）；nil 时文件库接口回 501。
	Library *LibraryDeps
	// Search 是对话内容全文搜索（search.go）；nil 时回 501。
	Search *SearchDeps
	// GitHub 是 M14 的 GitHub 集成（github.go）；nil 时回 501。
	GitHub *GitHubDeps
	// Artifacts 是 M14 的 Artifacts（artifacts.go）；nil 时回 501。
	Artifacts *ArtifactsDeps
	// Memory 是 M16 的内置 Hindsight 记忆（memory.go）；nil 时回 501 memory_unavailable。
	Memory *MemoryDeps
}

// PortalDeps 是 portal 限时公开接口（portals.go）的依赖。
type PortalDeps struct {
	Service *portal.Service
}
