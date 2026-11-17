package ownermem

// topics.go：主题整理与刷新（R8、R9）。每天一次：对 bank 做一次带 response_schema 的 reflect，得到主人当前的长期主题，
// 与已有主题比对后新建或归档（上限 12，owner-profile 不归档）；然后对每个活跃主题做一次 recall（不调 LLM），
// 有上次刷新之后提到的事实才刷新，owner-profile 每周至少刷新一次，同一主题两次刷新间隔不少于 20 小时。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	maxTopics       = 12
	curateInterval  = 24 * time.Hour
	refreshMinGap   = 20 * time.Hour
	profileMaxAge   = 7 * 24 * time.Hour
	curateMaxTokens = 2048
	checkMaxTokens  = 4096
	topicStatusOn   = "active"
	topicStatusOff  = "archived"
)

// curateSchema 是主题整理 reflect 的结构化输出。
var curateSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"topics": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":              map[string]any{"type": "string"},
					"scope":             map[string]any{"type": "string"},
					"keep_existing_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required": []string{"name", "scope"},
			},
		},
	},
	"required": []string{"topics"},
}

type curatedTopic struct {
	Name            string   `json:"name"`
	Scope           string   `json:"scope"`
	KeepExistingIDs []string `json:"keep_existing_ids"`
}

// curateQuery 是主题整理的问题：列出现有主题（id、名称、范围、最近有新事实的日期），请模型给出当前的长期主题。
func curateQuery(topics []db.OwnerMemoryTopic) string {
	var b strings.Builder
	b.WriteString("请整理主人当前的长期主题：主人持续投入、以后还会被问到的项目、目标、兴趣或生活事务。")
	fmt.Fprintf(&b, "最多 %d 个；不要包含主人画像（身份、背景、设备、偏好），那由固定主题负责；短期的一次性事务不算主题。\n", maxTopics-1)
	b.WriteString("每个主题给出简短名称（10 字以内）与一句范围说明。")
	b.WriteString("如果它延续下面某个已有主题，在 keep_existing_ids 里填那个主题的 id（名称不必相同）；长期没有新事实的已有主题不要再列出。\n")
	b.WriteString("按重要程度排序。\n\n已有主题：\n")
	n := 0
	for _, t := range topics {
		if t.Seed != 0 {
			continue
		}
		n++
		last := "无记录"
		if t.LastHitAt != nil {
			last = time.UnixMilli(*t.LastHitAt).UTC().Format("2006-01-02")
		}
		fmt.Fprintf(&b, "- %s：%s（%s；%s；最近有新事实：%s）\n", t.ModelID, t.Name, t.Scope, t.Status, last)
	}
	if n == 0 {
		b.WriteString("（无）\n")
	}
	return b.String()
}

// newTopicID 生成动态主题的 id。
func newTopicID(name string, now time.Time) string {
	sum := sha256.Sum256([]byte(name + "\x00" + now.Format(time.RFC3339Nano)))
	return TopicPrefix + hex.EncodeToString(sum[:5])
}

// Curate 运行一次主题整理，结果写进设置（管理页可见）。失败时也记录带错误的结果并返回错误。
func (s *Service) Curate(ctx context.Context) (apigen.OwnerMemoryCuration, error) {
	now := s.cfg.Now()
	cur := apigen.OwnerMemoryCuration{At: now.UnixMilli(), Created: []string{}, Kept: []string{}, Archived: []string{}}
	err := s.curate(ctx, now, &cur)
	if err != nil {
		if errors.Is(err, errUnavailable) {
			return cur, err
		}
		msg := err.Error()
		cur.Error = &msg
	}
	raw, _ := json.Marshal(cur)
	text := string(raw)
	if serr := s.cfg.Q.SetOwnerMemoryCuration(context.WithoutCancel(ctx), db.SetOwnerMemoryCurationParams{
		LastCuratedAt: &cur.At, LastCuration: &text,
	}); serr != nil {
		return cur, serr
	}
	return cur, err
}

