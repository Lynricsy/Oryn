package artifacts

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 用户同意（M14 计划步骤 7.3）。
//
// files 与 sample 两个能力要用户同意才能用。同意按「artifact × 能力」持久化在 artifact_consents 里，设置页可以收回；
// 拒绝只记在内存里，持续到没有任何连接在看这个 artifact 为止（之后再打开会重新询问）。
//
// 页面 use 一个还没获同意的能力时：没有任何观看者就直接按拒绝处理（缩略图、无人值守时没人能回答），不发事件；
// 否则向观看者发 artifact.consent {kind: "asked"}，最多等 ConsentWait（刻意短于页面 use 的 10 秒超时）。超时这次
// use 回 null，问题保持打开。用户经 Answer 回答，第一个回答生效；随后发 {kind: "settled", reload}，reload 在
// 「页面已经不在等」时为真，查看器据此重载帧。未获同意时 call / stream 一律回 not_allowed，不询问。

// ConsentWait 是 use 等待用户回答的上限。
const ConsentWait = 9 * time.Second

// consentCaps 是需要用户同意的能力。
var consentCaps = map[string]bool{"files": true, "sample": true}

// consentSet 是同意的进程内状态（零值可用）。
type consentSet struct {
	mu  sync.Mutex
	hub ActivityHub
	// open：请求 id → 打开着的请求；每个 artifact × 能力至多一个。
	open map[string]*consentAsk
	// denied：artifact id → 本观看期内被拒绝的能力。
	denied map[string]map[string]bool
	// timer 安排一次等待（nil 时用 time.NewTimer；测试注入假计时器）。
	timer func(d time.Duration) (<-chan time.Time, func() bool)
}

// consentAsk 是一个打开着的同意请求。settled、waiting、told 由 consentSet.mu 保护；allowed 在 done 关闭之前写定。
type consentAsk struct {
	msg     apigen.WsServerArtifactConsent
	cap     string
	done    chan struct{}
	settled bool
	allowed bool
	// waiting 是正在等这个回答的 use 数。
	waiting int
	// told 是已经收到 asked 的连接。
	told map[string]bool
}

// AttachConsentHub 接上观看者登记（与在场共用 realtime 的观看登记）并登记观看者变化的回调；须在开始服务之前调用。
func (s *Service) AttachConsentHub(h ActivityHub) {
	s.consent.mu.Lock()
	s.consent.hub = h
	s.consent.mu.Unlock()
	h.OnWatchChange(s.consentWatchChanged)
}

func (s *Service) consentHub() ActivityHub {
	s.consent.mu.Lock()
	defer s.consent.mu.Unlock()
	return s.consent.hub
}

// consentGate 是调度器的同意检查：不需要同意或已获同意时回 nil。use（ask 为真）在有观看者时询问并等回答；
// call 与 stream 不询问。拒绝一律是 not_allowed。
func (s *Service) consentGate(ctx context.Context, a Artifact, capability string, ask bool) error {
	if !consentCaps[capability] {
		return nil
	}
	_, err := s.st.Q.GetArtifactConsent(ctx, db.GetArtifactConsentParams{ArtifactID: a.ID, Capability: capability})
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	deny := opErr(codeNotAllowed, "用户还没有同意这个页面使用能力 %s。", capability)
	if !ask {
		return deny
	}
	q, err := s.askConsent(ctx, a, capability)
	if err != nil {
		return err
	}
	if q == nil {
		return deny
	}
	newTimer := s.consent.timer
	if newTimer == nil {
		newTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
			t := time.NewTimer(d)
			return t.C, t.Stop
		}
	}
	expired, stop := newTimer(ConsentWait)
	defer stop()
	select {
	case <-q.done:
	case <-expired:
	case <-ctx.Done():
	}
	s.consent.mu.Lock()
	settled := q.settled
	if !settled {
		q.waiting--
	}
	s.consent.mu.Unlock()
	if settled {
		// 回答已经落定（或正在落库）：等它写定 allowed
		<-q.done
		if q.allowed {
			return nil
		}
	}
	return deny
}

