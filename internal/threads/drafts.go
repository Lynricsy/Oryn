package threads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 写作块改稿：用户在转录里就地修改 agent 起草的写作块（draft-email / draft-message / draft-doc 围栏），
// 改稿按 (线程, 助手消息条目, 块键) 存最新全文；下一条用户消息发出时把尚未交给 agent 的改稿以
// <oryn-draft-edits> 块追加到消息正文之后（附件块之前），agent 据此修订。

// draftEditsHead 是改稿块的固定开头（web 的 DRAFT_EDITS_HEAD 与之逐字相同）。
const draftEditsHead = "<oryn-draft-edits>\nThe user edited these drafts from your earlier replies in place. " +
	"The text below is the current version of each; build on it rather than on your original wording.\n"

const (
	// maxDraftTitle 是改稿标题的字符数上限。
	maxDraftTitle = 200
	// maxDraftText 是改稿正文的字节数上限（100 KiB）。
	maxDraftText = 100 << 10
)

// draftBlockRE 校验块键 `<contentIndex>.<ordinal>`。
var draftBlockRE = regexp.MustCompile(`^\d{1,4}\.\d{1,4}$`)

// draftKinds 是合法的写作块类型。
var draftKinds = map[string]bool{"email": true, "message": true, "doc": true}

// Draft 是一份写作块改稿。
type Draft struct {
	EntryID string
	Block   string
	Kind    string
	Title   string
	Text    string
	// UpdatedAt 为最后修改的毫秒时间戳，单调递增。
	UpdatedAt int64
	// DeliveredAt 为已随用户消息交给 agent 的那一版的 UpdatedAt；从未送达为 nil。
	DeliveredAt *int64
}

// DraftInput 是写入改稿的请求内容。
type DraftInput struct {
	Kind  string
	Title string
	Text  string
}

// draftMark 记录一次送达标记，消息未能送出时据此回滚（prev 为标记前的 delivered_at）。
type draftMark struct {
	entryID, block string
	updatedAt      int64
	prev           *int64
}

func draftFromRow(r db.ThreadDraft) Draft {
	return Draft{EntryID: r.EntryID, Block: r.Block, Kind: r.Kind, Title: r.Title, Text: r.Text, UpdatedAt: r.UpdatedAt, DeliveredAt: r.DeliveredAt}
}

// ListDrafts 返回线程的全部改稿，按修改时间升序。
func (s *Service) ListDrafts(ctx context.Context, threadID string) ([]Draft, error) {
	if _, err := s.get(ctx, threadID); err != nil {
		return nil, err
	}
	rows, err := s.st.Q.ListThreadDrafts(ctx, threadID)
	if err != nil {
		return nil, err
	}
	out := make([]Draft, len(rows))
	for i, r := range rows {
		out[i] = draftFromRow(r)
	}
	return out, nil
}

// PutDraft 写入一份改稿的最新全文（delivered_at 不变）。updated_at 取当前时间，不大于旧值时取旧值 + 1。
func (s *Service) PutDraft(ctx context.Context, threadID, entryID, block string, in DraftInput) (Draft, error) {
	if _, err := s.get(ctx, threadID); err != nil {
		return Draft{}, err
	}
	switch {
	case !draftBlockRE.MatchString(block):
		return Draft{}, errf("invalid_draft", "block must look like <contentIndex>.<ordinal>")
	case !draftKinds[in.Kind]:
		return Draft{}, errf("invalid_draft", "kind must be email, message or doc")
	case utf8.RuneCountInString(in.Title) > maxDraftTitle:
		return Draft{}, errf("invalid_draft", fmt.Sprintf("title is longer than %d characters", maxDraftTitle))
	case len(in.Text) > maxDraftText:
		return Draft{}, errf("invalid_draft", fmt.Sprintf("text is larger than %d bytes", maxDraftText))
	}
	var out db.ThreadDraft
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		ok, err := q.ThreadEntryExists(ctx, db.ThreadEntryExistsParams{ThreadID: threadID, EntryID: entryID})
		if err != nil {
			return err
		}
		if !ok {
			return errf("entry_not_found", "entry not found in this thread")
		}
		now := s.now()
		old, err := q.GetThreadDraft(ctx, db.GetThreadDraftParams{ThreadID: threadID, EntryID: entryID, Block: block})
		switch {
		case err == nil && now <= old.UpdatedAt:
			now = old.UpdatedAt + 1
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return err
		}
		out, err = q.UpsertThreadDraft(ctx, db.UpsertThreadDraftParams{
			ThreadID: threadID, EntryID: entryID, Block: block, Kind: in.Kind, Title: in.Title, Text: in.Text, UpdatedAt: now,
		})
		return err
	})
	if err != nil {
		return Draft{}, err
	}
	return draftFromRow(out), nil
}