func (s *Service) curate(ctx context.Context, now time.Time, cur *apigen.OwnerMemoryCuration) error {
	b, err := s.bank(ctx)
	if err != nil {
		return err
	}
	topics, err := s.cfg.Q.ListOwnerMemoryTopics(ctx)
	if err != nil {
		return err
	}
	var resp struct {
		StructuredOutput      *struct{ Topics []curatedTopic } `json:"structured_output"`
		StructuredOutputError *string                          `json:"structured_output_error"`
	}
	if err := b.Call(ctx, http.MethodPost, "/reflect", map[string]any{
		"query": curateQuery(topics), "budget": "mid", "max_tokens": curateMaxTokens, "response_schema": curateSchema,
	}, &resp); err != nil {
		return upstream("主题整理", err)
	}
	if resp.StructuredOutput == nil {
		if resp.StructuredOutputError != nil {
			return &Error{Status: http.StatusBadGateway, Code: "memory_upstream", Message: "主题整理没有得到结构化结果：" + *resp.StructuredOutputError}
		}
		resp.StructuredOutput = &struct{ Topics []curatedTopic }{}
	}

	byID := make(map[string]db.OwnerMemoryTopic, len(topics))
	active := 0
	for _, t := range topics {
		byID[t.ModelID] = t
		if t.Seed != 0 && t.Status == topicStatusOn {
			active++
		}
	}
	kept := map[string]bool{}
	nowMs := now.UnixMilli()
	position := int64(1)
	for _, ct := range resp.StructuredOutput.Topics {
		name, scope := strings.TrimSpace(ct.Name), strings.TrimSpace(ct.Scope)
		if name == "" || scope == "" || active >= maxTopics {
			continue
		}
		var keep *db.OwnerMemoryTopic
		for _, id := range ct.KeepExistingIDs {
			if t, ok := byID[id]; ok && t.Seed == 0 && !kept[id] {
				keep = &t
				break
			}
		}
		if keep != nil {
			// 延续已有主题：名称与范围保持不变（不改 source_query，内容不必重写），归档的恢复
			kept[keep.ModelID] = true
			if err := s.cfg.Q.UpsertOwnerMemoryTopic(ctx, db.UpsertOwnerMemoryTopicParams{
				ModelID: keep.ModelID, Name: keep.Name, Scope: keep.Scope, Status: topicStatusOn, Position: position, Now: nowMs,
			}); err != nil {
				return err
			}
			cur.Kept = append(cur.Kept, keep.Name)
		} else {
			id := newTopicID(name, now)
			if err := b.Call(ctx, http.MethodPost, "/mental-models", topicSpec(id, name, scope), nil); err != nil {
				return upstream("创建主题「"+name+"」", err)
			}
			if err := s.cfg.Q.UpsertOwnerMemoryTopic(ctx, db.UpsertOwnerMemoryTopicParams{
				ModelID: id, Name: name, Scope: scope, Status: topicStatusOn, Position: position, Now: nowMs,
			}); err != nil {
				return err
			}
			// 创建即生成内容，算作一次刷新
			if err := s.cfg.Q.SetOwnerMemoryTopicRefreshed(ctx, db.SetOwnerMemoryTopicRefreshedParams{LastRefreshAt: &nowMs, ModelID: id}); err != nil {
				return err
			}
			cur.Created = append(cur.Created, name)
		}
		active++
		position++
	}
	for _, t := range topics {
		if t.Seed != 0 || t.Status != topicStatusOn || kept[t.ModelID] {
			continue
		}
		if err := s.cfg.Q.UpsertOwnerMemoryTopic(ctx, db.UpsertOwnerMemoryTopicParams{
			ModelID: t.ModelID, Name: t.Name, Scope: t.Scope, Status: topicStatusOff, Position: t.Position, Now: nowMs,
		}); err != nil {
			return err
		}
		cur.Archived = append(cur.Archived, t.Name)
	}
	return nil
}

