// Package notify 是通知服务（M5 计划 3.2–3.6）：把线程事件（Observer）去抖成通知，落库后按渠道与推送订阅投递，
// 失败重试并留记录；Telegram 渠道另做长轮询，支持在 Telegram 里作答 ask 与回复即发消息（忙时 steer、否则 prompt）。
//
// 并发约定：
//   - Observer 回调在线程服务持有线程锁时同步调用：只操作内存（定时器、未读集合）并入队，不碰库、不回调线程服务。
//   - 入队的事件由单个 goroutine 顺序处理（生成通知、解决待答、标记已读）；投递由另一个 goroutine 推进，
//     二者都可由测试同步驱动（export_test.go），测试不 sleep。
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/transcript"
	"github.com/Lynricsy/Oryn/internal/uiprompt"
	"github.com/Lynricsy/Oryn/internal/webpush"
)

// Threads 是通知服务需要的线程能力（threads.Service 实现）。
type Threads interface {
	Get(ctx context.Context, id string) (apigen.Thread, error)
	Command(ctx context.Context, threadID string, frame json.RawMessage, reply func(json.RawMessage)) error
	UIResponse(ctx context.Context, threadID string, frame json.RawMessage) error
}

// Broadcaster 把应用内通知推给已登录的客户端（realtime hub 实现）。
type Broadcaster interface {
	Notification(n apigen.Notification)
	// NotificationsRead 广播已读；threadID 为空 = 全部。
	NotificationsRead(threadID string)
}

// Config 是通知服务参数。
type Config struct {
	// PublicURL 是主站地址：渠道消息里的线程链接 = PublicURL + "/t/" + 线程 id。
	PublicURL string
	// DataDir 存放 vapid.key。
	DataDir string
	// Box 加解密渠道配置（与密钥库同一把主密钥）。
	Box *secrets.Box
	// Now 默认 time.Now。
	Now func() time.Time
	// AfterFunc 默认 time.AfterFunc；测试注入以手动触发。
	AfterFunc func(d time.Duration, f func()) (stop func())
	// HTTP 是渠道请求的客户端（默认超时 15s）。
	HTTP *http.Client
	// PollHTTP 是 Telegram getUpdates 的客户端（默认超时 70s）。
	PollHTTP *http.Client
	// DoneDelay / InputDelay 是去抖时长（默认 3s / 2s）。
	DoneDelay, InputDelay time.Duration
	// FCM 是 FCM 发送参数；FCM.Secret 为空 = 关闭 FCM（不登记、不投递 fcm 订阅）。
	FCM FCMConfig
}

const (
	defaultDoneDelay  = 3 * time.Second
	defaultInputDelay = 2 * time.Second
	httpTimeout       = 15 * time.Second
	pollTimeout       = 70 * time.Second
	retention         = 30 * 24 * time.Hour
	cleanupEvery      = time.Hour
)

// 通知种类。
const (
	KindDone  = "done"
	KindInput = "input"
	KindError = "error"
	// KindTest 是渠道「测试」的内部种类（不落库）。
	KindTest = "test"
)

// Error 是带 HTTP 状态与错误码的业务错误，API 层原样转成 {code, message}。
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errInvalidChannel(format string, a ...any) error {
	return &Error{Status: http.StatusBadRequest, Code: "invalid_channel", Message: fmt.Sprintf(format, a...)}
}

// ErrNotFound 表示渠道 / 订阅不存在。
var ErrNotFound = &Error{Status: http.StatusNotFound, Code: "not_found", Message: "not found"}

