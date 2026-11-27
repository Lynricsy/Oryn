// Package browserrelay 实现 M27 客户端浏览器中继控制面：
// 维护在线的 Chrome 扩展设备连接（/api/v1/browser-relay/ws）、线程与扩展设备的持久化绑定，
// 并将扩展上报的过滤后 /ext 协议帧经 orb link（StreamBrowserRelay）桥接到 orb 内的 omp browser-relay。
package browserrelay

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/auth"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// WSPath 是扩展直连控制面的 WebSocket 路由（挂在 Deps.Mounts，不经 /api/v1 鉴权组）。
const WSPath = "/api/v1/browser-relay/ws"

// 扩展 WS 关闭码（与 schema/openapi.yaml 保持一致）。
const (
	closeRevoked  websocket.StatusCode = 4401
	closeReplaced websocket.StatusCode = 4409
	closeFull     websocket.StatusCode = 4429
)

// 业务错误码（供 api 层映射为 HTTP 状态码）。
var (
	ErrThreadNotFound = errors.New("browserrelay: thread not found")
	ErrThreadNotWork  = errors.New("browserrelay: only work threads can bind a local browser")
	ErrDeviceNotFound = errors.New("browserrelay: device not found")
	ErrDeviceInvalid  = errors.New("browserrelay: device is not an active browser extension")
)

// TicketRedeemer 兑换一次性 WS 票据（auth.Tickets 实现）。
type TicketRedeemer interface {
	Redeem(ticket string) (auth.Principal, bool)
}

// PrincipalChecker 复核设备主体当前是否未吊销（auth.Service 实现）。
type PrincipalChecker interface {
	PrincipalValid(ctx context.Context, p auth.Principal) (bool, error)
}

// OrbDialer 抽象对 orb 打开 browser_relay 流与查询连接状态的能力（orbs.Service 实现）。
type OrbDialer interface {
	Connected(orbID string) bool
	HasFeature(orbID, feature string) bool
	DialBrowserRelay(ctx context.Context, orbID, threadID string) (net.Conn, error)
}

// Broadcaster 向 Web / App 客户端推送状态变更（realtime.Hub 实现）。
type Broadcaster interface {
	BrowserRelayState(state apigen.ThreadBrowserRelay)
	BrowserRelayClients(clients []apigen.BrowserRelayClient)
}

// ThreadDeliverer 向线程投递自动化提示消息（threads.Service 实现）。
type ThreadDeliverer interface {
	Deliver(ctx context.Context, threadID string, m threads.AutoMessage, ack func(err error)) (cancel func() bool, err error)
}

// Config 是 browserrelay.Service 的可选配置。
type Config struct {
	Valid   PrincipalChecker
	Hub     Broadcaster
	Deliver ThreadDeliverer
}

// sessionStats 保存某线程由扩展上报的实时共享与审批计数（内存态，断连即清零）。
type sessionStats struct {
	sharedTabCount       int
	pendingApprovals     int
	autoApproveCreateTab *bool
}

// Service 管理全部在线浏览器扩展与线程桥接会话。
type Service struct {
	st      *store.Store
	orbs    OrbDialer
	tickets TicketRedeemer
	cfg     Config

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	clients map[string]*extClient     // deviceID -> 在线扩展连接
	bridges map[string]*sessionBridge // threadID -> 当前活动的 orb 桥接流
	stats   map[string]sessionStats   // threadID -> 扩展上报的共享/审批计数
}

// New 创建客户端浏览器中继服务。
func New(cfg Config, st *store.Store, orbSvc OrbDialer, tickets TicketRedeemer) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		st:      st,
		orbs:    orbSvc,
		tickets: tickets,
		cfg:     cfg,
		ctx:     ctx,
		cancel:  cancel,
		clients: map[string]*extClient{},
		bridges: map[string]*sessionBridge{},
		stats:   map[string]sessionStats{},
	}
}

// SetBroadcaster 允许在 Hub 晚于 Service 构造时补挂推送器。
func (s *Service) SetBroadcaster(h Broadcaster) {
	s.mu.Lock()
	s.cfg.Hub = h
	s.mu.Unlock()
}

