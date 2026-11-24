package artifacts

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 在场（M14 计划步骤 6.7，服务端）。
//
// 每个 artifact 在内存里有一份活动：观看者（取自 realtime 的观看登记）与 agent 活动 {threadId, doing, focus}。
// agent 活动由 Call 记录（read_artifact 记 read；update_artifact 带文件或 edits 记 write，画布操作里新增画框记 draw、
// 改画框的位置尺寸或层级记 move），线程的回合结束（转入 idle / error，或带标签投递得到终态）时清掉。任何变化在
// ActivityDebounce 之后合并成一条 artifact.activity 发给观看者；观看者变化（含新观看者 watch）同样走这条路，
// 所以新打开的查看器很快就拿到当前快照，不必等下一次变化。

// ActivityDebounce 是 artifact.activity 的合并窗口：窗口内的连续变化只发一条。
const ActivityDebounce = 200 * time.Millisecond

// Viewer 是正在观看某个 artifact 的一个连接。
type Viewer struct {
	// Conn 是连接 id；Page 是它所在的画布页 id（没报告时为空）。
	Conn, Page string
}

// ActivityHub 是在场信息的出口（server 包把 realtime.Hub 适配成它）。
type ActivityHub interface {
	// Viewers 返回正在观看 artifact id 的连接（顺序稳定）。
	Viewers(id string) []Viewer
	// ToWatchers 把消息发给正在观看 artifact id 的全部连接。
	ToWatchers(id string, msg any)
	// OnWatchChange 登记观看者变化的回调。
	OnWatchChange(fn func(id string))
}

// agentActivity 是一个线程在某个 artifact 上的活动。
type agentActivity struct {
	doing apigen.ArtifactThreadActivityDoing
	// path 是正在读或改的文件（空 = 没有焦点）；page 是该文件对应的画框 id（不是画框文件时为空）。
	path, page string
	at         time.Time
}

// activitySet 是全部 artifact 的在场状态；零值可用。
type activitySet struct {
	mu sync.Mutex
	// agents：artifact id → 线程 id → 活动。
	agents map[string]map[string]agentActivity
	// armed：已安排了合并发送、还没发出的 artifact。
	armed map[string]bool
	hub   ActivityHub
	// after 安排一次延时回调；nil 时用 time.AfterFunc（测试注入假时钟）。
	after func(d time.Duration, f func()) (stop func() bool)
	// visitors 返回发布页的访客数（步骤 8.2 接上）；nil 时为 0。
	visitors func(id string) int
	// ended：回合结束、等后台清理的线程；wake 是有新事件的信号（容量 1，不阻塞投递方）。
	ended map[string]bool
	wake  chan struct{}
}

// AttachActivityHub 接上在场信息的出口并登记观看者变化的回调；须在开始服务之前调用。
func (s *Service) AttachActivityHub(h ActivityHub) {
	s.act.mu.Lock()
	s.act.hub = h
	s.act.mu.Unlock()
	h.OnWatchChange(s.armActivity)
}

func (s *Service) activityHub() ActivityHub {
	s.act.mu.Lock()
	defer s.act.mu.Unlock()
	return s.act.hub
}

// armActivity 安排一次 id 的 artifact.activity：观看者变化（watch、page 变化、离开）时由 hub 回调，
// 发布页访客数变化时由发布页调用。发出的是当时的快照，所以新观看者在合并窗口之后就收到一份，不必等下一次变化。
func (s *Service) armActivity(id string) {
	if !ValidID(id) {
		return
	}
	s.act.mu.Lock()
	s.armActivityLocked(id)
	s.act.mu.Unlock()
}

// SetActivityVisitors 接上发布页访客数（步骤 8.2）；不接时访客数为 0。访客数变化时调 armActivity。
func (s *Service) SetActivityVisitors(fn func(id string) int) {
	s.act.mu.Lock()
	s.act.visitors = fn
	s.act.mu.Unlock()
}

