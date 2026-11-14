package desktop

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Profile 的同步与保存（protocol.md「M8 → 控制面同步与保存」）。同一 orb 的同步与保存经 orbLocks 串行；
// 同一 Profile 的装载 / 提交新修订 / 删除经 profLocks 串行（锁序：先 orb 后 Profile，导出流不持 Profile 锁）。

const (
	accessExclusive = string(apigen.BrowserProfileAccessExclusive)
	accessReadonly  = string(apigen.BrowserProfileAccessReadonly)
	exportLive      = "live"
	exportClosed    = "closed"
)

// orbdError 是 orbd 本机 API 的非 2xx 应答。
type orbdError struct {
	Status        int
	Code, Message string
}

func (e *orbdError) Error() string {
	return fmt.Sprintf("orbd %d %s: %s", e.Status, e.Code, e.Message)
}

// orbdCall 向 orb 的 orbd 本机 API 发请求；非 2xx 时读出 {code, message} 并返回 *orbdError（响应体已关闭）。
func (s *Service) orbdCall(ctx context.Context, orbID, method, path string, query url.Values, body io.Reader, size int64) (*http.Response, error) {
	u := "http://orbd" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
		if size == 0 {
			req.Body = http.NoBody
		}
	}
	resp, err := s.o.Do(ctx, orbID, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer resp.Body.Close()
	e := &orbdError{Status: resp.StatusCode, Code: "failed"}
	var b struct{ Code, Message string }
	if raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10)); json.Unmarshal(raw, &b) == nil && b.Code != "" {
		e.Code, e.Message = b.Code, b.Message
	}
	if e.Status == http.StatusNotFound && e.Code == "not_found" {
		// orbd 没有 /desktop/* 路由（未启用桌面）
		return nil, orbs.ErrOrbdOutdated
	}
	return nil, e
}

func orbdCode(err error) string {
	var oe *orbdError
	if errors.As(err, &oe) {
		return oe.Code
	}
	return ""
}

// event 写一条 Profile 审计事件（同时记 slog）。
func (s *Service) event(ctx context.Context, profileID, orbID string, ev apigen.BrowserProfileEvent, detail map[string]any) {
	raw, _ := json.Marshal(detail)
	var orb *string
	if orbID != "" {
		orb = &orbID
	}
	slog.Info("浏览器 Profile 事件", "event", string(ev), "profile", profileID, "orb", orbID, "detail", string(raw))
	if err := s.st.Q.InsertBrowserProfileEvent(ctx, db.InsertBrowserProfileEventParams{
		ProfileID: profileID, OrbID: orb, Ts: s.nowMs(), Event: string(ev), Detail: string(raw),
	}); err != nil {
		slog.Warn("写入 Profile 事件失败", "profile", profileID, "event", string(ev), "err", err)
	}
}

func (s *Service) setLocal(ctx context.Context, orbID string, localRev int64, profileErr *string, fence int64) error {
	return s.st.Q.SetOrbDesktopLocal(ctx, db.SetOrbDesktopLocalParams{
		LocalRev: localRev, ProfileError: profileErr, Fence: fence, Now: s.nowMs(), OrbID: orbID,
	})
}

// getDesktop 读 orb 的桌面行；没有行返回 ok=false。
func (s *Service) getDesktop(ctx context.Context, orbID string) (db.OrbDesktop, bool, error) {
	d, err := s.st.Q.GetOrbDesktop(ctx, orbID)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	return d, err == nil, err
}

// ---- 同步 ----

// sync 让 orb 本地的 Profile 目录与绑定一致（BeforeReady 与 SetOrbProfile 调用）。
func (s *Service) sync(ctx context.Context, orbID string) error {
	unlock, err := s.orbLocks.lock(ctx, orbID)
	if err != nil {
		return err
	}
	defer unlock()
	return s.syncLocked(ctx, orbID)
}