// askConsent 找到或打开 artifact × 能力的同意请求，并登记一个等待者。本观看期内已被拒绝或没有观看者时回 nil。
func (s *Service) askConsent(ctx context.Context, a Artifact, capability string) (*consentAsk, error) {
	hub := s.consentHub()
	if hub == nil {
		return nil, nil
	}
	viewers := hub.Viewers(a.ID)
	if len(viewers) == 0 {
		return nil, nil
	}
	var project *string
	if a.ProjectID != "" {
		p, err := s.st.Q.GetProject(ctx, a.ProjectID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			project = &p.Name
		}
	}
	s.consent.mu.Lock()
	if s.consent.denied[a.ID][capability] {
		s.consent.mu.Unlock()
		return nil, nil
	}
	for _, q := range s.consent.open {
		if q.msg.ArtifactID == a.ID && q.cap == capability {
			q.waiting++
			s.consent.mu.Unlock()
			return q, nil
		}
	}
	title, c := a.Title, apigen.ArtifactConsentCapability(capability)
	q := &consentAsk{
		msg: apigen.WsServerArtifactConsent{
			Type: apigen.WsServerArtifactConsentTypeArtifactConsent, Kind: apigen.ArtifactConsentAsked,
			RequestID: ids.New("acr"), ArtifactID: a.ID, Title: &title, Project: project, Capability: &c,
		},
		cap: capability, done: make(chan struct{}), waiting: 1, told: map[string]bool{},
	}
	for _, v := range viewers {
		q.told[v.Conn] = true
	}
	if s.consent.open == nil {
		s.consent.open = map[string]*consentAsk{}
	}
	s.consent.open[q.msg.RequestID] = q
	s.consent.mu.Unlock()
	hub.ToWatchers(a.ID, q.msg)
	return q, nil
}

// AnswerConsent 回答同意请求 requestID：第一个回答生效，请求已结束（或不存在）时回 not_found。允许时写库，拒绝时
// 记进内存直到没人在看；之后向观看者发 settled。
func (s *Service) AnswerConsent(ctx context.Context, requestID string, allow bool) error {
	s.consent.mu.Lock()
	q := s.consent.open[requestID]
	if q == nil {
		s.consent.mu.Unlock()
		return opErr(CodeNotFound, "这个同意请求已经结束。")
	}
	delete(s.consent.open, requestID)
	q.settled = true
	reload := q.waiting == 0
	id := q.msg.ArtifactID
	if !allow {
		if s.consent.denied == nil {
			s.consent.denied = map[string]map[string]bool{}
		}
		if s.consent.denied[id] == nil {
			s.consent.denied[id] = map[string]bool{}
		}
		s.consent.denied[id][q.cap] = true
	}
	hub := s.consent.hub
	s.consent.mu.Unlock()

	var err error
	if allow {
		err = s.st.Q.UpsertArtifactConsent(ctx, db.UpsertArtifactConsentParams{ArtifactID: id, Capability: q.cap, GrantedAt: s.now().UnixMilli()})
	}
	q.allowed = allow && err == nil
	close(q.done)
	if hub != nil {
		msg := q.msg
		msg.Kind, msg.Title, msg.Project = apigen.ArtifactConsentSettled, nil, nil
		msg.Allowed, msg.Reload = &q.allowed, &reload
		hub.ToWatchers(id, msg)
	}
	return err
}

// consentWatchChanged 在观看者变化时调用：没人在看了就清掉拒绝并结束打开着的请求（按拒绝，没人能回答）；
// 来了新的观看者就把打开着的请求再发一次 asked（客户端按 requestId 去重）。
func (s *Service) consentWatchChanged(id string) {
	hub := s.consentHub()
	if hub == nil {
		return
	}
	viewers := hub.Viewers(id)
	var resend []apigen.WsServerArtifactConsent
	s.consent.mu.Lock()
	if len(viewers) == 0 {
		delete(s.consent.denied, id)
	}
	for rid, q := range s.consent.open {
		if q.msg.ArtifactID != id {
			continue
		}
		if len(viewers) == 0 {
			delete(s.consent.open, rid)
			q.settled = true
			close(q.done)
			continue
		}
		fresh := false
		for _, v := range viewers {
			if !q.told[v.Conn] {
				q.told[v.Conn], fresh = true, true
			}
		}
		if fresh {
			resend = append(resend, q.msg)
		}
	}
	s.consent.mu.Unlock()
	for _, m := range resend {
		hub.ToWatchers(id, m)
	}
}

// Consents 列出已授权的 artifact（不含回收站里的），最近授权的在前；同一 artifact 的能力合成一行。
func (s *Service) Consents(ctx context.Context) ([]apigen.ArtifactConsent, error) {
	rows, err := s.st.Q.ListArtifactConsents(ctx)
	if err != nil {
		return nil, err
	}
	out := []apigen.ArtifactConsent{}
	at := map[string]int{}
	for _, r := range rows {
		i, ok := at[r.ArtifactID]
		if !ok {
			i = len(out)
			at[r.ArtifactID] = i
			out = append(out, apigen.ArtifactConsent{ArtifactID: r.ArtifactID, Title: r.Title, ProjectID: r.ProjectID, GrantedAt: r.GrantedAt})
		}
		out[i].Capabilities = append(out[i].Capabilities, apigen.ArtifactConsentCapability(r.Capability))
	}
	for i := range out {
		slices.Sort(out[i].Capabilities)
	}
	return out, nil
}

// RevokeConsents 收回 artifact id 的全部能力授权（没有授权时什么也不做）。
func (s *Service) RevokeConsents(ctx context.Context, id string) error {
	if !ValidID(id) {
		return opErr(CodeInvalid, "artifact id 不合法。")
	}
	_, err := s.st.Q.DeleteArtifactConsents(ctx, id)
	return err
}
