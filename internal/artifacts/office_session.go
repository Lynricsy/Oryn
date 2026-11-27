package artifacts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/artifacts/office"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// M32 Office 编辑：会话、占用与 WOPI 端点（引擎客户端见 office 包，协议核实结论写在它的包注释里）。
//
// 会话是一次浏览器编辑器的打开（aos_<ULID>，访问令牌 32 字节随机、只存 sha256）；占用是某个 artifact 正在被编辑
// （artifact_office_holds 一行：WOPI 锁串、开编时的基准版本、自动保存的工作副本）。占用期间其他写入（Write、
// RestoreVersion、UploadVersion）回 locked，只有 WOPI 的提交路径能落版本。版本只在显式保存、退出保存与占用释放
// （最后一个会话过期或关闭宽限期满）三个时刻且内容有变时落下，作者是用户；自动保存只更新工作副本。

// 时间常量（计划 C7）。
const (
	officeTokenTTL   = 12 * time.Hour
	officeIdle       = 30 * time.Minute
	officeCloseGrace = 30 * time.Second
	officeSweepEvery = 15 * time.Second
)

// WOPIPathPrefix 是 WOPI 端点的路径前缀（挂在控制面根路由上，不经 cookie 鉴权，凭访问令牌）。
const WOPIPathPrefix = "/wopi/"

// officeAutosaveNote 是占用释放时把工作副本落成版本的备注。
const officeAutosaveNote = "编辑器自动保存"

// officeEditable 是能在引擎里编辑的类型（PDF 只读）。
var officeEditable = map[string]bool{
	TypeDocx: true, TypeDocm: true, TypeXlsx: true, TypeXlsm: true, TypePptx: true, TypePptm: true,
}

// officeSet 是引擎配置。
type officeSet struct {
	mu       sync.Mutex
	engine   *office.Engine
	wopiBase string
	// noProof 保证「引擎不发 proof，跳过验签」只记一次日志。
	noProof sync.Once
}

// UseOffice 接上 Office 引擎；wopiBase 是引擎回调控制面的地址（不带结尾斜杠）。不调用 = 没有引擎。
func (s *Service) UseOffice(e *office.Engine, wopiBase string) {
	s.office.mu.Lock()
	defer s.office.mu.Unlock()
	s.office.engine, s.office.wopiBase = e, strings.TrimRight(wopiBase, "/")
}

func (s *Service) officeEngine() (*office.Engine, string) {
	s.office.mu.Lock()
	defer s.office.mu.Unlock()
	return s.office.engine, s.office.wopiBase
}

// OfficeStatus 是 Office 引擎能力（REST 的 capabilities.office）。
type OfficeStatus struct {
	Edit, Convert bool
	// Reason 是不可用时的原因（两项都可用时为空）。
	Reason string
}

// OfficeStatus 报告引擎当前能否编辑、能否转换（discovery 与能力各自缓存，见 office 包）。
func (s *Service) OfficeStatus(ctx context.Context) OfficeStatus {
	e, _ := s.officeEngine()
	if e == nil {
		return OfficeStatus{Reason: "没有配置 Office 引擎（orynd.yaml 的 artifacts.office.url）。"}
	}
	var out OfficeStatus
	var reasons []string
	if _, err := e.Discover(ctx); err != nil {
		reasons = append(reasons, fmt.Sprintf("Office 引擎不可用：%v", err))
	} else {
		out.Edit = true
	}
	if c, err := e.Capabilities(ctx); err != nil {
		if out.Edit {
			reasons = append(reasons, fmt.Sprintf("读取 Office 引擎能力失败：%v", err))
		}
	} else if c.Convert {
		out.Convert = true
	} else {
		reasons = append(reasons, "Office 引擎没有开启转换（convert-to）。")
	}
	out.Reason = strings.Join(reasons, " ")
	return out
}

// officeConverter 返回可用于转换的引擎；没有配置或转换不可用时为 nil。
func (s *Service) officeConverter(ctx context.Context) *office.Engine {
	e, _ := s.officeEngine()
	if e == nil {
		return nil
	}
	if c, err := e.Capabilities(ctx); err != nil || !c.Convert {
		return nil
	}
	return e
}

