package research

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// newRunID 生成运行 id（即投递标签，以 RunTagPrefix 开头）。
func newRunID() string { return ids.New(strings.TrimSuffix(RunTagPrefix, "_")) }

// insertRun 记一条 running 的运行。
func (s *Service) insertRun(ctx context.Context, r *runState) error {
	p := db.InsertResearchRunParams{ID: r.id, Pipeline: r.pipeline, Subject: r.subject, StartedAt: r.started.UnixMilli()}
	if r.topicID != "" {
		p.TopicID = &r.topicID
	}
	return s.st.Q.InsertResearchRun(ctx, p)
}

// finishRun 结束一条运行（status 为 completed / failed）。
func (s *Service) finishRun(ctx context.Context, id, status, outcome, report, errMsg string) {
	p := db.FinishResearchRunParams{ID: id, Status: status, Outcome: outcome, Report: truncRunes(report, reportMax), FinishedAt: new(s.now().UnixMilli())}
	if errMsg != "" {
		p.Error = new(truncRunes(errMsg, 500))
	}
	if err := s.st.Q.FinishResearchRun(ctx, p); err != nil {
		slog.Warn("research: 记录运行结果失败", "run", id, "err", err)
	}
	if status == "failed" {
		slog.Info("research: 研究运行失败", "run", id, "err", errMsg)
	}
}

// ListRuns 返回最近的运行（新的在前）。
func (s *Service) ListRuns(ctx context.Context, limit int) ([]apigen.ResearchRun, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	rows, err := s.st.Q.ListResearchRuns(ctx, int64(limit))
	if err != nil {
		return nil, err
	}
	out := make([]apigen.ResearchRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, apigen.ResearchRun{
			ID: r.ID, Pipeline: apigen.ResearchRunPipeline(r.Pipeline), TopicID: r.TopicID, Subject: r.Subject,
			Status: apigen.ResearchRunStatus(r.Status), Outcome: r.Outcome, Error: r.Error, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		})
	}
	return out, nil
}

// jsonFence 匹配 ```json 围栏块（围栏行后到下一个 ``` 行之前）。
var jsonFence = regexp.MustCompile("(?s)```json[ \\t]*\\r?\\n(.*?)\\r?\\n[ \\t]*```")

// lastJSONBlock 取回复里最后一个 ```json 围栏块，且必须是合法 JSON 对象。
func lastJSONBlock(text string) (json.RawMessage, bool) {
	m := jsonFence.FindAllStringSubmatch(text, -1)
	if len(m) == 0 {
		return nil, false
	}
	body := strings.TrimSpace(m[len(m)-1][1])
	if !strings.HasPrefix(body, "{") || !json.Valid([]byte(body)) {
		return nil, false
	}
	return json.RawMessage(body), true
}

// truncRunes 把 s 截到至多 n 个字符。
func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// cleanText 去首尾空白并截断。
func cleanText(s string, n int) string { return truncRunes(strings.TrimSpace(s), n) }

// cleanSources 只留 http(s) 来源，截断字段与条数。
func cleanSources(in []apigen.ResearchSource) []apigen.ResearchSource {
	out := []apigen.ResearchSource{}
	for _, src := range in {
		u := strings.TrimSpace(src.URL)
		l := strings.ToLower(u)
		if !strings.HasPrefix(l, "https://") && !strings.HasPrefix(l, "http://") {
			continue
		}
		if utf8.RuneCountInString(u) > sourceURLMax {
			continue
		}
		c := apigen.ResearchSource{URL: u, Title: cleanText(src.Title, sourceTitleMax)}
		if c.Title == "" {
			c.Title = u
		}
		if src.Date != nil && strings.TrimSpace(*src.Date) != "" {
			c.Date = new(cleanText(*src.Date, 20))
		}
		out = append(out, c)
		if len(out) == sourcesMax {
			break
		}
	}
	return out
}
