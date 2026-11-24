package canvas

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// 操作种类。
const (
	OpAdd    = "add"
	OpSet    = "set"
	OpRemove = "remove"
	OpOrder  = "order"
)

// add 的对象类型。
const (
	TypeCanvas = "canvas"
	TypePage   = "page"
	TypeNote   = "note"
)

// Op 是对画布文档的一次修改：
//
//	{op: "add", object: {type: "canvas" | "page" | "note", …}, copyOf?}
//	{op: "set", id, key, value}
//	{op: "remove", id}
//	{op: "order", id, index}
//
// add 一个画框时 file 可以省略（取 page-<id>.html）；带 copyOf 表示新画框的 HTML 从那个画框复制，
// 由落版本的一方负责复制文件。
type Op struct {
	Op     string
	Object map[string]any // add：对象（含 type）
	CopyOf string         // add 画框：复制哪个画框的 HTML
	ID     string         // set / remove / order
	Key    string         // set
	Value  any            // set
	Index  int            // order
}

// ObjectType 返回 add 的对象类型。
func (o Op) ObjectType() string {
	t, _ := o.Object["type"].(string)
	return t
}

// ObjectID 返回 add 的对象 id。
func (o Op) ObjectID() string {
	id, _ := o.Object["id"].(string)
	return id
}

// MarshalJSON 写出与 ParseOps 接受的形状相同的 JSON。
func (o Op) MarshalJSON() ([]byte, error) {
	switch o.Op {
	case OpAdd:
		return json.Marshal(struct {
			Op     string         `json:"op"`
			Object map[string]any `json:"object"`
			CopyOf string         `json:"copyOf,omitempty"`
		}{o.Op, o.Object, o.CopyOf})
	case OpSet:
		return json.Marshal(struct {
			Op    string `json:"op"`
			ID    string `json:"id"`
			Key   string `json:"key"`
			Value any    `json:"value"`
		}{o.Op, o.ID, o.Key, o.Value})
	case OpRemove:
		return json.Marshal(struct {
			Op string `json:"op"`
			ID string `json:"id"`
		}{o.Op, o.ID})
	default:
		return json.Marshal(struct {
			Op    string `json:"op"`
			ID    string `json:"id"`
			Index int    `json:"index"`
		}{o.Op, o.ID, o.Index})
	}
}

// maxIndex 是 order 的 index 上限（与 JS 的 Number.MAX_SAFE_INTEGER 相同）。
const maxIndex = 1<<53 - 1

// ParseOps 解析一组操作的形状（对象本身的字段在 Apply 时对照文档校验）。
func ParseOps(raw json.RawMessage) ([]Op, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, &Error{Where: "ops", Msg: "不是合法的 JSON：" + err.Error()}
	}
	a, ok := v.([]any)
	if !ok {
		return nil, &Error{Where: "ops", Msg: "必须是数组"}
	}
	if len(a) > MaxOps {
		return nil, &Error{Where: "ops", Msg: fmt.Sprintf("一次最多 %d 条操作（现在 %d 条）", MaxOps, len(a))}
	}
	r := &reader{}
	out := make([]Op, 0, len(a))
	for i, x := range a {
		out = append(out, r.op(x, fmt.Sprintf("ops[%d]", i)))
		if r.err != nil {
			return nil, r.err
		}
	}
	return out, nil
}

func (r *reader) op(v any, where string) Op {
	m := r.object(v, where)
	if m == nil {
		return Op{}
	}
	kind, _ := m["op"].(string)
	o := Op{Op: kind}
	switch kind {
	case OpAdd:
		r.only(m, []string{"op", "object", "copyOf"}, where)
		o.Object = r.object(r.need(m, "object", where), at(where, "object"))
		if r.err != nil {
			return o
		}
		typ := o.ObjectType()
		if typ != TypeCanvas && typ != TypePage && typ != TypeNote {
			r.fail(at(where, "object.type"), "只能是 canvas、page、note 之一")
		}
		if c, ok := m["copyOf"]; ok {
			if typ != TypePage {
				r.fail(at(where, "copyOf"), "只有 add 画框（type: page）可以带 copyOf")
			}
			o.CopyOf = r.id(c, at(where, "copyOf"))
		}
	case OpSet:
		r.only(m, []string{"op", "id", "key", "value"}, where)
		o.ID = r.id(r.need(m, "id", where), at(where, "id"))
		key, ok := r.need(m, "key", where).(string)
		if !ok || key == "" {
			r.fail(at(where, "key"), "必须是字段名")
		}
		o.Key = key
		o.Value = r.need(m, "value", where)
	case OpRemove:
		r.only(m, []string{"op", "id"}, where)
		o.ID = r.id(r.need(m, "id", where), at(where, "id"))
	case OpOrder:
		r.only(m, []string{"op", "id", "index"}, where)
		o.ID = r.id(r.need(m, "id", where), at(where, "id"))
		n, ok := r.need(m, "index", where).(float64)
		if !ok || n < 0 || n > maxIndex || n != float64(int64(n)) {
			r.fail(at(where, "index"), "必须是不小于 0 的整数")
		}
		o.Index = int(n)
	default:
		r.fail(at(where, "op"), "只能是 add、set、remove、order 之一")
	}
	return o
}

