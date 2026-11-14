package orbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Desktop 与浏览器 Profile（M8 §3.1）：orbd 经 desktop.state 上报桌面全量状态，控制面只存镜像（orb_desktops）；
// Profile 的同步与保存由 internal/desktop 经 DesktopHooks 插在生命周期的三个点上（进入 ready 之前、冻结之前、shutdown 之前）。

const (
	// FeatureDesktop 是 M8 orbd 的 hello 特性：有 X11 桌面、desk 流与 /desktop/* 本机 API。
	FeatureDesktop = "desktop"
	// EventDesktop 表示桌面镜像或 Profile 绑定变化（From == To == 当前状态）。
	EventDesktop = "desktop"
)

// profileModeTimeout 是 profile.mode 请求的应答超时。
const profileModeTimeout = 10 * time.Second

// deskDialTimeout 是开 desk 流加读到应答行的上限（变量仅为测试可缩短；生产代码不修改）。
var deskDialTimeout = 10 * time.Second

// DeskDialError 是 orbd 对 desk 流的拒绝；Code ∈ display_unavailable | failed | timeout。
type DeskDialError struct{ Code, Message string }

func (e *DeskDialError) Error() string {
	if e.Message == "" {
		return "orbs: desk dial rejected: " + e.Code
	}
	return "orbs: desk dial rejected: " + e.Code + ": " + e.Message
}

// DesktopHooks 由 internal/desktop 实现；每个方法自带超时，失败只记日志，不阻断生命周期。
// 只对 hello 声明了 desktop 的 link 调用。
type DesktopHooks interface {
	// BeforeReady 在 setup / resume 成功之后、进入 ready 之前调用（定下 orb 本地的 Profile 目录）。
	BeforeReady(ctx context.Context, orbID string)
	// NeedsFreezeSave 只读库：该 orb 独占持有 Profile 且有东西可存。
	NeedsFreezeSave(ctx context.Context, orbID string) bool
	// BeforeFreeze 在提交冻结之前调用（活体保存）。
	BeforeFreeze(ctx context.Context, orbID string)
	// BeforeShutdown 在向 orbd 发 shutdown 之前调用（关闭式保存）。
	BeforeShutdown(ctx context.Context, orbID string)
	// DesktopReported 在 orbd 报告的桌面状态写进镜像之后调用（不得阻塞）：报告里 orbd 本地的 Profile 使用方式
	// 与库里的绑定不一致时（例如迟到的 profile.mode 把当前持有者降成了只读），由实现方以库为准纠正。
	DesktopReported(orbID string, st apigen.LinkCtlDesktopStateParams)
}

// SetDesktopHooks 设置 Profile 生命周期钩子；须在 orb link 接入之前调用。
func (s *Service) SetDesktopHooks(h DesktopHooks) {
	s.hmu.Lock()
	s.deskHooks = h
	s.hmu.Unlock()
}

// desktopHooksFor 返回 l 适用的钩子：已设置钩子且 l 的 hello 含 desktop；否则 nil。
func (s *Service) desktopHooksFor(l *orbLink) DesktopHooks {
	if l == nil || !l.helloSeen() || !slices.Contains(l.hello.Features, FeatureDesktop) {
		return nil
	}
	s.hmu.RLock()
	defer s.hmu.RUnlock()
	return s.deskHooks
}

// currentLink 返回 orb 当前的活动 link（可能尚未 hello）。
func (s *Service) currentLink(orbID string) *orbLink {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.links[orbID]
}

// preFreeze 在冻结提交之前给 Profile 一次活体保存的机会：钩子判定有东西可存，且闸门确认线程静止
// （commit 返回 false，不提交任何变化）之后才调用 BeforeFreeze。冻结本身之后仍由 freezeTx 复核空闲条件。
func (s *Service) preFreeze(ctx context.Context, orbID string) {
	h := s.desktopHooksFor(s.currentLink(orbID))
	if h == nil || !h.NeedsFreezeSave(ctx, orbID) {
		return
	}
	quiet := false
	s.tryQuiesce(orbID, func() bool { quiet = true; return false })
	if !quiet {
		return
	}
	h.BeforeFreeze(ctx, orbID)
}