// Service 是通知服务。
type Service struct {
	cfg  Config
	st   *store.Store
	th   Threads
	out  Broadcaster
	keys *webpush.Keys

	// senders / pushSenders 是发送方注册表：渠道 kind → 构造器；订阅 kind → 构造器（M7 在此注册 apns / fcm）。
	senders     map[string]func(ch db.NotificationChannel, cfg []byte) (Sender, error)
	pushSenders map[string]func(sub db.PushSubscription) Sender
	// fcm 缓存 FCM 的 access token。
	fcm fcmTokenCache

	mu sync.Mutex
	// timers 是线程 → 该线程的去抖定时器（按 kind / 请求区分）。
	timers map[string]map[timerKey]*pendingTimer
	// unread 是有未读通知的线程（Seen 的 O(1) 快速路径）。
	unread map[string]bool
	// emitGen 在每次 emit 预登记未读时递增；emitting 是已预登记、尚未结束的 emit 数。REST 的 MarkRead 据此判断
	// 它的 SQL 是否可能早于某条新通知落库：可能时不清内存未读（宁可多一次 Seen 标读，不能让库里的未读失去登记）。
	emitGen  uint64
	emitting int
	queue    []event
	wake     chan struct{}
	// deliverWake 唤醒投递循环。
	deliverWake chan struct{}

	// stateMu 串行化渠道 state 的读改写（Telegram offset / 绑定码 / 绑定结果）。
	stateMu sync.Mutex

	tgMu    sync.Mutex
	runCtx  context.Context
	pollers map[string]*poller
}

type timerKey struct {
	kind    string // done | error | input
	request string // input：请求 id
}

// pendingTimer 是一个去抖定时器；触发后仍留在 timers 里直到被处理，期间的取消（Seen / 回到 working）仍然生效。
type pendingTimer struct {
	stop   func()
	reason string          // error
	frame  json.RawMessage // input
	plan   bool
}

type eventKind int

const (
	evEmit eventKind = iota
	evResolve
	evRead
)

type event struct {
	kind      eventKind
	threadID  string
	key       timerKey
	timer     *pendingTimer
	requestID string
}