// OfficeSession 是一次打开的编辑会话（Token 只在打开时给出一次）。
type OfficeSession struct {
	ID, ActionURL, Token string
	TokenExpires         time.Time
}

func officeKey(id string) string { return "office:" + id }

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// OpenOffice 为 artifact 打开一个编辑会话：建会话与占用（已有占用时复用，同一文档的多个编辑器由引擎合并成协同编辑），
// 返回引擎编辑页地址与访问令牌。locale 为空时不带 lang。
func (s *Service) OpenOffice(ctx context.Context, id, locale string) (OfficeSession, error) {
	if locale != "" && !localeRe.MatchString(locale) {
		return OfficeSession{}, opErr(CodeInvalid, "locale %q 不合法（形如 zh-CN）。", locale)
	}
	e, wopiBase := s.officeEngine()
	if e == nil {
		return OfficeSession{}, opErr(CodeInvalid, "没有配置 Office 引擎，不能在浏览器里编辑。")
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return OfficeSession{}, err
	}
	if a.Deleted() {
		return OfficeSession{}, deletedErr(id)
	}
	if !officeEditable[a.Type] {
		return OfficeSession{}, opErr(CodeInvalid, "只有 Word / Excel / PowerPoint artifact 能在编辑器里编辑；artifact %s 不是。", id)
	}
	disc, err := e.Discover(ctx)
	if err != nil {
		return OfficeSession{}, opErr(CodeInvalid, "Office 引擎不可用：%v", err)
	}
	ext := strings.TrimPrefix(OfficeExt(a.Type), ".")
	src, ok := disc.Action(ext, "edit")
	if !ok {
		return OfficeSession{}, opErr(CodeInvalid, "Office 引擎不支持编辑 .%s 文件。", ext)
	}
	action, err := officeActionURL(src, e.URL(), wopiBase+"/wopi/files/"+id, locale)
	if err != nil {
		return OfficeSession{}, err
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return OfficeSession{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	now := s.now()
	out := OfficeSession{ID: ids.New("aos"), ActionURL: action, Token: token, TokenExpires: now.Add(officeTokenTTL)}
	unlock := s.locks.lock(officeKey(id))
	defer unlock()
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		row, err := getRow(ctx, q, id)
		if err != nil {
			return err
		}
		if row.DeletedAt != nil {
			return deletedErr(id)
		}
		if err := q.InsertArtifactOfficeHold(ctx, db.InsertArtifactOfficeHoldParams{ArtifactID: id, BaseVersion: row.Head}); err != nil {
			return err
		}
		return q.InsertArtifactOfficeSession(ctx, db.InsertArtifactOfficeSessionParams{
			ID: out.ID, ArtifactID: id, TokenHash: tokenHash(token), CreatedAt: now.UnixMilli(), ActiveAt: now.UnixMilli(),
		})
	})
	if err != nil {
		return OfficeSession{}, err
	}
	s.changed()
	return out, nil
}

// officeActionURL 把 discovery 的 urlsrc 换成浏览器访问引擎的地址，并加上 WOPISrc 与 lang。
func officeActionURL(urlsrc, engineURL, wopiSrc, locale string) (string, error) {
	u, err := url.Parse(urlsrc)
	if err != nil {
		return "", fmt.Errorf("discovery 的 urlsrc %q 无效：%w", urlsrc, err)
	}
	eu, err := url.Parse(engineURL)
	if err != nil {
		return "", err
	}
	u.Scheme, u.Host = eu.Scheme, eu.Host
	out := u.String()
	switch {
	case !strings.Contains(out, "?"):
		out += "?"
	case !strings.HasSuffix(out, "?") && !strings.HasSuffix(out, "&"):
		out += "&"
	}
	out += "WOPISrc=" + url.QueryEscape(wopiSrc)
	if locale != "" {
		out += "&lang=" + url.QueryEscape(locale)
	}
	return out, nil
}