// RefreshDue 检查每个活跃主题是否需要刷新（R9），提交需要的刷新。返回提交的主题数。
func (s *Service) RefreshDue(ctx context.Context) (int, error) {
	b, err := s.bank(ctx)
	if err != nil {
		return 0, err
	}
	topics, err := s.cfg.Q.ListOwnerMemoryTopics(ctx)
	if err != nil {
		return 0, err
	}
	now := s.cfg.Now()
	n := 0
	for _, t := range topics {
		if t.Status != topicStatusOn {
			continue
		}
		var last time.Time
		if t.LastRefreshAt != nil {
			last = time.UnixMilli(*t.LastRefreshAt)
			if now.Sub(last) < refreshMinGap {
				continue
			}
		}
		need := t.LastRefreshAt == nil || (t.Seed != 0 && now.Sub(last) >= profileMaxAge)
		if !need {
			hit, err := s.newestMention(ctx, b, t)
			if err != nil {
				return n, err
			}
			if hit.After(last) {
				ms := hit.UnixMilli()
				if err := s.cfg.Q.SetOwnerMemoryTopicHit(ctx, db.SetOwnerMemoryTopicHitParams{LastHitAt: &ms, ModelID: t.ModelID}); err != nil {
					return n, err
				}
				need = true
			}
		}
		if !need {
			continue
		}
		if err := s.refresh(ctx, b, t.ModelID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// newestMention 用主题的范围做一次 recall（不调 LLM），返回结果里最新的 mentioned_at；没有结果时为零值。
func (s *Service) newestMention(ctx context.Context, b *memory.Bank, t db.OwnerMemoryTopic) (time.Time, error) {
	var resp struct {
		Results []struct {
			MentionedAt *string `json:"mentioned_at"`
		} `json:"results"`
	}
	if err := b.Call(ctx, http.MethodPost, "/memories/recall", map[string]any{
		"query": "主题「" + t.Name + "」：" + t.Scope, "budget": "low", "max_tokens": checkMaxTokens,
	}, &resp); err != nil {
		return time.Time{}, upstream("检查主题「"+t.Name+"」", err)
	}
	var newest time.Time
	for _, r := range resp.Results {
		if r.MentionedAt == nil {
			continue
		}
		if at, err := time.Parse(time.RFC3339Nano, *r.MentionedAt); err == nil && at.After(newest) {
			newest = at
		}
	}
	return newest, nil
}

// refresh 提交一次刷新（按主题自己的 delta 模式）并记下时间。
func (s *Service) refresh(ctx context.Context, b *memory.Bank, id string) error {
	if err := b.Call(ctx, http.MethodPost, "/mental-models/"+url.PathEscape(id)+"/refresh", nil, nil); err != nil {
		return upstream("刷新主题", err)
	}
	ms := s.cfg.Now().UnixMilli()
	return s.cfg.Q.SetOwnerMemoryTopicRefreshed(ctx, db.SetOwnerMemoryTopicRefreshedParams{LastRefreshAt: &ms, ModelID: id})
}

// RefreshTopic 立即刷新一个主题（不受间隔限制）。
func (s *Service) RefreshTopic(ctx context.Context, id string) error {
	if _, err := s.cfg.Q.GetOwnerMemoryTopic(ctx, id); errors.Is(err, errNoRows) {
		return errNotFound("主题不存在")
	} else if err != nil {
		return err
	}
	b, err := s.bank(ctx)
	if err != nil {
		return err
	}
	return s.refresh(ctx, b, id)
}

// topicContents 读 bank 里主题的当前内容（id -> content）。
func (s *Service) topicContents(ctx context.Context, b *memory.Bank) (map[string]string, error) {
	var resp struct {
		Items []struct {
			ID      string  `json:"id"`
			Content *string `json:"content"`
		} `json:"items"`
	}
	if err := b.Call(ctx, http.MethodGet, "/mental-models?detail=content&limit=100", nil, &resp); err != nil {
		return nil, upstream("读取主题", err)
	}
	out := make(map[string]string, len(resp.Items))
	for _, it := range resp.Items {
		if it.Content != nil {
			out[it.ID] = *it.Content
		}
	}
	return out, nil
}

// Topics 列出主题与最近一次主题整理。连得上 Hindsight 时带当前内容与遗忘后等待刷新的标记。
func (s *Service) Topics(ctx context.Context) (apigen.OwnerMemoryTopicList, error) {
	rows, err := s.cfg.Q.ListOwnerMemoryTopics(ctx)
	if err != nil {
		return apigen.OwnerMemoryTopicList{}, err
	}
	out := apigen.OwnerMemoryTopicList{Items: make([]apigen.OwnerMemoryTopic, 0, len(rows))}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return out, err
	}
	if st.LastCuration != nil {
		var cur apigen.OwnerMemoryCuration
		if json.Unmarshal([]byte(*st.LastCuration), &cur) == nil {
			out.LastCuration = &cur
		}
	}
	var contents map[string]string
	var stale map[string]bool
	if b, err := s.bank(ctx); err == nil {
		contents, _ = s.topicContents(ctx, b)
		stale, _ = b.StaleModelIDs(ctx)
	}
	for _, r := range rows {
		t := apigen.OwnerMemoryTopic{
			ID: r.ModelID, Name: r.Name, Scope: r.Scope, Seed: r.Seed != 0,
			Status: apigen.OwnerMemoryTopicStatus(r.Status), LastRefreshedAt: r.LastRefreshAt, Stale: stale[r.ModelID],
		}
		if c, ok := contents[r.ModelID]; ok {
			t.Content = &c
		}
		out.Items = append(out.Items, t)
	}
	return out, nil
}

// daily 是每天一次的主题整理与刷新检查：首次整理等回填完成（否则 bank 还是空的）。
func (s *Service) daily(ctx context.Context) error {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	if st.LastCuratedAt != nil && s.cfg.Now().Sub(time.UnixMilli(*st.LastCuratedAt)) < curateInterval {
		return nil
	}
	if st.LastCuratedAt == nil && st.BackfillSince != nil {
		total, err := s.cfg.Q.CountOwnerMemoryBackfillThreads(ctx, st.BackfillSince)
		if err != nil {
			return err
		}
		done, err := s.cfg.Q.CountOwnerMemoryBackfillDone(ctx, st.BackfillSince)
		if err != nil {
			return err
		}
		if done < total {
			return nil
		}
	}
	if _, err := s.Curate(ctx); err != nil {
		return err
	}
	_, err = s.RefreshDue(ctx)
	return err
}