func (s *Service) syncLocked(ctx context.Context, orbID string) error {
	defer s.o.RefreshDesktop(context.WithoutCancel(ctx), orbID)
	d, ok, err := s.getDesktop(ctx, orbID)
	if err != nil {
		return err
	}
	if !ok || d.ProfileID == nil {
		return s.clearLocal(ctx, orbID, ok, nil)
	}
	p, err := getProfile(ctx, s.st.Q, *d.ProfileID)
	if errors.Is(err, errNotFound) {
		return s.clearLocal(ctx, orbID, true, nil)
	}
	if err != nil {
		return err
	}
	if d.ProfileMode == accessReadonly {
		// 只读副本：不取租约、从不回写
		return s.load(ctx, orbID, d, p.ID, 0, accessReadonly)
	}
	if d.Takeover != 0 {
		if err := s.takeover(ctx, orbID, p); err != nil {
			slog.Warn("接管浏览器 Profile 失败", "orb", orbID, "profile", p.ID, "err", err)
		}
	}
	fence, err := s.st.Q.AcquireBrowserProfile(ctx, db.AcquireBrowserProfileParams{OrbID: orbID, Now: s.nowMs(), ID: p.ID})
	if errors.Is(err, sql.ErrNoRows) {
		// 被另一台 orb 独占持有：本地目录清空（视图 in_use）
		return s.clearLocal(ctx, orbID, true, nil)
	}
	if err != nil {
		return err
	}
	return s.load(ctx, orbID, d, p.ID, fence, accessExclusive)
}

// clearLocal 把 orb 定为不用 Profile（DELETE /desktop/profile，幂等），有桌面行时记 local_rev=-1。
func (s *Service) clearLocal(ctx context.Context, orbID string, hasRow bool, profileErr *string) error {
	resp, err := s.orbdCall(ctx, orbID, http.MethodDelete, "/desktop/profile", nil, nil, 0)
	if err == nil {
		resp.Body.Close()
	}
	if hasRow {
		if serr := s.setLocal(ctx, orbID, -1, profileErr, 0); serr != nil && err == nil {
			err = serr
		}
	}
	return err
}

// load 按绑定装载 Profile 到 orb：已装载过且（独占时）本地就是当前修订 → keep=1（profile_mismatch 才全量）；
// 否则把当前修订的归档解密后作为 PUT 体（修订 0 = 空体）。失败 → 清空本地、独占时释放租约、记 profile_error。
func (s *Service) load(ctx context.Context, orbID string, d db.OrbDesktop, profileID string, fence int64, access string) error {
	unlock, err := s.profLocks.lock(ctx, profileID)
	if err != nil {
		return err
	}
	defer unlock()
	p, err := getProfile(ctx, s.st.Q, profileID)
	if err != nil {
		return err
	}
	q := url.Values{"id": {p.ID}, "name": {p.Name}, "mode": {access}}
	keep := d.LocalRev >= 0 && (access == accessReadonly || d.LocalRev == p.Revision)
	if keep {
		kq := url.Values{"id": {p.ID}, "name": {p.Name}, "mode": {access}, "keep": {"1"}}
		resp, err := s.orbdCall(ctx, orbID, http.MethodPut, "/desktop/profile", kq, http.NoBody, 0)
		if err == nil {
			resp.Body.Close()
			return s.setLocal(ctx, orbID, d.LocalRev, nil, fence)
		}
		if orbdCode(err) != "profile_mismatch" {
			return s.loadFailed(ctx, orbID, profileID, access, err)
		}
		slog.Info("orb 本地 Profile 与绑定不符，全量装载", "orb", orbID, "profile", p.ID)
	}
	tctx, cancel := context.WithTimeout(ctx, transferTimeout)
	defer cancel()
	var body io.Reader = http.NoBody
	var size int64
	var src *trackedReader
	if p.Revision > 0 {
		f, err := os.Open(s.archivePath(p.ID, p.Revision))
		if err != nil {
			return s.loadFailed(ctx, orbID, profileID, access, fmt.Errorf("打开归档: %w", err))
		}
		defer f.Close()
		plain, err := s.cfg.Box.OpenStream(f, archiveName(p.ID, p.Revision))
		if err != nil {
			return s.loadFailed(ctx, orbID, profileID, access, fmt.Errorf("解密归档: %w", err))
		}
		src = &trackedReader{r: plain}
		body, size = src, -1
	}
	resp, err := s.orbdCall(tctx, orbID, http.MethodPut, "/desktop/profile", q, body, size)
	if err == nil {
		resp.Body.Close()
		if src != nil && src.err != nil {
			err = fmt.Errorf("解密归档: %w", src.err)
		}
	}
	if err != nil {
		if src != nil && src.err != nil && orbdCode(err) == "" {
			err = fmt.Errorf("解密归档: %w", src.err)
		}
		return s.loadFailed(ctx, orbID, profileID, access, err)
	}
	return s.setLocal(ctx, orbID, p.Revision, nil, fence)
}