// officeSessionLive 报告会话是否仍可用：令牌未过期，关闭后未超过宽限期，也没有空闲超时。
func officeSessionLive(r db.ArtifactOfficeSession, now time.Time) bool {
	ms := now.UnixMilli()
	if r.CreatedAt+officeTokenTTL.Milliseconds() <= ms || r.ActiveAt+officeIdle.Milliseconds() <= ms {
		return false
	}
	return r.ClosingAt == nil || *r.ClosingAt+officeCloseGrace.Milliseconds() > ms
}

func sessionGone(sid string) *OpError {
	return opErr(CodeNotFound, "编辑会话 %s 不存在或已过期；请重新打开编辑器。", sid)
}

// BeatOffice 刷新会话的活动时间（编辑器每 60 秒一次）；会话不存在或已过期回 not_found。
func (s *Service) BeatOffice(ctx context.Context, sid string) error {
	r, err := s.st.Q.GetArtifactOfficeSession(ctx, sid)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionGone(sid)
	}
	if err != nil {
		return err
	}
	now := s.now()
	if !officeSessionLive(r, now) {
		return sessionGone(sid)
	}
	return s.st.Q.TouchArtifactOfficeSession(ctx, db.TouchArtifactOfficeSessionParams{ID: sid, ActiveAt: now.UnixMilli()})
}

// CloseOffice 关闭会话：只记下关闭时刻，引擎的退出保存在 officeCloseGrace 内仍可提交；之后由清扫删掉会话，
// 没有剩余会话时释放占用。会话不存在时什么也不做。
func (s *Service) CloseOffice(ctx context.Context, sid string) error {
	ts := s.now().UnixMilli()
	return s.st.Q.CloseArtifactOfficeSession(ctx, db.CloseArtifactOfficeSessionParams{ID: sid, ClosingAt: &ts})
}

// Editing 报告 artifact 当前是否有打开着的编辑器：存在没关闭、未过期的会话。关闭后的宽限期里占用仍在（还能收退出
// 保存、仍拒绝其他写入），但已不算「正在编辑」。
func (s *Service) Editing(ctx context.Context, id string) (bool, error) {
	now := s.now()
	n, err := s.st.Q.CountOpenArtifactOfficeSessions(ctx, db.CountOpenArtifactOfficeSessionsParams{
		ArtifactID: id, CreatedAfter: now.Add(-officeTokenTTL).UnixMilli(), ActiveAfter: now.Add(-officeIdle).UnixMilli(),
	})
	return n > 0, err
}

// officeGate 是写入闸门：artifact 有编辑占用时回 locked。
func officeGate(ctx context.Context, q *db.Queries, id string) error {
	_, err := q.GetArtifactOfficeHold(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return opErr(CodeLocked, "用户正在编辑器里编辑这个文档；请让用户保存并关闭编辑器后再试。")
}

// RunOffice 启动时先清扫一轮，之后每 officeSweepEvery 一轮（直到 ctx 结束）。
func (s *Service) RunOffice(ctx context.Context) {
	s.SweepOffice(ctx)
	tick := time.NewTicker(officeSweepEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.SweepOffice(ctx)
		}
	}
}

// SweepOffice 删掉令牌过期、关闭超过宽限期或空闲超过 officeIdle 的会话；没有剩余会话的占用被释放：
// 工作副本与最新版本不同时先以用户身份落一个版本（备注「编辑器自动保存」）。
func (s *Service) SweepOffice(ctx context.Context) {
	now := s.now()
	closing := now.Add(-officeCloseGrace).UnixMilli()
	if _, err := s.st.Q.DeleteStaleArtifactOfficeSessions(ctx, db.DeleteStaleArtifactOfficeSessionsParams{
		CreatedBefore: now.Add(-officeTokenTTL).UnixMilli(), ClosingBefore: &closing, ActiveBefore: now.Add(-officeIdle).UnixMilli(),
	}); err != nil {
		slog.Warn("artifacts: 清理 Office 编辑会话失败", "err", err)
		return
	}
	holds, err := s.st.Q.ListOrphanArtifactOfficeHolds(ctx)
	if err != nil {
		slog.Warn("artifacts: 列出待释放的 Office 编辑占用失败", "err", err)
		return
	}
	released := false
	for _, h := range holds {
		if s.releaseHold(ctx, h.ArtifactID) {
			released = true
		}
	}
	if released {
		s.changed()
	}
}

