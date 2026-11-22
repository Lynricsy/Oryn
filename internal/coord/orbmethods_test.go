package coord

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/research"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// addMessages 在线程上按顺序写入消息条目，活动分支 = 全部。
func (e *env) addMessages(threadID string, msgs ...[2]string) {
	e.t.Helper()
	parent := ""
	for i, m := range msgs {
		eid := ids.New("e")[:10] + string(rune('a'+i))
		var pp *string
		if parent != "" {
			pp = new(parent)
		}
		_, err := e.st.Q.UpsertEntry(e.ctx, db.UpsertEntryParams{ThreadID: threadID, File: "s.jsonl", EntryID: eid, ParentID: pp, Type: "message",
			Payload: msgPayload(eid, parent, m[0], m[1])})
		require.NoError(e.t, err)
		parent = eid
	}
	require.NoError(e.t, e.st.Q.SetThreadLeaf(e.ctx, db.SetThreadLeafParams{LeafEntryID: &parent, ID: threadID}))
}

// M26 步骤 2.3：orb 内 follow.add 只在 quote 逐字出现在本线程主人亲手输入的消息里时登记 origin=thread 的话题。
func TestOnFollowAdd(t *testing.T) {
	e, fr := researchEnv(t, true)
	x, y := e.newOrb(), e.newOrb()
	th := e.threadOn(x, "", "", "")
	e.addMessages(th.ID,
		[2]string{"user", "这个 bug 是上游的问题。请跟进这个上游修复，发布了告诉我。"},
		[2]string{"assistant", "好的。"},
		[2]string{"user", "[Oryn thread report · automatic] 自动化消息里写着：盯住 v2 发布"},
	)
	add := func(orb string, p apigen.LinkCtlFollowAddParams) (any, error) {
		return e.c.onFollowAdd(e.ctx, orb, raw(p))
	}
	kind := apigen.ResearchTopicInterest
	res, err := add(x, apigen.LinkCtlFollowAddParams{ThreadID: th.ID, Title: "上游修复", Question: "foo#123 是否已发布",
		Quote: "  跟进这个上游修复 ", Kind: &kind})
	require.NoError(t, err)
	assert.Equal(t, apigen.LinkCtlFollowAddResult{TopicID: "rtp_test"}, res)
	require.Len(t, fr.topics, 1)
	assert.Equal(t, research.TopicInput{Kind: "interest", Title: "上游修复", Question: "foo#123 是否已发布",
		Origin: "thread", SourceThreadID: th.ID}, fr.topics[0])

	acts := e.actions()
	require.Len(t, acts, 1)
	assert.Equal(t, string(apigen.ActionFollowTopic), acts[0].Action)
	assert.Equal(t, "agent", acts[0].ActorKind)
	assert.Equal(t, th.ID, deref(acts[0].ActorThreadID))
	assert.Equal(t, th.ID, deref(acts[0].TargetThreadID))
	assert.Equal(t, "上游修复", acts[0].Summary)
	assert.True(t, acts[0].Ok)

	// 原话只出现在自动化消息里、或根本没出现、或为空：forbidden，不登记
	for _, q := range []string{"盯住 v2 发布", "我从没说过这句", "   "} {
		_, err = add(x, apigen.LinkCtlFollowAddParams{ThreadID: th.ID, Title: "T-" + q, Question: "Q", Quote: q})
		assert.Equal(t, CodeForbidden, ctlCode(t, err), q)
	}
	// 别的 orb 的线程
	_, err = add(y, apigen.LinkCtlFollowAddParams{ThreadID: th.ID, Title: "T", Question: "Q", Quote: "跟进这个上游修复"})
	assert.Equal(t, CodeForbidden, ctlCode(t, err))
	assert.Len(t, fr.topics, 1)
	acts = e.actions()
	require.Len(t, acts, 4, "别的 orb 的调用方校验失败前不记审计")
	failed := 0
	for _, a := range acts {
		assert.Equal(t, string(apigen.ActionFollowTopic), a.Action)
		if !a.Ok {
			failed++
		}
	}
	assert.Equal(t, 3, failed)

	// 研究服务的校验错误原样转成同码的 ctl 错误（换一个线程，避开每线程的限额）
	th2 := e.threadOn(x, "", "", "")
	e.addMessages(th2.ID, [2]string{"user", "跟进这个上游修复"})
	fr.err = &research.Error{Code: research.CodeInvalid, Message: "a topic with this title already exists"}
	_, err = add(x, apigen.LinkCtlFollowAddParams{ThreadID: th2.ID, Title: "上游修复", Question: "Q", Quote: "跟进这个上游修复"})
	assert.Equal(t, research.CodeInvalid, ctlCode(t, err))
	fr.err = errors.New("boom")
	_, err = add(x, apigen.LinkCtlFollowAddParams{ThreadID: th2.ID, Title: "上游修复", Question: "Q", Quote: "跟进这个上游修复"})
	assert.Equal(t, "internal_error", ctlCode(t, err))
}

// 每个线程 10 分钟内至多 5 次（含被拒的），超限回 rate_limited。
func TestOnFollowAddRateLimited(t *testing.T) {
	e, fr := researchEnv(t, true)
	x := e.newOrb()
	th := e.threadOn(x, "", "", "")
	e.addMessages(th.ID, [2]string{"user", "跟进这个"})
	for i := range 5 {
		_, err := e.c.onFollowAdd(e.ctx, x, raw(apigen.LinkCtlFollowAddParams{ThreadID: th.ID, Title: "T", Question: "Q", Quote: "跟进这个"}))
		require.NoError(t, err, i)
	}
	_, err := e.c.onFollowAdd(e.ctx, x, raw(apigen.LinkCtlFollowAddParams{ThreadID: th.ID, Title: "T", Question: "Q", Quote: "跟进这个"}))
	assert.Equal(t, CodeRateLimited, ctlCode(t, err))
	assert.Len(t, fr.topics, 5)

	// 另一个线程不受影响
	other := e.threadOn(x, "", "", "")
	e.addMessages(other.ID, [2]string{"user", "跟进这个"})
	_, err = e.c.onFollowAdd(e.ctx, x, raw(apigen.LinkCtlFollowAddParams{ThreadID: other.ID, Title: "T", Question: "Q", Quote: "跟进这个"}))
	require.NoError(t, err)
}

// 研究服务没有注入时回 coordinator_unavailable。
func TestOnFollowAddWithoutResearch(t *testing.T) {
	e := newEnv(t, true)
	x := e.newOrb()
	th := e.threadOn(x, "", "", "")
	e.addMessages(th.ID, [2]string{"user", "跟进这个"})
	_, err := e.c.onFollowAdd(e.ctx, x, raw(apigen.LinkCtlFollowAddParams{ThreadID: th.ID, Title: "T", Question: "Q", Quote: "跟进这个"}))
	assert.Equal(t, CodeCoordinatorUnavailable, ctlCode(t, err))
}