// New 创建通知服务：加载（必要时生成）VAPID 密钥、装载未读线程集合。
func New(cfg Config, st *store.Store, th Threads, out Broadcaster) (*Service, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.AfterFunc == nil {
		cfg.AfterFunc = func(d time.Duration, f func()) func() {
			t := time.AfterFunc(d, f)
			return func() { t.Stop() }
		}
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: httpTimeout}
	}
	if cfg.PollHTTP == nil {
		cfg.PollHTTP = &http.Client{Timeout: pollTimeout}
	}
	if cfg.DoneDelay <= 0 {
		cfg.DoneDelay = defaultDoneDelay
	}
	if cfg.InputDelay <= 0 {
		cfg.InputDelay = defaultInputDelay
	}
	if cfg.Box == nil {
		return nil, errors.New("notify: Config.Box is required")
	}
	if cfg.FCM.Secret != "" && cfg.FCM.Secrets == nil {
		return nil, errors.New("notify: Config.FCM.Secrets is required when FCM.Secret is set")
	}
	keys, err := webpush.LoadOrCreateKeys(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Service{
		cfg: cfg, st: st, th: th, out: out, keys: keys,
		timers:      map[string]map[timerKey]*pendingTimer{},
		unread:      map[string]bool{},
		wake:        make(chan struct{}, 1),
		deliverWake: make(chan struct{}, 1),
		pollers:     map[string]*poller{},
	}
	s.registerSenders()
	if s.fcmEnabled() {
		// 只提示不阻止启动：密钥可以在启动后再用 `oryn secrets set` 写入
		if _, err := s.fcmAccount(context.Background()); err != nil {
			slog.Warn("notify: FCM 已配置但服务账号不可用", "secret", cfg.FCM.Secret, "err", err)
		}
	}
	if err := s.loadUnread(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) now() int64 { return s.cfg.Now().UnixMilli() }

func (s *Service) loadUnread(ctx context.Context) error {
	ts, err := s.st.Q.ListUnreadNotificationThreads(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.unread = make(map[string]bool, len(ts))
	for _, t := range ts {
		s.unread[t] = true
	}
	s.mu.Unlock()
	return nil
}

// Run 启动事件处理、投递、保留期清理与 Telegram 轮询，直到 ctx 结束。
func (s *Service) Run(ctx context.Context) {
	s.tgMu.Lock()
	s.runCtx = ctx
	s.tgMu.Unlock()
	s.reloadTelegram(ctx)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s.eventLoop(ctx)
	}()
	go func() {
		defer wg.Done()
		s.deliverLoop(ctx)
	}()
	wg.Wait()
	s.tgMu.Lock()
	for id, p := range s.pollers {
		p.cancel()
		delete(s.pollers, id)
	}
	s.runCtx = nil
	s.tgMu.Unlock()
}

func (s *Service) eventLoop(ctx context.Context) {
	cleanup := time.NewTicker(cleanupEvery)
	defer cleanup.Stop()
	s.cleanup(ctx)
	for {
		s.processEvents(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-cleanup.C:
			s.cleanup(ctx)
		}
	}
}

// cleanup 删除超过保留期的通知，并重新装载未读集合。
func (s *Service) cleanup(ctx context.Context) {
	n, err := s.st.Q.DeleteNotificationsBefore(ctx, s.cfg.Now().Add(-retention).UnixMilli())
	if err != nil {
		slog.Warn("notify: 清理过期通知失败", "err", err)
		return
	}
	if n > 0 {
		if err := s.loadUnread(ctx); err != nil {
			slog.Warn("notify: 装载未读线程失败", "err", err)
		}
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// ---- Observer（持线程锁调用：只操作内存并入队）----

var _ threads.Observer = (*Service)(nil)

// StateChanged 实现 threads.Observer：完成 / 出错排去抖定时器，其余状态变化取消它们。
func (s *Service) StateChanged(ev threads.StateEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	done, errk := timerKey{kind: KindDone}, timerKey{kind: KindError}
	switch {
	case ev.State == threads.StateIdle && (ev.Prev == threads.StateWorking || ev.Prev == threads.StateAwaitingInput) && !ev.Focused:
		s.cancelLocked(ev.ThreadID, errk)
		s.armLocked(ev.ThreadID, done, s.cfg.DoneDelay, &pendingTimer{})
	case ev.State == threads.StateError && ev.Prev != threads.StateError && !ev.Focused:
		s.cancelLocked(ev.ThreadID, done)
		s.armLocked(ev.ThreadID, errk, s.cfg.DoneDelay, &pendingTimer{reason: ev.Reason})
	default:
		s.cancelLocked(ev.ThreadID, done)
		s.cancelLocked(ev.ThreadID, errk)
	}
}

// InputRequested 实现 threads.Observer：无人在看时按 (线程, 请求) 排去抖定时器。
func (s *Service) InputRequested(ev threads.InputEvent) {
	if ev.Focused {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	frame := append(json.RawMessage(nil), ev.Frame...)
	s.armLocked(ev.ThreadID, timerKey{kind: KindInput, request: ev.RequestID}, s.cfg.InputDelay, &pendingTimer{frame: frame, plan: ev.Plan})
}

// InputResolved 实现 threads.Observer：取消对应定时器，并在后台把已发出的通知标为已解决、撤回 Telegram 按钮。
func (s *Service) InputResolved(threadID, requestID string) {
	s.mu.Lock()
	s.cancelLocked(threadID, timerKey{kind: KindInput, request: requestID})
	s.queue = append(s.queue, event{kind: evResolve, threadID: threadID, requestID: requestID})
	s.mu.Unlock()
	signal(s.wake)
}

// Seen 实现 threads.Observer（热路径）：线程没有定时器、也没有未读通知时直接返回。
func (s *Service) Seen(threadID string) {
	s.mu.Lock()
	tm := s.timers[threadID]
	unread := s.unread[threadID]
	if len(tm) == 0 && !unread {
		s.mu.Unlock()
		return
	}
	for k, pt := range tm {
		pt.stop()
		delete(tm, k)
	}
	delete(s.timers, threadID)
	if unread {
		delete(s.unread, threadID)
		s.queue = append(s.queue, event{kind: evRead, threadID: threadID})
	}
	s.mu.Unlock()
	if unread {
		signal(s.wake)
	}
}

// armLocked 排（或重置）一个定时器。调用方持有 s.mu。
func (s *Service) armLocked(threadID string, key timerKey, d time.Duration, pt *pendingTimer) {
	s.cancelLocked(threadID, key)
	tm := s.timers[threadID]
	if tm == nil {
		tm = map[timerKey]*pendingTimer{}
		s.timers[threadID] = tm
	}
	tm[key] = pt
	pt.stop = s.cfg.AfterFunc(d, func() { s.fire(threadID, key, pt) })
}

// cancelLocked 取消一个定时器（含已触发、尚未处理的）。调用方持有 s.mu。
func (s *Service) cancelLocked(threadID string, key timerKey) {
	tm := s.timers[threadID]
	pt := tm[key]
	if pt == nil {
		return
	}
	pt.stop()
	delete(tm, key)
	if len(tm) == 0 {
		delete(s.timers, threadID)
	}
}

func (s *Service) fire(threadID string, key timerKey, pt *pendingTimer) {
	s.mu.Lock()
	if s.timers[threadID][key] != pt {
		s.mu.Unlock()
		return
	}
	s.queue = append(s.queue, event{kind: evEmit, threadID: threadID, key: key, timer: pt})
	s.mu.Unlock()
	signal(s.wake)
}

// processEvents 顺序处理已入队的事件，直到队列为空。
func (s *Service) processEvents(ctx context.Context) {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		ev := s.queue[0]
		s.queue = s.queue[1:]
		if ev.kind == evEmit {
			// 触发后被取消（Seen / 状态回到 working / 请求已作答）：放弃
			if s.timers[ev.threadID][ev.key] != ev.timer {
				s.mu.Unlock()
				continue
			}
			s.cancelLocked(ev.threadID, ev.key)
			// 先登记未读：此后到达的 Seen 会在本通知落库之后把它标为已读
			s.unread[ev.threadID] = true
			s.emitGen++
			s.emitting++
		}
		s.mu.Unlock()
		switch ev.kind {
		case evEmit:
			s.emit(ctx, ev.threadID, ev.key, ev.timer)
			s.mu.Lock()
			s.emitting--
			s.mu.Unlock()
		case evResolve:
			s.resolve(ctx, ev.threadID, ev.requestID)
		case evRead:
			s.markThreadRead(ctx, ev.threadID)
		}
	}
}

// ---- 生成 ----

// emit 生成一条通知：落库、为每个匹配的渠道与推送订阅排一条待投递，然后广播并唤醒投递循环。
func (s *Service) emit(ctx context.Context, threadID string, key timerKey, pt *pendingTimer) {
	th, err := s.th.Get(ctx, threadID)
	if err != nil {
		if !errors.Is(err, threads.ErrNotFound) {
			slog.Warn("notify: 读取线程失败", "thread", threadID, "err", err)
		}
		s.dropUnreadIfNone(ctx, threadID)
		return
	}
	// 去抖期间线程被归档：用户已明确不再关心它，不再发通知（归档线程也不计入未读与托盘状态）
	if th.ArchivedAt != nil {
		s.dropUnreadIfNone(ctx, threadID)
		return
	}
	p := db.InsertNotificationParams{ID: ids.New("ntf"), ThreadID: &threadID, Kind: key.kind, Title: th.Title, CreatedAt: s.now()}
	switch key.kind {
	case KindDone:
		p.Body = s.lastAssistantText(ctx, threadID)
	case KindError:
		p.Body = pt.reason
	case KindInput:
		p.RequestID, p.Request = new(key.request), new(string(pt.frame))
		if !pt.plan {
			p.Body = uiprompt.Describe(pt.frame, uiprompt.Labels{}).Title
		}
	}
	err = s.record(ctx, p)
	if errors.Is(err, errDuplicate) {
		// 同一未决请求已通知过（控制面重启后 orbd 重放了它）
		s.dropUnreadIfNone(ctx, threadID)
		return
	}
	if err != nil {
		slog.Warn("notify: 写入通知失败", "thread", threadID, "kind", key.kind, "err", err)
		s.dropUnreadIfNone(ctx, threadID)
	}
}

// errDuplicate 表示同一未决请求的通知已存在（InsertNotification 忽略了重复行）。
var errDuplicate = errors.New("notify: duplicate notification")

// record 落库一条通知、为每个匹配的渠道与推送订阅排一条待投递，然后广播并唤醒投递循环（emit 与 Post 共用）。
func (s *Service) record(ctx context.Context, p db.InsertNotificationParams) error {
	now := p.CreatedAt
	var dels []db.NotificationDelivery
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		n, err := q.InsertNotification(ctx, p)
		if err != nil {
			return err
		}
		if n == 0 {
			return errDuplicate
		}
		chs, err := q.ListNotificationChannels(ctx)
		if err != nil {
			return err
		}
		for _, ch := range chs {
			if !ch.Enabled || !hasEvent(ch.Events, p.Kind) {
				continue
			}
			d := db.InsertNotificationDeliveryParams{ID: ids.New("ndl"), NotificationID: p.ID, ChannelID: new(ch.ID), Target: ch.Kind, NextAttemptAt: now, CreatedAt: now}
			if err := q.InsertNotificationDelivery(ctx, d); err != nil {
				return err
			}
			dels = append(dels, pendingRow(d))
		}
		subs, err := q.ListDeliverablePushSubscriptions(ctx)
		if err != nil {
			return err
		}
		for _, sub := range subs {
			if s.pushSenders[sub.Kind] == nil {
				continue
			}
			d := db.InsertNotificationDeliveryParams{ID: ids.New("ndl"), NotificationID: p.ID, SubscriptionID: new(sub.ID), Target: sub.Kind, NextAttemptAt: now, CreatedAt: now}
			if err := q.InsertNotificationDelivery(ctx, d); err != nil {
				return err
			}
			dels = append(dels, pendingRow(d))
		}
		return nil
	})
	if err != nil {
		return err
	}
	names, _ := s.channelNames(ctx)
	s.out.Notification(notificationAPI(db.Notification{
		ID: p.ID, ThreadID: p.ThreadID, Kind: p.Kind, Title: p.Title, Body: p.Body, RequestID: p.RequestID, Request: p.Request, CreatedAt: now,
	}, dels, names))
	if len(dels) > 0 {
		signal(s.deliverWake)
	}
	return nil
}

// AutomationsPath 是没有线程的通知（定时无法建出线程、定时被自动暂停）在 Web 上的落点。
const AutomationsPath = "/settings/automations"

// Post 立即生成一条 kind = error 的通知（M9 定时补「线程状态机看不到」的失败，不去抖）。
// threadID 非空时标题取线程标题（忽略 title），线程不存在或已归档则不发；threadID 为空时用 title，
// 链接指向 AutomationsPath。
func (s *Service) Post(ctx context.Context, threadID, title, body string) error {
	p := db.InsertNotificationParams{ID: ids.New("ntf"), Kind: KindError, Title: title, Body: body}
	if threadID == "" {
		p.CreatedAt = s.now()
		return s.record(ctx, p)
	}
	th, err := s.th.Get(ctx, threadID)
	if err != nil {
		if errors.Is(err, threads.ErrNotFound) {
			return nil
		}
		return err
	}
	if th.ArchivedAt != nil {
		return nil
	}
	p.ThreadID, p.Title = &threadID, th.Title
	// 与 processEvents 相同：先登记未读，此后到达的 Seen 会在本通知落库之后把它标为已读
	s.mu.Lock()
	s.unread[threadID] = true
	s.emitGen++
	s.emitting++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.emitting--
		s.mu.Unlock()
	}()
	p.CreatedAt = s.now()
	if err := s.record(ctx, p); err != nil {
		s.dropUnreadIfNone(ctx, threadID)
		return err
	}
	return nil
}

