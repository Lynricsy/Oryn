package artifacts

import (
	"context"
	"encoding/json"
)

// dbCap 是 db 能力（M14 步骤 7.1）：artifact 自己的 JSON 文档存储，不需要用户同意，但最新版本必须声明 db。
// 参数是位置参数（与页面里的调用一一对应）：
//   - get(collection, id) → DataDoc | null
//   - set(collection, id, value, {baseVersion}?) → {id, version}
//   - update(collection, id, patch, {baseVersion}?) → {id, version}（patch 的字段浅合并进已有的对象文档）
//   - delete(collection, id, {baseVersion}?) → {deleted}
//   - list(collection, {after, limit}?) → {docs, next}（next 为 null 表示没有更多）
//   - query(collection, {where, orderBy, limit}?) → DataDoc[]
//   - collections() → [{name, count, bytes}]
//   - watch(collections?)：流，事件 {type: "set", collection, id, value, version} 与 {type: "delete", collection, id}；
//     collections 是集合名、集合名数组或缺省（全部集合）。
//
// baseVersion：0 表示文档必须还不存在，否则必须等于文档当前的版本，不符回 conflict。
func (h *Host) dbCap() runtimeCap {
	return runtimeCap{
		declared: true,
		ops: map[string]runtimeOp{
			"get":         h.dbGet,
			"set":         func(ctx context.Context, rc *runtimeCall) (any, error) { return h.dbPut(ctx, rc, false) },
			"update":      func(ctx context.Context, rc *runtimeCall) (any, error) { return h.dbPut(ctx, rc, true) },
			"delete":      h.dbDelete,
			"list":        h.dbList,
			"query":       h.dbQuery,
			"collections": h.dbCollections,
		},
		streams: map[string]streamOp{"watch": h.dbWatch},
	}
}

// pageDataErr 把数据层的错误换成页面看的码：回收站里的 artifact 对页面来说就是不在了。
func pageDataErr(err error) error {
	if oe, ok := AsOpError(err); ok && oe.Code == CodeDeleted {
		return opErr(CodeNotFound, "artifact 已被删除。")
	}
	return err
}

// collDoc 取前两个位置参数 collection 与 id。
func collDoc(list []json.RawMessage) (coll, id string, err error) {
	if coll, err = stringArg(list, 0, "collection"); err != nil {
		return "", "", err
	}
	id, err = stringArg(list, 1, "id")
	return coll, id, err
}

type dbBase struct {
	BaseVersion *int64 `json:"baseVersion"`
}

func (h *Host) dbGet(ctx context.Context, rc *runtimeCall) (any, error) {
	list, err := posArgs(rc.args, 2, 2)
	if err != nil {
		return nil, err
	}
	coll, id, err := collDoc(list)
	if err != nil {
		return nil, err
	}
	d, err := h.s.GetData(ctx, rc.art.ID, coll, id)
	if err != nil || d == nil {
		return nil, err
	}
	return d, nil
}

func (h *Host) dbPut(ctx context.Context, rc *runtimeCall, merge bool) (any, error) {
	list, err := posArgs(rc.args, 3, 4)
	if err != nil {
		return nil, err
	}
	coll, id, err := collDoc(list)
	if err != nil {
		return nil, err
	}
	var o dbBase
	if err := optsArg(list, 3, &o); err != nil {
		return nil, err
	}
	d, err := h.s.PutData(ctx, rc.art.ID, DataPut{Collection: coll, ID: id, Value: list[2], Merge: merge, BaseVersion: o.BaseVersion})
	if err != nil {
		return nil, pageDataErr(err)
	}
	return map[string]any{"id": d.ID, "version": d.Version}, nil
}

func (h *Host) dbDelete(ctx context.Context, rc *runtimeCall) (any, error) {
	list, err := posArgs(rc.args, 2, 3)
	if err != nil {
		return nil, err
	}
	coll, id, err := collDoc(list)
	if err != nil {
		return nil, err
	}
	var o dbBase
	if err := optsArg(list, 2, &o); err != nil {
		return nil, err
	}
	deleted, err := h.s.DeleteData(ctx, rc.art.ID, coll, id, o.BaseVersion)
	if err != nil {
		return nil, pageDataErr(err)
	}
	return map[string]any{"deleted": deleted}, nil
}

func (h *Host) dbList(ctx context.Context, rc *runtimeCall) (any, error) {
	list, err := posArgs(rc.args, 1, 2)
	if err != nil {
		return nil, err
	}
	coll, err := stringArg(list, 0, "collection")
	if err != nil {
		return nil, err
	}
	var o struct {
		After string `json:"after"`
		Limit int    `json:"limit"`
	}
	if err := optsArg(list, 1, &o); err != nil {
		return nil, err
	}
	docs, next, err := h.s.ListData(ctx, rc.art.ID, coll, o.After, o.Limit)
	if err != nil {
		return nil, err
	}
	var nx any
	if next != "" {
		nx = next
	}
	return map[string]any{"docs": docs, "next": nx}, nil
}

func (h *Host) dbQuery(ctx context.Context, rc *runtimeCall) (any, error) {
	list, err := posArgs(rc.args, 1, 2)
	if err != nil {
		return nil, err
	}
	coll, err := stringArg(list, 0, "collection")
	if err != nil {
		return nil, err
	}
	var o struct {
		Where   []DataFilter `json:"where"`
		OrderBy *DataOrder   `json:"orderBy"`
		Limit   int          `json:"limit"`
	}
	if err := optsArg(list, 1, &o); err != nil {
		return nil, err
	}
	return h.s.QueryData(ctx, rc.art.ID, coll, o.Where, o.OrderBy, o.Limit)
}

func (h *Host) dbCollections(ctx context.Context, rc *runtimeCall) (any, error) {
	if _, err := posArgs(rc.args, 0, 0); err != nil {
		return nil, err
	}
	cs, _, err := h.s.DataCollections(ctx, rc.art.ID)
	return cs, err
}

// dbWatch 把数据变化逐条送出，直到 ctx 结束；监听跟不上时以 failed 结束（页面重新 watch 并重读）。
func (h *Host) dbWatch(ctx context.Context, rc *runtimeCall, emit func(event any) error) error {
	list, err := posArgs(rc.args, 0, 1)
	if err != nil {
		return err
	}
	var colls []string
	if raw := argAt(list, 0); raw != nil {
		var one string
		if json.Unmarshal(raw, &one) == nil {
			colls = []string{one}
		} else if json.Unmarshal(raw, &colls) != nil {
			return opErr(CodeInvalid, "watch 的参数是集合名、集合名数组，或者不给（监听全部集合）。")
		}
	}
	w, err := h.s.WatchData(rc.art.ID, colls)
	if err != nil {
		return err
	}
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.C:
			if !ok {
				return opErr(codeFailed, "数据变化太快，监听跟不上，已经结束。重新 watch 并重新读取数据。")
			}
			if err := emit(ev); err != nil {
				return err
			}
		}
	}
}
