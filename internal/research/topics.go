package research

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 话题状态、种类与来源（与 apigen 的枚举同值）。
const (
	topicActive    = "active"
	topicPaused    = "paused"
	topicDone      = "done"
	topicDismissed = "dismissed"

	kindQuestion = "question"
	kindInterest = "interest"

	originMined  = "mined"
	originManual = "manual"
	originThread = "thread"
)

// normTitle 是去重用的标题规范化：小写、压缩空白。
func normTitle(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// looksLikeLink 报告自动挖出的话题是否带网址或 @ 引用（这些不登记：挖掘不应把私有对话里的地址带给公网研究员）。
func looksLikeLink(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "http://") || strings.Contains(l, "https://") || strings.Contains(l, "www.") || strings.Contains(l, "@")
}

// checkLen 校验去掉首尾空白后的长度（Unicode 字符数）。
func checkLen(field, v string, minLen, maxLen int) (string, error) {
	v = strings.TrimSpace(v)
	n := utf8.RuneCountInString(v)
	if n < minLen || n > maxLen {
		return "", errInvalid(fmt.Sprintf("%s must be %d-%d characters", field, minLen, maxLen))
	}
	return v, nil
}

// titleTaken 报告规范化后的标题是否与已有话题（含已删除的；except 除外）相同。
func (s *Service) titleTaken(ctx context.Context, title, except string) (bool, error) {
	rows, err := s.st.Q.ListResearchTopics(ctx, new("all"))
	if err != nil {
		return false, err
	}
	want := normTitle(title)
	for _, r := range rows {
		if r.ID != except && normTitle(r.Title) == want {
			return true, nil
		}
	}
	return false, nil
}

// CreateTopic 新建一个跟进话题：第一次检查在 webFirstDelay 之后；「问题跟进」questionTTL 后结束。
func (s *Service) CreateTopic(ctx context.Context, in TopicInput) (apigen.ResearchTopic, error) {
	kind := in.Kind
	if kind == "" {
		kind = kindQuestion
	}
	if kind != kindQuestion && kind != kindInterest {
		return apigen.ResearchTopic{}, errInvalid("kind must be question or interest")
	}
	origin := in.Origin
	if origin == "" {
		origin = originManual
	}
	if origin != originMined && origin != originManual && origin != originThread {
		return apigen.ResearchTopic{}, errInvalid("unknown origin")
	}
	title, err := checkLen("title", in.Title, 1, titleMax)
	if err != nil {
		return apigen.ResearchTopic{}, err
	}
	question, err := checkLen("question", in.Question, 1, questionMax)
	if err != nil {
		return apigen.ResearchTopic{}, err
	}
	baseline, err := checkLen("baseline", in.Baseline, 0, baselineMax)
	if err != nil {
		return apigen.ResearchTopic{}, err
	}
	if origin == originMined && (looksLikeLink(title) || looksLikeLink(question)) {
		return apigen.ResearchTopic{}, errInvalid("mined topics must not contain links or @ mentions")
	}
	if taken, err := s.titleTaken(ctx, title, ""); err != nil {
		return apigen.ResearchTopic{}, err
	} else if taken {
		return apigen.ResearchTopic{}, errInvalid("a topic with this title already exists")
	}
	active, err := s.st.Q.CountActiveResearchTopics(ctx)
	if err != nil {
		return apigen.ResearchTopic{}, err
	}
	if active >= maxActiveTopics {
		return apigen.ResearchTopic{}, errInvalid(fmt.Sprintf("at most %d active topics", maxActiveTopics))
	}
	now := s.now()
	p := db.InsertResearchTopicParams{
		ID: ids.New("rtp"), Kind: kind, Title: title, Question: question, Baseline: baseline, Origin: origin,
		State: topicActive, IntervalS: int64(webBaseInterval / time.Second), NextRunAt: new(now.Add(webFirstDelay).UnixMilli()),
		CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli(),
	}
	if in.SourceThreadID != "" {
		p.SourceThreadID = &in.SourceThreadID
	}
	if kind == kindQuestion {
		p.ExpiresAt = new(now.Add(questionTTL).UnixMilli())
	}
	if err := s.st.Q.InsertResearchTopic(ctx, p); err != nil {
		return apigen.ResearchTopic{}, err
	}
	s.Wake()
	return s.GetTopic(ctx, p.ID)
}

// GetTopic 读一个话题。
func (s *Service) GetTopic(ctx context.Context, id string) (apigen.ResearchTopic, error) {
	row, err := s.getTopic(ctx, id)
	if err != nil {
		return apigen.ResearchTopic{}, err
	}
	return topicToAPI(row), nil
}

func (s *Service) getTopic(ctx context.Context, id string) (db.ResearchTopic, error) {
	row, err := s.st.Q.GetResearchTopic(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.ResearchTopic{}, &Error{Code: CodeNotFound, Message: "topic not found"}
	}
	return row, err
}

// ListTopics 列出话题：state 空 = 除 dismissed 之外的全部；"all" = 全部。
func (s *Service) ListTopics(ctx context.Context, state string) ([]apigen.ResearchTopic, error) {
	var filter *string
	if state != "" {
		filter = &state
	}
	rows, err := s.st.Q.ListResearchTopics(ctx, filter)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.ResearchTopic, 0, len(rows))
	for _, r := range rows {
		out = append(out, topicToAPI(r))
	}
	return out, nil
}

