package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestActiveBranch(t *testing.T) {
	p := func(s string) *string { return &s }
	msg := func(role, text string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"message": map[string]any{"role": role, "content": []any{map[string]any{"type": "text", "text": text}}}})
		return b
	}
	entries := []cliEntry{
		{Seq: 1, File: "a.jsonl", EntryID: "__title__", Type: "title"},
		{Seq: 2, File: "a.jsonl", EntryID: "u1", Type: "message", Payload: msg("user", "hi")},
		{Seq: 3, File: "a.jsonl", EntryID: "x1", ParentID: p("u1"), Type: "message", Payload: msg("assistant", "old branch")},
		{Seq: 4, File: "a.jsonl", EntryID: "x2", ParentID: p("u1"), Type: "message", Payload: msg("assistant", "new branch")},
		{Seq: 5, File: "a/sub.jsonl", EntryID: "s1", Type: "message", Payload: msg("assistant", "subagent")},
		{Seq: 6, File: "b.jsonl", EntryID: "u2", Type: "message", Payload: msg("user", "second session")},
	}
	var got []string
	for _, e := range activeBranch(entries, "x2") {
		role, text := entryText(e)
		got = append(got, role+":"+text)
	}
	assert.Equal(t, []string{"user:hi", "assistant:new branch", "user:second session"}, got)

	role, text := entryText(cliEntry{Type: "message", Payload: json.RawMessage(`{"message":{"role":"user","content":"plain"}}`)})
	assert.Equal(t, "user", role)
	assert.Equal(t, "plain", text)
}
