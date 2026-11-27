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
	TypeGroup  = "group"
)

// Op 是对画布文档的一次修改：
//
//	{op: "add", object: {type: "canvas" | "page" | "note" | "group", …}, copyOf?}
//	{op: "set", id, key, value}
//	{op: "remove", id}
//	{op: "order", id, index}
//
// add 一个画框时 file 可以省略（取 page-<id>.html）；带 copyOf 表示新画框的 HTML 从那个画框复制，
// 由落版本的一方负责复制文件。方案组的 picked 与 pickedBy 用 set 改；pickedBy（以及 add 方案组时带的 pickedBy）
// 由服务端按提交方盖章，提交方给的值会被覆盖。
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
		if typ != TypeCanvas && typ != TypePage && typ != TypeNote && typ != TypeGroup {
			r.fail(at(where, "object.type"), "只能是 canvas、page、note、group 之一")
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
// 设计画布的方案组由 src/variants.json 经封存维护：不能 add、remove、order，set 只能改 picked 与 pickedBy。
// set 方案组的 picked 会把 pickedBy 置 null（选定者由服务端随后盖章）。
func Apply(d *Doc, ops []Op) error {
	return apply(d, ops, &reader{})
}

// ApplySeal 是封存（seal_design）用的 Apply：先把 d 变成设计画布（没有 design 时补上
// {engine: DesignEngine, start: null}，已有的 design 原样保留），再应用 ops。与 Apply 不同的是：
// add 画框可以带 source（省略 file 时取 FrameFile(source)），set 能改画框的 source 与 file；
// 方案组可以 add、set（name、variants、picked、pickedBy）、remove 与 order。
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
	w := d.clone()
	for i, op := range ops {
		if r.step(&w, op, fmt.Sprintf("ops[%d]", i)); r.err != nil {
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
	case len(w.Groups) > MaxGroups:
		r.fail("groups", "最多 %d 个方案组", MaxGroups)
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

// clone 复制各列表（元素浅拷贝；操作只整体替换元素，不改元素内部的切片）。
func (d *Doc) clone() Doc {
	return Doc{
		Design:   d.Design,
		Canvases: slices.Clone(d.Canvases),
		Pages:    slices.Clone(d.Pages),
		Notes:    slices.Clone(d.Notes),
		Groups:   slices.Clone(d.Groups),
	}
}

// step 应用一条操作，不做整体检查（数量、引用、大小）。
func (r *reader) step(d *Doc, op Op, where string) {
	switch op.Op {
	case OpAdd:
		r.add(d, op, where)
	case OpSet:
		r.set(d, op, where)
	case OpRemove, OpOrder:
		if t, _ := d.find(op.ID); t == TypeGroup && r.sealedGroups(d) {
			r.fail(at(where, "id"), "设计画布的方案组由 src/variants.json 维护，不能用操作删除或排序")
			return
		}
		if op.Op == OpRemove {
			d.remove(op.ID)
		} else {
			d.order(op.ID, op.Index)
		}
	default:
		r.fail(at(where, "op"), "只能是 add、set、remove、order 之一")
	}
}

// sealedGroups 报告方案组是否只能由封存维护：设计画布上、且不是封存在改文档。
func (r *reader) sealedGroups(d *Doc) bool { return d.Design != nil && !r.seal }

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
	if i := slices.IndexFunc(d.Groups, func(g Group) bool { return g.ID == id }); i >= 0 {
		return TypeGroup, i
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
	case TypeGroup:
		if r.sealedGroups(d) {
			r.fail(at(ow, "type"), "方案组由 src/variants.json 维护，设计画布上不能用操作添加")
			return
		}
		g := r.group(obj, ow, d.Pages, true)
		if r.err == nil && !used(g.ID) {
			d.Groups = append(d.Groups, g)
		}
	default:
		r.fail(at(ow, "type"), "只能是 canvas、page、note、group 之一")
	}
}

var (
	canvasSettable = []string{"name"}
	pageSettable   = []string{"name", "canvas", "file", "x", "y", "width", "height", "locked", "device"}
	// framePageSettable 是带 source 的画框能改的字段：file 跟着 source 走，只由封存改。
	framePageSettable = []string{"name", "canvas", "x", "y", "width", "height", "locked", "device"}
	sealPageSettable  = append(slices.Clone(pageSettable), "source")
	noteFixed         = []string{"id", "canvas", "kind", "by"}
	groupSettable     = []string{"name", "variants", "picked", "pickedBy"}
	// pickSettable 是设计画布上方案组能改的字段：其余由 src/variants.json 经封存维护。
	pickSettable = []string{"picked", "pickedBy"}
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
	case TypeGroup:
		if r.sealedGroups(d) {
			return pickSettable
		}
		return groupSettable
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
		if typ == TypeGroup && r.sealedGroups(d) {
			r.fail(at(where, "key"), "设计画布的方案组由 src/variants.json 维护，画布上只能改 %s（不能改 %q）", strings.Join(keys, "、"), op.Key)
			return
		}
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
			if op.Key == "canvas" {
				d.detach(op.ID, p.Canvas)
			}
		}
	case TypeNote:
		m := toMap(d.Notes[i])
		m[op.Key] = op.Value
		if n := r.note(m, where); r.err == nil {
			d.Notes[i] = n
		}
	case TypeGroup:
		r.setGroup(d, i, op, where)
	}
}

