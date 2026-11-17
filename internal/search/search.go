// Package search 是对话内容的全文搜索：主会话 user / assistant 消息正文的 FTS5 trigram 索引（0027_search.sql）
// 由 entries 上的触发器随条目入库、改写、作废与删除同步维护；本包负责后台回填历史条目与查询。
package search

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 查询的上限。
const (
	DefaultLimit = 20
	MaxLimit     = 50
	// MaxQueryRunes 是 q 的字符数上限，MaxTerms 是词数上限。
	MaxQueryRunes = 200
	MaxTerms      = 8
	// trigramMin 是 trigram 分词器能查的最短词长；更短的词改为子串过滤。
	trigramMin = 3
)

// ErrInvalid 是请求参数不合法（空查询、超长、词太多、游标损坏）；API 回 400 `invalid_request`。
var ErrInvalid = errors.New("invalid search request")

func invalid(msg string) error { return &invalidError{msg} }

type invalidError struct{ msg string }

func (e *invalidError) Error() string { return e.msg }
func (e *invalidError) Unwrap() error { return ErrInvalid }

// Service 是搜索服务。
type Service struct {
	st *store.Store
}

// New 返回搜索服务。
func New(st *store.Store) *Service { return &Service{st: st} }

// query 是解析后的查询：long 拼成 FTS5 表达式，short 走子串过滤，terms 是全部词（已折叠，用于高亮）。
type query struct {
	long  []string
	short []string
	terms [][]rune
}

// parseQuery 按空白切词、去重；每个词作为 FTS5 的带引号短语（内部的双引号加倍转义），不解析任何 FTS5 语法。
func parseQuery(q string) (query, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return query{}, invalid("q 不能为空")
	}
	if utf8.RuneCountInString(q) > MaxQueryRunes {
		return query{}, invalid("q 不能超过 200 个字符")
	}
	fields := strings.Fields(q)
	seen := make(map[string]bool, len(fields))
	var out query
	for _, f := range fields {
		folded := foldRunes([]rune(f))
		key := string(folded)
		if seen[key] {
			continue
		}
		seen[key] = true
		out.terms = append(out.terms, folded)
		if utf8.RuneCountInString(f) >= trigramMin {
			out.long = append(out.long, `"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
		} else {
			out.short = append(out.short, f)
		}
	}
	if len(out.terms) > MaxTerms {
		return query{}, invalid("q 至多 8 个词")
	}
	return out, nil
}

// cursor 是翻页游标 "<ts>.<id>"：下一页取 (ts, id) 严格小于它的行。
type cursor struct{ ts, id int64 }

func (c cursor) String() string {
	return strconv.FormatInt(c.ts, 10) + "." + strconv.FormatInt(c.id, 10)
}

func parseCursor(s string) (cursor, error) {
	a, b, ok := strings.Cut(s, ".")
	ts, err1 := strconv.ParseInt(a, 10, 64)
	id, err2 := strconv.ParseInt(b, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return cursor{}, invalid("cursor 无法解析")
	}
	return cursor{ts, id}, nil
}

// Params 是一次搜索的参数；Limit ≤ 0 取 DefaultLimit，超过 MaxLimit 截到 MaxLimit。
// Kinds 非空时只搜这些种类的线程；ProjectID 非空时只搜该项目；Before > 0 时只要这个时间（Unix 毫秒）之前的消息
// （有 Cursor 时以 Cursor 为准）。
type Params struct {
	Q         string
	Limit     int
	Cursor    string
	Kinds     []string
	ProjectID string
	Before    int64
}

// row 是两条查询共同的结果列。
type row struct {
	ID         int64
	ThreadID   string
	Seq        int64
	EntryID    string
	Ts         int64
	Role       string
	Body       string
	Title      string
	Kind       string
	ArchivedAt *int64
	Copies     int64
}

// Search 执行一次搜索，按消息时间从新到旧返回一页。
func (s *Service) Search(ctx context.Context, p Params) (apigen.SearchResults, error) {
	qq, err := parseQuery(p.Q)
	if err != nil {
		return apigen.SearchResults{}, err
	}
	limit := p.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	// 缺省游标取最大值：首页即全部行
	cur := cursor{ts: 1<<63 - 1, id: 1<<63 - 1}
	if p.Before > 0 {
		cur.ts = p.Before - 1 // 严格早于 Before：ts = Before-1 的行在 id 上全部放行
	}
	if p.Cursor != "" {
		if cur, err = parseCursor(p.Cursor); err != nil {
			return apigen.SearchResults{}, err
		}
	}
	shortJSON, err := json.Marshal(append([]string{}, qq.short...))
	if err != nil {
		return apigen.SearchResults{}, err
	}
	kindsJSON, err := json.Marshal(append([]string{}, p.Kinds...))
	if err != nil {
		return apigen.SearchResults{}, err
	}
	var rows []row
	if len(qq.long) > 0 {
		rs, err := s.st.Q.SearchMessages(ctx, db.SearchMessagesParams{
			Match: strings.Join(qq.long, " AND "), Short: string(shortJSON),
			BeforeTs: cur.ts, BeforeID: cur.id, Kinds: string(kindsJSON), Project: p.ProjectID, Limit: int64(limit),
		})
		if err != nil {
			return apigen.SearchResults{}, err
		}
		for _, r := range rs {
			rows = append(rows, row(r))
		}
	} else {
		rs, err := s.st.Q.SearchMessagesShort(ctx, db.SearchMessagesShortParams{
			Short: string(shortJSON), BeforeTs: cur.ts, BeforeID: cur.id, Kinds: string(kindsJSON), Project: p.ProjectID,
			Limit: int64(limit),
		})
		if err != nil {
			return apigen.SearchResults{}, err
		}
		for _, r := range rs {
			rows = append(rows, row(r))
		}
	}
	res := apigen.SearchResults{Hits: make([]apigen.SearchHit, 0, len(rows))}
	for _, r := range rows {
		snippet, hl := buildSnippet(r.Body, qq.terms)
		res.Hits = append(res.Hits, apigen.SearchHit{
			ThreadID: r.ThreadID, ThreadTitle: r.Title, ThreadKind: apigen.ThreadKind(r.Kind), Archived: r.ArchivedAt != nil,
			EntryID: r.EntryID, Seq: r.Seq, Role: apigen.SearchHitRole(r.Role), TS: r.Ts,
			Snippet: snippet, Highlights: hl, Copies: int(max(r.Copies, 1)),
		})
	}
	if len(rows) == limit {
		last := rows[len(rows)-1]
		next := cursor{last.Ts, last.ID}.String()
		res.NextCursor = &next
	}
	if res.Indexing, err = s.Indexing(ctx); err != nil {
		return apigen.SearchResults{}, err
	}
	return res, nil
}
