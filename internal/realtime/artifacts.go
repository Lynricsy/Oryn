package realtime

import (
	"encoding/json"
	"regexp"
	"sort"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// Watcher 是正在观看某个 artifact 的一个私有连接。
type Watcher struct {
	// Conn 是连接 id（进程内唯一）。
	Conn string
	// Page 是画布 artifact 当前所在的画布页 id；没有时为空。
	Page string
}

const (
	// maxArtifactWatches 是单连接同时观看的 artifact 上限。
	maxArtifactWatches = 64
	// maxWatchPageBytes 是 artifact.watch 里 page 的长度上限。
	maxWatchPageBytes = 128
)

var artifactIDRe = regexp.MustCompile(`^art_[0-9A-Z]{26}$`)

// ArtifactsChanged 实现 artifacts.Hub：artifact 列表变了，广播给全部私有连接。
func (h *Hub) ArtifactsChanged() {
	h.toAll(apigen.WsServerArtifactsChanged{Type: apigen.WsServerArtifactsChangedTypeArtifactsChanged})
}

// ArtifactVersion 实现 artifacts.Hub：某个 artifact 有了新版本，只发给正在观看它的连接。
func (h *Hub) ArtifactVersion(id string, version int) {
	h.ToWatchers(id, apigen.WsServerArtifactVersion{Type: apigen.WsServerArtifactVersionTypeArtifactVersion, ID: id, Version: version})
}

// ArtifactComments 实现 artifacts.Hub：某个 artifact 的评论有变化，只发给正在观看它的连接。
func (h *Hub) ArtifactComments(id string) {
	h.ToWatchers(id, apigen.WsServerArtifactComments{Type: apigen.WsServerArtifactCommentsTypeArtifactComments, ID: id})
}

// ArtifactCanvas 实现 artifacts.CanvasHub：画布的实时变化，只发给正在观看它的连接。
func (h *Hub) ArtifactCanvas(msg apigen.WsServerArtifactCanvas) {
	msg.Type = apigen.WsServerArtifactCanvasTypeArtifactCanvas
	h.ToWatchers(msg.ID, msg)
}

// WatchPages 实现 artifacts.CanvasHub：正在观看 artifact id 的各连接所在的画布页 id（按连接 id 排序；没报告的为空串）。
func (h *Hub) WatchPages(id string) []string {
	ws := h.Watchers(id)
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Page
	}
	return out
}

// ToWatchers 把消息发给正在观看 artifact id 的全部连接。
func (h *Hub) ToWatchers(id string, msg any) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.watches[id] {
		c.enqueue(b)
	}
}

// Watchers 返回正在观看 artifact id 的连接（按连接 id 排序）。
func (h *Hub) Watchers(id string) []Watcher {
	h.mu.Lock()
	out := make([]Watcher, 0, len(h.watches[id]))
	for c, page := range h.watches[id] {
		out = append(out, Watcher{Conn: c.id, Page: page})
	}
	h.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Conn < out[j].Conn })
	return out
}

// OnWatchChange 登记回调：某个 artifact 的观看者变化（watch、page 变化、unwatch、连接断开）时以 artifact id 调用。
// 回调在 hub 锁外同步执行，可以回调 Watchers / ToWatchers。
func (h *Hub) OnWatchChange(fn func(id string)) {
	h.mu.Lock()
	h.watchFns = append(h.watchFns, fn)
	h.mu.Unlock()
}

// fireWatch 在锁外依次通知 ids 的观看者变化。
func (h *Hub) fireWatch(ids ...string) {
	if len(ids) == 0 {
		return
	}
	h.mu.Lock()
	fns := append([]func(string){}, h.watchFns...)
	h.mu.Unlock()
	for _, id := range ids {
		for _, fn := range fns {
			fn(id)
		}
	}
}

// watchArtifact 处理 `artifact.watch {id, page?}`：登记或更新观看（重复发送用来更新 page）。
func (h *Hub) watchArtifact(c *conn, id string, page *string) {
	if !artifactIDRe.MatchString(id) {
		c.sendError("bad_message", "artifact.watch requires a valid artifact id", nil, "")
		return
	}
	p := ""
	if page != nil {
		p = *page
	}
	if len(p) > maxWatchPageBytes {
		c.sendError("bad_message", "artifact.watch page too long", nil, "")
		return
	}
	h.mu.Lock()
	if h.watches == nil {
		h.watches = map[string]map[*conn]string{}
	}
	m := h.watches[id]
	old, had := m[c]
	if !had && c.artifactWatches >= maxArtifactWatches {
		h.mu.Unlock()
		c.sendError("too_many_watches", "too many artifacts watched on one connection", nil, "")
		return
	}
	if m == nil {
		m = map[*conn]string{}
		h.watches[id] = m
	}
	m[c] = p
	if !had {
		c.artifactWatches++
	}
	h.mu.Unlock()
	if !had || old != p {
		h.fireWatch(id)
	}
}

// unwatchArtifact 处理 `artifact.unwatch {id}`；没在观看时什么也不做。
func (h *Hub) unwatchArtifact(c *conn, id string) {
	h.mu.Lock()
	m := h.watches[id]
	_, had := m[c]
	if had {
		delete(m, c)
		c.artifactWatches--
		if len(m) == 0 {
			delete(h.watches, id)
		}
	}
	h.mu.Unlock()
	if had {
		h.fireWatch(id)
	}
}

// unwatchAll 在连接断开时清除它的全部观看。
func (h *Hub) unwatchAll(c *conn) {
	h.mu.Lock()
	var ids []string
	for id, m := range h.watches {
		if _, ok := m[c]; !ok {
			continue
		}
		delete(m, c)
		if len(m) == 0 {
			delete(h.watches, id)
		}
		ids = append(ids, id)
	}
	c.artifactWatches = 0
	h.mu.Unlock()
	h.fireWatch(ids...)
}
