package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 会话文件格式取自 oryn-agent `packages/coding-agent/src/session/`：
//   - 第 1 行：定宽标题槽（session-title-slot.ts serializeTitleSlot），恰好 256 字节含换行，
//     `{"type":"title","v":1,"title":"","updatedAt":"<iso>","pad":"<空格>"}`；
//   - 第 2 行：会话头（session-entries.ts SessionHeader），`{"type":"session","version":3,"id":<uuidv7>,"timestamp":<iso>,"cwd":…}`；
//   - 其后每行一个条目：`{"type":"message","id":<8 位十六进制>,"parentId":<上一条 id|null>,"timestamp":<iso>,"message":{…}}`。
//
// 文件名 `<ISO 时间戳，: 与 . 换成 ->_<会话 id>.jsonl`。
const (
	titleSlotBytes = 256
	sessionVersion = 3
)

type sessionFile struct {
	path string
	id   string
	leaf string // 最后一条条目的 id；空表示尚无条目
	ids  map[string]bool
}

func isoTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// titleSlotLine 生成恰好 256 字节（含换行）的标题槽行。
func titleSlotLine(updatedAt string) ([]byte, error) {
	build := func(pad string) []byte {
		b, _ := json.Marshal(struct {
			Type      string `json:"type"`
			V         int    `json:"v"`
			Title     string `json:"title"`
			UpdatedAt string `json:"updatedAt"`
			Pad       string `json:"pad"`
		}{"title", 1, "", updatedAt, pad})
		return append(b, '\n')
	}
	padBytes := titleSlotBytes - len(build(""))
	if padBytes < 0 {
		return nil, fmt.Errorf("title slot overflow")
	}
	return build(strings.Repeat(" ", padBytes)), nil
}

// openSessionDir 打开 dir 下最新的主会话文件；没有则新建。
func openSessionDir(dir, cwd string, now time.Time) (*sessionFile, bool, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, false, err
	}
	if len(matches) > 0 {
		sort.Strings(matches) // 文件名以时间戳开头，字典序即时间序
		s, err := loadSession(matches[len(matches)-1])
		return s, true, err
	}
	s, err := createSession(dir, cwd, now)
	return s, false, err
}

func createSession(dir, cwd string, now time.Time) (*sessionFile, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	ts := isoTime(now)
	name := strings.NewReplacer(":", "-", ".", "-").Replace(ts) + "_" + id.String() + ".jsonl"
	path := filepath.Join(dir, name)
	slot, err := titleSlotLine(ts)
	if err != nil {
		return nil, err
	}
	header, err := json.Marshal(struct {
		Type      string `json:"type"`
		Version   int    `json:"version"`
		ID        string `json:"id"`
		Timestamp string `json:"timestamp"`
		Cwd       string `json:"cwd"`
	}{"session", sessionVersion, id.String(), ts, cwd})
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Write(slot)
	buf.Write(header)
	buf.WriteByte('\n')
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return nil, err
	}
	return &sessionFile{path: path, id: id.String(), ids: map[string]bool{}}, nil
}

func loadSession(path string) (*sessionFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := &sessionFile{path: path, ids: map[string]bool{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 256<<20)
	for sc.Scan() {
		var e struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
		case "title":
		case "session":
			s.id = e.ID
		default:
			if e.ID != "" {
				s.ids[e.ID] = true
				s.leaf = e.ID
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if s.id == "" {
		return nil, fmt.Errorf("%s: missing session header", path)
	}
	return s, nil
}

func (s *sessionFile) newEntryID() string {
	for {
		var b [4]byte
		_, _ = rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		if !s.ids[id] {
			s.ids[id] = true
			return id
		}
	}
}

// appendMessage 追加一条 message 条目并推进叶子，返回条目 id。
func (s *sessionFile) appendMessage(message map[string]any, now time.Time) (string, error) {
	id := s.newEntryID()
	var parent any
	if s.leaf != "" {
		parent = s.leaf
	}
	line, err := json.Marshal(struct {
		Type      string         `json:"type"`
		ID        string         `json:"id"`
		ParentID  any            `json:"parentId"`
		Timestamp string         `json:"timestamp"`
		Message   map[string]any `json:"message"`
	}{"message", id, parent, isoTime(now), message})
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return "", err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	s.leaf = id
	return id, nil
}
