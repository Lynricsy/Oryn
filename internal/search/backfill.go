package search

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// IndexVersion 是索引口径（search_source 的取文规则）的版本；提高它会让下次启动清空索引、整体重建。
const IndexVersion = 1

// search_state 的键。
const (
	stateVersion = "index_version"
	// stateCursor 是回填进度 "<thread_id>:<seq>"：该线程 seq 及之前、以及 id 更小的线程都已处理；空串 = 尚未开始。
	stateCursor = "backfill_cursor"
	// stateTarget 是这轮回填的目标 IndexVersion：与当前版本不同时游标作废、从头重建。
	stateTarget = "backfill_target"
)

// backfillPage 是每个事务处理的 seq 跨度；backfillThreads 是每次列出的线程数。
const (
	backfillPage    = 500
	backfillThreads = 200
)

// beforeBackfillPage 是测试钩子：回填每个事务之前调用（测试据此在指定位置取消 ctx）。
var beforeBackfillPage func(threadID string, fromSeq int64)

func getState(ctx context.Context, q *db.Queries, key string) (string, bool, error) {
	v, err := q.GetSearchState(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// Indexing 报告历史条目是否仍在回填（索引版本还不是 IndexVersion）。
func (s *Service) Indexing(ctx context.Context) (bool, error) {
	v, _, err := getState(ctx, s.st.Q, stateVersion)
	if err != nil {
		return false, err
	}
	return v != strconv.Itoa(IndexVersion), nil
}

// Backfill 在索引版本不是 IndexVersion 时把已有条目补进索引：按线程 id 升序、每线程按 seq 每 500 条一个事务，
// 用与触发器同一个 search_source 视图取文（已由触发器写过的行跳过），每个事务同时推进游标；全部完成后写版本号并删除进度键。
// 可被 ctx 中断，下次从游标续作——仅当 backfill_target 等于当前版本；否则那是旧口径的进度，作废后从头重建。
// 库里已有旧版本的索引（version 非空且不同）时，先在一个事务里清空索引。
//
// 回填期间新入库的条目照常由触发器写入索引，回填与触发器写同一行时以 INSERT OR IGNORE / NOT EXISTS 去重。
func Backfill(ctx context.Context, st *store.Store) error {
	ver, _, err := getState(ctx, st.Q, stateVersion)
	if err != nil {
		return err
	}
	want := strconv.Itoa(IndexVersion)
	if ver == want {
		return nil
	}
	curRaw, resuming, err := getState(ctx, st.Q, stateCursor)
	if err != nil {
		return err
	}
	if resuming {
		target, _, err := getState(ctx, st.Q, stateTarget)
		if err != nil {
			return err
		}
		if target != want {
			slog.Info("search: 回填进度属于其它索引版本，从头重建", "target", target, "version", IndexVersion)
			resuming = false
		}
	}
	fromThread, fromSeq := "", int64(0)
	if resuming {
		if i := strings.LastIndexByte(curRaw, ':'); i >= 0 {
			if n, err := strconv.ParseInt(curRaw[i+1:], 10, 64); err == nil {
				fromThread, fromSeq = curRaw[:i], n
			}
		}
	} else {
		err := st.Tx(ctx, func(q *db.Queries) error {
			if ver != "" {
				if err := q.PurgeSearchFTS(ctx); err != nil {
					return err
				}
				if err := q.PurgeSearchDocs(ctx); err != nil {
					return err
				}
			}
			if err := q.SetSearchState(ctx, db.SetSearchStateParams{Key: stateTarget, Value: want}); err != nil {
				return err
			}
			return q.SetSearchState(ctx, db.SetSearchStateParams{Key: stateCursor, Value: ""})
		})
		if err != nil {
			return err
		}
	}
	slog.Info("search: 开始回填对话内容索引", "resume", fromThread != "", "version", IndexVersion)
	for {
		threads, err := st.Q.ListSearchBackfillThreads(ctx, db.ListSearchBackfillThreadsParams{FromID: fromThread, Limit: backfillThreads})
		if err != nil {
			return err
		}
		if len(threads) == 0 {
			break
		}
		for _, t := range threads {
			lo := int64(0)
			if t.ID == fromThread {
				lo = fromSeq
			}
			for ; lo < t.LastSeq; lo += backfillPage {
				if beforeBackfillPage != nil {
					beforeBackfillPage(t.ID, lo)
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				hi := min(lo+backfillPage, t.LastSeq)
				err := st.Tx(ctx, func(q *db.Queries) error {
					if err := q.BackfillSearchDocs(ctx, db.BackfillSearchDocsParams{ThreadID: t.ID, FromSeq: lo, ToSeq: hi}); err != nil {
						return err
					}
					if err := q.BackfillSearchFTS(ctx, db.BackfillSearchFTSParams{ThreadID: t.ID, FromSeq: lo, ToSeq: hi}); err != nil {
						return err
					}
					return q.SetSearchState(ctx, db.SetSearchStateParams{Key: stateCursor, Value: t.ID + ":" + strconv.FormatInt(hi, 10)})
				})
				if err != nil {
					return err
				}
			}
		}
		last := threads[len(threads)-1]
		if len(threads) < backfillThreads {
			break
		}
		// 下一批从最后一个线程之后开始（它已处理完）："\x00" 后缀让 id >= 条件严格越过它
		fromThread, fromSeq = last.ID+"\x00", 0
	}
	err = st.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetSearchState(ctx, db.SetSearchStateParams{Key: stateVersion, Value: want}); err != nil {
			return err
		}
		if err := q.DeleteSearchState(ctx, stateCursor); err != nil {
			return err
		}
		return q.DeleteSearchState(ctx, stateTarget)
	})
	if err != nil {
		return err
	}
	slog.Info("search: 对话内容索引回填完成", "version", IndexVersion)
	return nil
}
