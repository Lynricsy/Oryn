package notify

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func TestPostForThread(t *testing.T) {
	f := newFixture(t)
	h := newHook(t)
	f.webhookChannel(h.srv.URL+"/in", "", apigen.NotificationKindError)
	id := f.thread("每日巡检")

	require.NoError(t, f.s.Post(f.ctx, id, "ignored", `Scheduled run "daily" could not be delivered: queue_full`))
	ns := f.notifications()
	require.Len(t, ns, 1)
	n := ns[0]
	assert.Equal(t, KindError, n.Kind)
	assert.Equal(t, "每日巡检", n.Title, "有线程时标题取线程标题")
	assert.Equal(t, id, *n.ThreadID)
	assert.Equal(t, `Scheduled run "daily" could not be delivered: queue_full`, n.Body)
	assert.True(t, f.s.unread[id], "登记未读")
	require.Len(t, f.out.Notes(), 1)
	assert.Equal(t, "/t/"+id, f.out.Notes()[0].URL)
	require.Len(t, f.deliveries(n.ID), 1)

	assert.Equal(t, 1, f.s.DeliverDue(f.ctx))
	var body struct {
		Event  string
		Thread struct{ ID, URL string }
	}
	require.NoError(t, json.Unmarshal(h.requests()[0].Body, &body))
	assert.Equal(t, "thread.error", body.Event)
	assert.Equal(t, "https://oryn.test/t/"+id, body.Thread.URL)

	// 已归档或不存在的线程不发
	_, err := f.st.DB.ExecContext(f.ctx, `UPDATE threads SET archived_at = 1 WHERE id = ?`, id)
	require.NoError(t, err)
	require.NoError(t, f.s.Post(f.ctx, id, "", "x"))
	require.NoError(t, f.s.Post(f.ctx, "T-missing", "", "x"))
	assert.Len(t, f.notifications(), 1)
}

func TestPostWithoutThread(t *testing.T) {
	f := newFixture(t)
	h := newHook(t)
	f.webhookChannel(h.srv.URL+"/in", "")

	require.NoError(t, f.s.Post(f.ctx, "", `Schedule "bad"`, "Scheduled run could not start a thread: provider not found"))
	ns := f.notifications()
	require.Len(t, ns, 1)
	assert.Nil(t, ns[0].ThreadID)
	assert.Equal(t, `Schedule "bad"`, ns[0].Title)
	assert.Equal(t, KindError, ns[0].Kind)
	notes := f.out.Notes()
	require.Len(t, notes, 1)
	assert.Equal(t, AutomationsPath, notes[0].URL)
	assert.Nil(t, notes[0].ThreadID)
	assert.Empty(t, f.s.unread, "没有线程就没有线程未读")

	list, err := f.s.List(f.ctx, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, AutomationsPath, list.Notifications[0].URL)

	assert.Equal(t, 1, f.s.DeliverDue(f.ctx))
	var body map[string]any
	require.NoError(t, json.Unmarshal(h.requests()[0].Body, &body))
	assert.Equal(t, "thread.error", body["event"])
	assert.Equal(t, `Schedule "bad"`, body["title"])
	assert.Equal(t, "https://oryn.test"+AutomationsPath, body["url"])
	assert.NotContains(t, body, "thread")

	payload, err := pushPayload(Message{NotificationID: "ntf_1", Kind: KindError, Title: "t"})
	require.NoError(t, err)
	var push map[string]any
	require.NoError(t, json.Unmarshal(payload, &push))
	assert.Equal(t, AutomationsPath, push["url"])
	assert.Equal(t, "oryn-automations", push["tag"])
}