// dropUnreadIfNone 在预先登记的未读没有落成通知时，按库里的实际情况更正未读集合。
func (s *Service) dropUnreadIfNone(ctx context.Context, threadID string) {
	ts, err := s.st.Q.ListUnreadNotificationThreads(ctx)
	if err != nil {
		return
	}
	for _, t := range ts {
		if t == threadID {
			return
		}
	}
	s.mu.Lock()
	delete(s.unread, threadID)
	s.mu.Unlock()
}

func pendingRow(d db.InsertNotificationDeliveryParams) db.NotificationDelivery {
	next := d.NextAttemptAt
	return db.NotificationDelivery{ID: d.ID, NotificationID: d.NotificationID, ChannelID: d.ChannelID, SubscriptionID: d.SubscriptionID,
		Target: d.Target, Status: statusPending, NextAttemptAt: &next, CreatedAt: d.CreatedAt}
}

// lastAssistantText 返回最近一条有文本的 assistant 消息的摘要（≤200 字符），没有则空。
func (s *Service) lastAssistantText(ctx context.Context, threadID string) string {
	rows, err := s.st.Q.ListRecentMainMessages(ctx, db.ListRecentMainMessagesParams{ThreadID: threadID, Limit: 20})
	if err != nil {
		return ""
	}
	for _, r := range rows {
		m, ok := transcript.ParseMessage(transcript.Entry{Seq: r.Seq, Type: "message", Payload: r.Payload})
		if ok && m.Role == "assistant" && m.Text != "" {
			return transcript.Snippet(m.Text, 200)
		}
	}
	return ""
}