// Close 关闭全部在线扩展连接与 orb 桥接流。
func (s *Service) Close() {
	s.cancel()
	s.mu.Lock()
	clients := make([]*extClient, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	bridges := make([]*sessionBridge, 0, len(s.bridges))
	for _, b := range s.bridges {
		bridges = append(bridges, b)
	}
	s.clients = map[string]*extClient{}
	s.bridges = map[string]*sessionBridge{}
	s.stats = map[string]sessionStats{}
	s.mu.Unlock()
	for _, b := range bridges {
		b.close()
	}
	for _, c := range clients {
		c.closeWith(websocket.StatusGoingAway, "server shutting down")
	}
}

// Revoke 响应 auth.Revocations：设备吊销时立即断开扩展 WS、清除其绑定与桥接流。
func (s *Service) Revoke(kind, id string) {
	if kind != auth.KindDevice {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bindings, _ := s.st.Q.ListThreadBrowserRelayBindingsByDevice(ctx, id)
	_, _ = s.st.Q.DeleteThreadBrowserRelayBindingsByDevice(ctx, id)

	s.mu.Lock()
	c := s.clients[id]
	if c != nil {
		delete(s.clients, id)
	}
	var toClose []*sessionBridge
	affected := make([]string, 0, len(bindings))
	for _, b := range bindings {
		affected = append(affected, b.ThreadID)
		delete(s.stats, b.ThreadID)
		if br := s.bridges[b.ThreadID]; br != nil && br.deviceID == id {
			delete(s.bridges, b.ThreadID)
			toClose = append(toClose, br)
		}
	}
	// 同时关闭任何绑定记录已删但内存中仍挂在此设备上的桥接
	for tid, br := range s.bridges {
		if br.deviceID == id {
			delete(s.bridges, tid)
			delete(s.stats, tid)
			toClose = append(toClose, br)
			affected = append(affected, tid)
		}
	}
	s.mu.Unlock()

	for _, br := range toClose {
		br.close()
	}
	if c != nil {
		c.closeWith(closeRevoked, "revoked")
	}
	for _, tid := range dedupStrings(affected) {
		s.emitThreadState(ctx, tid)
	}
	s.emitClients(ctx)
}

// OnOrbEvent 监听 orb 生命周期事件：orb link 恢复/就绪时，自动为该 orb 下已绑定在线扩展的线程重连桥接流。
func (s *Service) OnOrbEvent(ev orbs.Event) {
	if ev.Kind != orbs.EventLinkUp || ev.OrbID == "" {
		return
	}
	go func(orbID string) {
		ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
		defer cancel()
		rows, err := s.st.Q.ListThreadBrowserRelayBindings(ctx)
		if err != nil {
			return
		}
		for _, row := range rows {
			th, err := s.st.Q.GetThread(ctx, row.ThreadID)
			if err != nil || th.OrbID == nil || *th.OrbID != orbID {
				continue
			}
			s.ensureBridge(ctx, row.ThreadID)
		}
	}(ev.OrbID)
}

// ListClients 返回当前在线的全部本机浏览器扩展客户端及其已绑定线程摘要。
func (s *Service) ListClients(ctx context.Context) []apigen.BrowserRelayClient {
	bindingsByDev := map[string][]string{}
	if rows, err := s.st.Q.ListThreadBrowserRelayBindings(ctx); err == nil {
		for _, r := range rows {
			bindingsByDev[r.DeviceID] = append(bindingsByDev[r.DeviceID], r.ThreadID)
		}
	}

	s.mu.Lock()
	out := make([]apigen.BrowserRelayClient, 0, len(s.clients))
	for _, c := range s.clients {
		c.metaMu.RLock()
		browserVer := c.browserVersion
		extVer := c.extVersion
		c.metaMu.RUnlock()

		tids := bindingsByDev[c.deviceID]
		summaries := make([]apigen.BrowserRelayBindingSummary, 0, len(tids))
		for _, tid := range tids {
			st := s.stats[tid]
			summaries = append(summaries, apigen.BrowserRelayBindingSummary{
				ThreadID:             tid,
				SharedTabCount:       st.sharedTabCount,
				PendingApprovals:     st.pendingApprovals,
				AutoApproveCreateTab: st.autoApproveCreateTab,
			})
		}
		sort.Slice(summaries, func(i, j int) bool { return summaries[i].ThreadID < summaries[j].ThreadID })

		item := apigen.BrowserRelayClient{
			DeviceID:    c.deviceID,
			Name:        c.deviceName,
			ConnectedAt: c.connectedAt,
			Bindings:    summaries,
		}
		if browserVer != "" {
			v := browserVer
			item.BrowserVersion = &v
		}
		if extVer != "" {
			v := extVer
			item.ExtVersion = &v
		}
		out = append(out, item)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].ConnectedAt != out[j].ConnectedAt {
			return out[i].ConnectedAt < out[j].ConnectedAt
		}
		return out[i].DeviceID < out[j].DeviceID
	})
	return out
}