// DeleteDraft 删除一份改稿（恢复原稿且改稿从未送达时由前端调用）。
func (s *Service) DeleteDraft(ctx context.Context, threadID, entryID, block string) error {
	if _, err := s.get(ctx, threadID); err != nil {
		return err
	}
	n, err := s.st.Q.DeleteThreadDraft(ctx, db.DeleteThreadDraftParams{ThreadID: threadID, EntryID: entryID, Block: block})
	if err != nil {
		return err
	}
	if n == 0 {
		return errf("draft_not_found", "draft not found")
	}
	return nil
}

// takePendingDrafts 在一个事务里取出线程尚未交给 agent 的改稿并标记送达，返回要追加到消息正文后的改稿块
// 与送达标记（消息未能送出时交给 restoreDrafts 回滚）。没有待送达的改稿时返回空串。
func (s *Service) takePendingDrafts(ctx context.Context, threadID string) (string, []draftMark, error) {
	var rows []db.ThreadDraft
	var marks []draftMark
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		var err error
		if rows, err = q.ListPendingThreadDrafts(ctx, threadID); err != nil {
			return err
		}
		marks = make([]draftMark, 0, len(rows))
		for _, r := range rows {
			at := r.UpdatedAt
			if err := q.MarkThreadDraftDelivered(ctx, db.MarkThreadDraftDeliveredParams{
				UpdatedAt: &at, ThreadID: threadID, EntryID: r.EntryID, Block: r.Block,
			}); err != nil {
				return err
			}
			marks = append(marks, draftMark{entryID: r.EntryID, block: r.Block, updatedAt: r.UpdatedAt, prev: r.DeliveredAt})
		}
		return nil
	})
	if err != nil || len(rows) == 0 {
		return "", nil, err
	}
	drafts := make([]Draft, len(rows))
	for i, r := range rows {
		drafts[i] = draftFromRow(r)
	}
	return draftEditsBlock(drafts), marks, nil
}

// restoreDrafts 撤销 takePendingDrafts 的送达标记：只有 delivered_at 仍等于本次标记的值时才回到原值，
// 期间用户又改过（或已随另一条消息送达）的改稿保持现状。失败只记日志。
func (s *Service) restoreDrafts(ctx context.Context, threadID string, marks []draftMark) {
	if len(marks) == 0 {
		return
	}
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		for _, m := range marks {
			at := m.updatedAt
			if err := q.RestoreThreadDraftDelivered(ctx, db.RestoreThreadDraftDeliveredParams{
				Prev: m.prev, ThreadID: threadID, EntryID: m.entryID, Block: m.block, UpdatedAt: &at,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		slog.Warn("threads: 回滚改稿送达标记失败", "thread", threadID, "err", err)
	}
}

// draftAttrEscaper 转义改稿块属性值。
var draftAttrEscaper = strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;")

// draftBodyEscaper 转义改稿正文里会提前闭合标签的序列。
var draftBodyEscaper = strings.NewReplacer("</draft", `<\/draft`, "</oryn-draft-edits", `<\/oryn-draft-edits`)

// draftEditsBlock 构造追加到用户消息正文之后的改稿块（以 "\n\n" 开头；与 web 的 buildDraftEditsBlock 输出一致）。
func draftEditsBlock(drafts []Draft) string {
	var b strings.Builder
	b.WriteString("\n\n")
	b.WriteString(draftEditsHead)
	for _, d := range drafts {
		fmt.Fprintf(&b, "<draft kind=\"%s\" title=\"%s\">\n", d.Kind, draftAttrEscaper.Replace(d.Title))
		b.WriteString(draftBodyEscaper.Replace(d.Text))
		b.WriteString("\n</draft>\n")
	}
	b.WriteString("</oryn-draft-edits>")
	return b.String()
}
