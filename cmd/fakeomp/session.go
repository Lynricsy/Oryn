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

// 会话文件格式取自 oryn-core `packages/coding-agent/src/session/`：
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
	// hasMessage / hasSource：文件里已有 message 条目 / Oryn 模型来源记录（oryn_mark_session_start 据此判断会话是否全新）。
	hasMessage bool
	hasSource  bool
}

// modelSourceType 是 Oryn 模型来源记录的 customType（oryn-core `modes/rpc/oryn-model-source.ts` ORYN_MODEL_SOURCE_ENTRY）。
const modelSourceType = "oryn-model-source"

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
	path, id, content, err := sessionPreamble(dir, cwd, now)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return nil, err
	}
	return &sessionFile{path: path, id: id, ids: map[string]bool{}}, nil
}

// sessionPreamble 生成新会话文件的路径、会话 id 与开头两行（标题槽 + 会话头）。
func sessionPreamble(dir, cwd string, now time.Time) (string, string, []byte, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", "", nil, err
	}
	ts := isoTime(now)
	name := strings.NewReplacer(":", "-", ".", "-").Replace(ts) + "_" + id.String() + ".jsonl"
	slot, err := titleSlotLine(ts)
	if err != nil {
		return "", "", nil, err
	}
	header, err := json.Marshal(struct {
		Type      string `json:"type"`
		Version   int    `json:"version"`
		ID        string `json:"id"`
		Timestamp string `json:"timestamp"`
		Cwd       string `json:"cwd"`
	}{"session", sessionVersion, id.String(), ts, cwd})
	if err != nil {
		return "", "", nil, err
	}
	var buf bytes.Buffer
	buf.Write(slot)
	buf.Write(header)
	buf.WriteByte('\n')
	return filepath.Join(dir, name), id.String(), buf.Bytes(), nil
}