// Apply 按顺序把操作应用到 d：同一属性后写的赢；set / remove / order 指向已不存在的对象时跳过
// （别的客户端可能先删了它）。任何一条让文档不合法就报错，d 保持不变。
// 画框的 source 只由封存维护：add 不能带 source，set 不能改 source，也不能改带 source 的画框的 file。
func Apply(d *Doc, ops []Op) error {
	return apply(d, ops, &reader{})
}

// ApplySeal 是封存（seal_design）用的 Apply：先把 d 变成设计画布（没有 design 时补上
// {engine: DesignEngine, start: null}，已有的 design 原样保留），再应用 ops。与 Apply 不同的是：
// add 画框可以带 source（省略 file 时取 FrameFile(source)），set 能改画框的 source 与 file。
// 其余规则与 Apply 相同，任何一条不合法就报错，d 保持不变。
func ApplySeal(d *Doc, ops []Op) error {
	w := *d
	if w.Design == nil {
		w.Design = &Design{Engine: DesignEngine}
	}
	if err := apply(&w, ops, &reader{seal: true}); err != nil {
		return err
	}
	*d = w
	return nil
}

func apply(d *Doc, ops []Op, r *reader) error {
	w := Doc{
		Design:   d.Design,
		Canvases: slices.Clone(d.Canvases),
		Pages:    slices.Clone(d.Pages),
		Notes:    slices.Clone(d.Notes),
	}
	for i, op := range ops {
		where := fmt.Sprintf("ops[%d]", i)
		switch op.Op {
		case OpAdd:
			r.add(&w, op, where)
		case OpSet:
			r.set(&w, op, where)
		case OpRemove:
			w.remove(op.ID)
		case OpOrder:
			w.order(op.ID, op.Index)
		default:
			r.fail(at(where, "op"), "只能是 add、set、remove、order 之一")
		}
		if r.err != nil {
			return r.err
		}
	}
	switch {
	case len(w.Canvases) == 0:
		r.fail("canvases", "至少要保留一个画布页")
	case len(w.Canvases) > MaxCanvases:
		r.fail("canvases", "最多 %d 个画布页", MaxCanvases)
	case len(w.Pages) > MaxPages:
		r.fail("pages", "最多 %d 个画框", MaxPages)
	case len(w.Notes) > MaxNotes:
		r.fail("notes", "最多 %d 个标注", MaxNotes)
	}
	if r.err == nil {
		r.links(w)
	}
	if r.err == nil && len(w.JSON()) > MaxJSONBytes {
		r.fail("", "canvas.json 会超过 %d MiB", MaxJSONBytes>>20)
	}
	if r.err != nil {
		return r.err
	}
	w.Format = w.format()
	*d = w
	return nil
}

// find 返回 id 对应对象的类型与下标（类型为空表示不存在）。
func (d *Doc) find(id string) (string, int) {
	if i := slices.IndexFunc(d.Canvases, func(c Canvas) bool { return c.ID == id }); i >= 0 {
		return TypeCanvas, i
	}
	if i := slices.IndexFunc(d.Pages, func(p Page) bool { return p.ID == id }); i >= 0 {
		return TypePage, i
	}
	if i := slices.IndexFunc(d.Notes, func(n Note) bool { return n.ID == id }); i >= 0 {
		return TypeNote, i
	}
	return "", -1
}

func (r *reader) add(d *Doc, op Op, where string) {
	typ := op.ObjectType()
	obj := make(map[string]any, len(op.Object)+1)
	for k, v := range op.Object {
		if k != "type" {
			obj[k] = v
		}
	}
	if _, has := obj["canvas"]; typ != TypeCanvas && !has && len(d.Canvases) == 1 {
		obj["canvas"] = d.Canvases[0].ID
	}
	if typ == TypePage {
		_, hasSource := obj["source"]
		if hasSource && !r.seal {
			r.fail(at(at(where, "object"), "source"), "画框的 source 只由封存维护，不能用操作添加")
			return
		}
		if _, has := obj["file"]; !has {
			if src, ok := obj["source"].(string); ok && hasSource {
				obj["file"] = FrameFile(src)
			} else if id, ok := obj["id"].(string); ok {
				obj["file"] = "page-" + id + ".html"
			}
		}
	}
	ow := at(where, "object")
	used := func(id string) bool {
		if t, _ := d.find(id); t != "" {
			r.fail(at(ow, "id"), "id %q 已被使用", id)
			return true
		}
		return false
	}
	switch typ {
	case TypeCanvas:
		c := r.canvas(obj, ow)
		if r.err == nil && !used(c.ID) {
			d.Canvases = append(d.Canvases, c)
		}
	case TypePage:
		lp := r.page(obj, ow)
		if r.err != nil || used(lp.ID) {
			return
		}
		if op.CopyOf != "" {
			if _, ok := d.PageByID(op.CopyOf); !ok {
				r.fail(at(where, "copyOf"), "没有 id 为 %q 的画框", op.CopyOf)
				return
			}
		}
		if !lp.placed {
			lp.X, lp.Y = nextSlot(d.Pages, lp.Canvas)
		}
		d.Pages = append(d.Pages, lp.Page)
	case TypeNote:
		n := r.note(obj, ow)
		if r.err == nil && !used(n.ID) {
			d.Notes = append(d.Notes, n)
		}
	default:
		r.fail(at(ow, "type"), "只能是 canvas、page、note 之一")
	}
}