// armActivityLocked 在合并窗口之后发一次 id 的在场信息；窗口内已安排过就不再安排。调用方持有 act.mu。
func (s *Service) armActivityLocked(id string) {
	if s.act.hub == nil || s.act.armed[id] {
		return
	}
	if s.act.armed == nil {
		s.act.armed = map[string]bool{}
	}
	s.act.armed[id] = true
	after := s.act.after
	if after == nil {
		after = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	after(ActivityDebounce, func() { s.emitActivity(id) })
}

// emitActivity 把 id 当前的在场信息发给它的观看者；没人在看时什么也不发。
func (s *Service) emitActivity(id string) {
	s.act.mu.Lock()
	delete(s.act.armed, id)
	h, visitors := s.act.hub, s.act.visitors
	s.act.mu.Unlock()
	if h == nil {
		return
	}
	viewers := h.Viewers(id)
	if len(viewers) == 0 {
		return
	}
	msg := s.Activity(id)
	msg.Viewers = len(viewers)
	for _, v := range viewers {
		c := struct {
			Client string  `json:"client"`
			Page   *string `json:"page,omitempty"`
		}{Client: v.Conn}
		if v.Page != "" {
			c.Page = &v.Page
		}
		msg.Clients = append(msg.Clients, c)
	}
	if visitors != nil {
		msg.Visitors = visitors(id)
	}
	h.ToWatchers(id, msg)
}

// Activity 返回 id 上的 agent 活动：正在改（write / draw / move）的线程在前、只在读的在后，同类按最近的在前。
// 观看者与访客字段由发送方填写。
func (s *Service) Activity(id string) apigen.WsServerArtifactActivity {
	s.act.mu.Lock()
	type entry struct {
		thread string
		agentActivity
	}
	list := make([]entry, 0, len(s.act.agents[id]))
	for t, a := range s.act.agents[id] {
		list = append(list, entry{t, a})
	}
	s.act.mu.Unlock()
	slices.SortFunc(list, func(a, b entry) int {
		if ar, br := a.doing == apigen.ArtifactDoingRead, b.doing == apigen.ArtifactDoingRead; ar != br {
			if ar {
				return 1
			}
			return -1
		}
		if c := b.at.Compare(a.at); c != 0 {
			return c
		}
		return cmp.Compare(a.thread, b.thread)
	})
	msg := apigen.WsServerArtifactActivity{
		Type: apigen.WsServerArtifactActivityTypeArtifactActivity, ID: id,
		Threads: make([]apigen.ArtifactThreadActivity, 0, len(list)),
		Clients: []struct {
			Client string  `json:"client"`
			Page   *string `json:"page,omitempty"`
		}{},
	}
	for _, e := range list {
		ta := apigen.ArtifactThreadActivity{ThreadID: e.thread, Doing: e.doing}
		if e.path != "" {
			ta.Focus = &struct {
				Page *string `json:"page,omitempty"`
				Path string  `json:"path"`
			}{Path: e.path}
			if e.page != "" {
				page := e.page
				ta.Focus.Page = &page
			}
		}
		msg.Threads = append(msg.Threads, ta)
	}
	return msg
}

// setActivity 记下线程 thread 在 id 上的活动；有变化时安排发送。
func (s *Service) setActivity(id, thread string, a agentActivity) {
	s.act.mu.Lock()
	defer s.act.mu.Unlock()
	if s.act.agents == nil {
		s.act.agents = map[string]map[string]agentActivity{}
	}
	m := s.act.agents[id]
	if m == nil {
		m = map[string]agentActivity{}
		s.act.agents[id] = m
	}
	old, had := m[thread]
	m[thread] = a
	if had && old.doing == a.doing && old.path == a.path && old.page == a.page {
		return
	}
	s.armActivityLocked(id)
}

// clearThreadActivity 清掉线程在全部 artifact 上的活动，并给受影响的 artifact 安排发送。
func (s *Service) clearThreadActivity(thread string) {
	s.act.mu.Lock()
	defer s.act.mu.Unlock()
	for id, m := range s.act.agents {
		if _, ok := m[thread]; !ok {
			continue
		}
		delete(m, thread)
		if len(m) == 0 {
			delete(s.act.agents, id)
		}
		s.armActivityLocked(id)
	}
}

// activityArgs 是 read_artifact / update_artifact 里决定活动的那几个参数（其余字段忽略）。
type activityArgs struct {
	Path       string          `json:"path"`
	HTML       *string         `json:"html"`
	HTMLSource *string         `json:"htmlSource"`
	Files      []fileParam     `json:"files"`
	Edits      []editArg       `json:"edits"`
	Canvas     json.RawMessage `json:"canvas"`
}

// recordActivity 在一次成功的 agent 操作之后记下调用线程的活动。没接在场出口、调用方不是线程时不记。
func (s *Service) recordActivity(ctx context.Context, c Caller, op string, params json.RawMessage, r Result) {
	if c.ThreadID == "" || (op != OpRead && op != OpUpdate) || s.activityHub() == nil {
		return
	}
	d, _ := r.Details.(obj)
	id, _ := d["id"].(string)
	if id == "" {
		return
	}
	var p activityArgs
	if len(params) > 0 && json.Unmarshal(params, &p) != nil {
		return
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return
	}
	act := agentActivity{at: s.now()}
	var paths []string
	if op == OpRead {
		act.doing = apigen.ArtifactDoingRead
		path, _ := d["path"].(string)
		paths = append(paths, path)
	} else {
		if p.HTML != nil || p.HTMLSource != nil {
			paths = append(paths, EntryHTML)
		}
		for _, f := range p.Files {
			paths = append(paths, f.Path)
		}
		for _, e := range p.Edits {
			if e.Path == "" {
				e.Path = a.Entry()
			}
			paths = append(paths, e.Path)
		}
		if len(paths) == 0 && len(p.Canvas) == 0 {
			return // 只改名：不是内容活动
		}
		act.doing = apigen.ArtifactDoingWrite
	}
	var doc canvas.Doc
	if a.Kind == KindCanvas {
		doc, _ = s.activityDoc(ctx, id)
	}
	act.path, act.page = focusOf(doc, paths)
	if op == OpUpdate && len(paths) == 0 {
		// 只有画布操作：新增画框记 draw，改画框的位置尺寸或层级记 move，其余是改 canvas.json
		act.path = EntryCanvas
		if ops, err := canvas.ParseOps(p.Canvas); err == nil {
			if doing, page := canvasDoing(doc, ops); doing != "" {
				act.doing, act.page = doing, page
				if i := slices.IndexFunc(doc.Pages, func(pg canvas.Page) bool { return pg.ID == page }); i >= 0 {
					act.path = doc.Pages[i].File
				}
			}
		}
	}
	s.setActivity(id, c.ThreadID, act)
}

// focusOf 从被读或改的文件里挑焦点：优先某个画框的文件（返回它的画框 id），否则第一个文件。
func focusOf(doc canvas.Doc, paths []string) (path, page string) {
	for _, p := range paths {
		if i := slices.IndexFunc(doc.Pages, func(pg canvas.Page) bool { return pg.File == p }); i >= 0 {
			return p, doc.Pages[i].ID
		}
	}
	if len(paths) > 0 {
		return paths[0], ""
	}
	return "", ""
}

// canvasDoing 归类一批画布操作：新增画框 → draw（最后一个新增的画框）；改画框的 x / y / width / height
// 或层级 → move（最后一个被挪的画框）。都没有时返回空。
func canvasDoing(doc canvas.Doc, ops []canvas.Op) (apigen.ArtifactThreadActivityDoing, string) {
	isPage := func(id string) bool {
		return slices.ContainsFunc(doc.Pages, func(pg canvas.Page) bool { return pg.ID == id })
	}
	var drawn, moved string
	for _, o := range ops {
		switch o.Op {
		case canvas.OpAdd:
			if o.ObjectType() == canvas.TypePage {
				drawn = o.ObjectID()
			}
		case canvas.OpSet:
			switch o.Key {
			case "x", "y", "width", "height":
				if isPage(o.ID) {
					moved = o.ID
				}
			}
		case canvas.OpOrder:
			if isPage(o.ID) {
				moved = o.ID
			}
		}
	}
	switch {
	case drawn != "":
		return apigen.ArtifactDoingDraw, drawn
	case moved != "":
		return apigen.ArtifactDoingMove, moved
	}
	return "", ""
}

// activityDoc 返回画布的当前文档（有 live 时取 live 的，否则读最新版本）。
func (s *Service) activityDoc(ctx context.Context, id string) (canvas.Doc, bool) {
	unlock := s.live.locks.lock(id)
	l := s.liveOf(id)
	var doc canvas.Doc
	if l != nil {
		doc = l.doc
	}
	unlock()
	if l != nil {
		return doc, true
	}
	_, doc, err := s.loadCanvas(ctx, id)
	return doc, err == nil
}

// TurnObserver 返回清理在场信息用的线程观察者（server 包 AddObserver）：线程的回合结束时清掉它的活动。
// 回调在线程锁内同步执行，这里只登记线程并唤醒 RunActivity，清理在后台做。
func (s *Service) TurnObserver() threads.Observer { return turnObserver{s} }

// RunActivity 处理回合结束的事件，直到 ctx 结束。
func (s *Service) RunActivity(ctx context.Context) {
	wake := s.activityWake()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			s.drainTurns()
		}
	}
}