// resolve 把该请求的未决通知标为已解决，并撤回它们在 Telegram 里的按钮。
func (s *Service) resolve(ctx context.Context, threadID, requestID string) {
	nids, err := s.st.Q.ResolveNotificationRequest(ctx, db.ResolveNotificationRequestParams{Now: s.now(), ThreadID: threadID, RequestID: requestID})
	if err != nil {
		slog.Warn("notify: 标记请求已解决失败", "thread", threadID, "err", err)
		return
	}
	for _, id := range nids {
		s.revokeButtons(ctx, id)
	}
}

func (s *Service) markThreadRead(ctx context.Context, threadID string) {
	n, err := s.st.Q.MarkThreadNotificationsRead(ctx, db.MarkThreadNotificationsReadParams{Now: s.now(), ThreadID: threadID})
	if err != nil {
		slog.Warn("notify: 标记已读失败", "thread", threadID, "err", err)
		return
	}
	if n > 0 {
		s.out.NotificationsRead(threadID)
	}
}

// ---- 查询与已读（REST）----

// List 返回最近的通知（createdAt < before，按时间倒序；unreadOnly 时只含未读）与全部未读数。
func (s *Service) List(ctx context.Context, limit int, before int64, unreadOnly bool) (apigen.NotificationList, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 100)
	if before <= 0 {
		before = math.MaxInt64
	}
	onlyUnread := int64(0)
	if unreadOnly {
		onlyUnread = 1
	}
	rows, err := s.st.Q.ListNotifications(ctx, db.ListNotificationsParams{Before: before, UnreadOnly: onlyUnread, Limit: int64(limit)})
	if err != nil {
		return apigen.NotificationList{}, err
	}
	byN := map[string][]db.NotificationDelivery{}
	if len(rows) > 0 {
		nids := make([]string, len(rows))
		for i, r := range rows {
			nids[i] = r.ID
		}
		dels, err := s.st.Q.ListDeliveriesByNotifications(ctx, nids)
		if err != nil {
			return apigen.NotificationList{}, err
		}
		for _, d := range dels {
			byN[d.NotificationID] = append(byN[d.NotificationID], d)
		}
	}
	names, err := s.channelNames(ctx)
	if err != nil {
		return apigen.NotificationList{}, err
	}
	unread, err := s.st.Q.CountUnreadNotifications(ctx)
	if err != nil {
		return apigen.NotificationList{}, err
	}
	out := apigen.NotificationList{Notifications: make([]apigen.Notification, 0, len(rows)), Unread: int(unread)}
	for _, r := range rows {
		out.Notifications = append(out.Notifications, notificationAPI(r, byN[r.ID], names))
	}
	return out, nil
}