// ApplyDesktopState 把 orbd 报告的桌面状态写进镜像并发出 EventDesktop（desktop.state 通知与 REST 的浏览器启停共用）。
// browser 不合法则丢弃；orb 为 deleting / deleted 时不写。
func (s *Service) ApplyDesktopState(ctx context.Context, orbID string, st apigen.LinkCtlDesktopStateParams) {
	if !st.Browser.Valid() {
		slog.Warn("desktop.state 的 browser 无效，丢弃", "orb", orbID, "browser", truncUTF8(string(st.Browser), 32))
		return
	}
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		orb, err := q.GetOrb(ctx, orbID)
		if err != nil {
			return err
		}
		if orb.State == StateDeleting || orb.State == StateDeleted {
			return errOrbDeletedTx
		}
		return q.UpsertOrbDesktopState(ctx, db.UpsertOrbDesktopStateParams{
			OrbID: orbID, DisplayReady: b2i(st.DisplayReady), Browser: string(st.Browser), Dirty: b2i(st.Dirty), Now: s.nowMs(),
		})
	})
	if errors.Is(err, errOrbDeletedTx) {
		return
	}
	if err != nil {
		slog.Warn("写入桌面镜像失败", "orb", orbID, "err", err)
		return
	}
	s.RefreshDesktop(ctx, orbID)
	s.hmu.RLock()
	h := s.deskHooks
	s.hmu.RUnlock()
	if h != nil {
		h.DesktopReported(orbID, st)
	}
}

// onDesktopState 处理 desktop.state 通知。
func (s *Service) onDesktopState(ctx context.Context, orbID string, params json.RawMessage) {
	var p apigen.LinkCtlDesktopStateParams
	if err := json.Unmarshal(params, &p); err != nil {
		slog.Warn("desktop.state 参数无效", "orb", orbID, "err", err)
		return
	}
	s.ApplyDesktopState(ctx, orbID, p)
}

// RefreshDesktop 读取 orb 并发出 EventDesktop（桌面镜像或 Profile 绑定变化后推送 orb 更新）。
func (s *Service) RefreshDesktop(ctx context.Context, orbID string) {
	orb, err := s.Get(ctx, orbID)
	if err != nil {
		slog.Warn("刷新 orb 失败", "orb", orbID, "err", err)
		return
	}
	s.emitInfo(ctx, EventDesktop, orb)
}

// SendProfileMode 经 ctl 向 orbd 发 profile.mode（10s）。无 link 或 orbd 不支持桌面时返回 ErrOrbNotReady / ErrOrbdOutdated。
func (s *Service) SendProfileMode(ctx context.Context, orbID string, p apigen.LinkCtlProfileModeParams) error {
	l := s.currentLink(orbID)
	if l == nil || !l.helloSeen() {
		return fmt.Errorf("%w: orb %s has no link", ErrOrbNotReady, orbID)
	}
	if !slices.Contains(l.hello.Features, FeatureDesktop) {
		return ErrOrbdOutdated
	}
	cctx, cancel := context.WithTimeout(ctx, profileModeTimeout)
	defer cancel()
	var res apigen.LinkCtlOkResult
	return l.ctl.Call(cctx, string(apigen.MethodProfileMode), p, &res)
}

// DialDesk 在 orb 当前 link 上打开一条 desk 流，读出 orbd 的应答行后返回该流（之后是原始 RFB 字节）。
// 无已 hello 的 link → ErrOrbNotReady；hello 未声明 desktop → ErrOrbdOutdated；orbd 拒绝或应答超时 → *DeskDialError。
// 不检查 orb 状态（调用方先 EnsureUsable）。
func (s *Service) DialDesk(ctx context.Context, orbID string) (net.Conn, error) {
	l := s.currentLink(orbID)
	if l == nil || !l.helloSeen() {
		return nil, fmt.Errorf("%w: orb %s has no link", ErrOrbNotReady, orbID)
	}
	if !slices.Contains(l.hello.Features, FeatureDesktop) {
		return nil, ErrOrbdOutdated
	}
	deadline := time.Now().Add(deskDialTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	octx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	st, err := l.sess.Open(octx, link.StreamHeader{T: link.StreamDesk})
	if err != nil {
		if ctx.Err() == nil && octx.Err() != nil {
			return nil, &DeskDialError{Code: "timeout", Message: "opening the desk stream timed out"}
		}
		return nil, err
	}
	_ = st.SetReadDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = st.SetReadDeadline(time.Now()) })
	line, err := st.ReadLine(portalReplyMax)
	if !stop() {
		_ = st.Close()
		return nil, ctx.Err()
	}
	if err != nil {
		_ = st.Close()
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, &DeskDialError{Code: "timeout", Message: "orbd did not answer the desk stream in time"}
		}
		return nil, fmt.Errorf("orbs: read desk reply: %w", err)
	}
	var rep apigen.LinkDeskReply
	if err := json.Unmarshal(line, &rep); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("orbs: invalid desk reply: %w", err)
	}
	if !rep.Ok {
		_ = st.Close()
		e := &DeskDialError{Code: "failed"}
		if rep.Code != nil && *rep.Code != "" {
			e.Code = *rep.Code
		}
		if rep.Message != nil {
			e.Message = *rep.Message
		}
		return nil, e
	}
	_ = st.SetReadDeadline(time.Time{})
	return st, nil
}