// GetThreadState 查询指定线程的本机浏览器绑定与在线状态。
func (s *Service) GetThreadState(ctx context.Context, threadID string) (apigen.ThreadBrowserRelay, error) {
	if _, err := s.st.Q.GetThread(ctx, threadID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apigen.ThreadBrowserRelay{}, ErrThreadNotFound
		}
		return apigen.ThreadBrowserRelay{}, err
	}
	return s.threadStateSnapshot(ctx, threadID), nil
}

// BindThread 把工作线程绑定到指定的 browser_extension 设备，若设备与 orb 均在线则立刻建立桥接流。
func (s *Service) BindThread(ctx context.Context, threadID, deviceID string) (apigen.ThreadBrowserRelay, error) {
	th, err := s.st.Q.GetThread(ctx, threadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apigen.ThreadBrowserRelay{}, ErrThreadNotFound
		}
		return apigen.ThreadBrowserRelay{}, err
	}
	if th.Kind != "work" {
		return apigen.ThreadBrowserRelay{}, ErrThreadNotWork
	}
	dev, err := s.st.Q.GetDevice(ctx, deviceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apigen.ThreadBrowserRelay{}, ErrDeviceNotFound
		}
		return apigen.ThreadBrowserRelay{}, err
	}
	if dev.Kind != string(apigen.BrowserExtension) || dev.RevokedAt != nil {
		return apigen.ThreadBrowserRelay{}, ErrDeviceInvalid
	}

	// 若此前绑在别的设备上，先关闭旧设备侧会话与桥接
	var prevDeviceID string
	if prev, err := s.st.Q.GetThreadBrowserRelayBinding(ctx, threadID); err == nil && prev.DeviceID != deviceID {
		prevDeviceID = prev.DeviceID
	}

	now := time.Now().UnixMilli()
	if err := s.st.Q.UpsertThreadBrowserRelayBinding(ctx, dbUpsertParams(threadID, deviceID, now)); err != nil {
		return apigen.ThreadBrowserRelay{}, err
	}

	if prevDeviceID != "" {
		s.closeBridge(threadID, "rebound")
	}
	s.ensureBridge(ctx, threadID)

	state := s.threadStateSnapshot(ctx, threadID)
	s.emitThreadStateVal(state)
	s.emitClients(ctx)
	return state, nil
}

// UnbindThread 幂等解除线程与本机浏览器扩展的绑定。
func (s *Service) UnbindThread(ctx context.Context, threadID string) (apigen.ThreadBrowserRelay, error) {
	if _, err := s.st.Q.GetThread(ctx, threadID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apigen.ThreadBrowserRelay{}, ErrThreadNotFound
		}
		return apigen.ThreadBrowserRelay{}, err
	}
	_, err := s.st.Q.DeleteThreadBrowserRelayBinding(ctx, threadID)
	if err != nil {
		return apigen.ThreadBrowserRelay{}, err
	}
	s.closeBridge(threadID, "unbound")

	state := s.threadStateSnapshot(ctx, threadID)
	s.emitThreadStateVal(state)
	s.emitClients(ctx)
	return state, nil
}

func (s *Service) threadStateSnapshot(ctx context.Context, threadID string) apigen.ThreadBrowserRelay {
	out := apigen.ThreadBrowserRelay{
		ThreadID: threadID,
		Bound:    false,
		Online:   false,
	}
	row, err := s.st.Q.GetThreadBrowserRelayBinding(ctx, threadID)
	if err != nil || row.DeviceRevokedAt != nil {
		return out
	}
	devID := row.DeviceID
	devName := row.DeviceName
	out.Bound = true
	out.DeviceID = &devID
	out.DeviceName = &devName

	s.mu.Lock()
	_, online := s.clients[row.DeviceID]
	st := s.stats[threadID]
	s.mu.Unlock()
	out.Online = online
	if online {
		out.SharedTabCount = st.sharedTabCount
		out.PendingApprovals = st.pendingApprovals
		out.AutoApproveCreateTab = st.autoApproveCreateTab
	}
	return out
}

