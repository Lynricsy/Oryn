package research

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 内部数据关联（步骤 3.1）：工作线程有新活动时，私有研究员用只读宿主工具翻看线程，报告跨线程的矛盾、重复与卡住。
// 发现来自主人自己的数据，进主线时不受限。

// internalOutputFormat 是内部运行的输出格式说明。
const internalOutputFormat = "End your reply with exactly one ```json code block and nothing after it:\n" +
	"```json\n" +
	`{"findings":[{"title":"…","summary":"…","threads":["<thread id>"]}]}` + "\n" +
	"```\n" +
	`Give at most 3 findings, each with the ids of the threads involved; give an empty array when nothing qualifies (that is the usual answer).`

// internalPrompt 是一次内部运行的提示词。
func internalPrompt(since time.Time, reported []string, preferences string) string {
	var b strings.Builder
	b.WriteString("[Oryn research task · internal]\n")
	b.WriteString("Use list_threads and read_thread to look at the work threads with activity since the time below; use search_history, recall_memory and recall_engineering when they help. ")
	b.WriteString("Report only findings that span threads: a decision in one thread that contradicts another thread or a PR comment, two threads changing the same thing, ")
	b.WriteString("something a thread is waiting for that was solved or broken elsewhere, or work that looks forgotten. Do not report the progress of a single thread on its own. ")
	b.WriteString("Do not repeat what was already reported.\n\n")
	b.WriteString(internalOutputFormat)
	fmt.Fprintf(&b, "\n\nsince: %s\n", since.UTC().Format(time.RFC3339))
	b.WriteString("\nOwner's research preferences:\n" + preferencesText(preferences))
	b.WriteString("\n\nAlready reported:\n" + listBlock(reported) + "\n")
	return b.String()
}

// recentReported 取一条管线最近 n 次有发现的运行里报告过的标题（运行的 report 存标题组成的 JSON 数组）。
func (s *Service) recentReported(ctx context.Context, pipeline string, runs, max int) []string {
	reports, err := s.st.Q.ListRecentResearchReports(ctx, db.ListRecentResearchReportsParams{Pipeline: pipeline, Lim: int64(runs)})
	if err != nil {
		slog.Warn("research: 读取已报告的标题失败", "pipeline", pipeline, "err", err)
		return nil
	}
	var out []string
	for _, r := range reports {
		var titles []string
		if json.Unmarshal([]byte(r), &titles) != nil {
			continue
		}
		for _, t := range titles {
			if len(out) == max {
				return out
			}
			out = append(out, t)
		}
	}
	return out
}

// dueInternal 取到期的内部运行：内部关联开着、距上次不少于 internalEvery、其间有工作线程产生新消息。没有新消息时
// 只把游标推进到现在，不调用模型。
func (s *Service) dueInternal(ctx context.Context, set settingsRow) (*job, error) {
	if set.Internal == 0 {
		return nil, nil
	}
	now := s.now()
	var since time.Time
	if set.InternalCursor == nil {
		// 第一次：从 internalEvery 之前看起，并把起点记下（失败重试时仍从这里看起）
		since = now.Add(-internalEvery)
		s.setInternalCursor(ctx, since)
	} else {
		since = time.UnixMilli(*set.InternalCursor)
		if now.Sub(since) < internalEvery {
			return nil, nil
		}
	}
	n, err := s.st.Q.CountWorkThreadsActiveSince(ctx, new(since.UnixMilli()))
	if err != nil {
		return nil, err
	}
	if n == 0 {
		s.setInternalCursor(ctx, now)
		return nil, nil
	}
	reported := s.recentReported(ctx, PipelineInternal, 10, 30)
	return &job{pipeline: PipelineInternal, prompt: internalPrompt(since, reported, set.Preferences)}, nil
}

func (s *Service) setInternalCursor(ctx context.Context, t time.Time) {
	if err := s.st.Q.SetResearchInternalCursor(ctx, db.SetResearchInternalCursorParams{Cursor: new(t.UnixMilli()), UpdatedAt: s.now().UnixMilli()}); err != nil {
		slog.Warn("research: 记录内部关联游标失败", "err", err)
	}
}

// findingsReport 是内部运行与 MCP 运行报告的 json 块。
type findingsReport struct {
	Findings []struct {
		Title   string   `json:"title"`
		Summary string   `json:"summary"`
		Threads []string `json:"threads"`
		Source  string   `json:"source"`
	} `json:"findings"`
}

// applyInternal 逐条发出不受限的发现（至多 internalMaxFindings 条），成功后把游标推进到运行开始的时间。
func (s *Service) applyInternal(ctx context.Context, r *runState, raw json.RawMessage) (outcome, report string, err error) {
	var rep findingsReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		return "", "", fmt.Errorf("invalid report: %w", err)
	}
	var titles []string
	for _, f := range rep.Findings {
		if len(titles) == internalMaxFindings {
			break
		}
		title, summary := cleanText(f.Title, titleMax), cleanText(f.Summary, summaryMax)
		if title == "" || summary == "" {
			continue
		}
		var threadIDs []string
		for _, id := range f.Threads {
			if id = strings.TrimSpace(id); id != "" && len(threadIDs) < 10 {
				threadIDs = append(threadIDs, truncRunes(id, 64))
			}
		}
		fd := Finding{RunID: r.id, Pipeline: PipelineInternal, Title: title, Summary: summary, Threads: threadIDs}
		if len(threadIDs) > 0 {
			fd.ThreadID = threadIDs[0]
		}
		s.cfg.Host.PostFinding(ctx, fd)
		titles = append(titles, title)
	}
	s.setInternalCursor(ctx, r.started)
	return findingsOutcome(titles)
}

// findingsOutcome 是有发现的运行的 outcome 与 report（标题组成的 JSON 数组）。
func findingsOutcome(titles []string) (outcome, report string, err error) {
	if len(titles) == 0 {
		return "nothing", "", nil
	}
	b, _ := json.Marshal(titles)
	return fmt.Sprintf("findings:%d", len(titles)), string(b), nil
}