var (
	canvasSettable = []string{"name"}
	pageSettable   = []string{"name", "file", "x", "y", "width", "height", "locked", "device"}
	// framePageSettable 是带 source 的画框能改的字段：file 跟着 source 走，只由封存改。
	framePageSettable = []string{"name", "x", "y", "width", "height", "locked", "device"}
	sealPageSettable  = append(slices.Clone(pageSettable), "source")
	noteFixed         = []string{"id", "canvas", "kind", "by"}
)

// settable 返回某类对象能用 set 改的字段。
func (r *reader) settable(d *Doc, typ string, i int) []string {
	switch typ {
	case TypeCanvas:
		return canvasSettable
	case TypePage:
		switch {
		case r.seal:
			return sealPageSettable
		case d.Pages[i].Source != "":
			return framePageSettable
		}
		return pageSettable
	}
	var out []string
	for _, k := range noteKeys[d.Notes[i].Kind] {
		if !slices.Contains(noteFixed, k) {
			out = append(out, k)
		}
	}
	return out
}

// toMap 把对象换成解码后的 JSON 形式，以便改一个字段后走同一套解析。
func toMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		panic(err)
	}
	return m
}

func (r *reader) set(d *Doc, op Op, where string) {
	typ, i := d.find(op.ID)
	if typ == "" {
		return
	}
	keys := r.settable(d, typ, i)
	if !slices.Contains(keys, op.Key) {
		r.fail(at(where, "key"), "%s %q 不能改 %q（能改的字段：%s）", typeLabel[typ], op.ID, op.Key, strings.Join(keys, "、"))
		return
	}
	switch typ {
	case TypeCanvas:
		m := toMap(d.Canvases[i])
		m[op.Key] = op.Value
		if c := r.canvas(m, where); r.err == nil {
			d.Canvases[i] = c
		}
	case TypePage:
		m := toMap(d.Pages[i])
		m[op.Key] = op.Value
		if p := r.page(m, where); r.err == nil {
			d.Pages[i] = p.Page
		}
	case TypeNote:
		m := toMap(d.Notes[i])
		m[op.Key] = op.Value
		if n := r.note(m, where); r.err == nil {
			d.Notes[i] = n
		}
	}
}

var typeLabel = map[string]string{TypeCanvas: "画布页", TypePage: "画框", TypeNote: "标注"}

// remove 删除对象：删画布页连带删它的画框与标注；删画框把吸附它的 from / to 与指向它的 design.start 置空。
func (d *Doc) remove(id string) {
	typ, i := d.find(id)
	gone := map[string]bool{}
	switch typ {
	case TypeCanvas:
		d.Canvases = slices.Delete(d.Canvases, i, i+1)
		d.Pages = slices.DeleteFunc(d.Pages, func(p Page) bool {
			if p.Canvas == id {
				gone[p.ID] = true
				return true
			}
			return false
		})
		d.Notes = slices.DeleteFunc(d.Notes, func(n Note) bool { return n.Canvas == id })
	case TypePage:
		d.Pages = slices.Delete(d.Pages, i, i+1)
		gone[id] = true
	case TypeNote:
		d.Notes = slices.Delete(d.Notes, i, i+1)
	}
	if len(gone) == 0 {
		return
	}
	if d.Design != nil && d.Design.Start != nil && gone[*d.Design.Start] {
		d.Design = &Design{Engine: d.Design.Engine} // 换新对象，不改调用方的文档
	}
	for j, n := range d.Notes {
		if n.From != nil && gone[*n.From] {
			d.Notes[j].From = nil
		}
		if n.To != nil && gone[*n.To] {
			d.Notes[j].To = nil
		}
	}
}

// order 把对象移到它所在列表的第 index 位（超出时放到最后）。
func (d *Doc) order(id string, index int) {
	typ, i := d.find(id)
	switch typ {
	case TypeCanvas:
		d.Canvases = move(d.Canvases, i, index)
	case TypePage:
		d.Pages = move(d.Pages, i, index)
	case TypeNote:
		d.Notes = move(d.Notes, i, index)
	}
}

func move[T any](s []T, from, to int) []T {
	x := s[from]
	s = slices.Delete(s, from, from+1)
	return slices.Insert(s, min(to, len(s)), x)
}