// UpdateTopic 修改话题的标题、问题、种类，或在 active / paused 之间切换。
func (s *Service) UpdateTopic(ctx context.Context, id string, p apigen.ResearchTopicPatch) (apigen.ResearchTopic, error) {
	row, err := s.getTopic(ctx, id)
	if err != nil {
		return apigen.ResearchTopic{}, err
	}
	if row.State == topicDone || row.State == topicDismissed {
		return apigen.ResearchTopic{}, errInvalid("the topic is no longer active")
	}
	now := s.now()
	meta := db.UpdateResearchTopicMetaParams{ID: id, Title: row.Title, Question: row.Question, Kind: row.Kind, ExpiresAt: row.ExpiresAt, UpdatedAt: now.UnixMilli()}
	if p.Title != nil {
		if meta.Title, err = checkLen("title", *p.Title, 1, titleMax); err != nil {
			return apigen.ResearchTopic{}, err
		}
		if taken, err := s.titleTaken(ctx, meta.Title, id); err != nil {
			return apigen.ResearchTopic{}, err
		} else if taken {
			return apigen.ResearchTopic{}, errInvalid("a topic with this title already exists")
		}
	}
	if p.Question != nil {
		if meta.Question, err = checkLen("question", *p.Question, 1, questionMax); err != nil {
			return apigen.ResearchTopic{}, err
		}
	}
	if p.Kind != nil && string(*p.Kind) != row.Kind {
		switch *p.Kind {
		case apigen.ResearchTopicQuestion:
			meta.Kind, meta.ExpiresAt = kindQuestion, new(now.Add(questionTTL).UnixMilli())
		case apigen.ResearchTopicInterest:
			meta.Kind, meta.ExpiresAt = kindInterest, nil
		default:
			return apigen.ResearchTopic{}, errInvalid("kind must be question or interest")
		}
	}
	if _, err := s.st.Q.UpdateResearchTopicMeta(ctx, meta); err != nil {
		return apigen.ResearchTopic{}, err
	}
	if p.State != nil && string(*p.State) != row.State {
		switch *p.State {
		case apigen.ResearchTopicPatchActive:
			if row.State == topicPaused {
				active, err := s.st.Q.CountActiveResearchTopics(ctx)
				if err != nil {
					return apigen.ResearchTopic{}, err
				}
				if active >= maxActiveTopics {
					return apigen.ResearchTopic{}, errInvalid(fmt.Sprintf("at most %d active topics", maxActiveTopics))
				}
			}
			next := now.Add(time.Duration(row.IntervalS) * time.Second).UnixMilli()
			err = s.setState(ctx, id, topicActive, &next)
		case apigen.ResearchTopicPatchPaused:
			err = s.setState(ctx, id, topicPaused, nil)
		default:
			err = errInvalid("state must be active or paused")
		}
		if err != nil {
			return apigen.ResearchTopic{}, err
		}
	}
	s.Wake()
	return s.GetTopic(ctx, id)
}

func (s *Service) setState(ctx context.Context, id, state string, next *int64) error {
	_, err := s.st.Q.SetResearchTopicState(ctx, db.SetResearchTopicStateParams{ID: id, State: state, NextRunAt: next, UpdatedAt: s.now().UnixMilli()})
	return err
}

// DismissTopic 删除话题：置为 dismissed（标题保留，挖掘不会再登记同名话题）。
func (s *Service) DismissTopic(ctx context.Context, id string) error {
	if _, err := s.getTopic(ctx, id); err != nil {
		return err
	}
	return s.setState(ctx, id, topicDismissed, nil)
}

// RunTopicNow 让活跃话题立即到期。
func (s *Service) RunTopicNow(ctx context.Context, id string) (apigen.ResearchTopic, error) {
	row, err := s.getTopic(ctx, id)
	if err != nil {
		return apigen.ResearchTopic{}, err
	}
	if row.State != topicActive {
		return apigen.ResearchTopic{}, errInvalid("only active topics can be checked")
	}
	if err := s.setState(ctx, id, topicActive, new(s.now().UnixMilli())); err != nil {
		return apigen.ResearchTopic{}, err
	}
	s.Wake()
	return s.GetTopic(ctx, id)
}

// topicSources 解出话题的上一次来源。
func topicSources(row db.ResearchTopic) []apigen.ResearchSource {
	out := []apigen.ResearchSource{}
	_ = json.Unmarshal([]byte(row.LastSources), &out)
	return out
}

// topicToAPI 把话题行转成接口类型。
func topicToAPI(row db.ResearchTopic) apigen.ResearchTopic {
	return apigen.ResearchTopic{
		ID: row.ID, Kind: apigen.ResearchTopicKind(row.Kind), Title: row.Title, Question: row.Question, Baseline: row.Baseline,
		Origin: apigen.ResearchTopicOrigin(row.Origin), SourceThreadID: row.SourceThreadID, State: apigen.ResearchTopicState(row.State),
		IntervalSeconds: int(row.IntervalS), NextRunAt: row.NextRunAt, ExpiresAt: row.ExpiresAt, LastRunAt: row.LastRunAt,
		RunCount: int(row.RunCount), LastReport: row.LastReport, LastSources: topicSources(row),
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