// releaseHold 释放一个已经没有会话的占用（锁内重查：期间新开的会话会复用占用，不释放）。
func (s *Service) releaseHold(ctx context.Context, id string) bool {
	unlock := s.locks.lock(officeKey(id))
	defer unlock()
	n, err := s.st.Q.CountArtifactOfficeSessions(ctx, id)
	if err != nil || n > 0 {
		return false
	}
	h, err := s.st.Q.GetArtifactOfficeHold(ctx, id)
	if err != nil {
		return false
	}
	if h.DraftSha != nil {
		_, err := s.officeCommit(ctx, id, int(h.BaseVersion), stored{sha: *h.DraftSha, size: deref(h.DraftSize)}, officeAutosaveNote)
		if _, op := AsOpError(err); err != nil && !op {
			// 数据库等临时错误：保留占用与草稿，下一轮再试
			slog.Warn("artifacts: 把编辑器工作副本落成版本失败，下一轮重试", "artifact", id, "err", err)
			return false
		} else if err != nil {
			slog.Warn("artifacts: 编辑器工作副本没能落成版本，已丢弃", "artifact", id, "err", err)
		}
	}
	if err := s.st.Q.DeleteArtifactOfficeHold(ctx, id); err != nil {
		slog.Warn("artifacts: 释放 Office 编辑占用失败", "artifact", id, "err", err)
		return false
	}
	return true
}

// officeCommit 把已落进内容存储的 st 作为入口文件以用户身份写成新版本（内容与最新版本相同则不写），
// 并把占用的基准推进到新的最新版本、清掉草稿。调用方持有 officeKey 锁。
func (s *Service) officeCommit(ctx context.Context, id string, base int, st stored, note string) (Artifact, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return Artifact{}, err
	}
	head, err := s.st.Q.GetArtifactFile(ctx, db.GetArtifactFileParams{ArtifactID: id, N: int64(a.Version), Path: TypeEntry(a.Type)})
	if err != nil {
		return Artifact{}, err
	}
	if head.Sha256 != st.sha {
		if a, err = s.Write(ctx, WriteParams{ID: id, BaseVersion: base, Note: note, officeCommit: &st}); err != nil {
			return Artifact{}, err
		}
	}
	return a, s.st.Q.AdvanceArtifactOfficeHold(ctx, db.AdvanceArtifactOfficeHoldParams{ArtifactID: id, BaseVersion: int64(a.Version)})
}

// ---- WOPI 端点 ----

// WOPIHandler 返回 WOPI 端点（挂在 WOPIPathPrefix + "*"）：
// GET /wopi/files/{id}（CheckFileInfo）、GET|POST /wopi/files/{id}/contents（GetFile / PutFile）、
// POST /wopi/files/{id}（X-WOPI-Override：LOCK、UNLOCK、REFRESH_LOCK、GET_LOCK）。
func (s *Service) WOPIHandler() http.Handler { return http.HandlerFunc(s.serveWOPI) }

