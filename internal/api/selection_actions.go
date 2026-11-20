package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 与 schema/openapi.yaml 的 SelectionAction / SelectionActions 保持一致。
const (
	selectionActionsMax   = 12
	selectionActionName   = 24
	selectionActionPrompt = 2000
)

var selectionActionID = regexp.MustCompile(`^[a-z0-9]{8}$`)

// GetSelectionActions 返回自定义选区快捷动作；尚未初始化所有者或存的内容解不出时为空列表。
func (s *Server) GetSelectionActions(w http.ResponseWriter, r *http.Request) {
	raw, err := s.Store.Q.GetOwnerSelectionActions(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, r, err)
		return
	}
	// 起一个非 nil 的空切片：空列表要序列化成 [] 而不是 null。
	out := apigen.SelectionActions{Actions: make([]apigen.SelectionAction, 0)}
	if err == nil {
		var stored []apigen.SelectionAction
		if json.Unmarshal([]byte(raw), &stored) == nil && stored != nil {
			out.Actions = stored
		}
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutSelectionActions 整体替换自定义选区快捷动作；名称与提示词去首尾空白后保存，尚未初始化所有者 → 409。
func (s *Server) PutSelectionActions(w http.ResponseWriter, r *http.Request) {
	var req apigen.SelectionActions
	if !decodeJSON(w, r, &req) {
		return
	}
	actions, err := normalizeSelectionActions(req.Actions)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	body, err := json.Marshal(actions)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := s.Store.Q.SetOwnerSelectionActions(r.Context(), string(body))
	if !savedOwner(w, r, n, err) {
		return
	}
	WriteJSON(w, http.StatusOK, apigen.SelectionActions{Actions: actions})
}

// normalizeSelectionActions 校验并规整列表：条数上限、id 格式与唯一、名称与提示词去首尾空白后非空且不超长（按字符计）。
func normalizeSelectionActions(in []apigen.SelectionAction) ([]apigen.SelectionAction, error) {
	if len(in) > selectionActionsMax {
		return nil, errInvalid(fmt.Sprintf("actions 至多 %d 条", selectionActionsMax))
	}
	out := make([]apigen.SelectionAction, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for i, a := range in {
		if !selectionActionID.MatchString(a.ID) {
			return nil, errInvalid(fmt.Sprintf("actions[%d].id 须为 8 位小写字母或数字", i))
		}
		if _, dup := seen[a.ID]; dup {
			return nil, errInvalid(fmt.Sprintf("actions[%d].id 重复", i))
		}
		seen[a.ID] = struct{}{}
		name := strings.TrimSpace(a.Name)
		if name == "" || utf8.RuneCountInString(name) > selectionActionName {
			return nil, errInvalid(fmt.Sprintf("actions[%d].name 须为 1-%d 个字符", i, selectionActionName))
		}
		prompt := strings.TrimSpace(a.Prompt)
		if prompt == "" || utf8.RuneCountInString(prompt) > selectionActionPrompt {
			return nil, errInvalid(fmt.Sprintf("actions[%d].prompt 须为 1-%d 个字符", i, selectionActionPrompt))
		}
		out = append(out, apigen.SelectionAction{ID: a.ID, Name: name, Prompt: prompt})
	}
	return out, nil
}