// HolderView 返回使用 Profile 的 orb 的展示形态：线程（按创建时间）、代表线程（最早创建）及其标题、开始使用的时间。
func HolderView(ctx context.Context, q *db.Queries, orbID string, since int64) (apigen.BrowserProfileHolder, error) {
	ids, err := q.ListThreadIDsByOrb(ctx, &orbID)
	if err != nil {
		return apigen.BrowserProfileHolder{}, err
	}
	if ids == nil {
		ids = []string{}
	}
	h := apigen.BrowserProfileHolder{OrbID: orbID, ThreadIds: ids, Since: since}
	rep, err := q.GetOrbRepresentativeThread(ctx, orbID)
	switch {
	case err == nil:
		h.ThreadID, h.ThreadTitle = &rep.ID, &rep.Title
	case !errors.Is(err, sql.ErrNoRows):
		return apigen.BrowserProfileHolder{}, err
	}
	return h, nil
}

// DesktopView 返回 orb 的桌面形态；orbd 从未上报过 desktop.state（没有行或 reported=0）时为 nil。
//
// profile.status：profile_error 非空 → error；只读绑定 → readonly；持有者是本 orb → attached；
// 持有者是别的 orb → in_use（带 heldBy）；否则 released。revision = local_rev（未装载为 0）。
func DesktopView(ctx context.Context, q *db.Queries, orbID string) (*apigen.OrbDesktop, error) {
	d, err := q.GetOrbDesktop(ctx, orbID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if d.Reported == 0 {
		return nil, nil
	}
	out := &apigen.OrbDesktop{DisplayReady: d.DisplayReady != 0, Browser: apigen.OrbBrowserStatus(d.Browser)}
	if d.ProfileID == nil {
		return out, nil
	}
	p, err := q.GetBrowserProfile(ctx, *d.ProfileID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	v := &apigen.OrbDesktopProfile{
		ID: p.ID, Name: p.Name, Mode: apigen.BrowserProfileAccess(d.ProfileMode), Revision: max(d.LocalRev, 0),
	}
	switch {
	case d.ProfileError != nil:
		v.Status, v.Error = apigen.OrbDesktopProfileStatusError, d.ProfileError
	case d.ProfileMode == string(apigen.BrowserProfileAccessReadonly):
		v.Status = apigen.OrbDesktopProfileStatusReadonly
		if d.ReadonlyReason != nil {
			reason := apigen.BrowserProfileReadonlyReason(*d.ReadonlyReason)
			v.ReadonlyReason = &reason
			if reason == apigen.BrowserProfileReadonlyReasonTakenOver {
				v.TakenOverBy = &apigen.OrbDesktopTakenOver{At: deref64(d.ReadonlyAt), ThreadID: d.ReadonlyByThreadID, ThreadTitle: d.ReadonlyBy}
			}
		}
	case p.HolderOrbID != nil && *p.HolderOrbID == orbID:
		v.Status = apigen.OrbDesktopProfileStatusAttached
	case p.HolderOrbID != nil:
		v.Status = apigen.OrbDesktopProfileStatusInUse
		h, err := HolderView(ctx, q, *p.HolderOrbID, deref64(p.HeldAt))
		if err != nil {
			return nil, err
		}
		v.HeldBy = &h
	default:
		v.Status = apigen.OrbDesktopProfileStatusReleased
	}
	out.Profile = v
	return out, nil
}

// bindProfileIn 在事务内把 Profile 绑定到新 orb（mode 为 apigen.BrowserProfileMode，空 = exclusive）。
func bindProfileIn(ctx context.Context, q *db.Queries, orbID, profileID, mode string, now int64) error {
	return q.UpsertOrbDesktopProfile(ctx, ProfileBinding(orbID, profileID, apigen.BrowserProfileMode(mode), now))
}

// ProfileBinding 返回把 profileID（空 = 不用 Profile）以 mode 绑定到 orb 的参数：readonly → 只读副本（原因 requested）；
// takeover → 独占并在下次同步时接管；其余 → 独占。
func ProfileBinding(orbID, profileID string, mode apigen.BrowserProfileMode, now int64) db.UpsertOrbDesktopProfileParams {
	p := db.UpsertOrbDesktopProfileParams{OrbID: orbID, ProfileMode: string(apigen.BrowserProfileAccessExclusive), Now: now}
	if profileID == "" {
		return p
	}
	p.ProfileID = &profileID
	switch mode {
	case apigen.BrowserProfileModeReadonly:
		reason := string(apigen.BrowserProfileReadonlyReasonRequested)
		p.ProfileMode, p.ReadonlyReason, p.ReadonlyAt = string(apigen.BrowserProfileAccessReadonly), &reason, &now
	case apigen.BrowserProfileModeTakeover:
		p.Takeover = 1
	}
	return p
}