func (s *Service) serveWOPI(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, WOPIPathPrefix+"files/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	id, sub, _ := strings.Cut(rest, "/")
	if !ValidID(id) || (sub != "" && sub != "contents") {
		http.NotFound(w, r)
		return
	}
	if !s.wopiAuth(w, r, id) {
		return
	}
	switch {
	case sub == "" && r.Method == http.MethodGet:
		s.wopiCheckFileInfo(w, r, id)
	case sub == "" && r.Method == http.MethodPost:
		s.wopiLock(w, r, id)
	case sub == "contents" && r.Method == http.MethodGet:
		s.wopiGetFile(w, r, id)
	case sub == "contents" && r.Method == http.MethodPost:
		s.wopiPutFile(w, r, id)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

// wopiAuth 校验访问令牌（会话存在、属于该 artifact、仍可用）与引擎签名（discovery 有 proof-key 时），通过后刷新
// 会话的活动时间。失败时已写出 401（或 500）。
func (s *Service) wopiAuth(w http.ResponseWriter, r *http.Request, id string) bool {
	ctx := r.Context()
	token := r.URL.Query().Get("access_token")
	if token == "" {
		http.Error(w, "missing access_token", http.StatusUnauthorized)
		return false
	}
	sess, err := s.st.Q.GetArtifactOfficeSessionByToken(ctx, tokenHash(token))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "invalid access_token", http.StatusUnauthorized)
		return false
	}
	if err != nil {
		slog.Warn("artifacts: 查 Office 编辑会话失败", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return false
	}
	now := s.now()
	if sess.ArtifactID != id || !officeSessionLive(sess, now) {
		http.Error(w, "invalid access_token", http.StatusUnauthorized)
		return false
	}
	e, wopiBase := s.officeEngine()
	if e == nil {
		http.Error(w, "office engine not configured", http.StatusUnauthorized)
		return false
	}
	disc, err := e.Discover(ctx)
	if err != nil {
		slog.Warn("artifacts: WOPI 请求验签前读 discovery 失败", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return false
	}
	if disc.Proof == nil {
		s.office.noProof.Do(func() {
			slog.Info("artifacts: Office 引擎的 discovery 没有 proof-key，WOPI 请求不验签（只凭访问令牌）")
		})
	} else {
		ticks, err := strconv.ParseInt(r.Header.Get("X-WOPI-TimeStamp"), 10, 64)
		if err == nil {
			err = disc.Proof.Verify(token, wopiBase+r.URL.RequestURI(), ticks, r.Header.Get("X-WOPI-Proof"), r.Header.Get("X-WOPI-ProofOld"), now)
		}
		if err != nil {
			slog.Warn("artifacts: WOPI 请求验签失败", "artifact", id, "err", err)
			http.Error(w, "invalid proof", http.StatusUnauthorized)
			return false
		}
	}
	if err := s.st.Q.TouchArtifactOfficeSession(ctx, db.TouchArtifactOfficeSessionParams{ID: sess.ID, ActiveAt: now.UnixMilli()}); err != nil {
		slog.Warn("artifacts: 刷新 Office 编辑会话失败", "err", err)
	}
	return true
}

// officeDoc 是编辑中的文档在一次 WOPI 请求里的状态。
type officeDoc struct {
	a    Artifact
	hold db.ArtifactOfficeHold
	// head 是最新版本的入口文件；modified 是最新版本的创建时刻（LastModifiedTime 与 X-COOL-WOPI-Timestamp 的比较基准）。
	head     db.ArtifactFile
	modified time.Time
}

// working 返回工作副本：有草稿且基准仍是最新版本时是草稿，否则是最新版本的入口文件。
func (d officeDoc) working() (sha string, size int64) {
	if d.hold.DraftSha != nil && d.hold.BaseVersion == int64(d.a.Version) {
		return *d.hold.DraftSha, deref(d.hold.DraftSize)
	}
	return d.head.Sha256, d.head.Size
}

// officeDoc 读编辑中的文档；不存在、已删除、不能编辑或没有占用时已写出 404。
func (s *Service) loadOfficeDoc(w http.ResponseWriter, r *http.Request, id string) (officeDoc, bool) {
	ctx := r.Context()
	fail := func(err error) (officeDoc, bool) {
		if _, op := AsOpError(err); op || errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
		} else {
			slog.Warn("artifacts: 读 Office 编辑文档失败", "artifact", id, "err", err)
			w.WriteHeader(http.StatusInternalServerError)
		}
		return officeDoc{}, false
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return fail(err)
	}
	if a.Deleted() || !officeEditable[a.Type] {
		return fail(notFound(id))
	}
	hold, err := s.st.Q.GetArtifactOfficeHold(ctx, id)
	if err != nil {
		return fail(err)
	}
	head, err := s.st.Q.GetArtifactFile(ctx, db.GetArtifactFileParams{ArtifactID: id, N: int64(a.Version), Path: TypeEntry(a.Type)})
	if err != nil {
		return fail(err)
	}
	v, err := s.Version(ctx, id, a.Version)
	if err != nil {
		return fail(err)
	}
	return officeDoc{a: a, hold: hold, head: head, modified: v.CreatedAt}, true
}

// wopiTime 是 WOPI 的 ISO 8601 时间（UTC，毫秒）。
func wopiTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (s *Service) wopiCheckFileInfo(w http.ResponseWriter, r *http.Request, id string) {
	d, ok := s.loadOfficeDoc(w, r, id)
	if !ok {
		return
	}
	_, size := d.working()
	name := "用户"
	if o, err := s.st.Q.GetOwner(r.Context()); err == nil && strings.TrimSpace(o.DisplayName) != "" {
		name = o.DisplayName
	}
	origin := ""
	if u, err := url.Parse(s.PublicURL()); err == nil {
		origin = u.Scheme + "://" + u.Host
	}
	writeWOPIJSON(w, map[string]any{
		"BaseFileName":            strings.ReplaceAll(d.a.Title, "/", "_") + OfficeExt(d.a.Type),
		"Size":                    size,
		"OwnerId":                 "oryn",
		"UserId":                  "owner",
		"UserFriendlyName":        name,
		"UserCanWrite":            true,
		"SupportsLocks":           true,
		"SupportsUpdate":          true,
		"UserCanNotWriteRelative": true,
		"LastModifiedTime":        wopiTime(d.modified),
		"PostMessageOrigin":       origin,
	})
}

func writeWOPIJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Service) wopiGetFile(w http.ResponseWriter, r *http.Request, id string) {
	d, ok := s.loadOfficeDoc(w, r, id)
	if !ok {
		return
	}
	sha, size := d.working()
	f, err := s.c.open(sha)
	if err != nil {
		slog.Warn("artifacts: 打开 Office 工作副本失败", "artifact", id, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("Cache-Control", "no-store")
	h.Set("X-WOPI-ItemVersion", strconv.Itoa(d.a.Version))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

// wopiConflict 回 409 并带上当前锁串。
func wopiConflict(w http.ResponseWriter, lock string) {
	w.Header().Set("X-WOPI-Lock", lock)
	w.WriteHeader(http.StatusConflict)
}

// wopiPutFile 存引擎发来的整个文件：自动保存只更新工作副本；显式保存与退出保存在内容与最新版本不同时以用户身份
// 落一个版本。正文先过该类型的 Inspect，不过回 400。回 {"LastModifiedTime"}，与 CheckFileInfo 同一基准。
func (s *Service) wopiPutFile(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	unlock := s.locks.lock(officeKey(id))
	defer unlock()
	d, ok := s.loadOfficeDoc(w, r, id)
	if !ok {
		return
	}
	if lock := r.Header.Get("X-WOPI-Lock"); d.hold.Lock != "" && lock != d.hold.Lock {
		wopiConflict(w, d.hold.Lock)
		return
	}
	if ts := r.Header.Get("X-COOL-WOPI-Timestamp"); ts != "" {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil && t.UnixMilli() != d.modified.UnixMilli() {
			// 引擎打开之后存储里的文件被别处改过（Collabora 据此提示「文档已在存储中更改」）
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"COOLStatusCode":1010}`))
			return
		}
	}
	// 读锁只罩住落内容：落下之后文件的 mtime 是新的，清理在 orphanGrace 内不会动它；officeCommit 里的 Write
	// 会自己再拿读锁（RWMutex 的读锁不能重入）
	release := s.holdContent()
	st, err := s.c.put(r.Body, MaxOfficeFileBytes, false)
	release()
	if errors.Is(err, errFileTooLarge) {
		http.Error(w, fmt.Sprintf("文件超过单文件上限 %d MiB", MaxOfficeFileBytes>>20), http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		slog.Warn("artifacts: 接收编辑器保存的文件失败", "artifact", id, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if msg := s.inspectStored(d.a.Type, st); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	modified := d.modified
	switch {
	case st.sha == d.head.Sha256:
		err = s.st.Q.AdvanceArtifactOfficeHold(ctx, db.AdvanceArtifactOfficeHoldParams{ArtifactID: id, BaseVersion: int64(d.a.Version)})
	case r.Header.Get("X-COOL-WOPI-IsAutosave") == "true":
		size := st.size
		err = s.st.Q.SetArtifactOfficeHoldDraft(ctx, db.SetArtifactOfficeHoldDraftParams{ArtifactID: id, DraftSha: &st.sha, DraftSize: &size})
	default:
		var v Version
		if _, err = s.officeCommit(ctx, id, int(d.hold.BaseVersion), st, ""); err == nil {
			if v, err = s.Version(ctx, id, 0); err == nil {
				modified = v.CreatedAt
			}
		}
	}
	if oe, ok := AsOpError(err); ok {
		http.Error(w, oe.Message, oe.Status())
		return
	}
	if err != nil {
		slog.Warn("artifacts: 保存编辑器的文件失败", "artifact", id, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	writeWOPIJSON(w, map[string]string{"LastModifiedTime": wopiTime(modified)})
}

// inspectStored 用类型的 Inspect 校验内容存储里的文件；通过时返回空串，否则返回问题说明。
func (s *Service) inspectStored(typ string, st stored) string {
	spec, ok := LookupType(typ)
	if !ok || spec.Inspect == nil {
		return "不是 Office 类型"
	}
	f, err := s.c.open(st.sha)
	if err != nil {
		return err.Error()
	}
	defer f.Close()
	if _, ps := spec.Inspect(f, st.size); len(ps) > 0 {
		return typedInvalid(typ, ps).Error()
	}
	return ""
}

// wopiLock 处理 LOCK（含带 X-WOPI-OldLock 的 UNLOCK_AND_RELOCK）、UNLOCK、REFRESH_LOCK、GET_LOCK；锁串不符回 409
// 并带当前锁串。其余 X-WOPI-Override 回 501。
func (s *Service) wopiLock(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	op := r.Header.Get("X-WOPI-Override")
	switch op {
	case "LOCK", "UNLOCK", "REFRESH_LOCK", "GET_LOCK":
	default:
		w.WriteHeader(http.StatusNotImplemented)
		return
	}
	unlock := s.locks.lock(officeKey(id))
	defer unlock()
	d, ok := s.loadOfficeDoc(w, r, id)
	if !ok {
		return
	}
	cur, lock, old := d.hold.Lock, r.Header.Get("X-WOPI-Lock"), r.Header.Get("X-WOPI-OldLock")
	set := func(v string) {
		if err := s.st.Q.SetArtifactOfficeHoldLock(ctx, db.SetArtifactOfficeHoldLockParams{ArtifactID: id, Lock: v}); err != nil {
			slog.Warn("artifacts: 更新 WOPI 锁失败", "artifact", id, "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-WOPI-ItemVersion", strconv.Itoa(d.a.Version))
		w.WriteHeader(http.StatusOK)
	}
	if op == "GET_LOCK" {
		w.Header().Set("X-WOPI-Lock", cur)
		w.WriteHeader(http.StatusOK)
		return
	}
	if lock == "" {
		http.Error(w, "missing X-WOPI-Lock", http.StatusBadRequest)
		return
	}
	switch {
	case op == "LOCK" && old != "":
		if cur != old {
			wopiConflict(w, cur)
			return
		}
		set(lock)
	case op == "LOCK":
		if cur != "" && cur != lock {
			wopiConflict(w, cur)
			return
		}
		set(lock)
	case op == "REFRESH_LOCK":
		if cur != lock {
			wopiConflict(w, cur)
			return
		}
		set(lock)
	default: // UNLOCK
		if cur != lock {
			wopiConflict(w, cur)
			return
		}
		set("")
	}
}