func (s *Service) activityWake() chan struct{} {
	s.act.mu.Lock()
	defer s.act.mu.Unlock()
	if s.act.wake == nil {
		s.act.wake = make(chan struct{}, 1)
	}
	return s.act.wake
}

// turnEnded 登记回合结束的线程并唤醒后台（不阻塞）。
func (s *Service) turnEnded(thread string) {
	wake := s.activityWake()
	s.act.mu.Lock()
	if s.act.ended == nil {
		s.act.ended = map[string]bool{}
	}
	s.act.ended[thread] = true
	s.act.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

// drainTurns 清掉已登记的回合结束线程的活动。
func (s *Service) drainTurns() {
	s.act.mu.Lock()
	ended := s.act.ended
	s.act.ended = nil
	s.act.mu.Unlock()
	for t := range ended {
		s.clearThreadActivity(t)
	}
}

// turnObserver 把线程事件里的「回合结束」交给 Service。
type turnObserver struct{ s *Service }

var _ threads.Observer = turnObserver{}

// StateChanged：线程离开 working / awaiting_input（转入 idle、error 等）即回合结束；转入 idle 时投递等着它的设计便签。
func (o turnObserver) StateChanged(ev threads.StateEvent) {
	if ev.State == threads.StateIdle {
		o.s.notesIdle(ev.ThreadID)
	}
	if ev.State == threads.StateWorking || ev.State == threads.StateAwaitingInput {
		return
	}
	if ev.Prev == threads.StateWorking || ev.Prev == threads.StateAwaitingInput {
		o.s.turnEnded(ev.ThreadID)
	}
}

// TurnEnded：带标签的投递得到终态，同样是回合结束。
func (o turnObserver) TurnEnded(ev threads.TurnEvent) { o.s.turnEnded(ev.ThreadID) }

func (turnObserver) InputRequested(threads.InputEvent) {}
func (turnObserver) InputResolved(_, _ string)         {}
func (turnObserver) Seen(string)                       {}
func (turnObserver) EntriesAppended(string)            {}