// loadFailed 处理装载失败：DELETE、独占时释放租约、记 profile_error（orbd 的错误码，否则 load_failed）。
func (s *Service) loadFailed(ctx context.Context, orbID, profileID, access string, cause error) error {
	code := orbdCode(cause)
	if code == "" {
		code = "load_failed"
	}
	slog.Warn("装载浏览器 Profile 失败", "orb", orbID, "profile", profileID, "code", code, "err", cause)
	resp, err := s.orbdCall(ctx, orbID, http.MethodDelete, "/desktop/profile", nil, nil, 0)
	if err == nil {
		resp.Body.Close()
	}
	if access == accessExclusive {
		if _, err := s.st.Q.ReleaseBrowserProfileByOrb(ctx, orbID); err != nil {
			slog.Warn("释放 Profile 租约失败", "orb", orbID, "err", err)
		}
	}
	if err := s.setLocal(ctx, orbID, -1, &code, 0); err != nil {
		return err
	}
	return cause
}

// trackedReader 记下底层读取器的第一个非 EOF 错误（归档解密失败与传输失败区分开）。
type trackedReader struct {
	r   io.Reader
	err error
}

func (t *trackedReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if err != nil && err != io.EOF && t.err == nil {
		t.err = err
	}
	return n, err
}

// takeover 把 Profile 从当前持有者手里转给 orbID：持有者可用时先请它活体保存（takeoverSaveTimeout；失败记
// takeover_unsaved 后继续），再在同一事务里按保存之前读到的围栏代数 StealBrowserProfile、把原持有者降级为只读、
// 写 profile.taken_over；最后经 ctl 向原 orbd 发 profile.mode。CAS 失败则重读重试（至多 3 次）：每次尝试都针对
// 本次读到的（持有者, 代数）先保存一次（同一对只存一次），新持有者刚产生的登录态不会被跳过；
// 没有持有者或持有者已是自己时不做任何事（随后的 Acquire 取得租约）。
//
// profile.mode 只带 profileId / mode / reason：接管方线程的标题与 id 只写进库（orb_desktops.readonly_by*，
// 面板据此展示），不下发给 orb（orb 对控制面不可信，见安全审查 F5）。
func (s *Service) takeover(ctx context.Context, orbID string, p db.BrowserProfile) error {
	saved := ""
	for attempt := range 3 {
		if p.HolderOrbID == nil || *p.HolderOrbID == orbID {
			return nil
		}
		holder, fence := *p.HolderOrbID, p.Fence
		if key := fmt.Sprintf("%s/%d", holder, fence); key != saved {
			s.preTakeoverSave(ctx, holder, p)
			saved = key
		}
		if s.beforeSteal != nil {
			s.beforeSteal(orbID, attempt)
		}
		var by *string
		var byThread *string
		if rep, err := s.st.Q.GetOrbRepresentativeThread(ctx, orbID); err == nil {
			by, byThread = &rep.Title, &rep.ID
		}
		var newFence int64
		stolen := false
		err := s.st.Tx(ctx, func(q *db.Queries) error {
			f, err := q.StealBrowserProfile(ctx, db.StealBrowserProfileParams{OrbID: orbID, Now: s.nowMs(), ID: p.ID, ExpectedFence: fence})
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if _, err := q.DemoteOrbDesktopReadonly(ctx, db.DemoteOrbDesktopReadonlyParams{
				ReadonlyBy: by, ReadonlyByThreadID: byThread, Now: s.nowMs(), OrbID: holder, ProfileID: p.ID,
			}); err != nil {
				return err
			}
			newFence, stolen = f, true
			return nil
		})
		if err != nil {
			return err
		}
		if stolen {
			s.event(ctx, p.ID, holder, apigen.BrowserProfileEventTakenOver, map[string]any{"by": orbID, "fence": newFence})
			mp := apigen.LinkCtlProfileModeParams{
				ProfileID: p.ID, Mode: apigen.BrowserProfileAccessReadonly, Reason: apigen.BrowserProfileReadonlyReasonTakenOver,
			}
			if err := s.o.SendProfileMode(ctx, holder, mp); err != nil {
				// 丢失无害：迟到的保存会被 confirmLease 与围栏挡下，下次同步的 PUT 也带 mode；
				// 它若迟到、落在原持有者反向接管之后，由 DesktopReported 的纠正以库为准改回。
				slog.Info("向原持有者发 profile.mode 失败（忽略）", "orb", holder, "profile", p.ID, "err", err)
			}
			s.o.RefreshDesktop(ctx, holder)
			return nil
		}
		if p, err = getProfile(ctx, s.st.Q, p.ID); err != nil {
			return err
		}
	}
	return errors.New("desktop: 接管时租约反复变化")
}