// setGroup 改方案组的一个字段：改 picked 时 pickedBy 置 null；改 variants 时要求 MinVariants–MaxVariants 个方案，
// picked 指向的方案还在就保留 picked 与 pickedBy，否则两者都置 null。
func (r *reader) setGroup(d *Doc, i int, op Op, where string) {
	old := d.Groups[i]
	m := toMap(old)
	m[op.Key] = op.Value
	switch op.Key {
	case "picked":
		m["pickedBy"] = nil
	case "variants":
		m["picked"], m["pickedBy"] = nil, nil
	}
	g := r.group(m, where, d.Pages, op.Key == "variants")
	if r.err != nil {
		return
	}
	if op.Key == "variants" && old.Picked != nil && slices.ContainsFunc(g.Variants, func(v Variant) bool { return v.ID == *old.Picked }) {
		g.Picked, g.PickedBy = old.Picked, old.PickedBy
	}
	d.Groups[i] = g
}

// detach 把其它画布页上吸附画框 id 的形状端点置空（画框换了画布页之后）。
func (d *Doc) detach(id, canvas string) {
	for j, n := range d.Notes {
		if n.Canvas == canvas {
			continue
		}
		if n.From != nil && *n.From == id {
			d.Notes[j].From = nil
		}
		if n.To != nil && *n.To == id {
			d.Notes[j].To = nil
		}
	}
}

var typeLabel = map[string]string{TypeCanvas: "画布页", TypePage: "画框", TypeNote: "标注", TypeGroup: "方案组"}

// remove 删除对象：删画布页连带删它的画框、标注与以它为 canvas 的方案组；删画框把吸附它的 from / to
// 与指向它的 design.start 置空，并把它从方案里移除（见 prune）。删方案组不动它的画框。
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
		d.Groups = slices.DeleteFunc(d.Groups, func(g Group) bool { return g.Canvas == id })
	case TypePage:
		d.Pages = slices.Delete(d.Pages, i, i+1)
		gone[id] = true
	case TypeNote:
		d.Notes = slices.Delete(d.Notes, i, i+1)
	case TypeGroup:
		d.Groups = slices.Delete(d.Groups, i, i+1)
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
	d.Groups = prune(d.Groups, gone)
}

// prune 把删掉的画框从方案里移除：方案变空就删掉方案，组里一个方案都不剩就删掉组；
// picked 指向的方案被删掉时 picked 与 pickedBy 置 null。
func prune(groups []Group, gone map[string]bool) []Group {
	var out []Group
	for _, g := range groups {
		var vs []Variant
		for _, v := range g.Variants {
			v.Pages = slices.DeleteFunc(slices.Clone(v.Pages), func(p string) bool { return gone[p] })
			if len(v.Pages) > 0 {
				vs = append(vs, v)
			}
		}
		if len(vs) == 0 {
			continue
		}
		g.Variants = vs
		if g.Picked != nil && !slices.ContainsFunc(vs, func(v Variant) bool { return v.ID == *g.Picked }) {
			g.Picked, g.PickedBy = nil, nil
		}
		out = append(out, g)
	}
	return out
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
	case TypeGroup:
		d.Groups = move(d.Groups, i, index)
	}
}

func move[T any](s []T, from, to int) []T {
	x := s[from]
	s = slices.Delete(s, from, from+1)
	return slices.Insert(s, min(to, len(s)), x)
}

