package transcript

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func taskMsg(content string) Entry {
	return Entry{Type: "message", Payload: []byte(`{"type":"message","message":{"role":"assistant","content":[` + content + `]}}`)}
}

func TestUsedAgents(t *testing.T) {
	branch := []Entry{
		taskMsg(`{"type":"text","text":"hi"},{"type":"toolCall","name":"task","arguments":{"tasks":[{"agent":"scout","task":"a"},{"task":"b"}]}}`),
		{Type: "custom", Payload: []byte(`{"customType":"x"}`)},
		taskMsg(`{"type":"toolCall","name":"task","arguments":{"agent":"reviewer","task":"c"}}`),
		taskMsg(`{"type":"toolCall","name":"bash","arguments":{"agent":"nope"}}`),
		taskMsg(`{"type":"toolCall","name":"task","arguments":{"agent":"scout","tasks":[{"task":"d"},{"agent":"Custom-1","task":"e"}]}}`),
		{Type: "message", Payload: []byte(`not json`)},
		taskMsg(`{"type":"toolCall","name":"task","arguments":"bad"}`),
	}
	assert.Equal(t, []string{"scout", "task", "reviewer", "Custom-1"}, UsedAgents(branch))
	assert.Nil(t, UsedAgents(nil))
}