// preTakeoverSave 在接管前请原持有者活体保存：它已冻结 / 不可达时跳过；失败给 Profile 记 takeover_unsaved。
func (s *Service) preTakeoverSave(ctx context.Context, holder string, p db.BrowserProfile) {
	orb, err := s.o.Get(ctx, holder)
	if err != nil || orb.State != orbs.StateReady || s.o.EnsureUsable(ctx, holder, orbs.FeatureDesktop, false, 0) != nil {
		slog.Info("接管：原持有者不可用，跳过保存", "orb", holder, "profile", p.ID)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, takeoverSaveTimeout)
	defer cancel()
	if err := s.checkin(cctx, holder, exportLive); err != nil {
		slog.Warn("接管：原持有者的保存失败，接管方装载更早的修订", "orb", holder, "profile", p.ID, "err", err)
		code := string(apigen.TakeoverUnsaved)
		if serr := s.st.Q.SetBrowserProfileSaveError(ctx, db.SetBrowserProfileSaveErrorParams{SaveError: &code, Now: s.nowMs(), ID: p.ID}); serr != nil {
			slog.Warn("记录 Profile 保存错误失败", "profile", p.ID, "err", serr)
		}
	}
}

// ---- 保存 ----

// saveError 是一次保存失败（code ∈ too_large | export_failed）。
type saveError struct {
	code  string
	cause error
}

func (e *saveError) Error() string { return "desktop: save " + e.code + ": " + e.cause.Error() }
func (e *saveError) Unwrap() error { return e.cause }

// checkin 把 orb 本地的 Profile 存为新修订（mode ∈ live | closed）。
func (s *Service) checkin(ctx context.Context, orbID, mode string) error {
	unlock, err := s.orbLocks.lock(ctx, orbID)
	if err != nil {
		return err
	}
	defer unlock()
	return s.checkinLocked(ctx, orbID, mode)
}