// MarkRead 标记已读：threadID 为空 = 全部；随后广播。
//
// 与 emit 交错时（emit 已预登记未读、尚未落库，或在本次 SQL 与清集合之间落库），SQL 可能没标到那条新通知：
// 这时保留内存未读登记，之后的 Seen 会把它读掉，库与集合不失配。
func (s *Service) MarkRead(ctx context.Context, threadID string) error {
	s.mu.Lock()
	gen, busy := s.emitGen, s.emitting > 0
	s.mu.Unlock()
	var err error
	if threadID == "" {
		_, err = s.st.Q.MarkAllNotificationsRead(ctx, s.now())
	} else {
		_, err = s.st.Q.MarkThreadNotificationsRead(ctx, db.MarkThreadNotificationsReadParams{Now: s.now(), ThreadID: threadID})
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	if !busy && gen == s.emitGen {
		if threadID == "" {
			clear(s.unread)
		} else {
			delete(s.unread, threadID)
		}
	}
	s.mu.Unlock()
	s.out.NotificationsRead(threadID)
	return nil
}

func (s *Service) channelNames(ctx context.Context) (map[string]string, error) {
	chs, err := s.st.Q.ListNotificationChannels(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(chs))
	for _, c := range chs {
		m[c.ID] = c.Name
	}
	return m, nil
}

// notificationAPI 把通知行与其投递行转换成 API 形态。
func notificationAPI(n db.Notification, dels []db.NotificationDelivery, names map[string]string) apigen.Notification {
	out := apigen.Notification{
		ID: n.ID, Kind: apigen.NotificationKind(n.Kind), Title: n.Title, Body: n.Body, CreatedAt: n.CreatedAt,
		ThreadID: n.ThreadID, ReadAt: n.ReadAt, ResolvedAt: n.ResolvedAt, Deliveries: make([]apigen.NotificationDelivery, 0, len(dels)),
	}
	if n.ThreadID != nil {
		out.URL = "/t/" + *n.ThreadID
	} else {
		out.URL = AutomationsPath
	}
	for _, d := range dels {
		at := d.CreatedAt
		switch {
		case d.FinishedAt != nil:
			at = *d.FinishedAt
		case d.NextAttemptAt != nil:
			at = *d.NextAttemptAt
		}
		ad := apigen.NotificationDelivery{Target: apigen.NotificationDeliveryTarget(d.Target), Status: apigen.NotificationDeliveryStatus(d.Status),
			Attempts: int(d.Attempts), At: at, ChannelID: d.ChannelID, Error: d.LastError}
		if d.ChannelID != nil {
			if name, ok := names[*d.ChannelID]; ok {
				ad.ChannelName = new(name)
			}
		}
		out.Deliveries = append(out.Deliveries, ad)
	}
	return out
}

func hasEvent(eventsJSON, kind string) bool {
	var evs []string
	if json.Unmarshal([]byte(eventsJSON), &evs) != nil {
		return false
	}
	for _, e := range evs {
		if e == kind {
			return true
		}
	}
	return false
}