// ExpandPicks 在 ops 里每条改方案组 picked 的 set 之后紧跟着插入搬移画框的操作（纯函数，不改 d 与 ops）：
//   - 选定后落选、但不在「备选方案」画布页（AlternatesID）上的画框，整体平移到该页；该页不存在就先 add（名字 AlternatesName）；
//   - 选中方案里位于备选方案页上的画框，整体平移回组的 canvas 页；选中方案里本来就不在备选方案页上的画框不动；
//   - 平移的落点：左边缘 = 目标页上留下的画框的最右边缘 + PageGap*4，上边缘 = 它们的最小 y；目标页上没有画框时为 (0, 0)。
//
// 插入的操作写的是目标值，所以对已展开的 ops 再展开结果不变（已有备选方案页时，ops 里再 add 它的操作被去掉）。
// 某条操作模拟应用时出错，后面的操作原样保留、不再展开，由 Apply 报错。服务端在 live 与 agent 路径应用之前都调用它；
// 客户端提交前调用逐条相同的 TS 实现（expandPicks），乐观状态与服务端回显一致。
func ExpandPicks(d Doc, ops []Op) []Op {
	w := d.clone()
	r := &reader{}
	out := make([]Op, 0, len(ops))
	for i, op := range ops {
		if r.err != nil {
			out = append(out, op)
			continue
		}
		if op.Op == OpAdd && op.ObjectType() == TypeCanvas && op.ObjectID() == AlternatesID {
			if t, _ := w.find(AlternatesID); t == TypeCanvas {
				continue
			}
		}
		out = append(out, op)
		where := fmt.Sprintf("ops[%d]", i)
		r.step(&w, op, where)
		if r.err != nil || op.Op != OpSet || op.Key != "picked" {
			continue
		}
		if t, gi := w.find(op.ID); t == TypeGroup {
			for _, m := range pickMoves(w, w.Groups[gi]) {
				out = append(out, m)
				r.step(&w, m, where)
			}
		}
	}
	return out
}

// pickMoves 返回组 g 按当前 picked 该有的搬移操作（见 ExpandPicks）。
func pickMoves(d Doc, g Group) []Op {
	if g.Picked == nil {
		return nil
	}
	var away, back []Page
	moving := map[string]bool{}
	for _, v := range g.Variants {
		for _, id := range v.Pages {
			p, ok := d.PageByID(id)
			switch {
			case !ok:
			case v.ID != *g.Picked && p.Canvas != AlternatesID:
				away = append(away, p)
				moving[id] = true
			case v.ID == *g.Picked && p.Canvas == AlternatesID && g.Canvas != AlternatesID:
				back = append(back, p)
				moving[id] = true
			}
		}
	}
	var ops []Op
	if len(away) > 0 {
		if t, _ := d.find(AlternatesID); t != TypeCanvas {
			ops = append(ops, Op{Op: OpAdd, Object: map[string]any{"type": TypeCanvas, "id": AlternatesID, "name": AlternatesName}})
		}
		ops = append(ops, shift(d.Pages, away, AlternatesID, moving)...)
	}
	return append(ops, shift(d.Pages, back, g.Canvas, moving)...)
}

// shift 把 block 整体平移到 canvas 页（落点见 ExpandPicks）；moving 里的画框不算目标页上留下的画框。
func shift(pages, block []Page, canvas string, moving map[string]bool) []Op {
	if len(block) == 0 {
		return nil
	}
	found := false
	var right, top float64
	for _, p := range pages {
		if p.Canvas != canvas || moving[p.ID] {
			continue
		}
		if !found || p.X+p.Width > right {
			right = p.X + p.Width
		}
		if !found || p.Y < top {
			top = p.Y
		}
		found = true
	}
	left := 0.0
	if found {
		left = right + PageGap*4
	}
	minX, minY := block[0].X, block[0].Y
	for _, p := range block[1:] {
		minX, minY = min(minX, p.X), min(minY, p.Y)
	}
	ops := make([]Op, 0, 3*len(block))
	for _, p := range block {
		ops = append(ops,
			Op{Op: OpSet, ID: p.ID, Key: "canvas", Value: canvas},
			Op{Op: OpSet, ID: p.ID, Key: "x", Value: round2(p.X + left - minX)},
			Op{Op: OpSet, ID: p.ID, Key: "y", Value: round2(p.Y + top - minY)},
		)
	}
	return ops
}