// checkinLocked：只对独占绑定；持有者是别人 / 本地不是当前修订 / dirty=0 → 不保存。导出体经 LimitReader 同时算
// SHA-256 并 SealStream 到临时文件；摘要与当前修订相同不产生新修订；否则 rename 为 <revision+1>.oss 后
// SaveBrowserProfileRevision（被围栏挡下 → 删除新文件、记 profile.save_fenced，不报错）。
func (s *Service) checkinLocked(ctx context.Context, orbID, mode string) error {
	d, ok, err := s.getDesktop(ctx, orbID)
	if err != nil || !ok || d.ProfileID == nil || d.ProfileMode != accessExclusive {
		return err
	}
	p, err := getProfile(ctx, s.st.Q, *d.ProfileID)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if p.Revision != d.LocalRev || d.Dirty == 0 {
		return nil
	}
	if s.beforeLease != nil {
		s.beforeLease(orbID)
	}
	fence, ok, err := s.confirmLease(ctx, orbID, d)
	if err != nil || !ok {
		return err
	}
	defer s.o.RefreshDesktop(context.WithoutCancel(ctx), orbID)
	tctx, cancel := context.WithTimeout(ctx, transferTimeout)
	defer cancel()
	resp, err := s.orbdCall(tctx, orbID, http.MethodGet, "/desktop/profile/export", url.Values{"mode": {mode}}, nil, 0)
	if err != nil {
		switch orbdCode(err) {
		case "no_profile", "profile_readonly":
			slog.Info("orbd 没有可导出的 Profile，跳过保存", "orb", orbID, "profile", p.ID, "code", orbdCode(err))
			return nil
		case "too_large":
			// orbd 在开始输出前按还原端的条目数 / 解包大小上限检查过：超限不交出无法还原的归档，旧修订保留
			return s.saveFailed(ctx, orbID, p.ID, string(apigen.TooLarge), err)
		}
		return s.saveFailed(ctx, orbID, p.ID, string(apigen.ExportFailed), err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Oryn-Profile-Id"); got != p.ID {
		slog.Warn("导出的 Profile 与绑定不符，放弃保存", "orb", orbID, "want", p.ID, "got", got)
		return nil
	}
	base := p.Revision
	dir := s.profileDir(p.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return s.saveFailed(ctx, orbID, p.ID, string(apigen.ExportFailed), err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return s.saveFailed(ctx, orbID, p.ID, string(apigen.ExportFailed), err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return s.saveFailed(ctx, orbID, p.ID, string(apigen.ExportFailed), err)
	}
	next := base + 1
	w, err := s.cfg.Box.SealStream(tmp, archiveName(p.ID, next))
	if err != nil {
		return s.saveFailed(ctx, orbID, p.ID, string(apigen.ExportFailed), err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(h, w), io.LimitReader(resp.Body, maxArchiveBytes+1))
	if err != nil {
		return s.saveFailed(ctx, orbID, p.ID, string(apigen.ExportFailed), err)
	}
	if n > maxArchiveBytes {
		return s.saveFailed(ctx, orbID, p.ID, string(apigen.TooLarge), fmt.Errorf("归档超过 %d 字节", maxArchiveBytes))
	}
	if err := w.Close(); err != nil {
		return s.saveFailed(ctx, orbID, p.ID, string(apigen.ExportFailed), err)
	}
	if err := tmp.Sync(); err != nil {
		return s.saveFailed(ctx, orbID, p.ID, string(apigen.ExportFailed), err)
	}
	st, err := tmp.Stat()
	if err != nil {
		return s.saveFailed(ctx, orbID, p.ID, string(apigen.ExportFailed), err)
	}
	if err := tmp.Close(); err != nil {
		return s.saveFailed(ctx, orbID, p.ID, string(apigen.ExportFailed), err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if s.beforeSave != nil {
		s.beforeSave(orbID)
	}

	unlock, err := s.profLocks.lock(ctx, p.ID)
	if err != nil {
		return err
	}
	defer unlock()
	cur, err := getProfile(ctx, s.st.Q, p.ID)
	if err != nil {
		return err
	}
	if cur.Revision == base && cur.ContentSha256 != nil && *cur.ContentSha256 == sum {
		slog.Debug("Profile 内容未变，不产生新修订", "orb", orbID, "profile", p.ID)
		return nil
	}
	if cur.Revision != base {
		// 期间别的 orb 保存过：本次一定会被围栏挡下
		s.fenced(ctx, orbID, p.ID, fence, cur.Fence)
		return nil
	}
	final := s.archivePath(p.ID, next)
	if err := os.Rename(tmpName, final); err != nil {
		return s.saveFailed(ctx, orbID, p.ID, string(apigen.ExportFailed), err)
	}
	committed = true
	syncDir(dir)
	rev, err := s.st.Q.SaveBrowserProfileRevision(ctx, db.SaveBrowserProfileRevisionParams{
		SizeBytes: st.Size(), ContentSha256: sum, Now: s.nowMs(), ID: p.ID, OrbID: orbID, Fence: fence,
	})
	if errors.Is(err, sql.ErrNoRows) {
		_ = os.Remove(final)
		s.fenced(ctx, orbID, p.ID, fence, cur.Fence)
		return nil
	}
	if err != nil {
		_ = os.Remove(final)
		return err
	}
	if rev != next {
		// 不应发生（同一 Profile 的提交由 profLocks 串行）
		slog.Error("Profile 修订号与预期不符", "profile", p.ID, "want", next, "got", rev)
	}
	if err := s.setLocal(ctx, orbID, rev, nil, fence); err != nil {
		slog.Warn("记录本地修订失败", "orb", orbID, "err", err)
	}
	s.pruneArchives(p.ID, rev)
	s.event(ctx, p.ID, orbID, apigen.BrowserProfileEventSaved, map[string]any{"revision": rev, "sizeBytes": st.Size(), "mode": mode})
	return nil
}

// confirmLease 在一个事务里复核保存资格，给出本次保存要出示的围栏代数（ok=false = 不保存）：
//   - 绑定仍是同一 Profile 的独占，本地修订与围栏代数与开始保存时读到的一致，且就是 Profile 的当前修订与代数；
//   - 本 orb 仍持有租约 → 沿用该代数；
//   - 租约空着且代数仍是本 orb 上次持有时的值（期间没有别的 orb 持有过，例如冻结释放租约后停机前的关闭式保存）
//     → 重新取得租约并记下新代数；
//   - 其余（已被接管降级、别的 orb 持有或持有过）→ 不保存。迟到的保存因此不能把已释放的租约重新领成新围栏。
func (s *Service) confirmLease(ctx context.Context, orbID string, d db.OrbDesktop) (int64, bool, error) {
	if d.ProfileID == nil || d.Fence <= 0 {
		return 0, false, nil
	}
	var fence int64
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		cd, err := q.GetOrbDesktop(ctx, orbID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if cd.ProfileID == nil || *cd.ProfileID != *d.ProfileID || cd.ProfileMode != accessExclusive ||
			cd.LocalRev != d.LocalRev || cd.Fence != d.Fence {
			return nil
		}
		p, err := q.GetBrowserProfile(ctx, *cd.ProfileID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if p.Revision != cd.LocalRev || p.Fence != cd.Fence {
			return nil
		}
		switch {
		case p.HolderOrbID != nil && *p.HolderOrbID == orbID:
			fence = p.Fence
		case p.HolderOrbID == nil:
			f, err := q.AcquireBrowserProfile(ctx, db.AcquireBrowserProfileParams{OrbID: orbID, Now: s.nowMs(), ID: p.ID})
			if err != nil {
				return err
			}
			if err := q.SetOrbDesktopLocal(ctx, db.SetOrbDesktopLocalParams{
				LocalRev: cd.LocalRev, ProfileError: cd.ProfileError, Fence: f, Now: s.nowMs(), OrbID: orbID,
			}); err != nil {
				return err
			}
			fence = f
		}
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	if fence == 0 {
		slog.Info("保存资格已失效（被接管或租约换过手），不保存", "orb", orbID, "profile", *d.ProfileID, "fence", d.Fence)
	}
	return fence, fence > 0, nil
}

// fenced 记录一次被围栏挡下的保存（不报错给用户）。
func (s *Service) fenced(ctx context.Context, orbID, profileID string, fence, current int64) {
	s.event(ctx, profileID, orbID, apigen.BrowserProfileEventSaveFenced, map[string]any{"fence": fence, "currentFence": current})
}

// saveFailed 给 Profile 记 save_error、写 profile.save_failed，返回 *saveError。
func (s *Service) saveFailed(ctx context.Context, orbID, profileID, code string, cause error) error {
	bg := context.WithoutCancel(ctx)
	if err := s.st.Q.SetBrowserProfileSaveError(bg, db.SetBrowserProfileSaveErrorParams{SaveError: &code, Now: s.nowMs(), ID: profileID}); err != nil {
		slog.Warn("记录 Profile 保存错误失败", "profile", profileID, "err", err)
	}
	s.event(bg, profileID, orbID, apigen.BrowserProfileEventSaveFailed, map[string]any{"code": code, "error": cause.Error()})
	return &saveError{code: code, cause: cause}
}

// pruneArchives 删除比 keep 更旧的归档文件。
func (s *Service) pruneArchives(profileID string, keep int64) {
	entries, err := os.ReadDir(s.profileDir(profileID))
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".oss") {
			continue
		}
		rev, err := strconv.ParseInt(strings.TrimSuffix(name, ".oss"), 10, 64)
		if err != nil || rev >= keep {
			continue
		}
		if err := os.Remove(filepath.Join(s.profileDir(profileID), name)); err != nil {
			slog.Warn("删除旧归档失败", "profile", profileID, "file", name, "err", err)
		}
	}
}

// syncDir 尽力 fsync 目录（让 rename 落盘）。
func syncDir(dir string) {
	if f, err := os.Open(dir); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
}

// ---- 生命周期钩子（orbs.DesktopHooks） ----

// BeforeReady 在 orb 进入 ready 之前同步 Profile。
func (s *Service) BeforeReady(ctx context.Context, orbID string) {
	cctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	if err := s.sync(cctx, orbID); err != nil {
		slog.Warn("同步浏览器 Profile 失败", "orb", orbID, "err", err)
	}
}

// NeedsFreezeSave 报告冻结前是否值得保存：独占绑定、本地是当前修订、dirty、持有者为空或是自己。
func (s *Service) NeedsFreezeSave(ctx context.Context, orbID string) bool {
	d, ok, err := s.getDesktop(ctx, orbID)
	if err != nil || !ok || d.ProfileID == nil || d.ProfileMode != accessExclusive || d.Dirty == 0 {
		return false
	}
	p, err := getProfile(ctx, s.st.Q, *d.ProfileID)
	if err != nil {
		return false
	}
	return p.Revision == d.LocalRev && (p.HolderOrbID == nil || *p.HolderOrbID == orbID)
}

// BeforeFreeze 冻结前活体保存（浏览器原样保留）。
func (s *Service) BeforeFreeze(ctx context.Context, orbID string) {
	s.hookCheckin(ctx, orbID, exportLive)
}

// BeforeShutdown 停机 / 删除前关闭式保存。
func (s *Service) BeforeShutdown(ctx context.Context, orbID string) {
	s.hookCheckin(ctx, orbID, exportClosed)
}

func (s *Service) hookCheckin(ctx context.Context, orbID, mode string) {
	cctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	if err := s.checkin(cctx, orbID, mode); err != nil {
		slog.Warn("保存浏览器 Profile 失败", "orb", orbID, "mode", mode, "err", err)
	}
}

// ---- 以库为准纠正 orbd 本地的使用方式 ----

// DesktopReported 实现 orbs.DesktopHooks（不阻塞）：记下 orb 最新一次报告，排入纠正队列。
func (s *Service) DesktopReported(orbID string, st apigen.LinkCtlDesktopStateParams) {
	if st.ProfileID == nil || *st.ProfileID == "" || st.ProfileMode == nil {
		return
	}
	s.fixMu.Lock()
	_, queued := s.fixes[orbID]
	s.fixes[orbID] = st
	s.fixMu.Unlock()
	if !queued {
		s.modeFixes.push(orbID)
	}
}

func (s *Service) modeFixLoop() {
	for {
		orbID, ok := s.modeFixes.pop(s.ctx)
		if !ok {
			return
		}
		s.fixMu.Lock()
		st, has := s.fixes[orbID]
		delete(s.fixes, orbID)
		s.fixMu.Unlock()
		if has {
			s.fixMode(s.ctx, orbID, st)
		}
		if s.onModeFixed != nil {
			s.onModeFixed(orbID)
		}
	}
}

// fixMode 让 orbd 本地标记的使用方式与库里的绑定一致（profile.mode 没有代数，消息可能迟到）：
//   - orbd 报只读、库里是本 orb 独占且仍持有当前修订与代数（迟到的降级落在反向接管之后）→ 按绑定重新装载
//     （本地就是当前修订，即 keep=1&mode=exclusive），orbd 恢复独占、dirty 按浏览器是否在运行重算；
//   - orbd 报独占、库里已降为只读（profile.mode 丢失）→ 重发 profile.mode。
//
// 两种动作都只是把 orbd 改成库里的样子，基于过时的报告执行也无害；持 orb 锁，与同步 / 保存串行。
func (s *Service) fixMode(ctx context.Context, orbID string, st apigen.LinkCtlDesktopStateParams) {
	cctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	unlock, err := s.orbLocks.lock(cctx, orbID)
	if err != nil {
		return
	}
	defer unlock()
	d, ok, err := s.getDesktop(cctx, orbID)
	if err != nil || !ok || d.ProfileID == nil || *d.ProfileID != *st.ProfileID {
		return
	}
	reported := string(*st.ProfileMode)
	switch {
	case reported == accessReadonly && d.ProfileMode == accessExclusive:
		p, err := getProfile(cctx, s.st.Q, *d.ProfileID)
		if err != nil || p.HolderOrbID == nil || *p.HolderOrbID != orbID || p.Fence != d.Fence || d.LocalRev != p.Revision {
			return
		}
		slog.Warn("orbd 把当前持有者的 Profile 记成了只读（迟到的 profile.mode），以库为准改回独占", "orb", orbID, "profile", p.ID)
		if err := s.load(cctx, orbID, d, p.ID, d.Fence, accessExclusive); err != nil {
			slog.Warn("纠正 Profile 使用方式失败", "orb", orbID, "profile", p.ID, "err", err)
		}
		s.o.RefreshDesktop(context.WithoutCancel(cctx), orbID)
	case reported == accessExclusive && d.ProfileMode == accessReadonly:
		reason := apigen.BrowserProfileReadonlyReasonRequested
		if d.ReadonlyReason != nil {
			reason = apigen.BrowserProfileReadonlyReason(*d.ReadonlyReason)
		}
		slog.Info("orbd 仍以独占使用已降为只读的 Profile，重发 profile.mode", "orb", orbID, "profile", *d.ProfileID)
		if err := s.o.SendProfileMode(cctx, orbID, apigen.LinkCtlProfileModeParams{
			ProfileID: *d.ProfileID, Mode: apigen.BrowserProfileAccessReadonly, Reason: reason,
		}); err != nil {
			slog.Info("重发 profile.mode 失败（忽略）", "orb", orbID, "err", err)
		}
	}
}

// ---- 面向 REST 的操作 ----

// SetOrbProfile 切换 orb 选用的 Profile（profileID 为空 = 解除）：先唤醒；以 exclusive 选用正被别的 orb 持有的
// Profile → profile_in_use（不做任何改动）。当前独占持有另一个 Profile 时先关闭式保存并释放，再写绑定并同步。
func (s *Service) SetOrbProfile(ctx context.Context, orbID, profileID string, mode apigen.BrowserProfileMode) error {
	mode, err := normMode(mode)
	if err != nil {
		return err
	}
	if err := s.o.EnsureUsable(ctx, orbID, orbs.FeatureDesktop, true, wakeWait); err != nil {
		return err
	}
	if profileID != "" {
		p, err := getProfile(ctx, s.st.Q, profileID)
		if err != nil {
			return err
		}
		if mode == apigen.BrowserProfileModeExclusive && p.HolderOrbID != nil && *p.HolderOrbID != orbID {
			return s.inUse(ctx, s.st.Q, p)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	unlock, err := s.orbLocks.lock(cctx, orbID)
	if err != nil {
		return err
	}
	defer unlock()
	access := accessExclusive
	if mode == apigen.BrowserProfileModeReadonly {
		access = accessReadonly
	}
	d, ok, err := s.getDesktop(cctx, orbID)
	if err != nil {
		return err
	}
	var same bool
	if !ok || d.ProfileID == nil {
		same = profileID == ""
	} else {
		same = *d.ProfileID == profileID && d.ProfileMode == access
	}
	if ok && d.ProfileID != nil && d.ProfileMode == accessExclusive && !same {
		if err := s.checkinLocked(cctx, orbID, exportClosed); err != nil {
			slog.Warn("切换 Profile 前保存失败（继续）", "orb", orbID, "profile", *d.ProfileID, "err", err)
		}
		if _, err := s.st.Q.ReleaseBrowserProfileByOrb(cctx, orbID); err != nil {
			return err
		}
	}
	// 同一绑定不重写（重写会把 local_rev 置 -1、强制重装）；对已持有的 Profile 再要求接管也无需重写
	if !same || (mode == apigen.BrowserProfileModeTakeover && !s.holds(cctx, orbID, profileID)) {
		if err := s.st.Q.UpsertOrbDesktopProfile(cctx, orbs.ProfileBinding(orbID, profileID, mode, s.nowMs())); err != nil {
			return err
		}
	}
	return s.syncLocked(cctx, orbID)
}

// Save 手动「立即保存」（活体）；该 orb 没有独占持有 Profile → no_profile，只读副本 → profile_readonly。
func (s *Service) Save(ctx context.Context, orbID string) (apigen.BrowserProfile, error) {
	if err := s.o.EnsureUsable(ctx, orbID, orbs.FeatureDesktop, true, wakeWait); err != nil {
		return apigen.BrowserProfile{}, err
	}
	d, ok, err := s.getDesktop(ctx, orbID)
	if err != nil {
		return apigen.BrowserProfile{}, err
	}
	if !ok || d.ProfileID == nil {
		return apigen.BrowserProfile{}, errNoProfile
	}
	if d.ProfileMode == accessReadonly {
		return apigen.BrowserProfile{}, errReadonly
	}
	p, err := getProfile(ctx, s.st.Q, *d.ProfileID)
	if err != nil {
		return apigen.BrowserProfile{}, err
	}
	if p.HolderOrbID == nil || *p.HolderOrbID != orbID {
		return apigen.BrowserProfile{}, errNoProfile
	}
	if err := s.checkin(ctx, orbID, exportLive); err != nil {
		var se *saveError
		if errors.As(err, &se) {
			if se.code == string(apigen.TooLarge) {
				return apigen.BrowserProfile{}, errf(http.StatusRequestEntityTooLarge, "profile_too_large", "Profile 归档超过上限")
			}
			return apigen.BrowserProfile{}, errf(http.StatusBadGateway, "profile_save_failed", se.cause.Error())
		}
		return apigen.BrowserProfile{}, err
	}
	return s.get(ctx, p.ID)
}

// SetBrowser 启动或停止 orb 桌面上的浏览器，把 orbd 返回的状态写进镜像。
func (s *Service) SetBrowser(ctx context.Context, orbID string, run bool) error {
	if err := s.o.EnsureUsable(ctx, orbID, orbs.FeatureDesktop, true, wakeWait); err != nil {
		return err
	}
	path := "/desktop/browser/stop"
	if run {
		path = "/desktop/browser/start"
	}
	cctx, cancel := context.WithTimeout(ctx, browserTimeout)
	defer cancel()
	resp, err := s.orbdCall(cctx, orbID, http.MethodPost, path, nil, nil, 0)
	if err != nil {
		if orbdCode(err) == "not_ready" {
			return errf(http.StatusServiceUnavailable, "orb_unavailable", "桌面或 Profile 尚未就绪")
		}
		return err
	}
	defer resp.Body.Close()
	var st apigen.LinkCtlDesktopStateParams
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&st); err != nil {
		return fmt.Errorf("desktop: orbd 返回的桌面状态无效: %w", err)
	}
	s.o.ApplyDesktopState(ctx, orbID, st)
	return nil
}

// holds 报告 orb 是否独占持有 Profile。
func (s *Service) holds(ctx context.Context, orbID, profileID string) bool {
	p, err := getProfile(ctx, s.st.Q, profileID)
	return err == nil && p.HolderOrbID != nil && *p.HolderOrbID == orbID
}