func (s *Service) emitThreadState(ctx context.Context, threadID string) {
	s.emitThreadStateVal(s.threadStateSnapshot(ctx, threadID))
}

func (s *Service) emitThreadStateVal(state apigen.ThreadBrowserRelay) {
	s.mu.Lock()
	hub := s.cfg.Hub
	s.mu.Unlock()
	if hub != nil {
		hub.BrowserRelayState(state)
	}
}

func (s *Service) emitClients(ctx context.Context) {
	s.mu.Lock()
	hub := s.cfg.Hub
	s.mu.Unlock()
	if hub != nil {
		hub.BrowserRelayClients(s.ListClients(ctx))
	}
}

// threadSummary 是发给 Chrome 扩展 Popup 供用户选择绑定的活跃工作线程摘要。
type threadSummary struct {
	ThreadID    string `json:"threadId"`
	Title       string `json:"title"`
	ProjectName string `json:"projectName,omitempty"`
	State       string `json:"state"`
}

func (s *Service) activeWorkThreads(ctx context.Context) []threadSummary {
	workKind := "work"
	archivedFalse := false
	rows, err := s.st.Q.ListThreads(ctx, db.ListThreadsParams{Kind: &workKind, Archived: &archivedFalse})
	if err != nil {
		return []threadSummary{}
	}
	projNames := map[string]string{}
	if projs, err := s.st.Q.ListProjects(ctx, nil); err == nil {
		for _, p := range projs {
			projNames[p.ID] = p.Name
		}
	}
	out := make([]threadSummary, 0, len(rows))
	for _, r := range rows {
		if r.Kind != "work" || r.ArchivedAt != nil || r.Hidden != 0 {
			continue
		}
		var pname string
		if r.ProjectID != nil {
			pname = projNames[*r.ProjectID]
		}
		title := strings.TrimSpace(r.Title)
		if title == "" {
			title = r.ID
		}
		out = append(out, threadSummary{
			ThreadID:    r.ID,
			Title:       title,
			ProjectName: pname,
			State:       r.State,
		})
	}
	return out
}

// notifyApprovalResolved 在用户于扩展 Popup 作出审批决议后，向线程注入简短系统提示，
// 以便 agent 在任意等待时间（包括超过 bridge.ts 20s 超时窗口）后安全重试或改用其它方案。
func (s *Service) notifyApprovalResolved(threadID, decision, summary string) {
	s.mu.Lock()
	deliver := s.cfg.Deliver
	s.mu.Unlock()
	if deliver == nil {
		return
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = "本机浏览器操作"
	}
	var text string
	switch decision {
	case "approved_once":
		text = fmt.Sprintf("[Oryn] 用户已在 Chrome 扩展中批准你的本机浏览器操作请求（%s，单次放行）。现在可以重试该操作。", summary)
	case "approved_session":
		text = fmt.Sprintf("[Oryn] 用户已在 Chrome 扩展中批准你的本机浏览器操作请求（%s，本会话同类操作自动放行）。现在可以重试该操作。", summary)
	default:
		text = fmt.Sprintf("[Oryn] 用户在 Chrome 扩展中拒绝了你的本机浏览器操作请求（%s）。请不要重复同一请求，改用其他方案或向用户说明。", summary)
	}
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		defer cancel()
		if _, err := deliver.Deliver(ctx, threadID, threads.AutoMessage{Text: text, Steer: true}, nil); err != nil {
			slog.Debug("投递本机浏览器审批结果到线程失败", "thread", threadID, "err", err)
		}
	}()
}

// HTTPStatusForError 把本包业务错误映射为 HTTP 状态码与错误码。
func HTTPStatusForError(err error) (int, string) {
	switch {
	case errors.Is(err, ErrThreadNotFound):
		return http.StatusNotFound, "thread_not_found"
	case errors.Is(err, ErrThreadNotWork):
		return http.StatusBadRequest, "invalid_thread"
	case errors.Is(err, ErrDeviceNotFound):
		return http.StatusNotFound, "device_not_found"
	case errors.Is(err, ErrDeviceInvalid):
		return http.StatusBadRequest, "invalid_device"
	default:
		return http.StatusInternalServerError, "internal"
	}
}

func dedupStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// marshalLine 把任意值序列化为紧凑 JSON（不含换行）。
func marshalLine(v any) ([]byte, error) {
	return json.Marshal(v)
}