// copyBranch 实现 fork_from（oryn-core SessionManager.copyBranchToNewSession）：把从根到 entryID 的路径上的条目
// 逐字拷进同目录下的新会话文件（同 id、同时间戳；分支外条目不在路径上，不拷），整文件原子写入；返回新文件路径与会话 id。
// fakeomp 不写 label，无需携带标签。
func (s *sessionFile) copyBranch(cwd, entryID string, now time.Time) (string, string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return "", "", err
	}
	type node struct {
		parent string
		raw    []byte
	}
	byID := map[string]node{}
	for _, line := range bytes.Split(data, []byte("\n")) {
		var e struct {
			Type     string  `json:"type"`
			ID       string  `json:"id"`
			ParentID *string `json:"parentId"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type == "title" || e.Type == "session" || e.ID == "" {
			continue
		}
		n := node{raw: line}
		if e.ParentID != nil {
			n.parent = *e.ParentID
		}
		byID[e.ID] = n
	}
	var path [][]byte
	for id := entryID; id != ""; {
		n, ok := byID[id]
		if !ok {
			break
		}
		path = append(path, n.raw)
		delete(byID, id) // 防环
		id = n.parent
	}
	if len(path) == 0 {
		return "", "", fmt.Errorf("Entry %s not found", entryID)
	}
	dst, id, content, err := sessionPreamble(filepath.Dir(s.path), cwd, now)
	if err != nil {
		return "", "", err
	}
	for i := len(path) - 1; i >= 0; i-- {
		content = append(append(content, path[i]...), '\n')
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	return dst, id, nil
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
			Type       string `json:"type"`
			ID         string `json:"id"`
			CustomType string `json:"customType"`
			// ActiveLeafID 存在（含 null）= 分支外条目：叶子取它而不是该条目（与 omp 加载会话一致）。
			ActiveLeafID json.RawMessage `json:"activeLeafId"`
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
				if len(e.ActiveLeafID) > 0 {
					var leaf *string
					_ = json.Unmarshal(e.ActiveLeafID, &leaf)
					s.leaf = ""
					if leaf != nil {
						s.leaf = *leaf
					}
				}
			}
			s.note(e.Type, e.CustomType)
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

// note 记下条目类型对「会话是否全新」的影响。
func (s *sessionFile) note(typ, customType string) {
	switch {
	case typ == "message":
		s.hasMessage = true
	case typ == "custom" && customType == modelSourceType:
		s.hasSource = true
	}
}

// appendMessage 追加一条 message 条目并推进叶子，返回条目 id。
func (s *sessionFile) appendMessage(message map[string]any, now time.Time) (string, error) {
	return s.appendEntry("message", map[string]any{"message": message}, now)
}

// appendEntry 追加一条 typ 类型的条目（`type`、`id`、`parentId`、`timestamp` 在前，fields 按键名排序随后）并推进叶子，
// 返回条目 id。fields 不得含这四个键。
func (s *sessionFile) appendEntry(typ string, fields map[string]any, now time.Time) (string, error) {
	id, err := s.writeEntry(typ, s.leaf, fields, now)
	if err != nil {
		return "", err
	}
	s.leaf = id
	return id, nil
}

// appendOffBranch 追加一条写在活动分支之外的条目（omp appendModelUsage：父条目不是当前叶子时带 activeLeafId）：
// parentId 取 parent，`activeLeafId` 记当前叶子，叶子不动。返回条目 id。
func (s *sessionFile) appendOffBranch(typ, parent string, fields map[string]any, now time.Time) (string, error) {
	f := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		f[k] = v
	}
	f["activeLeafId"] = nilIfEmpty(s.leaf)
	return s.writeEntry(typ, parent, f, now)
}

// writeEntry 把一条条目写到文件末尾（不动叶子）。parent 为空串时 parentId 为 null。
func (s *sessionFile) writeEntry(typ, parent string, fields map[string]any, now time.Time) (string, error) {
	id := s.newEntryID()
	head, err := json.Marshal(struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		ParentID  any    `json:"parentId"`
		Timestamp string `json:"timestamp"`
	}{typ, id, nilIfEmpty(parent), isoTime(now)})
	if err != nil {
		return "", err
	}
	line := head[:len(head)-1]
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		kb, _ := json.Marshal(k)
		vb, err := json.Marshal(fields[k])
		if err != nil {
			return "", err
		}
		line = append(append(append(append(line, ','), kb...), ':'), vb...)
	}
	line = append(line, '}', '\n')
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return "", err
	}
	_, err = f.Write(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	customType, _ := fields["customType"].(string)
	s.note(typ, customType)
	return id, nil
}

// treeNode 对应 oryn-core session-entries.ts 的 SessionTreeNode（get_tree 应答的 tree 元素；fakeomp 不写 label）。
type treeNode struct {
	Entry    json.RawMessage `json:"entry"`
	Children []*treeNode     `json:"children"`
}

// tree 读会话文件，按 parentId 组装条目树（SessionManager.getTree）：父条目不存在的条目（断链）也作为根；
// 同级按文件顺序（即追加的时间顺序）排列。
func (s *sessionFile) tree() ([]*treeNode, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	type item struct {
		node   *treeNode
		parent string
	}
	var items []item
	byID := map[string]*treeNode{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 256<<20)
	for sc.Scan() {
		var e struct {
			Type     string  `json:"type"`
			ID       string  `json:"id"`
			ParentID *string `json:"parentId"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type == "title" || e.Type == "session" || e.ID == "" {
			continue
		}
		n := &treeNode{Entry: append(json.RawMessage(nil), sc.Bytes()...), Children: []*treeNode{}}
		it := item{node: n}
		if e.ParentID != nil {
			it.parent = *e.ParentID
		}
		items = append(items, it)
		byID[e.ID] = n
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	roots := []*treeNode{}
	for _, it := range items {
		if p := byID[it.parent]; it.parent != "" && p != nil {
			p.Children = append(p.Children, it.node)
		} else {
			roots = append(roots, it.node)
		}
	}
	return roots, nil
}
