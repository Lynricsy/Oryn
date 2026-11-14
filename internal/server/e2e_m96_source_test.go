package server

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/config"
)

// M9.6 来源链路全链路验证：process 驱动 + 真实 orbd + fakeomp。

type sourceRec struct {
	seq  int64
	data map[string]any
}

// modelSourceRecords 经 REST 读线程条目，返回来源记录、第一条 user 消息的 seq 与 custom 条目总数。
func (s *stack) modelSourceRecords(threadID string) (recs []sourceRec, firstUser int64, customs int) {
	s.t.Helper()
	var page struct {
		Entries []struct {
			Seq     int64           `json:"seq"`
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		} `json:"entries"`
	}
	s.rest("GET", "/api/v1/threads/"+threadID+"/entries?limit=500", nil, &page, 200)
	for _, e := range page.Entries {
		var p struct {
			CustomType string         `json:"customType"`
			Data       map[string]any `json:"data"`
			Message    struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		require.NoError(s.t, json.Unmarshal(e.Payload, &p))
		switch {
		case e.Type == "custom":
			customs++
			if p.CustomType == "oryn-model-source" {
				recs = append(recs, sourceRec{seq: e.Seq, data: p.Data})
			}
		case e.Type == "message" && p.Message.Role == "user" && firstUser == 0:
			firstUser = e.Seq
		}
	}
	return recs, firstUser, customs
}

// newSourceThread 经 REST 建线程（model 为 nil 时不选模型），等到 idle 且第一个回合的条目入库。
func (s *stack) newSourceThread(model map[string]any) string {
	s.t.Helper()
	body := map[string]any{"prompt": "hello", "executor": map[string]any{"kind": "new_orb", "provider": "local", "type": "small"}}
	if model != nil {
		body["model"] = model
	}
	var created struct {
		Thread threadView `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", body, &created, 201)
	id := created.Thread.ID
	s.waitThread(id, 60*time.Second, func(v threadView) bool { return v.State == "idle" })
	require.Eventually(s.t, func() bool {
		var page struct {
			Entries []struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			} `json:"entries"`
		}
		s.rest("GET", "/api/v1/threads/"+id+"/entries?limit=500", nil, &page, 200)
		n := 0
		for _, e := range page.Entries {
			if e.Type == "message" {
				n++
			}
		}
		return n >= 4
	}, 20*time.Second, 50*time.Millisecond, "第一个回合入库")
	return id
}

func TestE2EModelSource(t *testing.T) {
	idle := config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)}

	t.Run("新 Core", func(t *testing.T) {
		s := newStack(t, idle)
		a := s.dial()

		// 不带模型：startup 记录，在第一条 user 消息之前
		th := s.newSourceThread(nil)
		recs, firstUser, _ := s.modelSourceRecords(th)
		require.Len(t, recs, 1)
		assert.Equal(t, map[string]any{"v": 1.0, "source": "startup", "role": "default", "model": "fake/fake-model"}, recs[0].data)
		require.NotZero(t, firstUser)
		assert.Less(t, recs[0].seq, firstUser)

		// 建线程时显式选了模型：manual，不是 startup
		th2 := s.newSourceThread(map[string]any{"provider": "openai", "id": "gpt-6.1-sol"})
		recs2, firstUser2, _ := s.modelSourceRecords(th2)
		require.Len(t, recs2, 1)
		assert.Equal(t, map[string]any{"v": 1.0, "source": "manual", "model": "openai/gpt-6.1-sol"}, recs2[0].data)
		assert.Less(t, recs2[0].seq, firstUser2)

		// WS cmd：点 role，再手动选同一模型
		a.send(map[string]any{"type": "sub", "threadId": th, "afterSeq": 0})
		for i, frame := range []map[string]any{
			{"type": "oryn_select_model", "role": "A"},
			{"type": "oryn_select_model", "provider": "fake", "modelId": "fake-model"},
		} {
			a.send(map[string]any{"type": "cmd", "threadId": th, "id": "s" + strconv.Itoa(i), "frame": frame})
			require.Eventually(t, func() bool {
				recs, _, _ := s.modelSourceRecords(th)
				return len(recs) == i+2
			}, 20*time.Second, 50*time.Millisecond, "第 %d 条选择记录入库", i+1)
		}
		recs, _, _ = s.modelSourceRecords(th)
		assert.Equal(t, map[string]any{"v": 1.0, "source": "role", "role": "A", "model": "fake/fake-model"}, recs[1].data)
		assert.Equal(t, map[string]any{"v": 1.0, "source": "manual", "model": "fake/fake-model"}, recs[2].data, "manual 记录没有 role")
	})

	t.Run("旧 Core", func(t *testing.T) {
		t.Setenv("FAKEOMP_LEGACY_CORE", "1")
		s := newStack(t, idle)
		th := s.newSourceThread(nil)
		recs, firstUser, customs := s.modelSourceRecords(th)
		assert.Empty(t, recs, "旧 Core 不写来源记录")
		assert.Zero(t, customs)
		assert.NotZero(t, firstUser, "线程照常就绪并执行了首条提示词")
		v := s.waitThread(th, time.Second, func(threadView) bool { return true })
		assert.Equal(t, "idle", v.State)
		assert.Nil(t, v.Error)
	})
}
