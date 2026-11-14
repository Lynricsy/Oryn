package stats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// MaxSampledCommits 是每线程采样的提交上限。
const MaxSampledCommits = 200

// maxGitBody 是 orbd git 应答的读取上限。
const maxGitBody = 8 << 20

var _ threads.Observer = (*Service)(nil)

// StateChanged 实现 threads.Observer：线程由 working / awaiting_input 转入 idle 时按线程去抖 SampleDelay 后采样提交。
// 回调在持有线程锁时调用：这里只登记计时器，采样在 Run 的循环里做。
func (s *Service) StateChanged(ev threads.StateEvent) {
	if ev.State != threads.StateIdle || (ev.Prev != threads.StateWorking && ev.Prev != threads.StateAwaitingInput) {
		return
	}
	id := ev.ThreadID
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.pending[id]; ok {
		old.stop()
	}
	s.gen++
	gen := s.gen
	stop := s.cfg.AfterFunc(s.cfg.SampleDelay, func() {
		s.mu.Lock()
		// 已被更晚的一次去抖取代：这次作废
		if p, ok := s.pending[id]; !ok || p.gen != gen {
			s.mu.Unlock()
			return
		}
		delete(s.pending, id)
		s.ready[id] = true
		s.mu.Unlock()
		select {
		case s.wake <- struct{}{}:
		default:
		}
	})
	s.pending[id] = pendingSample{gen: gen, stop: stop}
}

// InputRequested 实现 threads.Observer（不关心）。
func (s *Service) InputRequested(threads.InputEvent) {}

// InputResolved 实现 threads.Observer（不关心）。
func (s *Service) InputResolved(string, string) {}

// Seen 实现 threads.Observer（不关心）。
func (s *Service) Seen(string) {}

// TurnEnded 实现 threads.Observer（不关心）。
func (s *Service) TurnEnded(threads.TurnEvent) {}

// EntriesAppended 实现 threads.Observer（不关心）。
func (s *Service) EntriesAppended(string) {}

// drainSamples 采样全部去抖到期的线程。
func (s *Service) drainSamples(ctx context.Context) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.ready))
	for id := range s.ready {
		ids = append(ids, id)
	}
	clear(s.ready)
	s.mu.Unlock()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if err := s.SampleCommits(ctx, id); err != nil {
			slog.Debug("提交采样失败", "thread", id, "err", err)
		}
	}
}

// SampleCommits 读线程所在 orb 的 git 状态并整体替换 thread_commits 中该线程的行。线程须为工作线程、有 orb、
// 所属项目有仓库；orb 不可用时什么都不做（不唤醒、不计交互）。解析不出基线（或不是仓库）时只清空该线程的提交行，
// 绝不取 /git/commits——没有基线时它退化为整段 HEAD 历史。
func (s *Service) SampleCommits(ctx context.Context, threadID string) error {
	if s.orbs == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()
	t, err := s.st.Q.GetThread(ctx, threadID)
	if err != nil {
		return err
	}
	if t.Kind != threads.KindWork || t.OrbID == nil || t.ProjectID == nil {
		return nil
	}
	p, err := s.st.Q.GetProject(ctx, *t.ProjectID)
	if err != nil {
		return err
	}
	if p.RepoUrl == nil || *p.RepoUrl == "" {
		return nil
	}
	orbID := *t.OrbID
	if err := s.orbs.EnsureUsable(ctx, orbID, "git", false, 0); err != nil {
		return err
	}
	base := ""
	if p.BaseBranch != nil {
		base = *p.BaseBranch
	}
	q := url.Values{"scope": {"uncommitted"}}
	if base != "" {
		q.Set("base", base)
	}
	var st apigen.GitStatus
	if err := s.gitGet(ctx, orbID, "/git/status", q, &st); err != nil {
		return err
	}
	if !st.Repo || st.Base == nil {
		return s.st.Tx(ctx, func(q *db.Queries) error { return q.DeleteThreadCommits(ctx, threadID) })
	}
	q = url.Values{"limit": {fmt.Sprint(MaxSampledCommits)}}
	if base != "" {
		q.Set("base", base)
	}
	var list apigen.GitCommitList
	if err := s.gitGet(ctx, orbID, "/git/commits", q, &list); err != nil {
		return err
	}
	return s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.DeleteThreadCommits(ctx, threadID); err != nil {
			return err
		}
		for i, c := range list.Commits {
			if i == MaxSampledCommits {
				break
			}
			if err := q.InsertThreadCommit(ctx, db.InsertThreadCommitParams{ThreadID: threadID, Sha: c.Sha, Ts: c.TS}); err != nil {
				return err
			}
		}
		return nil
	})
}

// gitGet 经 orb 的 fs 流 GET orbd 的 git 接口并解析 JSON 应答。
func (s *Service) gitGet(ctx context.Context, orbID, path string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://orb"+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := s.orbs.Do(ctx, orbID, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGitBody))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("orbd %s: HTTP %d: %s", path, resp.StatusCode, body)
	}
	return json.Unmarshal(body, out)
}
